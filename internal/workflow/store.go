package workflow

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrNotFound reports that workflow state is absent from a store.
var ErrNotFound = errors.New("bus record not found")

// errUnsupportedTransitionReceipt keeps mixed-version workers from treating
// unreadable provenance as either a missing receipt or permission to replay.
var errUnsupportedTransitionReceipt = errors.New("unsupported workflow transition receipt")

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

// BatchJobOutcome identifies the durable result that first settled one member.
type BatchJobOutcome string

const (
	// BatchJobSucceeded records successful member settlement.
	BatchJobSucceeded BatchJobOutcome = "succeeded"
	// BatchJobFailed records failed member settlement.
	BatchJobFailed BatchJobOutcome = "failed"
)

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

// Store defines the compatibility state transitions required by chain, batch,
// and callback orchestration; built-ins also implement outcomeStore.
type Store interface {
	// CreateChain persists a newly accepted chain.
	CreateChain(ctx context.Context, rec ChainRecord) error
	// AdvanceChain atomically claims completedNode and returns the current successor.
	// Repeating the same (chainID, completedNode) claim must not advance again.
	// When done is true, GetChain must immediately expose Completed or Failed state.
	AdvanceChain(ctx context.Context, chainID string, completedNode string) (next *ChainNode, done bool, err error)
	// FailChain commits terminal failure without replacing completed state.
	FailChain(ctx context.Context, chainID string, cause error) error
	// GetChain returns current chain state.
	GetChain(ctx context.Context, chainID string) (ChainState, error)

	// CreateBatch persists a newly accepted batch.
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

// outcomeStore exposes first-writer outcome arbitration without expanding the
// compatibility-critical Store interface implemented by existing consumers.
type outcomeStore interface {
	FailChainNode(ctx context.Context, chainID, nodeID string, cause error) (ChainState, bool, error)
	// SettleBatchJob arbitrates the durable category while the established batch
	// model keeps failure detail local to the physical delivery that reports it.
	SettleBatchJob(ctx context.Context, batchID, jobID string, outcome BatchJobOutcome, cause error) (BatchState, bool, error)
}

type transitionClaim struct {
	deliveryID     string
	attempt        int
	dispatchID     string
	jobID          string
	jobFingerprint string
}

// valid reports whether a settlement generation supplied every identity field
// required for durable transition provenance.
func (c transitionClaim) valid() bool {
	return c.deliveryID != "" && c.attempt >= 0 && c.dispatchID != "" && c.jobID != "" && c.jobFingerprint != ""
}

type transitionReceipt struct {
	version            int
	eventSchemaVersion int
	workflowKind       string
	workflowID         string
	workflowDispatchID string
	workflowCreatedAt  time.Time
	memberID           string
	outcome            BatchJobOutcome
	owner              transitionClaim
	aggregateCompleted bool
	aggregateCancelled bool
	createdAt          time.Time
}

type transitionReceiptKey struct {
	workflowKind string
	workflowID   string
	memberID     string
}

const (
	transitionReceiptVersion = 1
	chainTransitionKind      = "chain"
	batchTransitionKind      = "batch"
)

// supported reports whether this runtime can interpret both durable identity
// and the event contract reconstructed from it.
func (r transitionReceipt) supported() bool {
	return r.version == transitionReceiptVersion && r.eventSchemaVersion == eventSchemaVersion
}

// validateTransitionReceiptSupport fails closed when a worker cannot interpret
// either the durable receipt identity or the observer facts reconstructed from it.
func validateTransitionReceiptSupport(receipt transitionReceipt) error {
	if receipt.supported() {
		return nil
	}
	return fmt.Errorf("%w: receipt version %d, event schema %d", errUnsupportedTransitionReceipt, receipt.version, receipt.eventSchemaVersion)
}

// chainAdvanceResult distinguishes the logical success owner from the physical
// delivery that claimed it so recovery never repeats continuation effects.
type chainAdvanceResult struct {
	state        ChainState
	next         *ChainNode
	done         bool
	successOwned bool
	claimedNow   bool
	receipt      transitionReceipt
	receiptKnown bool
}

// chainAdvanceStore exposes built-in atomic transition ownership without
// expanding the compatibility-critical Store interface.
type chainAdvanceStore interface {
	advanceChainOutcome(ctx context.Context, chainID, nodeID string, claim transitionClaim) (chainAdvanceResult, error)
}

// chainFailureResult distinguishes durable failure ownership from the physical
// delivery that atomically persisted its recovery receipt.
type chainFailureResult struct {
	state        ChainState
	owned        bool
	claimedNow   bool
	receipt      transitionReceipt
	receiptKnown bool
}

// chainFailureStore exposes built-in atomic failure provenance without
// expanding either Store or the established outcomeStore capability.
type chainFailureStore interface {
	failChainOutcome(ctx context.Context, chainID, nodeID string, cause error, claim transitionClaim) (chainFailureResult, error)
}

// batchSettlementResult separates first-writer category ownership from the
// delivery that changed aggregate counters in this transaction.
type batchSettlementResult struct {
	state        BatchState
	owned        bool
	claimedNow   bool
	receipt      transitionReceipt
	receiptKnown bool
}

// batchSettlementStore exposes built-in member transition ownership without
// requiring established custom stores to implement another public method.
type batchSettlementStore interface {
	settleBatchOutcome(ctx context.Context, batchID, jobID string, outcome BatchJobOutcome, cause error, claim transitionClaim) (batchSettlementResult, error)
}

// transitionReceiptStore exposes durable writer identity only to the workflow
// engine; established public stores remain source-compatible.
type transitionReceiptStore interface {
	chainTransitionReceipt(ctx context.Context, chainID, nodeID string) (transitionReceipt, bool, error)
	batchTransitionReceipt(ctx context.Context, batchID, jobID string) (transitionReceipt, bool, error)
}

// chainNodePosition resolves persisted order so stale and future deliveries
// cannot mutate the aggregate merely because they carry a valid chain ID.
func chainNodePosition(nodes []ChainNode, nodeID string) (int, bool) {
	for index := range nodes {
		if nodes[index].NodeID == nodeID {
			return index, true
		}
	}
	return 0, false
}

// validateChainState rejects representations that cannot prove which nodes
// committed before recovery reconstructs any externally visible fact.
func validateChainState(state ChainState) error {
	if err := validateChainRecord(ChainRecord{ChainID: state.ChainID, Nodes: state.Nodes}); err != nil {
		return err
	}
	if state.NextIndex < 0 || state.NextIndex > len(state.Nodes) {
		return fmt.Errorf("chain %q has invalid next index %d", state.ChainID, state.NextIndex)
	}
	if state.Completed != (state.NextIndex == len(state.Nodes)) {
		return fmt.Errorf("chain %q completion does not match next index %d", state.ChainID, state.NextIndex)
	}
	return nil
}

// chainNodeSuccessDisposition reports whether the persisted ordering proves
// that node success won while rejecting future or internally inconsistent deliveries.
func chainNodeSuccessDisposition(state ChainState, nodeID string) (bool, error) {
	if err := validateChainState(state); err != nil {
		return false, err
	}
	index, ok := chainNodePosition(state.Nodes, nodeID)
	if !ok {
		return false, fmt.Errorf("chain %q does not contain node %q", state.ChainID, nodeID)
	}
	if index > state.NextIndex {
		return false, fmt.Errorf("chain %q received node %q before node %q", state.ChainID, nodeID, state.Nodes[state.NextIndex].NodeID)
	}
	return index < state.NextIndex, nil
}

// validateChainRecord rejects ambiguous order because duplicate or empty node
// IDs make physical redelivery indistinguishable from a different chain step.
func validateChainRecord(record ChainRecord) error {
	if record.ChainID == "" {
		return errors.New("chain id is required")
	}
	if len(record.Nodes) == 0 {
		return errors.New("chain requires at least one node")
	}
	seen := make(map[string]struct{}, len(record.Nodes))
	for _, node := range record.Nodes {
		if node.NodeID == "" {
			return errors.New("chain node id is required")
		}
		if _, exists := seen[node.NodeID]; exists {
			return fmt.Errorf("chain contains duplicate node id %q", node.NodeID)
		}
		seen[node.NodeID] = struct{}{}
	}
	return nil
}

// validateBatchRecord rejects ambiguous member identity because first-writer
// outcome ownership is keyed by the stable (batchID, jobID) pair.
func validateBatchRecord(record BatchRecord) error {
	if record.BatchID == "" {
		return errors.New("batch id is required")
	}
	if len(record.Jobs) == 0 {
		return errors.New("batch requires at least one job")
	}
	seen := make(map[string]struct{}, len(record.Jobs))
	for _, job := range record.Jobs {
		if job.JobID == "" {
			return errors.New("batch job id is required")
		}
		if _, exists := seen[job.JobID]; exists {
			return fmt.Errorf("batch contains duplicate job id %q", job.JobID)
		}
		seen[job.JobID] = struct{}{}
	}
	return nil
}

// cloneChainNodes isolates immutable order and payload bytes from callers that
// retain either a creation record or a state returned by the memory store.
func cloneChainNodes(nodes []ChainNode) []ChainNode {
	if nodes == nil {
		return nil
	}
	cloned := make([]ChainNode, len(nodes))
	for index := range nodes {
		cloned[index] = cloneChainNode(nodes[index])
	}
	return cloned
}

// cloneChainNode copies the only reference-bearing field in a persisted node.
func cloneChainNode(node ChainNode) ChainNode {
	cloned := node
	cloned.Job.Payload = append([]byte(nil), node.Job.Payload...)
	return cloned
}

// chainNodeAdvanceDisposition separates immutable order validation from the
// store-specific compare-and-swap that owns the current node's success.
func chainNodeAdvanceDisposition(state ChainState, nodeID string) (next *ChainNode, done, claimable bool, err error) {
	index, ok := chainNodePosition(state.Nodes, nodeID)
	if !ok {
		return nil, false, false, fmt.Errorf("chain %q does not contain node %q", state.ChainID, nodeID)
	}
	if state.Completed {
		return nil, true, false, nil
	}
	if state.NextIndex < 0 || state.NextIndex >= len(state.Nodes) {
		return nil, false, false, fmt.Errorf("chain %q has invalid next index %d", state.ChainID, state.NextIndex)
	}
	if state.Failed {
		if index > state.NextIndex {
			return nil, false, false, fmt.Errorf("chain %q received node %q after failure at node %q", state.ChainID, nodeID, state.Nodes[state.NextIndex].NodeID)
		}
		return nil, true, false, nil
	}
	if index > state.NextIndex {
		return nil, false, false, fmt.Errorf("chain %q received node %q before node %q", state.ChainID, nodeID, state.Nodes[state.NextIndex].NodeID)
	}
	if index == state.NextIndex {
		return nil, false, true, nil
	}
	node := state.Nodes[state.NextIndex]
	return &node, false, false, nil
}

// chainNodeFailureDisposition classifies whether failure already owns a node,
// can still claim it, or lost to an earlier successful transition.
func chainNodeFailureDisposition(state ChainState, nodeID string) (owned, claimable bool, err error) {
	index, ok := chainNodePosition(state.Nodes, nodeID)
	if !ok {
		return false, false, fmt.Errorf("chain %q does not contain node %q", state.ChainID, nodeID)
	}
	// Legacy SQL rows can contain both flags only when completion happened
	// first, because the old advancement path never completed a failed chain.
	if state.Completed {
		return false, false, nil
	}
	if state.NextIndex < 0 || state.NextIndex >= len(state.Nodes) {
		return false, false, fmt.Errorf("chain %q has invalid next index %d", state.ChainID, state.NextIndex)
	}
	if state.Failed {
		if index > state.NextIndex {
			return false, false, fmt.Errorf("chain %q received node %q after failure at node %q", state.ChainID, nodeID, state.Nodes[state.NextIndex].NodeID)
		}
		return index == state.NextIndex, false, nil
	}
	if index < state.NextIndex {
		return false, false, nil
	}
	if index > state.NextIndex {
		return false, false, fmt.Errorf("chain %q received node %q before node %q", state.ChainID, nodeID, state.Nodes[state.NextIndex].NodeID)
	}
	return false, true, nil
}
