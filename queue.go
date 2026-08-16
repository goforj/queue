package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/goforj/queue/busruntime"
)

type queueRuntime interface {
	// Driver returns the active queue driver.
	// @group Driver Integration
	Driver() Driver

	// WithContext returns a derived queue runtime handle bound to ctx.
	// @group Driver Integration
	WithContext(ctx context.Context) queueRuntime

	// Dispatch submits a typed job payload using the default queue.
	// @group Driver Integration
	Dispatch(job any) error

	// Register associates a handler with a job type.
	// @group Driver Integration
	Register(jobType string, handler Handler)

	// StartWorkers starts worker execution.
	// @group Driver Integration
	StartWorkers(ctx context.Context) error

	// PauseWorkers stops new worker intake after active handlers finish.
	// @group Driver Integration
	PauseWorkers(ctx context.Context) error

	// ResumeWorkers restarts worker intake after a pause.
	// @group Driver Integration
	ResumeWorkers(ctx context.Context) error

	// Workers sets desired worker concurrency before StartWorkers.
	// @group Driver Integration
	Workers(count int) queueRuntime

	// Shutdown drains running work and releases resources.
	// @group Driver Integration
	Shutdown(ctx context.Context) error

	// Ready checks backend readiness for dispatch/worker operation.
	// @group Driver Integration
	Ready(ctx context.Context) error

	// physicalQueueNameOrDefault resolves the effective backend queue name used in canonical events.
	physicalQueueNameOrDefault(queueName string) string

	// setHandlerContextDecorator decorates handler execution context at registration time.
	setHandlerContextDecorator(func(context.Context) context.Context)
}

// WorkerpoolConfig configures the in-memory workerpool q.
// @group Config
type WorkerpoolConfig struct {
	Workers           int
	QueueCapacity     int
	DefaultJobTimeout time.Duration
}

func (c WorkerpoolConfig) normalize() WorkerpoolConfig {
	c.Workers = defaultWorkerCount(c.Workers)
	if c.QueueCapacity <= 0 {
		c.QueueCapacity = c.Workers
	}
	return c
}

// Config configures queue creation for New (and advanced driver/runtime interop).
// @group Config
type Config struct {
	Driver Driver
	Logger Logger

	DefaultQueue string
}

type queueBackend interface {
	Driver() Driver
	Dispatch(ctx context.Context, job Job) error
	Shutdown(ctx context.Context) error
}

type runtimeQueueBackend interface {
	queueBackend
	Register(jobType string, handler Handler)
	StartWorkers(ctx context.Context) error
	DrainWorkers(ctx context.Context) error
}

// workerLifecycleBackend supports pausing consumers without closing producer resources.
type workerLifecycleBackend interface {
	PauseWorkers(ctx context.Context) error
	ResumeWorkers(ctx context.Context) error
}

func newSyncQueue() queueBackend {
	return newLocalQueueWithConfig(DriverSync, WorkerpoolConfig{})
}

// New creates the high-level Queue API based on Config.Driver.
// @group Constructors
//
// Example: create a queue and dispatch a workflow-capable job
//
//	q, err := queue.New(queue.Config{Driver: queue.DriverWorkerpool})
//	if err != nil {
//		return
//	}
//	type EmailPayload struct {
//		ID int `json:"id"`
//	}
//	q.Register("emails:send", func(ctx context.Context, m queue.Message) error {
//		var payload EmailPayload
//		if err := m.Bind(&payload); err != nil {
//			return err
//		}
//		_ = payload
//		return nil
//	})
//	_ = q.WithWorkers(1).StartWorkers(context.Background()) // optional; default: runtime.NumCPU() (min 1)
//	defer q.Shutdown(context.Background())
//	_, _ = q.Dispatch(
//		queue.NewJob("emails:send").
//			Payload(EmailPayload{ID: 1}).
//			OnQueue("default"),
//	)
func New(cfg Config, opts ...Option) (*Queue, error) {
	return newHighLevelQueue(cfg, opts...)
}

func newRuntime(cfg Config) (queueRuntime, error) {
	return newRuntimeWithObserver(cfg, nil)
}

// newRuntimeWithObserver builds a root runtime with an internal observer sink for focused runtime tests.
func newRuntimeWithObserver(cfg Config, observer Observer) (queueRuntime, error) {
	cfg = cfg.normalize()
	observer = ensureObserverSink(observer)

	var q queueBackend
	var err error
	switch cfg.Driver {
	case DriverNull:
		q = newNullQueue()
	case DriverSync:
		q = newSyncQueue()
	case DriverWorkerpool:
		q = newLocalQueueWithConfig(DriverWorkerpool, WorkerpoolConfig{})
	case DriverDatabase:
		return nil, optionalDriverMovedError(cfg.Driver)
	case DriverRedis:
		return nil, optionalDriverMovedError(cfg.Driver)
	case DriverNATS:
		return nil, optionalDriverMovedError(cfg.Driver)
	case DriverSQS:
		return nil, optionalDriverMovedError(cfg.Driver)
	case DriverRabbitMQ:
		return nil, optionalDriverMovedError(cfg.Driver)
	default:
		return nil, fmt.Errorf("unsupported queue driver %q", cfg.Driver)
	}
	if err != nil {
		return nil, err
	}
	var runtime runtimeQueueBackend
	if native, ok := q.(runtimeQueueBackend); ok {
		runtime = native
	}
	common := &queueCommon{
		inner:        newObservedQueue(q, cfg.Driver, observer),
		cfg:          cfg,
		driver:       cfg.Driver,
		observerSink: observer,
	}
	if runtime != nil {
		return &nativeQueueRuntime{
			common:  common,
			runtime: runtime,
			nativeQueueRuntimeState: &nativeQueueRuntimeState{
				registered:   make(map[string]Handler),
				continuation: busruntime.NewContinuationScope(),
			},
		}, nil
	}
	return &externalQueueRuntime{
		common: common,
		externalQueueRuntimeState: &externalQueueRuntimeState{
			registered:   make(map[string]Handler),
			continuation: busruntime.NewContinuationScope(),
		},
	}, nil
}

func (cfg Config) normalize() Config {
	if cfg.DefaultQueue == "" {
		cfg.DefaultQueue = "default"
	}
	return cfg
}

type queueCommon struct {
	inner                   queueBackend
	cfg                     Config
	driver                  Driver
	observerSink            Observer
	ctx                     context.Context
	handlerContextDecorator func(context.Context) context.Context
}

type nativeQueueRuntime struct {
	common  *queueCommon
	runtime runtimeQueueBackend
	*nativeQueueRuntimeState
}

// nativeQueueRuntimeState stays shared by context-bound handles because worker registration and lifecycle belong to the runtime, not an individual dispatch context.
type nativeQueueRuntimeState struct {
	mu                   sync.Mutex
	registered           map[string]Handler
	handlerSlots         map[string]*runtimeHandlerSlot
	runtimeRegistrations map[string]struct{}
	started              bool
	paused               bool
	draining             bool
	closed               bool
	start                *runtimeStartAttempt
	pause                *runtimePauseAttempt
	shutdown             *runtimeShutdownAttempt
	operations           runtimeOperationState
	continuation         *busruntime.ContinuationScope
	workers              int
}

type externalQueueRuntime struct {
	common    *queueCommon
	newWorker driverWorkerFactory
	*externalQueueRuntimeState
}

// externalQueueRuntimeState keeps the constructed worker and lifecycle state synchronized across derived queue handles.
type externalQueueRuntimeState struct {
	mu                  sync.Mutex
	registered          map[string]Handler
	handlerSlots        map[string]*runtimeHandlerSlot
	worker              runtimeWorkerBackend
	workerRegistrations map[string]struct{}
	started             bool
	paused              bool
	draining            bool
	closed              bool
	start               *runtimeStartAttempt
	pause               *runtimePauseAttempt
	shutdown            *runtimeShutdownAttempt
	operations          runtimeOperationState
	continuation        *busruntime.ContinuationScope
	workers             int
}

type runtimeOperationState struct {
	active int
	idle   chan struct{}
}

// acquire reserves backend resources while the owning lifecycle mutex is held.
func (s *runtimeOperationState) acquire() {
	if s.active == 0 {
		s.idle = make(chan struct{})
	}
	s.active++
}

// release returns true when the final operation completed and an idle waiter should be released.
func (s *runtimeOperationState) release() bool {
	s.active--
	return s.active == 0 && s.idle != nil
}

// markIdle closes the current idle generation after release identifies the final operation.
func (s *runtimeOperationState) markIdle() {
	close(s.idle)
	s.idle = nil
}

type runtimeShutdownAttempt struct {
	done chan struct{}
	err  error
}

type runtimeStartAttempt struct {
	done chan struct{}
	err  error
}

// runtimePauseAttempt lets concurrent lifecycle callers observe one consumer pause.
type runtimePauseAttempt struct {
	done chan struct{}
	err  error
}

type runtimeHandlerSlot struct {
	mu      sync.RWMutex
	handler Handler
}

// replace changes the application handler behind one stable backend registration.
func (s *runtimeHandlerSlot) replace(handler Handler) {
	s.mu.Lock()
	s.handler = handler
	s.mu.Unlock()
}

// invoke resolves the latest handler without holding the slot lock during application execution.
func (s *runtimeHandlerSlot) invoke(ctx context.Context, job Job) error {
	s.mu.RLock()
	handler := s.handler
	s.mu.RUnlock()
	return handler(ctx, job)
}

// updateRuntimeHandlerSlot creates or updates the stable target used for one non-nil job registration.
func updateRuntimeHandlerSlot(slots map[string]*runtimeHandlerSlot, jobType string, handler Handler) (map[string]*runtimeHandlerSlot, *runtimeHandlerSlot) {
	if handler == nil {
		return slots, nil
	}
	if slots == nil {
		slots = make(map[string]*runtimeHandlerSlot)
	}
	slot := slots[jobType]
	if slot == nil {
		slot = &runtimeHandlerSlot{}
		slots[jobType] = slot
	}
	slot.replace(handler)
	return slots, slot
}

// installRuntimeHandler installs one stable trampoline per non-nil job type on a backend.
func installRuntimeHandler(backend interface{ Register(string, Handler) }, common *queueCommon, registrations map[string]struct{}, jobType string, handler Handler, slot *runtimeHandlerSlot) map[string]struct{} {
	if handler == nil {
		backend.Register(jobType, nil)
		return registrations
	}
	if _, installed := registrations[jobType]; installed {
		return registrations
	}
	backend.Register(jobType, common.wrapRegisteredHandler(jobType, slot.invoke))
	if registrations == nil {
		registrations = make(map[string]struct{})
	}
	registrations[jobType] = struct{}{}
	return registrations
}

type runtimeWorkerBackend interface {
	Register(jobType string, handler Handler)
	StartWorkers(ctx context.Context) error
	Shutdown(ctx context.Context) error
}

type runtimeWorkerContextDecoratorSetter interface {
	SetHandlerContextDecorator(func(context.Context) context.Context)
}

func (q *queueCommon) Driver() Driver {
	return q.driver
}

func (q *queueCommon) context() context.Context {
	if q == nil || q.ctx == nil {
		return context.Background()
	}
	return q.ctx
}

// addObserver composes observers at construction time so queue and workflow layers publish to the same application sink.
func (q *queueCommon) addObserver(observer Observer) {
	if q == nil || observer == nil {
		return
	}
	q.observerSink = addObserverToSink(q.observerSink, observer)
	if observed, ok := q.inner.(*observedQueue); ok {
		observed.observer = q.observerSink
		return
	}
	q.inner = newObservedQueue(q.inner, q.driver, q.observerSink)
}

// observer returns the composed application observer shared by execution and workflow adapters.
func (q *queueCommon) observer() Observer {
	if q == nil || !observerHasRecipients(q.observerSink) {
		return nil
	}
	return q.observerSink
}

func (q *queueCommon) WithContext(ctx context.Context) *queueCommon {
	if q == nil {
		return nil
	}
	clone := *q
	clone.ctx = ctx
	return &clone
}

func (q *queueCommon) setHandlerContextDecorator(fn func(context.Context) context.Context) {
	if q == nil {
		return
	}
	q.handlerContextDecorator = fn
}

func (q *queueCommon) Dispatch(job any) error {
	dispatchJob, err := q.jobFromAny(job)
	if err != nil {
		return err
	}
	dispatchJob = q.physicalJob(dispatchJob)
	ctx, _ := newDispatchAcceptance(q.context())
	return q.inner.Dispatch(ctx, dispatchJob)
}

// physicalJob namespaces explicit targets while preserving the current
// backend-specific contract for jobs that omit a queue.
func (q *queueCommon) physicalJob(job Job) Job {
	if job.options.queueName == "" {
		return job
	}
	job.options.queueName = q.physicalQueueName(job.options.queueName)
	return job
}

func (q *queueCommon) physicalQueueName(queueName string) string {
	if q == nil {
		return PhysicalQueueName("", queueName)
	}
	return PhysicalQueueName(q.cfg.DefaultQueue, queueName)
}

// physicalQueueNameOrDefault resolves the configured default and namespace before a queue name reaches the backend.
func (q *queueCommon) physicalQueueNameOrDefault(queueName string) string {
	queueName = strings.TrimSpace(queueName)
	if queueName == "" && q != nil {
		queueName = q.cfg.DefaultQueue
	}
	return q.physicalQueueName(queueName)
}

// PhysicalQueueName maps a logical queue name into the physical name used by the backing queue driver.
func PhysicalQueueName(defaultQueue string, queueName string) string {
	defaultQueue = strings.TrimSpace(defaultQueue)
	queueName = strings.TrimSpace(queueName)
	if queueName == "" {
		return defaultQueue
	}
	prefix := queueNamePrefix(defaultQueue)
	if prefix == "" {
		return queueName
	}
	if strings.HasPrefix(queueName, prefix) {
		return queueName
	}
	return prefix + queueName
}

// PhysicalQueueWeights maps logical weighted queue names into their physical backend names.
func PhysicalQueueWeights(defaultQueue string, weights map[string]int) map[string]int {
	if len(weights) == 0 {
		return weights
	}
	out := make(map[string]int, len(weights))
	for queueName, weight := range weights {
		out[PhysicalQueueName(defaultQueue, queueName)] = weight
	}
	return out
}

func queueNamePrefix(defaultQueue string) string {
	defaultQueue = strings.TrimSpace(defaultQueue)
	const suffix = "_default"
	if !strings.HasSuffix(defaultQueue, suffix) {
		return ""
	}
	prefix := strings.TrimSuffix(defaultQueue, suffix)
	if prefix == "" {
		return ""
	}
	return prefix + "_"
}

// Driver returns the native runtime's configured backend identifier.
func (q *nativeQueueRuntime) Driver() Driver { return q.common.Driver() }

// physicalQueueNameOrDefault keeps canonical event labels aligned with native backend queue names.
func (q *nativeQueueRuntime) physicalQueueNameOrDefault(queueName string) string {
	if q == nil || q.common == nil {
		return PhysicalQueueName("default", queueName)
	}
	return q.common.physicalQueueNameOrDefault(queueName)
}

// Dispatch rejects new application work once native runtime draining begins.
func (q *nativeQueueRuntime) Dispatch(job any) error {
	release, err := q.acquireOperation(q.common.context(), true)
	if err != nil {
		return err
	}
	defer release()
	return q.common.Dispatch(job)
}
func (q *nativeQueueRuntime) WithContext(ctx context.Context) queueRuntime {
	if q == nil {
		return nil
	}
	clone := *q
	clone.common = q.common.WithContext(ctx)
	return &clone
}

// Driver returns the external runtime's configured backend identifier.
func (q *externalQueueRuntime) Driver() Driver { return q.common.Driver() }

// physicalQueueNameOrDefault keeps canonical event labels aligned with external backend queue names.
func (q *externalQueueRuntime) physicalQueueNameOrDefault(queueName string) string {
	if q == nil || q.common == nil {
		return PhysicalQueueName("default", queueName)
	}
	return q.common.physicalQueueNameOrDefault(queueName)
}

// Dispatch rejects new application work once external runtime draining begins.
func (q *externalQueueRuntime) Dispatch(job any) error {
	release, err := q.acquireOperation(q.common.context(), true)
	if err != nil {
		return err
	}
	defer release()
	return q.common.Dispatch(job)
}
func (q *externalQueueRuntime) WithContext(ctx context.Context) queueRuntime {
	if q == nil {
		return nil
	}
	clone := *q
	clone.common = q.common.WithContext(ctx)
	return &clone
}

func (q *nativeQueueRuntime) setHandlerContextDecorator(fn func(context.Context) context.Context) {
	if q == nil {
		return
	}
	q.common.setHandlerContextDecorator(fn)
}

func (q *externalQueueRuntime) setHandlerContextDecorator(fn func(context.Context) context.Context) {
	if q == nil {
		return
	}
	q.common.setHandlerContextDecorator(fn)
}

func (q *nativeQueueRuntime) BusRegister(jobType string, handler busruntime.Handler) {
	if handler == nil {
		q.Register(jobType, nil)
		return
	}
	scope := q.continuationScope()
	q.Register(jobType, func(ctx context.Context, job Job) error {
		handlerCtx, release := withBusDeliveryContext(ctx, job, scope)
		defer release()
		return handler(handlerCtx, job)
	})
}

func (q *externalQueueRuntime) BusRegister(jobType string, handler busruntime.Handler) {
	if handler == nil {
		q.Register(jobType, nil)
		return
	}
	scope := q.continuationScope()
	q.Register(jobType, func(ctx context.Context, job Job) error {
		handlerCtx, release := withBusDeliveryContext(ctx, job, scope)
		defer release()
		return handler(handlerCtx, job)
	})
}

// withBusDeliveryContext attaches physical attempt and correlation metadata to
// one invocation while keeping both channels out of the application payload.
func withBusDeliveryContext(ctx context.Context, job Job, scope *busruntime.ContinuationScope) (context.Context, func()) {
	if ctx == nil {
		ctx = context.Background()
	}
	opts := job.jobOptions()
	ctx, release := scope.Permit(ctx)
	metadata := DriverMetadata(job)
	// Every physical invocation shadows parent metadata so nested legacy or
	// low-level jobs cannot inherit correlation from the job that dispatched them.
	ctx = busruntime.WithDeliveryMetadata(ctx, metadata)
	return busruntime.WithDeliveryAttempt(ctx, busruntime.DeliveryAttempt{
		Number:   opts.attempt,
		MaxRetry: optionInt(opts.maxRetry),
	}), release
}

func (q *nativeQueueRuntime) BusDispatch(ctx context.Context, jobType string, payload []byte, opts busruntime.JobOptions) error {
	release, err := q.acquireOperation(ctx, true)
	if err != nil {
		return err
	}
	defer release()
	return q.common.dispatchBusJob(ctx, jobType, payload, opts)
}

func (q *externalQueueRuntime) BusDispatch(ctx context.Context, jobType string, payload []byte, opts busruntime.JobOptions) error {
	release, err := q.acquireOperation(ctx, true)
	if err != nil {
		return err
	}
	defer release()
	return q.common.dispatchBusJob(ctx, jobType, payload, opts)
}

// BusDispatchDirect submits an ordinary application job without a workflow envelope.
func (q *nativeQueueRuntime) BusDispatchDirect(ctx context.Context, jobType string, payload []byte, metadata busruntime.DeliveryMetadata, opts busruntime.JobOptions) error {
	release, err := q.acquireOperation(ctx, true)
	if err != nil {
		return err
	}
	defer release()
	return q.common.dispatchDirectJob(ctx, jobType, payload, metadata, opts)
}

// BusDispatchDirect submits an ordinary application job without a workflow envelope.
func (q *externalQueueRuntime) BusDispatchDirect(ctx context.Context, jobType string, payload []byte, metadata busruntime.DeliveryMetadata, opts busruntime.JobOptions) error {
	release, err := q.acquireOperation(ctx, true)
	if err != nil {
		return err
	}
	defer release()
	return q.common.dispatchDirectJob(ctx, jobType, payload, metadata, opts)
}

// acquireOperation leases native backend resources through one complete operation.
func (q *nativeQueueRuntime) acquireOperation(ctx context.Context, allowContinuation bool) (func(), error) {
	q.mu.Lock()
	scope := q.continuationScopeLocked()
	if q.closed || (q.draining && (!allowContinuation || !scope.Owns(ctx))) {
		q.mu.Unlock()
		return nil, ErrQueuerShuttingDown
	}
	q.operations.acquire()
	q.mu.Unlock()
	return q.releaseOperation, nil
}

// releaseOperation ends one native lease and wakes a waiting shutdown when the backend becomes idle.
func (q *nativeQueueRuntime) releaseOperation() {
	q.mu.Lock()
	if q.operations.release() {
		q.operations.markIdle()
	}
	q.mu.Unlock()
}

// acquireOperation leases external producer resources through one complete operation.
func (q *externalQueueRuntime) acquireOperation(ctx context.Context, allowContinuation bool) (func(), error) {
	q.mu.Lock()
	scope := q.continuationScopeLocked()
	if q.closed || (q.draining && (!allowContinuation || !scope.Owns(ctx))) {
		q.mu.Unlock()
		return nil, ErrQueuerShuttingDown
	}
	q.operations.acquire()
	q.mu.Unlock()
	return q.releaseOperation, nil
}

// releaseOperation ends one external lease and wakes a waiting shutdown when the producer becomes idle.
func (q *externalQueueRuntime) releaseOperation() {
	q.mu.Lock()
	if q.operations.release() {
		q.operations.markIdle()
	}
	q.mu.Unlock()
}

// continuationScope returns the native runtime's stable permission owner.
func (q *nativeQueueRuntime) continuationScope() *busruntime.ContinuationScope {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.continuationScopeLocked()
}

// continuationScopeLocked lazily initializes test-constructed native states while the lifecycle mutex is held.
func (q *nativeQueueRuntime) continuationScopeLocked() *busruntime.ContinuationScope {
	if q.continuation == nil {
		q.continuation = busruntime.NewContinuationScope()
	}
	return q.continuation
}

// continuationScope returns the external runtime's stable permission owner.
func (q *externalQueueRuntime) continuationScope() *busruntime.ContinuationScope {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.continuationScopeLocked()
}

// continuationScopeLocked lazily initializes test-constructed external states while the lifecycle mutex is held.
func (q *externalQueueRuntime) continuationScopeLocked() *busruntime.ContinuationScope {
	if q.continuation == nil {
		q.continuation = busruntime.NewContinuationScope()
	}
	return q.continuation
}

// Register linearizes logical and physical state so an activating backend cannot consume a newly registered type without its handler.
func (q *nativeQueueRuntime) Register(jobType string, handler Handler) {
	if jobType == "" || handler == nil {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.registered == nil {
		q.registered = make(map[string]Handler)
	}
	q.registered[jobType] = handler
	var slot *runtimeHandlerSlot
	q.handlerSlots, slot = updateRuntimeHandlerSlot(q.handlerSlots, jobType, handler)
	if !q.draining && (q.start != nil || q.started) {
		q.runtimeRegistrations = installRuntimeHandler(q.runtime, q.common, q.runtimeRegistrations, jobType, handler, slot)
	}
}

// Register linearizes logical and physical state once the external worker generation has been published for activation.
func (q *externalQueueRuntime) Register(jobType string, handler Handler) {
	if jobType == "" || handler == nil {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.registered == nil {
		q.registered = make(map[string]Handler)
	}
	q.registered[jobType] = handler
	var slot *runtimeHandlerSlot
	q.handlerSlots, slot = updateRuntimeHandlerSlot(q.handlerSlots, jobType, handler)
	if !q.draining && q.worker != nil && (q.start != nil || q.started) {
		q.workerRegistrations = installRuntimeHandler(q.worker, q.common, q.workerRegistrationsLocked(), jobType, handler, slot)
	}
}

// workerRegistrationsLocked returns the handler types already installed on the retained external worker.
func (q *externalQueueRuntime) workerRegistrationsLocked() map[string]struct{} {
	if q.workerRegistrations == nil {
		q.workerRegistrations = make(map[string]struct{})
	}
	return q.workerRegistrations
}

// StartWorkers installs the current handler generation before activating the backend and serializes concurrent lifecycle calls.
func (q *nativeQueueRuntime) StartWorkers(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	q.mu.Lock()
	if q.closed || q.draining {
		q.mu.Unlock()
		return ErrQueuerShuttingDown
	}
	if q.started || q.paused {
		q.mu.Unlock()
		return nil
	}
	if q.start != nil {
		attempt := q.start
		q.mu.Unlock()
		return waitForRuntimeStart(ctx, attempt)
	}
	attempt := &runtimeStartAttempt{done: make(chan struct{})}
	q.start = attempt
	for jobType, handler := range q.registered {
		var slot *runtimeHandlerSlot
		q.handlerSlots, slot = updateRuntimeHandlerSlot(q.handlerSlots, jobType, handler)
		q.runtimeRegistrations = installRuntimeHandler(q.runtime, q.common, q.runtimeRegistrations, jobType, handler, slot)
	}
	q.mu.Unlock()

	err := q.runtime.StartWorkers(ctx)
	q.mu.Lock()
	if err == nil {
		q.started = true
	}
	attempt.err = err
	q.start = nil
	close(attempt.done)
	q.mu.Unlock()
	return err
}

// StartWorkers publishes and catches up a worker before activation so registrations cannot complete against a stale startup snapshot.
func (q *externalQueueRuntime) StartWorkers(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	q.mu.Lock()
	if q.closed || q.draining {
		q.mu.Unlock()
		return ErrQueuerShuttingDown
	}
	if q.started || q.paused {
		q.mu.Unlock()
		return nil
	}
	if q.start != nil {
		attempt := q.start
		q.mu.Unlock()
		return waitForRuntimeStart(ctx, attempt)
	}
	attempt := &runtimeStartAttempt{done: make(chan struct{})}
	q.start = attempt
	w := q.worker
	workers := q.workers
	q.mu.Unlock()

	var err error
	if w == nil {
		if q.newWorker != nil {
			driverWorker, e := q.newWorker(defaultWorkerCount(workers))
			if e != nil {
				err = e
			} else {
				w = driverWorkerBackendAdapter{driverWorker}
			}
		} else {
			w, err = newExternalWorker(q.common.cfg, workers)
		}
	}
	if err == nil {
		q.mu.Lock()
		q.worker = w
		if setter, ok := w.(runtimeWorkerContextDecoratorSetter); ok {
			setter.SetHandlerContextDecorator(q.common.handlerContextDecorator)
		}
		for jobType, handler := range q.registered {
			var slot *runtimeHandlerSlot
			q.handlerSlots, slot = updateRuntimeHandlerSlot(q.handlerSlots, jobType, handler)
			q.workerRegistrations = installRuntimeHandler(w, q.common, q.workerRegistrationsLocked(), jobType, handler, slot)
		}
		q.mu.Unlock()
		err = w.StartWorkers(ctx)
	}
	q.mu.Lock()
	if w != nil {
		// A partially started worker remains owned so Shutdown can finish cleanup instead of leaking factory resources.
		q.worker = w
	}
	if err == nil {
		q.started = true
	}
	attempt.err = err
	q.start = nil
	close(attempt.done)
	q.mu.Unlock()
	return err
}

// PauseWorkers stops native worker intake while retaining dispatch resources.
func (q *nativeQueueRuntime) PauseWorkers(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	q.mu.Lock()
	if q.closed || q.draining {
		q.mu.Unlock()
		return ErrQueuerShuttingDown
	}
	if q.paused {
		q.mu.Unlock()
		return nil
	}
	if q.start != nil {
		start := q.start
		q.mu.Unlock()
		if err := waitForRuntimeStartCompletion(ctx, start); err != nil {
			return err
		}
		return q.PauseWorkers(ctx)
	}
	if q.pause != nil {
		pause := q.pause
		q.mu.Unlock()
		return waitForRuntimePause(ctx, pause)
	}
	attempt := &runtimePauseAttempt{done: make(chan struct{})}
	q.pause = attempt
	started := q.started
	if !started {
		for jobType, handler := range q.registered {
			var slot *runtimeHandlerSlot
			q.handlerSlots, slot = updateRuntimeHandlerSlot(q.handlerSlots, jobType, handler)
			q.runtimeRegistrations = installRuntimeHandler(q.runtime, q.common, q.runtimeRegistrations, jobType, handler, slot)
		}
	}
	q.mu.Unlock()

	var err error
	if started {
		if lifecycle, ok := q.runtime.(workerLifecycleBackend); ok {
			err = lifecycle.PauseWorkers(ctx)
		}
	}
	q.mu.Lock()
	if err == nil {
		q.paused = true
	}
	attempt.err = err
	q.pause = nil
	close(attempt.done)
	q.mu.Unlock()
	return err
}

// ResumeWorkers restarts native worker intake after a pause.
func (q *nativeQueueRuntime) ResumeWorkers(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	q.mu.Lock()
	if q.closed || q.draining {
		q.mu.Unlock()
		return ErrQueuerShuttingDown
	}
	if q.pause != nil {
		pause := q.pause
		q.mu.Unlock()
		if err := waitForRuntimePause(ctx, pause); err != nil {
			return err
		}
		return q.ResumeWorkers(ctx)
	}
	if !q.paused {
		started := q.started
		q.mu.Unlock()
		if started {
			return nil
		}
		return q.StartWorkers(ctx)
	}
	started := q.started
	q.mu.Unlock()

	if started {
		if lifecycle, ok := q.runtime.(workerLifecycleBackend); ok {
			if err := lifecycle.ResumeWorkers(ctx); err != nil {
				return err
			}
		}
		q.mu.Lock()
		q.paused = false
		q.mu.Unlock()
		return nil
	}
	q.mu.Lock()
	q.paused = false
	q.mu.Unlock()
	if err := q.StartWorkers(ctx); err != nil {
		q.mu.Lock()
		q.paused = true
		q.mu.Unlock()
		return err
	}
	return nil
}

// PauseWorkers stops the external consumer while retaining producer resources.
func (q *externalQueueRuntime) PauseWorkers(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	q.mu.Lock()
	if q.closed || q.draining {
		q.mu.Unlock()
		return ErrQueuerShuttingDown
	}
	if q.paused {
		q.mu.Unlock()
		return nil
	}
	if q.start != nil {
		start := q.start
		q.mu.Unlock()
		if err := waitForRuntimeStartCompletion(ctx, start); err != nil {
			return err
		}
		return q.PauseWorkers(ctx)
	}
	if q.pause != nil {
		pause := q.pause
		q.mu.Unlock()
		return waitForRuntimePause(ctx, pause)
	}
	attempt := &runtimePauseAttempt{done: make(chan struct{})}
	q.pause = attempt
	w := q.worker
	q.mu.Unlock()

	var err error
	if w != nil {
		err = w.Shutdown(ctx)
	}
	q.mu.Lock()
	if err == nil {
		q.worker = nil
		q.workerRegistrations = nil
		q.started = false
		q.paused = true
	}
	attempt.err = err
	q.pause = nil
	close(attempt.done)
	q.mu.Unlock()
	return err
}

// ResumeWorkers creates a fresh external consumer after a pause.
func (q *externalQueueRuntime) ResumeWorkers(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	q.mu.Lock()
	if q.closed || q.draining {
		q.mu.Unlock()
		return ErrQueuerShuttingDown
	}
	if q.pause != nil {
		pause := q.pause
		q.mu.Unlock()
		if err := waitForRuntimePause(ctx, pause); err != nil {
			return err
		}
		return q.ResumeWorkers(ctx)
	}
	if !q.paused {
		started := q.started
		q.mu.Unlock()
		if started {
			return nil
		}
		return q.StartWorkers(ctx)
	}
	q.paused = false
	q.mu.Unlock()
	if err := q.StartWorkers(ctx); err != nil {
		q.mu.Lock()
		q.paused = true
		q.mu.Unlock()
		return err
	}
	return nil
}

func (q *nativeQueueRuntime) Workers(count int) queueRuntime {
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.started && !q.draining && !q.closed && q.start == nil && count > 0 {
		q.workers = count
		if setter, ok := q.runtime.(interface{ setWorkers(int) }); ok {
			setter.setWorkers(count)
		}
	}
	return q
}

func (q *externalQueueRuntime) Workers(count int) queueRuntime {
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.started && !q.draining && !q.closed && q.start == nil && count > 0 {
		q.workers = count
	}
	return q
}

// Shutdown retains native runtime state until cleanup succeeds so timed-out drains remain retryable.
func (q *nativeQueueRuntime) Shutdown(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	q.mu.Lock()
	if q.pause != nil {
		pause := q.pause
		q.mu.Unlock()
		if err := waitForRuntimePause(ctx, pause); err != nil {
			return err
		}
		return q.Shutdown(ctx)
	}
	if q.start != nil {
		q.draining = true
		attempt := q.start
		q.mu.Unlock()
		if err := waitForRuntimeStartCompletion(ctx, attempt); err != nil {
			return err
		}
		return q.Shutdown(ctx)
	}
	if q.shutdown != nil {
		attempt := q.shutdown
		q.mu.Unlock()
		return waitForRuntimeShutdown(ctx, attempt)
	}
	if q.closed {
		q.mu.Unlock()
		return nil
	}
	q.draining = true
	attempt := &runtimeShutdownAttempt{done: make(chan struct{})}
	q.shutdown = attempt
	idle := q.operations.idle
	q.mu.Unlock()

	err := waitForRuntimeOperations(ctx, idle)
	if err == nil {
		err = q.runtime.DrainWorkers(ctx)
	}
	if err == nil {
		// Worker drain expires every handler-issued continuation permit. A second
		// operation snapshot is therefore stable and must finish before resources close.
		q.mu.Lock()
		idle = q.operations.idle
		q.mu.Unlock()
		err = waitForRuntimeOperations(ctx, idle)
	}
	if err == nil {
		err = q.common.inner.Shutdown(ctx)
	}
	q.mu.Lock()
	attempt.err = err
	q.shutdown = nil
	if err == nil {
		q.started = false
		q.draining = false
		q.closed = true
	}
	close(attempt.done)
	q.mu.Unlock()
	return err
}

// Shutdown drains the worker before producer resources and retains both until every cleanup succeeds.
func (q *externalQueueRuntime) Shutdown(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	q.mu.Lock()
	if q.pause != nil {
		pause := q.pause
		q.mu.Unlock()
		if err := waitForRuntimePause(ctx, pause); err != nil {
			return err
		}
		return q.Shutdown(ctx)
	}
	if q.start != nil {
		q.draining = true
		attempt := q.start
		q.mu.Unlock()
		if err := waitForRuntimeStartCompletion(ctx, attempt); err != nil {
			return err
		}
		return q.Shutdown(ctx)
	}
	if q.shutdown != nil {
		attempt := q.shutdown
		q.mu.Unlock()
		return waitForRuntimeShutdown(ctx, attempt)
	}
	if q.closed {
		q.mu.Unlock()
		return nil
	}
	w := q.worker
	q.draining = true
	attempt := &runtimeShutdownAttempt{done: make(chan struct{})}
	q.shutdown = attempt
	idle := q.operations.idle
	q.mu.Unlock()

	err := waitForRuntimeOperations(ctx, idle)
	if w != nil {
		if err == nil {
			err = w.Shutdown(ctx)
		}
		if err == nil {
			q.mu.Lock()
			q.worker = nil
			q.workerRegistrations = nil
			q.started = false
			q.mu.Unlock()
		}
	}
	if err == nil {
		// A handler may admit a descendant after the initial snapshot. Once worker drain returns, its scoped permit has expired, so this generation is stable.
		q.mu.Lock()
		idle = q.operations.idle
		q.mu.Unlock()
		err = waitForRuntimeOperations(ctx, idle)
	}
	if err == nil {
		err = q.common.inner.Shutdown(ctx)
	}
	q.mu.Lock()
	attempt.err = err
	q.shutdown = nil
	if err == nil {
		q.draining = false
		q.closed = true
	}
	close(attempt.done)
	q.mu.Unlock()
	return err
}

// waitForRuntimeOperations prevents resource cleanup from overtaking an operation that already passed the lifecycle gate.
func waitForRuntimeOperations(ctx context.Context, idle <-chan struct{}) error {
	if idle == nil {
		return nil
	}
	select {
	case <-idle:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// waitForRuntimeShutdown lets concurrent callers share one cleanup attempt while honoring their own deadline.
func waitForRuntimeShutdown(ctx context.Context, attempt *runtimeShutdownAttempt) error {
	select {
	case <-attempt.done:
		return attempt.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// waitForRuntimePause lets concurrent lifecycle callers share one pause attempt while honoring their own deadline.
func waitForRuntimePause(ctx context.Context, attempt *runtimePauseAttempt) error {
	select {
	case <-attempt.done:
		return attempt.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// waitForRuntimeStart lets concurrent callers share one startup attempt while honoring their own deadline.
func waitForRuntimeStart(ctx context.Context, attempt *runtimeStartAttempt) error {
	select {
	case <-attempt.done:
		return attempt.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// waitForRuntimeStartCompletion lets shutdown wait for ownership of any worker that startup creates.
func waitForRuntimeStartCompletion(ctx context.Context, attempt *runtimeStartAttempt) error {
	select {
	case <-attempt.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (q *queueCommon) Pause(ctx context.Context, queueName string) error {
	controller, ok := q.inner.(QueueController)
	if !ok {
		return ErrPauseUnsupported
	}
	return controller.Pause(ctx, q.physicalQueueNameOrDefault(queueName))
}

func (q *queueCommon) Resume(ctx context.Context, queueName string) error {
	controller, ok := q.inner.(QueueController)
	if !ok {
		return ErrPauseUnsupported
	}
	return controller.Resume(ctx, q.physicalQueueNameOrDefault(queueName))
}

func (q *queueCommon) Stats(ctx context.Context) (StatsSnapshot, error) {
	provider, ok := q.inner.(StatsProvider)
	if !ok {
		return StatsSnapshot{}, fmt.Errorf("stats provider is not available for driver %q", q.Driver())
	}
	return provider.Stats(ctx)
}

func (q *queueCommon) ListJobs(ctx context.Context, opts ListJobsOptions) (ListJobsResult, error) {
	admin, ok := q.inner.(QueueAdmin)
	if !ok {
		return ListJobsResult{}, ErrQueueAdminUnsupported
	}
	opts.Queue = q.physicalQueueNameOrDefault(opts.Queue)
	return admin.ListJobs(ctx, opts)
}

func (q *queueCommon) RetryJob(ctx context.Context, queueName, jobID string) error {
	admin, ok := q.inner.(QueueAdmin)
	if !ok {
		return ErrQueueAdminUnsupported
	}
	return admin.RetryJob(ctx, q.physicalQueueNameOrDefault(queueName), jobID)
}

func (q *queueCommon) CancelJob(ctx context.Context, jobID string) error {
	admin, ok := q.inner.(QueueAdmin)
	if !ok {
		return ErrQueueAdminUnsupported
	}
	return admin.CancelJob(ctx, jobID)
}

func (q *queueCommon) DeleteJob(ctx context.Context, queueName, jobID string) error {
	admin, ok := q.inner.(QueueAdmin)
	if !ok {
		return ErrQueueAdminUnsupported
	}
	return admin.DeleteJob(ctx, q.physicalQueueNameOrDefault(queueName), jobID)
}

func (q *queueCommon) ClearQueue(ctx context.Context, queueName string) error {
	admin, ok := q.inner.(QueueAdmin)
	if !ok {
		return ErrQueueAdminUnsupported
	}
	return admin.ClearQueue(ctx, q.physicalQueueNameOrDefault(queueName))
}

func (q *queueCommon) History(ctx context.Context, queueName string, window QueueHistoryWindow) ([]QueueHistoryPoint, error) {
	history, ok := q.inner.(QueueHistoryProvider)
	if !ok {
		return nil, ErrQueueAdminUnsupported
	}
	return history.History(ctx, q.physicalQueueNameOrDefault(queueName), window)
}

func (q *queueCommon) Ready(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return runtimeReadyCheck(ctx, q.inner)
}

func (q *nativeQueueRuntime) Pause(ctx context.Context, queueName string) error {
	release, err := q.acquireOperation(ctx, false)
	if err != nil {
		return err
	}
	defer release()
	return q.common.Pause(ctx, queueName)
}
func (q *nativeQueueRuntime) Resume(ctx context.Context, queueName string) error {
	release, err := q.acquireOperation(ctx, false)
	if err != nil {
		return err
	}
	defer release()
	return q.common.Resume(ctx, queueName)
}
func (q *nativeQueueRuntime) Stats(ctx context.Context) (StatsSnapshot, error) {
	release, err := q.acquireOperation(ctx, false)
	if err != nil {
		return StatsSnapshot{}, err
	}
	defer release()
	return q.common.Stats(ctx)
}
func (q *nativeQueueRuntime) Ready(ctx context.Context) error {
	release, err := q.acquireOperation(ctx, false)
	if err != nil {
		return err
	}
	defer release()
	return q.common.Ready(ctx)
}
func (q *externalQueueRuntime) Pause(ctx context.Context, queueName string) error {
	release, err := q.acquireOperation(ctx, false)
	if err != nil {
		return err
	}
	defer release()
	return q.common.Pause(ctx, queueName)
}
func (q *externalQueueRuntime) Resume(ctx context.Context, queueName string) error {
	release, err := q.acquireOperation(ctx, false)
	if err != nil {
		return err
	}
	defer release()
	return q.common.Resume(ctx, queueName)
}
func (q *externalQueueRuntime) Stats(ctx context.Context) (StatsSnapshot, error) {
	release, err := q.acquireOperation(ctx, false)
	if err != nil {
		return StatsSnapshot{}, err
	}
	defer release()
	return q.common.Stats(ctx)
}
func (q *externalQueueRuntime) Ready(ctx context.Context) error {
	release, err := q.acquireOperation(ctx, false)
	if err != nil {
		return err
	}
	defer release()
	return q.common.Ready(ctx)
}

// wrapRegisteredHandler keeps each backend's context decoration and process
// observation at a single execution boundary.
func (q *queueCommon) wrapRegisteredHandler(jobType string, handler Handler) Handler {
	if handler == nil {
		return handler
	}
	// Redis worker emits process lifecycle events natively.
	// Skip shared handler wrapping and decoration to avoid duplicate process_* events
	// and context decoration.
	if q.cfg.Driver == DriverRedis {
		return handler
	}
	if !observerHasRecipients(q.observerSink) {
		return wrapHandlerContext(q.handlerContextDecorator, handler)
	}
	return wrapObservedHandler(q.observerSink, q.cfg.Driver, "", jobType, q.handlerContextDecorator, handler)
}

// wrapHandlerContext applies optional execution context decoration while
// preserving the original context when the decorator returns nil.
func wrapHandlerContext(decorator func(context.Context) context.Context, handler Handler) Handler {
	if decorator == nil || handler == nil {
		return handler
	}
	return func(ctx context.Context, job Job) error {
		if decorated := decorator(ctx); decorated != nil {
			ctx = busruntime.PreserveDeliveryContext(ctx, decorated)
		}
		return handler(ctx, job)
	}
}

// dispatchBusJob preserves workflow policy and logical identity while adapting onto the canonical root job.
func (q *queueCommon) dispatchBusJob(ctx context.Context, jobType string, payload []byte, opts busruntime.JobOptions) error {
	ctx, acceptance := newDispatchAcceptance(ctx)
	job := NewJob(jobType).Payload(payload)
	if opts.Queue != "" {
		job = job.OnQueue(opts.Queue)
	}
	if opts.Delay > 0 {
		job = job.Delay(opts.Delay)
	}
	if opts.Timeout > 0 {
		job = job.Timeout(opts.Timeout)
	}
	// Workflow policy always owns the retry budget; omitting zero lets several backends invent a different default.
	job = job.Retry(opts.Retry)
	if opts.Backoff > 0 {
		job = job.Backoff(opts.Backoff)
	}
	if opts.UniqueFor > 0 {
		logical := resolveLogicalJob(jobType, payload)
		job = job.UniqueFor(opts.UniqueFor).withLogicalIdentity(logical.jobType, logical.payload)
	}
	err := q.inner.Dispatch(ctx, q.physicalJob(job))
	if err == nil {
		acceptance.markAccepted()
		return nil
	}
	if acceptance.isAccepted() {
		return acceptedExecutionError{cause: err}
	}
	return err
}

// dispatchDirectJob preserves direct application bytes while attaching
// correlation through the driver metadata channel instead of a workflow envelope.
func (q *queueCommon) dispatchDirectJob(ctx context.Context, jobType string, payload []byte, metadata busruntime.DeliveryMetadata, opts busruntime.JobOptions) error {
	ctx, acceptance := newDispatchAcceptance(ctx)
	job := NewJob(jobType).Payload(payload)
	if opts.Queue != "" {
		job = job.OnQueue(opts.Queue)
	}
	if opts.Delay > 0 {
		job = job.Delay(opts.Delay)
	}
	if opts.Timeout > 0 {
		job = job.Timeout(opts.Timeout)
	}
	// Direct workflow policy still owns an explicit zero retry budget.
	job = job.Retry(opts.Retry)
	if opts.Backoff > 0 {
		job = job.Backoff(opts.Backoff)
	}
	if opts.UniqueFor > 0 {
		job = job.UniqueFor(opts.UniqueFor)
	}
	job = DriverWithMetadata(job, metadata)
	err := q.inner.Dispatch(ctx, q.physicalJob(job))
	if err == nil {
		acceptance.markAccepted()
		return nil
	}
	if acceptance.isAccepted() {
		return acceptedExecutionError{cause: err}
	}
	return err
}

func newExternalWorker(cfg Config, concurrency int) (runtimeWorkerBackend, error) {
	switch cfg.Driver {
	default:
		return nil, fmt.Errorf("unsupported queue driver %q", cfg.Driver)
	}
}

type driverQueueBackendAdapter struct {
	driverQueueBackend
}

type driverRuntimeQueueBackendAdapter struct {
	driverRuntimeQueueBackend
}

// DrainWorkers forwards the native driver's worker-drain lifecycle phase.
func (a driverRuntimeQueueBackendAdapter) DrainWorkers(ctx context.Context) error {
	return a.driverRuntimeQueueBackend.DrainWorkers(ctx)
}

type driverWorkerBackendAdapter struct {
	driverWorkerBackend
}

func (a driverWorkerBackendAdapter) SetHandlerContextDecorator(fn func(context.Context) context.Context) {
	if setter, ok := a.driverWorkerBackend.(runtimeWorkerContextDecoratorSetter); ok {
		setter.SetHandlerContextDecorator(fn)
	}
}

func (a driverQueueBackendAdapter) Pause(ctx context.Context, queueName string) error {
	controller, ok := a.driverQueueBackend.(QueueController)
	if !ok {
		return ErrPauseUnsupported
	}
	return controller.Pause(ctx, queueName)
}

func (a driverQueueBackendAdapter) Resume(ctx context.Context, queueName string) error {
	controller, ok := a.driverQueueBackend.(QueueController)
	if !ok {
		return ErrPauseUnsupported
	}
	return controller.Resume(ctx, queueName)
}

func (a driverQueueBackendAdapter) Stats(ctx context.Context) (StatsSnapshot, error) {
	provider, ok := a.driverQueueBackend.(StatsProvider)
	if !ok {
		return StatsSnapshot{}, fmt.Errorf("stats provider is not available for driver %q", a.Driver())
	}
	return provider.Stats(ctx)
}

func (a driverQueueBackendAdapter) Ready(ctx context.Context) error {
	return runtimeReadyCheck(ctx, a.driverQueueBackend)
}

func (a driverQueueBackendAdapter) ListJobs(ctx context.Context, opts ListJobsOptions) (ListJobsResult, error) {
	admin, ok := a.driverQueueBackend.(QueueAdmin)
	if !ok {
		return ListJobsResult{}, ErrQueueAdminUnsupported
	}
	return admin.ListJobs(ctx, opts)
}

func (a driverQueueBackendAdapter) RetryJob(ctx context.Context, queueName, jobID string) error {
	admin, ok := a.driverQueueBackend.(QueueAdmin)
	if !ok {
		return ErrQueueAdminUnsupported
	}
	return admin.RetryJob(ctx, queueName, jobID)
}

func (a driverQueueBackendAdapter) CancelJob(ctx context.Context, jobID string) error {
	admin, ok := a.driverQueueBackend.(QueueAdmin)
	if !ok {
		return ErrQueueAdminUnsupported
	}
	return admin.CancelJob(ctx, jobID)
}

func (a driverQueueBackendAdapter) DeleteJob(ctx context.Context, queueName, jobID string) error {
	admin, ok := a.driverQueueBackend.(QueueAdmin)
	if !ok {
		return ErrQueueAdminUnsupported
	}
	return admin.DeleteJob(ctx, queueName, jobID)
}

func (a driverQueueBackendAdapter) ClearQueue(ctx context.Context, queueName string) error {
	admin, ok := a.driverQueueBackend.(QueueAdmin)
	if !ok {
		return ErrQueueAdminUnsupported
	}
	return admin.ClearQueue(ctx, queueName)
}

func (a driverQueueBackendAdapter) History(ctx context.Context, queueName string, window QueueHistoryWindow) ([]QueueHistoryPoint, error) {
	admin, ok := a.driverQueueBackend.(QueueAdmin)
	if !ok {
		return nil, ErrQueueAdminUnsupported
	}
	return admin.History(ctx, queueName, window)
}

func (a driverRuntimeQueueBackendAdapter) Pause(ctx context.Context, queueName string) error {
	controller, ok := a.driverRuntimeQueueBackend.(QueueController)
	if !ok {
		return ErrPauseUnsupported
	}
	return controller.Pause(ctx, queueName)
}

func (a driverRuntimeQueueBackendAdapter) Resume(ctx context.Context, queueName string) error {
	controller, ok := a.driverRuntimeQueueBackend.(QueueController)
	if !ok {
		return ErrPauseUnsupported
	}
	return controller.Resume(ctx, queueName)
}

func (a driverRuntimeQueueBackendAdapter) Stats(ctx context.Context) (StatsSnapshot, error) {
	provider, ok := a.driverRuntimeQueueBackend.(StatsProvider)
	if !ok {
		return StatsSnapshot{}, fmt.Errorf("stats provider is not available for driver %q", a.Driver())
	}
	return provider.Stats(ctx)
}

func (a driverRuntimeQueueBackendAdapter) Ready(ctx context.Context) error {
	return runtimeReadyCheck(ctx, a.driverRuntimeQueueBackend)
}

func (a driverRuntimeQueueBackendAdapter) ListJobs(ctx context.Context, opts ListJobsOptions) (ListJobsResult, error) {
	admin, ok := a.driverRuntimeQueueBackend.(QueueAdmin)
	if !ok {
		return ListJobsResult{}, ErrQueueAdminUnsupported
	}
	return admin.ListJobs(ctx, opts)
}

func (a driverRuntimeQueueBackendAdapter) RetryJob(ctx context.Context, queueName, jobID string) error {
	admin, ok := a.driverRuntimeQueueBackend.(QueueAdmin)
	if !ok {
		return ErrQueueAdminUnsupported
	}
	return admin.RetryJob(ctx, queueName, jobID)
}

func (a driverRuntimeQueueBackendAdapter) CancelJob(ctx context.Context, jobID string) error {
	admin, ok := a.driverRuntimeQueueBackend.(QueueAdmin)
	if !ok {
		return ErrQueueAdminUnsupported
	}
	return admin.CancelJob(ctx, jobID)
}

func (a driverRuntimeQueueBackendAdapter) DeleteJob(ctx context.Context, queueName, jobID string) error {
	admin, ok := a.driverRuntimeQueueBackend.(QueueAdmin)
	if !ok {
		return ErrQueueAdminUnsupported
	}
	return admin.DeleteJob(ctx, queueName, jobID)
}

func (a driverRuntimeQueueBackendAdapter) ClearQueue(ctx context.Context, queueName string) error {
	admin, ok := a.driverRuntimeQueueBackend.(QueueAdmin)
	if !ok {
		return ErrQueueAdminUnsupported
	}
	return admin.ClearQueue(ctx, queueName)
}

func (a driverRuntimeQueueBackendAdapter) History(ctx context.Context, queueName string, window QueueHistoryWindow) ([]QueueHistoryPoint, error) {
	admin, ok := a.driverRuntimeQueueBackend.(QueueAdmin)
	if !ok {
		return nil, ErrQueueAdminUnsupported
	}
	return admin.History(ctx, queueName, window)
}

func runtimeReadyCheck(ctx context.Context, raw any) error {
	if checker, ok := raw.(interface{ Ready(context.Context) error }); ok {
		return checker.Ready(ctx)
	}
	// Backward-compatible bridge for older backend implementations.
	if checker, ok := raw.(interface{ Preflight(context.Context) error }); ok {
		return checker.Preflight(ctx)
	}
	return nil
}

func optionalDriverMovedError(driver Driver) error {
	switch driver {
	case DriverRedis:
		return fmt.Errorf("redis driver moved; use github.com/goforj/queue/driver/redisqueue")
	case DriverNATS:
		return fmt.Errorf("nats driver moved; use github.com/goforj/queue/driver/natsqueue")
	case DriverSQS:
		return fmt.Errorf("sqs driver moved; use github.com/goforj/queue/driver/sqsqueue")
	case DriverRabbitMQ:
		return fmt.Errorf("rabbitmq driver moved; use github.com/goforj/queue/driver/rabbitmqqueue")
	case DriverDatabase:
		return fmt.Errorf("database drivers moved; use github.com/goforj/queue/driver/{mysqlqueue,postgresqueue,sqlitequeue}")
	default:
		return fmt.Errorf("unsupported queue driver %q", driver)
	}
}

// jobFromAny applies this runtime's default queue while sharing the canonical
// value-to-job conversion with the public fake.
func (q *queueCommon) jobFromAny(job any) (Job, error) {
	return normalizeDispatchJob(job, q.cfg.DefaultQueue)
}

// normalizeDispatchJob keeps typed-value inference and default queue selection
// identical without changing when production backends validate acceptance.
func normalizeDispatchJob(job any, defaultQueue string) (Job, error) {
	if job, ok := job.(Job); ok {
		if job.Type == "" {
			return Job{}, fmt.Errorf("dispatch job type is required")
		}
		return job, nil
	}
	if job == nil {
		return Job{}, fmt.Errorf("dispatch job is nil")
	}
	jobType := jobTypeFromValue(job)
	if jobType == "" {
		return Job{}, fmt.Errorf("dispatch job type could not be inferred")
	}
	if marshaler, ok := job.(interface{ JobType() string }); ok {
		if t := marshaler.JobType(); t != "" {
			jobType = t
		}
	}
	payload, err := json.Marshal(job)
	if err != nil {
		return Job{}, fmt.Errorf("marshal dispatch job: %w", err)
	}
	return NewJob(jobType).Payload(payload).OnQueue(defaultQueue), nil
}

// jobTypeFromValue limits implicit names to declared Go types so anonymous
// payload shapes cannot accidentally become unstable queue contracts.
func jobTypeFromValue(v any) string {
	t := reflect.TypeOf(v)
	if t == nil {
		return ""
	}
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Name() == "" {
		return ""
	}
	return t.Name()
}
