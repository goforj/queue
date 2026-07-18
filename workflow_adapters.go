package queue

import (
	"context"

	"github.com/goforj/queue/internal/workflow"
)

// cloneWorkflowPayload preserves nil-versus-empty payload semantics while isolating mutable bytes.
func cloneWorkflowPayload(payload []byte) []byte {
	if payload == nil {
		return nil
	}
	cloned := make([]byte, len(payload))
	copy(cloned, payload)
	return cloned
}

// messageFromWorkflow converts an engine context into the root-owned public message model.
func messageFromWorkflow(message workflow.Context) Message {
	return Message{
		SchemaVersion: message.SchemaVersion,
		DispatchID:    message.DispatchID,
		JobID:         message.JobID,
		ChainID:       message.ChainID,
		BatchID:       message.BatchID,
		Attempt:       message.Attempt,
		JobType:       message.JobType,
		payload:       message.PayloadBytes(),
	}
}

// messageToWorkflow converts a public message into the engine's private context model.
func messageToWorkflow(message Message) workflow.Context {
	return workflow.NewContext(
		message.SchemaVersion,
		message.DispatchID,
		message.JobID,
		message.ChainID,
		message.BatchID,
		message.Attempt,
		message.JobType,
		message.payload,
	)
}

// dispatchResultFromWorkflow converts the engine's dispatch receipt into the root-owned result.
func dispatchResultFromWorkflow(result workflow.DispatchResult) DispatchResult {
	return DispatchResult{DispatchID: result.DispatchID}
}

// storedJobOptionsToWorkflow converts the root-owned delivery policy to its engine representation.
func storedJobOptionsToWorkflow(options StoredJobOptions) workflow.JobOptions {
	return workflow.JobOptions{
		Queue:     options.Queue,
		Delay:     options.Delay,
		Timeout:   options.Timeout,
		Retry:     options.Retry,
		Backoff:   options.Backoff,
		UniqueFor: options.UniqueFor,
	}
}

// storedJobOptionsFromWorkflow converts the engine delivery policy to its root-owned representation.
func storedJobOptionsFromWorkflow(options workflow.JobOptions) StoredJobOptions {
	return StoredJobOptions{
		Queue:     options.Queue,
		Delay:     options.Delay,
		Timeout:   options.Timeout,
		Retry:     options.Retry,
		Backoff:   options.Backoff,
		UniqueFor: options.UniqueFor,
	}
}

// storedJobToWorkflow converts a persisted public job without changing its version-one JSON shape.
func storedJobToWorkflow(job StoredJob) workflow.StoredJob {
	return workflow.StoredJob{
		Type:    job.Type,
		Payload: cloneWorkflowPayload(job.Payload),
		Options: storedJobOptionsToWorkflow(job.Options),
	}
}

// storedJobFromWorkflow converts a persisted engine job into the public root-owned shape.
func storedJobFromWorkflow(job workflow.StoredJob) StoredJob {
	return StoredJob{
		Type:    job.Type,
		Payload: cloneWorkflowPayload(job.Payload),
		Options: storedJobOptionsFromWorkflow(job.Options),
	}
}

// chainNodeToWorkflow converts one public chain node into the engine model.
func chainNodeToWorkflow(node ChainNode) workflow.ChainNode {
	return workflow.ChainNode{
		NodeID: node.NodeID,
		Job:    storedJobToWorkflow(node.Job),
	}
}

// chainNodeFromWorkflow converts one engine chain node into the public model.
func chainNodeFromWorkflow(node workflow.ChainNode) ChainNode {
	return ChainNode{
		NodeID: node.NodeID,
		Job:    storedJobFromWorkflow(node.Job),
	}
}

// chainNodesToWorkflow converts a public node slice while preserving nil slices.
func chainNodesToWorkflow(nodes []ChainNode) []workflow.ChainNode {
	if nodes == nil {
		return nil
	}
	converted := make([]workflow.ChainNode, len(nodes))
	for i, node := range nodes {
		converted[i] = chainNodeToWorkflow(node)
	}
	return converted
}

// chainNodesFromWorkflow converts an engine node slice while preserving nil slices.
func chainNodesFromWorkflow(nodes []workflow.ChainNode) []ChainNode {
	if nodes == nil {
		return nil
	}
	converted := make([]ChainNode, len(nodes))
	for i, node := range nodes {
		converted[i] = chainNodeFromWorkflow(node)
	}
	return converted
}

// chainRecordToWorkflow converts a public chain creation record into the engine model.
func chainRecordToWorkflow(record ChainRecord) workflow.ChainRecord {
	return workflow.ChainRecord{
		ChainID:    record.ChainID,
		DispatchID: record.DispatchID,
		Queue:      record.Queue,
		Nodes:      chainNodesToWorkflow(record.Nodes),
		CreatedAt:  record.CreatedAt,
	}
}

// chainRecordFromWorkflow converts an engine chain creation record into the public model.
func chainRecordFromWorkflow(record workflow.ChainRecord) ChainRecord {
	return ChainRecord{
		ChainID:    record.ChainID,
		DispatchID: record.DispatchID,
		Queue:      record.Queue,
		Nodes:      chainNodesFromWorkflow(record.Nodes),
		CreatedAt:  record.CreatedAt,
	}
}

// chainStateToWorkflow converts a public chain state into the engine model.
func chainStateToWorkflow(state ChainState) workflow.ChainState {
	return workflow.ChainState{
		ChainID:    state.ChainID,
		DispatchID: state.DispatchID,
		Queue:      state.Queue,
		Nodes:      chainNodesToWorkflow(state.Nodes),
		NextIndex:  state.NextIndex,
		Completed:  state.Completed,
		Failed:     state.Failed,
		Failure:    state.Failure,
		CreatedAt:  state.CreatedAt,
		UpdatedAt:  state.UpdatedAt,
	}
}

// chainStateFromWorkflow converts an engine chain state into the public model.
func chainStateFromWorkflow(state workflow.ChainState) ChainState {
	return ChainState{
		ChainID:    state.ChainID,
		DispatchID: state.DispatchID,
		Queue:      state.Queue,
		Nodes:      chainNodesFromWorkflow(state.Nodes),
		NextIndex:  state.NextIndex,
		Completed:  state.Completed,
		Failed:     state.Failed,
		Failure:    state.Failure,
		CreatedAt:  state.CreatedAt,
		UpdatedAt:  state.UpdatedAt,
	}
}

// batchJobToWorkflow converts one public batch member into the engine model.
func batchJobToWorkflow(job BatchJob) workflow.BatchJob {
	return workflow.BatchJob{
		JobID: job.JobID,
		Job:   storedJobToWorkflow(job.Job),
	}
}

// batchJobFromWorkflow converts one engine batch member into the public model.
func batchJobFromWorkflow(job workflow.BatchJob) BatchJob {
	return BatchJob{
		JobID: job.JobID,
		Job:   storedJobFromWorkflow(job.Job),
	}
}

// batchJobsToWorkflow converts a public batch member slice while preserving nil slices.
func batchJobsToWorkflow(jobs []BatchJob) []workflow.BatchJob {
	if jobs == nil {
		return nil
	}
	converted := make([]workflow.BatchJob, len(jobs))
	for i, job := range jobs {
		converted[i] = batchJobToWorkflow(job)
	}
	return converted
}

// batchJobsFromWorkflow converts an engine batch member slice while preserving nil slices.
func batchJobsFromWorkflow(jobs []workflow.BatchJob) []BatchJob {
	if jobs == nil {
		return nil
	}
	converted := make([]BatchJob, len(jobs))
	for i, job := range jobs {
		converted[i] = batchJobFromWorkflow(job)
	}
	return converted
}

// batchRecordToWorkflow converts a public batch creation record into the engine model.
func batchRecordToWorkflow(record BatchRecord) workflow.BatchRecord {
	return workflow.BatchRecord{
		BatchID:     record.BatchID,
		DispatchID:  record.DispatchID,
		Name:        record.Name,
		Queue:       record.Queue,
		AllowFailed: record.AllowFailed,
		Jobs:        batchJobsToWorkflow(record.Jobs),
		CreatedAt:   record.CreatedAt,
	}
}

// batchRecordFromWorkflow converts an engine batch creation record into the public model.
func batchRecordFromWorkflow(record workflow.BatchRecord) BatchRecord {
	return BatchRecord{
		BatchID:     record.BatchID,
		DispatchID:  record.DispatchID,
		Name:        record.Name,
		Queue:       record.Queue,
		AllowFailed: record.AllowFailed,
		Jobs:        batchJobsFromWorkflow(record.Jobs),
		CreatedAt:   record.CreatedAt,
	}
}

// batchStateToWorkflow converts a public aggregate state into the engine model.
func batchStateToWorkflow(state BatchState) workflow.BatchState {
	return workflow.BatchState{
		BatchID:     state.BatchID,
		DispatchID:  state.DispatchID,
		Name:        state.Name,
		Queue:       state.Queue,
		AllowFailed: state.AllowFailed,
		Total:       state.Total,
		Pending:     state.Pending,
		Processed:   state.Processed,
		Failed:      state.Failed,
		Cancelled:   state.Cancelled,
		Completed:   state.Completed,
		CreatedAt:   state.CreatedAt,
		UpdatedAt:   state.UpdatedAt,
	}
}

// batchStateFromWorkflow converts an engine aggregate state into the public model.
func batchStateFromWorkflow(state workflow.BatchState) BatchState {
	return BatchState{
		BatchID:     state.BatchID,
		DispatchID:  state.DispatchID,
		Name:        state.Name,
		Queue:       state.Queue,
		AllowFailed: state.AllowFailed,
		Total:       state.Total,
		Pending:     state.Pending,
		Processed:   state.Processed,
		Failed:      state.Failed,
		Cancelled:   state.Cancelled,
		Completed:   state.Completed,
		CreatedAt:   state.CreatedAt,
		UpdatedAt:   state.UpdatedAt,
	}
}

// workflowMiddlewareAdapter translates physical root messages around one public middleware.
type workflowMiddlewareAdapter struct {
	middleware Middleware
}

// Handle preserves middleware message replacement while crossing the private engine boundary.
func (a workflowMiddlewareAdapter) Handle(ctx context.Context, message workflow.Context, next workflow.Next) error {
	return a.middleware.Handle(ctx, messageFromWorkflow(message), func(nextContext context.Context, nextMessage Message) error {
		return next(nextContext, messageToWorkflow(nextMessage))
	})
}

// middlewaresToWorkflow converts public middleware into private engine adapters.
func middlewaresToWorkflow(middlewares []Middleware) []workflow.Middleware {
	if middlewares == nil {
		return nil
	}
	converted := make([]workflow.Middleware, 0, len(middlewares))
	for _, middleware := range middlewares {
		if middleware != nil {
			converted = append(converted, workflowMiddlewareAdapter{middleware: middleware})
		}
	}
	return converted
}

// chainCatchToWorkflow adapts an optional public chain failure callback to engine state.
func chainCatchToWorkflow(callback func(context.Context, ChainState, error) error) func(context.Context, workflow.ChainState, error) error {
	if callback == nil {
		return nil
	}
	return func(ctx context.Context, state workflow.ChainState, err error) error {
		return callback(ctx, chainStateFromWorkflow(state), err)
	}
}

// chainFinallyToWorkflow adapts an optional public chain terminal callback to engine state.
func chainFinallyToWorkflow(callback func(context.Context, ChainState) error) func(context.Context, workflow.ChainState) error {
	if callback == nil {
		return nil
	}
	return func(ctx context.Context, state workflow.ChainState) error {
		return callback(ctx, chainStateFromWorkflow(state))
	}
}

// batchStateCallbackToWorkflow adapts an optional public batch callback to engine state.
func batchStateCallbackToWorkflow(callback func(context.Context, BatchState) error) func(context.Context, workflow.BatchState) error {
	if callback == nil {
		return nil
	}
	return func(ctx context.Context, state workflow.BatchState) error {
		return callback(ctx, batchStateFromWorkflow(state))
	}
}

// batchCatchToWorkflow adapts an optional public batch failure callback to engine state.
func batchCatchToWorkflow(callback func(context.Context, BatchState, error) error) func(context.Context, workflow.BatchState, error) error {
	if callback == nil {
		return nil
	}
	return func(ctx context.Context, state workflow.BatchState, err error) error {
		return callback(ctx, batchStateFromWorkflow(state), err)
	}
}
