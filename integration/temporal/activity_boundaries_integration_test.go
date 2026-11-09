//go:build integration

package temporal

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goforj/queue"
	"github.com/goforj/queue/driver/temporalqueue"
	"github.com/goforj/queue/queueconfig"
	"go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

// queryDrainFixture represents an interceptor with independently owned callback dependencies.
type queryDrainFixture struct {
	interceptor.WorkerInterceptorBase
	entered, release, returned chan struct{}
}

// InterceptWorkflow retains normal native registration and execution around the query callback.
func (f *queryDrainFixture) InterceptWorkflow(_ workflow.Context, next interceptor.WorkflowInboundInterceptor) interceptor.WorkflowInboundInterceptor {
	return &queryDrainInbound{WorkflowInboundInterceptorBase: interceptor.WorkflowInboundInterceptorBase{Next: next}, fixture: f}
}

// queryDrainInbound holds a query callback beyond SDK stop grace without taking ownership of its RPC client.
type queryDrainInbound struct {
	interceptor.WorkflowInboundInterceptorBase
	fixture *queryDrainFixture
}

// HandleQuery exposes actual callback lifetime after the worker's stop grace expires.
func (i *queryDrainInbound) HandleQuery(ctx workflow.Context, input *interceptor.HandleQueryInput) (any, error) {
	close(i.fixture.entered)
	defer close(i.fixture.returned)
	<-i.fixture.release
	return i.Next.HandleQuery(ctx, input)
}

// queryDrainWorkflow keeps ordinary workflow state available for an external client's query.
func queryDrainWorkflow(ctx workflow.Context) error {
	if err := workflow.SetQueryHandler(ctx, "value", func() (int, error) { return 1, nil }); err != nil {
		return err
	}
	var done bool
	workflow.GetSignalChannel(ctx, "done").Receive(ctx, &done)
	return nil
}

// TestTemporalDriverShutdownRetainsNativeQuery joins a worker-owned callback even when a separately owned client issued the query.
func TestTemporalDriverShutdownRetainsNativeQuery(t *testing.T) {
	requireTemporalServer(t)
	name := fmt.Sprintf("driver-query-drain-%d", time.Now().UnixNano())
	fixture := &queryDrainFixture{entered: make(chan struct{}), release: make(chan struct{}), returned: make(chan struct{})}
	var release sync.Once
	q := newDriverResource(t, name, func(registry worker.Registry) error {
		registry.RegisterWorkflow(queryDrainWorkflow)
		return nil
	}, worker.Options{WorkerStopTimeout: 20 * time.Millisecond, Interceptors: []interceptor.WorkerInterceptor{fixture}})
	t.Cleanup(func() { release.Do(func() { close(fixture.release) }) })
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	if err := q.StartWorkers(ctx); err != nil {
		t.Fatal(err)
	}
	external, err := client.Dial(client.Options{HostPort: temporalAddress})
	if err != nil {
		t.Fatal(err)
	}
	run, err := external.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: name, TaskQueue: name}, queryDrainWorkflow)
	if err != nil {
		external.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_ = external.TerminateWorkflow(cleanup, name, run.GetRunID(), "synthetic query fixture cleanup")
		external.Close()
	})
	queryCtx, stopQuery := context.WithCancel(ctx)
	defer stopQuery()
	queryDone := make(chan struct{})
	go func() { defer close(queryDone); _, _ = external.QueryWorkflow(queryCtx, name, run.GetRunID(), "value") }()
	select {
	case <-fixture.entered:
	case <-ctx.Done():
		t.Fatal("native query callback never entered", ctx.Err())
	}
	for range 2 {
		short, stop := context.WithTimeout(ctx, 200*time.Millisecond)
		err := q.Shutdown(short)
		stop()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal("shutdown escaped an active query callback", err)
		}
	}
	release.Do(func() { close(fixture.release) })
	select {
	case <-fixture.returned:
	case <-ctx.Done():
		t.Fatal("native query callback did not return", ctx.Err())
	}
	stopQuery()
	select {
	case <-queryDone:
	case <-ctx.Done():
		t.Fatal("external query RPC did not finish", ctx.Err())
	}
	if err := q.Shutdown(ctx); err != nil {
		t.Fatal("shutdown failed to join released query callback", err)
	}
}

// rawBoundaryActivity exercises the actual SDK payload-level executor instead of a typed function.
type rawBoundaryActivity struct {
	entered  chan struct{}
	release  chan struct{}
	returned chan struct{}
}

// Execute deliberately survives cancellation so the resource must retain its dependencies.
func (a *rawBoundaryActivity) Execute(context.Context, *common.Payloads) (*common.Payloads, error) {
	close(a.entered)
	<-a.release
	close(a.returned)
	return nil, nil
}

// ActivityType supplies the SDK raw interface's metadata path.
func (*rawBoundaryActivity) ActivityType() activity.Type {
	return activity.Type{Name: "payload-boundary"}
}

// GetFunction distinguishes raw dispatch from ordinary struct method registration.
func (a *rawBoundaryActivity) GetFunction() interface{} { return a.Execute }

// rawBoundaryWorkflow uses native SDK scheduling with no portable coordinator or mock activity executor.
func rawBoundaryWorkflow(ctx workflow.Context) error {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: time.Minute, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 1}})
	return workflow.ExecuteActivity(ctx, "payload-boundary").Get(ctx, nil)
}

// TestTemporalDriverShutdownRetainsPayloadActivity verifies timeout and repeated join across actual SDK raw dispatch.
func TestTemporalDriverShutdownRetainsPayloadActivity(t *testing.T) {
	requireTemporalServer(t)
	name := fmt.Sprintf("driver-payload-activity-%d", time.Now().UnixNano())
	raw := &rawBoundaryActivity{entered: make(chan struct{}), release: make(chan struct{}), returned: make(chan struct{})}
	var release sync.Once
	q := newDriverResource(t, name, func(registry worker.Registry) error {
		registry.RegisterWorkflow(rawBoundaryWorkflow)
		registry.RegisterActivityWithOptions(raw, activity.RegisterOptions{Name: "payload-boundary"})
		return nil
	}, worker.Options{WorkerStopTimeout: 10 * time.Millisecond})
	t.Cleanup(func() { release.Do(func() { close(raw.release) }) })
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	if err := q.StartWorkers(ctx); err != nil {
		t.Fatal(err)
	}
	sdk, err := temporalqueue.Client(q)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sdk.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: name, TaskQueue: name}, rawBoundaryWorkflow); err != nil {
		t.Fatal(err)
	}
	select {
	case <-raw.entered:
	case <-ctx.Done():
		t.Fatal("SDK raw activity never entered", ctx.Err())
	}
	for range 2 {
		short, stop := context.WithTimeout(ctx, 200*time.Millisecond)
		err := q.Shutdown(short)
		stop()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal("shutdown escaped live payload-level execution", err)
		}
		select {
		case <-raw.returned:
			t.Fatal("resistant fixture returned before release")
		default:
		}
	}
	if _, err := sdk.CheckHealth(ctx, &client.CheckHealthRequest{}); err != nil {
		t.Fatal("owned client closed across live raw activity", err)
	}
	release.Do(func() { close(raw.release) })
	select {
	case <-raw.returned:
	case <-ctx.Done():
		t.Fatal("raw activity did not finish", ctx.Err())
	}
	if err := q.Shutdown(ctx); err != nil {
		t.Fatal("shutdown failed to join released raw activity", err)
	}
}

// businessBoundaryPropagator permits acceptance and rejects business scheduling before any activity command.
type businessBoundaryPropagator struct{ hookSchedulePropagator }

// InjectFromWorkflow models a temporary bounded adapter outage at SDK scheduling preflight.
func (*businessBoundaryPropagator) InjectFromWorkflow(workflow.Context, workflow.HeaderWriter) error {
	return temporalqueue.ErrAdapterUnavailable
}

// TestTemporalDriverBusinessSchedulingOutageIsInterruption protects real chain and batch outcomes, including allowed failures.
func TestTemporalDriverBusinessSchedulingOutageIsInterruption(t *testing.T) {
	requireTemporalServer(t)
	for _, mode := range []string{"chain", "batch", "allowed batch"} {
		t.Run(mode, func(t *testing.T) {
			name := fmt.Sprintf("driver-business-preflight-%d", time.Now().UnixNano())
			q, err := temporalqueue.NewWithConfig(temporalqueue.Config{DriverBaseConfig: queueconfig.DriverBaseConfig{DefaultQueue: name}, ClientOptions: client.Options{HostPort: temporalAddress, ContextPropagators: []workflow.ContextPropagator{&businessBoundaryPropagator{}}}, DefaultActivityTimeout: time.Second, ShutdownTimeout: 5 * time.Second})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = q.Shutdown(context.Background()) })
			var calls atomic.Int32
			q.Register("business", func(context.Context, queue.Message) error { calls.Add(1); return nil })
			q.Register("hook", func(context.Context, queue.Message) error { calls.Add(1); return nil })
			var id string
			if mode == "chain" {
				id, err = queue.Continue(q.Chain(queue.NewJob("business"))).OnCompletion(queue.NewJob("hook")).Dispatch(t.Context())
			} else {
				batch := q.Batch(queue.NewJob("business"))
				if mode == "allowed batch" {
					batch = batch.AllowFailures()
				}
				id, err = queue.Continue(batch).OnCompletion(queue.NewJob("hook")).Dispatch(t.Context())
			}
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			if err := q.StartWorkers(ctx); err != nil {
				t.Fatal(err)
			}
			sdk, err := temporalqueue.Client(q)
			if err != nil {
				t.Fatal(err)
			}
			var category *temporal.ApplicationError
			if err := sdk.GetWorkflow(ctx, id, "").Get(ctx, nil); !errors.As(err, &category) || category.Type() != "goforj.adapter-unavailable.v1" || category.NonRetryable() {
				t.Fatal("preflight outage lost its infrastructure outcome", err)
			}
			if mode == "chain" {
				state, err := q.FindChain(ctx, id)
				if err != nil || state.NextIndex != 0 || state.Completed || !state.Failed || state.Failure != "workflow interrupted" {
					t.Fatal("unscheduled chain member became a business result", state, err)
				}
			} else {
				state, err := q.FindBatch(ctx, id)
				if err != nil || state.Processed != 0 || state.Failed != 0 || state.Pending != 1 || !state.Completed || !state.Cancelled {
					t.Fatal("unscheduled batch member became a business result", state, err)
				}
			}
			if outcomes, err := q.FindContinuations(ctx, id); err != nil || len(outcomes) != 0 || calls.Load() != 0 {
				t.Fatal("preflight failure triggered application work", calls.Load(), outcomes, err)
			}
			replayer, err := temporalqueue.NewWorkflowReplayer(q)
			if err != nil {
				t.Fatal(err)
			}
			if err := replayer.ReplayWorkflowExecution(ctx, sdk.WorkflowService(), nil, "default", workflow.Execution{ID: id}); err != nil {
				t.Fatal("interrupted scheduling history did not replay", err)
			}
		})
	}
}
