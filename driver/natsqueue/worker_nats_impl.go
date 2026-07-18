package natsqueue

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"

	"github.com/goforj/queue"
	"github.com/goforj/queue/busruntime"
	"github.com/goforj/queue/queuecore"
	"github.com/nats-io/nats.go"
)

type natsWorker struct {
	url          string
	defaultQueue string
	workers      int

	mu       sync.RWMutex
	handlers map[string]queue.Handler

	startStop sync.Mutex
	started   bool
	stopDone  chan struct{}
	stopErr   error
	connect   natsWorkerConnector

	conn natsConnection
	sub  natsWorkerSubscription
	sem  chan struct{}

	running  sync.WaitGroup
	delayed  sync.WaitGroup
	observer queue.Observer
}

type natsWorkerSubscription interface {
	Drain() error
}

type synchronousNATSSubscription struct {
	*nats.Subscription
}

// Drain waits until Core NATS has stopped intake and completed every queued callback.
func (s *synchronousNATSSubscription) Drain() error {
	if s == nil || s.Subscription == nil || !s.IsValid() {
		return nil
	}
	closed := s.StatusChanged(nats.SubscriptionClosed)
	if err := s.Subscription.Drain(); err != nil {
		return err
	}
	for status := range closed {
		if status == nats.SubscriptionClosed {
			return nil
		}
	}
	return nil
}

type natsWorkerConnector func(url, subject string, callback nats.MsgHandler) (natsConnection, natsWorkerSubscription, error)

type natsWorkerConfig struct {
	URL          string
	DefaultQueue string
	Workers      int
	Observer     queue.Observer
}

func newNATSWorker(url string) *natsWorker {
	return newNATSWorkerWithConfig(natsWorkerConfig{URL: url})
}

func newNATSWorkerWithConfig(cfg natsWorkerConfig) *natsWorker {
	if cfg.DefaultQueue == "" {
		cfg.DefaultQueue = "default"
	}
	cfg.Workers = defaultWorkerCount(cfg.Workers)
	return &natsWorker{
		url:          cfg.URL,
		defaultQueue: cfg.DefaultQueue,
		workers:      cfg.Workers,
		handlers:     make(map[string]queue.Handler),
		observer:     cfg.Observer,
	}
}

func (w *natsWorker) Register(jobType string, handler queue.Handler) {
	if jobType == "" || handler == nil {
		return
	}
	w.mu.Lock()
	w.handlers[jobType] = handler
	w.mu.Unlock()
}

func (w *natsWorker) StartWorkers(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	w.startStop.Lock()
	defer w.startStop.Unlock()
	if w.stopDone != nil {
		return queue.ErrQueuerShuttingDown
	}
	if w.started {
		return nil
	}
	connect := w.connect
	if connect == nil {
		connect = connectNATSWorker
	}
	w.sem = make(chan struct{}, w.workers)
	ready := make(chan struct{})
	var acceptCallbacks atomic.Bool
	nc, sub, err := connect(w.url, natsSubject(w.defaultQueue), func(message *nats.Msg) {
		<-ready
		if !acceptCallbacks.Load() {
			return
		}
		w.running.Add(1)
		w.sem <- struct{}{}
		go func() {
			defer func() {
				<-w.sem
				w.running.Done()
			}()
			w.processMessage(message)
		}()
	})
	if err != nil {
		return err
	}
	w.conn = nc
	w.sub = sub
	flushCtx, cancel := natsPublishContext(ctx)
	flushErr := nc.FlushWithContext(flushCtx)
	cancel()
	if flushErr != nil {
		close(ready)
		_ = sub.Drain()
		nc.Close()
		w.conn = nil
		w.sub = nil
		return flushErr
	}
	acceptCallbacks.Store(true)
	w.started = true
	close(ready)
	return nil
}

// Shutdown stops intake before waiting for handlers and delayed republishes, then closes their shared connection.
func (w *natsWorker) Shutdown(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	w.startStop.Lock()
	if !w.started && w.stopDone == nil {
		w.startStop.Unlock()
		return nil
	}
	if w.stopDone == nil {
		w.stopDone = make(chan struct{})
		done := w.stopDone
		sub := w.sub
		conn := w.conn
		go func() {
			var stopErr error
			if sub != nil {
				stopErr = sub.Drain()
			}
			w.running.Wait()
			w.delayed.Wait()
			if conn != nil {
				if drainErr := conn.Drain(); stopErr == nil {
					stopErr = drainErr
				}
				conn.Close()
			}
			w.startStop.Lock()
			w.started = false
			w.conn = nil
			w.sub = nil
			w.stopErr = stopErr
			w.startStop.Unlock()
			close(done)
		}()
	}
	done := w.stopDone
	w.startStop.Unlock()
	select {
	case <-done:
		w.startStop.Lock()
		err := w.stopErr
		// Drain diagnostics describe a cleanup that has already completed; report them once so a later root shutdown can finish producer cleanup.
		w.stopErr = nil
		w.startStop.Unlock()
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *natsWorker) processMessage(message *nats.Msg) {
	var incoming natsMessage
	if err := json.Unmarshal(message.Data, &incoming); err != nil {
		return
	}
	if incoming.AvailableAtMS > 0 {
		remaining := time.Until(time.UnixMilli(incoming.AvailableAtMS))
		if remaining > 0 {
			w.delayed.Add(1)
			time.AfterFunc(remaining, func() {
				defer w.delayed.Done()
				if err := w.republish(incoming); err != nil {
					w.observeRepublishFailure(context.Background(), incoming, err)
				}
			})
			return
		}
	}

	w.mu.RLock()
	handler, ok := w.handlers[incoming.Type]
	w.mu.RUnlock()
	if !ok {
		return
	}

	attempt := busruntime.DeliveryAttempt{Number: incoming.Attempt, MaxRetry: incoming.MaxRetry}
	ctx := busruntime.WithDeliveryAttempt(context.Background(), attempt)
	if incoming.TimeoutMillis > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(incoming.TimeoutMillis)*time.Millisecond)
		defer cancel()
	}
	err := handler(
		ctx,
		natsDeliveryJob(incoming),
	)
	switch busruntime.ClassifyAttempt(attempt, err) {
	case busruntime.AttemptSucceeded, busruntime.AttemptFailed:
		return
	case busruntime.AttemptRedeliver:
		// Core NATS has no broker-managed negative acknowledgement, so uncommitted work must be republished explicitly.
		incoming.AvailableAtMS = 0
		if err := w.republish(incoming); err != nil {
			w.observeRepublishFailure(ctx, incoming, err)
		}
		return
	case busruntime.AttemptRetry:
	}
	incoming.Attempt++
	if incoming.BackoffMillis > 0 {
		incoming.AvailableAtMS = time.Now().Add(time.Duration(incoming.BackoffMillis) * time.Millisecond).UnixMilli()
	} else {
		incoming.AvailableAtMS = 0
	}
	if err := w.republish(incoming); err != nil {
		w.observeRepublishFailure(ctx, incoming, err)
	}
}

// connectNATSWorker creates the Core NATS subscription owned by one worker lifecycle.
func connectNATSWorker(url, subject string, callback nats.MsgHandler) (natsConnection, natsWorkerSubscription, error) {
	nc, err := nats.Connect(url)
	if err != nil {
		return nil, nil, err
	}
	sub, err := nc.Subscribe(subject, callback)
	if err != nil {
		nc.Close()
		return nil, nil, err
	}
	return &synchronousNATSConnection{Conn: nc}, &synchronousNATSSubscription{Subscription: sub}, nil
}

func (w *natsWorker) republish(message natsMessage) error {
	if w.conn == nil {
		return nats.ErrConnectionClosed
	}
	payload, err := json.Marshal(message)
	if err != nil {
		return err
	}
	if err := w.conn.Publish(natsSubject(message.Queue), payload); err != nil {
		return err
	}
	ctx, cancel := natsPublishContext(context.Background())
	defer cancel()
	return w.conn.FlushWithContext(ctx)
}

func (w *natsWorker) observeRepublishFailure(ctx context.Context, message natsMessage, err error) {
	metadata := queue.ResolveObservedJobMetadataFromJob(natsDeliveryJob(message))
	queuecore.SafeObserve(ctx, w.observer, queue.Event{
		Kind:       queue.EventRepublishFailed,
		Driver:     queue.DriverNATS,
		Queue:      queuecore.NormalizeQueueName(message.Queue),
		JobType:    metadata.JobType,
		JobKey:     metadata.JobKey,
		DispatchID: metadata.DispatchID,
		JobID:      metadata.JobID,
		ChainID:    metadata.ChainID,
		BatchID:    metadata.BatchID,
		Attempt:    message.Attempt,
		MaxRetry:   message.MaxRetry,
		Err:        err,
		Time:       time.Now(),
	})
}

// natsDeliveryJob reconstructs one NATS delivery without coupling application
// payload bytes to the optional direct-delivery metadata channel.
func natsDeliveryJob(message natsMessage) queue.Job {
	job := queuecore.DriverWithAttempt(
		queue.NewJob(message.Type).
			Payload(message.Payload).
			OnQueue(message.Queue).
			Retry(message.MaxRetry),
		message.Attempt,
	)
	if len(message.Metadata) > 0 {
		var metadata queue.DriverJobMetadata
		if err := json.Unmarshal(message.Metadata, &metadata); err == nil {
			job = queue.DriverWithMetadata(job, metadata)
		}
	}
	return job
}

func defaultWorkerCount(n int) int {
	if n <= 0 {
		return 1
	}
	return n
}
