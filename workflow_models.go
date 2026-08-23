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

// PayloadAs unmarshals the delivered payload and returns it as T.
// @group Queue
//
// Example: typed message payload
//
//	type EmailPayload struct {
//		To string `json:"to"`
//	}
//	message := queue.NewMessage("emails:send", []byte(`{"to":"user@example.com"}`))
//	payload, err := message.PayloadAs[EmailPayload]()
//	fmt.Println(err == nil, payload.To)
//	// true user@example.com
func (m Message) PayloadAs[T any]() (T, error) {
	var out T
	err := m.Bind(&out)
	return out, err
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

// BatchJobOutcome identifies the durable result that first settled one batch member.
// @group Queue
type BatchJobOutcome string

const (
	// BatchJobSucceeded records successful member settlement.
	// @group Queue
	BatchJobSucceeded BatchJobOutcome = "succeeded"
	// BatchJobFailed records failed member settlement.
	// @group Queue
	BatchJobFailed BatchJobOutcome = "failed"
)

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
// Implement WorkflowOutcomeStore as well when a custom store must arbitrate
// contradictory physical deliveries atomically; built-in stores provide both.
// @group Queue
type WorkflowStore interface {
	// CreateChain persists a newly accepted chain. ChainID and every NodeID must
	// be non-empty, Nodes must contain at least one entry, and NodeIDs must be unique.
	CreateChain(ctx context.Context, rec ChainRecord) error
	// AdvanceChain atomically claims completedNode and returns the current successor.
	// Repeating the same (chainID, completedNode) claim must not advance again.
	// When done is true, GetChain must immediately expose Completed or Failed state.
	AdvanceChain(ctx context.Context, chainID string, completedNode string) (next *ChainNode, done bool, err error)
	// FailChain commits terminal failure without replacing completed state.
	FailChain(ctx context.Context, chainID string, cause error) error
	// GetChain returns current chain state.
	GetChain(ctx context.Context, chainID string) (ChainState, error)

	// CreateBatch persists a newly accepted batch. BatchID and every JobID must
	// be non-empty, Jobs must contain at least one entry, and JobIDs must be unique.
	CreateBatch(ctx context.Context, rec BatchRecord) error
	// MarkBatchJobStarted records that one batch member began execution.
	MarkBatchJobStarted(ctx context.Context, batchID, jobID string) error
	// MarkBatchJobSucceeded commits the first outcome for (batchID, jobID).
	// Duplicate outcomes must return current state without changing counters.
	MarkBatchJobSucceeded(ctx context.Context, batchID, jobID string) (BatchState, bool, error)
	// MarkBatchJobFailed commits the first outcome for (batchID, jobID).
	// Duplicate outcomes must return current state without changing counters.
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

// WorkflowOutcomeStore strengthens WorkflowStore with first-writer ownership
// when duplicate physical deliveries disagree about a logical job outcome.
// Built-in stores implement this additive capability; established custom
// WorkflowStore implementations remain source-compatible.
// @group Queue
type WorkflowOutcomeStore interface {
	WorkflowStore

	// FailChainNode commits failure only while nodeID is the current unsettled node.
	// owned remains true on replay while that node's failure owns the chain.
	FailChainNode(ctx context.Context, chainID, nodeID string, cause error) (state ChainState, owned bool, err error)
	// SettleBatchJob returns the first committed outcome for one batch member.
	// owned remains true on same-outcome replay and false when the opposite outcome won.
	// Ownership covers the outcome category; BatchState does not retain a per-member cause.
	SettleBatchJob(ctx context.Context, batchID, jobID string, outcome BatchJobOutcome, cause error) (state BatchState, owned bool, err error)
}
