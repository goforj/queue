package queue

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestQueueErrorContract_DispatchCtxCancellation(t *testing.T) {
	newSaturatedQueue := func(t *testing.T) (*Queue, chan struct{}) {
		t.Helper()
		backend := newLocalQueueWithConfig(DriverWorkerpool, WorkerpoolConfig{
			Workers:       1,
			QueueCapacity: 1,
		})
		cfg := (Config{Driver: DriverWorkerpool}).normalize()
		rt := &nativeQueueRuntime{
			common: &queueCommon{
				inner:  newObservedQueue(backend, cfg.Driver, cfg.Observer),
				cfg:    cfg,
				driver: cfg.Driver,
			},
			runtime:    backend,
			registered: make(map[string]Handler),
		}
		q, err := newQueueFromRuntime(rt)
		if err != nil {
			t.Fatalf("new queue from workerpool runtime: %v", err)
		}
		blockHandler := make(chan struct{})
		q.Register("job:error-contract:block", func(context.Context, Context) error {
			<-blockHandler
			return nil
		})
		if err := q.StartWorkers(context.Background()); err != nil {
			t.Fatalf("start workers: %v", err)
		}
		t.Cleanup(func() {
			close(blockHandler)
			_ = q.Shutdown(context.Background())
		})

		// First dispatch occupies the lone worker and blocks in the handler.
		if _, err := q.Dispatch(NewJob("job:error-contract:block").OnQueue("default")); err != nil {
			t.Fatalf("dispatch blocking worker job: %v", err)
		}
		// Second dispatch fills the queue buffer while the worker is blocked.
		if _, err := q.Dispatch(NewJob("job:error-contract:block").OnQueue("default")); err != nil {
			t.Fatalf("dispatch queued blocker job: %v", err)
		}
		return q, blockHandler
	}

	t.Run("precanceled_context", func(t *testing.T) {
		q, _ := newSaturatedQueue(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := q.DispatchCtx(ctx, NewJob("job:error-contract:block").OnQueue("default"))
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	})

	t.Run("expired_deadline_context", func(t *testing.T) {
		q, _ := newSaturatedQueue(t)

		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-1*time.Second))
		defer cancel()

		_, err := q.DispatchCtx(ctx, NewJob("job:error-contract:block").OnQueue("default"))
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expected context.DeadlineExceeded, got %v", err)
		}
	})
}

func TestQueueErrorContract_UnsupportedCapabilities(t *testing.T) {
	q, err := NewNull()
	if err != nil {
		t.Fatalf("new null queue: %v", err)
	}

	if err := q.Pause(context.Background(), "default"); !errors.Is(err, ErrPauseUnsupported) {
		t.Fatalf("expected ErrPauseUnsupported from Queue.Pause, got %v", err)
	}
	if err := q.Resume(context.Background(), "default"); !errors.Is(err, ErrPauseUnsupported) {
		t.Fatalf("expected ErrPauseUnsupported from Queue.Resume, got %v", err)
	}

	_, err = q.Stats(context.Background())
	if err == nil {
		t.Fatal("expected stats unsupported error")
	}
	if !strings.Contains(err.Error(), "stats provider is not available") {
		t.Fatalf("expected unsupported stats error message, got %v", err)
	}
	if !strings.Contains(err.Error(), string(DriverNull)) {
		t.Fatalf("expected driver name in stats unsupported error, got %v", err)
	}
}

func TestQueueErrorContract_WorkflowNotFound(t *testing.T) {
	q, err := NewSync()
	if err != nil {
		t.Fatalf("new sync queue: %v", err)
	}

	if _, err := q.FindChain(context.Background(), "missing-chain"); !errors.Is(err, ErrWorkflowNotFound) {
		t.Fatalf("expected ErrWorkflowNotFound for chain lookup, got %v", err)
	}
	if _, err := q.FindBatch(context.Background(), "missing-batch"); !errors.Is(err, ErrWorkflowNotFound) {
		t.Fatalf("expected ErrWorkflowNotFound for batch lookup, got %v", err)
	}
}

func TestQueueErrorContract_Constructors(t *testing.T) {
	t.Run("unsupported_driver", func(t *testing.T) {
		_, err := New(Config{Driver: Driver("nope")})
		if err == nil {
			t.Fatal("expected unsupported driver error")
		}
		if !strings.Contains(err.Error(), "unsupported queue driver") {
			t.Fatalf("expected unsupported driver error message, got %v", err)
		}
		if !strings.Contains(err.Error(), `"nope"`) {
			t.Fatalf("expected driver name in unsupported driver error, got %v", err)
		}
	})

	t.Run("optional_driver_moved_guidance", func(t *testing.T) {
		tests := []struct {
			driver Driver
			want   string
		}{
			{driver: DriverRedis, want: "driver/redisqueue"},
			{driver: DriverNATS, want: "driver/natsqueue"},
			{driver: DriverSQS, want: "driver/sqsqueue"},
			{driver: DriverRabbitMQ, want: "driver/rabbitmqqueue"},
			{driver: DriverDatabase, want: "driver/{mysqlqueue,postgresqueue,sqlitequeue}"},
		}
		for _, tt := range tests {
			tt := tt
			t.Run(string(tt.driver), func(t *testing.T) {
				_, err := New(Config{Driver: tt.driver})
				if err == nil {
					t.Fatalf("expected moved-driver guidance error for %q", tt.driver)
				}
				if !strings.Contains(err.Error(), tt.want) {
					t.Fatalf("expected guidance %q in error, got %v", tt.want, err)
				}
			})
		}
	})
}
