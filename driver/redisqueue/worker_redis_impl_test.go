package redisqueue

import (
	"context"
	"errors"
	"testing"

	"github.com/goforj/queue"
	backend "github.com/hibiken/asynq"
)

type serverStub struct {
	startErr         error
	startCalls       int
	shutdownCalls    int
	shutdownCh       chan struct{}
	lastStartHandler backend.Handler
}

func (s *serverStub) Start(handler backend.Handler) error {
	s.startCalls++
	s.lastStartHandler = handler
	return s.startErr
}

func (s *serverStub) Shutdown() {
	s.shutdownCalls++
	if s.shutdownCh != nil {
		<-s.shutdownCh
	}
}
func (s *serverStub) Stop() {}

func TestRedisWorker_RegisterStartShutdownBranches(t *testing.T) {
	server := &serverStub{}
	mux := backend.NewServeMux()
	w := newRedisWorker(server, mux, nil)

	// Register no-op branches.
	w.Register("", func(context.Context, queue.Job) error { return nil })
	w.Register("job:nil", nil)
	w.Register("job:ok", func(context.Context, queue.Job) error { return nil })

	// Start with canceled context branch.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := w.StartWorkers(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled, got %v", err)
	}

	if err := w.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers failed: %v", err)
	}
	if err := w.StartWorkers(context.Background()); err != nil {
		t.Fatalf("second start should be idempotent, got %v", err)
	}
	if server.startCalls != 1 {
		t.Fatalf("expected one start call, got %d", server.startCalls)
	}

	if err := w.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown failed: %v", err)
	}
	if err := w.Shutdown(context.Background()); err != nil {
		t.Fatalf("second shutdown should be idempotent, got %v", err)
	}
	if server.shutdownCalls != 1 {
		t.Fatalf("expected one shutdown call, got %d", server.shutdownCalls)
	}
}

func TestRedisWorker_StartError(t *testing.T) {
	server := &serverStub{startErr: errors.New("start failed")}
	w := newRedisWorker(server, backend.NewServeMux(), nil)

	if err := w.StartWorkers(context.Background()); err == nil {
		t.Fatal("expected start error")
	}
	if w.started {
		t.Fatal("worker should remain not started on start error")
	}
}

func TestRedisWorker_ShutdownHonorsContext(t *testing.T) {
	server := &serverStub{shutdownCh: make(chan struct{})}
	w := newRedisWorker(server, backend.NewServeMux(), nil)

	if err := w.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := w.Shutdown(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled, got %v", err)
	}

	close(server.shutdownCh)
}

func TestRedisWorker_ProcessEventsWithObserver(t *testing.T) {
	server := &serverStub{}
	var events []queue.Event
	observer := queue.ObserverFunc(func(_ context.Context, event queue.Event) { events = append(events, event) })
	w := newRedisWorker(server, backend.NewServeMux(), observer)

	w.Register("job:ok", func(context.Context, queue.Job) error { return nil })
	w.Register("job:fail", func(context.Context, queue.Job) error { return errors.New("boom") })
	if err := w.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers failed: %v", err)
	}
	if server.lastStartHandler == nil {
		t.Fatal("expected start handler")
	}

	if err := server.lastStartHandler.ProcessTask(context.Background(), backend.NewTask("job:ok", []byte("ok"))); err != nil {
		t.Fatalf("process ok task failed: %v", err)
	}
	if err := server.lastStartHandler.ProcessTask(context.Background(), backend.NewTask("job:fail", []byte("fail"))); err == nil {
		t.Fatal("expected failing task error")
	}
	if len(events) != 5 {
		t.Fatalf("expected 5 process events, got %d", len(events))
	}
	if events[0].Kind != queue.EventProcessStarted || events[1].Kind != queue.EventProcessSucceeded {
		t.Fatalf("unexpected first pair kinds: %s, %s", events[0].Kind, events[1].Kind)
	}
	if events[2].Kind != queue.EventProcessStarted || events[3].Kind != queue.EventProcessFailed {
		t.Fatalf("unexpected second pair kinds: %s, %s", events[2].Kind, events[3].Kind)
	}
	if events[4].Kind != queue.EventProcessArchived {
		t.Fatalf("unexpected terminal event kind: %s", events[4].Kind)
	}
	for _, event := range events {
		if event.Driver != queue.DriverRedis {
			t.Fatalf("expected redis driver, got %q", event.Driver)
		}
		if event.Queue == "" {
			t.Fatal("expected queue to be set")
		}
	}
	if events[1].Duration < 0 {
		t.Fatalf("expected non-negative success duration, got %s", events[1].Duration)
	}
	if events[3].Err == nil {
		t.Fatal("expected failed event error")
	}
	if events[1].Time.IsZero() || events[3].Time.IsZero() {
		t.Fatal("expected event timestamps to be set")
	}
	if events[4].Err != nil {
		t.Fatal("expected archived event error to be nil")
	}
}

func TestRedisWorker_ProcessEventsUnwrapBusEnvelopeJobType(t *testing.T) {
	server := &serverStub{}
	var events []queue.Event
	observer := queue.ObserverFunc(func(_ context.Context, event queue.Event) { events = append(events, event) })
	w := newRedisWorker(server, backend.NewServeMux(), observer)

	w.Register("bus:job", func(context.Context, queue.Job) error { return nil })
	if err := w.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers failed: %v", err)
	}

	payload := []byte(`{"job":{"type":"monitoring:check"}}`)
	if err := server.lastStartHandler.ProcessTask(context.Background(), backend.NewTask("bus:job", payload)); err != nil {
		t.Fatalf("process task failed: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 process events, got %d", len(events))
	}
	for _, event := range events {
		if event.JobType != "monitoring:check" {
			t.Fatalf("expected unwrapped observed job type, got %q", event.JobType)
		}
	}
}

func TestRedisWorker_NoObserverFastPath(t *testing.T) {
	server := &serverStub{}
	w := newRedisWorker(server, backend.NewServeMux(), nil)

	called := 0
	w.Register("job:plain", func(_ context.Context, job queue.Job) error {
		called++
		if job.Type != "job:plain" {
			t.Fatalf("expected job type job:plain, got %q", job.Type)
		}
		opts := queue.DriverOptions(job)
		if opts.QueueName != "" {
			t.Fatalf("expected empty queue name in no-observer path, got %q", opts.QueueName)
		}
		if opts.Attempt != 0 {
			t.Fatalf("expected zero attempt in no-observer path, got %d", opts.Attempt)
		}
		if opts.MaxRetry != nil {
			t.Fatalf("expected nil max retry in no-observer path, got %v", *opts.MaxRetry)
		}
		return nil
	})
	if err := w.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers failed: %v", err)
	}
	if err := server.lastStartHandler.ProcessTask(context.Background(), backend.NewTask("job:plain", []byte("ok"))); err != nil {
		t.Fatalf("process task failed: %v", err)
	}
	if called != 1 {
		t.Fatalf("expected handler called once, got %d", called)
	}
}

func TestRedisWorker_ObserverSeesDecoratedContext(t *testing.T) {
	server := &serverStub{}
	type ctxKey struct{}
	key := ctxKey{}
	const want = "jobs"

	var observed []string
	var handled []string
	observer := queue.ObserverFunc(func(ctx context.Context, event queue.Event) {
		if event.Kind != queue.EventProcessStarted && event.Kind != queue.EventProcessSucceeded {
			return
		}
		value, _ := ctx.Value(key).(string)
		observed = append(observed, value)
	})
	w := newRedisWorker(server, backend.NewServeMux(), observer)
	w.SetHandlerContextDecorator(func(ctx context.Context) context.Context {
		return context.WithValue(ctx, key, want)
	})

	w.Register("job:decorated", func(ctx context.Context, _ queue.Job) error {
		value, _ := ctx.Value(key).(string)
		handled = append(handled, value)
		return nil
	})
	if err := w.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers failed: %v", err)
	}
	if err := server.lastStartHandler.ProcessTask(context.Background(), backend.NewTask("job:decorated", []byte("ok"))); err != nil {
		t.Fatalf("process task failed: %v", err)
	}

	if len(observed) != 2 {
		t.Fatalf("expected 2 observed events, got %d", len(observed))
	}
	for i, got := range observed {
		if got != want {
			t.Fatalf("expected observed[%d] = %q, got %q", i, want, got)
		}
	}
	if len(handled) != 1 || handled[0] != want {
		t.Fatalf("expected handler to see %q, got %#v", want, handled)
	}
}
