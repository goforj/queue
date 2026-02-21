package queue

import (
	"context"
	"errors"
	"testing"
)

type runtimeBackendStub struct {
	registered map[string]Handler
	startCalls int
	stopCalls  int
	startErr   error
	stopErr    error
}

func (s *runtimeBackendStub) Driver() Driver { return DriverSync }
func (s *runtimeBackendStub) Dispatch(context.Context, Job) error {
	return nil
}

func (s *runtimeBackendStub) Register(taskType string, handler Handler) {
	if s.registered == nil {
		s.registered = make(map[string]Handler)
	}
	s.registered[taskType] = handler
}

func (s *runtimeBackendStub) StartWorkers(context.Context) error {
	s.startCalls++
	return s.startErr
}

func (s *runtimeBackendStub) Shutdown(context.Context) error {
	s.stopCalls++
	return s.stopErr
}

type queueBackendRecorder struct {
	dispatched []Job
	shutdowns  int
}

func (q *queueBackendRecorder) Driver() Driver { return DriverNull }
func (q *queueBackendRecorder) Dispatch(_ context.Context, task Job) error {
	q.dispatched = append(q.dispatched, task)
	return nil
}
func (q *queueBackendRecorder) Shutdown(context.Context) error {
	q.shutdowns++
	return nil
}

func TestQueueCommon_TaskFromJobAndHelpers(t *testing.T) {
	common := &queueCommon{cfg: Config{DefaultQueue: "default"}}

	if _, err := common.taskFromJob(nil); err == nil {
		t.Fatal("expected nil job error")
	}
	if _, err := common.taskFromJob(NewJob("")); err == nil {
		t.Fatal("expected empty task type error")
	}
	if _, err := common.taskFromJob(struct{ F func() }{}); err == nil {
		t.Fatal("expected marshal error for func field")
	}
	type namedJob struct{}
	if got := taskTypeFromValue(namedJob{}); got != "namedJob" {
		t.Fatalf("expected inferred type namedJob, got %q", got)
	}
	if got := taskTypeFromValue(&namedJob{}); got != "namedJob" {
		t.Fatalf("expected inferred pointer type namedJob, got %q", got)
	}
	if got := taskTypeFromValue(map[string]any{}); got != "" {
		t.Fatalf("expected anonymous type to return empty, got %q", got)
	}
}

func TestQueueCommonDispatchAndNativeRuntimeWrappers(t *testing.T) {
	inner := &queueBackendRecorder{}
	worker := &runtimeBackendStub{}
	common := &queueCommon{inner: inner, cfg: Config{DefaultQueue: "default"}, driver: DriverSync}
	q := &nativeQueueRuntime{common: common, runtime: worker, registered: map[string]Handler{}}

	if q.Driver() != DriverSync {
		t.Fatalf("expected driver sync, got %q", q.Driver())
	}

	type emailJob struct{ ID int }
	if err := q.Dispatch(emailJob{ID: 1}); err != nil {
		t.Fatalf("dispatch wrapper failed: %v", err)
	}
	if len(inner.dispatched) != 1 || inner.dispatched[0].Type != "emailJob" {
		t.Fatalf("expected one inferred task dispatch, got %+v", inner.dispatched)
	}

	q.Register("job:one", func(context.Context, Job) error { return nil })
	if err := q.StartWorkers(nil); err != nil {
		t.Fatalf("start workers failed: %v", err)
	}
	if worker.startCalls != 1 {
		t.Fatalf("expected start called once, got %d", worker.startCalls)
	}
	if _, ok := worker.registered["job:one"]; !ok {
		t.Fatal("expected registered handler to be forwarded on start")
	}
	q.Register("job:two", func(context.Context, Job) error { return nil })
	if _, ok := worker.registered["job:two"]; !ok {
		t.Fatal("expected register after start to forward immediately")
	}
	if err := q.Shutdown(nil); err != nil {
		t.Fatalf("shutdown failed: %v", err)
	}
	if worker.stopCalls != 1 {
		t.Fatalf("expected shutdown called once, got %d", worker.stopCalls)
	}
}

func TestExternalQueueRuntimeRegisterShutdownAndWorkers(t *testing.T) {
	inner := &queueBackendRecorder{}
	worker := &runtimeBackendStub{}
	common := &queueCommon{inner: inner, cfg: Config{DefaultQueue: "default"}, driver: DriverNATS}
	q := &externalQueueRuntime{
		common:     common,
		registered: map[string]Handler{},
		worker:     worker,
		started:    true,
	}

	q.Workers(3)
	if q.workers != 0 {
		t.Fatalf("expected workers unchanged when started, got %d", q.workers)
	}
	q.started = false
	q.Workers(3)
	if q.workers != 3 {
		t.Fatalf("expected workers=3 before start, got %d", q.workers)
	}
	q.started = true

	q.Register("job:external", func(context.Context, Job) error { return nil })
	if _, ok := worker.registered["job:external"]; !ok {
		t.Fatal("expected register to forward to started external worker")
	}
	if err := q.Dispatch(NewJob("job:external").OnQueue("default")); err != nil {
		t.Fatalf("dispatch wrapper failed: %v", err)
	}
	if err := q.DispatchCtx(context.Background(), NewJob("job:external").OnQueue("default")); err != nil {
		t.Fatalf("dispatch ctx failed: %v", err)
	}
	if err := q.Shutdown(nil); err != nil {
		t.Fatalf("shutdown failed: %v", err)
	}
	if worker.stopCalls != 1 {
		t.Fatalf("expected worker shutdown once, got %d", worker.stopCalls)
	}
	if inner.shutdowns != 1 {
		t.Fatalf("expected inner shutdown once, got %d", inner.shutdowns)
	}
}

func TestExternalQueueRuntimePauseResumeStatsWrappers(t *testing.T) {
	inner := &queueBackendStub{
		stats: StatsSnapshot{
			ByQueue: map[string]QueueCounters{
				"default": {Pending: 3},
			},
		},
	}
	common := &queueCommon{inner: inner, cfg: Config{DefaultQueue: "default"}, driver: DriverNull}
	q := &externalQueueRuntime{common: common, registered: map[string]Handler{}}

	if err := q.Pause(context.Background(), "default"); err != nil {
		t.Fatalf("pause failed: %v", err)
	}
	if err := q.Resume(context.Background(), "default"); err != nil {
		t.Fatalf("resume failed: %v", err)
	}
	snap, err := q.Stats(context.Background())
	if err != nil {
		t.Fatalf("stats failed: %v", err)
	}
	if got := snap.Pending("default"); got != 3 {
		t.Fatalf("expected pending=3, got %d", got)
	}
}

func TestQueueConstructorsAndBackendDriverMethods(t *testing.T) {
	if got := newNullQueue().Driver(); got != DriverNull {
		t.Fatalf("expected null driver, got %q", got)
	}
	newNullQueue().(*nullQueue).Register("job:nil", func(context.Context, Job) error { return nil })
	if got := newNATSQueue("nats://example").Driver(); got != DriverNATS {
		t.Fatalf("expected nats driver, got %q", got)
	}
	if got := newSQSQueue(Config{}).Driver(); got != DriverSQS {
		t.Fatalf("expected sqs driver, got %q", got)
	}
	if got := newRabbitMQQueue("amqp://example", "default").Driver(); got != DriverRabbitMQ {
		t.Fatalf("expected rabbitmq driver, got %q", got)
	}
	if got := newRedisQueue(nil, nil, false).Driver(); got != DriverRedis {
		t.Fatalf("expected redis driver, got %q", got)
	}
	if got := (&databaseQueue{}).Driver(); got != DriverDatabase {
		t.Fatalf("expected database driver, got %q", got)
	}
}

func TestNewQueueWithDefaultsAndNewExternalWorker(t *testing.T) {
	q, err := NewQueueWithDefaults("critical", Config{Driver: DriverSync})
	if err != nil {
		t.Fatalf("new queue with defaults failed: %v", err)
	}
	if q.Driver() != DriverSync {
		t.Fatalf("expected sync driver, got %q", q.Driver())
	}

	w, err := newExternalWorker(Config{Driver: DriverNATS, NATSURL: "nats://example"}, 2)
	if err != nil {
		t.Fatalf("new external nats worker failed: %v", err)
	}
	if _, ok := w.(*natsWorker); !ok {
		t.Fatalf("expected nats worker type, got %T", w)
	}
	if _, err := newExternalWorker(Config{Driver: Driver("unknown")}, 1); err == nil {
		t.Fatal("expected unsupported worker driver error")
	}
}

func TestNativeRuntimeStartWorkersErrorPath(t *testing.T) {
	inner := &queueBackendRecorder{}
	worker := &runtimeBackendStub{startErr: errors.New("start failed")}
	q := &nativeQueueRuntime{
		common:     &queueCommon{inner: inner, cfg: Config{DefaultQueue: "default"}, driver: DriverSync},
		runtime:    worker,
		registered: map[string]Handler{"job:one": func(context.Context, Job) error { return nil }},
	}
	if err := q.StartWorkers(context.Background()); err == nil {
		t.Fatal("expected start workers error")
	}
	if q.started {
		t.Fatal("expected runtime to remain not started on error")
	}
}

func TestQueueCommonWrapRegisteredHandlerWithoutObserver(t *testing.T) {
	common := &queueCommon{cfg: Config{Driver: DriverSync}}
	h := func(context.Context, Job) error { return nil }
	if got := common.wrapRegisteredHandler("job:x", h); got == nil {
		t.Fatal("expected non-nil passthrough handler")
	}
	if got := common.wrapRegisteredHandler("job:x", nil); got != nil {
		t.Fatal("expected nil passthrough handler")
	}
	common.cfg.Observer = ObserverFunc(func(Event) {})
	if wrapped := common.wrapRegisteredHandler("job:x", h); wrapped == nil {
		t.Fatal("expected wrapped handler")
	}
}

func TestWorkersSetOnlyBeforeStartNative(t *testing.T) {
	q := &nativeQueueRuntime{common: &queueCommon{cfg: Config{}}, runtime: &runtimeBackendStub{}}
	q.Workers(0)
	if q.workers != 0 {
		t.Fatalf("expected workers unchanged for non-positive, got %d", q.workers)
	}
	q.Workers(4)
	if q.workers != 4 {
		t.Fatalf("expected workers=4, got %d", q.workers)
	}
	q.started = true
	q.Workers(8)
	if q.workers != 4 {
		t.Fatalf("expected workers unchanged after start, got %d", q.workers)
	}
}

func TestQueueCommonPauseResumeStatsUnsupported(t *testing.T) {
	common := &queueCommon{inner: &queueBackendRecorder{}, driver: DriverNull}
	if err := common.Pause(context.Background(), "default"); !errors.Is(err, ErrPauseUnsupported) {
		t.Fatalf("expected pause unsupported, got %v", err)
	}
	if err := common.Resume(context.Background(), "default"); !errors.Is(err, ErrPauseUnsupported) {
		t.Fatalf("expected resume unsupported, got %v", err)
	}
	if _, err := common.Stats(context.Background()); err == nil {
		t.Fatal("expected stats unsupported error")
	}
}

func TestExternalQueueRuntimeStartWorkersErrorBranches(t *testing.T) {
	t.Run("factory error for unsupported driver", func(t *testing.T) {
		q := &externalQueueRuntime{
			common:     &queueCommon{inner: &queueBackendRecorder{}, cfg: Config{Driver: Driver("unknown")}, driver: Driver("unknown")},
			registered: map[string]Handler{},
		}
		if err := q.StartWorkers(context.Background()); err == nil {
			t.Fatal("expected start workers error for unsupported driver")
		}
		if q.started {
			t.Fatal("expected runtime to remain not started")
		}
	})

	t.Run("worker start error propagates", func(t *testing.T) {
		q := &externalQueueRuntime{
			common: &queueCommon{
				inner:  &queueBackendRecorder{},
				cfg:    Config{Driver: DriverNATS, NATSURL: "nats://127.0.0.1:1"},
				driver: DriverNATS,
			},
			registered: map[string]Handler{
				"job:nats": func(context.Context, Job) error { return nil },
			},
		}
		if err := q.StartWorkers(context.Background()); err == nil {
			t.Fatal("expected start workers error for unreachable nats")
		}
		if q.started || q.worker != nil {
			t.Fatal("expected runtime to remain stopped when worker start fails")
		}
	})
}
