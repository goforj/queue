package temporalqueue

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/goforj/queue"
	"github.com/nexus-rpc/sdk-go/nexus"
	"go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

// registrationWorkflowFixture supplies a valid default-name workflow without application dependencies.
func registrationWorkflowFixture(workflow.Context) error { return nil }

// registrationActivityFixture supplies a valid default-name activity without service access.
func registrationActivityFixture(context.Context) error { return nil }

// activityMethodsFixture mixes supported and invalid SDK method signatures to exercise registration filtering.
type activityMethodsFixture struct{}

// Good represents a valid native activity method.
func (*activityMethodsFixture) Good(context.Context) error { return nil }

// Invalid represents a method with no SDK error result.
func (*activityMethodsFixture) Invalid() string { return "" }

// WorkflowOnly represents a deterministic workflow method that cannot register as an activity.
func (*activityMethodsFixture) WorkflowOnly(workflow.Context) error { return nil }

// FunctionResult represents a result the SDK cannot serialize as an activity value.
func (*activityMethodsFixture) FunctionResult() (func(), error) { return nil, nil }

// TooManyResults represents an invalid activity result arity.
func (*activityMethodsFixture) TooManyResults() (int, int, error) { return 0, 0, nil }

// rawActivityFixture models the SDK payload-level activity registration contract.
type rawActivityFixture struct{}

// Execute leaves payload conversion to the caller for native raw activity registration.
func (*rawActivityFixture) Execute(_ context.Context, input *common.Payloads) (*common.Payloads, error) {
	return input, nil
}

// ActivityType supplies the second method required by SDK payload-level dispatch.
func (*rawActivityFixture) ActivityType() activity.Type { return activity.Type{Name: "raw"} }

// GetFunction supplies the SDK metadata method, distinguishing this fixture from an Execute-only struct.
func (f *rawActivityFixture) GetFunction() interface{} { return f.Execute }

// TestRegistrationPlanPreservesOptionsAndFiltersInvalidMethods verifies logical catalog inspection does not alter SDK registration intent.
func TestRegistrationPlanPreservesOptionsAndFiltersInvalidMethods(t *testing.T) {
	var captured worker.Registry
	calls := 0
	service := &nexus.Service{Name: "reports"}
	cfg := testConfig()
	cfg.Register = func(registry worker.Registry) error {
		calls++
		captured = registry
		registry.RegisterWorkflow(registrationWorkflowFixture)
		registry.RegisterActivity(registrationActivityFixture)
		registry.RegisterActivityWithOptions(&activityMethodsFixture{}, activity.RegisterOptions{Name: "reports.", SkipInvalidStructFunctions: true})
		registry.RegisterActivityWithOptions(&rawActivityFixture{}, activity.RegisterOptions{Name: "raw"})
		registry.RegisterNexusService(service)
		return nil
	}
	backend := &backend{cfg: cfg, handlers: make(map[string]queue.Handler)}
	plan, err := backend.applicationPlan()
	if err != nil {
		t.Fatal(err)
	}
	again, err := backend.applicationPlan()
	if err != nil || again != plan || calls != 1 {
		t.Fatal("registration callback repeated")
	}
	if len(plan.workflows) != 1 || plan.workflows[0].options.Name != "registrationWorkflowFixture" || len(plan.activities) != 3 || plan.activities[0].options.Name != "registrationActivityFixture" || !plan.activities[1].options.SkipInvalidStructFunctions || len(plan.services) != 1 || plan.services[0] != service {
		t.Fatalf("registration intent changed: %+v", plan)
	}
	names := strings.Join(backend.nativeNames, ",")
	if !strings.Contains(names, "activity:reports.Good") || strings.Contains(names, "Invalid") || strings.Contains(names, "WorkflowOnly") || strings.Contains(names, "FunctionResult") || strings.Contains(names, "TooManyResults") {
		t.Fatal("invalid method entered native catalog", names)
	}
	before := backend.RegistrationFingerprint()
	backend.Register("logical", func(context.Context, queue.Job) error { return nil })
	after := backend.RegistrationFingerprint()
	if before == after || after != backend.RegistrationFingerprint() {
		t.Fatal("fingerprint is stale or unstable after logical registration")
	}
	var rejected any
	func() {
		defer func() { rejected = recover() }()
		captured.RegisterWorkflowWithOptions(registrationWorkflowFixture, workflow.RegisterOptions{Name: "late"})
	}()
	if rejected == nil {
		t.Fatal("sealed native registry accepted late registration")
	}
}

// TestRegistrationRejectsReservedDuplicateAndDynamicTypes prevents protected names from being shadowed before SDK polling.
func TestRegistrationRejectsReservedDuplicateAndDynamicTypes(t *testing.T) {
	cases := map[string]func(worker.Registry) error{
		"reserved workflow": func(r worker.Registry) error {
			r.RegisterWorkflowWithOptions(registrationWorkflowFixture, workflow.RegisterOptions{Name: coordinatorWorkflowType})
			return nil
		},
		"duplicate workflow": func(r worker.Registry) error {
			r.RegisterWorkflow(registrationWorkflowFixture)
			r.RegisterWorkflowWithOptions(registrationWorkflowFixture, workflow.RegisterOptions{DisableAlreadyRegisteredCheck: true})
			return nil
		},
		"dynamic workflow": func(r worker.Registry) error {
			r.RegisterDynamicWorkflow(nil, workflow.DynamicRegisterOptions{})
			return nil
		},
		"dynamic activity": func(r worker.Registry) error {
			r.RegisterDynamicActivity(nil, activity.DynamicRegisterOptions{})
			return nil
		},
		"invalid function": func(r worker.Registry) error { r.RegisterActivity(42); return nil },
		"nil function":     func(r worker.Registry) error { var f func(context.Context) error; r.RegisterActivity(f); return nil },
		"unnamed raw":      func(r worker.Registry) error { r.RegisterActivity(&rawActivityFixture{}); return nil },
		"duplicate service": func(r worker.Registry) error {
			r.RegisterNexusService(&nexus.Service{Name: "reports"})
			r.RegisterNexusService(&nexus.Service{Name: "reports"})
			return nil
		},
		"callback error": func(worker.Registry) error { return errors.New("private registration diagnostic") },
	}
	for name, register := range cases {
		t.Run(name, func(t *testing.T) {
			b := &backend{cfg: Config{Register: register}}
			plan, err := b.applicationPlan()
			if err == nil || plan != nil || strings.Contains(err.Error(), "private") {
				t.Fatalf("invalid registration accepted or leaked diagnostic: %+v %v", plan, err)
			}
		})
	}
	for name, invoke := range map[string]func(){"plan dynamic workflow": func() { (&registrationPlan{}).RegisterDynamicWorkflow(nil, workflow.DynamicRegisterOptions{}) }, "plan dynamic activity": func() { (&registrationPlan{}).RegisterDynamicActivity(nil, activity.DynamicRegisterOptions{}) }} {
		t.Run(name, func(t *testing.T) {
			var rejected any
			func() { defer func() { rejected = recover() }(); invoke() }()
			if rejected == nil {
				t.Fatal("dynamic registration accepted")
			}
		})
	}
}
