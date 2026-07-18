package workflow

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound reports that workflow state is absent from a store.
var ErrNotFound = errors.New("bus record not found")

// ChainNode binds a stable node identifier to its serialized job.
type ChainNode struct {
	NodeID string
	Job    StoredJob
}

// ChainRecord contains the immutable data required to create a chain.
type ChainRecord struct {
	ChainID    string
	DispatchID string
	Queue      string
	Nodes      []ChainNode
	CreatedAt  time.Time
}

// ChainState is the persisted execution view of a chain.
type ChainState struct {
	ChainID    string
	DispatchID string
	Queue      string
	Nodes      []ChainNode
	NextIndex  int
	Completed  bool
	Failed     bool
	Failure    string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// BatchRecord contains the immutable data required to create a batch.
type BatchRecord struct {
	BatchID     string
	DispatchID  string
	Name        string
	Queue       string
	AllowFailed bool
	Jobs        []BatchJob
	CreatedAt   time.Time
}

// BatchJob binds a stable member identifier to its serialized job.
type BatchJob struct {
	JobID string
	Job   StoredJob
}

// BatchState is the persisted aggregate execution view of a batch.
type BatchState struct {
	BatchID     string
	DispatchID  string
	Name        string
	Queue       string
	AllowFailed bool
	Total       int
	Pending     int
	Processed   int
	Failed      int
	Cancelled   bool
	Completed   bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Store defines the durable state transitions required by chain, batch, and callback orchestration.
type Store interface {
	// CreateChain persists a newly accepted chain.
	CreateChain(ctx context.Context, rec ChainRecord) error
	// AdvanceChain commits one completed node and returns the next node, if any.
	AdvanceChain(ctx context.Context, chainID string, completedNode string) (next *ChainNode, done bool, err error)
	// FailChain commits terminal chain failure.
	FailChain(ctx context.Context, chainID string, cause error) error
	// GetChain returns current chain state.
	GetChain(ctx context.Context, chainID string) (ChainState, error)

	// CreateBatch persists a newly accepted batch.
	CreateBatch(ctx context.Context, rec BatchRecord) error
	// MarkBatchJobStarted records that one batch member began execution.
	MarkBatchJobStarted(ctx context.Context, batchID, jobID string) error
	// MarkBatchJobSucceeded commits one successful batch member.
	MarkBatchJobSucceeded(ctx context.Context, batchID, jobID string) (BatchState, bool, error)
	// MarkBatchJobFailed commits one failed batch member.
	MarkBatchJobFailed(ctx context.Context, batchID, jobID string, cause error) (BatchState, bool, error)
	// CancelBatch commits aggregate batch cancellation.
	CancelBatch(ctx context.Context, batchID string) error
	// GetBatch returns current batch state.
	GetBatch(ctx context.Context, batchID string) (BatchState, error)

	// MarkCallbackInvoked atomically claims one callback idempotency key.
	MarkCallbackInvoked(ctx context.Context, key string) (bool, error)
	// Prune removes terminal workflow state older than before.
	Prune(ctx context.Context, before time.Time) error
}
