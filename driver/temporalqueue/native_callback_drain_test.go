package temporalqueue

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/workflow"
)

// synchronousInboundFixture isolates a native message interceptor callback from long-lived workflow execution.
type synchronousInboundFixture struct {
	interceptor.WorkflowInboundInterceptorBase
	call func() error
}

// HandleQuery preserves a native result after the callback's actual return.
func (f *synchronousInboundFixture) HandleQuery(workflow.Context, *interceptor.HandleQueryInput) (any, error) {
	return 7, f.call()
}

// HandleSignal supplies the synchronous signal-dispatch callback.
func (f *synchronousInboundFixture) HandleSignal(workflow.Context, *interceptor.HandleSignalInput) error {
	return f.call()
}

// ValidateUpdate supplies validation before update acceptance, rather than the potentially suspended update body.
func (f *synchronousInboundFixture) ValidateUpdate(workflow.Context, *interceptor.UpdateInput) error {
	return f.call()
}

// TestSynchronousNativeCallbacksRetainAdmission prevents cleanup across live queries, signal dispatch and update validation.
func TestSynchronousNativeCallbacksRetainAdmission(t *testing.T) {
	for _, method := range []string{"query", "signal", "validation"} {
		t.Run(method, func(t *testing.T) {
			var flowCtx workflow.Context
			env := guardedWorkflowEnvironment(t)
			env.ExecuteWorkflow(func(ctx workflow.Context) error { flowCtx = ctx; return nil })
			if err := env.GetWorkflowError(); err != nil {
				t.Fatal(err)
			}
			gate := newAdmission()
			entered, release := make(chan struct{}), make(chan struct{})
			defer func() {
				select {
				case <-release:
				default:
					close(release)
				}
			}()
			var calls atomic.Int32
			fixture := &synchronousInboundFixture{call: func() error {
				calls.Add(1)
				close(entered)
				<-release
				return nil
			}}
			guard := &guardedInboundWorkflow{gate: gate, WorkflowInboundInterceptorBase: interceptor.WorkflowInboundInterceptorBase{Next: fixture}}
			invoke := func() error {
				switch method {
				case "query":
					result, err := guard.HandleQuery(flowCtx, &interceptor.HandleQueryInput{})
					if err == nil && result != 7 {
						return errors.New("native query result changed")
					}
					return err
				case "signal":
					return guard.HandleSignal(flowCtx, &interceptor.HandleSignalInput{})
				default:
					return guard.ValidateUpdate(flowCtx, &interceptor.UpdateInput{})
				}
			}
			done := make(chan error, 1)
			go func() { done <- invoke() }()
			ctx, stop := context.WithTimeout(t.Context(), 5*time.Second)
			defer stop()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("native callback never entered", ctx.Err())
			}
			gate.close()
			requirePropagationCategory(t, invoke(), true)
			short, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
			if err := gate.wait(short); !errors.Is(err, context.DeadlineExceeded) {
				cancel()
				t.Fatal("cleanup escaped live native callback", err)
			}
			cancel()
			close(release)
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("native callback did not return", ctx.Err())
			}
			if err := gate.wait(ctx); err != nil || calls.Load() != 1 {
				t.Fatal("native callback accounting changed", calls.Load(), err)
			}
		})
	}
}
