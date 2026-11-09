//go:build integration

package temporal

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goforj/queue"
	"github.com/goforj/queue/driver/temporalqueue"
	"github.com/goforj/queue/queueconfig"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

// hookSchedulePropagator rejects only continuation scheduling, using persisted activity identity for replay stability.
type hookSchedulePropagator struct{ mode string }

// MaxDecodedBytes declares the fixture's bounded propagation contract.
func (*hookSchedulePropagator) MaxDecodedBytes() int { return 1024 }

// Inject leaves client starts unaffected so accepted business work can complete.
func (*hookSchedulePropagator) Inject(context.Context, workflow.HeaderWriter) error { return nil }

// Extract preserves the activity context without decoding arbitrary data.
func (*hookSchedulePropagator) Extract(ctx context.Context, _ workflow.HeaderReader) (context.Context, error) {
	return ctx, nil
}

// ExtractToWorkflow permits original accepted workflow input and retained history replay.
func (*hookSchedulePropagator) ExtractToWorkflow(ctx workflow.Context, _ workflow.HeaderReader) (workflow.Context, error) {
	return ctx, nil
}

// InjectFromWorkflow fails before the SDK emits a scheduling command for a selected hook.
func (p *hookSchedulePropagator) InjectFromWorkflow(ctx workflow.Context, _ workflow.HeaderWriter) error {
	if strings.HasPrefix(workflow.GetActivityOptions(ctx).ActivityID, "cnt_") {
		if p.mode == "unavailable" {
			return temporalqueue.ErrAdapterUnavailable
		}
		if p.mode == "invalid" {
			return errors.New("synthetic hook propagation rejection")
		}
	}
	return nil
}

// hookMemoFailure selects the command/Memo atomicity boundary in an actual workflow task.
type hookMemoFailure struct {
	interceptor.WorkerInterceptorBase
}

// InterceptWorkflow isolates the failure counter to each replay-safe workflow instance.
func (*hookMemoFailure) InterceptWorkflow(_ workflow.Context, next interceptor.WorkflowInboundInterceptor) interceptor.WorkflowInboundInterceptor {
	return &hookMemoInbound{WorkflowInboundInterceptorBase: interceptor.WorkflowInboundInterceptorBase{Next: next}}
}

// hookMemoInbound preserves normal SDK execution while installing the selected publication error.
type hookMemoInbound struct {
	interceptor.WorkflowInboundInterceptorBase
}

// Init places failure at the second journal operation, after business settlement and activity scheduling.
func (i *hookMemoInbound) Init(next interceptor.WorkflowOutboundInterceptor) error {
	return i.Next.Init(&hookMemoOutbound{WorkflowOutboundInterceptorBase: interceptor.WorkflowOutboundInterceptorBase{Next: next}})
}

// hookMemoOutbound counts deterministic workflow commands rather than process-global calls.
type hookMemoOutbound struct {
	interceptor.WorkflowOutboundInterceptorBase
	updates int
}

// UpsertMemo rejects the hook scheduling journal without replacing the production coordinator or activity.
func (i *hookMemoOutbound) UpsertMemo(ctx workflow.Context, memo map[string]interface{}) error {
	i.updates++
	if i.updates == 2 {
		return errors.New("synthetic hook journal rejection")
	}
	return i.Next.UpsertMemo(ctx, memo)
}

// TestTemporalDriverHookSchedulingFailuresRetainReadableAuthority exercises failed SDK preflight and same-task journal rejection on the pinned server.
func TestTemporalDriverHookSchedulingFailuresRetainReadableAuthority(t *testing.T) {
	requireTemporalServer(t)
	for _, mode := range []string{"invalid", "unavailable", "memo"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			name := fmt.Sprintf("driver-hook-scheduling-%s-%d", mode, time.Now().UnixNano())
			cfg := temporalqueue.Config{DriverBaseConfig: queueconfig.DriverBaseConfig{DefaultQueue: name}, ClientOptions: client.Options{HostPort: temporalAddress, ContextPropagators: []workflow.ContextPropagator{&hookSchedulePropagator{mode: mode}}}, DefaultActivityTimeout: time.Second, ShutdownTimeout: 5 * time.Second}
			if mode == "memo" {
				cfg.WorkerOptions = worker.Options{Interceptors: []interceptor.WorkerInterceptor{&hookMemoFailure{}}}
			}
			q, err := temporalqueue.NewWithConfig(cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := q.Shutdown(context.Background()); err != nil {
					t.Error(err)
				}
			})
			var business, hooks atomic.Int32
			q.Register("business", func(context.Context, queue.Message) error { business.Add(1); return nil })
			q.Register("hook", func(context.Context, queue.Message) error { hooks.Add(1); return nil })
			if err := q.StartWorkers(ctx); err != nil {
				t.Fatal(err)
			}
			id, err := queue.Continue(q.Chain(queue.NewJob("business"))).OnCompletion(queue.NewJob("hook")).Dispatch(ctx)
			if err != nil {
				t.Fatal(err)
			}
			sdk, err := temporalqueue.Client(q)
			if err != nil {
				t.Fatal(err)
			}
			if err := sdk.GetWorkflow(ctx, id, "").Get(ctx, nil); err == nil {
				t.Fatal("failed scheduling completed workflow successfully")
			}
			state, err := q.FindChain(ctx, id)
			if err != nil || !state.Completed || state.Failed || state.NextIndex != 1 {
				t.Fatal("scheduling failure corrupted committed business state", state, err)
			}
			outcomes, err := q.FindContinuations(ctx, id)
			if err != nil || len(outcomes) != 1 || outcomes[0].Status != "interrupted" || outcomes[0].FailureCategory != "workflow_interrupted" || !outcomes[0].ScheduledAt.IsZero() {
				t.Fatal("scheduling failure corrupted continuation authority", outcomes, err)
			}
			iter := sdk.GetWorkflowHistory(ctx, id, "", false, enums.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
			scheduled := 0
			for iter.HasNext() {
				event, err := iter.Next()
				if err != nil {
					t.Fatal(err)
				}
				if activity := event.GetActivityTaskScheduledEventAttributes(); activity != nil && strings.HasPrefix(activity.ActivityId, "cnt_") {
					scheduled++
				}
			}
			wantScheduled := 0
			if mode == "memo" {
				wantScheduled = 1
			}
			if scheduled != wantScheduled {
				t.Fatal("fixture missed the SDK scheduling boundary", scheduled, wantScheduled)
			}
			if err := q.Shutdown(ctx); err != nil {
				t.Fatal(err)
			}
			if business.Load() != 1 || hooks.Load() != 0 {
				t.Fatal("effect crossed missing journal authority", business.Load(), hooks.Load())
			}
		})
	}
}
