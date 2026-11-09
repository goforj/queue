package temporalqueue

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

// TestConcurrentNativeRegistrationRetainsCompleteCatalog preserves SDK registry synchronization inside the registration callback.
func TestConcurrentNativeRegistrationRetainsCompleteCatalog(t *testing.T) {
	b := &backend{cfg: Config{Register: func(registry worker.Registry) error {
		start := make(chan struct{})
		var joined sync.WaitGroup
		for i := range 16 {
			joined.Go(func() {
				<-start
				registry.RegisterWorkflowWithOptions(registrationWorkflowFixture, workflow.RegisterOptions{Name: fmt.Sprintf("workflow-%02d", i)})
				registry.RegisterActivityWithOptions(func(context.Context) error { return nil }, activity.RegisterOptions{Name: fmt.Sprintf("activity-%02d", i)})
			})
		}
		close(start)
		joined.Wait()
		return nil
	}}}
	plan, err := b.applicationPlan()
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.workflows) != 16 || len(plan.activities) != 16 || len(b.nativeNames) != 32 {
		t.Fatalf("lost native registrations: workflows=%d activities=%d names=%d", len(plan.workflows), len(plan.activities), len(b.nativeNames))
	}
	names := make(map[string]bool)
	for _, registration := range plan.workflows {
		names["workflow:"+registration.options.Name] = true
	}
	for _, registration := range plan.activities {
		names["activity:"+registration.options.Name] = true
	}
	for _, name := range b.nativeNames {
		if !names[name] {
			t.Fatal("catalog and registration plan disagree", name)
		}
		delete(names, name)
	}
	if len(names) != 0 {
		t.Fatal("registration missing from fingerprint catalog", names)
	}
}
