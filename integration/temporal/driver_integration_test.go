//go:build integration

package temporal

import (
	"context"
	"fmt"
	"google.golang.org/protobuf/encoding/protojson"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goforj/queue"
	"github.com/goforj/queue/driver/temporalqueue"
	"github.com/goforj/queue/queueconfig"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/history/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

// newDriverResource creates the public production driver against the pinned local server.
func newDriverResource(t *testing.T, name string, register func(worker.Registry) error, options ...worker.Options) *queue.Queue {
	t.Helper()
	var workerOptions worker.Options
	if len(options) > 0 {
		workerOptions = options[0]
	}
	q, err := temporalqueue.NewWithConfig(temporalqueue.Config{
		DriverBaseConfig: queueconfig.DriverBaseConfig{DefaultQueue: name},
		ClientOptions:    client.Options{HostPort: temporalAddress, Interceptors: []interceptor.ClientInterceptor{&interceptor.ClientInterceptorBase{}}, ConnectionOptions: client.ConnectionOptions{GetSystemInfoTimeout: 20 * time.Second}},
		WorkerOptions:    workerOptions, DefaultActivityTimeout: 5 * time.Second, ShutdownTimeout: 5 * time.Second, Register: register,
	}, queue.WithWorkers(4))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := q.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return q
}

// waitDriverState waits for a durable condition under the test deadline.
func waitDriverState(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.NewTimer(25 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		if condition() {
			return
		}
		select {
		case <-deadline.C:
			t.Fatal("driver state did not settle")
		case <-tick.C:
		case <-t.Context().Done():
			t.Fatal(t.Context().Err())
		}
	}
}

// TestTemporalDriverPortableDispatch exercises public registration, bytes, retry and correlation.
func TestTemporalDriverPortableDispatch(t *testing.T) {
	requireTemporalServer(t)
	q := newDriverResource(t, fmt.Sprintf("driver-job-%d", time.Now().UnixNano()), nil)
	var calls atomic.Int32
	messages := make(chan queue.Message, 2)
	q.Register("reports:build", func(_ context.Context, m queue.Message) error {
		calls.Add(1)
		messages <- m
		if m.Attempt == 0 {
			return fmt.Errorf("transient private detail")
		}
		return nil
	})
	if err := q.StartWorkers(t.Context()); err != nil {
		t.Fatal(err)
	}
	result, err := q.WithContext(t.Context()).Dispatch(queue.NewJob("reports:build").Payload([]byte(" exact bytes \n")).Retry(1).Backoff(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	waitDriverState(t, func() bool { return calls.Load() == 2 })
	for attempt := range 2 {
		m := <-messages
		if m.Attempt != attempt || m.DispatchID != result.DispatchID || m.JobID == "" || string(m.PayloadBytes()) != " exact bytes \n" {
			t.Fatalf("unexpected delivery: %+v bytes=%q", m, m.PayloadBytes())
		}
	}
}

// TestTemporalDriverWorkflowStateWithoutWorker proves accepted input and journal reads use history.
func TestTemporalDriverWorkflowStateWithoutWorker(t *testing.T) {
	requireTemporalServer(t)
	name := fmt.Sprintf("driver-state-%d", time.Now().UnixNano())
	producer := newDriverResource(t, name, nil)
	chainID, err := producer.Chain(queue.NewJob("one").Payload([]byte("raw")), queue.NewJob("two")).Dispatch(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	initial, err := producer.FindChain(t.Context(), chainID)
	if err != nil {
		t.Fatal(err)
	}
	if initial.NextIndex != 0 || initial.Completed || string(initial.Nodes[0].Job.Payload) != "raw" {
		t.Fatalf("initial state: %+v", initial)
	}
	poller := newDriverResource(t, name, nil)
	poller.Register("one", func(context.Context, queue.Message) error { return nil })
	poller.Register("two", func(context.Context, queue.Message) error { return nil })
	if err := poller.StartWorkers(t.Context()); err != nil {
		t.Fatal(err)
	}
	waitDriverState(t, func() bool {
		st, err := producer.FindChain(t.Context(), chainID)
		return err == nil && st.Completed && st.NextIndex == 2
	})
	batchID, err := producer.Batch(queue.NewJob("one"), queue.NewJob("two")).Name("sample").Dispatch(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	waitDriverState(t, func() bool {
		st, err := producer.FindBatch(t.Context(), batchID)
		return err == nil && st.Completed && st.Processed == 2 && st.Pending == 0
	})
	if err := poller.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	final, err := producer.FindChain(t.Context(), chainID)
	if err != nil || !final.Completed {
		t.Fatalf("post-stop state: %+v %v", final, err)
	}
}

// nativeDriverActivity returns typed data without queue.Message translation.
func nativeDriverActivity(context.Context) (string, error) { return "native result", nil }

// nativeDriverWorkflow exercises a native timer and result-producing activity.
func nativeDriverWorkflow(ctx workflow.Context) (string, error) {
	if err := workflow.Sleep(ctx, time.Millisecond); err != nil {
		return "", err
	}
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: time.Second})
	var result string
	err := workflow.ExecuteActivity(ctx, nativeDriverActivity).Get(ctx, &result)
	return result, err
}

// TestTemporalDriverNativeClient exercises actual SDK calls through the same public resource.
func TestTemporalDriverNativeClient(t *testing.T) {
	requireTemporalServer(t)
	name := fmt.Sprintf("driver-native-%d", time.Now().UnixNano())
	q := newDriverResource(t, name, func(reg worker.Registry) error {
		reg.RegisterActivity(nativeDriverActivity)
		reg.RegisterWorkflow(nativeDriverWorkflow)
		return nil
	})
	if err := q.StartWorkers(t.Context()); err != nil {
		t.Fatal(err)
	}
	c, err := temporalqueue.Client(q)
	if err != nil {
		t.Fatal(err)
	}
	run, err := c.ExecuteWorkflow(t.Context(), client.StartWorkflowOptions{ID: name, TaskQueue: name}, nativeDriverWorkflow)
	if err != nil {
		t.Fatal(err)
	}
	var result string
	if err := run.Get(t.Context(), &result); err != nil {
		t.Fatal(err)
	}
	if result != "native result" {
		t.Fatal(result)
	}
}

// TestTemporalDriverNamedContinuations proves serial hooks do not block business-result collection.
func TestTemporalDriverNamedContinuations(t *testing.T) {
	requireTemporalServer(t)
	name := fmt.Sprintf("driver-continuations-%d", time.Now().UnixNano())
	q := newDriverResource(t, name, nil)
	firstProgress := make(chan struct{})
	releaseProgress := make(chan struct{})
	var progress atomic.Int32
	q.Register("business", func(context.Context, queue.Message) error { return nil })
	q.Register("hook", func(ctx context.Context, m queue.Message) error {
		invocation, ok := m.Continuation()
		if !ok || string(m.PayloadBytes()) != "hook bytes" {
			return queue.Permanent(fmt.Errorf("missing invocation metadata"))
		}
		if invocation.Phase == "progress" && progress.Add(1) == 1 {
			close(firstProgress)
			select {
			case <-releaseProgress:
			case <-ctx.Done():
				return ctx.Err()
			}
			return queue.Permanent(fmt.Errorf("private continuation failure"))
		}
		return nil
	})
	if err := q.StartWorkers(t.Context()); err != nil {
		t.Fatal(err)
	}
	hook := queue.NewJob("hook").Payload([]byte("hook bytes"))
	id, err := queue.Continue(q.Batch(queue.NewJob("business"), queue.NewJob("business"))).OnProgress(hook).OnSuccess(hook).OnCompletion(hook).Dispatch(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-firstProgress:
	case <-time.After(20 * time.Second):
		t.Fatal("progress hook did not start")
	}
	waitDriverState(t, func() bool {
		state, err := q.FindBatch(t.Context(), id)
		return err == nil && state.Completed && state.Processed == 2
	})
	pending, err := q.FindContinuations(t.Context(), id)
	if err != nil || len(pending) != 4 || pending[0].Status != "scheduled" || pending[1].Status != "pending" {
		t.Fatalf("blocked ledger %+v %v", pending, err)
	}
	close(releaseProgress)
	waitDriverState(t, func() bool {
		outcomes, err := q.FindContinuations(t.Context(), id)
		return err == nil && len(outcomes) == 4 && outcomes[3].Status == "succeeded"
	})
	outcomes, err := q.FindContinuations(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if outcomes[0].Status != "exhausted" || outcomes[0].Invocation.Trigger.Processed != 1 || outcomes[1].Invocation.Trigger.Processed != 2 {
		t.Fatalf("settled ledger %+v", outcomes)
	}
}

// TestTemporalDriverOriginalHistoryReplay exercises the production workflow definitions with actual committed history.
func TestTemporalDriverOriginalHistoryReplay(t *testing.T) {
	requireTemporalServer(t)
	name := fmt.Sprintf("driver-replay-%d", time.Now().UnixNano())
	q := newDriverResource(t, name, nil)
	q.Register("business", func(context.Context, queue.Message) error { return nil })
	if err := q.StartWorkers(t.Context()); err != nil {
		t.Fatal(err)
	}
	id, err := queue.Continue(q.Batch(queue.NewJob("business"), queue.NewJob("business"))).OnProgress(queue.NewJob("business")).OnCompletion(queue.NewJob("business")).Dispatch(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	c, err := temporalqueue.Client(q)
	if err != nil {
		t.Fatal(err)
	}
	run := c.GetWorkflow(t.Context(), id, "")
	if err := run.Get(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	iterator := c.GetWorkflowHistory(t.Context(), id, run.GetRunID(), false, enums.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
	recorded := &history.History{}
	for iterator.HasNext() {
		event, err := iterator.Next()
		if err != nil {
			t.Fatal(err)
		}
		recorded.Events = append(recorded.Events, event)
	}
	if path := os.Getenv("QUEUE_TEMPORAL_HISTORY_OUTPUT"); path != "" {
		data, err := protojson.MarshalOptions{Indent: "  "}.Marshal(recorded)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	replayer, err := temporalqueue.NewWorkflowReplayer(q)
	if err != nil {
		t.Fatal(err)
	}
	if err := replayer.ReplayWorkflowExecution(t.Context(), c.WorkflowService(), nil, "default", workflow.Execution{ID: id, RunID: run.GetRunID()}); err != nil {
		t.Fatal(err)
	}
	if err := replayer.ReplayWorkflowHistory(nil, recorded); err != nil {
		t.Fatal(err)
	}
	if err := replayer.ReplayWorkflowHistoryWithOptions(nil, recorded, worker.ReplayWorkflowHistoryOptions{OriginalExecution: workflow.Execution{ID: id, RunID: run.GetRunID()}}); err != nil {
		t.Fatal(err)
	}
}

// TestTemporalDriverFailureDrainsAdmittedBatch preserves late member accounting and terminal SDK failure.
func TestTemporalDriverFailureDrainsAdmittedBatch(t *testing.T) {
	requireTemporalServer(t)
	q := newDriverResource(t, fmt.Sprintf("driver-drain-%d", time.Now().UnixNano()), nil)
	entered := make(chan struct{})
	release := make(chan struct{})
	var later atomic.Int32
	q.Register("slow", func(context.Context, queue.Message) error { close(entered); <-release; later.Add(1); return nil })
	q.Register("fail", func(context.Context, queue.Message) error {
		<-entered
		return queue.Permanent(fmt.Errorf("business failure"))
	})
	q.Register("hook", func(context.Context, queue.Message) error { return nil })
	if err := q.StartWorkers(t.Context()); err != nil {
		t.Fatal(err)
	}
	id, err := queue.Continue(q.Batch(queue.NewJob("fail"), queue.NewJob("slow"))).OnProgress(queue.NewJob("hook")).OnFailure(queue.NewJob("hook")).OnCompletion(queue.NewJob("hook")).Dispatch(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	waitDriverState(t, func() bool {
		st, err := q.FindBatch(t.Context(), id)
		return err == nil && st.Completed && st.Cancelled && st.Pending == 1
	})
	close(release)
	c, _ := temporalqueue.Client(q)
	if err := c.GetWorkflow(t.Context(), id, "").Get(t.Context(), nil); err == nil {
		t.Fatal("failed business workflow closed successfully")
	}
	st, err := q.FindBatch(t.Context(), id)
	if err != nil || st.Processed != 2 || st.Pending != 0 || st.Failed != 1 || later.Load() != 1 {
		t.Fatalf("late member lost: %+v %v", st, err)
	}
	hooks, err := q.FindContinuations(t.Context(), id)
	if err != nil || len(hooks) != 4 {
		t.Fatalf("hooks %+v %v", hooks, err)
	}
	for _, hook := range hooks {
		if hook.Status != "succeeded" {
			t.Fatalf("hook failed %+v", hook)
		}
	}
}

// TestTemporalDriverCanceledDelayAndResourceIsolation prevents successful closure or payload reads across resources.
func TestTemporalDriverCanceledDelayAndResourceIsolation(t *testing.T) {
	requireTemporalServer(t)
	name := fmt.Sprintf("driver-cancel-%d", time.Now().UnixNano())
	q := newDriverResource(t, name, nil)
	q.Register("business", func(context.Context, queue.Message) error { t.Error("canceled delayed member executed"); return nil })
	if err := q.StartWorkers(t.Context()); err != nil {
		t.Fatal(err)
	}
	id, err := q.Chain(queue.NewJob("business").Delay(time.Hour)).Dispatch(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	c, _ := temporalqueue.Client(q)
	waitDriverState(t, func() bool {
		description, err := c.DescribeWorkflowExecution(t.Context(), id, "")
		return err == nil && description.WorkflowExecutionInfo.HistoryLength >= 5
	})
	other := newDriverResource(t, name+"-other", nil)
	if _, err := other.FindChain(t.Context(), id); err == nil {
		t.Fatal("cross-resource payload read accepted")
	}
	if err := c.CancelWorkflow(t.Context(), id, ""); err != nil {
		t.Fatal(err)
	}
	if err := c.GetWorkflow(t.Context(), id, "").Get(t.Context(), nil); err == nil {
		t.Fatal("canceled delayed chain closed successfully")
	}
	st, err := q.FindChain(t.Context(), id)
	if err != nil || !st.Failed || st.Completed || st.NextIndex != 0 {
		t.Fatalf("interrupted state %+v %v", st, err)
	}
}

// TestTemporalDriverAllowedFailuresRetainBusinessSuccess preserves native completion and all durable hook phases.
func TestTemporalDriverAllowedFailuresRetainBusinessSuccess(t *testing.T) {
	requireTemporalServer(t)
	q := newDriverResource(t, fmt.Sprintf("driver-allowed-%d", time.Now().UnixNano()), nil)
	q.Register("ok", func(context.Context, queue.Message) error { return nil })
	q.Register("fail", func(context.Context, queue.Message) error { return queue.Permanent(fmt.Errorf("business failure")) })
	q.Register("hook", func(context.Context, queue.Message) error { return nil })
	if err := q.StartWorkers(t.Context()); err != nil {
		t.Fatal(err)
	}
	id, err := queue.Continue(q.Batch(queue.NewJob("fail"), queue.NewJob("ok")).AllowFailures()).OnProgress(queue.NewJob("hook")).OnFailure(queue.NewJob("hook")).OnSuccess(queue.NewJob("hook")).OnCompletion(queue.NewJob("hook")).Dispatch(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	c, _ := temporalqueue.Client(q)
	if err := c.GetWorkflow(t.Context(), id, "").Get(t.Context(), nil); err != nil {
		t.Fatal("allowed business failure changed native completion", err)
	}
	state, err := q.FindBatch(t.Context(), id)
	if err != nil || !state.Completed || state.Cancelled || state.Pending != 0 || state.Processed != 2 || state.Failed != 1 {
		t.Fatalf("allowed state %+v %v", state, err)
	}
	outcomes, err := q.FindContinuations(t.Context(), id)
	if err != nil || len(outcomes) != 5 {
		t.Fatalf("allowed outcomes %+v %v", outcomes, err)
	}
	for _, outcome := range outcomes {
		if outcome.Status != "succeeded" {
			t.Fatalf("allowed hook %+v", outcome)
		}
	}
}
