package queue

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

func TestNATSWorker_NewRegisterAndShutdown(t *testing.T) {
	w := newNATSWorker("nats://example:4222").(*natsWorker)
	if w.url != "nats://example:4222" {
		t.Fatalf("expected url to be preserved, got %q", w.url)
	}

	w.Register("", func(context.Context, Task) error { return nil })
	w.Register("job:nil", nil)
	if len(w.handlers) != 0 {
		t.Fatalf("expected ignored registrations, got %d handlers", len(w.handlers))
	}

	w.Register("job:ok", func(context.Context, Task) error { return nil })
	if len(w.handlers) != 1 {
		t.Fatalf("expected one handler, got %d", len(w.handlers))
	}

	if err := w.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown should be no-op with nil conn/sub: %v", err)
	}
}

func TestNATSWorker_StartWorkersCanceledContext(t *testing.T) {
	w := newNATSWorker("nats://example:4222").(*natsWorker)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := w.StartWorkers(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled, got %v", err)
	}
}

func TestNATSWorker_ProcessMessageBranches(t *testing.T) {
	t.Run("invalid json ignored", func(t *testing.T) {
		w := newNATSWorker("nats://example:4222").(*natsWorker)
		w.processMessage(&nats.Msg{Data: []byte("{")})
	})

	t.Run("missing handler ignored", func(t *testing.T) {
		w := newNATSWorker("nats://example:4222").(*natsWorker)
		body, err := json.Marshal(natsMessage{Type: "job:none", Queue: "default"})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		w.processMessage(&nats.Msg{Data: body})
	})

	t.Run("success uses timeout and task options", func(t *testing.T) {
		called := 0
		w := newNATSWorker("nats://example:4222").(*natsWorker)
		w.Register("job:ok", func(ctx context.Context, task Task) error {
			called++
			if _, ok := ctx.Deadline(); !ok {
				t.Fatal("expected timeout context")
			}
			opts := task.enqueueOptions()
			if task.Type != "job:ok" || opts.queueName != "critical" || opts.attempt != 2 {
				t.Fatalf("unexpected task fields: type=%q queue=%q attempt=%d", task.Type, opts.queueName, opts.attempt)
			}
			if opts.maxRetry == nil || *opts.maxRetry != 3 {
				t.Fatalf("expected retry=3, got %+v", opts.maxRetry)
			}
			return nil
		})
		body, err := json.Marshal(natsMessage{Type: "job:ok", Queue: "critical", Attempt: 2, MaxRetry: 3, TimeoutMillis: 20})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		w.processMessage(&nats.Msg{Data: body})
		if called != 1 {
			t.Fatalf("expected handler once, got %d", called)
		}
	})

	t.Run("future message schedules republish without panic", func(t *testing.T) {
		w := newNATSWorker("nats://example:4222").(*natsWorker)
		body, err := json.Marshal(natsMessage{Type: "job:future", Queue: "default", AvailableAtMS: time.Now().Add(10 * time.Millisecond).UnixMilli()})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		w.processMessage(&nats.Msg{Data: body})
		time.Sleep(20 * time.Millisecond)
	})

	t.Run("failed handler with retries calls republish path", func(t *testing.T) {
		w := newNATSWorker("nats://example:4222").(*natsWorker)
		w.Register("job:fail", func(context.Context, Task) error { return errors.New("boom") })
		body, err := json.Marshal(natsMessage{Type: "job:fail", Queue: "default", Attempt: 0, MaxRetry: 2, BackoffMillis: 5})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		w.processMessage(&nats.Msg{Data: body})
	})

	t.Run("failed handler at max retries stops", func(t *testing.T) {
		w := newNATSWorker("nats://example:4222").(*natsWorker)
		w.Register("job:terminal", func(context.Context, Task) error { return errors.New("boom") })
		body, err := json.Marshal(natsMessage{Type: "job:terminal", Queue: "default", Attempt: 2, MaxRetry: 2})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		w.processMessage(&nats.Msg{Data: body})
	})
}
