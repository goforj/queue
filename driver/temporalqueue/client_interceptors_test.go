package temporalqueue

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/interceptor"
)

// workflowUpdateHandleFixture supplies an opaque SDK handle whose identity must survive interception.
type workflowUpdateHandleFixture struct{ client.WorkflowUpdateHandle }

// workflowRunFixture supplies an opaque SDK handle whose identity must survive interception.
type workflowRunFixture struct{ client.WorkflowRun }

// encodedValueFixture supplies an opaque SDK handle whose identity must survive interception.
type encodedValueFixture struct{ converter.EncodedValue }

// scheduleHandleFixture supplies an opaque SDK handle whose identity must survive interception.
type scheduleHandleFixture struct{ client.ScheduleHandle }

// activityHandleFixture supplies an opaque SDK handle whose identity must survive interception.
type activityHandleFixture struct{ client.ActivityHandle }

// nexusHandleFixture supplies an opaque SDK handle whose identity must survive interception.
type nexusHandleFixture struct{ client.NexusOperationHandle }

// clientOutboundProbe records native request identity and returns independently chosen SDK results or service errors.
type clientOutboundProbe struct {
	interceptor.ClientOutboundInterceptorBase
	input     any
	ctx       context.Context
	result    any
	err       error
	panicCall bool
	calls     int
}

// capture distinguishes an admitted native call from a rejected call that must not touch disposed dependencies.
func (p *clientOutboundProbe) capture(ctx context.Context, input any) {
	p.ctx = ctx
	p.input = input
	p.calls++
	if p.panicCall {
		panic("private interceptor diagnostic")
	}
}

// UpdateWorkflow models native SDK result and error semantics for the ownership contract.
func (p *clientOutboundProbe) UpdateWorkflow(ctx context.Context, in *interceptor.ClientUpdateWorkflowInput) (client.WorkflowUpdateHandle, error) {
	p.capture(ctx, in)
	return p.result.(client.WorkflowUpdateHandle), p.err
}

// UpdateWithStartWorkflow models native SDK result and error semantics for the ownership contract.
func (p *clientOutboundProbe) UpdateWithStartWorkflow(ctx context.Context, in *interceptor.ClientUpdateWithStartWorkflowInput) (client.WorkflowUpdateHandle, error) {
	p.capture(ctx, in)
	return p.result.(client.WorkflowUpdateHandle), p.err
}

// ExecuteWorkflow models native SDK result and error semantics for the ownership contract.
func (p *clientOutboundProbe) ExecuteWorkflow(ctx context.Context, in *interceptor.ClientExecuteWorkflowInput) (client.WorkflowRun, error) {
	p.capture(ctx, in)
	return p.result.(client.WorkflowRun), p.err
}

// SignalWorkflow models native SDK result and error semantics for the ownership contract.
func (p *clientOutboundProbe) SignalWorkflow(ctx context.Context, in *interceptor.ClientSignalWorkflowInput) error {
	p.capture(ctx, in)
	return p.err
}

// SignalWithStartWorkflow models native SDK result and error semantics for the ownership contract.
func (p *clientOutboundProbe) SignalWithStartWorkflow(ctx context.Context, in *interceptor.ClientSignalWithStartWorkflowInput) (client.WorkflowRun, error) {
	p.capture(ctx, in)
	return p.result.(client.WorkflowRun), p.err
}

// CancelWorkflow models native SDK result and error semantics for the ownership contract.
func (p *clientOutboundProbe) CancelWorkflow(ctx context.Context, in *interceptor.ClientCancelWorkflowInput) error {
	p.capture(ctx, in)
	return p.err
}

// TerminateWorkflow models native SDK result and error semantics for the ownership contract.
func (p *clientOutboundProbe) TerminateWorkflow(ctx context.Context, in *interceptor.ClientTerminateWorkflowInput) error {
	p.capture(ctx, in)
	return p.err
}

// QueryWorkflow models native SDK result and error semantics for the ownership contract.
func (p *clientOutboundProbe) QueryWorkflow(ctx context.Context, in *interceptor.ClientQueryWorkflowInput) (converter.EncodedValue, error) {
	p.capture(ctx, in)
	return p.result.(converter.EncodedValue), p.err
}

// DescribeWorkflow models native SDK result and error semantics for the ownership contract.
func (p *clientOutboundProbe) DescribeWorkflow(ctx context.Context, in *interceptor.ClientDescribeWorkflowInput) (*interceptor.ClientDescribeWorkflowOutput, error) {
	p.capture(ctx, in)
	return p.result.(*interceptor.ClientDescribeWorkflowOutput), p.err
}

// CreateSchedule models native SDK result and error semantics for the ownership contract.
func (p *clientOutboundProbe) CreateSchedule(ctx context.Context, in *interceptor.ScheduleClientCreateInput) (client.ScheduleHandle, error) {
	p.capture(ctx, in)
	return p.result.(client.ScheduleHandle), p.err
}

// ExecuteActivity models native SDK result and error semantics for the ownership contract.
func (p *clientOutboundProbe) ExecuteActivity(ctx context.Context, in *interceptor.ClientExecuteActivityInput) (client.ActivityHandle, error) {
	p.capture(ctx, in)
	return p.result.(client.ActivityHandle), p.err
}

// GetActivityHandle models native SDK result and error semantics for the ownership contract.
func (p *clientOutboundProbe) GetActivityHandle(in *interceptor.ClientGetActivityHandleInput) client.ActivityHandle {
	p.capture(nil, in)
	return p.result.(client.ActivityHandle)
}

// CancelActivity models native SDK result and error semantics for the ownership contract.
func (p *clientOutboundProbe) CancelActivity(ctx context.Context, in *interceptor.ClientCancelActivityInput) error {
	p.capture(ctx, in)
	return p.err
}

// TerminateActivity models native SDK result and error semantics for the ownership contract.
func (p *clientOutboundProbe) TerminateActivity(ctx context.Context, in *interceptor.ClientTerminateActivityInput) error {
	p.capture(ctx, in)
	return p.err
}

// DescribeActivity models native SDK result and error semantics for the ownership contract.
func (p *clientOutboundProbe) DescribeActivity(ctx context.Context, in *interceptor.ClientDescribeActivityInput) (*interceptor.ClientDescribeActivityOutput, error) {
	p.capture(ctx, in)
	return p.result.(*interceptor.ClientDescribeActivityOutput), p.err
}

// PollActivityResult models native SDK result and error semantics for the ownership contract.
func (p *clientOutboundProbe) PollActivityResult(ctx context.Context, in *interceptor.ClientPollActivityResultInput) (*interceptor.ClientPollActivityResultOutput, error) {
	p.capture(ctx, in)
	return p.result.(*interceptor.ClientPollActivityResultOutput), p.err
}

// ExecuteNexusOperation models native SDK result and error semantics for the ownership contract.
func (p *clientOutboundProbe) ExecuteNexusOperation(ctx context.Context, in *interceptor.ClientExecuteNexusOperationInput) (client.NexusOperationHandle, error) {
	p.capture(ctx, in)
	return p.result.(client.NexusOperationHandle), p.err
}

// GetNexusOperationHandle models native SDK result and error semantics for the ownership contract.
func (p *clientOutboundProbe) GetNexusOperationHandle(in *interceptor.ClientGetNexusOperationHandleInput) client.NexusOperationHandle {
	p.capture(nil, in)
	return p.result.(client.NexusOperationHandle)
}

// CancelNexusOperation models native SDK result and error semantics for the ownership contract.
func (p *clientOutboundProbe) CancelNexusOperation(ctx context.Context, in *interceptor.ClientCancelNexusOperationInput) error {
	p.capture(ctx, in)
	return p.err
}

// TerminateNexusOperation models native SDK result and error semantics for the ownership contract.
func (p *clientOutboundProbe) TerminateNexusOperation(ctx context.Context, in *interceptor.ClientTerminateNexusOperationInput) error {
	p.capture(ctx, in)
	return p.err
}

// DescribeNexusOperation models native SDK result and error semantics for the ownership contract.
func (p *clientOutboundProbe) DescribeNexusOperation(ctx context.Context, in *interceptor.ClientDescribeNexusOperationInput) (*interceptor.ClientDescribeNexusOperationOutput, error) {
	p.capture(ctx, in)
	return p.result.(*interceptor.ClientDescribeNexusOperationOutput), p.err
}

// PollNexusOperationResult models native SDK result and error semantics for the ownership contract.
func (p *clientOutboundProbe) PollNexusOperationResult(ctx context.Context, in *interceptor.ClientPollNexusOperationResultInput) (*interceptor.ClientPollNexusOperationResultOutput, error) {
	p.capture(ctx, in)
	return p.result.(*interceptor.ClientPollNexusOperationResultOutput), p.err
}

// TestNativeClientInterceptionPreservesResultsErrorsAndLateFencing checks the public SDK operations, including standalone activities and Nexus.
func TestNativeClientInterceptionPreservesResultsErrorsAndLateFencing(t *testing.T) {
	cases := []struct {
		name               string
		input, result      any
		noContext, noError bool
	}{
		{"UpdateWorkflow", &interceptor.ClientUpdateWorkflowInput{}, &workflowUpdateHandleFixture{}, false, false},
		{"UpdateWithStartWorkflow", &interceptor.ClientUpdateWithStartWorkflowInput{}, &workflowUpdateHandleFixture{}, false, false},
		{"ExecuteWorkflow", &interceptor.ClientExecuteWorkflowInput{}, &workflowRunFixture{}, false, false},
		{"SignalWorkflow", &interceptor.ClientSignalWorkflowInput{}, nil, false, false},
		{"SignalWithStartWorkflow", &interceptor.ClientSignalWithStartWorkflowInput{}, &workflowRunFixture{}, false, false},
		{"CancelWorkflow", &interceptor.ClientCancelWorkflowInput{}, nil, false, false},
		{"TerminateWorkflow", &interceptor.ClientTerminateWorkflowInput{}, nil, false, false},
		{"QueryWorkflow", &interceptor.ClientQueryWorkflowInput{}, &encodedValueFixture{}, false, false},
		{"DescribeWorkflow", &interceptor.ClientDescribeWorkflowInput{}, &interceptor.ClientDescribeWorkflowOutput{}, false, false},
		{"CreateSchedule", &interceptor.ScheduleClientCreateInput{}, &scheduleHandleFixture{}, false, false},
		{"ExecuteActivity", &interceptor.ClientExecuteActivityInput{}, &activityHandleFixture{}, false, false},
		{"GetActivityHandle", &interceptor.ClientGetActivityHandleInput{}, &activityHandleFixture{}, true, true},
		{"CancelActivity", &interceptor.ClientCancelActivityInput{}, nil, false, false},
		{"TerminateActivity", &interceptor.ClientTerminateActivityInput{}, nil, false, false},
		{"DescribeActivity", &interceptor.ClientDescribeActivityInput{}, &interceptor.ClientDescribeActivityOutput{}, false, false},
		{"PollActivityResult", &interceptor.ClientPollActivityResultInput{}, &interceptor.ClientPollActivityResultOutput{}, false, false},
		{"ExecuteNexusOperation", &interceptor.ClientExecuteNexusOperationInput{}, &nexusHandleFixture{}, false, false},
		{"GetNexusOperationHandle", &interceptor.ClientGetNexusOperationHandleInput{}, &nexusHandleFixture{}, true, true},
		{"CancelNexusOperation", &interceptor.ClientCancelNexusOperationInput{}, nil, false, false},
		{"TerminateNexusOperation", &interceptor.ClientTerminateNexusOperationInput{}, nil, false, false},
		{"DescribeNexusOperation", &interceptor.ClientDescribeNexusOperationInput{}, &interceptor.ClientDescribeNexusOperationOutput{}, false, false},
		{"PollNexusOperationResult", &interceptor.ClientPollNexusOperationResultInput{}, &interceptor.ClientPollNexusOperationResultOutput{}, false, false},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			for _, mode := range []string{"success", "native error", "panic", "closed", "rejected"} {
				t.Run(mode, func(t *testing.T) {
					native := errors.New("native service result")
					probe := &clientOutboundProbe{result: item.result, panicCall: mode == "panic"}
					if mode == "native error" {
						probe.err = native
					}
					gate := newAdmission()
					if mode == "closed" {
						gate.close()
					}
					guard := &trackedClientOutbound{ClientOutboundInterceptorBase: interceptor.ClientOutboundInterceptorBase{Next: probe}, gate: gate, rejected: mode == "rejected"}
					args := []reflect.Value{reflect.ValueOf(item.input)}
					if !item.noContext {
						args = append([]reflect.Value{reflect.ValueOf(t.Context())}, args...)
					}
					values := reflect.ValueOf(guard).MethodByName(item.name).Call(args)
					var err error
					if !item.noError && !values[len(values)-1].IsNil() {
						err = values[len(values)-1].Interface().(error)
					}
					if mode == "closed" || mode == "rejected" {
						if probe.calls != 0 {
							t.Fatal("late native callback touched disposed interceptor")
						}
						if !item.noError && err == nil {
							t.Fatal("rejected native call reported success")
						}
					} else {
						expectedContext := t.Context()
						if item.noContext {
							expectedContext = nil
						}
						if probe.calls != 1 || probe.input != item.input || probe.ctx != expectedContext {
							t.Fatal("native input/context identity changed")
						}
						if mode == "panic" {
							if !item.noError && (err == nil || strings.Contains(err.Error(), "private")) {
								t.Fatal("interceptor panic escaped redaction", err)
							}
						} else {
							if !item.noError && err != probe.err {
								t.Fatal("native service error identity changed", err)
							}
							if item.result != nil && values[0].Interface() != item.result {
								t.Fatal("native SDK result identity changed")
							}
						}
					}
					if err := gate.wait(t.Context()); err != nil {
						t.Fatal("native callback leaked admission", err)
					}
				})
			}
		})
	}
}

// clientFactoryProbe exercises initialization rejection separately from genuine native call errors.
type clientFactoryProbe struct {
	interceptor.ClientInterceptorBase
	mode  string
	calls int
}

// InterceptClient exposes factory failures before any outbound application callback can run.
func (p *clientFactoryProbe) InterceptClient(next interceptor.ClientOutboundInterceptor) interceptor.ClientOutboundInterceptor {
	p.calls++
	if p.mode == "panic" {
		panic("private client factory diagnostic")
	}
	return next
}

// TestClientFactoryFailureFencesOutboundChain ensures factory rejection cannot expose a partially initialized interceptor.
func TestClientFactoryFailureFencesOutboundChain(t *testing.T) {
	for _, mode := range []string{"success", "panic", "closed"} {
		t.Run(mode, func(t *testing.T) {
			factory := &clientFactoryProbe{mode: mode}
			gate := newAdmission()
			if mode == "closed" {
				gate.close()
			}
			guard := &trackedClientInterceptor{inner: factory, gate: gate}
			probe := &clientOutboundProbe{result: &workflowRunFixture{}}
			output := guard.InterceptClient(probe)
			_, err := output.ExecuteWorkflow(t.Context(), &interceptor.ClientExecuteWorkflowInput{})
			if mode == "success" {
				if err != nil || probe.calls != 1 {
					t.Fatal("valid factory chain lost", err)
				}
			} else {
				if err == nil || strings.Contains(err.Error(), "private") || probe.calls != 0 {
					t.Fatal("rejected factory exposed outbound chain", err)
				}
			}
			if mode == "closed" && factory.calls != 0 {
				t.Fatal("late factory invoked")
			}
		})
	}
}
