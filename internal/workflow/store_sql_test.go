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
			want:       []string{"chain_id TEXT PRIMARY KEY", "nodes_json BLOB NOT NULL"},
		},
		{
			name:       "mysql",
			driverName: "mysql",
			want:       []string{"chain_id VARBINARY(255) PRIMARY KEY", "nodes_json LONGBLOB NOT NULL", "callback_key VARBINARY(512) PRIMARY KEY"},
			reject:     []string{"chain_id TEXT PRIMARY KEY"},
		},
		{
			name:       "postgres",
			driverName: "pgx",
			want:       []string{"chain_id TEXT PRIMARY KEY", "nodes_json BYTEA NOT NULL"},
			reject:     []string{"nodes_json BLOB NOT NULL"},
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
}

// TestMySQLWorkflowKeyLimitsFromColumns pins capacity intersection for logical
// IDs shared by parent and child tables and rejects incomplete managed schemas.
func TestMySQLWorkflowKeyLimitsFromColumns(t *testing.T) {
	columns := map[string]mysqlColumnCapacity{
		"bus_chains.chain_id":                   {dataType: "varbinary", characters: 512, bytes: 512},
		"bus_chain_completed_nodes.chain_id":    {dataType: "varbinary", characters: 300, bytes: 300},
		"bus_chain_completed_nodes.node_id":     {dataType: "varbinary", characters: 400, bytes: 400},
		"bus_batches.batch_id":                  {dataType: "varbinary", characters: 600, bytes: 600},
		"bus_batch_jobs.batch_id":               {dataType: "varbinary", characters: 350, bytes: 350},
		"bus_batch_jobs.job_id":                 {dataType: "varbinary", characters: 450, bytes: 450},
		"bus_callback_invocations.callback_key": {dataType: "varbinary", characters: 1024, bytes: 1024},
	}
	limits, err := mysqlWorkflowKeyLimitsFromColumns(columns)
	if err != nil {
		t.Fatalf("derive key limits: %v", err)
	}
	if limits.chainID.bytes != 300 || limits.chainNode.bytes != 400 || limits.batchID.bytes != 350 || limits.batchJob.bytes != 450 || limits.callback.bytes != 1024 {
		t.Fatalf("derived key limits = %+v", limits)
	}
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
