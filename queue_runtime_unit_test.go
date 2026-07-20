package queue

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/goforj/queue/busruntime"
)

type runtimeBackendStub struct {
	registered      map[string]Handler
	startCalls      int
	drainCalls      int
	stopCalls       int
	startErr        error
	stopErr         error
	dispatchEntered chan struct{}
	releaseDispatch chan struct{}
	dispatchOnce    sync.Once
}

type blockingRuntimeBackendStub struct {
	runtimeBackendStub
	startEntered chan struct{}
	releaseStart chan struct{}
	startOnce    sync.Once
}

type blockingReadyRuntimeBackendStub struct {
	runtimeBackendStub
	readyEntered chan struct{}
	releaseReady chan struct{}
	readyOnce    sync.Once
	readyCalls   int
}

type blockingShutdownRuntimeBackendStub struct {
	runtimeBackendStub
	shutdownEntered chan struct{}
	releaseShutdown chan struct{}
	shutdownOnce    sync.Once
}

type phasedShutdownRuntimeBackendStub struct {
	runtimeBackendStub
	drainEntered chan struct{}
	releaseDrain chan struct{}
	drainOnce    sync.Once
}

type strictRegistrationRuntimeBackendStub struct {
	runtimeBackendStub
	registrations map[string]int
}

// Register panics when one worker receives the same pattern twice, matching Asynq ServeMux behavior.
func (s *strictRegistrationRuntimeBackendStub) Register(jobType string, handler Handler) {
	if s.registrations == nil {
		s.registrations = make(map[string]int)
	}
	s.registrations[jobType]++
	if s.registrations[jobType] > 1 {
		panic("duplicate worker registration: " + jobType)
	}
	s.runtimeBackendStub.Register(jobType, handler)
}

// StartWorkers rejects a canceled attempt before accepting a later live retry.
func (s *strictRegistrationRuntimeBackendStub) StartWorkers(ctx context.Context) error {
	s.startCalls++
	return ctx.Err()
}

// Shutdown exposes the worker-drained boundary while deliberately ignoring cancellation like a backend cleanup that already committed.
func (s *blockingShutdownRuntimeBackendStub) Shutdown(context.Context) error {
	s.stopCalls++
	s.shutdownOnce.Do(func() { close(s.shutdownEntered) })
	<-s.releaseShutdown
	return s.stopErr
}

// DrainWorkers exposes the pre-resource-close boundary of a native shutdown.
func (s *phasedShutdownRuntimeBackendStub) DrainWorkers(context.Context) error {
	s.drainOnce.Do(func() { close(s.drainEntered) })
	<-s.releaseDrain
	return nil
}

// Ready exposes a deterministic producer-resource boundary for shutdown lease tests.
func (s *blockingReadyRuntimeBackendStub) Ready(ctx context.Context) error {
	s.readyCalls++
	s.readyOnce.Do(func() { close(s.readyEntered) })
	select {
	case <-s.releaseReady:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// StartWorkers exposes a deterministic startup boundary for lifecycle race tests.
func (s *blockingRuntimeBackendStub) StartWorkers(context.Context) error {
	s.startCalls++
	s.startOnce.Do(func() { close(s.startEntered) })
	<-s.releaseStart
	return s.startErr
}

// waitForRuntimeDraining waits until a shutdown goroutine has crossed the lifecycle gate.
func waitForRuntimeDraining(t *testing.T, draining func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !draining() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for runtime to begin draining")
		}
		time.Sleep(time.Millisecond)
	}
}

func (s *runtimeBackendStub) Driver() Driver { return DriverSync }
func (s *runtimeBackendStub) Dispatch(context.Context, Job) error {
	if s.dispatchEntered != nil {
		s.dispatchOnce.Do(func() { close(s.dispatchEntered) })
		<-s.releaseDispatch
	}
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

// DrainWorkers completes the stub's distinct worker-drain lifecycle phase.
func (s *runtimeBackendStub) DrainWorkers(context.Context) error {
	s.drainCalls++
	return nil
}

func (s *runtimeBackendStub) Shutdown(context.Context) error {
	s.stopCalls++
	return s.stopErr
}

type queueBackendRecorder struct {
	dispatched      []Job
	shutdowns       int
	shutdownErr     error
	dispatchEntered chan struct{}
	releaseDispatch chan struct{}
	dispatchOnce    sync.Once
}

func (q *queueBackendRecorder) Driver() Driver { return DriverNull }
func (q *queueBackendRecorder) Dispatch(_ context.Context, job Job) error {
	q.dispatched = append(q.dispatched, job)
	if q.dispatchEntered != nil {
		q.dispatchOnce.Do(func() { close(q.dispatchEntered) })
		<-q.releaseDispatch
	}
	return nil
}
func (q *queueBackendRecorder) Shutdown(context.Context) error {
	q.shutdowns++
	return q.shutdownErr
}

type driverQueueBackendStub struct {
	driver       Driver
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
	startErr   error
	startCalls int
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

// DrainWorkers completes the driver stub's distinct worker-drain phase.
func (s *driverRuntimeBackendStub) DrainWorkers(context.Context) error {
	return nil
}

func TestQueueCommon_JobFromAnyAndHelpers(t *testing.T) {
	common := &queueCommon{cfg: Config{DefaultQueue: "default"}}

	if _, err := common.jobFromAny(nil); err == nil {
		t.Fatal("expected nil job error")
	}
	if _, err := common.jobFromAny(NewJob("")); err == nil {
		t.Fatal("expected empty job type error")
	}
	if _, err := common.jobFromAny(NewJob("deferred:validation").Retry(-1)); err != nil {
		t.Fatalf("jobFromAny changed backend validation timing: %v", err)
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
	q := &nativeQueueRuntime{
		common:  common,
		runtime: worker,
		nativeQueueRuntimeState: &nativeQueueRuntimeState{
			registered: map[string]Handler{},
		},
	}

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
	if worker.drainCalls != 1 || worker.stopCalls != 0 || inner.shutdowns != 1 {
		t.Fatalf("native drain/runtime close/inner close calls = %d/%d/%d, want 1/0/1", worker.drainCalls, worker.stopCalls, inner.shutdowns)
	}
}

func TestRuntimeWithContextSharesLifecycleState(t *testing.T) {
	native := &nativeQueueRuntime{
		common:  &queueCommon{cfg: Config{DefaultQueue: "default"}},
		runtime: &runtimeBackendStub{},
		nativeQueueRuntimeState: &nativeQueueRuntimeState{
			registered: map[string]Handler{},
		},
	}
	nativeDerived, ok := native.WithContext(context.Background()).(*nativeQueueRuntime)
	if !ok {
		t.Fatal("expected a derived native runtime")
	}
	if nativeDerived.nativeQueueRuntimeState != native.nativeQueueRuntimeState {
		t.Fatal("derived native runtime does not share lifecycle state")
	}
	nativeDerived.Workers(3)
	if native.workers != 3 {
		t.Fatalf("native worker count = %d, want shared value 3", native.workers)
	}

	external := &externalQueueRuntime{
		common: &queueCommon{cfg: Config{DefaultQueue: "default"}},
		externalQueueRuntimeState: &externalQueueRuntimeState{
			registered: map[string]Handler{},
		},
	}
	externalDerived, ok := external.WithContext(context.Background()).(*externalQueueRuntime)
	if !ok {
		t.Fatal("expected a derived external runtime")
	}
	if externalDerived.externalQueueRuntimeState != external.externalQueueRuntimeState {
		t.Fatal("derived external runtime does not share lifecycle state")
	}
	externalDerived.Workers(5)
	if external.workers != 5 {
		t.Fatalf("external worker count = %d, want shared value 5", external.workers)
	}
}

// TestRuntimeEventQueueResolvers verifies every runtime shape exposes the same
// namespace mapping without requiring a live backend.
func TestRuntimeEventQueueResolvers(t *testing.T) {
	common := &queueCommon{cfg: Config{DefaultQueue: "billing_default"}}
	if got := common.physicalQueueNameOrDefault(""); got != "billing_default" {
		t.Fatalf("common default queue = %q, want billing_default", got)
	}
	if got := common.physicalQueueNameOrDefault("critical"); got != "billing_critical" {
		t.Fatalf("common explicit queue = %q, want billing_critical", got)
	}

	native := &nativeQueueRuntime{common: common}
	if got := native.physicalQueueNameOrDefault("critical"); got != "billing_critical" {
		t.Fatalf("native explicit queue = %q, want billing_critical", got)
	}
	if got := (*nativeQueueRuntime)(nil).physicalQueueNameOrDefault(""); got != "default" {
		t.Fatalf("nil native default queue = %q, want default", got)
	}
	external := &externalQueueRuntime{common: common}
	if got := external.physicalQueueNameOrDefault("critical"); got != "billing_critical" {
		t.Fatalf("external explicit queue = %q, want billing_critical", got)
	}
	if got := (*externalQueueRuntime)(nil).physicalQueueNameOrDefault(""); got != "default" {
		t.Fatalf("nil external default queue = %q, want default", got)
	}

	fake := NewFake()
	if got := fake.physicalQueueNameOrDefault(""); got != "default" {
		t.Fatalf("fake default queue = %q, want default", got)
	}
	if got := fake.physicalQueueNameOrDefault("critical"); got != "critical" {
		t.Fatalf("fake explicit queue = %q, want critical", got)
	}
	if got := (*FakeQueue)(nil).physicalQueueNameOrDefault(""); got != "default" {
		t.Fatalf("nil fake default queue = %q, want default", got)
	}
}

func TestPhysicalQueueNameInfersTargetPrefixFromDefaultQueue(t *testing.T) {
	tests := []struct {
		defaultQueue string
		queueName    string
		want         string
	}{
		{defaultQueue: "default", queueName: "default", want: "default"},
		{defaultQueue: "default", queueName: "reports", want: "reports"},
		{defaultQueue: "billing_default", queueName: "default", want: "billing_default"},
		{defaultQueue: "billing_default", queueName: "reports", want: "billing_reports"},
		{defaultQueue: "billing_default", queueName: "billing_reports", want: "billing_reports"},
		{defaultQueue: "critical", queueName: "reports", want: "reports"},
		{defaultQueue: "billing_default", queueName: "", want: "billing_default"},
	}
	for _, tc := range tests {
		if got := PhysicalQueueName(tc.defaultQueue, tc.queueName); got != tc.want {
			t.Fatalf("PhysicalQueueName(%q, %q) = %q, want %q", tc.defaultQueue, tc.queueName, got, tc.want)
		}
	}
}

func TestQueueCommonDispatchPhysicalizesTargetQueues(t *testing.T) {
	inner := &queueBackendRecorder{}
	q := &nativeQueueRuntime{
		common:  &queueCommon{inner: inner, cfg: Config{DefaultQueue: "billing_default"}, driver: DriverSync},
		runtime: &runtimeBackendStub{},
		nativeQueueRuntimeState: &nativeQueueRuntimeState{
			registered: map[string]Handler{},
		},
	}

	if err := q.Dispatch(NewJob("job:explicit").OnQueue("reports")); err != nil {
		t.Fatalf("dispatch explicit queue: %v", err)
	}
	type inferredJob struct{ ID int }
	if err := q.Dispatch(inferredJob{ID: 7}); err != nil {
		t.Fatalf("dispatch inferred job: %v", err)
	}

	if len(inner.dispatched) != 2 {
		t.Fatalf("expected 2 dispatched jobs, got %d", len(inner.dispatched))
	}
	if got := inner.dispatched[0].jobOptions().queueName; got != "billing_reports" {
		t.Fatalf("expected explicit queue billing_reports, got %q", got)
	}
	if got := inner.dispatched[1].jobOptions().queueName; got != "billing_default" {
		t.Fatalf("expected default queue billing_default, got %q", got)
	}
}

func TestExternalQueueRuntimeRegisterShutdownAndWorkers(t *testing.T) {
	inner := &queueBackendRecorder{}
	worker := &runtimeBackendStub{}
	common := &queueCommon{inner: inner, cfg: Config{DefaultQueue: "default"}, driver: DriverNATS}
	q := &externalQueueRuntime{
		common: common,
		externalQueueRuntimeState: &externalQueueRuntimeState{
			registered: map[string]Handler{},
			worker:     worker,
			started:    true,
		},
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
	if err := q.Dispatch(NewJob("job:external").OnQueue("default")); err != nil {
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

// TestRuntimeSameKeyReplacementDuringBlockedStart verifies a completed registration remains current while startup is in flight.
func TestRuntimeSameKeyReplacementDuringBlockedStart(t *testing.T) {
	for _, external := range []bool{false, true} {
		name := "native"
		if external {
			name = "external"
		}
		t.Run(name, func(t *testing.T) {
			worker := &blockingRuntimeBackendStub{
				startEntered: make(chan struct{}),
				releaseStart: make(chan struct{}),
			}
			var runtime queueRuntime
			if external {
				runtime = &externalQueueRuntime{
					common: &queueCommon{inner: &queueBackendRecorder{}, cfg: Config{DefaultQueue: "default"}, driver: DriverSQS},
					newWorker: func(int) (driverWorkerBackend, error) {
						return worker, nil
					},
					externalQueueRuntimeState: &externalQueueRuntimeState{registered: map[string]Handler{}},
				}
			} else {
				runtime = &nativeQueueRuntime{
					common:  &queueCommon{inner: worker, cfg: Config{DefaultQueue: "default"}, driver: DriverSync},
					runtime: worker,
					nativeQueueRuntimeState: &nativeQueueRuntimeState{
						registered: map[string]Handler{},
					},
				}
			}

			var firstCalls, secondCalls int
			runtime.Register("job:replace", func(context.Context, Job) error {
				firstCalls++
				return nil
			})
			startResult := make(chan error, 1)
			go func() { startResult <- runtime.StartWorkers(context.Background()) }()
			<-worker.startEntered
			runtime.Register("job:replace", func(context.Context, Job) error {
				secondCalls++
				return nil
			})
			close(worker.releaseStart)
			if err := <-startResult; err != nil {
				t.Fatalf("start workers: %v", err)
			}
			handler := worker.registered["job:replace"]
			if handler == nil {
				t.Fatal("worker did not receive replacement slot")
			}
			if err := handler(context.Background(), NewJob("job:replace")); err != nil {
				t.Fatalf("invoke replacement: %v", err)
			}
			if firstCalls != 0 || secondCalls != 1 {
				t.Fatalf("replacement calls = first:%d second:%d, want 0/1", firstCalls, secondCalls)
			}
			if err := runtime.Shutdown(context.Background()); err != nil {
				t.Fatalf("shutdown: %v", err)
			}
		})
	}
}

// TestNativeRuntimeShutdownRetainsStateForPublicRetry verifies a failed drain cannot make later cleanup a no-op.
func TestNativeRuntimeShutdownRetainsStateForPublicRetry(t *testing.T) {
	shutdownErr := errors.New("native shutdown timed out")
	backend := &runtimeBackendStub{stopErr: shutdownErr}
	runtime := &nativeQueueRuntime{
		common:  &queueCommon{inner: backend, cfg: Config{DefaultQueue: "default"}, driver: DriverSync},
		runtime: backend,
		nativeQueueRuntimeState: &nativeQueueRuntimeState{
			registered: map[string]Handler{},
			started:    true,
		},
	}
	publicQueue, err := newQueueFromRuntime(runtime)
	if err != nil {
		t.Fatalf("new public queue: %v", err)
	}

	if err := publicQueue.Shutdown(context.Background()); !errors.Is(err, shutdownErr) {
		t.Fatalf("first shutdown error = %v, want %v", err, shutdownErr)
	}
	if !runtime.started || !runtime.draining {
		t.Fatalf("native runtime lost retryable state: started=%t draining=%t", runtime.started, runtime.draining)
	}
	if err := publicQueue.StartWorkers(context.Background()); !errors.Is(err, ErrQueuerShuttingDown) {
		t.Fatalf("start during drain error = %v, want ErrQueuerShuttingDown", err)
	}

	backend.stopErr = nil
	if err := publicQueue.Shutdown(context.Background()); err != nil {
		t.Fatalf("retry shutdown: %v", err)
	}
	if backend.stopCalls != 2 {
		t.Fatalf("native shutdown calls = %d, want 2", backend.stopCalls)
	}
	if runtime.started || runtime.draining {
		t.Fatalf("native runtime remained active: started=%t draining=%t", runtime.started, runtime.draining)
	}
}

// TestExternalRuntimeShutdownRetainsWorkerForPublicRetry verifies worker and producer cleanup preserve their ordering after timeout.
func TestExternalRuntimeShutdownRetainsWorkerForPublicRetry(t *testing.T) {
	shutdownErr := errors.New("worker shutdown timed out")
	inner := &queueBackendRecorder{}
	worker := &runtimeBackendStub{stopErr: shutdownErr}
	runtime := &externalQueueRuntime{
		common: &queueCommon{inner: inner, cfg: Config{DefaultQueue: "default"}, driver: DriverSQS},
		externalQueueRuntimeState: &externalQueueRuntimeState{
			registered: map[string]Handler{},
			worker:     worker,
			started:    true,
		},
	}
	publicQueue, err := newQueueFromRuntime(runtime)
	if err != nil {
		t.Fatalf("new public queue: %v", err)
	}

	if err := publicQueue.Shutdown(context.Background()); !errors.Is(err, shutdownErr) {
		t.Fatalf("first shutdown error = %v, want %v", err, shutdownErr)
	}
	if runtime.worker != worker || !runtime.started || !runtime.draining {
		t.Fatalf("external runtime lost retryable state: worker=%T started=%t draining=%t", runtime.worker, runtime.started, runtime.draining)
	}
	if inner.shutdowns != 0 {
		t.Fatalf("producer shutdowns = %d before worker drain, want 0", inner.shutdowns)
	}
	if err := publicQueue.StartWorkers(context.Background()); !errors.Is(err, ErrQueuerShuttingDown) {
		t.Fatalf("start during drain error = %v, want ErrQueuerShuttingDown", err)
	}

	worker.stopErr = nil
	if err := publicQueue.Shutdown(context.Background()); err != nil {
		t.Fatalf("retry shutdown: %v", err)
	}
	if worker.stopCalls != 2 || inner.shutdowns != 1 {
		t.Fatalf("worker/producer shutdown calls = %d/%d, want 2/1", worker.stopCalls, inner.shutdowns)
	}
	if runtime.worker != nil || runtime.started || runtime.draining {
		t.Fatalf("external runtime retained completed state: worker=%T started=%t draining=%t", runtime.worker, runtime.started, runtime.draining)
	}
}

// TestNativeRuntimeShutdownClosesNeverStartedBackend verifies producer-owned resources do not depend on worker startup.
func TestNativeRuntimeShutdownClosesNeverStartedBackend(t *testing.T) {
	backend := &runtimeBackendStub{}
	runtime := &nativeQueueRuntime{
		common:  &queueCommon{inner: backend, cfg: Config{DefaultQueue: "default"}, driver: DriverDatabase},
		runtime: backend,
		nativeQueueRuntimeState: &nativeQueueRuntimeState{
			registered: map[string]Handler{},
		},
	}
	if err := runtime.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown never-started runtime: %v", err)
	}
	if backend.stopCalls != 1 || !runtime.closed {
		t.Fatalf("backend stops/closed = %d/%t, want 1/true", backend.stopCalls, runtime.closed)
	}
	if err := runtime.Shutdown(context.Background()); err != nil {
		t.Fatalf("idempotent shutdown: %v", err)
	}
	if backend.stopCalls != 1 {
		t.Fatalf("idempotent backend stops = %d, want 1", backend.stopCalls)
	}
	if err := runtime.StartWorkers(context.Background()); !errors.Is(err, ErrQueuerShuttingDown) {
		t.Fatalf("start after shutdown error = %v, want ErrQueuerShuttingDown", err)
	}
	if err := runtime.Dispatch(NewJob("job:closed")); !errors.Is(err, ErrQueuerShuttingDown) {
		t.Fatalf("dispatch after shutdown error = %v, want ErrQueuerShuttingDown", err)
	}
}

// TestExternalRuntimeShutdownLatchesIntentDuringStart verifies a blocked startup cannot admit work after shutdown begins.
func TestExternalRuntimeShutdownLatchesIntentDuringStart(t *testing.T) {
	inner := &queueBackendRecorder{}
	worker := &blockingRuntimeBackendStub{
		startEntered: make(chan struct{}),
		releaseStart: make(chan struct{}),
	}
	var factoryCalls int
	runtime := &externalQueueRuntime{
		common: &queueCommon{inner: inner, cfg: Config{DefaultQueue: "default"}, driver: DriverSQS},
		newWorker: func(int) (driverWorkerBackend, error) {
			factoryCalls++
			return worker, nil
		},
		externalQueueRuntimeState: &externalQueueRuntimeState{registered: map[string]Handler{}},
	}

	startResult := make(chan error, 1)
	go func() { startResult <- runtime.StartWorkers(context.Background()) }()
	<-worker.startEntered
	shutdownResult := make(chan error, 1)
	go func() { shutdownResult <- runtime.Shutdown(context.Background()) }()
	waitForRuntimeDraining(t, func() bool {
		runtime.mu.Lock()
		defer runtime.mu.Unlock()
		return runtime.draining
	})
	if err := runtime.Dispatch(NewJob("job:rejected").OnQueue("default")); !errors.Is(err, ErrQueuerShuttingDown) {
		t.Fatalf("dispatch during startup drain = %v, want ErrQueuerShuttingDown", err)
	}
	if err := runtime.StartWorkers(context.Background()); !errors.Is(err, ErrQueuerShuttingDown) {
		t.Fatalf("fresh start during startup drain = %v, want ErrQueuerShuttingDown", err)
	}
	close(worker.releaseStart)
	if err := <-startResult; err != nil {
		t.Fatalf("original start: %v", err)
	}
	if err := <-shutdownResult; err != nil {
		t.Fatalf("shutdown racing start: %v", err)
	}
	if factoryCalls != 1 || worker.startCalls != 1 || worker.stopCalls != 1 || inner.shutdowns != 1 {
		t.Fatalf("factory/start/stop/producer calls = %d/%d/%d/%d, want 1/1/1/1", factoryCalls, worker.startCalls, worker.stopCalls, inner.shutdowns)
	}
	if runtime.worker != nil || runtime.started || runtime.draining || !runtime.closed {
		t.Fatalf("runtime lifecycle after shutdown = worker:%T started:%t draining:%t closed:%t", runtime.worker, runtime.started, runtime.draining, runtime.closed)
	}
}

// TestNativeRuntimeShutdownLatchesIntentDuringStart verifies native startup uses the same shutdown gate.
func TestNativeRuntimeShutdownLatchesIntentDuringStart(t *testing.T) {
	worker := &blockingRuntimeBackendStub{
		startEntered: make(chan struct{}),
		releaseStart: make(chan struct{}),
	}
	runtime := &nativeQueueRuntime{
		common:  &queueCommon{inner: worker, cfg: Config{DefaultQueue: "default"}, driver: DriverSync},
		runtime: worker,
		nativeQueueRuntimeState: &nativeQueueRuntimeState{
			registered: map[string]Handler{},
		},
	}
	startResult := make(chan error, 1)
	go func() { startResult <- runtime.StartWorkers(context.Background()) }()
	<-worker.startEntered
	shutdownResult := make(chan error, 1)
	go func() { shutdownResult <- runtime.Shutdown(context.Background()) }()
	waitForRuntimeDraining(t, func() bool {
		runtime.mu.Lock()
		defer runtime.mu.Unlock()
		return runtime.draining
	})
	if err := runtime.Dispatch(NewJob("job:rejected")); !errors.Is(err, ErrQueuerShuttingDown) {
		t.Fatalf("dispatch during startup drain = %v, want ErrQueuerShuttingDown", err)
	}
	if err := runtime.StartWorkers(context.Background()); !errors.Is(err, ErrQueuerShuttingDown) {
		t.Fatalf("fresh start during startup drain = %v, want ErrQueuerShuttingDown", err)
	}
	close(worker.releaseStart)
	if err := <-startResult; err != nil {
		t.Fatalf("original start: %v", err)
	}
	if err := <-shutdownResult; err != nil {
		t.Fatalf("shutdown racing start: %v", err)
	}
	if worker.startCalls != 1 || worker.stopCalls != 1 {
		t.Fatalf("native start/stop calls = %d/%d, want 1/1", worker.startCalls, worker.stopCalls)
	}
	if runtime.started || runtime.draining || !runtime.closed {
		t.Fatalf("native lifecycle after shutdown = started:%t draining:%t closed:%t", runtime.started, runtime.draining, runtime.closed)
	}
}

// TestExternalRuntimeRetainsFailedStartWorkerForCleanup verifies partial factory resources remain reachable by Shutdown.
func TestExternalRuntimeRetainsFailedStartWorkerForCleanup(t *testing.T) {
	startErr := errors.New("worker start failed")
	worker := &runtimeBackendStub{startErr: startErr}
	inner := &queueBackendRecorder{}
	runtime := &externalQueueRuntime{
		common: &queueCommon{inner: inner, cfg: Config{DefaultQueue: "default"}, driver: DriverSQS},
		newWorker: func(int) (driverWorkerBackend, error) {
			return worker, nil
		},
		externalQueueRuntimeState: &externalQueueRuntimeState{registered: map[string]Handler{}},
	}
	if err := runtime.StartWorkers(context.Background()); !errors.Is(err, startErr) {
		t.Fatalf("start error = %v, want %v", err, startErr)
	}
	if runtime.worker == nil || runtime.started {
		t.Fatalf("failed-start ownership = worker:%T started:%t", runtime.worker, runtime.started)
	}
	if err := runtime.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown failed-start worker: %v", err)
	}
	if worker.stopCalls != 1 || inner.shutdowns != 1 || runtime.worker != nil || !runtime.closed {
		t.Fatalf("cleanup = worker stops:%d producer stops:%d retained:%T closed:%t", worker.stopCalls, inner.shutdowns, runtime.worker, runtime.closed)
	}
}

// TestExternalRuntimeRetryPreservesSameKeyReplacement verifies a retained worker exposes the latest handler without duplicate registration.
func TestExternalRuntimeRetryPreservesSameKeyReplacement(t *testing.T) {
	worker := &strictRegistrationRuntimeBackendStub{}
	var factoryCalls int
	runtime := &externalQueueRuntime{
		common: &queueCommon{inner: &queueBackendRecorder{}, cfg: Config{DefaultQueue: "default"}, driver: DriverRedis},
		newWorker: func(int) (driverWorkerBackend, error) {
			factoryCalls++
			return worker, nil
		},
		externalQueueRuntimeState: &externalQueueRuntimeState{registered: map[string]Handler{}},
	}
	t.Cleanup(func() {
		if err := runtime.Shutdown(context.Background()); err != nil {
			t.Errorf("shutdown retried runtime: %v", err)
		}
	})
	var firstCalls, secondCalls int
	runtime.Register("job:replace", func(context.Context, Job) error {
		firstCalls++
		return nil
	})
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := runtime.StartWorkers(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled start error = %v, want context.Canceled", err)
	}
	runtime.Register("job:replace", func(context.Context, Job) error {
		secondCalls++
		return nil
	})
	if err := runtime.StartWorkers(context.Background()); err != nil {
		t.Fatalf("retry start: %v", err)
	}
	if factoryCalls != 1 {
		t.Fatalf("worker factory calls = %d, want retained worker reused once", factoryCalls)
	}
	if worker.registrations["job:replace"] != 1 {
		t.Fatalf("worker registrations = %d, want 1", worker.registrations["job:replace"])
	}
	if err := worker.registered["job:replace"](context.Background(), NewJob("job:replace")); err != nil {
		t.Fatalf("invoke replacement: %v", err)
	}
	if firstCalls != 0 || secondCalls != 1 {
		t.Fatalf("replacement calls = first:%d second:%d, want 0/1", firstCalls, secondCalls)
	}
}

// TestExternalRuntimeStartedReplacementUsesOneRegistration verifies strict workers never receive a duplicate pattern after startup.
func TestExternalRuntimeStartedReplacementUsesOneRegistration(t *testing.T) {
	worker := &strictRegistrationRuntimeBackendStub{}
	runtime := &externalQueueRuntime{
		common: &queueCommon{inner: &queueBackendRecorder{}, cfg: Config{DefaultQueue: "default"}, driver: DriverRedis},
		newWorker: func(int) (driverWorkerBackend, error) {
			return worker, nil
		},
		externalQueueRuntimeState: &externalQueueRuntimeState{registered: map[string]Handler{}},
	}
	t.Cleanup(func() {
		if err := runtime.Shutdown(context.Background()); err != nil {
			t.Errorf("shutdown started runtime: %v", err)
		}
	})
	var firstCalls, secondCalls int
	runtime.Register("job:replace", func(context.Context, Job) error {
		firstCalls++
		return nil
	})
	if err := runtime.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers: %v", err)
	}
	runtime.Register("job:replace", func(context.Context, Job) error {
		secondCalls++
		return nil
	})
	if worker.registrations["job:replace"] != 1 {
		t.Fatalf("worker registrations = %d, want 1", worker.registrations["job:replace"])
	}
	if err := worker.registered["job:replace"](context.Background(), NewJob("job:replace")); err != nil {
		t.Fatalf("invoke replacement: %v", err)
	}
	if firstCalls != 0 || secondCalls != 1 {
		t.Fatalf("replacement calls = first:%d second:%d, want 0/1", firstCalls, secondCalls)
	}
}

// TestExternalRuntimeDoesNotRedrainWorkerAfterProducerFailure verifies retry resumes at the incomplete cleanup phase.
func TestExternalRuntimeDoesNotRedrainWorkerAfterProducerFailure(t *testing.T) {
	producerErr := errors.New("producer shutdown failed")
	worker := &runtimeBackendStub{}
	inner := &queueBackendRecorder{shutdownErr: producerErr}
	runtime := &externalQueueRuntime{
		common: &queueCommon{inner: inner, cfg: Config{DefaultQueue: "default"}, driver: DriverSQS},
		externalQueueRuntimeState: &externalQueueRuntimeState{
			registered: map[string]Handler{},
			worker:     worker,
			started:    true,
		},
	}
	if err := runtime.Shutdown(context.Background()); !errors.Is(err, producerErr) {
		t.Fatalf("first shutdown error = %v, want %v", err, producerErr)
	}
	if worker.stopCalls != 1 || runtime.worker != nil || runtime.started || !runtime.draining {
		t.Fatalf("partial cleanup = worker stops:%d retained:%T started:%t draining:%t", worker.stopCalls, runtime.worker, runtime.started, runtime.draining)
	}
	inner.shutdownErr = nil
	if err := runtime.Shutdown(context.Background()); err != nil {
		t.Fatalf("retry producer shutdown: %v", err)
	}
	if worker.stopCalls != 1 || inner.shutdowns != 2 || !runtime.closed {
		t.Fatalf("retry cleanup = worker stops:%d producer stops:%d closed:%t", worker.stopCalls, inner.shutdowns, runtime.closed)
	}
}

// TestRuntimeShutdownWaitsForDispatchLease verifies cleanup honors its deadline without overtaking an accepted producer operation.
func TestRuntimeShutdownWaitsForDispatchLease(t *testing.T) {
	tests := []struct {
		name      string
		construct func(backend *runtimeBackendStub, producer *queueBackendRecorder) queueRuntime
		shutdowns func(backend *runtimeBackendStub, producer *queueBackendRecorder) int
	}{
		{
			name: "native",
			construct: func(backend *runtimeBackendStub, _ *queueBackendRecorder) queueRuntime {
				return &nativeQueueRuntime{
					common:  &queueCommon{inner: backend, cfg: Config{DefaultQueue: "default"}, driver: DriverSync},
					runtime: backend,
					nativeQueueRuntimeState: &nativeQueueRuntimeState{
						registered: map[string]Handler{},
					},
				}
			},
			shutdowns: func(backend *runtimeBackendStub, _ *queueBackendRecorder) int { return backend.stopCalls },
		},
		{
			name: "external",
			construct: func(_ *runtimeBackendStub, producer *queueBackendRecorder) queueRuntime {
				return &externalQueueRuntime{
					common: &queueCommon{inner: producer, cfg: Config{DefaultQueue: "default"}, driver: DriverSQS},
					externalQueueRuntimeState: &externalQueueRuntimeState{
						registered: map[string]Handler{},
					},
				}
			},
			shutdowns: func(_ *runtimeBackendStub, producer *queueBackendRecorder) int { return producer.shutdowns },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			entered := make(chan struct{})
			release := make(chan struct{})
			backend := &runtimeBackendStub{dispatchEntered: entered, releaseDispatch: release}
			producer := &queueBackendRecorder{dispatchEntered: entered, releaseDispatch: release}
			runtime := test.construct(backend, producer)
			dispatchResult := make(chan error, 1)
			go func() { dispatchResult <- runtime.Dispatch(NewJob("job:leased")) }()
			<-entered
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
			defer cancel()
			if err := runtime.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("shutdown error = %v, want deadline exceeded", err)
			}
			if calls := test.shutdowns(backend, producer); calls != 0 {
				t.Fatalf("backend shutdown overtook dispatch: calls=%d", calls)
			}
			close(release)
			if err := <-dispatchResult; err != nil {
				t.Fatalf("dispatch: %v", err)
			}
			if err := runtime.Shutdown(context.Background()); err != nil {
				t.Fatalf("retry shutdown: %v", err)
			}
			if calls := test.shutdowns(backend, producer); calls != 1 {
				t.Fatalf("backend shutdown calls = %d, want 1", calls)
			}
		})
	}
}

// TestExternalRuntimeShutdownWaitsForLateContinuationLease verifies a descendant admitted during worker drain finishes before producer cleanup.
func TestExternalRuntimeShutdownWaitsForLateContinuationLease(t *testing.T) {
	worker := &blockingShutdownRuntimeBackendStub{
		shutdownEntered: make(chan struct{}),
		releaseShutdown: make(chan struct{}),
	}
	producer := &queueBackendRecorder{
		dispatchEntered: make(chan struct{}),
		releaseDispatch: make(chan struct{}),
	}
	runtime := &externalQueueRuntime{
		common: &queueCommon{inner: producer, cfg: Config{DefaultQueue: "default"}, driver: DriverSQS},
		externalQueueRuntimeState: &externalQueueRuntimeState{
			registered:   map[string]Handler{},
			worker:       worker,
			started:      true,
			continuation: busruntime.NewContinuationScope(),
		},
	}

	shutdownCtx, cancelShutdown := context.WithCancel(context.Background())
	shutdownResult := make(chan error, 1)
	go func() { shutdownResult <- runtime.Shutdown(shutdownCtx) }()
	<-worker.shutdownEntered

	continuationCtx, releaseContinuation := runtime.continuationScope().Permit(context.Background())
	dispatchResult := make(chan error, 1)
	go func() {
		dispatchResult <- runtime.WithContext(continuationCtx).Dispatch(NewJob("job:late-continuation"))
	}()
	<-producer.dispatchEntered
	// Handler return expires its permit, but the operation it admitted still owns the producer until Dispatch returns.
	releaseContinuation()
	cancelShutdown()
	close(worker.releaseShutdown)

	if err := <-shutdownResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("shutdown error = %v, want context canceled while continuation is active", err)
	}
	if producer.shutdowns != 0 {
		t.Fatalf("producer shutdown overtook late continuation: calls=%d", producer.shutdowns)
	}

	close(producer.releaseDispatch)
	if err := <-dispatchResult; err != nil {
		t.Fatalf("late continuation dispatch: %v", err)
	}
	if err := runtime.Shutdown(context.Background()); err != nil {
		t.Fatalf("retry shutdown: %v", err)
	}
	if worker.stopCalls != 1 || producer.shutdowns != 1 {
		t.Fatalf("worker/producer shutdown calls = %d/%d, want 1/1", worker.stopCalls, producer.shutdowns)
	}
}

// TestNativeRuntimeShutdownWaitsForLateContinuationBeforeResourceClose verifies
// native cleanup takes a stable post-drain lease snapshot before closing resources.
func TestNativeRuntimeShutdownWaitsForLateContinuationBeforeResourceClose(t *testing.T) {
	backend := &phasedShutdownRuntimeBackendStub{
		runtimeBackendStub: runtimeBackendStub{
			dispatchEntered: make(chan struct{}),
			releaseDispatch: make(chan struct{}),
		},
		drainEntered: make(chan struct{}),
		releaseDrain: make(chan struct{}),
	}
	runtime := &nativeQueueRuntime{
		common:  &queueCommon{inner: backend, cfg: Config{DefaultQueue: "default"}, driver: DriverSync},
		runtime: backend,
		nativeQueueRuntimeState: &nativeQueueRuntimeState{
			registered:   map[string]Handler{},
			started:      true,
			continuation: busruntime.NewContinuationScope(),
		},
	}

	shutdownCtx, cancelShutdown := context.WithCancel(context.Background())
	shutdownResult := make(chan error, 1)
	go func() { shutdownResult <- runtime.Shutdown(shutdownCtx) }()
	<-backend.drainEntered

	continuationCtx, releaseContinuation := runtime.continuationScope().Permit(context.Background())
	dispatchResult := make(chan error, 1)
	go func() {
		dispatchResult <- runtime.WithContext(continuationCtx).Dispatch(NewJob("job:late-native-continuation"))
	}()
	<-backend.dispatchEntered
	// The originating handler can return after admission while the dispatch
	// lease continues to protect the backend resource on its behalf.
	releaseContinuation()
	close(backend.releaseDrain)
	cancelShutdown()

	if err := <-shutdownResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("shutdown error = %v, want context canceled while continuation is active", err)
	}
	if backend.stopCalls != 0 {
		t.Fatalf("resource close overtook late continuation: calls=%d", backend.stopCalls)
	}

	close(backend.releaseDispatch)
	if err := <-dispatchResult; err != nil {
		t.Fatalf("late continuation dispatch: %v", err)
	}
	if err := runtime.Shutdown(context.Background()); err != nil {
		t.Fatalf("retry shutdown: %v", err)
	}
	if backend.stopCalls != 1 {
		t.Fatalf("resource close calls = %d, want 1", backend.stopCalls)
	}
}

// TestRuntimeShutdownWaitsForReadinessLease verifies readiness cannot reopen or outlive producer cleanup.
func TestRuntimeShutdownWaitsForReadinessLease(t *testing.T) {
	for _, external := range []bool{false, true} {
		name := "native"
		if external {
			name = "external"
		}
		t.Run(name, func(t *testing.T) {
			backend := &blockingReadyRuntimeBackendStub{
				readyEntered: make(chan struct{}),
				releaseReady: make(chan struct{}),
			}
			var runtime queueRuntime
			if external {
				runtime = &externalQueueRuntime{
					common: &queueCommon{inner: backend, cfg: Config{DefaultQueue: "default"}, driver: DriverNATS},
					externalQueueRuntimeState: &externalQueueRuntimeState{
						registered: map[string]Handler{},
					},
				}
			} else {
				runtime = &nativeQueueRuntime{
					common:  &queueCommon{inner: backend, cfg: Config{DefaultQueue: "default"}, driver: DriverSync},
					runtime: backend,
					nativeQueueRuntimeState: &nativeQueueRuntimeState{
						registered: map[string]Handler{},
					},
				}
			}
			readyResult := make(chan error, 1)
			go func() { readyResult <- runtime.Ready(context.Background()) }()
			<-backend.readyEntered
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
			if err := runtime.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("shutdown error = %v, want deadline exceeded", err)
			}
			cancel()
			if backend.stopCalls != 0 {
				t.Fatalf("backend shutdown overtook readiness: calls=%d", backend.stopCalls)
			}
			if err := runtime.Ready(context.Background()); !errors.Is(err, ErrQueuerShuttingDown) {
				t.Fatalf("ready during drain = %v, want ErrQueuerShuttingDown", err)
			}
			close(backend.releaseReady)
			if err := <-readyResult; err != nil {
				t.Fatalf("readiness operation: %v", err)
			}
			if err := runtime.Shutdown(context.Background()); err != nil {
				t.Fatalf("retry shutdown: %v", err)
			}
			if err := runtime.Ready(context.Background()); !errors.Is(err, ErrQueuerShuttingDown) {
				t.Fatalf("ready after close = %v, want ErrQueuerShuttingDown", err)
			}
			if backend.readyCalls != 1 {
				t.Fatalf("backend readiness calls = %d, want 1", backend.readyCalls)
			}
		})
	}
}

// TestRuntimeContinuationPermissionIsScopedAndEphemeral verifies foreign or escaped handler contexts cannot bypass a drain.
func TestRuntimeContinuationPermissionIsScopedAndEphemeral(t *testing.T) {
	backend := &runtimeBackendStub{}
	runtime := &nativeQueueRuntime{
		common:  &queueCommon{inner: backend, cfg: Config{DefaultQueue: "default"}, driver: DriverSync},
		runtime: backend,
		nativeQueueRuntimeState: &nativeQueueRuntimeState{
			registered: map[string]Handler{},
			draining:   true,
		},
	}
	foreign := busruntime.NewContinuationScope()
	foreignCtx, releaseForeign := foreign.Permit(context.Background())
	defer releaseForeign()
	if err := runtime.WithContext(foreignCtx).Dispatch(NewJob("job:foreign")); !errors.Is(err, ErrQueuerShuttingDown) {
		t.Fatalf("foreign continuation dispatch = %v, want ErrQueuerShuttingDown", err)
	}

	ownCtx, releaseOwn := runtime.continuationScope().Permit(context.Background())
	if err := runtime.WithContext(ownCtx).Dispatch(NewJob("job:owned")); err != nil {
		t.Fatalf("owned continuation dispatch: %v", err)
	}
	releaseOwn()
	if err := runtime.WithContext(ownCtx).Dispatch(NewJob("job:escaped")); !errors.Is(err, ErrQueuerShuttingDown) {
		t.Fatalf("escaped continuation dispatch = %v, want ErrQueuerShuttingDown", err)
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

func TestRuntimeBusWrappers_NilRegisterAndDispatch(t *testing.T) {
	inner := &queueBackendRecorder{}
	nativeBackend := &runtimeBackendStub{}
	native := &nativeQueueRuntime{
		common:  &queueCommon{inner: inner, cfg: Config{DefaultQueue: "default"}, driver: DriverSync},
		runtime: nativeBackend,
		nativeQueueRuntimeState: &nativeQueueRuntimeState{
			registered: map[string]Handler{},
		},
	}
	externalWorker := &runtimeBackendStub{}
	external := &externalQueueRuntime{
		common: &queueCommon{inner: inner, cfg: Config{DefaultQueue: "default"}, driver: DriverNATS},
		externalQueueRuntimeState: &externalQueueRuntimeState{
			registered: map[string]Handler{},
			worker:     externalWorker,
			started:    true,
		},
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

// TestRuntimeBusRegisterPropagatesDeliveryAttempt verifies native and external adapters expose physical retry metadata to orchestration.
func TestRuntimeBusRegisterPropagatesDeliveryAttempt(t *testing.T) {
	tests := []struct {
		name     string
		register func(string, busruntime.Handler) Handler
	}{
		{
			name: "native",
			register: func(jobType string, handler busruntime.Handler) Handler {
				native := &nativeQueueRuntime{
					common:  &queueCommon{driver: DriverSync},
					runtime: &runtimeBackendStub{},
					nativeQueueRuntimeState: &nativeQueueRuntimeState{
						registered: map[string]Handler{},
					},
				}
				native.BusRegister(jobType, handler)
				return native.registered[jobType]
			},
		},
		{
			name: "external",
			register: func(jobType string, handler busruntime.Handler) Handler {
				external := &externalQueueRuntime{
					common: &queueCommon{driver: DriverNATS},
					externalQueueRuntimeState: &externalQueueRuntimeState{
						registered: map[string]Handler{},
					},
				}
				external.BusRegister(jobType, handler)
				return external.registered[jobType]
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			const jobType = "job:attempt"
			var got busruntime.DeliveryAttempt
			var ok bool
			handler := test.register(jobType, func(ctx context.Context, _ busruntime.InboundJob) error {
				got, ok = busruntime.DeliveryAttemptFromContext(ctx)
				return nil
			})
			if handler == nil {
				t.Fatal("bus handler was not registered")
			}
			job := DriverWithAttempt(NewJob(jobType).Retry(4), 2)
			if err := handler(context.Background(), job); err != nil {
				t.Fatalf("invoke bus handler: %v", err)
			}
			want := busruntime.DeliveryAttempt{Number: 2, MaxRetry: 4}
			if !ok || got != want {
				t.Fatalf("delivery attempt = %+v, %t; want %+v, true", got, ok, want)
			}
		})
	}
}

func TestRuntimeBusDispatchPhysicalizesTargetQueue(t *testing.T) {
	inner := &queueBackendRecorder{}
	native := &nativeQueueRuntime{
		common:  &queueCommon{inner: inner, cfg: Config{DefaultQueue: "billing_default"}, driver: DriverSync},
		runtime: &runtimeBackendStub{},
		nativeQueueRuntimeState: &nativeQueueRuntimeState{
			registered: map[string]Handler{},
		},
	}

	if err := native.BusDispatch(context.Background(), "job:native", []byte(`{"n":1}`), busruntime.JobOptions{Queue: "reports"}); err != nil {
		t.Fatalf("BusDispatch failed: %v", err)
	}
	if len(inner.dispatched) != 1 {
		t.Fatalf("expected one dispatched job, got %d", len(inner.dispatched))
	}
	if got := inner.dispatched[0].jobOptions().queueName; got != "billing_reports" {
		t.Fatalf("expected bus queue billing_reports, got %q", got)
	}
}

// TestRuntimeBusDispatchPreservesZeroRetry verifies backend defaults cannot replace workflow policy.
func TestRuntimeBusDispatchPreservesZeroRetry(t *testing.T) {
	inner := &queueBackendRecorder{}
	native := &nativeQueueRuntime{
		common:  &queueCommon{inner: inner, cfg: Config{DefaultQueue: "default"}, driver: DriverSync},
		runtime: &runtimeBackendStub{},
		nativeQueueRuntimeState: &nativeQueueRuntimeState{
			registered: map[string]Handler{},
		},
	}
	if err := native.BusDispatch(context.Background(), "bus:job", []byte(`{"schema_version":1}`), busruntime.JobOptions{}); err != nil {
		t.Fatalf("BusDispatch failed: %v", err)
	}
	if len(inner.dispatched) != 1 {
		t.Fatalf("dispatched jobs = %d, want 1", len(inner.dispatched))
	}
	maxRetry := inner.dispatched[0].jobOptions().maxRetry
	if maxRetry == nil || *maxRetry != 0 {
		t.Fatalf("max retry = %v, want explicit zero", maxRetry)
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
		stats:  StatsSnapshot{ByQueue: map[string]QueueCounters{"default": {Pending: 1}}},
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
	q := &externalQueueRuntime{
		common: common,
		externalQueueRuntimeState: &externalQueueRuntimeState{
			registered: map[string]Handler{},
		},
	}

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
		common:  &queueCommon{inner: inner, cfg: Config{DefaultQueue: "default"}, driver: DriverSync},
		runtime: worker,
		nativeQueueRuntimeState: &nativeQueueRuntimeState{
			registered: map[string]Handler{"job:one": func(context.Context, Job) error { return nil }},
		},
	}
	if err := q.StartWorkers(context.Background()); err == nil {
		t.Fatal("expected start workers error")
	}
	if q.started {
		t.Fatal("expected runtime to remain not started on error")
	}
}

// TestQueueCommonWrapRegisteredHandlerPreservesContextOnNilDecoration verifies a decorator can decline replacement without erasing the original context.
func TestQueueCommonWrapRegisteredHandlerPreservesContextOnNilDecoration(t *testing.T) {
	type contextKey struct{}
	key := contextKey{}
	const want = "original"

	for _, withObserver := range []bool{false, true} {
		name := "without observer"
		if withObserver {
			name = "with observer"
		}
		t.Run(name, func(t *testing.T) {
			original := context.WithValue(context.Background(), key, want)
			var observed int
			observer := ensureObserverSink(nil)
			if withObserver {
				observer = ensureObserverSink(ObserverFunc(func(ctx context.Context, event Event) {
					if event.Kind != EventProcessStarted && event.Kind != EventProcessSucceeded {
						return
					}
					observed++
					if ctx != original {
						t.Errorf("observer context changed after nil decorator result")
					}
				}))
			}
			decoratorCalls := 0
			common := &queueCommon{
				cfg: Config{Driver: DriverSync, Observer: observer},
				handlerContextDecorator: func(context.Context) context.Context {
					decoratorCalls++
					return nil
				},
			}
			handlerCalls := 0
			wrapped := common.wrapRegisteredHandler("job:nil-decoration", func(ctx context.Context, _ Job) error {
				handlerCalls++
				if ctx != original {
					t.Error("handler context changed after nil decorator result")
				}
				if got, _ := ctx.Value(key).(string); got != want {
					t.Errorf("handler context value = %q, want %q", got, want)
				}
				return nil
			})
			if err := wrapped(original, NewJob("job:nil-decoration")); err != nil {
				t.Fatalf("wrapped handler: %v", err)
			}
			if decoratorCalls != 1 || handlerCalls != 1 {
				t.Fatalf("decorator/handler calls = %d/%d, want 1/1", decoratorCalls, handlerCalls)
			}
			wantObserved := 0
			if withObserver {
				wantObserved = 2
			}
			if observed != wantObserved {
				t.Fatalf("observed process events = %d, want %d", observed, wantObserved)
			}
		})
	}

	common := &queueCommon{cfg: Config{Driver: DriverSync}}
	if got := common.wrapRegisteredHandler("job:nil-handler", nil); got != nil {
		t.Fatal("expected nil handler to remain nil")
	}
}

// TestRuntimeHandlerContextDecoratorNativeExternalParity verifies both runtime registration paths decorate handlers regardless of observer recipients.
func TestRuntimeHandlerContextDecoratorNativeExternalParity(t *testing.T) {
	type contextKey struct{}
	key := contextKey{}
	const want = "decorated"

	for _, runtimeShape := range []string{"native", "external"} {
		for _, withObserver := range []bool{false, true} {
			name := runtimeShape + "/without observer"
			if withObserver {
				name = runtimeShape + "/with observer"
			}
			t.Run(name, func(t *testing.T) {
				backend := &runtimeBackendStub{}
				var observed int
				observer := ensureObserverSink(nil)
				if withObserver {
					observer = ensureObserverSink(ObserverFunc(func(ctx context.Context, event Event) {
						if event.Kind != EventProcessStarted && event.Kind != EventProcessSucceeded {
							return
						}
						observed++
						if got, _ := ctx.Value(key).(string); got != want {
							t.Errorf("observer context value = %q, want %q", got, want)
						}
					}))
				}

				driver := DriverSync
				common := &queueCommon{
					inner:  backend,
					cfg:    Config{Driver: driver, DefaultQueue: "default", Observer: observer},
					driver: driver,
				}
				var runtime queueRuntime = &nativeQueueRuntime{
					common:  common,
					runtime: backend,
					nativeQueueRuntimeState: &nativeQueueRuntimeState{
						registered: map[string]Handler{},
					},
				}
				if runtimeShape == "external" {
					driver = DriverSQS
					common.inner = &queueBackendRecorder{}
					common.cfg.Driver = driver
					common.driver = driver
					runtime = &externalQueueRuntime{
						common: common,
						newWorker: func(int) (driverWorkerBackend, error) {
							return backend, nil
						},
						externalQueueRuntimeState: &externalQueueRuntimeState{
							registered: map[string]Handler{},
						},
					}
				}

				decoratorCalls := 0
				runtime.setHandlerContextDecorator(func(ctx context.Context) context.Context {
					decoratorCalls++
					return context.WithValue(ctx, key, want)
				})
				handlerCalls := 0
				runtime.Register("job:parity", func(ctx context.Context, _ Job) error {
					handlerCalls++
					if got, _ := ctx.Value(key).(string); got != want {
						t.Errorf("handler context value = %q, want %q", got, want)
					}
					return nil
				})
				if err := runtime.StartWorkers(context.Background()); err != nil {
					t.Fatalf("start workers: %v", err)
				}
				registered := backend.registered["job:parity"]
				if registered == nil {
					t.Fatal("backend did not receive registered handler")
				}
				if err := registered(context.Background(), NewJob("job:parity")); err != nil {
					t.Fatalf("registered handler: %v", err)
				}
				if err := runtime.Shutdown(context.Background()); err != nil {
					t.Fatalf("shutdown runtime: %v", err)
				}

				if decoratorCalls != 1 || handlerCalls != 1 {
					t.Fatalf("decorator/handler calls = %d/%d, want 1/1", decoratorCalls, handlerCalls)
				}
				wantObserved := 0
				if withObserver {
					wantObserved = 2
				}
				if observed != wantObserved {
					t.Fatalf("observed process events = %d, want %d", observed, wantObserved)
				}
			})
		}
	}
}

// TestQueueCommonWrapRegisteredHandlerDefersRedisDecoration verifies the shared wrapper leaves Redis's native handler boundary untouched.
func TestQueueCommonWrapRegisteredHandlerDefersRedisDecoration(t *testing.T) {
	decoratorCalls := 0
	observerCalls := 0
	common := &queueCommon{
		cfg: Config{
			Driver: DriverRedis,
			Observer: ObserverFunc(func(context.Context, Event) {
				observerCalls++
			}),
		},
		handlerContextDecorator: func(ctx context.Context) context.Context {
			decoratorCalls++
			return context.WithValue(ctx, "decorated", true)
		},
	}
	handlerCalls := 0
	wrapped := common.wrapRegisteredHandler("job:redis", func(ctx context.Context, _ Job) error {
		handlerCalls++
		if ctx.Value("decorated") != nil {
			t.Fatal("shared wrapper decorated Redis handler context")
		}
		return nil
	})
	if err := wrapped(context.Background(), NewJob("job:redis")); err != nil {
		t.Fatalf("wrapped Redis handler: %v", err)
	}
	if decoratorCalls != 0 || observerCalls != 0 || handlerCalls != 1 {
		t.Fatalf("decorator/observer/handler calls = %d/%d/%d, want 0/0/1", decoratorCalls, observerCalls, handlerCalls)
	}
}

func TestWorkersSetOnlyBeforeStartNative(t *testing.T) {
	q := &nativeQueueRuntime{
		common:  &queueCommon{cfg: Config{}},
		runtime: &runtimeBackendStub{},
		nativeQueueRuntimeState: &nativeQueueRuntimeState{
			registered: map[string]Handler{},
		},
	}
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
			common: &queueCommon{inner: &queueBackendRecorder{}, cfg: Config{Driver: Driver("unknown")}, driver: Driver("unknown")},
			externalQueueRuntimeState: &externalQueueRuntimeState{
				registered: map[string]Handler{},
			},
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
			newWorker: func(int) (driverWorkerBackend, error) {
				return nil, errors.New("dial failed")
			},
			externalQueueRuntimeState: &externalQueueRuntimeState{
				registered: map[string]Handler{
					"job:nats": func(context.Context, Job) error { return nil },
				},
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
