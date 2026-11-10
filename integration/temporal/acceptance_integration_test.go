//go:build integration

package temporal

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goforj/queue"
	"github.com/goforj/queue/driver/temporalqueue"
	"github.com/goforj/queue/queueconfig"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/interceptor"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// loseStartResponse discards one successful service response after actual server acceptance.
type loseStartResponse struct {
	interceptor.ClientInterceptorBase
	lost atomic.Bool
}

// lossOutbound preserves real SDK calls and injects only a caller-visible response loss.
type lossOutbound struct {
	interceptor.ClientOutboundInterceptorBase
	loss *loseStartResponse
}

// InterceptClient installs response loss above actual production transport and conversion.
func (i *loseStartResponse) InterceptClient(next interceptor.ClientOutboundInterceptor) interceptor.ClientOutboundInterceptor {
	return &lossOutbound{ClientOutboundInterceptorBase: interceptor.ClientOutboundInterceptorBase{Next: next}, loss: i}
}

// ExecuteWorkflow models accepted-start response loss with no new identity allocated by the injector.
func (o *lossOutbound) ExecuteWorkflow(ctx context.Context, in *interceptor.ClientExecuteWorkflowInput) (client.WorkflowRun, error) {
	run, err := o.Next.ExecuteWorkflow(ctx, in)
	if err == nil && !o.loss.lost.Swap(true) {
		return nil, serviceerror.NewUnavailable("synthetic response loss")
	}
	return run, err
}

// TestTemporalDriverReconcilesLostStartResponse verifies original-input reconciliation against the retained service.
func TestTemporalDriverReconcilesLostStartResponse(t *testing.T) {
	requireTemporalServer(t)
	for _, chain := range []bool{false, true} {
		t.Run(fmt.Sprintf("chain=%t", chain), func(t *testing.T) {
			name := fmt.Sprintf("driver-loss-%d", time.Now().UnixNano())
			loss := &loseStartResponse{}
			q, err := temporalqueue.NewWithConfig(temporalqueue.Config{DriverBaseConfig: queueconfig.DriverBaseConfig{DefaultQueue: name}, ClientOptions: client.Options{HostPort: temporalAddress, Interceptors: []interceptor.ClientInterceptor{loss}}, DefaultActivityTimeout: time.Second, ShutdownTimeout: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			defer q.Shutdown(context.Background())
			var calls atomic.Int32
			q.Register("business", func(context.Context, queue.Message) error { calls.Add(1); return nil })
			if err := q.StartWorkers(t.Context()); err != nil {
				t.Fatal(err)
			}
			var id string
			if chain {
				id, err = q.Chain(queue.NewJob("business")).Dispatch(t.Context())
			} else {
				var result queue.DispatchResult
				result, err = q.Dispatch(queue.NewJob("business"))
				id = result.DispatchID
			}
			if err != nil || id == "" || !loss.lost.Load() {
				t.Fatalf("reconciliation failed: %s %v", id, err)
			}
			c, _ := temporalqueue.Client(q)
			if err := c.GetWorkflow(t.Context(), id, "").Get(t.Context(), nil); err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 1 {
				t.Fatalf("duplicated work: %d", calls.Load())
			}
		})
	}
}

// fatalTraffic causes the actual SDK's sustained nonretryable-poll failure path.
type fatalTraffic struct{}

// CheckCallAllowed leaves service setup intact and fails only workflow polling.
func (*fatalTraffic) CheckCallAllowed(_ context.Context, method string, _ interface{}, _ interface{}) error {
	if strings.HasSuffix(method, "PollWorkflowTaskQueue") {
		return status.Error(codes.InvalidArgument, "synthetic poll fatal")
	}
	return nil
}

// TestTemporalDriverActualSDKFatalReachesRun qualifies the driver-owned stop arbiter through the SDK's self-poller path.
func TestTemporalDriverActualSDKFatalReachesRun(t *testing.T) {
	requireTemporalServer(t)
	q, err := temporalqueue.NewWithConfig(temporalqueue.Config{DriverBaseConfig: queueconfig.DriverBaseConfig{DefaultQueue: fmt.Sprintf("driver-fatal-%d", time.Now().UnixNano())}, ClientOptions: client.Options{HostPort: temporalAddress, TrafficController: &fatalTraffic{}}, DefaultActivityTimeout: time.Second, ShutdownTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 160*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- q.Run(ctx) }()
	select {
	case err := <-result:
		if err == nil || ctx.Err() != nil || strings.Contains(err.Error(), "synthetic") {
			t.Fatalf("fatal result %v, ctx %v", err, ctx.Err())
		}
	case <-ctx.Done():
		t.Fatal("actual SDK fatal did not reach Run")
	}
	joins := make(chan error, 8)
	for range 8 {
		go func() { joins <- q.Shutdown(t.Context()) }()
	}
	for range 8 {
		if err := <-joins; err != nil {
			t.Fatal(err)
		}
	}
}

// TestTemporalDriverRejectsAdministrativeReset keeps copied portable activity tasks from creating new effects.
func TestTemporalDriverRejectsAdministrativeReset(t *testing.T) {
	requireTemporalServer(t)
	q := newDriverResource(t, fmt.Sprintf("driver-reset-%d", time.Now().UnixNano()), nil)
	var calls atomic.Int32
	q.Register("business", func(context.Context, queue.Message) error { calls.Add(1); return nil })
	if err := q.StartWorkers(t.Context()); err != nil {
		t.Fatal(err)
	}
	id, err := q.Chain(queue.NewJob("business"), queue.NewJob("business").Delay(time.Hour)).Dispatch(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	waitDriverState(t, func() bool { state, err := q.FindChain(t.Context(), id); return err == nil && state.NextIndex == 1 })
	c, _ := temporalqueue.Client(q)
	description, err := c.DescribeWorkflowExecution(t.Context(), id, "")
	if err != nil {
		t.Fatal(err)
	}
	execution := description.WorkflowExecutionInfo.Execution
	iterator := c.GetWorkflowHistory(t.Context(), id, execution.RunId, false, enums.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
	var finish int64
	for iterator.HasNext() {
		event, err := iterator.Next()
		if err != nil {
			t.Fatal(err)
		}
		if event.GetWorkflowTaskCompletedEventAttributes() != nil {
			finish = event.EventId
		}
	}
	response, err := c.WorkflowService().ResetWorkflowExecution(t.Context(), &workflowservice.ResetWorkflowExecutionRequest{Namespace: "default", WorkflowExecution: execution, Reason: "synthetic portable reset rejection", WorkflowTaskFinishEventId: finish, RequestId: fmt.Sprintf("reset-%d", time.Now().UnixNano())})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.GetWorkflow(t.Context(), id, response.RunId).Get(t.Context(), nil); err == nil {
		t.Fatal("portable reset executed successfully")
	}
	if _, err := q.FindChain(t.Context(), id); err == nil {
		t.Fatal("reset lineage read accepted")
	}
	if calls.Load() != 1 {
		t.Fatalf("reset repeated business effect: %d", calls.Load())
	}
}
