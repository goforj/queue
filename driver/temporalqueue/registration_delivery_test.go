package temporalqueue

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/goforj/queue"
	"github.com/goforj/queue/busruntime"
	"github.com/goforj/queue/internal/runtimehook"
	engine "github.com/goforj/queue/internal/workflow"
	"go.temporal.io/api/common/v1"
	"go.temporal.io/api/history/v1"
	"go.temporal.io/api/taskqueue/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// logicalRegistrationBarrier holds physical registration after Jobs has installed the logical handler.
type logicalRegistrationBarrier struct {
	busruntime.DirectRuntime
	entered, release chan struct{}
}

// BusRegister exposes the supported registration interleaving without relying on scheduler timing.
func (r *logicalRegistrationBarrier) BusRegister(name string, _ busruntime.Handler) {
	if name == "business" {
		close(r.entered)
		<-r.release
	}
}

// TestLateLogicalRegistrationPreservesExecutionOutcome keeps SDK results and logical observer facts consistent while physical registration is pending.
func TestLateLogicalRegistrationPreservesExecutionOutcome(t *testing.T) {
	for _, mode := range []string{"success", "retry", "permanent", "panic", "uncommitted", "missing"} {
		t.Run(mode, func(t *testing.T) {
			runtime := &logicalRegistrationBarrier{entered: make(chan struct{}), release: make(chan struct{})}
			successes, failures, calls := 0, 0, 0
			jobs, err := engine.NewJobs(runtime, engine.JobConfig{TranslateError: translateQueueError, PanicError: translateQueuePanic, Observer: engine.ObserverFunc(func(_ context.Context, event engine.Event) {
				if event.Kind == engine.EventJobSucceeded {
					successes++
				}
				if event.Kind == engine.EventJobFailed {
					failures++
				}
			})})
			if err != nil {
				t.Fatal(err)
			}
			if mode != "missing" {
				registered := make(chan struct{})
				go func() {
					defer close(registered)
					jobs.Register("business", func(context.Context, engine.Context) error {
						calls++
						switch mode {
						case "retry":
							return errors.New("retryable failure")
						case "permanent":
							return queue.Permanent(errors.New("terminal failure"))
						case "panic":
							panic("private panic")
						case "uncommitted":
							return busruntime.Uncommitted(errors.New("infrastructure result"))
						}
						return nil
					})
				}()
				defer func() { close(runtime.release); <-registered }()
				select {
				case <-runtime.entered:
				case <-time.After(5 * time.Second):
					t.Fatal("logical registration never reached physical barrier")
				}
			}
			d := delivery{Version: 1, Type: "business", WorkflowQueue: "reports", ActivityQueue: "reports", Metadata: busruntime.DeliveryMetadata{SchemaVersion: 1, DispatchID: "dispatch", JobID: "member", Queue: "reports"}}
			if err := d.resolve(time.Second); err != nil {
				t.Fatal(err)
			}
			payload, err := converter.GetDefaultDataConverter().ToPayloads(d)
			if err != nil {
				t.Fatal(err)
			}
			start := &history.WorkflowExecutionStartedEventAttributes{WorkflowType: &common.WorkflowType{Name: jobWorkflowType}, TaskQueue: &taskqueue.TaskQueue{Name: "reports"}, Input: payload, Attempt: 1, OriginalExecutionRunId: "default-test-run-id", FirstExecutionRunId: "default-test-run-id"}
			service := &acceptanceHistory{response: &workflowservice.GetWorkflowExecutionHistoryResponse{History: &history.History{Events: []*history.HistoryEvent{{EventId: 1, Attributes: &history.HistoryEvent_WorkflowExecutionStartedEventAttributes{WorkflowExecutionStartedEventAttributes: start}}}}}}
			dc, err := newGuardedConverter(nil, DefaultSerializationLimits(), newAdmission())
			if err != nil {
				t.Fatal(err)
			}
			b := &backend{gate: newAdmission(), taskQueue: "reports", workflowQueue: "reports", activityQueue: "reports", handlers: make(map[string]queue.Handler), clientBinding: &ClientBinding{client: &acceptanceClient{service: service}, options: client.Options{Namespace: "default-test-namespace", DataConverter: dc}}}
			b.binding = runtimehook.NewWorkflowBinding(b, jobs, string(queue.DriverTemporal))
			env := guardedWorkflowEnvironment(t)
			env.SetStartWorkflowOptions(client.StartWorkflowOptions{ID: "dispatch", TaskQueue: "reports"})
			env.RegisterActivityWithOptions(b.executeDelivery, activity.RegisterOptions{Name: deliveryActivityType})
			env.RegisterWorkflowWithOptions(func(ctx workflow.Context) error {
				ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{ActivityID: "member", TaskQueue: "reports", StartToCloseTimeout: time.Second, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 1}})
				return workflow.ExecuteActivity(ctx, deliveryActivityType, d).Get(ctx, nil)
			}, workflow.RegisterOptions{Name: jobWorkflowType})
			env.ExecuteWorkflow(jobWorkflowType)
			if mode == "success" {
				if err := env.GetWorkflowError(); err != nil || calls != 1 || successes != 1 || failures != 0 {
					t.Fatal("successful shared execution became missing-handler failure", calls, successes, failures, err)
				}
				return
			}
			var application *temporal.ApplicationError
			if !errors.As(env.GetWorkflowError(), &application) {
				t.Fatal("missing SDK classification", env.GetWorkflowError())
			}
			expected := map[string]string{"retry": "errorString", "permanent": "goforj.permanent.v1", "panic": "goforj.panic.v1", "uncommitted": "goforj.unsupported-outcome.v1", "missing": "goforj.missing-handler.v1"}[mode]
			if (mode != "retry" && application.Type() != expected) || (mode == "retry" && application.Type() == "goforj.missing-handler.v1") || application.NonRetryable() != (mode != "retry") {
				t.Fatalf("shared execution classification changed: mode=%s type=%s terminal=%t expected=%s error=%v", mode, application.Type(), application.NonRetryable(), expected, env.GetWorkflowError())
			}
			wantCalls := 1
			if mode == "missing" {
				wantCalls = 0
			}
			if calls != wantCalls || successes != 0 || failures != 1 {
				t.Fatal("logical outcome facts disagreed with SDK result", calls, successes, failures)
			}
		})
	}
}
