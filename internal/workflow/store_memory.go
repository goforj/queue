package workflow

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// NewMemoryStore creates an in-memory orchestration store implementation.
func NewMemoryStore() Store {
	return &memoryStore{
		chains:             make(map[string]*memoryChain),
		batch:              make(map[string]*memoryBatch),
		callbacks:          make(map[string]time.Time),
		transitionReceipts: make(map[transitionReceiptKey]transitionReceipt),
	}
}

type memoryStore struct {
	mu                 sync.Mutex
	chains             map[string]*memoryChain
	batch              map[string]*memoryBatch
	callbacks          map[string]time.Time
	transitionReceipts map[transitionReceiptKey]transitionReceipt
}

var _ Store = (*memoryStore)(nil)
var _ chainAdvanceStore = (*memoryStore)(nil)
var _ chainFailureStore = (*memoryStore)(nil)
var _ batchSettlementStore = (*memoryStore)(nil)
var _ transitionReceiptStore = (*memoryStore)(nil)

type memoryChain struct {
	state         ChainState
	completedNode map[string]bool
}

type batchJobStatus struct {
	started bool
	done    bool
	failed  bool
}

type memoryBatch struct {
	state BatchState
	jobs  map[string]batchJobStatus
}

// CreateChain installs the complete chain under one mutex so readers never observe partial state.
func (m *memoryStore) CreateChain(_ context.Context, rec ChainRecord) error {
	if err := validateChainRecord(rec); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	m.deleteTransitionReceipts(chainTransitionKind, rec.ChainID)
	m.chains[rec.ChainID] = &memoryChain{
		state: ChainState{
			ChainID:    rec.ChainID,
			DispatchID: rec.DispatchID,
			Queue:      rec.Queue,
			Nodes:      cloneChainNodes(rec.Nodes),
			NextIndex:  0,
			CreatedAt:  rec.CreatedAt,
			UpdatedAt:  now,
		},
		completedNode: make(map[string]bool),
	}
	return nil
}

// AdvanceChain serializes node deduplication and index advancement so retries cannot skip work.
func (m *memoryStore) AdvanceChain(ctx context.Context, chainID string, completedNode string) (next *ChainNode, done bool, err error) {
	result, err := m.advanceChainOutcome(ctx, chainID, completedNode, transitionClaim{})
	return result.next, result.done, err
}

// advanceChainOutcome retains transition ownership under the same mutex used
// for node advancement so a racing delivery cannot repeat continuation effects.
func (m *memoryStore) advanceChainOutcome(_ context.Context, chainID string, completedNode string, claim transitionClaim) (chainAdvanceResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ch, ok := m.chains[chainID]
	if !ok {
		return chainAdvanceResult{}, ErrNotFound
	}
	successOwned, err := chainNodeSuccessDisposition(ch.state, completedNode)
	if err != nil {
		return chainAdvanceResult{}, err
	}
	next, done, claimable, err := chainNodeAdvanceDisposition(ch.state, completedNode)
	if err != nil {
		return chainAdvanceResult{}, err
	}
	if !claimable {
		if next != nil {
			cloned := cloneChainNode(*next)
			next = &cloned
		}
		state := ch.state
		state.Nodes = cloneChainNodes(state.Nodes)
		if state.DispatchID != "" && claim.dispatchID != "" && state.DispatchID != claim.dispatchID {
			return chainAdvanceResult{state: state}, nil
		}
		receipt, receiptKnown := m.transitionReceipt(chainTransitionKind, chainID, completedNode)
		return chainAdvanceResult{state: state, next: next, done: done, successOwned: successOwned, receipt: receipt, receiptKnown: receiptKnown}, nil
	}
	if ch.state.DispatchID != "" && claim.dispatchID != "" && ch.state.DispatchID != claim.dispatchID {
		return chainAdvanceResult{}, fmt.Errorf("chain %q dispatch mismatch", chainID)
	}
	ch.completedNode[completedNode] = true
	ch.state.NextIndex++
	ch.state.UpdatedAt = time.Now()
	if ch.state.NextIndex >= len(ch.state.Nodes) {
		ch.state.Completed = true
		state := ch.state
		state.Nodes = cloneChainNodes(state.Nodes)
		receipt, receiptKnown := m.recordTransitionReceipt(chainTransitionKind, chainID, state.DispatchID, state.CreatedAt, completedNode, BatchJobSucceeded, claim, true, false)
		return chainAdvanceResult{state: state, done: true, successOwned: true, claimedNow: true, receipt: receipt, receiptKnown: receiptKnown}, nil
	}
	n := cloneChainNode(ch.state.Nodes[ch.state.NextIndex])
	state := ch.state
	state.Nodes = cloneChainNodes(state.Nodes)
	receipt, receiptKnown := m.recordTransitionReceipt(chainTransitionKind, chainID, state.DispatchID, state.CreatedAt, completedNode, BatchJobSucceeded, claim, false, false)
	return chainAdvanceResult{state: state, next: &n, successOwned: true, claimedNow: true, receipt: receipt, receiptKnown: receiptKnown}, nil
}

// FailChainNode serializes failure against advancement so the first outcome
// for a sequential node remains authoritative across physical redelivery.
func (m *memoryStore) FailChainNode(ctx context.Context, chainID, nodeID string, cause error) (ChainState, bool, error) {
	result, err := m.failChainOutcome(ctx, chainID, nodeID, cause, transitionClaim{})
	return result.state, result.owned, err
}

// failChainOutcome records terminal failure and its exact delivery generation
// under one mutex so recovery never needs to replay application code.
func (m *memoryStore) failChainOutcome(_ context.Context, chainID, nodeID string, cause error, claim transitionClaim) (chainFailureResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ch, ok := m.chains[chainID]
	if !ok {
		return chainFailureResult{}, ErrNotFound
	}
	owned, claimable, err := chainNodeFailureDisposition(ch.state, nodeID)
	if err != nil {
		return chainFailureResult{}, err
	}
	if !claimable {
		state := ch.state
		state.Nodes = cloneChainNodes(state.Nodes)
		if state.DispatchID != "" && claim.dispatchID != "" && state.DispatchID != claim.dispatchID {
			return chainFailureResult{state: state}, nil
		}
		receipt, receiptKnown := m.transitionReceipt(chainTransitionKind, chainID, nodeID)
		return chainFailureResult{state: state, owned: owned, receipt: receipt, receiptKnown: receiptKnown}, nil
	}
	if ch.state.DispatchID != "" && claim.dispatchID != "" && ch.state.DispatchID != claim.dispatchID {
		return chainFailureResult{}, fmt.Errorf("chain %q dispatch mismatch", chainID)
	}
	ch.state.Failed = true
	if cause != nil {
		ch.state.Failure = cause.Error()
	}
	ch.state.UpdatedAt = time.Now()
	state := ch.state
	state.Nodes = cloneChainNodes(state.Nodes)
	receipt, receiptKnown := m.recordTransitionReceipt(chainTransitionKind, chainID, state.DispatchID, state.CreatedAt, nodeID, BatchJobFailed, claim, false, false)
	return chainFailureResult{state: state, owned: true, claimedNow: true, receipt: receipt, receiptKnown: receiptKnown}, nil
}

// FailChain leaves completed chains successful while recording a terminal cause for unfinished work.
func (m *memoryStore) FailChain(_ context.Context, chainID string, cause error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	ch, ok := m.chains[chainID]
	if !ok {
		return ErrNotFound
	}
	if !ch.state.Completed && !ch.state.Failed {
		ch.state.Failed = true
		if cause != nil {
			ch.state.Failure = cause.Error()
		}
		ch.state.UpdatedAt = time.Now()
	}
	return nil
}

// GetChain reads chain state under the same mutex used for every mutation.
func (m *memoryStore) GetChain(_ context.Context, chainID string) (ChainState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ch, ok := m.chains[chainID]
	if !ok {
		return ChainState{}, ErrNotFound
	}
	state := ch.state
	state.Nodes = cloneChainNodes(state.Nodes)
	return state, nil
}

// DiscardChain removes exactly one transient recording state. It intentionally
// remains outside Store because production retention continues to use Prune.
func (m *memoryStore) DiscardChain(chainID string) {
	m.mu.Lock()
	delete(m.chains, chainID)
	m.deleteTransitionReceipts(chainTransitionKind, chainID)
	m.mu.Unlock()
}

// CreateBatch installs aggregate and per-job state together so readers cannot observe a partial batch.
func (m *memoryStore) CreateBatch(_ context.Context, rec BatchRecord) error {
	if err := validateBatchRecord(rec); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	m.deleteTransitionReceipts(batchTransitionKind, rec.BatchID)
	st := BatchState{
		BatchID:     rec.BatchID,
		DispatchID:  rec.DispatchID,
		Name:        rec.Name,
		Queue:       rec.Queue,
		AllowFailed: rec.AllowFailed,
		Total:       len(rec.Jobs),
		Pending:     len(rec.Jobs),
		CreatedAt:   rec.CreatedAt,
		UpdatedAt:   now,
	}
	jm := make(map[string]batchJobStatus, len(rec.Jobs))
	for _, job := range rec.Jobs {
		jm[job.JobID] = batchJobStatus{}
	}
	m.batch[rec.BatchID] = &memoryBatch{
		state: st,
		jobs:  jm,
	}
	return nil
}

// MarkBatchJobStarted records a retry-safe started marker without changing aggregate counters.
func (m *memoryStore) MarkBatchJobStarted(_ context.Context, batchID, jobID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.batch[batchID]
	if !ok {
		return ErrNotFound
	}
	js, ok := b.jobs[jobID]
	if !ok {
		return ErrNotFound
	}
	js.started = true
	b.jobs[jobID] = js
	b.state.UpdatedAt = time.Now()
	return nil
}

// MarkBatchJobSucceeded applies completion counters at most once while holding the aggregate lock.
func (m *memoryStore) MarkBatchJobSucceeded(ctx context.Context, batchID, jobID string) (BatchState, bool, error) {
	state, _, err := m.SettleBatchJob(ctx, batchID, jobID, BatchJobSucceeded, nil)
	return state, state.Completed, err
}

// MarkBatchJobFailed counts each failure once and applies fail-fast cancellation atomically.
func (m *memoryStore) MarkBatchJobFailed(ctx context.Context, batchID, jobID string, cause error) (BatchState, bool, error) {
	state, _, err := m.SettleBatchJob(ctx, batchID, jobID, BatchJobFailed, cause)
	return state, state.Completed, err
}

// SettleBatchJob serializes aggregate counters with per-member outcome
// ownership so inconsistent redelivery cannot publish a different result.
func (m *memoryStore) SettleBatchJob(ctx context.Context, batchID, jobID string, outcome BatchJobOutcome, cause error) (BatchState, bool, error) {
	result, err := m.settleBatchOutcome(ctx, batchID, jobID, outcome, cause, transitionClaim{})
	return result.state, result.owned, err
}

// settleBatchOutcome retains the first counter claim alongside category
// ownership so fact recovery cannot impersonate a later terminal member.
func (m *memoryStore) settleBatchOutcome(_ context.Context, batchID, jobID string, outcome BatchJobOutcome, _ error, claim transitionClaim) (batchSettlementResult, error) {
	if outcome != BatchJobSucceeded && outcome != BatchJobFailed {
		return batchSettlementResult{}, fmt.Errorf("unsupported batch job outcome %q", outcome)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.batch[batchID]
	if !ok {
		return batchSettlementResult{}, ErrNotFound
	}
	js, ok := b.jobs[jobID]
	if !ok {
		return batchSettlementResult{}, ErrNotFound
	}
	requestedFailure := outcome == BatchJobFailed
	if js.done {
		if b.state.DispatchID != "" && claim.dispatchID != "" && b.state.DispatchID != claim.dispatchID {
			return batchSettlementResult{state: b.state}, nil
		}
		b.state.UpdatedAt = time.Now()
		receipt, receiptKnown := m.transitionReceipt(batchTransitionKind, batchID, jobID)
		return batchSettlementResult{state: b.state, owned: js.failed == requestedFailure, receipt: receipt, receiptKnown: receiptKnown}, nil
	}
	if b.state.DispatchID != "" && claim.dispatchID != "" && b.state.DispatchID != claim.dispatchID {
		return batchSettlementResult{}, fmt.Errorf("batch %q dispatch mismatch", batchID)
	}
	js.done = true
	js.failed = requestedFailure
	b.jobs[jobID] = js
	wasCompleted := b.state.Completed
	b.state.Pending--
	b.state.Processed++
	if requestedFailure {
		b.state.Failed++
	}
	if requestedFailure && !b.state.AllowFailed {
		b.state.Cancelled = true
		b.state.Completed = true
	} else if b.state.Pending <= 0 {
		b.state.Completed = true
	}
	b.state.UpdatedAt = time.Now()
	aggregateCompleted := !wasCompleted && b.state.Completed
	receipt, receiptKnown := m.recordTransitionReceipt(batchTransitionKind, batchID, b.state.DispatchID, b.state.CreatedAt, jobID, outcome, claim, aggregateCompleted, aggregateCompleted && b.state.Cancelled)
	return batchSettlementResult{state: b.state, owned: true, claimedNow: true, receipt: receipt, receiptKnown: receiptKnown}, nil
}

// recordTransitionReceipt persists immutable generation provenance only when a
// settlement owner supplied a complete transition claim.
func (m *memoryStore) recordTransitionReceipt(kind, workflowID, workflowDispatchID string, workflowCreatedAt time.Time, memberID string, outcome BatchJobOutcome, claim transitionClaim, aggregateCompleted, aggregateCancelled bool) (transitionReceipt, bool) {
	if !claim.valid() {
		return transitionReceipt{}, false
	}
	receipt := transitionReceipt{
		version:            transitionReceiptVersion,
		eventSchemaVersion: eventSchemaVersion,
		workflowKind:       kind,
		workflowID:         workflowID,
		workflowDispatchID: workflowDispatchID,
		workflowCreatedAt:  workflowCreatedAt,
		memberID:           memberID,
		outcome:            outcome,
		owner:              claim,
		aggregateCompleted: aggregateCompleted,
		aggregateCancelled: aggregateCancelled,
		createdAt:          time.Now(),
	}
	m.transitionReceipts[transitionReceiptKey{workflowKind: kind, workflowID: workflowID, memberID: memberID}] = receipt
	return receipt, true
}

// transitionReceipt returns one immutable receipt while the caller holds the
// store mutex used for the corresponding state transition.
func (m *memoryStore) transitionReceipt(kind, workflowID, memberID string) (transitionReceipt, bool) {
	receipt, ok := m.transitionReceipts[transitionReceiptKey{workflowKind: kind, workflowID: workflowID, memberID: memberID}]
	return receipt, ok
}

// chainTransitionReceipt distinguishes corrupt cross-incarnation provenance
// from a genuinely absent receipt so recovery always fails closed.
func (m *memoryStore) chainTransitionReceipt(_ context.Context, chainID, nodeID string) (transitionReceipt, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	chain, ok := m.chains[chainID]
	if !ok {
		return transitionReceipt{}, false, ErrNotFound
	}
	receipt, ok := m.transitionReceipt(chainTransitionKind, chainID, nodeID)
	if !ok {
		return transitionReceipt{}, false, nil
	}
	if receipt.workflowDispatchID != chain.state.DispatchID || !receipt.workflowCreatedAt.Equal(chain.state.CreatedAt) {
		return transitionReceipt{}, false, fmt.Errorf("chain %q transition receipt does not match current workflow incarnation", chainID)
	}
	if err := validateTransitionReceiptSupport(receipt); err != nil {
		return transitionReceipt{}, false, err
	}
	return receipt, true, nil
}

// batchTransitionReceipt returns provenance only when it still belongs to the
// current batch incarnation.
func (m *memoryStore) batchTransitionReceipt(_ context.Context, batchID, jobID string) (transitionReceipt, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	batch, ok := m.batch[batchID]
	if !ok {
		return transitionReceipt{}, false, ErrNotFound
	}
	receipt, ok := m.transitionReceipt(batchTransitionKind, batchID, jobID)
	if !ok {
		return transitionReceipt{}, false, nil
	}
	if receipt.workflowDispatchID != batch.state.DispatchID || !receipt.workflowCreatedAt.Equal(batch.state.CreatedAt) {
		return transitionReceipt{}, false, fmt.Errorf("batch %q transition receipt does not match current workflow incarnation", batchID)
	}
	if err := validateTransitionReceiptSupport(receipt); err != nil {
		return transitionReceipt{}, false, err
	}
	return receipt, true, nil
}

// deleteTransitionReceipts removes stale in-memory provenance before a test or
// local caller intentionally reuses a workflow identifier.
func (m *memoryStore) deleteTransitionReceipts(kind, workflowID string) {
	for key := range m.transitionReceipts {
		if key.workflowKind == kind && key.workflowID == workflowID {
			delete(m.transitionReceipts, key)
		}
	}
}

// CancelBatch marks the batch terminal under the mutation lock so observers see a consistent state.
func (m *memoryStore) CancelBatch(_ context.Context, batchID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.batch[batchID]
	if !ok {
		return ErrNotFound
	}
	b.state.Cancelled = true
	b.state.Completed = true
	b.state.UpdatedAt = time.Now()
	return nil
}

// GetBatch reads aggregate batch state under the same mutex used for settlement.
func (m *memoryStore) GetBatch(_ context.Context, batchID string) (BatchState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.batch[batchID]
	if !ok {
		return BatchState{}, ErrNotFound
	}
	return b.state, nil
}

// DiscardBatch removes exactly one transient recording state. It intentionally
// remains outside Store because production retention continues to use Prune.
func (m *memoryStore) DiscardBatch(batchID string) {
	m.mu.Lock()
	delete(m.batch, batchID)
	m.deleteTransitionReceipts(batchTransitionKind, batchID)
	m.mu.Unlock()
}

// MarkCallbackInvoked atomically reserves a callback key so retries cannot invoke it twice.
func (m *memoryStore) MarkCallbackInvoked(_ context.Context, key string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.callbacks[key]; exists {
		return false, nil
	}
	m.callbacks[key] = time.Now()
	return true, nil
}

// Prune removes only expired terminal workflows and callback markers while mutations are excluded.
func (m *memoryStore) Prune(_ context.Context, before time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for chainID, ch := range m.chains {
		if (ch.state.Completed || ch.state.Failed) && ch.state.UpdatedAt.Before(before) {
			delete(m.chains, chainID)
			m.deleteTransitionReceipts(chainTransitionKind, chainID)
		}
	}
	for batchID, b := range m.batch {
		if b.state.Completed && b.state.UpdatedAt.Before(before) {
			delete(m.batch, batchID)
			m.deleteTransitionReceipts(batchTransitionKind, batchID)
		}
	}
	for key, createdAt := range m.callbacks {
		if createdAt.Before(before) {
			delete(m.callbacks, key)
		}
	}
	return nil
}
