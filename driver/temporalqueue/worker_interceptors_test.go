package temporalqueue

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nexus-rpc/sdk-go/nexus"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/workflow"
)

// workerFactoryFixture models user interceptor setup independently of worker poller implementation.
type workerFactoryFixture struct {
	interceptor.WorkerInterceptorBase
	mode  string
	calls int
}

// factory distinguishes setup failures from genuine application-body failures.
func (f *workerFactoryFixture) factory() {
	f.calls++
	if f.mode == "panic" {
		panic("private worker factory diagnostic")
	}
}

// InterceptActivity preserves the supplied activity chain after setup succeeds.
func (f *workerFactoryFixture) InterceptActivity(_ context.Context, next interceptor.ActivityInboundInterceptor) interceptor.ActivityInboundInterceptor {
	f.factory()
	return next
}

// InterceptWorkflow preserves the supplied workflow chain after setup succeeds.
func (f *workerFactoryFixture) InterceptWorkflow(_ workflow.Context, next interceptor.WorkflowInboundInterceptor) interceptor.WorkflowInboundInterceptor {
	f.factory()
	return next
}

// InterceptNexusOperation preserves the supplied Nexus chain after setup succeeds.
func (f *workerFactoryFixture) InterceptNexusOperation(_ context.Context, next interceptor.NexusOperationInboundInterceptor) interceptor.NexusOperationInboundInterceptor {
	f.factory()
	return next
}

// activityInboundFixture keeps native body results distinct from initialization rejection.
type activityInboundFixture struct {
	interceptor.ActivityInboundInterceptorBase
	mode                    string
	err                     error
	initializations, bodies int
}

// Init exposes controlled adapter initialization failure without executing the activity body.
func (i *activityInboundFixture) Init(interceptor.ActivityOutboundInterceptor) error {
	i.initializations++
	if i.mode == "panic" {
		panic("private activity initialization diagnostic")
	}
	return i.err
}

// ExecuteActivity represents a native body whose result must survive successful initialization.
func (i *activityInboundFixture) ExecuteActivity(context.Context, *interceptor.ExecuteActivityInput) (any, error) {
	i.bodies++
	return "native activity result", i.err
}

// workflowInboundFixture exposes native panic policy after initialization has already completed.
type workflowInboundFixture struct {
	interceptor.WorkflowInboundInterceptorBase
	mode                    string
	err                     error
	initializations, bodies int
}

// Init simulates adapter setup rather than a business-workflow failure.
func (i *workflowInboundFixture) Init(interceptor.WorkflowOutboundInterceptor) error {
	i.initializations++
	if i.mode == "init panic" {
		panic("private workflow initialization diagnostic")
	}
	return i.err
}

// ExecuteWorkflow deliberately retains native errors and genuine workflow panics for SDK handling.
func (i *workflowInboundFixture) ExecuteWorkflow(workflow.Context, *interceptor.ExecuteWorkflowInput) (any, error) {
	i.bodies++
	if i.mode == "body panic" {
		panic("genuine workflow panic")
	}
	return "native workflow result", i.err
}

// nexusInboundFixture records typed service results without relying on network dispatch.
type nexusInboundFixture struct {
	interceptor.NexusOperationInboundInterceptorBase
	err                              error
	initializations, starts, cancels int
	result                           nexus.HandlerStartOperationResult[any]
}

// Init retains the native service initialization error identity.
func (i *nexusInboundFixture) Init(context.Context, interceptor.NexusOperationOutboundInterceptor) error {
	i.initializations++
	return i.err
}

// StartOperation returns an independently chosen typed result and error.
func (i *nexusInboundFixture) StartOperation(context.Context, interceptor.NexusStartOperationInput) (nexus.HandlerStartOperationResult[any], error) {
	i.starts++
	return i.result, i.err
}

// CancelOperation retains native service cancellation errors.
func (i *nexusInboundFixture) CancelOperation(context.Context, interceptor.NexusCancelOperationInput) error {
	i.cancels++
	return i.err
}

// TestActivityInterceptorInitializationFailureIsTerminal rejects failed factories and setup panics before business code.
func TestActivityInterceptorInitializationFailureIsTerminal(t *testing.T) {
	for _, mode := range []string{"success", "factory panic", "init panic", "init error", "closed"} {
		t.Run(mode, func(t *testing.T) {
			gate := newAdmission()
			factory := &workerFactoryFixture{}
			if mode == "factory panic" {
				factory.mode = "panic"
			}
			if mode == "closed" {
				gate.close()
			}
			next := &activityInboundFixture{}
			if mode == "init panic" {
				next.mode = "panic"
			}
			if mode == "init error" {
				next.err = errors.New("native initialization error")
			}
			tracked := &trackedWorkerInterceptor{inner: factory, gate: gate}
			inbound := tracked.InterceptActivity(t.Context(), next)
			err := inbound.Init(nil)
			if mode == "success" {
				if err != nil {
					t.Fatal(err)
				}
				result, err := inbound.ExecuteActivity(t.Context(), &interceptor.ExecuteActivityInput{})
				if result != "native activity result" || err != nil || next.bodies != 1 {
					t.Fatal("native result changed", result, err)
				}
			} else {
				if err == nil || strings.Contains(err.Error(), "private") || next.bodies != 0 {
					t.Fatal("unsafe activity initialization outcome", err)
				}
				if mode == "factory panic" || mode == "init panic" {
					requirePropagationCategory(t, err, false)
				}
				if mode == "init error" && err != next.err {
					t.Fatal("native setup error identity lost")
				}
			}
			if mode == "closed" && factory.calls != 0 {
				t.Fatal("late activity factory invoked")
			}
			if err := gate.wait(t.Context()); err != nil {
				t.Fatal(err)
			}
		})
	}
	gate := newAdmission()
	next := &activityInboundFixture{}
	inbound := (&trackedWorkerInterceptor{inner: &workerFactoryFixture{}, gate: gate}).InterceptActivity(t.Context(), next)
	gate.close()
	if err := inbound.Init(nil); !errors.Is(err, errAdmissionClosed) || next.initializations != 0 {
		t.Fatal("late activity initialization touched provider", err)
	}
}

// TestWorkflowInitializationRejectsDeterministicallyWithoutChangingNativePanicPolicy separates adapter rejection from genuine workflow execution.
func TestWorkflowInitializationRejectsDeterministicallyWithoutChangingNativePanicPolicy(t *testing.T) {
	for _, mode := range []string{"success", "factory panic", "init panic", "init error", "closed", "closed before init", "body error", "body panic"} {
		t.Run(mode, func(t *testing.T) {
			gate := newAdmission()
			factory := &workerFactoryFixture{}
			if mode == "factory panic" {
				factory.mode = "panic"
			}
			if mode == "closed" {
				gate.close()
			}
			next := &workflowInboundFixture{mode: mode}
			if mode == "init error" {
				next.err = errors.New("private workflow setup diagnostic")
			}
			if mode == "body error" {
				next.err = errors.New("native business error")
			}
			tracked := &trackedWorkerInterceptor{inner: factory, gate: gate}
			inbound := tracked.InterceptWorkflow(nil, next)
			if mode == "closed before init" {
				gate.close()
			}
			// Native body errors are installed after successful adapter initialization.
			native := next.err
			if mode == "body error" {
				next.err = nil
			}
			if err := inbound.Init(nil); err != nil {
				t.Fatal("workflow initialization must defer rejection to entry", err)
			}
			if mode == "body error" {
				next.err = native
			}
			if mode == "body panic" {
				var recovered any
				func() {
					defer func() { recovered = recover() }()
					_, _ = inbound.ExecuteWorkflow(nil, &interceptor.ExecuteWorkflowInput{})
				}()
				if recovered != "genuine workflow panic" {
					t.Fatal("native workflow panic policy changed", recovered)
				}
				return
			}
			result, err := inbound.ExecuteWorkflow(nil, &interceptor.ExecuteWorkflowInput{})
			if mode == "success" || mode == "body error" {
				if result != "native workflow result" || err != next.err || next.bodies != 1 {
					t.Fatal("native workflow result/error identity changed", result, err)
				}
			} else {
				requirePropagationCategory(t, err, false)
				if next.bodies != 0 {
					t.Fatal("rejected workflow body executed")
				}
			}
			if mode == "closed" && factory.calls != 0 {
				t.Fatal("late workflow factory invoked")
			}
		})
	}
}

// TestNexusInterceptionRetainsNativeResultsAndLateFencing verifies factories, initialization, start and cancellation admission independently.
func TestNexusInterceptionRetainsNativeResultsAndLateFencing(t *testing.T) {
	for _, mode := range []string{"success", "native error", "factory panic", "closed", "closed after init"} {
		t.Run(mode, func(t *testing.T) {
			gate := newAdmission()
			factory := &workerFactoryFixture{}
			if mode == "factory panic" {
				factory.mode = "panic"
			}
			if mode == "closed" {
				gate.close()
			}
			result := &nexus.HandlerStartOperationResultSync[any]{Value: "native Nexus result"}
			next := &nexusInboundFixture{result: result}
			if mode == "native error" {
				next.err = errors.New("native Nexus error")
			}
			tracked := &trackedWorkerInterceptor{inner: factory, gate: gate}
			inbound := tracked.InterceptNexusOperation(t.Context(), next)
			initErr := inbound.Init(t.Context(), nil)
			if mode == "factory panic" {
				if initErr == nil || next.initializations != 0 {
					t.Fatal("failed Nexus factory initialized application code")
				}
				return
			}
			if mode == "closed after init" {
				gate.close()
			}
			actual, startErr := inbound.StartOperation(t.Context(), interceptor.NexusStartOperationInput{})
			cancelErr := inbound.CancelOperation(t.Context(), interceptor.NexusCancelOperationInput{})
			if mode == "success" || mode == "native error" {
				if actual != result || initErr != next.err || startErr != next.err || cancelErr != next.err || next.initializations != 1 || next.starts != 1 || next.cancels != 1 {
					t.Fatal("native Nexus behavior changed")
				}
			} else {
				if startErr == nil || cancelErr == nil || next.starts != 0 || next.cancels != 0 {
					t.Fatal("late/rejected Nexus callback invoked")
				}
				if mode == "closed" && (factory.calls != 0 || initErr == nil) {
					t.Fatal("closed Nexus factory entered")
				}
				if mode == "factory panic" && initErr == nil {
					t.Fatal("failed Nexus factory initialized")
				}
			}
			if err := gate.wait(t.Context()); err != nil {
				t.Fatal(err)
			}
		})
	}
	gate := newAdmission()
	next := &nexusInboundFixture{}
	inbound := (&resourceInterceptor{gate: gate}).InterceptNexusOperation(t.Context(), next)
	gate.close()
	if err := inbound.Init(t.Context(), nil); !errors.Is(err, errAdmissionClosed) || next.initializations != 0 {
		t.Fatal("resource Nexus initialization escaped admission", err)
	}
}
