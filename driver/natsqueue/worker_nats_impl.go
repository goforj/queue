package natsqueue

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/goforj/queue"
	"github.com/goforj/queue/queuecore"
	"github.com/nats-io/nats.go"
)

type natsWorker struct {
	url     string
	workers int

	mu       sync.RWMutex
	handlers map[string]queue.Handler

	conn     *nats.Conn
	sub      *nats.Subscription
	start    sync.Once
	sem      chan struct{}
	running  sync.WaitGroup
	observer queue.Observer
}

type natsWorkerConfig struct {
	URL      string
	Workers  int
	Observer queue.Observer
}

func newNATSWorker(url string) *natsWorker {
	return newNATSWorkerWithConfig(natsWorkerConfig{URL: url})
}

func newNATSWorkerWithConfig(cfg natsWorkerConfig) *natsWorker {
	cfg.Workers = defaultWorkerCount(cfg.Workers)
	return &natsWorker{
		url:      cfg.URL,
		workers:  cfg.Workers,
		handlers: make(map[string]queue.Handler),
		observer: cfg.Observer,
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
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	var startErr error
	w.start.Do(func() {
		nc, err := nats.Connect(w.url)
		if err != nil {
			startErr = err
			return
		}
		w.sem = make(chan struct{}, w.workers)
		sub, err := nc.Subscribe("queue.*", func(message *nats.Msg) {
			w.sem <- struct{}{}
			w.running.Add(1)
			go func() {
				defer func() {
					<-w.sem
					w.running.Done()
				}()
				w.processMessage(message)
			}()
		})
		if err != nil {
			nc.Close()
			startErr = err
			return
		}
		w.conn = nc
		w.sub = sub
	})
	return startErr
}

func (w *natsWorker) Shutdown(_ context.Context) error {
	if w.sub != nil {
		_ = w.sub.Drain()
	}
	if w.conn != nil {
		_ = w.conn.Drain()
		w.conn.Close()
	}
	w.running.Wait()
	return nil
}

func (w *natsWorker) processMessage(message *nats.Msg) {
	var incoming natsMessage
	if err := json.Unmarshal(message.Data, &incoming); err != nil {
		return
	}
	if incoming.AvailableAtMS > 0 {
		remaining := time.Until(time.UnixMilli(incoming.AvailableAtMS))
		if remaining > 0 {
			time.AfterFunc(remaining, func() {
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

	ctx := context.Background()
	if incoming.TimeoutMillis > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(incoming.TimeoutMillis)*time.Millisecond)
		defer cancel()
	}
	err := handler(
		ctx,
		queuecore.DriverWithAttempt(
			queue.NewJob(incoming.Type).
				Payload(incoming.Payload).
				OnQueue(incoming.Queue).
				Retry(incoming.MaxRetry),
			incoming.Attempt,
		),
	)
	if err == nil {
		return
	}
	if incoming.Attempt >= incoming.MaxRetry {
		return
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

func (w *natsWorker) republish(message natsMessage) error {
	if w.conn == nil {
		return nats.ErrConnectionClosed
	}
	payload, err := json.Marshal(message)
	if err != nil {
		return err
	}
	return w.conn.Publish(natsSubject(message.Queue), payload)
}

func (w *natsWorker) observeRepublishFailure(ctx context.Context, message natsMessage, err error) {
	queuecore.SafeObserve(ctx, w.observer, queue.Event{
		Kind:     queue.EventRepublishFailed,
		Driver:   queue.DriverNATS,
		Queue:    queuecore.NormalizeQueueName(message.Queue),
		JobType:  queue.ResolveObservedJobType(message.Type, message.Payload),
		Attempt:  message.Attempt,
		MaxRetry: message.MaxRetry,
		Err:      err,
		Time:     time.Now(),
	})
}

func defaultWorkerCount(n int) int {
	if n <= 0 {
		return 1
	}
	return n
}
