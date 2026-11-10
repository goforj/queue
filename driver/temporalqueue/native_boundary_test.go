package temporalqueue

import (
	"testing"

	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/workflow"
)

// nativeBoundaryFixture counts actual forwarding independently for each SDK inbound entry point.
type nativeBoundaryFixture struct {
	interceptor.WorkflowInboundInterceptorBase
	calls map[string]int
}

// ExecuteWorkflow supplies a successful native result as the rejection test's control.
func (f *nativeBoundaryFixture) ExecuteWorkflow(workflow.Context, *interceptor.ExecuteWorkflowInput) (any, error) {
	f.calls["workflow"]++
	return "native", nil
}

// HandleSignal records delivery to the application signal channel.
func (f *nativeBoundaryFixture) HandleSignal(workflow.Context, *interceptor.HandleSignalInput) error {
	f.calls["signal"]++
	return nil
}

// HandleQuery makes accidental execution of read handlers visible.
func (f *nativeBoundaryFixture) HandleQuery(workflow.Context, *interceptor.HandleQueryInput) (any, error) {
	f.calls["query"]++
	return "native", nil
}

// ValidateUpdate records application validation before SDK acceptance.
func (f *nativeBoundaryFixture) ValidateUpdate(workflow.Context, *interceptor.UpdateInput) error {
	f.calls["validate"]++
	return nil
}

// ExecuteUpdate covers SDK replay paths that omit update validation.
func (f *nativeBoundaryFixture) ExecuteUpdate(workflow.Context, *interceptor.UpdateInput) (any, error) {
	f.calls["update"]++
	return "native", nil
}

// TestNativeCallbacksRejectPropagatedContext checks every entry point and aborts accepted-update replay instead of returning an alternate outcome.
func TestNativeCallbacksRejectPropagatedContext(t *testing.T) {
	for _, category := range []string{"", "invalid", "unavailable"} {
		t.Run("category="+category, func(t *testing.T) {
			env := guardedWorkflowEnvironment(t)
			env.ExecuteWorkflow(func(ctx workflow.Context) error {
				ctx = workflow.WithValue(ctx, headerRejectionKey{}, category)
				fixture := &nativeBoundaryFixture{calls: make(map[string]int)}
				guard := &guardedInboundWorkflow{WorkflowInboundInterceptorBase: interceptor.WorkflowInboundInterceptorBase{Next: fixture}, gate: newAdmission()}
				result, workflowErr := guard.ExecuteWorkflow(ctx, &interceptor.ExecuteWorkflowInput{})
				queryResult, queryErr := guard.HandleQuery(ctx, &interceptor.HandleQueryInput{})
				updateResult, updateErr, updatePanicked := invokeNativeUpdateBoundary(guard, ctx)
				if updatePanicked != (category != "") {
					t.Fatal("accepted update did not abort rejected replay", category)
				}
				validateErr := guard.ValidateUpdate(ctx, &interceptor.UpdateInput{})
				signalErr := guard.HandleSignal(ctx, &interceptor.HandleSignalInput{})
				for name, err := range map[string]error{"workflow": workflowErr, "query": queryErr, "update": updateErr, "validate": validateErr, "signal": signalErr} {
					wantError := category != "" && (name != "signal" || category == "unavailable")
					if (err != nil) != wantError {
						t.Fatalf("%s rejection changed: category=%q err=%v", name, category, err)
					}
					wantCalls := 0
					if category == "" {
						wantCalls = 1
					}
					if fixture.calls[name] != wantCalls {
						t.Fatal("native handler crossed rejected boundary", category, name, fixture.calls)
					}
					if err != nil {
						requirePropagationCategory(t, err, category == "unavailable")
					}
				}
				if category == "" && (result != "native" || queryResult != "native" || updateResult != "native") {
					t.Fatal("native results changed", result, queryResult, updateResult)
				}
				return nil
			})
			if err := env.GetWorkflowError(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// invokeNativeUpdateBoundary distinguishes replay abortion from a returned update outcome at the SDK boundary.
func invokeNativeUpdateBoundary(guard *guardedInboundWorkflow, ctx workflow.Context) (result any, err error, panicked bool) {
	defer func() {
		if value := recover(); value != nil {
			panicked = true
			err = value.(error)
		}
	}()
	result, err = guard.ExecuteUpdate(ctx, &interceptor.UpdateInput{})
	return result, err, false
}
