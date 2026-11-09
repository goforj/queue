package temporalqueue

import (
	"context"
	"errors"
	"testing"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

// staticProtectedFailuresFixture hides an optional SDK context interface while retaining the actual approved failure protection.
type staticProtectedFailuresFixture struct{ converter.FailureConverter }

// ProtectsFailureDetails retains the factory converter's bounded authenticated protection.
func (*staticProtectedFailuresFixture) ProtectsFailureDetails() bool { return true }

// TestContextIndependentProtectedFailureConverterPreservesNativeErrors uses a conforming context-independent converter through actual SDK workflow conversion.
func TestContextIndependentProtectedFailureConverterPreservesNativeErrors(t *testing.T) {
	dc := newFailureProtectionFixture(t)
	inner, err := NewProtectedFailureConverter(dc)
	if err != nil {
		t.Fatal(err)
	}
	configured := &staticProtectedFailuresFixture{FailureConverter: inner}
	binding, err := NewClientBinding(client.Options{DataConverter: dc, FailureConverter: configured}, DefaultSerializationLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer binding.Close(context.Background())
	original := temporal.NewApplicationError("synthetic native failure", "native.retryable")
	control := configured.ErrorToFailure(original)
	if control.GetApplicationFailureInfo().GetType() != "native.retryable" || control.EncodedAttributes == nil {
		t.Fatal("control converter did not protect native failure")
	}
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.SetDataConverter(binding.options.DataConverter)
	env.SetFailureConverter(binding.options.FailureConverter)
	env.ExecuteWorkflow(func(workflow.Context) error { return original })
	var application *temporal.ApplicationError
	if !errors.As(env.GetWorkflowError(), &application) {
		t.Fatal(env.GetWorkflowError())
	}
	if application.Type() != "native.retryable" || application.NonRetryable() {
		t.Fatalf("valid native error changed to type=%s nonretryable=%v", application.Type(), application.NonRetryable())
	}
}
