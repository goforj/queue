package bus

import (
	"context"
	"time"

	"github.com/goforj/queue"
	"github.com/goforj/queue/internal/workflow"
)

// toQueueMessage converts the private engine context into the root-owned message model.
func toQueueMessage(message workflow.Context) queue.Message {
	converted := queue.NewMessage(message.JobType, message.PayloadBytes())
	converted.SchemaVersion = message.SchemaVersion
	converted.DispatchID = message.DispatchID
	converted.JobID = message.JobID
	converted.ChainID = message.ChainID
	converted.BatchID = message.BatchID
	converted.Attempt = message.Attempt
	return converted
}

// toWorkflowContext converts a root-owned message back into the private engine context.
func toWorkflowContext(message queue.Message) workflow.Context {
	return workflow.NewContext(
		message.SchemaVersion,
		message.DispatchID,
		message.JobID,
		message.ChainID,
		message.BatchID,
		message.Attempt,
		message.JobType,
		message.PayloadBytes(),
	)
}

// toQueueDispatchResult converts an engine receipt into the root-owned result model.
func toQueueDispatchResult(result workflow.DispatchResult) queue.DispatchResult {
	return queue.DispatchResult{DispatchID: result.DispatchID}
}

// toWorkflowStoredJobOptions converts root-owned delivery policy into the engine model.
func toWorkflowStoredJobOptions(options queue.StoredJobOptions) workflow.JobOptions {
	return workflow.JobOptions{
		Queue:     options.Queue,
		Delay:     options.Delay,
		Timeout:   options.Timeout,
		Retry:     options.Retry,
		Backoff:   options.Backoff,
		UniqueFor: options.UniqueFor,
	}
}

// toQueueStoredJobOptions converts engine delivery policy into the root-owned model.
func toQueueStoredJobOptions(options workflow.JobOptions) queue.StoredJobOptions {
	return queue.StoredJobOptions{
		Queue:     options.Queue,
		Delay:     options.Delay,
		Timeout:   options.Timeout,
		Retry:     options.Retry,
		Backoff:   options.Backoff,
		UniqueFor: options.UniqueFor,
	}
}

// cloneStoredPayload isolates mutable persisted payload bytes without changing nil slices.
func cloneStoredPayload(payload []byte) []byte {
	if payload == nil {
		return nil
	}
	cloned := make([]byte, len(payload))
	copy(cloned, payload)
	return cloned
}

// toWorkflowStoredJob converts one root-owned persisted job into the engine model.
func toWorkflowStoredJob(job queue.StoredJob) workflow.StoredJob {
	return workflow.StoredJob{
		Type:    job.Type,
		Payload: cloneStoredPayload(job.Payload),
		Options: toWorkflowStoredJobOptions(job.Options),
	}
}

// toQueueStoredJob converts one engine persisted job into the root-owned model.
func toQueueStoredJob(job workflow.StoredJob) queue.StoredJob {
	return queue.StoredJob{
		Type:    job.Type,
		Payload: cloneStoredPayload(job.Payload),
		Options: toQueueStoredJobOptions(job.Options),
	}
}

// toWorkflowChainNode converts one root-owned chain node into the engine model.
func toWorkflowChainNode(node queue.ChainNode) workflow.ChainNode {
	return workflow.ChainNode{NodeID: node.NodeID, Job: toWorkflowStoredJob(node.Job)}
}

// toQueueChainNode converts one engine chain node into the root-owned model.
func toQueueChainNode(node workflow.ChainNode) queue.ChainNode {
	return queue.ChainNode{NodeID: node.NodeID, Job: toQueueStoredJob(node.Job)}
}

// toWorkflowChainNodes converts a root-owned node slice while retaining nil slices.
func toWorkflowChainNodes(nodes []queue.ChainNode) []workflow.ChainNode {
	if nodes == nil {
		return nil
	}
	converted := make([]workflow.ChainNode, len(nodes))
	for i, node := range nodes {
		converted[i] = toWorkflowChainNode(node)
	}
	return converted
}

// toQueueChainNodes converts an engine node slice while retaining nil slices.
func toQueueChainNodes(nodes []workflow.ChainNode) []queue.ChainNode {
	if nodes == nil {
		return nil
	}
	converted := make([]queue.ChainNode, len(nodes))
	for i, node := range nodes {
		converted[i] = toQueueChainNode(node)
	}
	return converted
}

// toQueueChainRecord converts engine chain creation state for a root-owned store.
func toQueueChainRecord(record workflow.ChainRecord) queue.ChainRecord {
	return queue.ChainRecord{
		ChainID:    record.ChainID,
		DispatchID: record.DispatchID,
		Queue:      record.Queue,
		Nodes:      toQueueChainNodes(record.Nodes),
		CreatedAt:  record.CreatedAt,
	}
}

// toWorkflowChainState converts root-owned chain state into the engine model.
func toWorkflowChainState(state queue.ChainState) workflow.ChainState {
	return workflow.ChainState{
		ChainID:    state.ChainID,
		DispatchID: state.DispatchID,
		Queue:      state.Queue,
		Nodes:      toWorkflowChainNodes(state.Nodes),
		NextIndex:  state.NextIndex,
		Completed:  state.Completed,
		Failed:     state.Failed,
		Failure:    state.Failure,
		CreatedAt:  state.CreatedAt,
		UpdatedAt:  state.UpdatedAt,
	}
}

// toQueueChainState converts engine chain state into the root-owned model.
func toQueueChainState(state workflow.ChainState) queue.ChainState {
	return queue.ChainState{
		ChainID:    state.ChainID,
		DispatchID: state.DispatchID,
		Queue:      state.Queue,
		Nodes:      toQueueChainNodes(state.Nodes),
		NextIndex:  state.NextIndex,
		Completed:  state.Completed,
		Failed:     state.Failed,
		Failure:    state.Failure,
		CreatedAt:  state.CreatedAt,
		UpdatedAt:  state.UpdatedAt,
	}
}

// toQueueBatchJob converts one engine batch member into the root-owned model.
func toQueueBatchJob(job workflow.BatchJob) queue.BatchJob {
	return queue.BatchJob{JobID: job.JobID, Job: toQueueStoredJob(job.Job)}
}

// toQueueBatchJobs converts an engine member slice while retaining nil slices.
func toQueueBatchJobs(jobs []workflow.BatchJob) []queue.BatchJob {
	if jobs == nil {
		return nil
	}
	converted := make([]queue.BatchJob, len(jobs))
	for i, job := range jobs {
		converted[i] = toQueueBatchJob(job)
	}
	return converted
}

// toQueueBatchRecord converts engine batch creation state for a root-owned store.
func toQueueBatchRecord(record workflow.BatchRecord) queue.BatchRecord {
	return queue.BatchRecord{
		BatchID:     record.BatchID,
		DispatchID:  record.DispatchID,
		Name:        record.Name,
		Queue:       record.Queue,
		AllowFailed: record.AllowFailed,
		Jobs:        toQueueBatchJobs(record.Jobs),
		CreatedAt:   record.CreatedAt,
	}
}

// toWorkflowBatchState converts root-owned aggregate state into the engine model.
func toWorkflowBatchState(state queue.BatchState) workflow.BatchState {
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

// toQueueBatchState converts engine aggregate state into the root-owned model.
func toQueueBatchState(state workflow.BatchState) queue.BatchState {
	return queue.BatchState{
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

type workflowMiddlewareAdapter struct {
	middleware Middleware
}

var _ workflow.Middleware = workflowMiddlewareAdapter{}

// Handle preserves middleware message replacement while crossing the private engine boundary.
func (a workflowMiddlewareAdapter) Handle(ctx context.Context, message workflow.Context, next workflow.Next) error {
	return a.middleware.Handle(ctx, toQueueMessage(message), func(nextContext context.Context, nextMessage queue.Message) error {
		return next(nextContext, toWorkflowContext(nextMessage))
	})
}

// toWorkflowMiddlewares converts root-owned middleware into private engine adapters.
func toWorkflowMiddlewares(middlewares []Middleware) []workflow.Middleware {
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

type workflowStoreAdapter struct {
	store Store
}

var _ workflow.Store = workflowStoreAdapter{}

// toWorkflowStore wraps a root-owned store for the retained raw-runtime route.
func toWorkflowStore(store Store) workflow.Store {
	if store == nil {
		return nil
	}
	return workflowStoreAdapter{store: store}
}

// CreateChain converts the engine record before invoking the root-owned store.
func (a workflowStoreAdapter) CreateChain(ctx context.Context, record workflow.ChainRecord) error {
	return a.store.CreateChain(ctx, toQueueChainRecord(record))
}

// AdvanceChain converts the optional root-owned next node back into the engine model.
func (a workflowStoreAdapter) AdvanceChain(ctx context.Context, chainID string, completedNode string) (*workflow.ChainNode, bool, error) {
	node, done, err := a.store.AdvanceChain(ctx, chainID, completedNode)
	if node == nil {
		return nil, done, err
	}
	converted := toWorkflowChainNode(*node)
	return &converted, done, err
}

// FailChain forwards the terminal cause without changing its error identity.
func (a workflowStoreAdapter) FailChain(ctx context.Context, chainID string, cause error) error {
	return a.store.FailChain(ctx, chainID, cause)
}

// GetChain converts root-owned state back into the engine model.
func (a workflowStoreAdapter) GetChain(ctx context.Context, chainID string) (workflow.ChainState, error) {
	state, err := a.store.GetChain(ctx, chainID)
	return toWorkflowChainState(state), err
}

// CreateBatch converts the engine record before invoking the root-owned store.
func (a workflowStoreAdapter) CreateBatch(ctx context.Context, record workflow.BatchRecord) error {
	return a.store.CreateBatch(ctx, toQueueBatchRecord(record))
}

// MarkBatchJobStarted forwards the retry-safe member-start mutation.
func (a workflowStoreAdapter) MarkBatchJobStarted(ctx context.Context, batchID, jobID string) error {
	return a.store.MarkBatchJobStarted(ctx, batchID, jobID)
}

// MarkBatchJobSucceeded converts the resulting root-owned state back into the engine model.
func (a workflowStoreAdapter) MarkBatchJobSucceeded(ctx context.Context, batchID, jobID string) (workflow.BatchState, bool, error) {
	state, done, err := a.store.MarkBatchJobSucceeded(ctx, batchID, jobID)
	return toWorkflowBatchState(state), done, err
}

// MarkBatchJobFailed preserves the failure cause and converts the resulting state.
func (a workflowStoreAdapter) MarkBatchJobFailed(ctx context.Context, batchID, jobID string, cause error) (workflow.BatchState, bool, error) {
	state, done, err := a.store.MarkBatchJobFailed(ctx, batchID, jobID, cause)
	return toWorkflowBatchState(state), done, err
}

// CancelBatch forwards aggregate cancellation to the root-owned store.
func (a workflowStoreAdapter) CancelBatch(ctx context.Context, batchID string) error {
	return a.store.CancelBatch(ctx, batchID)
}

// GetBatch converts root-owned aggregate state back into the engine model.
func (a workflowStoreAdapter) GetBatch(ctx context.Context, batchID string) (workflow.BatchState, error) {
	state, err := a.store.GetBatch(ctx, batchID)
	return toWorkflowBatchState(state), err
}

// MarkCallbackInvoked forwards the atomic callback claim unchanged.
func (a workflowStoreAdapter) MarkCallbackInvoked(ctx context.Context, key string) (bool, error) {
	return a.store.MarkCallbackInvoked(ctx, key)
}

// Prune forwards workflow retention to the root-owned store.
func (a workflowStoreAdapter) Prune(ctx context.Context, before time.Time) error {
	return a.store.Prune(ctx, before)
}
