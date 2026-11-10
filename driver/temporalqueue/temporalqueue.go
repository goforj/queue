package temporalqueue

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"time"

	"github.com/goforj/queue"
	"github.com/goforj/queue/internal/driverbridge"
	"github.com/goforj/queue/internal/runtimehook"
	"github.com/goforj/queue/queueconfig"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

// Config configures one Temporal resource with combined or split worker roles.
// Native workflow/update interceptors can remain in the SDK's process-wide cache
// after shutdown. Dependencies captured across SDK workflow yields must remain
// valid for that cache lifetime; resource-owned application I/O belongs in activities.
type Config struct {
	queueconfig.DriverBaseConfig
	ClientOptions          client.Options
	Binding                *ClientBinding
	WorkerOptions          worker.Options
	WorkflowTaskQueue      string
	ActivityTaskQueue      string
	WorkflowWorkerOptions  worker.Options
	ActivityWorkerOptions  worker.Options
	Register               func(worker.Registry) error
	DefaultActivityTimeout time.Duration
	ShutdownTimeout        time.Duration
	SerializationLimits    SerializationLimits
}

// NewWithConfig constructs a Temporal-backed queue without starting workers.
// @group Constructors
//
// Example: Temporal config constructor
//
//	q, err := temporalqueue.NewWithConfig(temporalqueue.Config{
//		DriverBaseConfig:       queueconfig.DriverBaseConfig{DefaultQueue: "reports"},
//		ClientOptions:          client.Options{HostPort: "localhost:7233"},
//		DefaultActivityTimeout: 30 * time.Second,
//		ShutdownTimeout:        15 * time.Second,
//	}, queue.WithWorkers(4))
//	if err != nil {
//		return
//	}
//	defer q.Shutdown(context.Background())
//	_ = q
func NewWithConfig(cfg Config, opts ...queue.Option) (*queue.Queue, error) {
	if _, err := roundPolicySeconds(cfg.DefaultActivityTimeout); err != nil {
		return nil, err
	}
	if cfg.DefaultActivityTimeout < time.Second {
		return nil, errors.New("Temporal default activity timeout must be at least one second")
	}
	if cfg.ShutdownTimeout <= 0 {
		return nil, errors.New("Temporal shutdown timeout must be positive")
	}
	cfg.WorkerOptions.Interceptors = append([]interceptor.WorkerInterceptor(nil), cfg.WorkerOptions.Interceptors...)
	cfg.WorkflowWorkerOptions.Interceptors = append([]interceptor.WorkerInterceptor(nil), cfg.WorkflowWorkerOptions.Interceptors...)
	cfg.ActivityWorkerOptions.Interceptors = append([]interceptor.WorkerInterceptor(nil), cfg.ActivityWorkerOptions.Interceptors...)
	workflowQueue, activityQueue, err := resolveTopology(cfg)
	if err != nil {
		return nil, err
	}
	binding := cfg.Binding
	owned := binding == nil
	if owned {
		var err error
		binding, err = NewClientBinding(cfg.ClientOptions, cfg.SerializationLimits)
		if err != nil {
			return nil, err
		}
	} else {
		// A borrowed binding is authoritative; reject options that could claim a divergent policy.
		if !emptyClientOptions(cfg.ClientOptions) || cfg.SerializationLimits != (SerializationLimits{}) {
			return nil, errors.New("borrowed Temporal binding cannot be combined with client policy options")
		}
	}
	if err := binding.acquire(); err != nil {
		return nil, err
	}
	b := &backend{workflowQueue: workflowQueue, activityQueue: activityQueue, cfg: cfg, clientBinding: binding, owned: owned, gate: newAdmission(), entry: newAdmission(), done: make(chan struct{}), cleanupDone: make(chan struct{}), handlers: make(map[string]queue.Handler)}
	b.taskQueue = queue.PhysicalQueueName(cfg.DefaultQueue, "")
	if b.taskQueue == "" {
		b.taskQueue = "default"
	}
	descriptor := runtimehook.Descriptor{
		Backend: b, WorkflowMode: runtimehook.WorkflowNative,
		TranslateJobError: translateQueueError, PanicJobError: translateQueuePanic,
		ValidateSettings: validateSettings,
		NewLifecycle: func(binding *runtimehook.WorkflowBinding) (runtimehook.DriverLifecycle, error) {
			b.binding = binding
			return b, nil
		},
		NewCoordinator: func(binding *runtimehook.WorkflowBinding) (runtimehook.NativeCoordinator, error) {
			return &coordinator{backend: b, binding: binding}, nil
		},
	}
	q, err := driverbridge.NewQueue(queue.Config{Driver: queue.DriverTemporal, DefaultQueue: cfg.DefaultQueue, Logger: cfg.Logger}, driverbridge.NewObserverSink(cfg.Observer), descriptor, opts...)
	if err != nil {
		binding.release()
		if owned {
			_ = binding.Close(context.Background())
		}
		return nil, err
	}
	return q, nil
}

// emptyClientOptions avoids comparing SDK options containing slices and callbacks.
func emptyClientOptions(options client.Options) bool {
	return reflect.ValueOf(options).IsZero()
}

// validateSettings rejects incompatible root policies before runtime registration.
func validateSettings(settings runtimehook.Settings) error {
	if settings.WorkersSupplied && settings.Workers < 1 {
		return errors.New("Temporal worker capacity must be positive")
	}
	if settings.StoreSupplied || settings.ClockSupplied || settings.LegacyEnvelopeSupplied || settings.ResourceIDSupplied {
		return errors.New("Temporal owns workflow state, workflow time and direct delivery protocol")
	}
	return nil
}

// backend binds ordinary delivery, native registrations and one physical lifecycle.
type backend struct {
	planOnce      sync.Once
	nativePlan    *registrationPlan
	planErr       error
	cfg           Config
	taskQueue     string
	workflowQueue string
	activityQueue string
	clientBinding *ClientBinding
	owned         bool
	binding       *runtimehook.WorkflowBinding
	gate          *admission
	entry         *admission
	mu            sync.Mutex
	nativeNames   []string
	handlers      map[string]queue.Handler
	workers       int
	configErr     error
	worker        worker.Worker
	workerClient  client.Client
	stop          *stopOperation
	starting      bool
	startErr      error
	stopping      bool
	closed        bool
	released      bool
	cleanupOnce   sync.Once
	cleanupDone   chan struct{}
	cleanupErr    error
	startDone     chan struct{}
	done          chan struct{}
	fatal         error
	fatalOnce     sync.Once
}

// Binding returns the root-selected resource identity.
func (b *backend) Binding() *runtimehook.WorkflowBinding { return b.binding }

// Driver identifies this optional integration without adding SDK types to queue.
func (b *backend) Driver() queue.Driver { return queue.DriverTemporal }

// Done signals a terminal worker failure independently of caller cancellation.
func (b *backend) Done() <-chan struct{} { return b.done }

// Err returns the stable sanitized fatal result.
func (b *backend) Err() error { b.mu.Lock(); defer b.mu.Unlock(); return b.fatal }

// ShutdownTimeout returns the aggregate resource budget.
func (b *backend) ShutdownTimeout() time.Duration { return b.cfg.ShutdownTimeout }

// ConfigurationError returns a deferred fluent worker configuration conflict.
func (b *backend) ConfigurationError() error { b.mu.Lock(); defer b.mu.Unlock(); return b.configErr }

// SetWorkers records remote activity concurrency before polling begins.
func (b *backend) SetWorkers(count int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if count <= 0 || b.worker != nil || b.starting || b.stopping {
		b.configErr = errors.New("Temporal worker capacity must be positive and set before startup")
		return
	}
	if b.remoteOptions().MaxConcurrentActivityExecutionSize != 0 && b.remoteOptions().MaxConcurrentActivityExecutionSize != count {
		b.configErr = errors.New("Temporal SDK activity concurrency conflicts with queue worker capacity")
		return
	}
	b.workers = count
}

// Register preserves logical handler replacement independently of SDK registration.
func (b *backend) Register(name string, handler queue.Handler) {
	b.mu.Lock()
	b.handlers[name] = handler
	b.mu.Unlock()
}

// Ready validates server connectivity without requiring a workflow worker.
func (b *backend) Ready(ctx context.Context) error {
	b.mu.Lock()
	starting, startErr := b.starting, b.startErr
	b.mu.Unlock()
	if starting {
		return errors.New("Temporal worker startup has not completed")
	}
	if startErr != nil {
		return startErr
	}
	release, err := b.entry.enter()
	if err != nil {
		return err
	}
	defer release()
	_, err = b.clientBinding.client.CheckHealth(ctx, &client.CheckHealthRequest{})
	if err != nil {
		return errors.New("Temporal service is not ready")
	}
	return nil
}

// StartWorkers freezes native registrations before starting either poller.
func (b *backend) StartWorkers(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	b.mu.Lock()
	if b.stopping {
		b.mu.Unlock()
		return errAdmissionClosed
	}
	if b.configErr != nil {
		err := b.configErr
		b.mu.Unlock()
		return err
	}
	if b.startDone == nil {
		b.starting = true
		b.startDone = make(chan struct{})
		go b.startOperation()
	}
	done := b.startDone
	b.mu.Unlock()
	select {
	case <-done:
		b.mu.Lock()
		defer b.mu.Unlock()
		return b.startErr
	default:
	}
	select {
	case <-done:
		b.mu.Lock()
		defer b.mu.Unlock()
		return b.startErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

// startOperation retains actual startup ownership after a caller's deadline expires.
func (b *backend) startOperation() {
	var err error
	defer func() {
		if recover() != nil {
			err = errors.New("Temporal worker startup adapter failed")
		}
		b.mu.Lock()
		b.startErr = err
		b.starting = false
		close(b.startDone)
		b.mu.Unlock()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err = b.startWorker(ctx)
}

// startWorker freezes native registrations before starting either poller.
func (b *backend) startWorker(ctx context.Context) (err error) {
	release, err := b.entry.enter()
	if err != nil {
		return err
	}
	defer release()
	options, err := b.workerClientOptions()
	if err != nil {
		return err
	}
	c, err := client.NewClientFromExistingWithContext(ctx, b.clientBinding.client, options)
	if err != nil {
		return errors.New("Temporal worker connection failed")
	}
	transferred := false
	defer func() {
		if !transferred {
			c.Close()
		}
	}()
	err = b.startWorkerSet(c)
	b.mu.Lock()
	transferred = b.workerClient == c
	b.mu.Unlock()
	return err
}

// onFatal signals Queue.Run and joins the sole stop arbiter before the SDK fallback.
func (b *backend) onFatal(error) {
	b.fatalOnce.Do(func() { b.mu.Lock(); b.fatal = errors.New("Temporal worker failed"); close(b.done); b.mu.Unlock() })
	b.mu.Lock()
	b.stopping = true
	stop := b.stop
	b.mu.Unlock()
	b.entry.close()
	if stop != nil {
		_ = stop.wait(context.Background())
	}
}

// DrainWorkers retains dependencies until both SDK stop and actual callback return.
func (b *backend) DrainWorkers(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, b.cfg.ShutdownTimeout)
	defer cancel()
	b.mu.Lock()
	b.stopping = true
	starting := b.startDone
	b.mu.Unlock()
	b.entry.close()
	if starting != nil {
		select {
		case <-starting:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	b.mu.Lock()
	stop := b.stop
	b.mu.Unlock()
	if stop != nil {
		if err := stop.wait(ctx); err != nil {
			return err
		}
	}
	if err := b.entry.wait(ctx); err != nil {
		return err
	}
	b.gate.close()
	return b.gate.wait(ctx)
}

// Shutdown joins owned workers and admitted resource callbacks before closing owned clients.
// SDK-cached workflow/update coroutines and their deferred tracing cleanup remain
// application-owned and do not delay resource shutdown.
func (b *backend) Shutdown(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, b.cfg.ShutdownTimeout)
	defer cancel()
	if err := b.DrainWorkers(ctx); err != nil {
		return err
	}
	b.cleanupOnce.Do(func() {
		go func() {
			b.mu.Lock()
			c := b.workerClient
			b.mu.Unlock()
			if c != nil {
				c.Close()
			}
			b.clientBinding.release()
			var err error
			if b.owned {
				err = b.clientBinding.Close(context.Background())
			}
			b.mu.Lock()
			b.cleanupErr = err
			b.closed = err == nil
			b.mu.Unlock()
			close(b.cleanupDone)
		}()
	})
	select {
	case <-b.cleanupDone:
		b.mu.Lock()
		defer b.mu.Unlock()
		return b.cleanupErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Client returns the actual SDK client belonging to this queue resource.
// Applications borrow it and must leave Close to the resource owner.
func Client(q *queue.Queue) (client.Client, error) {
	b, ok := runtimehook.DriverResource(q).(*backend)
	if !ok {
		return nil, errors.New("queue is not a Temporal resource")
	}
	return b.clientBinding.Client(), nil
}

// RegistrationFingerprint returns the current native and logical handler catalog digest.
func RegistrationFingerprint(q *queue.Queue) (string, error) {
	b, ok := runtimehook.DriverResource(q).(*backend)
	if !ok {
		return "", errors.New("queue is not a Temporal resource")
	}
	b.mu.Lock()
	ready := b.worker != nil && !b.starting && b.startErr == nil
	b.mu.Unlock()
	if !ready {
		return "", errors.New("Temporal registration catalog is not finalized")
	}
	return b.RegistrationFingerprint(), nil
}

// workerClientOptions shares existing connection authentication without reapplying SDK credentials or replacing its trust policy.
func (b *backend) workerClientOptions() (client.Options, error) {
	options := b.clientBinding.options
	options.Credentials = nil
	options.Interceptors = append([]interceptor.ClientInterceptor{&resourceInterceptor{limits: b.clientBinding.limits, overhead: b.clientBinding.options.DataConverter.(*guardedConverter).overhead, gate: b.entry}}, options.Interceptors...)
	var err error
	options.DataConverter, err = newGuardedConverter(options.DataConverter, b.clientBinding.limits, b.gate)
	if err != nil {
		return client.Options{}, err
	}
	options.FailureConverter = &guardedFailureConverter{inner: options.FailureConverter, dc: options.DataConverter, gate: b.gate, protected: true, workflowTask: true}
	options.Logger = &redactedLogger{inner: options.Logger, gate: b.gate}
	if options.MetricsHandler != nil {
		options.MetricsHandler = &guardedMetrics{inner: options.MetricsHandler, gate: b.gate}
	}
	if options.HeadersProvider != nil {
		options.HeadersProvider = &guardedHeaders{inner: options.HeadersProvider, gate: b.gate}
	}
	options.ContextPropagators = append([]workflow.ContextPropagator(nil), options.ContextPropagators...)
	for i, propagator := range options.ContextPropagators {
		options.ContextPropagators[i] = &guardedPropagator{inner: propagator.(BoundedContextPropagator), gate: b.gate}
	}
	return options, nil
}
