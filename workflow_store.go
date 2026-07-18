package queue

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/goforj/queue/internal/workflow"
)

// SQLStoreConfig configures connection ownership, dialect binding, and schema setup for a SQL workflow store.
// @group Queue
type SQLStoreConfig struct {
	DB          *sql.DB
	DriverName  string
	DSN         string
	AutoMigrate bool
}

// ErrWorkflowNotFound indicates a workflow state record is not present.
// @group Queue
var ErrWorkflowNotFound = workflow.ErrNotFound

// NewMemoryStore creates an in-memory workflow state store. It copies chain
// nodes and payload bytes on creation and return so callers retain independent ownership.
// @group Constructors
func NewMemoryStore() WorkflowStore {
	return &workflowStoreView{store: workflow.NewMemoryStore()}
}

// NewSQLStore creates a SQL-backed workflow state store.
// @group Constructors
func NewSQLStore(config SQLStoreConfig) (WorkflowStore, error) {
	store, err := workflow.NewSQLStore(workflow.SQLStoreConfig{
		DB:          config.DB,
		DriverName:  config.DriverName,
		DSN:         config.DSN,
		AutoMigrate: config.AutoMigrate,
	})
	if err != nil {
		return nil, err
	}
	return &workflowStoreView{store: store}, nil
}

// workflowStoreProvider identifies built-in root stores whose engine implementation can be reused directly.
type workflowStoreProvider interface {
	workflowStore() workflow.Store
}

// workflowStoreView exposes an internal built-in store through root-owned workflow models.
type workflowStoreView struct {
	store workflow.Store
}

var _ WorkflowStore = (*workflowStoreView)(nil)
var _ WorkflowOutcomeStore = (*workflowStoreView)(nil)

// workflowStore returns the built-in engine store so Queue construction avoids a redundant adapter layer.
func (s *workflowStoreView) workflowStore() workflow.Store {
	return s.store
}

// CreateChain persists a root-owned chain record through the built-in store.
func (s *workflowStoreView) CreateChain(ctx context.Context, record ChainRecord) error {
	return s.store.CreateChain(ctx, chainRecordToWorkflow(record))
}

// AdvanceChain commits a chain node and converts any returned successor into the root model.
func (s *workflowStoreView) AdvanceChain(ctx context.Context, chainID string, completedNode string) (*ChainNode, bool, error) {
	next, done, err := s.store.AdvanceChain(ctx, chainID, completedNode)
	if next == nil {
		return nil, done, err
	}
	converted := chainNodeFromWorkflow(*next)
	return &converted, done, err
}

// FailChain commits a terminal chain failure through the built-in store.
func (s *workflowStoreView) FailChain(ctx context.Context, chainID string, cause error) error {
	return s.store.FailChain(ctx, chainID, cause)
}

// FailChainNode exposes the built-in store's atomic per-node failure ownership.
func (s *workflowStoreView) FailChainNode(ctx context.Context, chainID, nodeID string, cause error) (ChainState, bool, error) {
	store, ok := s.store.(interface {
		FailChainNode(context.Context, string, string, error) (workflow.ChainState, bool, error)
	})
	if !ok {
		return ChainState{}, false, errors.New("workflow store does not support atomic chain-node failure")
	}
	state, owned, err := store.FailChainNode(ctx, chainID, nodeID, cause)
	return chainStateFromWorkflow(state), owned, err
}

// SettleBatchJob exposes the built-in store's first-writer member outcome.
func (s *workflowStoreView) SettleBatchJob(ctx context.Context, batchID, jobID string, outcome BatchJobOutcome, cause error) (BatchState, bool, error) {
	store, ok := s.store.(interface {
		SettleBatchJob(context.Context, string, string, workflow.BatchJobOutcome, error) (workflow.BatchState, bool, error)
	})
	if !ok {
		return BatchState{}, false, errors.New("workflow store does not support atomic batch-job outcomes")
	}
	state, owned, err := store.SettleBatchJob(ctx, batchID, jobID, workflow.BatchJobOutcome(outcome), cause)
	return batchStateFromWorkflow(state), owned, err
}

// GetChain reads and converts current chain state from the built-in store.
func (s *workflowStoreView) GetChain(ctx context.Context, chainID string) (ChainState, error) {
	state, err := s.store.GetChain(ctx, chainID)
	return chainStateFromWorkflow(state), err
}

// CreateBatch persists a root-owned batch record through the built-in store.
func (s *workflowStoreView) CreateBatch(ctx context.Context, record BatchRecord) error {
	return s.store.CreateBatch(ctx, batchRecordToWorkflow(record))
}

// MarkBatchJobStarted records a started member through the built-in store.
func (s *workflowStoreView) MarkBatchJobStarted(ctx context.Context, batchID, jobID string) error {
	return s.store.MarkBatchJobStarted(ctx, batchID, jobID)
}

// MarkBatchJobSucceeded commits a successful member and converts aggregate state.
func (s *workflowStoreView) MarkBatchJobSucceeded(ctx context.Context, batchID, jobID string) (BatchState, bool, error) {
	state, done, err := s.store.MarkBatchJobSucceeded(ctx, batchID, jobID)
	return batchStateFromWorkflow(state), done, err
}

// MarkBatchJobFailed commits a failed member and converts aggregate state.
func (s *workflowStoreView) MarkBatchJobFailed(ctx context.Context, batchID, jobID string, cause error) (BatchState, bool, error) {
	state, done, err := s.store.MarkBatchJobFailed(ctx, batchID, jobID, cause)
	return batchStateFromWorkflow(state), done, err
}

// CancelBatch commits aggregate cancellation through the built-in store.
func (s *workflowStoreView) CancelBatch(ctx context.Context, batchID string) error {
	return s.store.CancelBatch(ctx, batchID)
}

// GetBatch reads and converts current aggregate state from the built-in store.
func (s *workflowStoreView) GetBatch(ctx context.Context, batchID string) (BatchState, error) {
	state, err := s.store.GetBatch(ctx, batchID)
	return batchStateFromWorkflow(state), err
}

// MarkCallbackInvoked claims a callback idempotency key through the built-in store.
func (s *workflowStoreView) MarkCallbackInvoked(ctx context.Context, key string) (bool, error) {
	return s.store.MarkCallbackInvoked(ctx, key)
}

// Prune removes terminal state older than before through the built-in store.
func (s *workflowStoreView) Prune(ctx context.Context, before time.Time) error {
	return s.store.Prune(ctx, before)
}

// rootWorkflowStoreAdapter presents an application-defined root store to the private engine.
type rootWorkflowStoreAdapter struct {
	store WorkflowStore
}

var _ workflow.Store = rootWorkflowStoreAdapter{}

type rootWorkflowOutcomeStoreAdapter struct {
	rootWorkflowStoreAdapter
	atomic WorkflowOutcomeStore
}

// FailChainNode converts an atomic custom-store result back into the engine model.
func (a rootWorkflowOutcomeStoreAdapter) FailChainNode(ctx context.Context, chainID, nodeID string, cause error) (workflow.ChainState, bool, error) {
	state, owned, err := a.atomic.FailChainNode(ctx, chainID, nodeID, cause)
	return chainStateToWorkflow(state), owned, err
}

// SettleBatchJob converts an atomic custom-store result back into the engine model.
func (a rootWorkflowOutcomeStoreAdapter) SettleBatchJob(ctx context.Context, batchID, jobID string, outcome workflow.BatchJobOutcome, cause error) (workflow.BatchState, bool, error) {
	state, owned, err := a.atomic.SettleBatchJob(ctx, batchID, jobID, BatchJobOutcome(outcome), cause)
	return batchStateToWorkflow(state), owned, err
}

// CreateChain converts the engine record before invoking the application store.
func (a rootWorkflowStoreAdapter) CreateChain(ctx context.Context, record workflow.ChainRecord) error {
	return a.store.CreateChain(ctx, chainRecordFromWorkflow(record))
}

// AdvanceChain converts the application store's successor back into the engine model.
func (a rootWorkflowStoreAdapter) AdvanceChain(ctx context.Context, chainID string, completedNode string) (*workflow.ChainNode, bool, error) {
	next, done, err := a.store.AdvanceChain(ctx, chainID, completedNode)
	if next == nil {
		return nil, done, err
	}
	converted := chainNodeToWorkflow(*next)
	return &converted, done, err
}

// FailChain forwards a terminal failure without changing its error chain.
func (a rootWorkflowStoreAdapter) FailChain(ctx context.Context, chainID string, cause error) error {
	return a.store.FailChain(ctx, chainID, cause)
}

// GetChain converts application-owned chain state back into the engine model.
func (a rootWorkflowStoreAdapter) GetChain(ctx context.Context, chainID string) (workflow.ChainState, error) {
	state, err := a.store.GetChain(ctx, chainID)
	return chainStateToWorkflow(state), err
}

// CreateBatch converts the engine record before invoking the application store.
func (a rootWorkflowStoreAdapter) CreateBatch(ctx context.Context, record workflow.BatchRecord) error {
	return a.store.CreateBatch(ctx, batchRecordFromWorkflow(record))
}

// MarkBatchJobStarted forwards a started marker to the application store.
func (a rootWorkflowStoreAdapter) MarkBatchJobStarted(ctx context.Context, batchID, jobID string) error {
	return a.store.MarkBatchJobStarted(ctx, batchID, jobID)
}

// MarkBatchJobSucceeded converts application aggregate state back into the engine model.
func (a rootWorkflowStoreAdapter) MarkBatchJobSucceeded(ctx context.Context, batchID, jobID string) (workflow.BatchState, bool, error) {
	state, done, err := a.store.MarkBatchJobSucceeded(ctx, batchID, jobID)
	return batchStateToWorkflow(state), done, err
}

// MarkBatchJobFailed converts application aggregate state back into the engine model.
func (a rootWorkflowStoreAdapter) MarkBatchJobFailed(ctx context.Context, batchID, jobID string, cause error) (workflow.BatchState, bool, error) {
	state, done, err := a.store.MarkBatchJobFailed(ctx, batchID, jobID, cause)
	return batchStateToWorkflow(state), done, err
}

// CancelBatch forwards aggregate cancellation to the application store.
func (a rootWorkflowStoreAdapter) CancelBatch(ctx context.Context, batchID string) error {
	return a.store.CancelBatch(ctx, batchID)
}

// GetBatch converts application-owned aggregate state back into the engine model.
func (a rootWorkflowStoreAdapter) GetBatch(ctx context.Context, batchID string) (workflow.BatchState, error) {
	state, err := a.store.GetBatch(ctx, batchID)
	return batchStateToWorkflow(state), err
}

// MarkCallbackInvoked forwards callback idempotency claims to the application store.
func (a rootWorkflowStoreAdapter) MarkCallbackInvoked(ctx context.Context, key string) (bool, error) {
	return a.store.MarkCallbackInvoked(ctx, key)
}

// Prune forwards terminal-state retention to the application store.
func (a rootWorkflowStoreAdapter) Prune(ctx context.Context, before time.Time) error {
	return a.store.Prune(ctx, before)
}

// workflowStoreFromRoot unwraps built-ins and adapts application-defined stores exactly once.
func workflowStoreFromRoot(store WorkflowStore) workflow.Store {
	if store == nil {
		return nil
	}
	if provider, ok := store.(workflowStoreProvider); ok {
		return provider.workflowStore()
	}
	adapter := rootWorkflowStoreAdapter{store: store}
	if atomic, ok := store.(WorkflowOutcomeStore); ok {
		return rootWorkflowOutcomeStoreAdapter{rootWorkflowStoreAdapter: adapter, atomic: atomic}
	}
	return adapter
}
