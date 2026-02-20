package bus

import (
	"context"
	"errors"
	"path/filepath"
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
