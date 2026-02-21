package bus

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
			{NodeID: "n1", Job: wireJob{Type: "a"}},
			{NodeID: "n2", Job: wireJob{Type: "b"}},
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
			{JobID: "j1", Job: wireJob{Type: "monitor:poll"}},
			{JobID: "j2", Job: wireJob{Type: "monitor:downsample"}},
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
		Nodes:      []ChainNode{{NodeID: "n1", Job: wireJob{Type: "monitor:poll"}}},
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
		Jobs:        []BatchJob{{JobID: "j1", Job: wireJob{Type: "monitor:poll"}}},
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
			{NodeID: "n1", Job: wireJob{Type: "monitor:poll"}},
			{NodeID: "n2", Job: wireJob{Type: "monitor:alert"}},
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
		Nodes:      []ChainNode{{NodeID: "n1", Job: wireJob{Type: "monitor:poll"}}},
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
		Jobs:        []BatchJob{{JobID: "j1", Job: wireJob{Type: "monitor:poll"}}},
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
		Jobs:        []BatchJob{{JobID: "j1", Job: wireJob{Type: "monitor:poll"}}},
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

func TestSQLStoreRebindForPostgres(t *testing.T) {
	s := &sqlStore{driverName: "postgres"}
	got := s.rebind("SELECT * FROM t WHERE a=? AND b=?")
	if got != "SELECT * FROM t WHERE a=$1 AND b=$2" {
		t.Fatalf("unexpected rebind result: %q", got)
	}
}
