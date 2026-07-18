package bus

import "github.com/goforj/queue"

// StoredJob is the stable logical-job shape persisted inside workflow records.
//
// Deprecated: use queue.StoredJob.
type StoredJob = queue.StoredJob

// ChainNode is one persisted chain step.
//
// Deprecated: use queue.ChainNode.
type ChainNode = queue.ChainNode

// ChainRecord is the persisted representation used to create a chain.
//
// Deprecated: use queue.ChainRecord.
type ChainRecord = queue.ChainRecord

// ChainState is the persisted view of a chain workflow.
//
// Deprecated: use queue.ChainState.
type ChainState = queue.ChainState

// BatchJob is one persisted batch member.
//
// Deprecated: use queue.BatchJob.
type BatchJob = queue.BatchJob

// BatchRecord is the persisted representation used to create a batch.
//
// Deprecated: use queue.BatchRecord.
type BatchRecord = queue.BatchRecord

// BatchState is the persisted view of a batch workflow.
//
// Deprecated: use queue.BatchState.
type BatchState = queue.BatchState

// Store persists chain, batch, and callback state.
//
// Deprecated: use queue.WorkflowStore.
type Store = queue.WorkflowStore

// SQLStoreConfig configures the SQL-backed workflow store.
//
// Deprecated: use queue.SQLStoreConfig.
type SQLStoreConfig = queue.SQLStoreConfig

// ErrNotFound indicates a workflow state record is not present.
//
// Deprecated: use queue.ErrWorkflowNotFound.
var ErrNotFound = queue.ErrWorkflowNotFound

// NewMemoryStore creates an in-memory workflow state store.
//
// Deprecated: use queue.NewMemoryStore.
func NewMemoryStore() Store {
	return queue.NewMemoryStore()
}

// NewSQLStore creates a SQL-backed workflow state store.
//
// Deprecated: use queue.NewSQLStore.
func NewSQLStore(cfg SQLStoreConfig) (Store, error) {
	return queue.NewSQLStore(cfg)
}
