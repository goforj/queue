package temporalqueue

import (
	"bytes"
	"context"
	"errors"
	"go.temporal.io/api/history/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/log"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"io"
	"os"
	"time"

	"github.com/goforj/queue"
	"github.com/goforj/queue/internal/runtimehook"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

// NewWorkflowReplayer creates an SDK replayer with this resource's authoritative serialization policy.
// Its immutable registration plan is shared with worker startup and must contain deterministic definitions.
// Finish replays before closing this resource, or use a separate read/replay resource with a retained binding.
// Raw-history replay is routed through the stable namespace-aware SDK path so
// protected converters receive the same namespace as the original execution.
func NewWorkflowReplayer(q *queue.Queue) (result worker.WorkflowReplayer, err error) {
	b, ok := runtimehook.DriverResource(q).(*backend)
	if !ok {
		return nil, errors.New("queue is not a Temporal resource")
	}
	release, err := b.entry.enter()
	if err != nil {
		return nil, err
	}
	defer release()
	defer func() {
		if recover() != nil {
			result = nil
			err = errors.New("Temporal replay registration rejected")
		}
	}()
	options := b.cfg.WorkerOptions
	if b.workflowQueue != b.activityQueue {
		options = b.cfg.WorkflowWorkerOptions
	}
	interceptors := []interceptor.WorkerInterceptor{&resourceInterceptor{limits: b.clientBinding.limits, overhead: b.clientBinding.options.DataConverter.(*guardedConverter).overhead, gate: b.clientBinding.gate}}
	for _, item := range b.clientBinding.options.Interceptors {
		if workerInterceptor, ok := item.(interceptor.WorkerInterceptor); ok {
			interceptors = append(interceptors, workerInterceptor)
		}
	}
	for _, item := range options.Interceptors {
		interceptors = append(interceptors, &trackedWorkerInterceptor{inner: item, gate: b.clientBinding.gate})
	}
	result, err = worker.NewWorkflowReplayerWithOptions(worker.WorkflowReplayerOptions{DataConverter: b.clientBinding.options.DataConverter, FailureConverter: b.clientBinding.options.FailureConverter, ContextPropagators: b.clientBinding.options.ContextPropagators, Interceptors: interceptors, DisableRegistrationAliasing: options.DisableRegistrationAliasing, EnableLoggingInReplay: options.EnableLoggingInReplay})
	if err != nil {
		return nil, errors.New("Temporal replay options rejected")
	}
	result.RegisterWorkflowWithOptions(jobWorkflow, workflow.RegisterOptions{Name: jobWorkflowType})
	result.RegisterWorkflowWithOptions(coordinatorWorkflow, workflow.RegisterOptions{Name: coordinatorWorkflowType})
	plan, err := b.applicationPlan()
	if err != nil {
		return nil, err
	}
	for _, registration := range plan.workflows {
		result.RegisterWorkflowWithOptions(registration.fn, registration.options)
	}
	return &resourceReplayer{WorkflowReplayer: result, binding: b.clientBinding}, nil
}

// resourceReplayer binds SDK replay entrypoints to the original namespace and diagnostic policy.
type resourceReplayer struct {
	worker.WorkflowReplayer
	binding *ClientBinding
}

// replayLogger prevents the SDK's nil-logger default from printing replay stacks or decoded payloads.
func (r *resourceReplayer) replayLogger(logger log.Logger) log.Logger {
	if logger == nil {
		return r.binding.options.Logger
	}
	return &redactedLogger{inner: logger, gate: r.binding.gate}
}

// ReplayWorkflowExecution retains actual replay lifetime and rejects a mismatched namespace before reading history.
func (r *resourceReplayer) ReplayWorkflowExecution(ctx context.Context, service workflowservice.WorkflowServiceClient, logger log.Logger, namespace string, execution workflow.Execution) error {
	if namespace != r.binding.Namespace() {
		return errors.New("Temporal replay namespace does not match the binding")
	}
	release, err := r.binding.gate.enter()
	if err != nil {
		return err
	}
	defer release()
	return r.WorkflowReplayer.ReplayWorkflowExecution(ctx, service, r.replayLogger(logger), namespace, execution)
}

// ReplayWorkflowHistory preserves original namespace context through the stable SDK execution-replay path.
func (r *resourceReplayer) ReplayWorkflowHistory(logger log.Logger, history *history.History) error {
	return r.ReplayWorkflowHistoryWithOptions(logger, history, worker.ReplayWorkflowHistoryOptions{})
}

// ReplayWorkflowHistoryWithOptions bounds raw history and preserves caller-supplied original execution identity.
func (r *resourceReplayer) ReplayWorkflowHistoryWithOptions(logger log.Logger, recorded *history.History, options worker.ReplayWorkflowHistoryOptions) error {
	if recorded == nil || len(recorded.Events) == 0 || len(recorded.Events) > 100000 || proto.Size(recorded) > 64<<20 {
		return errors.New("Temporal replay history exceeds limits")
	}
	for i, event := range recorded.Events {
		if event == nil || event.EventId <= 0 || (i > 0 && event.EventId <= recorded.Events[i-1].EventId) {
			return errors.New("Temporal replay event sequence is invalid")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	start := recorded.Events[0].GetWorkflowExecutionStartedEventAttributes()
	if start == nil {
		return errors.New("Temporal replay history has no accepted input")
	}
	if options.OriginalExecution.ID == "" {
		options.OriginalExecution.ID = start.WorkflowId
	}
	if options.OriginalExecution.RunID == "" {
		options.OriginalExecution.RunID = start.OriginalExecutionRunId
	}
	if options.OriginalExecution.ID == "" {
		return errors.New("Temporal replay requires original workflow identity")
	}
	service := &recordedHistoryService{WorkflowServiceClient: r.binding.Client().WorkflowService(), recorded: recorded}
	return r.ReplayWorkflowExecution(ctx, service, logger, r.binding.Namespace(), options.OriginalExecution)
}

// recordedHistoryService feeds retained original history through the SDK's namespace-aware replay API.
type recordedHistoryService struct {
	workflowservice.WorkflowServiceClient
	recorded *history.History
}

// GetWorkflowExecutionHistory returns the supplied immutable capture without fetching a different execution.
func (s *recordedHistoryService) GetWorkflowExecutionHistory(context.Context, *workflowservice.GetWorkflowExecutionHistoryRequest, ...grpc.CallOption) (*workflowservice.GetWorkflowExecutionHistoryResponse, error) {
	return &workflowservice.GetWorkflowExecutionHistoryResponse{History: s.recorded}, nil
}

// ReplayWorkflowHistoryFromJSONFile bounds a user-selected capture before authoritative-context replay.
func (r *resourceReplayer) ReplayWorkflowHistoryFromJSONFile(logger log.Logger, path string) error {
	return r.ReplayPartialWorkflowHistoryFromJSONFile(logger, path, 0)
}

// ReplayPartialWorkflowHistoryFromJSONFile preserves namespace and payload policy for bounded partial captures.
func (r *resourceReplayer) ReplayPartialWorkflowHistoryFromJSONFile(logger log.Logger, path string, lastEventID int64) error {
	file, err := os.Open(path)
	if err != nil {
		return errors.New("Temporal replay capture is unavailable")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (64<<20)+1))
	if err != nil || len(data) > 64<<20 {
		return errors.New("Temporal replay capture exceeds limits")
	}
	recorded, err := client.HistoryFromJSON(bytes.NewReader(data), client.HistoryJSONOptions{})
	if err != nil {
		return errors.New("Temporal replay capture is invalid")
	}
	if lastEventID > 0 {
		end := 0
		for end < len(recorded.Events) && recorded.Events[end].EventId <= lastEventID {
			end++
		}
		recorded.Events = recorded.Events[:end]
	}
	return r.ReplayWorkflowHistory(logger, recorded)
}
