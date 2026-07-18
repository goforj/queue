package natsqueue

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/goforj/queue"
	"github.com/goforj/queue/busruntime"
	"github.com/goforj/queue/queuecore"
	"github.com/nats-io/nats.go"
)

func TestNATSWorker_NewRegisterAndShutdown(t *testing.T) {
	w := newNATSWorker("nats://example:4222")
	if w.url != "nats://example:4222" {
		t.Fatalf("expected url to be preserved, got %q", w.url)
	}
	if w.workers <= 0 {
		t.Fatalf("expected positive default workers, got %d", w.workers)
	}

	w.Register("", func(context.Context, queue.Job) error { return nil })
	w.Register("job:nil", nil)
	if len(w.handlers) != 0 {
		t.Fatalf("expected ignored registrations, got %d handlers", len(w.handlers))
	}

	w.Register("job:ok", func(context.Context, queue.Job) error { return nil })
	if len(w.handlers) != 1 {
		t.Fatalf("expected one handler, got %d", len(w.handlers))
	}

	if err := w.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown should be no-op with nil conn/sub: %v", err)
	}
}

func TestNATSWorker_StartWorkersCanceledContext(t *testing.T) {
	w := newNATSWorker("nats://example:4222")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := w.StartWorkers(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled, got %v", err)
	}
}

func TestNATSWorker_ProcessMessageBranches(t *testing.T) {
	t.Run("invalid json ignored", func(t *testing.T) {
		w := newNATSWorker("nats://example:4222")
		w.processMessage(&nats.Msg{Data: []byte("{")})
	})

	t.Run("missing handler ignored", func(t *testing.T) {
		w := newNATSWorker("nats://example:4222")
		body, err := json.Marshal(natsMessage{Type: "job:none", Queue: "default"})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		w.processMessage(&nats.Msg{Data: body})
	})

	t.Run("success uses timeout and job options", func(t *testing.T) {
		called := 0
		w := newNATSWorker("nats://example:4222")
		w.Register("job:ok", func(ctx context.Context, job queue.Job) error {
			called++
			if _, ok := ctx.Deadline(); !ok {
				t.Fatal("expected timeout context")
			}
			opts := queuecore.DriverOptions(job)
			if job.Type != "job:ok" || opts.QueueName != "critical" || opts.Attempt != 2 {
				t.Fatalf("unexpected job fields: type=%q queue=%q attempt=%d", job.Type, opts.QueueName, opts.Attempt)
			}
			if opts.MaxRetry == nil || *opts.MaxRetry != 3 {
				t.Fatalf("expected retry=3, got %+v", opts.MaxRetry)
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
		w := newNATSWorker("nats://example:4222")
		body, err := json.Marshal(natsMessage{Type: "job:future", Queue: "default", AvailableAtMS: time.Now().Add(10 * time.Millisecond).UnixMilli()})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		w.processMessage(&nats.Msg{Data: body})
		time.Sleep(20 * time.Millisecond)
	})

	t.Run("failed handler with retries calls republish path", func(t *testing.T) {
		var events []queue.Event
		w := newNATSWorkerWithConfig(natsWorkerConfig{
			URL:      "nats://example:4222",
			Workers:  1,
			Observer: queue.ObserverFunc(func(_ context.Context, e queue.Event) { events = append(events, e) }),
		})
		w.Register("job:fail", func(context.Context, queue.Job) error { return errors.New("boom") })
		body, err := json.Marshal(natsMessage{Type: "job:fail", Queue: "default", Attempt: 0, MaxRetry: 2, BackoffMillis: 5})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		w.processMessage(&nats.Msg{Data: body})
		if len(events) == 0 || events[0].Kind != queue.EventRepublishFailed || events[0].Driver != queue.DriverNATS {
			t.Fatalf("expected republish_failed nats event, got %+v", events)
		}
		if events[0].Layer != queue.EventLayerWorker {
			t.Fatalf("republish_failed layer = %q, want worker", events[0].Layer)
		}
	})

	t.Run("republish failure unwraps bus envelope job type", func(t *testing.T) {
		var events []queue.Event
		w := newNATSWorkerWithConfig(natsWorkerConfig{
			URL:      "nats://example:4222",
			Workers:  1,
			Observer: queue.ObserverFunc(func(_ context.Context, e queue.Event) { events = append(events, e) }),
		})
		w.Register("bus:job", func(context.Context, queue.Job) error { return errors.New("boom") })
		body, err := json.Marshal(natsMessage{
			Type:          "bus:job",
			Queue:         "default",
			Attempt:       0,
			MaxRetry:      2,
			BackoffMillis: 5,
			Payload:       []byte(`{"schema_version":1,"dispatch_id":"dsp_nats","job_id":"job_nats","chain_id":"chn_nats","job":{"type":"monitoring:check"}}`),
		})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		w.processMessage(&nats.Msg{Data: body})
		if len(events) == 0 {
			t.Fatal("expected republish failure event")
		}
		if events[0].JobType != "monitoring:check" {
			t.Fatalf("expected unwrapped observed job type, got %q", events[0].JobType)
		}
		if events[0].DispatchID != "dsp_nats" || events[0].JobID != "job_nats" || events[0].ChainID != "chn_nats" {
			t.Fatalf("expected correlated nats event, got %+v", events[0])
		}
	})

	t.Run("failed handler at max retries stops", func(t *testing.T) {
		w := newNATSWorker("nats://example:4222")
		w.Register("job:terminal", func(context.Context, queue.Job) error { return errors.New("boom") })
		body, err := json.Marshal(natsMessage{Type: "job:terminal", Queue: "default", Attempt: 2, MaxRetry: 2})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		w.processMessage(&nats.Msg{Data: body})
	})
}

// TestNATSWorker_AttemptDecisionSettlement verifies terminal and uncommitted outcomes choose distinct Core NATS settlement paths.
func TestNATSWorker_AttemptDecisionSettlement(t *testing.T) {
	t.Run("permanent failure does not republish", func(t *testing.T) {
		var events []queue.Event
		w := newNATSWorkerWithConfig(natsWorkerConfig{
			URL:      "nats://example:4222",
			Observer: queue.ObserverFunc(func(_ context.Context, event queue.Event) { events = append(events, event) }),
		})
		w.Register("job:permanent", func(ctx context.Context, _ queue.Job) error {
			attempt, ok := busruntime.DeliveryAttemptFromContext(ctx)
			if !ok || attempt.Number != 0 || attempt.MaxRetry != 3 {
				t.Fatalf("unexpected delivery attempt: %+v, present=%t", attempt, ok)
			}
			return busruntime.Permanent(errors.New("invalid job"))
		})
		body, err := json.Marshal(natsMessage{Type: "job:permanent", Queue: "default", MaxRetry: 3})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}

		w.processMessage(&nats.Msg{Data: body})

		if len(events) != 0 {
			t.Fatalf("permanent failure must not reach the republish path, got %+v", events)
		}
	})

	t.Run("uncommitted failure republishes the same attempt", func(t *testing.T) {
		var events []queue.Event
		w := newNATSWorkerWithConfig(natsWorkerConfig{
			URL:      "nats://example:4222",
			Observer: queue.ObserverFunc(func(_ context.Context, event queue.Event) { events = append(events, event) }),
		})
		w.Register("job:uncommitted", func(ctx context.Context, _ queue.Job) error {
			attempt, ok := busruntime.DeliveryAttemptFromContext(ctx)
			if !ok || attempt.Number != 1 || attempt.MaxRetry != 4 {
				t.Fatalf("unexpected delivery attempt: %+v, present=%t", attempt, ok)
			}
			return busruntime.Uncommitted(errors.New("store unavailable"))
		})
		body, err := json.Marshal(natsMessage{
			Type:          "job:uncommitted",
			Queue:         "default",
			Attempt:       1,
			MaxRetry:      4,
			BackoffMillis: 1_000,
		})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}

		w.processMessage(&nats.Msg{Data: body})

		if len(events) != 1 || events[0].Kind != queue.EventRepublishFailed {
			t.Fatalf("core NATS must attempt to republish uncommitted work, got %+v", events)
		}
		if events[0].Attempt != 1 || events[0].MaxRetry != 4 {
			t.Fatalf("uncommitted republish consumed an application attempt: %+v", events[0])
		}
	})
}
