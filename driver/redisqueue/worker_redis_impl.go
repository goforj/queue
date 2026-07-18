package redisqueue

import (
	"context"
	"errors"
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

	mu      sync.Mutex
	started bool
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
		if w.ctxDecorator != nil {
			if decorated := w.ctxDecorator(ctx); decorated != nil {
				ctx = decorated
			}
		}
		attempt, _ := backend.GetRetryCount(ctx)
		maxRetry, _ := backend.GetMaxRetry(ctx)
		physicalAttempt := busruntime.DeliveryAttempt{Number: attempt, MaxRetry: maxRetry}
		queueName, _ := backend.GetQueueName(ctx)
		queueName = queuecore.NormalizeQueueName(queueName)
		delivery := queuecore.DriverWithAttempt(
			queue.NewJob(job.Type()).
				Payload(job.Payload()).
				OnQueue(queueName).
				Retry(maxRetry),
			attempt,
		)
		if w.obs == nil {
			return redisSettlementError(physicalAttempt, handler(ctx, delivery))
		}
		metadata := queue.ResolveObservedJobMetadata(job.Type(), job.Payload())

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
			Attempt:    attempt,
			MaxRetry:   maxRetry,
			Time:       start,
		}
		observeRedisAttemptStart(ctx, w.obs, base)

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
	})
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

// redisSettlementError translates terminal application intent into Asynq's settlement control without hiding the handler error.
func redisSettlementError(attempt busruntime.DeliveryAttempt, err error) error {
	if busruntime.ClassifyAttempt(attempt, err) != busruntime.AttemptFailed || !busruntime.IsPermanent(err) {
		return err
	}
	if errors.Is(err, backend.SkipRetry) {
		return err
	}
	return errors.Join(err, backend.SkipRetry)
}

func (w *redisWorker) StartWorkers(ctx context.Context) error {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.started {
		return nil
	}
	if err := w.server.Start(w.mux); err != nil {
		return err
	}
	w.started = true
	return nil
}

func (w *redisWorker) Shutdown(ctx context.Context) error {
	w.mu.Lock()
	started := w.started
	w.started = false
	w.mu.Unlock()

	if !started {
		return nil
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.server.Shutdown()
	}()

	if ctx == nil {
		<-done
		return nil
	}

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
