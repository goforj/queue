package workflow

import (
	"context"
	"sync"
	"time"
)

// NewMemoryStore creates an in-memory orchestration store implementation.
func NewMemoryStore() Store {
	return &memoryStore{
		chains:    make(map[string]*memoryChain),
		batch:     make(map[string]*memoryBatch),
		callbacks: make(map[string]time.Time),
	}
}

type memoryStore struct {
	mu        sync.Mutex
	chains    map[string]*memoryChain
	batch     map[string]*memoryBatch
	callbacks map[string]time.Time
}

var _ Store = (*memoryStore)(nil)

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
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	m.chains[rec.ChainID] = &memoryChain{
		state: ChainState{
			ChainID:    rec.ChainID,
			DispatchID: rec.DispatchID,
			Queue:      rec.Queue,
			Nodes:      rec.Nodes,
			NextIndex:  0,
			CreatedAt:  rec.CreatedAt,
			UpdatedAt:  now,
		},
		completedNode: make(map[string]bool),
	}
	return nil
}

// AdvanceChain serializes node deduplication and index advancement so retries cannot skip work.
func (m *memoryStore) AdvanceChain(_ context.Context, chainID string, completedNode string) (next *ChainNode, done bool, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ch, ok := m.chains[chainID]
	if !ok {
		return nil, false, ErrNotFound
	}
	if ch.state.Completed || ch.state.Failed {
		return nil, true, nil
	}
	if ch.completedNode[completedNode] {
		if ch.state.NextIndex >= len(ch.state.Nodes) {
			return nil, true, nil
		}
		n := ch.state.Nodes[ch.state.NextIndex]
		return &n, false, nil
	}
	ch.completedNode[completedNode] = true
	ch.state.NextIndex++
	ch.state.UpdatedAt = time.Now()
	if ch.state.NextIndex >= len(ch.state.Nodes) {
		ch.state.Completed = true
		return nil, true, nil
	}
	n := ch.state.Nodes[ch.state.NextIndex]
	return &n, false, nil
}

// FailChain leaves completed chains successful while recording a terminal cause for unfinished work.
func (m *memoryStore) FailChain(_ context.Context, chainID string, cause error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	ch, ok := m.chains[chainID]
	if !ok {
		return ErrNotFound
	}
	if !ch.state.Completed {
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
	return ch.state, nil
}

// CreateBatch installs aggregate and per-job state together so readers cannot observe a partial batch.
func (m *memoryStore) CreateBatch(_ context.Context, rec BatchRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
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
	js := b.jobs[jobID]
	js.started = true
	b.jobs[jobID] = js
	b.state.UpdatedAt = time.Now()
	return nil
}

// MarkBatchJobSucceeded applies completion counters at most once while holding the aggregate lock.
func (m *memoryStore) MarkBatchJobSucceeded(_ context.Context, batchID, jobID string) (BatchState, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.batch[batchID]
	if !ok {
		return BatchState{}, false, ErrNotFound
	}
	js := b.jobs[jobID]
	if !js.done {
		js.done = true
		b.jobs[jobID] = js
		b.state.Pending--
		b.state.Processed++
	}
	if b.state.Pending <= 0 {
		b.state.Completed = true
		b.state.UpdatedAt = time.Now()
		return b.state, true, nil
	}
	b.state.UpdatedAt = time.Now()
	return b.state, false, nil
}

// MarkBatchJobFailed counts each failure once and applies fail-fast cancellation atomically.
func (m *memoryStore) MarkBatchJobFailed(_ context.Context, batchID, jobID string, _ error) (BatchState, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.batch[batchID]
	if !ok {
		return BatchState{}, false, ErrNotFound
	}
	js := b.jobs[jobID]
	if !js.done {
		js.done = true
		js.failed = true
		b.jobs[jobID] = js
		b.state.Pending--
		b.state.Processed++
		b.state.Failed++
	}
	if !b.state.AllowFailed {
		b.state.Cancelled = true
		b.state.Completed = true
		b.state.UpdatedAt = time.Now()
		return b.state, true, nil
	}
	if b.state.Pending <= 0 {
		b.state.Completed = true
		b.state.UpdatedAt = time.Now()
		return b.state, true, nil
	}
	b.state.UpdatedAt = time.Now()
	return b.state, false, nil
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
		}
	}
	for batchID, b := range m.batch {
		if b.state.Completed && b.state.UpdatedAt.Before(before) {
			delete(m.batch, batchID)
		}
	}
	for key, createdAt := range m.callbacks {
		if createdAt.Before(before) {
			delete(m.callbacks, key)
		}
	}
	return nil
}
