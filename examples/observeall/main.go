//go:build ignore
// +build ignore

// examplegen:manual

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync/atomic"
	"time"

	"github.com/goforj/queue"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	var flakyAttempts atomic.Int32
	ctx := context.Background()

	runtimeObserver := queue.ObserverFunc(func(event queue.Event) {
		logger.Info("runtime event",
			"kind", event.Kind,
			"driver", event.Driver,
			"queue", event.Queue,
			"job_type", event.JobType,
			"attempt", event.Attempt,
			"max_retry", event.MaxRetry,
			"duration", event.Duration,
			"err", event.Err,
		)
	})

	workflowObserver := queue.WorkflowObserverFunc(func(event queue.WorkflowEvent) {
		logger.Info("workflow event",
			"kind", event.Kind,
			"dispatch_id", event.DispatchID,
			"job_id", event.JobID,
			"chain_id", event.ChainID,
			"batch_id", event.BatchID,
			"job_type", event.JobType,
			"queue", event.Queue,
			"attempt", event.Attempt,
			"duration", event.Duration,
			"err", event.Err,
		)
	})

	q, err := queue.New(
		queue.Config{
			Driver:   queue.DriverWorkerpool,
			Observer: runtimeObserver,
		},
		queue.WithObserver(workflowObserver),
	)
	if err != nil {
		panic(err)
	}

	q.Register("emails:send", func(ctx context.Context, m queue.Message) error {
		var payload struct {
			To string `json:"to"`
		}
		if err := m.Bind(&payload); err != nil {
			return err
		}
		fmt.Println("sending", payload.To)
		return nil
	})
	q.Register("emails:flaky", func(ctx context.Context, m queue.Message) error {
		if flakyAttempts.Add(1) == 1 {
			return errors.New("transient smtp error")
		}
		return nil
	})
	q.Register("emails:fail", func(ctx context.Context, m queue.Message) error {
		return errors.New("terminal failure")
	})

	_ = q.StartWorkers(ctx)
	defer q.Shutdown(ctx)

	_, _ = q.DispatchCtx(
		ctx,
		queue.NewJob("emails:send").
			Payload(map[string]any{"to": "user@example.com"}).
			OnQueue("default").
			Timeout(2*time.Second),
	)

	dup := queue.NewJob("emails:send").
		Payload(map[string]any{"to": "dupe@example.com"}).
		OnQueue("default").
		UniqueFor(5 * time.Second)
	_, _ = q.DispatchCtx(ctx, dup)
	_, _ = q.DispatchCtx(ctx, dup)

	cancelCtx, cancel := context.WithCancel(ctx)
	cancel()
	_, _ = q.DispatchCtx(cancelCtx, queue.NewJob("emails:send").OnQueue("default"))

	_, _ = q.DispatchCtx(ctx, queue.NewJob("emails:flaky").OnQueue("default").Retry(1))
	_, _ = q.DispatchCtx(ctx, queue.NewJob("emails:fail").OnQueue("default").Retry(0))

	_, _ = q.Chain(
		queue.NewJob("emails:send").Payload(map[string]any{"to": "chain1@example.com"}).OnQueue("default"),
		queue.NewJob("emails:send").Payload(map[string]any{"to": "chain2@example.com"}).OnQueue("default"),
	).Finally(func(ctx context.Context, st queue.ChainState) error {
		return nil
	}).Dispatch(ctx)

	_, _ = q.Batch(
		queue.NewJob("emails:send").Payload(map[string]any{"to": "batch1@example.com"}).OnQueue("default"),
		queue.NewJob("emails:send").Payload(map[string]any{"to": "batch2@example.com"}).OnQueue("default"),
	).Progress(func(ctx context.Context, st queue.BatchState) error {
		return nil
	}).Finally(func(ctx context.Context, st queue.BatchState) error {
		return nil
	}).Dispatch(ctx)

	time.Sleep(500 * time.Millisecond)
}
