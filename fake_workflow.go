package queue

import (
	"context"
	"testing"
	"time"

	"github.com/goforj/queue/internal/workflow"
)

// fakeWorkflowRecorder runs fake workflows through the production engine while
// retaining immutable creation records for assertion-friendly inspection.
type fakeWorkflowRecorder struct {
	state            *fakeQueueState
	engine           workflow.Engine
	store            fakeWorkflowStateStore
	chains           map[string]ChainRecord
	batches          map[string]BatchRecord
	acceptedChainIDs []string
	acceptedBatchIDs []string
}

// fakeWorkflowStateStore narrows the internal memory store to exact rejection
// cleanup without widening the production workflow.Store contract.
type fakeWorkflowStateStore interface {
	workflow.Store
	// FailChainNode preserves first-writer ownership for duplicate fake deliveries.
	FailChainNode(context.Context, string, string, error) (workflow.ChainState, bool, error)
	// SettleBatchJob preserves first-writer ownership for duplicate fake deliveries.
	SettleBatchJob(context.Context, string, string, workflow.BatchJobOutcome, error) (workflow.BatchState, bool, error)
	// DiscardChain removes exactly one rejected chain from recording state.
	DiscardChain(string)
	// DiscardBatch removes exactly one rejected batch from recording state.
	DiscardBatch(string)
}

// newFakeWorkflowStateStore fails fast if the internal recording store loses
// the exact cleanup capability required by concurrent fake dispatches.
func newFakeWorkflowStateStore() fakeWorkflowStateStore {
	store, ok := workflow.NewMemoryStore().(fakeWorkflowStateStore)
	if !ok {
		panic("workflow memory store does not support exact fake cleanup")
	}
	return store
}

// newFakeWorkflowRecorder wires the real workflow engine to the recording
// transport and store owned by one FakeQueue state.
func newFakeWorkflowRecorder(fake *FakeQueue) *fakeWorkflowRecorder {
	recorder := &fakeWorkflowRecorder{
		state:   fake.state,
		store:   newFakeWorkflowStateStore(),
		chains:  make(map[string]ChainRecord),
		batches: make(map[string]BatchRecord),
	}
	engine, err := workflow.NewWithStore(fake, recorder, workflow.WithoutEphemeralCallbacks())
	if err != nil {
		panic(err)
	}
	recorder.engine = engine
	return recorder
}

// resetLocked replaces persisted workflow state while the shared fake mutex
// prevents readers from observing a half-reset projection.
func (r *fakeWorkflowRecorder) resetLocked() {
	r.store = newFakeWorkflowStateStore()
	r.chains = make(map[string]ChainRecord)
	r.batches = make(map[string]BatchRecord)
	r.acceptedChainIDs = nil
	r.acceptedBatchIDs = nil
}

// acceptChain publishes a chain to assertions only after its initial delivery
// was accepted; merely creating or rejecting a builder must remain invisible.
func (r *fakeWorkflowRecorder) acceptChain(chainID string) {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	if _, exists := r.chains[chainID]; !exists {
		return
	}
	r.acceptedChainIDs = append(r.acceptedChainIDs, chainID)
}

// acceptBatch publishes a batch to assertions only after every initial member
// delivery was accepted by the recording transport.
func (r *fakeWorkflowRecorder) acceptBatch(batchID string) {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	if _, exists := r.batches[batchID]; !exists {
		return
	}
	r.acceptedBatchIDs = append(r.acceptedBatchIDs, batchID)
}

// rejectChain removes state created before a failed initial delivery so
// rejected fake workflows cannot accumulate hidden terminal records.
func (r *fakeWorkflowRecorder) rejectChain(chainID string) {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	r.store.DiscardChain(chainID)
	delete(r.chains, chainID)
}

// rejectBatch removes state created before a failed member delivery so
// rejected fake workflows cannot accumulate hidden terminal records.
func (r *fakeWorkflowRecorder) rejectBatch(batchID string) {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	r.store.DiscardBatch(batchID)
	delete(r.batches, batchID)
}

// CreateChain records the exact committed engine model before the first node is
// offered to the fake transport.
func (r *fakeWorkflowRecorder) CreateChain(ctx context.Context, record workflow.ChainRecord) error {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	if err := r.store.CreateChain(ctx, record); err != nil {
		return err
	}
	r.chains[record.ChainID] = chainRecordFromWorkflow(record)
	return nil
}

// AdvanceChain delegates the retry-safe transition while serializing Reset
// against the same in-memory store generation.
func (r *fakeWorkflowRecorder) AdvanceChain(ctx context.Context, chainID string, completedNode string) (*workflow.ChainNode, bool, error) {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	next, done, err := r.store.AdvanceChain(ctx, chainID, completedNode)
	if next == nil {
		return nil, done, err
	}
	cloned := chainNodeToWorkflow(chainNodeFromWorkflow(*next))
	return &cloned, done, err
}

// FailChain delegates terminal failure within the active fake state generation.
func (r *fakeWorkflowRecorder) FailChain(ctx context.Context, chainID string, cause error) error {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	return r.store.FailChain(ctx, chainID, cause)
}

// FailChainNode delegates atomic node failure within the active fake state generation.
func (r *fakeWorkflowRecorder) FailChainNode(ctx context.Context, chainID, nodeID string, cause error) (workflow.ChainState, bool, error) {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	return r.store.FailChainNode(ctx, chainID, nodeID, cause)
}

// GetChain returns an isolated engine state so callers cannot mutate recorded
// payload bytes through a lookup result.
func (r *fakeWorkflowRecorder) GetChain(ctx context.Context, chainID string) (workflow.ChainState, error) {
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	state, err := r.store.GetChain(ctx, chainID)
	return chainStateToWorkflow(chainStateFromWorkflow(state)), err
}

// CreateBatch records the exact committed engine model before member delivery
// begins, then acceptance decides whether assertions may observe it.
func (r *fakeWorkflowRecorder) CreateBatch(ctx context.Context, record workflow.BatchRecord) error {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	if err := r.store.CreateBatch(ctx, record); err != nil {
		return err
	}
	r.batches[record.BatchID] = batchRecordFromWorkflow(record)
	return nil
}

// MarkBatchJobStarted delegates the transition within the active fake state generation.
func (r *fakeWorkflowRecorder) MarkBatchJobStarted(ctx context.Context, batchID, jobID string) error {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	return r.store.MarkBatchJobStarted(ctx, batchID, jobID)
}

// MarkBatchJobSucceeded delegates aggregate success within the active fake state generation.
func (r *fakeWorkflowRecorder) MarkBatchJobSucceeded(ctx context.Context, batchID, jobID string) (workflow.BatchState, bool, error) {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	return r.store.MarkBatchJobSucceeded(ctx, batchID, jobID)
}

// MarkBatchJobFailed delegates aggregate failure within the active fake state generation.
func (r *fakeWorkflowRecorder) MarkBatchJobFailed(ctx context.Context, batchID, jobID string, cause error) (workflow.BatchState, bool, error) {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	return r.store.MarkBatchJobFailed(ctx, batchID, jobID, cause)
}

// SettleBatchJob delegates atomic member settlement within the active fake state generation.
func (r *fakeWorkflowRecorder) SettleBatchJob(ctx context.Context, batchID, jobID string, outcome workflow.BatchJobOutcome, cause error) (workflow.BatchState, bool, error) {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	return r.store.SettleBatchJob(ctx, batchID, jobID, outcome, cause)
}

// CancelBatch delegates aggregate cancellation within the active fake state generation.
func (r *fakeWorkflowRecorder) CancelBatch(ctx context.Context, batchID string) error {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	return r.store.CancelBatch(ctx, batchID)
}

// GetBatch returns aggregate state from the active fake state generation.
func (r *fakeWorkflowRecorder) GetBatch(ctx context.Context, batchID string) (workflow.BatchState, error) {
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	return r.store.GetBatch(ctx, batchID)
}

// MarkCallbackInvoked delegates idempotency claims within the active fake state generation.
func (r *fakeWorkflowRecorder) MarkCallbackInvoked(ctx context.Context, key string) (bool, error) {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	return r.store.MarkCallbackInvoked(ctx, key)
}

// Prune delegates retention without deleting immutable dispatch evidence.
func (r *fakeWorkflowRecorder) Prune(ctx context.Context, before time.Time) error {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	return r.store.Prune(ctx, before)
}

// fakeWorkflowDispatchContextKey distinguishes the canonical fake engine from
// callers using FakeQueue as an explicit raw-runtime transport.
type fakeWorkflowDispatchContextKey struct{}

// withFakeWorkflowDispatch marks deliveries emitted by the fake's own engine so
// physical protocol envelopes do not pollute application dispatch assertions.
func withFakeWorkflowDispatch(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, fakeWorkflowDispatchContextKey{}, true)
}

// fakeWorkflowDeliverySuppressed recognizes only owned protocol types emitted
// by the canonical fake engine; direct raw-runtime calls remain observable.
func fakeWorkflowDeliverySuppressed(ctx context.Context, jobType string) bool {
	if ctx == nil || !workflow.IsDeliveryType(jobType) {
		return false
	}
	marked, _ := ctx.Value(fakeWorkflowDispatchContextKey{}).(bool)
	return marked
}

// guardFakeWorkflowDispatch keeps the engine's create, initial delivery, and
// acceptance cleanup intact while destructive state operations wait.
func (f *FakeQueue) guardFakeWorkflowDispatch() func() {
	f.state.workflowOps.RLock()
	return f.state.workflowOps.RUnlock
}

// Chain creates a fake chain backed by the production workflow builder and
// records it only when Dispatch accepts its initial delivery. Fluent function
// callbacks are accepted for compatibility but are not retained in fake runtime
// state or executed.
// @group Testing
func (f *FakeQueue) Chain(jobs ...Job) ChainBuilder {
	converted, err := toWorkflowJobs(jobs)
	if err != nil {
		return &chainBuilderAdapter{err: err}
	}
	return &chainBuilderAdapter{
		inner:           f.state.workflow.engine.Chain(converted...),
		dispatchGuard:   f.guardFakeWorkflowDispatch,
		dispatchContext: withFakeWorkflowDispatch,
		onAccepted:      f.state.workflow.acceptChain,
		onRejected:      f.state.workflow.rejectChain,
	}
}

// Batch creates a fake batch backed by the production workflow builder and
// records it only when Dispatch accepts all initial member deliveries. Fluent
// function callbacks are accepted for compatibility but are not retained in
// fake runtime state or executed.
// @group Testing
func (f *FakeQueue) Batch(jobs ...Job) BatchBuilder {
	converted, err := toWorkflowJobs(jobs)
	if err != nil {
		return &batchBuilderAdapter{err: err}
	}
	return &batchBuilderAdapter{
		inner:           f.state.workflow.engine.Batch(converted...),
		dispatchGuard:   f.guardFakeWorkflowDispatch,
		dispatchContext: withFakeWorkflowDispatch,
		onAccepted:      f.state.workflow.acceptBatch,
		onRejected:      f.state.workflow.rejectBatch,
	}
}

// ChainRecords returns isolated creation records for accepted fake chains.
// @group Testing
//
// Example: inspect a fake chain
//
//	fake := queue.NewFake()
//	_, _ = fake.Chain(
//		queue.NewJob("reports:build"),
//		queue.NewJob("reports:publish"),
//	).OnQueue("workflow").Dispatch(context.Background())
//	record := fake.ChainRecords()[0]
//	fmt.Println(len(record.Nodes), record.Queue)
//	// Output: 2 workflow
func (f *FakeQueue) ChainRecords() []ChainRecord {
	f.state.mu.RLock()
	defer f.state.mu.RUnlock()
	records := make([]ChainRecord, 0, len(f.state.workflow.acceptedChainIDs))
	for _, chainID := range f.state.workflow.acceptedChainIDs {
		record, exists := f.state.workflow.chains[chainID]
		if exists {
			records = append(records, cloneFakeChainRecord(record))
		}
	}
	return records
}

// BatchRecords returns isolated creation records for accepted fake batches.
// @group Testing
//
// Example: inspect a fake batch
//
//	fake := queue.NewFake()
//	_, _ = fake.Batch(
//		queue.NewJob("emails:first"),
//		queue.NewJob("emails:second"),
//	).Name("nightly").AllowFailures().Dispatch(context.Background())
//	record := fake.BatchRecords()[0]
//	fmt.Println(record.Name, len(record.Jobs), record.AllowFailed)
//	// Output: nightly 2 true
func (f *FakeQueue) BatchRecords() []BatchRecord {
	f.state.mu.RLock()
	defer f.state.mu.RUnlock()
	records := make([]BatchRecord, 0, len(f.state.workflow.acceptedBatchIDs))
	for _, batchID := range f.state.workflow.acceptedBatchIDs {
		record, exists := f.state.workflow.batches[batchID]
		if exists {
			records = append(records, cloneFakeBatchRecord(record))
		}
	}
	return records
}

// FindChain returns workflow state created by the fake's production engine.
// @group Testing
func (f *FakeQueue) FindChain(ctx context.Context, chainID string) (ChainState, error) {
	state, err := f.state.workflow.engine.FindChain(ctx, chainID)
	return chainStateFromWorkflow(state), err
}

// FindBatch returns workflow state created by the fake's production engine.
// @group Testing
func (f *FakeQueue) FindBatch(ctx context.Context, batchID string) (BatchState, error) {
	state, err := f.state.workflow.engine.FindBatch(ctx, batchID)
	return batchStateFromWorkflow(state), err
}

// Prune removes terminal workflow state while retaining fake dispatch records.
// @group Testing
func (f *FakeQueue) Prune(ctx context.Context, before time.Time) error {
	f.state.workflowOps.Lock()
	defer f.state.workflowOps.Unlock()
	return f.state.workflow.engine.Prune(ctx, before)
}

// AssertChained fails unless an accepted chain has the expected ordered job types.
// @group Testing
//
// Example: assert a fake chain
//
//	fake := queue.NewFake()
//	_, _ = fake.Chain(
//		queue.NewJob("reports:build"),
//		queue.NewJob("reports:publish"),
//	).Dispatch(context.Background())
//	fake.AssertChained(t, []string{"reports:build", "reports:publish"})
func (f *FakeQueue) AssertChained(t testing.TB, expected []string) {
	t.Helper()
	for _, record := range f.ChainRecords() {
		if fakeChainTypesEqual(record, expected) {
			return
		}
	}
	t.Fatalf("expected chain %v", expected)
}

// AssertBatchCount fails unless the accepted batch count equals expected.
// @group Testing
//
// Example: assert fake batch count
//
//	fake := queue.NewFake()
//	_, _ = fake.Batch(queue.NewJob("emails:send")).Dispatch(context.Background())
//	fake.AssertBatchCount(t, 1)
func (f *FakeQueue) AssertBatchCount(t testing.TB, expected int) {
	t.Helper()
	if got := len(f.BatchRecords()); got != expected {
		t.Fatalf("expected batch count %d, got %d", expected, got)
	}
}

// AssertNothingBatched fails when any accepted batch was recorded.
// @group Testing
func (f *FakeQueue) AssertNothingBatched(t testing.TB) {
	t.Helper()
	if got := len(f.BatchRecords()); got != 0 {
		t.Fatalf("expected no batches, got %d", got)
	}
}

// AssertBatched fails unless an accepted canonical batch matches predicate.
// The predicate runs outside the recorder lock so it may safely inspect the fake.
// @group Testing
//
// Example: assert fake batch policy
//
//	fake := queue.NewFake()
//	_, _ = fake.Batch(queue.NewJob("emails:send")).Name("nightly").Dispatch(context.Background())
//	fake.AssertBatched(t, func(record queue.BatchRecord) bool { return record.Name == "nightly" })
func (f *FakeQueue) AssertBatched(t testing.TB, predicate func(BatchRecord) bool) {
	t.Helper()
	for _, record := range f.BatchRecords() {
		if predicate(record) {
			return
		}
	}
	t.Fatalf("expected at least one batch to match predicate")
}

// fakeChainTypesEqual compares the assertion projection without discarding the
// richer canonical record exposed to callers that need payload or policy checks.
func fakeChainTypesEqual(record ChainRecord, expected []string) bool {
	if len(record.Nodes) != len(expected) {
		return false
	}
	for i, node := range record.Nodes {
		if node.Job.Type != expected[i] {
			return false
		}
	}
	return true
}

// cloneFakeChainRecord isolates nested node payloads from caller mutation.
func cloneFakeChainRecord(record ChainRecord) ChainRecord {
	return chainRecordFromWorkflow(chainRecordToWorkflow(record))
}

// cloneFakeBatchRecord isolates nested member payloads from caller mutation.
func cloneFakeBatchRecord(record BatchRecord) BatchRecord {
	return batchRecordFromWorkflow(batchRecordToWorkflow(record))
}
