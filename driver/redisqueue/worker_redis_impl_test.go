package redisqueue

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/goforj/queue"
	"github.com/goforj/queue/busruntime"
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

	if err := w.Shutdown(nil); err != nil {
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
	if !w.started || !w.draining {
		t.Fatalf("worker lost retryable drain state: started=%t draining=%t", w.started, w.draining)
	}
	if err := w.StartWorkers(context.Background()); !errors.Is(err, queue.ErrQueuerShuttingDown) {
		t.Fatalf("start during drain error = %v, want ErrQueuerShuttingDown", err)
	}

	close(server.shutdownCh)
	if err := w.Shutdown(context.Background()); err != nil {
		t.Fatalf("retry shutdown failed: %v", err)
	}
	if server.shutdownCalls != 1 {
		t.Fatalf("server shutdown calls = %d, want 1", server.shutdownCalls)
	}
	if w.started || w.draining {
		t.Fatalf("worker remained active after drain: started=%t draining=%t", w.started, w.draining)
	}
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
	if len(events) != 4 {
		t.Fatalf("expected 4 process events, got %d", len(events))
	}
	if events[0].Kind != queue.EventProcessStarted || events[1].Kind != queue.EventProcessSucceeded {
		t.Fatalf("unexpected first pair kinds: %s, %s", events[0].Kind, events[1].Kind)
	}
	if events[2].Kind != queue.EventProcessStarted || events[3].Kind != queue.EventProcessFailed {
		t.Fatalf("unexpected second pair kinds: %s, %s", events[2].Kind, events[3].Kind)
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
}

// TestRedisWorker_PanicClosesObserverActive verifies native Redis telemetry
// finalizes the failed attempt before preserving Asynq's panic semantics.
func TestRedisWorker_PanicClosesObserverActive(t *testing.T) {
	server := &serverStub{}
	collector := queue.NewStatsCollector()
	var events []queue.Event
	observer := queue.MultiObserver(
		collector,
		queue.ObserverFunc(func(_ context.Context, event queue.Event) {
			events = append(events, event)
		}),
	)
	w := newRedisWorker(server, backend.NewServeMux(), observer)
	w.Register("job:panic", func(context.Context, queue.Job) error {
		panic("redis panic")
	})
	if err := w.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers: %v", err)
	}
	task := backend.NewTaskWithHeaders("job:panic", nil, map[string]string{
		redisDriverJobMetadataHeader: `{"schema_version":1,"dispatch_id":"dsp_redis_panic","job_id":"job_redis_panic"}`,
	})
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		_ = server.lastStartHandler.ProcessTask(context.Background(), task)
	}()
	if recovered != "redis panic" {
		t.Fatalf("recovered panic = %#v, want original value", recovered)
	}
	if len(events) != 2 || events[0].Kind != queue.EventProcessStarted || events[1].Kind != queue.EventProcessFailed {
		t.Fatalf("panic events = %+v, want process_started then process_failed", events)
	}
	if events[1].Err == nil || events[1].Err.Error() != "redis handler panicked: redis panic" {
		t.Fatalf("panic failure error = %v, want stable diagnostic", events[1].Err)
	}
	counters, ok := collector.Snapshot().Queue("default")
	if !ok {
		t.Fatal("expected default queue counters")
	}
	if counters.Active != 0 || counters.Failed != 1 || counters.Processed != 0 {
		t.Fatalf("panic counters = %+v, want active=0 failed=1 processed=0", counters)
	}
}

// TestRedisHandlerPanicErrorPreservesErrorIdentity verifies Redis panic
// telemetry wraps error values and formats non-error values deterministically.
func TestRedisHandlerPanicErrorPreservesErrorIdentity(t *testing.T) {
	sentinel := errors.New("redis panic sentinel")
	if err := redisHandlerPanicError(sentinel); !errors.Is(err, sentinel) {
		t.Fatalf("error panic = %v, want wrapped sentinel", err)
	}
	if err := redisHandlerPanicError("value"); err == nil || err.Error() != "redis handler panicked: value" {
		t.Fatalf("value panic = %v, want stable diagnostic", err)
	}
}

// TestObserveRedisAttemptStartEmitsRetryDelivery verifies Redis reports a numbered retry when Asynq delivers that attempt.
func TestObserveRedisAttemptStartEmitsRetryDelivery(t *testing.T) {
	var events []queue.Event
	observer := queue.ObserverFunc(func(_ context.Context, event queue.Event) {
		events = append(events, event)
	})
	observeRedisAttemptStart(context.Background(), observer, queue.Event{
		Driver:   queue.DriverRedis,
		JobType:  "job:retry",
		Attempt:  1,
		MaxRetry: 3,
	})
	if len(events) != 2 || events[0].Kind != queue.EventProcessRetried || events[1].Kind != queue.EventProcessStarted {
		t.Fatalf("attempt start events = %+v, want retried then started", events)
	}
	if events[0].Attempt != 1 || events[1].Attempt != 1 {
		t.Fatalf("attempt metadata changed: %+v", events)
	}
}

// TestRedisWorker_PermanentFailureIncludesSkipRetryWithoutObserver verifies terminal settlement does not depend on observability being enabled.
func TestRedisWorker_PermanentFailureIncludesSkipRetryWithoutObserver(t *testing.T) {
	server := &serverStub{}
	w := newRedisWorker(server, backend.NewServeMux(), nil)
	cause := errors.New("invalid application payload")
	w.Register("job:permanent", func(context.Context, queue.Job) error {
		return busruntime.Permanent(cause)
	})
	if err := w.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers failed: %v", err)
	}

	err := server.lastStartHandler.ProcessTask(context.Background(), backend.NewTask("job:permanent", nil))
	if !errors.Is(err, cause) {
		t.Fatalf("expected returned error to preserve cause, got %v", err)
	}
	if !errors.Is(err, backend.SkipRetry) {
		t.Fatalf("expected permanent error to include asynq SkipRetry, got %v", err)
	}
}

// TestRedisSettlementErrorDecisions verifies every terminal application outcome consumes no reserved transport retry.
func TestRedisSettlementErrorDecisions(t *testing.T) {
	cause := errors.New("handler failed")

	retryErr := redisSettlementError(busruntime.DeliveryAttempt{Number: 0, MaxRetry: 2}, cause)
	if !errors.Is(retryErr, cause) || errors.Is(retryErr, backend.SkipRetry) {
		t.Fatalf("retry settlement = %v", retryErr)
	}

	exhaustedErr := redisSettlementError(busruntime.DeliveryAttempt{Number: 2, MaxRetry: 2}, cause)
	if !errors.Is(exhaustedErr, cause) || !errors.Is(exhaustedErr, backend.SkipRetry) {
		t.Fatalf("exhausted settlement = %v", exhaustedErr)
	}

	permanentErr := redisSettlementError(
		busruntime.DeliveryAttempt{Number: 0, MaxRetry: 2},
		busruntime.Permanent(cause),
	)
	if !errors.Is(permanentErr, cause) || !errors.Is(permanentErr, backend.SkipRetry) {
		t.Fatalf("permanent settlement = %v", permanentErr)
	}

	uncommittedErr := redisSettlementError(
		busruntime.DeliveryAttempt{Number: 2, MaxRetry: 2},
		busruntime.Uncommitted(cause),
	)
	if !errors.Is(uncommittedErr, cause) || errors.Is(uncommittedErr, backend.SkipRetry) {
		t.Fatalf("uncommitted settlement = %v", uncommittedErr)
	}

	skipRetryErr := redisSettlementError(
		busruntime.DeliveryAttempt{Number: 2, MaxRetry: 2},
		errors.Join(cause, backend.SkipRetry),
	)
	if !errors.Is(skipRetryErr, cause) || !errors.Is(skipRetryErr, backend.SkipRetry) {
		t.Fatalf("existing skip-retry settlement = %v", skipRetryErr)
	}
}

// TestRedisApplicationMaxRetry verifies only a valid reserve header changes the handler-visible retry budget.
func TestRedisApplicationMaxRetry(t *testing.T) {
	tests := []struct {
		name         string
		task         *backend.Task
		transportMax int
		want         int
	}{
		{name: "nil task", task: nil, transportMax: 4, want: 4},
		{name: "legacy task", task: backend.NewTask("job", nil), transportMax: 3, want: 3},
		{name: "reserved task", task: backend.NewTaskWithHeaders("job", nil, map[string]string{redisApplicationMaxRetryHeader: "2"}), transportMax: 3, want: 2},
		{name: "mismatched reserve", task: backend.NewTaskWithHeaders("job", nil, map[string]string{redisApplicationMaxRetryHeader: "2"}), transportMax: 4, want: 4},
		{name: "malformed reserve", task: backend.NewTaskWithHeaders("job", nil, map[string]string{redisApplicationMaxRetryHeader: "bad"}), transportMax: 3, want: 3},
		{name: "negative reserve", task: backend.NewTaskWithHeaders("job", nil, map[string]string{redisApplicationMaxRetryHeader: "-1"}), transportMax: 0, want: 0},
		{name: "overflow reserve", task: backend.NewTaskWithHeaders("job", nil, map[string]string{redisApplicationMaxRetryHeader: strconv.Itoa(int(^uint(0) >> 1))}), transportMax: int(^uint(0) >> 1), want: int(^uint(0) >> 1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := redisApplicationMaxRetry(test.task, test.transportMax); got != test.want {
				t.Fatalf("application max retry = %d, want %d", got, test.want)
			}
		})
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

	payload := []byte(`{"schema_version":1,"dispatch_id":"dsp_redis","job_id":"job_redis","chain_id":"chn_redis","job":{"type":"monitoring:check"}}`)
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
		if event.DispatchID != "dsp_redis" || event.JobID != "job_redis" || event.ChainID != "chn_redis" {
			t.Fatalf("expected correlated redis event, got %+v", event)
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
		if opts.QueueName != "default" {
			t.Fatalf("expected normalized queue name in no-observer path, got %q", opts.QueueName)
		}
		if opts.Attempt != 0 {
			t.Fatalf("expected zero attempt in no-observer path, got %d", opts.Attempt)
		}
		if opts.MaxRetry == nil || *opts.MaxRetry != 0 {
			t.Fatalf("expected zero max retry in no-observer path, got %v", opts.MaxRetry)
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
