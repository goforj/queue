package temporalqueue

import (
	"errors"
	"testing"

	"github.com/goforj/queue/internal/runtimehook"

	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/temporal"
)

// failureFactoryScopeFixture delegates actual protection while controlling only contextual factory construction.
type failureFactoryScopeFixture struct {
	ProtectedFailureConverter
	mode  string
	calls int
}

// WithSerializationContext preserves protected conversion unless the selected factory failure is requested.
func (f *failureFactoryScopeFixture) WithSerializationContext(ctx converter.SerializationContext) converter.FailureConverter {
	f.calls++
	switch f.mode {
	case "panic":
		panic("private factory diagnostic")
	case "nil":
		return nil
	case "rejected":
		return rejectedFailureConverter{}
	case "unavailable":
		return rejectedFailureConverter{unavailable: true}
	}
	return f
}

// captureFailureFactoryBinding distinguishes task abortion from a safe client/activity rejection object.
func captureFailureFactoryBinding(guard *guardedFailureConverter, ctx converter.SerializationContext) (result converter.FailureConverter, aborted any) {
	defer func() { aborted = recover() }()
	result = guard.WithSerializationContext(ctx)
	return result, nil
}

// TestFailureFactoryScopePreservesTaskAndClientBoundaries checks contextual failures before they can rewrite recorded activity outcomes.
func TestFailureFactoryScopePreservesTaskAndClientBoundaries(t *testing.T) {
	for _, scope := range []string{"client workflow", "worker activity", "worker workflow", "workflow activity"} {
		for _, mode := range []string{"success", "panic", "nil", "rejected", "unavailable", "closed", "data rejected", "data unavailable"} {
			t.Run(scope+"/"+mode, func(t *testing.T) {
				data := newFailureProtectionFixture(t)
				protected, err := NewProtectedFailureConverter(data)
				if err != nil {
					t.Fatal(err)
				}
				fixture := &failureFactoryScopeFixture{ProtectedFailureConverter: protected, mode: "success"}
				dataGate := newAdmission()
				dc, err := newGuardedConverter(data, DefaultSerializationLimits(), dataGate)
				if err != nil {
					t.Fatal(err)
				}
				dc, err = newGuardedConverter(dc, DefaultSerializationLimits(), newAdmission())
				if err != nil {
					t.Fatal(err)
				}
				gate := newAdmission()
				binding, err := newGuardedFailureConverter(fixture, dc, gate)
				if err != nil {
					t.Fatal(err)
				}
				guard := binding
				if scope != "client workflow" {
					guard = &guardedFailureConverter{inner: binding, dc: dc, gate: gate, protected: true, workflowTask: true}
				}
				rootContext := converter.WorkflowSerializationContext{Namespace: "default", WorkflowID: "native"}
				if scope == "workflow activity" {
					guard = guard.WithSerializationContext(rootContext).(*guardedFailureConverter)
				}
				fixture.mode = mode
				var ctx converter.SerializationContext = rootContext
				if scope == "worker activity" || scope == "workflow activity" {
					ctx = converter.ActivitySerializationContext{Namespace: "default", WorkflowID: "native", ActivityType: "fixture"}
				}
				if mode == "closed" {
					gate.close()
				}
				if mode == "data rejected" {
					data.mode = "context panic"
				}
				if mode == "data unavailable" {
					dataGate.close()
				}
				bound, aborted := captureFailureFactoryBinding(guard, ctx)
				wantAbort := (scope == "worker workflow" || scope == "workflow activity") && mode != "success"
				if (aborted != nil) != wantAbort {
					t.Fatal("factory crossed task/client boundary", aborted)
				}
				unavailable := mode == "unavailable" || mode == "closed" || mode == "data unavailable"
				if wantAbort {
					cause, ok := aborted.(error)
					if !ok {
						t.Fatal("task abortion lost safe error category", aborted)
					}
					requirePropagationCategory(t, cause, unavailable)
					return
				}
				original := temporal.NewApplicationError("synthetic failure", "native")
				result := bound.FailureToError(bound.ErrorToFailure(original))
				if mode == "success" {
					var application *temporal.ApplicationError
					if !errors.As(result, &application) || application.Type() != "native" {
						t.Fatal("valid contextual failure taxonomy changed", result)
					}
					if scope != "client workflow" && !bound.(*guardedFailureConverter).workflowTask {
						t.Fatal("context copy lost workflow-task policy")
					}
					if scope == "workflow activity" && !bound.(*guardedFailureConverter).workflowContext {
						t.Fatal("activity context lost workflow execution ownership")
					}
				} else if unavailable {
					requirePropagationCategory(t, result, true)
				} else {
					requireFailureProtocol(t, result)
				}
			})
		}
	}
}

// TestFailureFactoryPolicyIsPrivateToWorkersAndReplay verifies real resource wiring without changing its public binding client.
func TestFailureFactoryPolicyIsPrivateToWorkersAndReplay(t *testing.T) {
	history, definition := recordedJournalFixture(t)
	protected, err := NewProtectedFailureConverter(newFailureProtectionFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	factory := &failureFactoryScopeFixture{ProtectedFailureConverter: protected, mode: "success"}
	cfg := testConfig()
	cfg.DefaultQueue = definition.Queue
	cfg.ClientOptions.FailureConverter = factory
	q, err := NewWithConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = q.Shutdown(t.Context()) })
	backend := runtimehook.DriverResource(q).(*backend)
	binding := backend.clientBinding.options.FailureConverter.(*guardedFailureConverter)
	if binding.workflowTask {
		t.Fatal("public binding inherited workflow-task policy")
	}
	options, err := backend.workerClientOptions()
	if err != nil {
		t.Fatal(err)
	}
	if !options.FailureConverter.(*guardedFailureConverter).workflowTask {
		t.Fatal("SDK worker lost workflow-task factory policy")
	}
	replay, err := NewWorkflowReplayer(q)
	if err != nil {
		t.Fatal(err)
	}
	if binding.workflowTask {
		t.Fatal("replayer mutated shared binding policy")
	}
	if err := replay.ReplayWorkflowHistory(nil, history); err != nil {
		t.Fatal("valid retained history failed replay", err)
	}
	factory.mode = "panic"
	_, aborted := captureFailureFactoryBinding(binding, converter.WorkflowSerializationContext{Namespace: "default", WorkflowID: definition.ID})
	if aborted != nil {
		t.Fatal("public client factory rejection became a panic", aborted)
	}
	rejected := func() (failed bool) {
		defer func() {
			if recover() != nil {
				failed = true
			}
		}()
		return replay.ReplayWorkflowHistory(nil, history) != nil
	}()
	if !rejected {
		t.Fatal("retained history replay accepted a rejected failure factory")
	}
}
