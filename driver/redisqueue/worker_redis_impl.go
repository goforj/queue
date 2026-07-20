package redisqueue

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/goforj/queue"
	"github.com/goforj/queue/busruntime"
	"github.com/goforj/queue/queuecore"
	backend "github.com/hibiken/asynq"
)

type server interface {
	Start(handler backend.Handler) error
	Shutdown()
	Stop()
}

type redisWorker struct {
	server       server
	mux          *backend.ServeMux
	obs          queue.Observer
	ctxDecorator func(context.Context) context.Context

	mu       sync.Mutex
	started  bool
	draining bool
	stopDone chan struct{}
}

type redisTransportDelivery struct {
	attempt  int
	maxRetry int
	queue    string
}

func newRedisWorker(server server, mux *backend.ServeMux, observer queue.Observer) *redisWorker {
	return &redisWorker{server: server, mux: mux, obs: observer}
}

func (w *redisWorker) SetHandlerContextDecorator(fn func(context.Context) context.Context) {
	w.ctxDecorator = fn
}

func (w *redisWorker) Register(jobType string, handler queue.Handler) {
	if jobType == "" || handler == nil {
		return
	}
	w.mux.HandleFunc(jobType, func(ctx context.Context, job *backend.Task) error {
		return w.processTask(ctx, job, handler, redisTransportDeliveryFromContext(ctx))
	})
}

// redisTransportDeliveryFromContext captures Asynq-owned values before an
// application decorator can replace the transport context.
func redisTransportDeliveryFromContext(ctx context.Context) redisTransportDelivery {
	attempt, _ := backend.GetRetryCount(ctx)
	maxRetry, _ := backend.GetMaxRetry(ctx)
	queueName, _ := backend.GetQueueName(ctx)
	return redisTransportDelivery{attempt: attempt, maxRetry: maxRetry, queue: queueName}
}

// processTask decorates application context while deriving delivery and event
// facts exclusively from the transport snapshot captured by Register.
func (w *redisWorker) processTask(ctx context.Context, job *backend.Task, handler queue.Handler, transport redisTransportDelivery) error {
	if w.ctxDecorator != nil {
		if decorated := w.ctxDecorator(ctx); decorated != nil {
			ctx = busruntime.PreserveDeliveryContext(ctx, decorated)
		}
	}
	maxRetry := redisApplicationMaxRetry(job, transport.maxRetry)
	physicalAttempt := busruntime.DeliveryAttempt{Number: transport.attempt, MaxRetry: maxRetry}
	queueName := queuecore.NormalizeQueueName(transport.queue)
	delivery := queuecore.DriverWithAttempt(
		queue.NewJob(job.Type()).
			Payload(job.Payload()).
			OnQueue(queueName).
			Retry(maxRetry),
		transport.attempt,
	)
	delivery = redisJobWithDriverMetadata(delivery, job.Headers())
	if w.obs == nil {
		return redisSettlementError(physicalAttempt, handler(ctx, delivery))
	}
	metadata := queue.ResolveObservedJobMetadataFromJob(delivery)

	start := time.Now()
	base := queue.Event{
		Driver:     queue.DriverRedis,
		Queue:      queueName,
		JobType:    metadata.JobType,
		JobKey:     metadata.JobKey,
		DispatchID: metadata.DispatchID,
		JobID:      metadata.JobID,
		ChainID:    metadata.ChainID,
		BatchID:    metadata.BatchID,
		Attempt:    transport.attempt,
		MaxRetry:   maxRetry,
		Time:       start,
	}
	observeRedisAttemptStart(ctx, w.obs, base)
	defer func() {
		if recovered := recover(); recovered != nil {
			finish := base
			finish.Kind = queue.EventProcessFailed
			finish.Time = time.Now()
			finish.Duration = time.Since(start)
			finish.Err = redisHandlerPanicError(recovered)
			queuecore.SafeObserve(ctx, w.obs, finish)
			panic(recovered)
		}
	}()

	err := handler(ctx, delivery)
	finish := base
	finish.Time = time.Now()
	finish.Duration = time.Since(start)
	finish.Err = err
	if err == nil {
		finish.Kind = queue.EventProcessSucceeded
		queuecore.SafeObserve(ctx, w.obs, finish)
		return nil
	}
	finish.Kind = queue.EventProcessFailed
	queuecore.SafeObserve(ctx, w.obs, finish)
	return redisSettlementError(physicalAttempt, err)
}

// redisHandlerPanicError preserves error identity for telemetry while the
// worker re-panics so Asynq retains ownership of panic recovery and retry.
func redisHandlerPanicError(recovered any) error {
	if err, ok := recovered.(error); ok {
		return fmt.Errorf("redis handler panicked: %w", err)
	}
	return fmt.Errorf("redis handler panicked: %v", recovered)
}

// observeRedisAttemptStart treats an Asynq retry delivery as evidence that its application retry was scheduled; infrastructure redelivery may repeat the fact.
func observeRedisAttemptStart(ctx context.Context, observer queue.Observer, event queue.Event) {
	if event.Attempt > 0 {
		retry := event
		retry.Kind = queue.EventProcessRetried
		queuecore.SafeObserve(ctx, observer, retry)
	}
	event.Kind = queue.EventProcessStarted
	queuecore.SafeObserve(ctx, observer, event)
}

// redisSettlementError explicitly archives terminal application outcomes before the reserved Asynq slot can become an extra application retry.
func redisSettlementError(attempt busruntime.DeliveryAttempt, err error) error {
	if busruntime.ClassifyAttempt(attempt, err) != busruntime.AttemptFailed {
		return err
	}
	if errors.Is(err, backend.SkipRetry) {
		return err
	}
	return errors.Join(err, backend.SkipRetry)
}

// StartWorkers rejects restart while an earlier server instance is still draining.
func (w *redisWorker) StartWorkers(ctx context.Context) error {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.draining {
		return queue.ErrQueuerShuttingDown
	}
	if w.started {
		return nil
	}
	if err := w.server.Start(w.mux); err != nil {
		return err
	}
	w.started = true
	return nil
}

// Shutdown retains the server drain until completion so a caller can retry after its context expires.
func (w *redisWorker) Shutdown(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	w.mu.Lock()
	if !w.started && !w.draining {
		w.mu.Unlock()
		return nil
	}
	if !w.draining {
		w.draining = true
		w.stopDone = make(chan struct{})
		done := w.stopDone
		go func() {
			w.server.Shutdown()
			w.mu.Lock()
			w.started = false
			w.draining = false
			w.stopDone = nil
			w.mu.Unlock()
			close(done)
		}()
	}
	done := w.stopDone
	w.mu.Unlock()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
