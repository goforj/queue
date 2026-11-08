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

// Descriptor carries declared capabilities separately from adapted interfaces.
// New drivers must supply an explicit mode; only the legacy bridge defaults it.
type Descriptor struct {
	Backend          any
	WorkerFactory    WorkerFactory
	WorkflowMode     WorkflowMode
	NewCoordinator   func(*WorkflowBinding) (NativeCoordinator, error)
	ValidateSettings func(Settings) error
}

// BuildQueueFromDescriptorFunc constructs a queue while preserving explicit capabilities.
type BuildQueueFromDescriptorFunc func(cfg any, observer any, descriptor Descriptor, opts []any) (any, error)

// BuildQueueFromDescriptor is installed by the root package to avoid an import cycle.
var BuildQueueFromDescriptor BuildQueueFromDescriptorFunc
