package queue

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/goforj/queue/busruntime"
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

func (s *runtimeBackendStub) Register(jobType string, handler Handler) {
	if s.registered == nil {
		s.registered = make(map[string]Handler)
	}
	s.registered[jobType] = handler
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
func (q *queueBackendRecorder) Dispatch(_ context.Context, job Job) error {
	q.dispatched = append(q.dispatched, job)
	return nil
}
func (q *queueBackendRecorder) Shutdown(context.Context) error {
	q.shutdowns++
	return nil
}

type driverQueueBackendStub struct {
	driver      Driver
	dispatched   []Job
	shutdowns    int
	pauseErr     error
	resumeErr    error
	stats        StatsSnapshot
	statsErr     error
	lastQueueArg string
}

func (s *driverQueueBackendStub) Driver() Driver { return s.driver }
func (s *driverQueueBackendStub) Dispatch(_ context.Context, job Job) error {
	s.dispatched = append(s.dispatched, job)
	return nil
}
func (s *driverQueueBackendStub) Shutdown(context.Context) error {
	s.shutdowns++
	return nil
}
func (s *driverQueueBackendStub) Pause(_ context.Context, queueName string) error {
	s.lastQueueArg = queueName
	return s.pauseErr
}
func (s *driverQueueBackendStub) Resume(_ context.Context, queueName string) error {
	s.lastQueueArg = queueName
	return s.resumeErr
}
func (s *driverQueueBackendStub) Stats(context.Context) (StatsSnapshot, error) {
	return s.stats, s.statsErr
}

type driverRuntimeBackendStub struct {
	*driverQueueBackendStub
	registered map[string]Handler
	startErr    error
	startCalls  int
}

func (s *driverRuntimeBackendStub) Register(jobType string, h Handler) {
	if s.registered == nil {
		s.registered = map[string]Handler{}
	}
	s.registered[jobType] = h
}
func (s *driverRuntimeBackendStub) StartWorkers(context.Context) error {
	s.startCalls++
	return s.startErr
}

func TestQueueCommon_JobFromAnyAndHelpers(t *testing.T) {
	common := &queueCommon{cfg: Config{DefaultQueue: "default"}}

	if _, err := common.jobFromAny(nil); err == nil {
		t.Fatal("expected nil job error")
	}
	if _, err := common.jobFromAny(NewJob("")); err == nil {
		t.Fatal("expected empty job type error")
	}
	if _, err := common.jobFromAny(struct{ F func() }{}); err == nil {
		t.Fatal("expected marshal error for func field")
	}
	type namedJob struct{}
	if got := jobTypeFromValue(namedJob{}); got != "namedJob" {
		t.Fatalf("expected inferred type namedJob, got %q", got)
	}
	if got := jobTypeFromValue(&namedJob{}); got != "namedJob" {
		t.Fatalf("expected inferred pointer type namedJob, got %q", got)
	}
	if got := jobTypeFromValue(map[string]any{}); got != "" {
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
		t.Fatalf("expected one inferred job dispatch, got %+v", inner.dispatched)
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
	if q.Driver() != DriverNATS {
		t.Fatalf("expected external driver nats, got %q", q.Driver())
	}
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

func TestQueueCommon_PauseResumeStatsUnsupported(t *testing.T) {
	common := &queueCommon{
		inner:  &queueBackendRecorder{},
		driver: DriverNull,
	}
	if err := common.Pause(context.Background(), "default"); !errors.Is(err, ErrPauseUnsupported) {
		t.Fatalf("expected ErrPauseUnsupported, got %v", err)
	}
	if err := common.Resume(context.Background(), "default"); !errors.Is(err, ErrPauseUnsupported) {
		t.Fatalf("expected ErrPauseUnsupported, got %v", err)
	}
	if _, err := common.Stats(context.Background()); err == nil {
		t.Fatal("expected stats unsupported error")
	}
}

func TestRuntimeBusWrappers_NilRegisterAndDispatchCtx(t *testing.T) {
	inner := &queueBackendRecorder{}
	nativeBackend := &runtimeBackendStub{}
	native := &nativeQueueRuntime{
		common:     &queueCommon{inner: inner, cfg: Config{DefaultQueue: "default"}, driver: DriverSync},
		runtime:    nativeBackend,
		registered: map[string]Handler{},
	}
	externalWorker := &runtimeBackendStub{}
	external := &externalQueueRuntime{
		common:     &queueCommon{inner: inner, cfg: Config{DefaultQueue: "default"}, driver: DriverNATS},
		registered: map[string]Handler{},
		worker:     externalWorker,
		started:    true,
	}

	native.BusRegister("job:nil:native", nil)
	external.BusRegister("job:nil:external", nil)
	if _, ok := native.registered["job:nil:native"]; !ok {
		t.Fatal("expected native BusRegister(nil) to store registration")
	}
	if h, ok := externalWorker.registered["job:nil:external"]; !ok || h != nil {
		t.Fatal("expected external BusRegister(nil) to forward nil handler")
	}

	opts := busruntime.JobOptions{
		Queue:     "critical",
		Delay:     10 * time.Millisecond,
		Timeout:   20 * time.Millisecond,
		Retry:     2,
		Backoff:   5 * time.Millisecond,
		UniqueFor: 30 * time.Millisecond,
	}
	if err := native.BusDispatch(context.Background(), "job:native", []byte(`{"n":1}`), opts); err != nil {
		t.Fatalf("native BusDispatch failed: %v", err)
	}
	if err := external.BusDispatch(context.Background(), "job:external", []byte(`{"n":1}`), opts); err != nil {
		t.Fatalf("external BusDispatch failed: %v", err)
	}
	if got := len(inner.dispatched); got != 2 {
		t.Fatalf("expected 2 bus-dispatched jobs recorded, got %d", got)
	}
	for _, job := range inner.dispatched {
		jopts := job.jobOptions()
		if jopts.queueName != "critical" || jopts.timeout == nil || jopts.maxRetry == nil || jopts.backoff == nil {
			t.Fatalf("expected mapped bus job options, got %+v", jopts)
		}
	}
}

func TestDriverAdapters_PauseResumeStats_Branches(t *testing.T) {
	a := driverQueueBackendAdapter{&queueBackendRecorder{}}
	if err := a.Pause(context.Background(), "q"); !errors.Is(err, ErrPauseUnsupported) {
		t.Fatalf("expected unsupported pause, got %v", err)
	}
	if err := a.Resume(context.Background(), "q"); !errors.Is(err, ErrPauseUnsupported) {
		t.Fatalf("expected unsupported resume, got %v", err)
	}
	if _, err := a.Stats(context.Background()); err == nil {
		t.Fatal("expected unsupported stats error")
	}

	supported := &driverQueueBackendStub{
		driver: DriverRedis,
		stats: StatsSnapshot{ByQueue: map[string]QueueCounters{"default": {Pending: 1}}},
	}
	a2 := driverQueueBackendAdapter{supported}
	if err := a2.Pause(context.Background(), "default"); err != nil {
		t.Fatalf("pause: %v", err)
	}
	if err := a2.Resume(context.Background(), "default"); err != nil {
		t.Fatalf("resume: %v", err)
	}
	snap, err := a2.Stats(context.Background())
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if snap.Pending("default") != 1 {
		t.Fatalf("expected pending=1, got %d", snap.Pending("default"))
	}
	if supported.lastQueueArg != "default" {
		t.Fatalf("expected queue arg default, got %q", supported.lastQueueArg)
	}

	ar := driverRuntimeQueueBackendAdapter{&runtimeBackendStub{}}
	if err := ar.Pause(context.Background(), "q"); !errors.Is(err, ErrPauseUnsupported) {
		t.Fatalf("expected unsupported runtime pause, got %v", err)
	}
	if err := ar.Resume(context.Background(), "q"); !errors.Is(err, ErrPauseUnsupported) {
		t.Fatalf("expected unsupported runtime resume, got %v", err)
	}
	if _, err := ar.Stats(context.Background()); err == nil {
		t.Fatal("expected unsupported runtime stats error")
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
}

func TestNewQueueAndNewExternalWorker(t *testing.T) {
	q, err := newRuntime(Config{Driver: DriverSync, DefaultQueue: "critical"})
	if err != nil {
		t.Fatalf("new queue failed: %v", err)
	}
	if q.Driver() != DriverSync {
		t.Fatalf("expected sync driver, got %q", q.Driver())
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
				cfg:    Config{Driver: DriverNATS},
				driver: DriverNATS,
			},
			registered: map[string]Handler{
				"job:nats": func(context.Context, Job) error { return nil },
			},
			newWorker: func(int) (driverWorkerBackend, error) {
				return nil, errors.New("dial failed")
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
