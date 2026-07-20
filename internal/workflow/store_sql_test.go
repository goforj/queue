package workflow

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func newSQLiteStore(t *testing.T) Store {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "bus-store.db")
	store, err := NewSQLStore(SQLStoreConfig{
		DriverName: "sqlite",
		DSN:        dsn,
	})
	if err != nil {
		t.Fatalf("new sql store: %v", err)
	}
	t.Cleanup(func() { _ = store.(*sqlStore).db.Close() })
	return store
}

func TestSQLStoreChainAdvanceIdempotent(t *testing.T) {
	s := newSQLiteStore(t)
	ctx := context.Background()
	chainID := "chain-1"
	if err := s.CreateChain(ctx, ChainRecord{
		ChainID:    chainID,
		DispatchID: "d1",
		Queue:      "default",
		Nodes: []ChainNode{
			{NodeID: "n1", Job: StoredJob{Type: "a"}},
			{NodeID: "n2", Job: StoredJob{Type: "b"}},
		},
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create chain: %v", err)
	}

	next, done, err := s.AdvanceChain(ctx, chainID, "n1")
	if err != nil {
		t.Fatalf("advance chain first: %v", err)
	}
	if done {
		t.Fatal("expected not done after first advance")
	}
	if next == nil || next.NodeID != "n2" {
		t.Fatalf("expected next n2, got %+v", next)
	}

	// duplicate completion should not double-advance
	next, done, err = s.AdvanceChain(ctx, chainID, "n1")
	if err != nil {
		t.Fatalf("advance chain duplicate: %v", err)
	}
	if done {
		t.Fatal("expected not done after duplicate completion")
	}
	if next == nil || next.NodeID != "n2" {
		t.Fatalf("expected next n2 on duplicate, got %+v", next)
	}

	next, done, err = s.AdvanceChain(ctx, chainID, "n2")
	if err != nil {
		t.Fatalf("advance chain final: %v", err)
	}
	if !done || next != nil {
		t.Fatalf("expected done with nil next, got done=%v next=%+v", done, next)
	}
}

func TestSQLStoreBatchLifecycle(t *testing.T) {
	s := newSQLiteStore(t)
	ctx := context.Background()
	batchID := "batch-1"

	if err := s.CreateBatch(ctx, BatchRecord{
		BatchID:     batchID,
		DispatchID:  "d1",
		Name:        "monitor sweep",
		Queue:       "default",
		AllowFailed: false,
		Jobs: []BatchJob{
			{JobID: "j1", Job: StoredJob{Type: "monitor:poll"}},
			{JobID: "j2", Job: StoredJob{Type: "monitor:downsample"}},
		},
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create batch: %v", err)
	}

	if err := s.MarkBatchJobStarted(ctx, batchID, "j1"); err != nil {
		t.Fatalf("mark started: %v", err)
	}
	st, done, err := s.MarkBatchJobSucceeded(ctx, batchID, "j1")
	if err != nil {
		t.Fatalf("mark succeeded: %v", err)
	}
	if done {
		t.Fatal("expected not done after first success")
	}
	if st.Processed != 1 || st.Pending != 1 || st.Failed != 0 {
		t.Fatalf("unexpected state after success: %+v", st)
	}

	st, done, err = s.MarkBatchJobFailed(ctx, batchID, "j2", nil)
	if err != nil {
		t.Fatalf("mark failed: %v", err)
	}
	if !done {
		t.Fatal("expected done after failure when allow_failed=false")
	}
	if !st.Completed || !st.Cancelled || st.Failed != 1 {
		t.Fatalf("unexpected terminal state: %+v", st)
	}
}

func TestSQLStoreCallbackMarkerIdempotent(t *testing.T) {
	s := newSQLiteStore(t)
	ctx := context.Background()
	key := "chain_finally:chain-1"

	first, err := s.MarkCallbackInvoked(ctx, key)
	if err != nil {
		t.Fatalf("mark callback first: %v", err)
	}
	if !first {
		t.Fatal("expected first callback marker insert to be true")
	}

	second, err := s.MarkCallbackInvoked(ctx, key)
	if err != nil {
		t.Fatalf("mark callback second: %v", err)
	}
	if second {
		t.Fatal("expected duplicate callback marker insert to be false")
	}
}

// TestSQLStoreSchemaInitializationRetriesAfterTransientFailure proves one
// canceled first use cannot poison an otherwise healthy store instance.
func TestSQLStoreSchemaInitializationRetriesAfterTransientFailure(t *testing.T) {
	s := newSQLiteStore(t)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := s.MarkCallbackInvoked(canceled, "first-attempt"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled schema initialization error = %v, want context canceled", err)
	}

	inserted, err := s.MarkCallbackInvoked(context.Background(), "retry-attempt")
	if err != nil {
		t.Fatalf("retry schema initialization: %v", err)
	}
	if !inserted {
		t.Fatal("retry schema initialization did not persist callback marker")
	}
}

func TestSQLStorePruneRemovesOldTerminalState(t *testing.T) {
	s := newSQLiteStore(t)
	ctx := context.Background()
	old := time.Now().Add(-2 * time.Hour)
	cutoff := time.Now().Add(1 * time.Minute)

	if err := s.CreateChain(ctx, ChainRecord{
		ChainID:    "chain-old-done",
		DispatchID: "d1",
		Queue:      "default",
		Nodes:      []ChainNode{{NodeID: "n1", Job: StoredJob{Type: "monitor:poll"}}},
		CreatedAt:  old,
	}); err != nil {
		t.Fatalf("create chain old done: %v", err)
	}
	if _, _, err := s.AdvanceChain(ctx, "chain-old-done", "n1"); err != nil {
		t.Fatalf("advance old chain: %v", err)
	}

	if err := s.CreateBatch(ctx, BatchRecord{
		BatchID:     "batch-old-done",
		DispatchID:  "d2",
		Name:        "old-batch",
		Queue:       "default",
		AllowFailed: true,
		Jobs:        []BatchJob{{JobID: "j1", Job: StoredJob{Type: "monitor:poll"}}},
		CreatedAt:   old,
	}); err != nil {
		t.Fatalf("create batch old done: %v", err)
	}
	if _, _, err := s.MarkBatchJobSucceeded(ctx, "batch-old-done", "j1"); err != nil {
		t.Fatalf("mark old batch done: %v", err)
	}

	if _, err := s.MarkCallbackInvoked(ctx, "batch_then:batch-old-done"); err != nil {
		t.Fatalf("mark callback marker: %v", err)
	}

	if err := s.CreateChain(ctx, ChainRecord{
		ChainID:    "chain-active",
		DispatchID: "d3",
		Queue:      "default",
		Nodes: []ChainNode{
			{NodeID: "n1", Job: StoredJob{Type: "monitor:poll"}},
			{NodeID: "n2", Job: StoredJob{Type: "monitor:alert"}},
		},
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create chain active: %v", err)
	}

	if err := s.Prune(ctx, cutoff); err != nil {
		t.Fatalf("prune: %v", err)
	}

	if _, err := s.GetChain(ctx, "chain-old-done"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected old chain pruned, got err=%v", err)
	}
	if _, err := s.GetBatch(ctx, "batch-old-done"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected old batch pruned, got err=%v", err)
	}
	if _, err := s.GetChain(ctx, "chain-active"); err != nil {
		t.Fatalf("expected active chain retained, got err=%v", err)
	}
}

// TestSQLStorePruneRemovesTransitionReceiptsBeforeIdentifierReuse proves
// retention cannot let an old physical owner claim a new workflow incarnation.
func TestSQLStorePruneRemovesTransitionReceiptsBeforeIdentifierReuse(t *testing.T) {
	store := newSQLiteStore(t).(*sqlStore)
	ctx := context.Background()
	const (
		chainID = "reused-chain"
		nodeID  = "reused-node"
		batchID = "reused-batch"
		jobID   = "reused-job"
	)
	oldChainClaim := transitionClaim{deliveryID: "old-chain-owner", attempt: 0, dispatchID: "old-chain-dispatch", jobID: "old-chain-job", jobFingerprint: "old-chain-fingerprint"}
	if err := store.CreateChain(ctx, ChainRecord{
		ChainID:    chainID,
		DispatchID: oldChainClaim.dispatchID,
		Nodes:      []ChainNode{{NodeID: nodeID}},
		CreatedAt:  time.Now().Add(-2 * time.Hour),
	}); err != nil {
		t.Fatalf("create old chain: %v", err)
	}
	oldChain, err := store.advanceChainOutcome(ctx, chainID, nodeID, oldChainClaim)
	if err != nil || !oldChain.done || !oldChain.receiptKnown {
		t.Fatalf("complete old chain = %+v err:%v", oldChain, err)
	}

	oldBatchClaim := transitionClaim{deliveryID: "old-batch-owner", attempt: 0, dispatchID: "old-batch-dispatch", jobID: jobID, jobFingerprint: "old-batch-fingerprint"}
	if err := store.CreateBatch(ctx, BatchRecord{
		BatchID:    batchID,
		DispatchID: oldBatchClaim.dispatchID,
		Jobs:       []BatchJob{{JobID: jobID}},
		CreatedAt:  time.Now().Add(-2 * time.Hour),
	}); err != nil {
		t.Fatalf("create old batch: %v", err)
	}
	oldBatch, err := store.settleBatchOutcome(ctx, batchID, jobID, BatchJobSucceeded, nil, oldBatchClaim)
	if err != nil || !oldBatch.state.Completed || !oldBatch.receiptKnown || !oldBatch.receipt.aggregateCompleted {
		t.Fatalf("complete old batch = %+v err:%v", oldBatch, err)
	}
	oldBatchReplay, err := store.settleBatchOutcome(ctx, batchID, jobID, BatchJobSucceeded, nil, transitionClaim{
		deliveryID:     "old-batch-replay",
		attempt:        1,
		dispatchID:     oldBatchClaim.dispatchID,
		jobID:          oldBatchClaim.jobID,
		jobFingerprint: oldBatchClaim.jobFingerprint,
	})
	if err != nil || oldBatchReplay.claimedNow || !oldBatchReplay.receiptKnown || !oldBatchReplay.receipt.aggregateCompleted || oldBatchReplay.receipt.owner != oldBatchClaim {
		t.Fatalf("replay old terminal batch = %+v err:%v", oldBatchReplay, err)
	}

	var receiptCount int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM bus_workflow_transition_receipts`).Scan(&receiptCount); err != nil {
		t.Fatalf("count old receipts: %v", err)
	}
	if receiptCount != 3 {
		t.Fatalf("old receipt count = %d, want chain, batch member, and batch aggregate", receiptCount)
	}
	if err := store.Prune(ctx, time.Now().Add(time.Minute)); err != nil {
		t.Fatalf("prune old workflows: %v", err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM bus_workflow_transition_receipts`).Scan(&receiptCount); err != nil {
		t.Fatalf("count pruned receipts: %v", err)
	}
	if receiptCount != 0 {
		t.Fatalf("pruned receipt count = %d, want 0", receiptCount)
	}

	newChainClaim := transitionClaim{deliveryID: "new-chain-owner", attempt: 1, dispatchID: "new-chain-dispatch", jobID: "new-chain-job", jobFingerprint: "new-chain-fingerprint"}
	if err := store.CreateChain(ctx, ChainRecord{ChainID: chainID, DispatchID: newChainClaim.dispatchID, Nodes: []ChainNode{{NodeID: nodeID}}}); err != nil {
		t.Fatalf("recreate chain: %v", err)
	}
	newChain, err := store.advanceChainOutcome(ctx, chainID, nodeID, newChainClaim)
	if err != nil || !newChain.receiptKnown || newChain.receipt.owner != newChainClaim || newChain.receipt.workflowDispatchID != newChainClaim.dispatchID {
		t.Fatalf("complete recreated chain = %+v err:%v", newChain, err)
	}

	newBatchClaim := transitionClaim{deliveryID: "new-batch-owner", attempt: 1, dispatchID: "new-batch-dispatch", jobID: jobID, jobFingerprint: "new-batch-fingerprint"}
	if err := store.CreateBatch(ctx, BatchRecord{BatchID: batchID, DispatchID: newBatchClaim.dispatchID, Jobs: []BatchJob{{JobID: jobID}}}); err != nil {
		t.Fatalf("recreate batch: %v", err)
	}
	newBatch, err := store.settleBatchOutcome(ctx, batchID, jobID, BatchJobSucceeded, nil, newBatchClaim)
	if err != nil || !newBatch.receiptKnown || newBatch.receipt.owner != newBatchClaim || newBatch.receipt.workflowDispatchID != newBatchClaim.dispatchID || !newBatch.receipt.aggregateCompleted {
		t.Fatalf("complete recreated batch = %+v err:%v", newBatch, err)
	}
}

func TestNewSQLStoreValidationAndDefaults(t *testing.T) {
	if _, err := NewSQLStore(SQLStoreConfig{}); err == nil || !strings.Contains(err.Error(), "driver name is required") {
		t.Fatalf("expected driver validation error, got %v", err)
	}
	if _, err := NewSQLStore(SQLStoreConfig{DriverName: "sqlite"}); err == nil || !strings.Contains(err.Error(), "dsn is required") {
		t.Fatalf("expected dsn validation error, got %v", err)
	}

	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "defaults.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	store, err := NewSQLStore(SQLStoreConfig{DB: db})
	if err != nil {
		t.Fatalf("new sql store with db: %v", err)
	}
	ss, ok := store.(*sqlStore)
	if !ok {
		t.Fatalf("expected *sqlStore, got %T", store)
	}
	if ss.driverName != "sqlite" {
		t.Fatalf("expected default driver sqlite, got %q", ss.driverName)
	}
	if !ss.autoMigrate {
		t.Fatal("expected autoMigrate default true")
	}
}

// TestNewSQLStoreWithManagedSchemaSkipsDDL keeps default migration behavior
// intact while giving externally provisioned deployments an explicit opt-out.
func TestNewSQLStoreWithManagedSchemaSkipsDDL(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "managed-schema.db"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	store, err := NewSQLStoreWithManagedSchema(SQLStoreConfig{DB: db, DriverName: "sqlite"})
	if err != nil {
		t.Fatalf("new managed-schema store: %v", err)
	}
	managed := store.(*sqlStore)
	if managed.autoMigrate {
		t.Fatal("managed-schema constructor enabled migrations")
	}
	if _, err := managed.GetChain(ctx, "missing"); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("unprovisioned managed schema error = %v", err)
	}
	var tableCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name LIKE 'bus_%'`).Scan(&tableCount); err != nil {
		t.Fatalf("count managed tables: %v", err)
	}
	if tableCount != 0 {
		t.Fatalf("managed-schema constructor created %d tables", tableCount)
	}

	for _, statement := range managed.schemaStatements() {
		if _, err := db.ExecContext(ctx, managed.rebind(statement)); err != nil {
			t.Fatalf("provision managed schema: %v", err)
		}
	}
	if err := managed.CreateChain(ctx, ChainRecord{ChainID: "managed-chain", Nodes: []ChainNode{{NodeID: "managed-node"}}}); err != nil {
		t.Fatalf("use provisioned managed schema: %v", err)
	}
}

func TestSQLStoreFailChainAndCancelBatch(t *testing.T) {
	s := newSQLiteStore(t)
	ctx := context.Background()

	if err := s.CreateChain(ctx, ChainRecord{
		ChainID:    "chain-fail",
		DispatchID: "d-fail",
		Queue:      "default",
		Nodes:      []ChainNode{{NodeID: "n1", Job: StoredJob{Type: "monitor:poll"}}},
		CreatedAt:  time.Now(),
	}); err != nil {
		t.Fatalf("create chain: %v", err)
	}
	if err := s.FailChain(ctx, "chain-fail", errors.New("boom")); err != nil {
		t.Fatalf("fail chain: %v", err)
	}
	st, err := s.GetChain(ctx, "chain-fail")
	if err != nil {
		t.Fatalf("get chain: %v", err)
	}
	if !st.Failed || st.Failure != "boom" {
		t.Fatalf("expected failed chain with boom, got %+v", st)
	}

	if err := s.CreateBatch(ctx, BatchRecord{
		BatchID:     "batch-cancel",
		DispatchID:  "d-cancel",
		Name:        "cancel-me",
		Queue:       "default",
		AllowFailed: true,
		Jobs:        []BatchJob{{JobID: "j1", Job: StoredJob{Type: "monitor:poll"}}},
		CreatedAt:   time.Now(),
	}); err != nil {
		t.Fatalf("create batch: %v", err)
	}
	if err := s.CancelBatch(ctx, "batch-cancel"); err != nil {
		t.Fatalf("cancel batch: %v", err)
	}
	bs, err := s.GetBatch(ctx, "batch-cancel")
	if err != nil {
		t.Fatalf("get batch: %v", err)
	}
	if !bs.Cancelled || !bs.Completed {
		t.Fatalf("expected cancelled completed batch, got %+v", bs)
	}
}

func TestSQLStoreBatchTerminalIdempotentAndNotFound(t *testing.T) {
	s := newSQLiteStore(t)
	ctx := context.Background()

	if _, _, err := s.MarkBatchJobSucceeded(ctx, "missing-batch", "missing-job"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for missing batch job, got %v", err)
	}

	if err := s.CreateBatch(ctx, BatchRecord{
		BatchID:     "batch-idem",
		DispatchID:  "d-idem",
		Name:        "idem",
		Queue:       "default",
		AllowFailed: true,
		Jobs:        []BatchJob{{JobID: "j1", Job: StoredJob{Type: "monitor:poll"}}},
		CreatedAt:   time.Now(),
	}); err != nil {
		t.Fatalf("create batch: %v", err)
	}

	st1, done1, err := s.MarkBatchJobSucceeded(ctx, "batch-idem", "j1")
	if err != nil {
		t.Fatalf("first mark succeeded: %v", err)
	}
	if !done1 || st1.Processed != 1 || st1.Pending != 0 {
		t.Fatalf("unexpected first terminal state: done=%v state=%+v", done1, st1)
	}

	st2, done2, err := s.MarkBatchJobSucceeded(ctx, "batch-idem", "j1")
	if err != nil {
		t.Fatalf("second mark succeeded: %v", err)
	}
	if !done2 || st2.Processed != 1 || st2.Pending != 0 {
		t.Fatalf("expected idempotent terminal state, got done=%v state=%+v", done2, st2)
	}
}

// TestSQLStoreChainClaimRollsBackWithParentFailure proves a failed parent
// mutation cannot leave the completed-node claim committed on its own.
func TestSQLStoreChainClaimRollsBackWithParentFailure(t *testing.T) {
	store := newSQLiteStore(t).(*sqlStore)
	ctx := context.Background()
	const chainID = "chain-parent-update-rollback"
	if err := store.CreateChain(ctx, ChainRecord{
		ChainID:    chainID,
		DispatchID: "dispatch-parent-update-rollback",
		Nodes: []ChainNode{
			{NodeID: "node-first", Job: StoredJob{Type: "reports:first"}},
			{NodeID: "node-second", Job: StoredJob{Type: "reports:second"}},
		},
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create chain: %v", err)
	}
	const trigger = `CREATE TRIGGER reject_chain_parent_update
BEFORE UPDATE OF next_index ON bus_chains
BEGIN
    SELECT RAISE(ABORT, 'forced chain parent update failure');
END`
	if _, err := store.db.ExecContext(ctx, trigger); err != nil {
		t.Fatalf("create chain failure trigger: %v", err)
	}
	if _, _, err := store.AdvanceChain(ctx, chainID, "node-first"); err == nil || !strings.Contains(err.Error(), "forced chain parent update failure") {
		t.Fatalf("advance with parent failure error = %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `DROP TRIGGER reject_chain_parent_update`); err != nil {
		t.Fatalf("drop chain failure trigger: %v", err)
	}
	next, done, err := store.AdvanceChain(ctx, chainID, "node-first")
	if err != nil {
		t.Fatalf("retry chain advance: %v", err)
	}
	if done || next == nil || next.NodeID != "node-second" {
		t.Fatalf("retry chain advance = next:%+v done:%t, want second node", next, done)
	}
}

// TestSQLStoreChainCompletionRollsBackWithTerminalFlagFailure proves the node
// claim and index increment roll back when the final completion update fails.
func TestSQLStoreChainCompletionRollsBackWithTerminalFlagFailure(t *testing.T) {
	store := newSQLiteStore(t).(*sqlStore)
	ctx := context.Background()
	const chainID = "chain-completion-update-rollback"
	if err := store.CreateChain(ctx, ChainRecord{
		ChainID: chainID,
		Nodes:   []ChainNode{{NodeID: "node-final", Job: StoredJob{Type: "reports:final"}}},
	}); err != nil {
		t.Fatalf("create chain: %v", err)
	}
	const trigger = `CREATE TRIGGER reject_chain_completion_update
BEFORE UPDATE OF completed ON bus_chains
WHEN NEW.completed=1
BEGIN
    SELECT RAISE(ABORT, 'forced chain completion update failure');
END`
	if _, err := store.db.ExecContext(ctx, trigger); err != nil {
		t.Fatalf("create chain completion trigger: %v", err)
	}
	if _, _, err := store.AdvanceChain(ctx, chainID, "node-final"); err == nil || !strings.Contains(err.Error(), "forced chain completion update failure") {
		t.Fatalf("advance with completion failure error = %v", err)
	}
	state, err := store.GetChain(ctx, chainID)
	if err != nil {
		t.Fatalf("get rolled-back chain: %v", err)
	}
	if state.NextIndex != 0 || state.Completed || state.Failed {
		t.Fatalf("completion failure left partial chain state: %+v", state)
	}
	var claims int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM bus_chain_completed_nodes WHERE chain_id=?`, chainID).Scan(&claims); err != nil {
		t.Fatalf("count rolled-back node claims: %v", err)
	}
	if claims != 0 {
		t.Fatalf("rolled-back node claims = %d, want 0", claims)
	}
	if _, err := store.db.ExecContext(ctx, `DROP TRIGGER reject_chain_completion_update`); err != nil {
		t.Fatalf("drop chain completion trigger: %v", err)
	}
	next, done, err := store.AdvanceChain(ctx, chainID, "node-final")
	if err != nil || !done || next != nil {
		t.Fatalf("retry chain completion = next:%+v done:%t err:%v", next, done, err)
	}
}

// TestSQLStoreChainFailureClaimRetriesAfterParentFailure proves the node remains
// claimable when the atomic failure compare-and-swap is rejected by storage.
func TestSQLStoreChainFailureClaimRetriesAfterParentFailure(t *testing.T) {
	store := newSQLiteStore(t).(*sqlStore)
	ctx := context.Background()
	const chainID = "chain-failure-parent-rollback"
	if err := store.CreateChain(ctx, ChainRecord{ChainID: chainID, Nodes: []ChainNode{{NodeID: "node-first"}, {NodeID: "node-second"}}}); err != nil {
		t.Fatalf("create chain: %v", err)
	}
	const trigger = `CREATE TRIGGER reject_chain_failure_update
BEFORE UPDATE OF failed ON bus_chains
BEGIN
    SELECT RAISE(ABORT, 'forced chain failure update failure');
END`
	if _, err := store.db.ExecContext(ctx, trigger); err != nil {
		t.Fatalf("create chain failure trigger: %v", err)
	}
	if _, _, err := store.FailChainNode(ctx, chainID, "node-first", errors.New("application failed")); err == nil || !strings.Contains(err.Error(), "forced chain failure update failure") {
		t.Fatalf("fail node with parent failure error = %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `DROP TRIGGER reject_chain_failure_update`); err != nil {
		t.Fatalf("drop chain failure trigger: %v", err)
	}
	state, owned, err := store.FailChainNode(ctx, chainID, "node-first", errors.New("application failed"))
	if err != nil || !owned || !state.Failed || state.NextIndex != 0 {
		t.Fatalf("retry chain failure = state:%+v owned:%t err:%v", state, owned, err)
	}
}

// TestSQLStoreChainFailureReceiptRollsBackParent proves a receipt insert fault
// cannot leave terminal parent state without the provenance needed for recovery.
func TestSQLStoreChainFailureReceiptRollsBackParent(t *testing.T) {
	store := newSQLiteStore(t).(*sqlStore)
	ctx := context.Background()
	const (
		chainID    = "chain-failure-receipt-rollback"
		nodeID     = "node-failure-receipt-rollback"
		dispatchID = "dispatch-failure-receipt-rollback"
	)
	if err := store.CreateChain(ctx, ChainRecord{ChainID: chainID, DispatchID: dispatchID, Nodes: []ChainNode{{NodeID: nodeID}}}); err != nil {
		t.Fatalf("create chain: %v", err)
	}
	const trigger = `CREATE TRIGGER reject_chain_failure_receipt
BEFORE INSERT ON bus_workflow_transition_receipts
WHEN NEW.workflow_kind='chain' AND NEW.outcome='failed'
BEGIN
    SELECT RAISE(ABORT, 'forced chain failure receipt insert failure');
END`
	if _, err := store.db.ExecContext(ctx, trigger); err != nil {
		t.Fatalf("create failure receipt trigger: %v", err)
	}
	claim := transitionClaim{deliveryID: "generation-failure-receipt-rollback", attempt: 1, dispatchID: dispatchID, jobID: "job-failure-receipt-rollback", jobFingerprint: "fingerprint-failure-receipt-rollback"}
	if _, err := store.failChainOutcome(ctx, chainID, nodeID, errors.New("application failed"), claim); err == nil || !strings.Contains(err.Error(), "forced chain failure receipt insert failure") {
		t.Fatalf("failure receipt insert error = %v", err)
	}
	state, err := store.GetChain(ctx, chainID)
	if err != nil || state.Failed || state.Completed || state.NextIndex != 0 || state.Failure != "" {
		t.Fatalf("chain after receipt rollback = %+v err:%v", state, err)
	}
	if receipt, known, err := store.chainTransitionReceipt(ctx, chainID, nodeID); err != nil || known {
		t.Fatalf("rolled-back failure receipt = known:%t receipt:%+v err:%v", known, receipt, err)
	}
	if _, err := store.db.ExecContext(ctx, `DROP TRIGGER reject_chain_failure_receipt`); err != nil {
		t.Fatalf("drop failure receipt trigger: %v", err)
	}
	result, err := store.failChainOutcome(ctx, chainID, nodeID, errors.New("application failed"), claim)
	if err != nil || !result.claimedNow || !result.owned || !result.receiptKnown || !result.state.Failed || result.receipt.owner != claim || result.receipt.outcome != BatchJobFailed {
		t.Fatalf("retry failed chain receipt = %+v err:%v", result, err)
	}
}

// TestSQLStoreBatchAggregateIncarnationMismatchFailsClosed proves a stale
// aggregate receipt cannot be silently omitted from terminal ownership.
func TestSQLStoreBatchAggregateIncarnationMismatchFailsClosed(t *testing.T) {
	store := newSQLiteStore(t).(*sqlStore)
	ctx := context.Background()
	const (
		batchID    = "batch-aggregate-incarnation-mismatch"
		jobID      = "job-aggregate-incarnation-mismatch"
		dispatchID = "dispatch-aggregate-incarnation-mismatch"
	)
	if err := store.CreateBatch(ctx, BatchRecord{BatchID: batchID, DispatchID: dispatchID, Jobs: []BatchJob{{JobID: jobID}}}); err != nil {
		t.Fatalf("create batch: %v", err)
	}
	claim := transitionClaim{deliveryID: "generation-aggregate-incarnation", attempt: 0, dispatchID: dispatchID, jobID: jobID, jobFingerprint: "fingerprint-aggregate-incarnation"}
	if result, err := store.settleBatchOutcome(ctx, batchID, jobID, BatchJobSucceeded, nil, claim); err != nil || !result.receiptKnown || !result.receipt.aggregateCompleted {
		t.Fatalf("settle terminal batch = %+v err:%v", result, err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE bus_workflow_transition_receipts SET workflow_dispatch_id='dispatch-corrupt-aggregate' WHERE workflow_kind=? AND workflow_id=? AND member_id=''`, batchTransitionKind, batchID); err != nil {
		t.Fatalf("corrupt aggregate receipt incarnation: %v", err)
	}
	if receipt, known, err := store.batchTransitionReceipt(ctx, batchID, jobID); err == nil || known || !strings.Contains(err.Error(), "aggregate transition receipt") {
		t.Fatalf("mismatched aggregate receipt = known:%t receipt:%+v err:%v", known, receipt, err)
	}
}

// TestSQLStoreBatchAggregateOwnershipMismatchFailsClosed proves the terminal
// row cannot silently detach from the member transaction that created it.
func TestSQLStoreBatchAggregateOwnershipMismatchFailsClosed(t *testing.T) {
	tests := []struct {
		name       string
		diagnostic string
		mutate     string
	}{
		{name: "different owner", diagnostic: "exactly one", mutate: `UPDATE bus_workflow_transition_receipts SET owner_attempt=owner_attempt+1 WHERE workflow_kind='batch' AND workflow_id=? AND member_id=''`},
		{name: "different outcome", diagnostic: "member outcome", mutate: `UPDATE bus_workflow_transition_receipts SET outcome='failed' WHERE workflow_kind='batch' AND workflow_id=? AND member_id=''`},
		{name: "missing completion", diagnostic: "does not own completion", mutate: `UPDATE bus_workflow_transition_receipts SET aggregate_completed=0 WHERE workflow_kind='batch' AND workflow_id=? AND member_id=''`},
		{name: "successful cancellation", diagnostic: "does not own failure", mutate: `UPDATE bus_workflow_transition_receipts SET aggregate_cancelled=1 WHERE workflow_kind='batch' AND workflow_id=? AND member_id=''`},
		{name: "missing member", diagnostic: "exactly one", mutate: `DELETE FROM bus_workflow_transition_receipts WHERE workflow_kind='batch' AND workflow_id=? AND member_id<>''`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newSQLiteStore(t).(*sqlStore)
			ctx := context.Background()
			const (
				batchID    = "batch-aggregate-owner-mismatch"
				dispatchID = "dispatch-aggregate-owner-mismatch"
				jobID      = "job-aggregate-owner-mismatch"
			)
			claim := transitionClaim{deliveryID: "generation-aggregate-owner-mismatch", attempt: 2, dispatchID: dispatchID, jobID: jobID, jobFingerprint: "fingerprint-aggregate-owner-mismatch"}
			if err := store.CreateBatch(ctx, BatchRecord{BatchID: batchID, DispatchID: dispatchID, Jobs: []BatchJob{{JobID: jobID}}}); err != nil {
				t.Fatalf("create batch: %v", err)
			}
			if settled, err := store.settleBatchOutcome(ctx, batchID, jobID, BatchJobSucceeded, nil, claim); err != nil || !settled.receiptKnown || !settled.receipt.aggregateCompleted {
				t.Fatalf("settle batch = %+v err:%v", settled, err)
			}
			if _, err := store.db.ExecContext(ctx, test.mutate, batchID); err != nil {
				t.Fatalf("corrupt aggregate receipt: %v", err)
			}
			if receipt, known, err := store.batchTransitionReceipt(ctx, batchID, jobID); err == nil || known || !strings.Contains(err.Error(), test.diagnostic) {
				t.Fatalf("corrupt aggregate receipt = known:%t receipt:%+v err:%v", known, receipt, err)
			}
		})
	}
}

// TestSQLStoreBatchClaimRollsBackWithParentFailure proves a failed aggregate
// update cannot consume the member claim needed by a later retry.
func TestSQLStoreBatchClaimRollsBackWithParentFailure(t *testing.T) {
	store := newSQLiteStore(t).(*sqlStore)
	ctx := context.Background()
	const batchID = "batch-parent-update-rollback"
	if err := store.CreateBatch(ctx, BatchRecord{
		BatchID:    batchID,
		DispatchID: "dispatch-parent-update-rollback",
		Jobs:       []BatchJob{{JobID: "job-first", Job: StoredJob{Type: "reports:first"}}},
		CreatedAt:  time.Now(),
	}); err != nil {
		t.Fatalf("create batch: %v", err)
	}
	const trigger = `CREATE TRIGGER reject_batch_parent_update
BEFORE UPDATE OF pending_jobs ON bus_batches
BEGIN
    SELECT RAISE(ABORT, 'forced batch parent update failure');
END`
	if _, err := store.db.ExecContext(ctx, trigger); err != nil {
		t.Fatalf("create batch failure trigger: %v", err)
	}
	if _, _, err := store.MarkBatchJobSucceeded(ctx, batchID, "job-first"); err == nil || !strings.Contains(err.Error(), "forced batch parent update failure") {
		t.Fatalf("settle with parent failure error = %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `DROP TRIGGER reject_batch_parent_update`); err != nil {
		t.Fatalf("drop batch failure trigger: %v", err)
	}
	state, done, err := store.MarkBatchJobSucceeded(ctx, batchID, "job-first")
	if err != nil {
		t.Fatalf("retry batch settlement: %v", err)
	}
	if !done || !state.Completed || state.Pending != 0 || state.Processed != 1 {
		t.Fatalf("retry batch state = %+v done:%t, want one terminal settlement", state, done)
	}
}

// TestSQLStoreReceiptFailureRollsBackWorkflowMutation proves transition state
// and provenance remain one atomic write, including terminal aggregate ownership.
func TestSQLStoreReceiptFailureRollsBackWorkflowMutation(t *testing.T) {
	t.Run("chain member", func(t *testing.T) {
		store := newSQLiteStore(t).(*sqlStore)
		ctx := context.Background()
		claim := transitionClaim{deliveryID: "chain-owner", attempt: 0, dispatchID: "chain-dispatch", jobID: "chain-job", jobFingerprint: "chain-fingerprint"}
		if err := store.CreateChain(ctx, ChainRecord{ChainID: "chain-receipt-rollback", DispatchID: claim.dispatchID, Nodes: []ChainNode{{NodeID: "node-first"}, {NodeID: "node-final"}}}); err != nil {
			t.Fatalf("create chain: %v", err)
		}
		const trigger = `CREATE TRIGGER reject_chain_receipt
BEFORE INSERT ON bus_workflow_transition_receipts
BEGIN
    SELECT RAISE(ABORT, 'forced chain receipt failure');
END`
		if _, err := store.db.ExecContext(ctx, trigger); err != nil {
			t.Fatalf("create chain receipt trigger: %v", err)
		}
		if _, err := store.advanceChainOutcome(ctx, "chain-receipt-rollback", "node-first", claim); err == nil || !strings.Contains(err.Error(), "forced chain receipt failure") {
			t.Fatalf("advance with receipt failure error = %v", err)
		}
		state, err := store.GetChain(ctx, "chain-receipt-rollback")
		if err != nil {
			t.Fatalf("get rolled-back chain: %v", err)
		}
		if state.NextIndex != 0 || state.Completed || state.Failed {
			t.Fatalf("receipt failure left partial chain state: %+v", state)
		}
		var completedNodes, receipts int
		if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM bus_chain_completed_nodes WHERE chain_id=?`, state.ChainID).Scan(&completedNodes); err != nil {
			t.Fatalf("count completed-node claims: %v", err)
		}
		if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM bus_workflow_transition_receipts WHERE workflow_kind=? AND workflow_id=?`, chainTransitionKind, state.ChainID).Scan(&receipts); err != nil {
			t.Fatalf("count chain receipts: %v", err)
		}
		if completedNodes != 0 || receipts != 0 {
			t.Fatalf("rolled-back chain rows = completed:%d receipts:%d", completedNodes, receipts)
		}
	})

	t.Run("batch aggregate", func(t *testing.T) {
		store := newSQLiteStore(t).(*sqlStore)
		ctx := context.Background()
		claim := transitionClaim{deliveryID: "batch-owner", attempt: 0, dispatchID: "batch-dispatch", jobID: "batch-job", jobFingerprint: "batch-fingerprint"}
		if err := store.CreateBatch(ctx, BatchRecord{BatchID: "batch-receipt-rollback", DispatchID: claim.dispatchID, Jobs: []BatchJob{{JobID: claim.jobID}}}); err != nil {
			t.Fatalf("create batch: %v", err)
		}
		const trigger = `CREATE TRIGGER reject_batch_aggregate_receipt
BEFORE INSERT ON bus_workflow_transition_receipts
WHEN NEW.workflow_kind='batch' AND NEW.member_id=''
BEGIN
    SELECT RAISE(ABORT, 'forced batch aggregate receipt failure');
END`
		if _, err := store.db.ExecContext(ctx, trigger); err != nil {
			t.Fatalf("create batch receipt trigger: %v", err)
		}
		if _, err := store.settleBatchOutcome(ctx, "batch-receipt-rollback", claim.jobID, BatchJobSucceeded, nil, claim); err == nil || !strings.Contains(err.Error(), "forced batch aggregate receipt failure") {
			t.Fatalf("settle with aggregate receipt failure error = %v", err)
		}
		state, err := store.GetBatch(ctx, "batch-receipt-rollback")
		if err != nil {
			t.Fatalf("get rolled-back batch: %v", err)
		}
		if state.Pending != 1 || state.Processed != 0 || state.Completed || state.Cancelled {
			t.Fatalf("receipt failure left partial batch state: %+v", state)
		}
		var done, receipts int
		if err := store.db.QueryRowContext(ctx, `SELECT done FROM bus_batch_jobs WHERE batch_id=? AND job_id=?`, state.BatchID, claim.jobID).Scan(&done); err != nil {
			t.Fatalf("read rolled-back batch member: %v", err)
		}
		if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM bus_workflow_transition_receipts WHERE workflow_kind=? AND workflow_id=?`, batchTransitionKind, state.BatchID).Scan(&receipts); err != nil {
			t.Fatalf("count batch receipts: %v", err)
		}
		if done != 0 || receipts != 0 {
			t.Fatalf("rolled-back batch rows = done:%d receipts:%d", done, receipts)
		}
	})
}

// TestSQLStoreConflictingReceiptCannotAdoptTransition protects immutable owner
// identity when an inconsistent pre-existing receipt is encountered.
func TestSQLStoreConflictingReceiptCannotAdoptTransition(t *testing.T) {
	store := newSQLiteStore(t).(*sqlStore)
	ctx := context.Background()
	const (
		chainID = "chain-conflicting-receipt"
		nodeID  = "node-conflicting-receipt"
	)
	if err := store.CreateChain(ctx, ChainRecord{ChainID: chainID, DispatchID: "chain-dispatch", Nodes: []ChainNode{{NodeID: nodeID}, {NodeID: "node-final"}}}); err != nil {
		t.Fatalf("create chain: %v", err)
	}
	state, err := store.GetChain(ctx, chainID)
	if err != nil {
		t.Fatalf("get chain: %v", err)
	}
	originalOwner := transitionClaim{deliveryID: "original-owner", attempt: 0, dispatchID: state.DispatchID, jobID: "chain-job", jobFingerprint: "chain-fingerprint"}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin orphan receipt: %v", err)
	}
	persisted, known, err := store.insertTransitionReceipt(ctx, tx, transitionReceipt{
		workflowKind:       chainTransitionKind,
		workflowID:         chainID,
		workflowDispatchID: state.DispatchID,
		workflowCreatedAt:  state.CreatedAt,
		memberID:           nodeID,
		outcome:            BatchJobSucceeded,
		owner:              originalOwner,
		createdAt:          time.Now(),
	})
	if err != nil || !known {
		_ = tx.Rollback()
		t.Fatalf("seed conflicting receipt = %+v known:%t err:%v", persisted, known, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit conflicting receipt: %v", err)
	}
	if persisted.createdAt.Nanosecond()%int(time.Millisecond) != 0 {
		t.Fatalf("persisted receipt timestamp was not canonicalized: %v", persisted.createdAt)
	}

	newOwner := transitionClaim{deliveryID: "new-owner", attempt: 1, dispatchID: state.DispatchID, jobID: originalOwner.jobID, jobFingerprint: originalOwner.jobFingerprint}
	if _, err := store.advanceChainOutcome(ctx, chainID, nodeID, newOwner); err == nil || !strings.Contains(err.Error(), "conflicts with its persisted owner") {
		t.Fatalf("advance over conflicting receipt error = %v", err)
	}
	state, err = store.GetChain(ctx, chainID)
	if err != nil {
		t.Fatalf("get chain after conflict: %v", err)
	}
	if state.NextIndex != 0 || state.Completed {
		t.Fatalf("conflicting receipt advanced chain: %+v", state)
	}
	receipt, known, err := store.chainTransitionReceipt(ctx, chainID, nodeID)
	if err != nil || !known || receipt.owner != originalOwner {
		t.Fatalf("immutable receipt after conflict = %+v known:%t err:%v", receipt, known, err)
	}
}

func TestSQLStoreRebindForPostgres(t *testing.T) {
	s := &sqlStore{driverName: "postgres"}
	got := s.rebind("SELECT * FROM t WHERE a=? AND b=?")
	if got != "SELECT * FROM t WHERE a=$1 AND b=$2" {
		t.Fatalf("unexpected rebind result: %q", got)
	}
}

// TestSQLStoreSchemaStatementsUseDialectTypes pins the key and payload types
// required for each supported database to accept the shared legacy schema.
func TestSQLStoreSchemaStatementsUseDialectTypes(t *testing.T) {
	tests := []struct {
		name       string
		driverName string
		want       []string
		reject     []string
	}{
		{
			name:       "sqlite",
			driverName: "sqlite",
			want: []string{
				"chain_id TEXT PRIMARY KEY",
				"nodes_json BLOB NOT NULL",
				"bus_workflow_transition_receipts",
				"workflow_kind TEXT NOT NULL",
				"owner_delivery_id TEXT NOT NULL",
				"PRIMARY KEY (workflow_kind, workflow_id, member_id)",
			},
		},
		{
			name:       "mysql",
			driverName: "mysql",
			want: []string{
				"chain_id VARBINARY(255) PRIMARY KEY",
				"nodes_json LONGBLOB NOT NULL",
				"callback_key VARBINARY(512) PRIMARY KEY",
				"workflow_kind VARBINARY(16) NOT NULL",
				"workflow_id VARBINARY(255) NOT NULL",
				"member_id VARBINARY(255) NOT NULL",
				"PRIMARY KEY (workflow_kind, workflow_id, member_id)",
			},
			reject: []string{"chain_id TEXT PRIMARY KEY"},
		},
		{
			name:       "postgres",
			driverName: "pgx",
			want: []string{
				"chain_id TEXT PRIMARY KEY",
				"nodes_json BYTEA NOT NULL",
				"workflow_kind TEXT NOT NULL",
				"PRIMARY KEY (workflow_kind, workflow_id, member_id)",
			},
			reject: []string{"nodes_json BLOB NOT NULL"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			schema := strings.Join((&sqlStore{driverName: test.driverName}).schemaStatements(), "\n")
			for _, fragment := range test.want {
				if !strings.Contains(schema, fragment) {
					t.Fatalf("schema missing %q:\n%s", fragment, schema)
				}
			}
			for _, fragment := range test.reject {
				if strings.Contains(schema, fragment) {
					t.Fatalf("schema unexpectedly contains %q:\n%s", fragment, schema)
				}
			}
		})
	}
}

// TestSQLStoreMySQLKeyValidation uses the connected column's character and
// byte capacity rather than imposing the generated schema's default globally.
func TestSQLStoreMySQLKeyValidation(t *testing.T) {
	store := &sqlStore{driverName: "mysql"}
	capacity := mysqlColumnCapacity{characters: 2, bytes: 4}
	if err := store.validateMySQLKey("job id", "éé", capacity); err != nil {
		t.Fatalf("exact-capacity identifier: %v", err)
	}
	if err := store.validateMySQLKey("job id", "aaa", mysqlColumnCapacity{characters: 2, bytes: 100}); err == nil || !strings.Contains(err.Error(), "2 characters") {
		t.Fatalf("character-limit error = %v", err)
	}
	if err := store.validateMySQLKey("job id", "€€", mysqlColumnCapacity{characters: 100, bytes: 4}); err == nil || !strings.Contains(err.Error(), "4 bytes") {
		t.Fatalf("byte-limit error = %v", err)
	}
	if err := (&sqlStore{driverName: "postgres"}).validateMySQLKey("job id", strings.Repeat("a", 100), mysqlColumnCapacity{}); err != nil {
		t.Fatalf("PostgreSQL key inherited MySQL capacity: %v", err)
	}

	store.mysqlKeyLimit = mysqlWorkflowKeyLimits{
		chainID:   mysqlColumnCapacity{characters: 2, bytes: 2},
		chainNode: mysqlColumnCapacity{characters: 3, bytes: 3},
		batchID:   mysqlColumnCapacity{characters: 4, bytes: 4},
		batchJob:  mysqlColumnCapacity{characters: 5, bytes: 5},
	}
	if err := store.validateTransitionReceiptKeys(transitionReceipt{workflowKind: batchTransitionKind, workflowID: "bbbb", memberID: "jjjjj"}); err != nil {
		t.Fatalf("batch receipt inherited chain limits: %v", err)
	}
	if err := store.validateTransitionReceiptKeys(transitionReceipt{workflowKind: chainTransitionKind, workflowID: "ccc", memberID: "nnn"}); err == nil || !strings.Contains(err.Error(), "chain receipt id") {
		t.Fatalf("chain receipt capacity error = %v", err)
	}
	if err := store.validateTransitionReceiptKeys(transitionReceipt{workflowKind: "unknown", workflowID: "id", memberID: "member"}); err == nil || !strings.Contains(err.Error(), "unsupported workflow transition receipt kind") {
		t.Fatalf("unknown receipt-kind error = %v", err)
	}
}

// TestMySQLWorkflowKeyLimitsFromColumns pins capacity intersection for logical
// IDs shared by parent and child tables and rejects incomplete managed schemas.
func TestMySQLWorkflowKeyLimitsFromColumns(t *testing.T) {
	columns := map[string]mysqlColumnCapacity{
		"bus_chains.chain_id":                          {dataType: "varbinary", characters: 512, bytes: 512},
		"bus_chain_completed_nodes.chain_id":           {dataType: "varbinary", characters: 300, bytes: 300},
		"bus_chain_completed_nodes.node_id":            {dataType: "varbinary", characters: 400, bytes: 400},
		"bus_batches.batch_id":                         {dataType: "varbinary", characters: 600, bytes: 600},
		"bus_batch_jobs.batch_id":                      {dataType: "varbinary", characters: 350, bytes: 350},
		"bus_batch_jobs.job_id":                        {dataType: "varbinary", characters: 450, bytes: 450},
		"bus_callback_invocations.callback_key":        {dataType: "varbinary", characters: 1024, bytes: 1024},
		"bus_workflow_transition_receipts.workflow_id": {dataType: "varbinary", characters: 700, bytes: 700},
		"bus_workflow_transition_receipts.member_id":   {dataType: "varbinary", characters: 700, bytes: 700},
	}
	limits, err := mysqlWorkflowKeyLimitsFromColumns(columns)
	if err != nil {
		t.Fatalf("derive key limits: %v", err)
	}
	if limits.chainID.bytes != 300 || limits.chainNode.bytes != 400 || limits.batchID.bytes != 350 || limits.batchJob.bytes != 450 || limits.callback.bytes != 1024 {
		t.Fatalf("derived key limits = %+v", limits)
	}
	columns["bus_workflow_transition_receipts.workflow_id"] = mysqlColumnCapacity{dataType: "varbinary", characters: 325, bytes: 325}
	columns["bus_workflow_transition_receipts.member_id"] = mysqlColumnCapacity{dataType: "varbinary", characters: 425, bytes: 425}
	limits, err = mysqlWorkflowKeyLimitsFromColumns(columns)
	if err != nil {
		t.Fatalf("derive narrowed receipt limits: %v", err)
	}
	if limits.chainID.bytes != 300 || limits.chainNode.bytes != 400 || limits.batchID.bytes != 325 || limits.batchJob.bytes != 425 {
		t.Fatalf("receipt-intersected key limits = %+v", limits)
	}
	columns["bus_workflow_transition_receipts.workflow_id"] = mysqlColumnCapacity{dataType: "varbinary", characters: 700, bytes: 700}
	columns["bus_workflow_transition_receipts.member_id"] = mysqlColumnCapacity{dataType: "varbinary", characters: 700, bytes: 700}
	for _, dataType := range []string{"varchar", "text", "binary"} {
		columns["bus_chain_completed_nodes.node_id"] = mysqlColumnCapacity{dataType: dataType, characters: 400, bytes: 1600}
		if _, err := mysqlWorkflowKeyLimitsFromColumns(columns); err == nil || !strings.Contains(err.Error(), "bus_chain_completed_nodes.node_id must use VARBINARY") {
			t.Fatalf("%s identity-column error = %v", dataType, err)
		}
	}
	columns["bus_chain_completed_nodes.node_id"] = mysqlColumnCapacity{dataType: "varbinary", characters: 400, bytes: 400}
	delete(columns, "bus_batch_jobs.job_id")
	if _, err := mysqlWorkflowKeyLimitsFromColumns(columns); err == nil || !strings.Contains(err.Error(), "bus_batch_jobs.job_id") {
		t.Fatalf("missing-column error = %v", err)
	}
}

// TestMySQLTransitionReceiptWidthsFromColumns pins effective legacy-table
// intersections before the two workflow models expand into one shared receipt.
func TestMySQLTransitionReceiptWidthsFromColumns(t *testing.T) {
	columns := map[string]mysqlColumnCapacity{
		"bus_chains.chain_id":                   {dataType: "varbinary", characters: 512, bytes: 512},
		"bus_chain_completed_nodes.chain_id":    {dataType: "varbinary", characters: 300, bytes: 300},
		"bus_chain_completed_nodes.node_id":     {dataType: "varbinary", characters: 400, bytes: 400},
		"bus_batches.batch_id":                  {dataType: "varbinary", characters: 600, bytes: 600},
		"bus_batch_jobs.batch_id":               {dataType: "varbinary", characters: 350, bytes: 350},
		"bus_batch_jobs.job_id":                 {dataType: "varbinary", characters: 450, bytes: 450},
		"bus_callback_invocations.callback_key": {dataType: "varbinary", characters: 1024, bytes: 1024},
	}
	widths, err := mysqlTransitionReceiptWidthsFromColumns(columns)
	if err != nil {
		t.Fatalf("derive transition receipt widths: %v", err)
	}
	if widths.workflowID != 350 || widths.memberID != 450 {
		t.Fatalf("derived receipt widths = %+v, want workflow:350 member:450", widths)
	}

	columns["bus_chains.chain_id"] = mysqlColumnCapacity{dataType: "varbinary", characters: 700, bytes: 700}
	columns["bus_chain_completed_nodes.chain_id"] = mysqlColumnCapacity{dataType: "varbinary", characters: 650, bytes: 650}
	columns["bus_chain_completed_nodes.node_id"] = mysqlColumnCapacity{dataType: "varbinary", characters: 425, bytes: 425}
	columns["bus_batches.batch_id"] = mysqlColumnCapacity{dataType: "varbinary", characters: 375, bytes: 375}
	columns["bus_batch_jobs.batch_id"] = mysqlColumnCapacity{dataType: "varbinary", characters: 325, bytes: 325}
	columns["bus_batch_jobs.job_id"] = mysqlColumnCapacity{dataType: "varbinary", characters: 725, bytes: 725}
	widths, err = mysqlTransitionReceiptWidthsFromColumns(columns)
	if err != nil {
		t.Fatalf("derive asymmetric transition receipt widths: %v", err)
	}
	if widths.workflowID != 650 || widths.memberID != 725 {
		t.Fatalf("asymmetric receipt widths = %+v, want workflow:650 member:725", widths)
	}
}

// TestMySQLTransitionReceiptWidthsRejectUnsafeBaseSchema proves automatic
// receipt creation fails before depending on missing or conflating key columns.
func TestMySQLTransitionReceiptWidthsRejectUnsafeBaseSchema(t *testing.T) {
	columns := map[string]mysqlColumnCapacity{
		"bus_chains.chain_id":                   {dataType: "varbinary", characters: 512, bytes: 512},
		"bus_chain_completed_nodes.chain_id":    {dataType: "varbinary", characters: 512, bytes: 512},
		"bus_chain_completed_nodes.node_id":     {dataType: "varbinary", characters: 512, bytes: 512},
		"bus_batches.batch_id":                  {dataType: "varbinary", characters: 512, bytes: 512},
		"bus_batch_jobs.batch_id":               {dataType: "varbinary", characters: 512, bytes: 512},
		"bus_batch_jobs.job_id":                 {dataType: "varbinary", characters: 512, bytes: 512},
		"bus_callback_invocations.callback_key": {dataType: "varbinary", characters: 1024, bytes: 1024},
	}
	delete(columns, "bus_batch_jobs.job_id")
	if _, err := mysqlTransitionReceiptWidthsFromColumns(columns); err == nil || !strings.Contains(err.Error(), "bus_batch_jobs.job_id") {
		t.Fatalf("missing base-column error = %v", err)
	}
	columns["bus_batch_jobs.job_id"] = mysqlColumnCapacity{dataType: "varbinary", characters: 512, bytes: 512}
	columns["bus_callback_invocations.callback_key"] = mysqlColumnCapacity{dataType: "varchar", characters: 1024, bytes: 4096}
	if _, err := mysqlTransitionReceiptWidthsFromColumns(columns); err == nil || !strings.Contains(err.Error(), "bus_callback_invocations.callback_key must use VARBINARY") {
		t.Fatalf("unsafe base-column error = %v", err)
	}
}

// TestSQLStoreMySQLTransitionReceiptSchemaUsesDerivedWidths proves generated
// DDL can preserve different workflow and member capacities without an ALTER.
func TestSQLStoreMySQLTransitionReceiptSchemaUsesDerivedWidths(t *testing.T) {
	store := &sqlStore{driverName: "mysql"}
	statement := store.transitionReceiptSchemaStatement("VARBINARY(16)", "VARBINARY(650)", "VARBINARY(725)")
	for _, fragment := range []string{
		"CREATE TABLE IF NOT EXISTS bus_workflow_transition_receipts",
		"workflow_kind VARBINARY(16) NOT NULL",
		"workflow_id VARBINARY(650) NOT NULL",
		"member_id VARBINARY(725) NOT NULL",
		"PRIMARY KEY (workflow_kind, workflow_id, member_id)",
	} {
		if !strings.Contains(statement, fragment) {
			t.Fatalf("derived receipt schema missing %q:\n%s", fragment, statement)
		}
	}
	if strings.Contains(statement, "workflow_id VARBINARY(725)") || strings.Contains(statement, "member_id VARBINARY(650)") {
		t.Fatalf("derived receipt schema conflated asymmetric widths:\n%s", statement)
	}
}
