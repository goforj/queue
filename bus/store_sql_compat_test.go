package bus

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

const legacyV1NodesJSON = `[{"NodeID":"legacy-node-1","Job":{"type":"reports:build","payload":"eyJpZCI6MX0=","options":{"Queue":"critical","Delay":2000000000,"Timeout":15000000000,"Retry":4,"Backoff":500000000,"UniqueFor":30000000000}}},{"NodeID":"legacy-node-2","Job":{"type":"reports:notify","payload":"bnVsbA==","options":{"Queue":"critical","Delay":0,"Timeout":0,"Retry":0,"Backoff":0,"UniqueFor":0}}}]`

// TestSQLStoreV1PersistedDataCompatibility proves the current store can read and safely mutate the frozen v1 SQLite layout.
func TestSQLStoreV1PersistedDataCompatibility(t *testing.T) {
	const (
		legacyCreatedMS = int64(1704067200123)
		legacyUpdatedMS = int64(1704067201123)
		pruneCutoffMS   = int64(1705000000000)
	)

	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "workflow-v1.db"))
	if err != nil {
		t.Fatalf("open compatibility database: %v", err)
	}
	t.Cleanup(func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Errorf("close compatibility database: %v", closeErr)
		}
	})
	loadLegacySQLStoreFixture(t, ctx, db)

	store, err := NewSQLStore(SQLStoreConfig{DB: db, DriverName: "sqlite"})
	if err != nil {
		t.Fatalf("construct store over v1 database: %v", err)
	}

	activeChain, err := store.GetChain(ctx, "compat-chain-mutate")
	if err != nil {
		t.Fatalf("read v1 active chain: %v", err)
	}
	if activeChain.DispatchID != "compat-dispatch-mutate" || activeChain.Queue != "critical" || activeChain.NextIndex != 0 || activeChain.Completed || activeChain.Failed {
		t.Fatalf("active chain state changed: %+v", activeChain)
	}
	if activeChain.CreatedAt.UnixMilli() != legacyCreatedMS || activeChain.UpdatedAt.UnixMilli() != legacyUpdatedMS {
		t.Fatalf("active chain timestamps changed: created=%d updated=%d", activeChain.CreatedAt.UnixMilli(), activeChain.UpdatedAt.UnixMilli())
	}
	if len(activeChain.Nodes) != 2 {
		t.Fatalf("active chain node count=%d, want 2", len(activeChain.Nodes))
	}
	firstJob := activeChain.Nodes[0].Job
	if activeChain.Nodes[0].NodeID != "legacy-node-1" || firstJob.Type != "reports:build" || string(firstJob.Payload) != `{"id":1}` {
		t.Fatalf("first legacy node did not decode: %+v", activeChain.Nodes[0])
	}
	if firstJob.Options.Queue != "critical" || firstJob.Options.Delay != 2*time.Second || firstJob.Options.Timeout != 15*time.Second || firstJob.Options.Retry != 4 || firstJob.Options.Backoff != 500*time.Millisecond || firstJob.Options.UniqueFor != 30*time.Second {
		t.Fatalf("legacy nested job options changed: %+v", firstJob.Options)
	}
	secondJob := activeChain.Nodes[1].Job
	if activeChain.Nodes[1].NodeID != "legacy-node-2" || secondJob.Type != "reports:notify" || string(secondJob.Payload) != "null" {
		t.Fatalf("second legacy node did not decode: %+v", activeChain.Nodes[1])
	}

	completedChain, err := store.GetChain(ctx, "compat-chain-completed-old")
	if err != nil {
		t.Fatalf("read v1 completed chain: %v", err)
	}
	if !completedChain.Completed || completedChain.Failed || completedChain.NextIndex != 2 || completedChain.UpdatedAt.UnixMilli() != 1704067205000 {
		t.Fatalf("completed chain state changed: %+v", completedChain)
	}
	failedChain, err := store.GetChain(ctx, "compat-chain-failed-old")
	if err != nil {
		t.Fatalf("read v1 failed chain: %v", err)
	}
	if failedChain.Completed || !failedChain.Failed || failedChain.Failure != "legacy failure" || failedChain.NextIndex != 1 {
		t.Fatalf("failed chain state changed: %+v", failedChain)
	}

	activeBatch, err := store.GetBatch(ctx, "compat-batch-mutate")
	if err != nil {
		t.Fatalf("read v1 active batch: %v", err)
	}
	if activeBatch.Name != "legacy mutable batch" || activeBatch.Queue != "bulk" || !activeBatch.AllowFailed || activeBatch.Total != 2 || activeBatch.Pending != 2 || activeBatch.Processed != 0 || activeBatch.Failed != 0 || activeBatch.Cancelled || activeBatch.Completed {
		t.Fatalf("active batch state changed: %+v", activeBatch)
	}
	if activeBatch.CreatedAt.UnixMilli() != legacyCreatedMS || activeBatch.UpdatedAt.UnixMilli() != legacyUpdatedMS {
		t.Fatalf("active batch timestamps changed: created=%d updated=%d", activeBatch.CreatedAt.UnixMilli(), activeBatch.UpdatedAt.UnixMilli())
	}
	terminalBatch, err := store.GetBatch(ctx, "compat-batch-terminal-old")
	if err != nil {
		t.Fatalf("read v1 terminal batch: %v", err)
	}
	if !terminalBatch.Completed || terminalBatch.Cancelled || terminalBatch.Total != 2 || terminalBatch.Pending != 0 || terminalBatch.Processed != 2 || terminalBatch.Failed != 1 {
		t.Fatalf("terminal batch state changed: %+v", terminalBatch)
	}

	assertLegacySQLStoreSchema(t, ctx, db)
	assertLegacyNodesJSON(t, ctx, db, "compat-chain-mutate")

	existingCallback, err := store.MarkCallbackInvoked(ctx, "chain_finally:compat-chain-completed-old")
	if err != nil {
		t.Fatalf("claim existing v1 callback marker: %v", err)
	}
	if existingCallback {
		t.Fatal("existing v1 callback marker was claimed twice")
	}
	newCallback, err := store.MarkCallbackInvoked(ctx, "batch_finally:compat-batch-mutate")
	if err != nil {
		t.Fatalf("claim new callback marker: %v", err)
	}
	if !newCallback {
		t.Fatal("new callback marker was not claimed")
	}
	duplicateCallback, err := store.MarkCallbackInvoked(ctx, "batch_finally:compat-batch-mutate")
	if err != nil {
		t.Fatalf("claim duplicate callback marker: %v", err)
	}
	if duplicateCallback {
		t.Fatal("new callback marker was claimed twice")
	}

	next, done, err := store.AdvanceChain(ctx, "compat-chain-mutate", "legacy-node-1")
	if err != nil {
		t.Fatalf("advance v1 chain: %v", err)
	}
	if done || next == nil || next.NodeID != "legacy-node-2" || next.Job.Type != "reports:notify" || string(next.Job.Payload) != "null" {
		t.Fatalf("first v1 chain advance returned done=%v next=%+v", done, next)
	}
	duplicateNext, duplicateDone, err := store.AdvanceChain(ctx, "compat-chain-mutate", "legacy-node-1")
	if err != nil {
		t.Fatalf("repeat v1 chain advance: %v", err)
	}
	if duplicateDone || duplicateNext == nil || duplicateNext.NodeID != "legacy-node-2" {
		t.Fatalf("duplicate v1 chain advance returned done=%v next=%+v", duplicateDone, duplicateNext)
	}
	next, done, err = store.AdvanceChain(ctx, "compat-chain-mutate", "legacy-node-2")
	if err != nil {
		t.Fatalf("complete v1 chain: %v", err)
	}
	if !done || next != nil {
		t.Fatalf("completed v1 chain returned done=%v next=%+v", done, next)
	}
	mutatedChain, err := store.GetChain(ctx, "compat-chain-mutate")
	if err != nil {
		t.Fatalf("read mutated v1 chain: %v", err)
	}
	if !mutatedChain.Completed || mutatedChain.Failed || mutatedChain.NextIndex != 2 || mutatedChain.CreatedAt.UnixMilli() != legacyCreatedMS || mutatedChain.UpdatedAt.UnixMilli() <= legacyUpdatedMS {
		t.Fatalf("mutated v1 chain state is inconsistent: %+v", mutatedChain)
	}
	assertLegacyNodesJSON(t, ctx, db, "compat-chain-mutate")
	assertLegacySQLCount(t, ctx, db, `SELECT COUNT(*) FROM bus_chain_completed_nodes WHERE chain_id=?`, 2, "compat-chain-mutate")

	if err := store.MarkBatchJobStarted(ctx, "compat-batch-mutate", "legacy-batch-job-1"); err != nil {
		t.Fatalf("start v1 batch job: %v", err)
	}
	batchAfterSuccess, batchDone, err := store.MarkBatchJobSucceeded(ctx, "compat-batch-mutate", "legacy-batch-job-1")
	if err != nil {
		t.Fatalf("complete v1 batch job: %v", err)
	}
	if batchDone || batchAfterSuccess.Pending != 1 || batchAfterSuccess.Processed != 1 || batchAfterSuccess.Failed != 0 {
		t.Fatalf("v1 batch state after success is inconsistent: done=%v state=%+v", batchDone, batchAfterSuccess)
	}
	duplicateBatch, duplicateBatchDone, err := store.MarkBatchJobSucceeded(ctx, "compat-batch-mutate", "legacy-batch-job-1")
	if err != nil {
		t.Fatalf("repeat v1 batch completion: %v", err)
	}
	if duplicateBatchDone || duplicateBatch.Pending != 1 || duplicateBatch.Processed != 1 || duplicateBatch.Failed != 0 {
		t.Fatalf("duplicate v1 batch completion changed counters: done=%v state=%+v", duplicateBatchDone, duplicateBatch)
	}
	mutatedBatch, batchDone, err := store.MarkBatchJobFailed(ctx, "compat-batch-mutate", "legacy-batch-job-2", errors.New("legacy compatible failure"))
	if err != nil {
		t.Fatalf("fail v1 batch job: %v", err)
	}
	if !batchDone || !mutatedBatch.Completed || mutatedBatch.Cancelled || mutatedBatch.Pending != 0 || mutatedBatch.Processed != 2 || mutatedBatch.Failed != 1 || mutatedBatch.CreatedAt.UnixMilli() != legacyCreatedMS || mutatedBatch.UpdatedAt.UnixMilli() <= legacyUpdatedMS {
		t.Fatalf("mutated v1 batch state is inconsistent: done=%v state=%+v", batchDone, mutatedBatch)
	}
	assertLegacySQLCount(t, ctx, db, `SELECT COUNT(*) FROM bus_batch_jobs WHERE batch_id=? AND started=1 AND done=1`, 2, "compat-batch-mutate")
	assertLegacySQLCount(t, ctx, db, `SELECT COUNT(*) FROM bus_batch_jobs WHERE batch_id=? AND failed=1`, 1, "compat-batch-mutate")

	if err := store.Prune(ctx, time.UnixMilli(pruneCutoffMS)); err != nil {
		t.Fatalf("prune v1 persisted state: %v", err)
	}
	for _, chainID := range []string{"compat-chain-completed-old", "compat-chain-failed-old"} {
		if _, findErr := store.GetChain(ctx, chainID); !errors.Is(findErr, ErrNotFound) {
			t.Fatalf("old terminal chain %q survived prune: %v", chainID, findErr)
		}
	}
	for _, chainID := range []string{"compat-chain-mutate", "compat-chain-active-old", "compat-chain-completed-recent"} {
		if _, findErr := store.GetChain(ctx, chainID); findErr != nil {
			t.Fatalf("retained chain %q was lost during prune: %v", chainID, findErr)
		}
	}
	if _, findErr := store.GetBatch(ctx, "compat-batch-terminal-old"); !errors.Is(findErr, ErrNotFound) {
		t.Fatalf("old terminal batch survived prune: %v", findErr)
	}
	for _, batchID := range []string{"compat-batch-mutate", "compat-batch-active-old", "compat-batch-terminal-recent"} {
		if _, findErr := store.GetBatch(ctx, batchID); findErr != nil {
			t.Fatalf("retained batch %q was lost during prune: %v", batchID, findErr)
		}
	}

	assertLegacySQLCount(t, ctx, db, `SELECT COUNT(*) FROM bus_chains`, 3)
	assertLegacySQLCount(t, ctx, db, `SELECT COUNT(*) FROM bus_chain_completed_nodes`, 5)
	assertLegacySQLCount(t, ctx, db, `SELECT COUNT(*) FROM bus_batches`, 3)
	assertLegacySQLCount(t, ctx, db, `SELECT COUNT(*) FROM bus_batch_jobs`, 4)
	assertLegacySQLCount(t, ctx, db, `SELECT COUNT(*) FROM bus_callback_invocations`, 2)
	assertLegacySQLCount(t, ctx, db, `SELECT COUNT(*) FROM bus_callback_invocations WHERE callback_key=?`, 0, "chain_finally:compat-chain-completed-old")
	assertLegacySQLCount(t, ctx, db, `SELECT COUNT(*) FROM bus_callback_invocations WHERE callback_key=?`, 1, "chain_finally:compat-chain-completed-recent")
	assertLegacySQLCount(t, ctx, db, `SELECT COUNT(*) FROM bus_callback_invocations WHERE callback_key=?`, 1, "batch_finally:compat-batch-mutate")
	assertLegacyNodesJSON(t, ctx, db, "compat-chain-active-old")
	assertLegacySQLStoreSchema(t, ctx, db)
}

// loadLegacySQLStoreFixture loads literal v1 SQL in one transaction so setup cannot leave a partially seeded compatibility database.
func loadLegacySQLStoreFixture(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	fixturePath := filepath.Join("testdata", "compat", "workflow", "v1", "sqlite.sql")
	fixture, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("read compatibility fixture: %v", err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin compatibility fixture transaction: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, rawStatement := range strings.Split(string(fixture), ";") {
		statement := strings.TrimSpace(rawStatement)
		if statement == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			t.Fatalf("execute compatibility fixture statement: %v", err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit compatibility fixture: %v", err)
	}
}

// assertLegacySQLStoreSchema verifies the compatibility database still exposes exactly the five v1 store tables and columns.
func assertLegacySQLStoreSchema(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	wantTables := []string{
		"bus_batch_jobs",
		"bus_batches",
		"bus_callback_invocations",
		"bus_chain_completed_nodes",
		"bus_chains",
	}
	wantColumns := map[string][]string{
		"bus_batch_jobs":            {"batch_id", "job_id", "started", "done", "failed"},
		"bus_batches":               {"batch_id", "dispatch_id", "name", "queue_name", "allow_failed", "total_jobs", "pending_jobs", "processed_jobs", "failed_jobs", "cancelled", "completed", "created_at_ms", "updated_at_ms"},
		"bus_callback_invocations":  {"callback_key", "created_at_ms"},
		"bus_chain_completed_nodes": {"chain_id", "node_id", "created_at_ms"},
		"bus_chains":                {"chain_id", "dispatch_id", "queue_name", "nodes_json", "next_index", "completed", "failed", "failure", "created_at_ms", "updated_at_ms"},
	}

	rows, err := db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name LIKE 'bus_%' ORDER BY name`)
	if err != nil {
		t.Fatalf("query compatibility tables: %v", err)
	}
	var gotTables []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			_ = rows.Close()
			t.Fatalf("scan compatibility table: %v", err)
		}
		gotTables = append(gotTables, table)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close compatibility table rows: %v", err)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate compatibility tables: %v", err)
	}
	if !slices.Equal(gotTables, wantTables) {
		t.Fatalf("compatibility tables=%v, want %v", gotTables, wantTables)
	}

	for _, table := range wantTables {
		columns, err := legacySQLColumnNames(ctx, db, table)
		if err != nil {
			t.Fatalf("query columns for %s: %v", table, err)
		}
		if !slices.Equal(columns, wantColumns[table]) {
			t.Fatalf("compatibility columns for %s=%v, want %v", table, columns, wantColumns[table])
		}
	}
}

// legacySQLColumnNames returns SQLite column names in declaration order for one frozen fixture table.
func legacySQLColumnNames(ctx context.Context, db *sql.DB, table string) ([]string, error) {
	rows, err := db.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info(%q)", table))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var columns []string
	for rows.Next() {
		var (
			columnID     int
			name         string
			columnType   string
			notNull      int
			defaultValue any
			primaryKey   int
		)
		if err := rows.Scan(&columnID, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, err
		}
		columns = append(columns, name)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return columns, nil
}

// assertLegacyNodesJSON verifies state mutations never rewrite the persisted v1 node envelope.
func assertLegacyNodesJSON(t *testing.T, ctx context.Context, db *sql.DB, chainID string) {
	t.Helper()
	var nodesJSON string
	if err := db.QueryRowContext(ctx, `SELECT nodes_json FROM bus_chains WHERE chain_id=?`, chainID).Scan(&nodesJSON); err != nil {
		t.Fatalf("read nodes_json for %s: %v", chainID, err)
	}
	if nodesJSON != legacyV1NodesJSON {
		t.Fatalf("nodes_json for %s changed:\n got: %s\nwant: %s", chainID, nodesJSON, legacyV1NodesJSON)
	}
}

// assertLegacySQLCount verifies a compatibility row set has the exact expected cardinality.
func assertLegacySQLCount(t *testing.T, ctx context.Context, db *sql.DB, query string, want int, args ...any) {
	t.Helper()
	var got int
	if err := db.QueryRowContext(ctx, query, args...).Scan(&got); err != nil {
		t.Fatalf("query compatibility row count: %v", err)
	}
	if got != want {
		t.Fatalf("compatibility row count=%d, want %d for %q", got, want, query)
	}
}
