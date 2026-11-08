package runtimehook

import (
	"context"
	"time"

	"github.com/goforj/queue/internal/workflow"
)

// WorkflowMode selects exactly one coordinator authority for a driver resource.
type WorkflowMode string

const (
	// WorkflowBuiltIn retains the established store-backed coordinator.
	WorkflowBuiltIn WorkflowMode = "built-in"
	// WorkflowNative delegates coordination to the driver bound to the same runtime.
	WorkflowNative WorkflowMode = "native"
	// WorkflowUnsupported explicitly disables portable workflows without fallback.
	WorkflowUnsupported WorkflowMode = "unsupported"
)

// Settings retains typed effective options and explicit caller provenance.
// Optional drivers validate these before root registration or worker creation.
type Settings struct {
	ResourceID              string
	ResourceIDSupplied      bool
	Store                   workflow.Store
	StoreSupplied           bool
	Clock                   func() time.Time
	ClockSupplied           bool
	Middlewares             []workflow.Middleware
	MiddlewareSupplied      bool
	ObserverSupplied        bool
	Workers                 int
	WorkersSupplied         bool
	HandlerContextDecorator func(context.Context) context.Context
	DecoratorSupplied       bool
	LegacyDirectEnvelope    bool
	LegacyEnvelopeSupplied  bool
}

// WorkflowBinding identifies the shared executor, lifecycle owner, and driver
// selected by the root constructor. Its fields cannot be replaced by a driver.
type WorkflowBinding struct {
	runtime any
	jobs    *workflow.Jobs
	driver  string
}

// NewWorkflowBinding creates the identity token validated by root construction.
func NewWorkflowBinding(runtime any, jobs *workflow.Jobs, driver string) *WorkflowBinding {
	return &WorkflowBinding{runtime: runtime, jobs: jobs, driver: driver}
}

// Runtime returns the selected physical lifecycle owner.
func (b *WorkflowBinding) Runtime() any { return b.runtime }

// Jobs returns the executor shared with ordinary application deliveries.
func (b *WorkflowBinding) Jobs() *workflow.Jobs { return b.jobs }

// Driver returns the driver identity established by root construction.
func (b *WorkflowBinding) Driver() string { return b.driver }

// NativeCoordinator binds native coordination to the root-selected job runtime.
type NativeCoordinator interface {
	workflow.Engine
	// Binding returns the exact identity supplied during construction.
	Binding() *WorkflowBinding
}

// DriverLifecycle preserves resource-wide worker configuration and fatal results.
// Done closes when the driver has a terminal runtime result; caller cancellation
// remains separate so a timed-out shutdown can be joined again.
type DriverLifecycle interface {
	// Binding identifies the same resource as ordinary delivery and coordination.
	Binding() *WorkflowBinding
	// SetWorkers records the desired remote worker capacity or a deferred conflict.
	SetWorkers(int)
	// ConfigurationError returns a deferred fluent configuration failure.
	ConfigurationError() error
	// Done returns a nonnil terminal-result notification channel.
	Done() <-chan struct{}
	// Err returns the stable fatal result after Done closes.
	Err() error
	// ShutdownTimeout returns the resource's positive aggregate shutdown budget.
	ShutdownTimeout() time.Duration
}

// Descriptor carries declared capabilities separately from adapted interfaces.
// New drivers must supply an explicit mode; only the legacy bridge defaults it.
// Construction callbacks roll back their own allocations on error. The supplying
// module retains cleanup ownership of the backend if queue construction fails.
type Descriptor struct {
	TranslateJobError func(error) error
	PanicJobError     func(any) error
	Backend           any
	WorkerFactory     WorkerFactory
	WorkflowMode      WorkflowMode
	NewCoordinator    func(*WorkflowBinding) (NativeCoordinator, error)
	NewLifecycle      func(*WorkflowBinding) (DriverLifecycle, error)
	ValidateSettings  func(Settings) error
}

// BuildQueueFromDescriptorFunc constructs a queue while preserving explicit capabilities.
type BuildQueueFromDescriptorFunc func(cfg any, observer any, descriptor Descriptor, opts []any) (any, error)

// BuildQueueFromDescriptor is installed by the root package to avoid an import cycle.
var BuildQueueFromDescriptor BuildQueueFromDescriptorFunc

// DriverResource resolves an optional module's resource capability without public SDK types.
var DriverResource func(any) any
