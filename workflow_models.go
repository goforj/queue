package queue

import (
	"context"
	"encoding/json"
	"time"
)

// Message is the delivered logical job message passed to queue handlers and middleware.
// Its exported fields carry workflow correlation metadata while its payload remains
// isolated behind PayloadBytes and Bind.
// @group Queue
type Message struct {
	SchemaVersion int
	DispatchID    string
	JobID         string
	ChainID       string
	BatchID       string
	Attempt       int
	JobType       string
	payload       []byte
}

// NewMessage creates a logical queue message from an application job type and exact payload bytes.
// The payload is copied so callers can safely reuse or mutate their input buffer.
// @group Constructors
func NewMessage(jobType string, payload []byte) Message {
	return Message{
		JobType: jobType,
		payload: cloneWorkflowPayload(payload),
	}
}

// PayloadBytes returns an isolated copy of the raw job payload.
// @group Queue
func (m Message) PayloadBytes() []byte {
	return cloneWorkflowPayload(m.payload)
}

// Bind unmarshals the raw job payload into dst.
// @group Queue
func (m Message) Bind(dst any) error {
	return json.Unmarshal(m.payload, dst)
}

// DispatchResult identifies an accepted logical dispatch.
// @group Queue
type DispatchResult struct {
	DispatchID string
}

// StoredJobOptions is the stable delivery-policy shape persisted inside workflow records.
// Field names intentionally retain their version-one JSON casing.
// @group Queue
type StoredJobOptions struct {
	Queue     string
	Delay     time.Duration
	Timeout   time.Duration
	Retry     int
	Backoff   time.Duration
	UniqueFor time.Duration
}

// StoredJob is the stable logical-job shape persisted inside workflow records.
// @group Queue
type StoredJob struct {
	Type    string           `json:"type"`
	Payload []byte           `json:"payload"`
	Options StoredJobOptions `json:"options"`
}

// ChainNode is one persisted step in a chain workflow.
// @group Queue
type ChainNode struct {
	NodeID string
	Job    StoredJob
}

// ChainRecord is the persisted representation used to create a chain workflow.
// @group Queue
type ChainRecord struct {
	ChainID    string
	DispatchID string
	Queue      string
	Nodes      []ChainNode
	CreatedAt  time.Time
}

// ChainState is the persisted view of a chain workflow.
// @group Queue
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

// BatchJob is one persisted member of a batch workflow.
// @group Queue
type BatchJob struct {
	JobID string
	Job   StoredJob
}

// BatchRecord is the persisted representation used to create a batch workflow.
// @group Queue
type BatchRecord struct {
	BatchID     string
	DispatchID  string
	Name        string
	Queue       string
	AllowFailed bool
	Jobs        []BatchJob
	CreatedAt   time.Time
}

// BatchState is the persisted aggregate execution view of a batch workflow.
// @group Queue
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

// WorkflowStore persists chain, batch, and callback state for orchestration.
// @group Queue
type WorkflowStore interface {
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
