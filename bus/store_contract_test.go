package bus

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func testStoreFactories(t *testing.T) map[string]func(t *testing.T) Store {
	t.Helper()
	return map[string]func(t *testing.T) Store{
		"memory": func(t *testing.T) Store {
			t.Helper()
			return NewMemoryStore()
		},
		"sql_sqlite": func(t *testing.T) Store {
			t.Helper()
			dsn := filepath.Join(t.TempDir(), "store-contract.db")
			store, err := NewSQLStore(SQLStoreConfig{
				DriverName: "sqlite",
				DSN:        dsn,
			})
			if err != nil {
				t.Fatalf("new sql store: %v", err)
			}
			return store
		},
	}
}

func TestStoreContract_NotFound(t *testing.T) {
	for name, factory := range testStoreFactories(t) {
		t.Run(name, func(t *testing.T) {
			s := factory(t)
			ctx := context.Background()

			if _, err := s.GetChain(ctx, "missing"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("expected chain ErrNotFound, got %v", err)
			}
			if _, err := s.GetBatch(ctx, "missing"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("expected batch ErrNotFound, got %v", err)
			}
		})
	}
}

func TestStoreContract_ChainAdvanceIdempotent(t *testing.T) {
	for name, factory := range testStoreFactories(t) {
		t.Run(name, func(t *testing.T) {
			s := factory(t)
			ctx := context.Background()
			chainID := "chain-contract"

			if err := s.CreateChain(ctx, ChainRecord{
				ChainID:    chainID,
				DispatchID: "d1",
				Queue:      "default",
				Nodes: []ChainNode{
					{NodeID: "n1", Job: wireJob{Type: "monitor:poll"}},
					{NodeID: "n2", Job: wireJob{Type: "monitor:downsample"}},
				},
				CreatedAt: time.Now(),
			}); err != nil {
				t.Fatalf("create chain: %v", err)
			}

			next, done, err := s.AdvanceChain(ctx, chainID, "n1")
			if err != nil {
				t.Fatalf("advance first: %v", err)
			}
			if done || next == nil || next.NodeID != "n2" {
				t.Fatalf("expected next n2 on first advance, done=%v next=%+v", done, next)
			}

			next, done, err = s.AdvanceChain(ctx, chainID, "n1")
			if err != nil {
				t.Fatalf("advance duplicate: %v", err)
			}
			if done || next == nil || next.NodeID != "n2" {
				t.Fatalf("expected idempotent duplicate advance, done=%v next=%+v", done, next)
			}

			next, done, err = s.AdvanceChain(ctx, chainID, "n2")
			if err != nil {
				t.Fatalf("advance final: %v", err)
			}
			if !done || next != nil {
				t.Fatalf("expected chain done with nil next, done=%v next=%+v", done, next)
			}
		})
	}
}

func TestStoreContract_BatchTerminalBehavior(t *testing.T) {
	for name, factory := range testStoreFactories(t) {
		t.Run(name, func(t *testing.T) {
			s := factory(t)
			ctx := context.Background()
			batchID := "batch-contract"

			if err := s.CreateBatch(ctx, BatchRecord{
				BatchID:     batchID,
				DispatchID:  "d1",
				Name:        "contract",
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

			st, done, err := s.MarkBatchJobSucceeded(ctx, batchID, "j1")
			if err != nil {
				t.Fatalf("mark success: %v", err)
			}
			if done {
				t.Fatal("expected batch not done after first success")
			}
			if st.Pending != 1 || st.Processed != 1 || st.Failed != 0 {
				t.Fatalf("unexpected mid state: %+v", st)
			}

			st, done, err = s.MarkBatchJobFailed(ctx, batchID, "j2", errors.New("boom"))
			if err != nil {
				t.Fatalf("mark failed: %v", err)
			}
			if !done {
				t.Fatal("expected batch done on failure when allow_failed=false")
			}
			if !st.Completed || !st.Cancelled || st.Failed != 1 {
				t.Fatalf("unexpected terminal state: %+v", st)
			}
		})
	}
}

func TestStoreContract_CallbackMarkerIdempotent(t *testing.T) {
	for name, factory := range testStoreFactories(t) {
		t.Run(name, func(t *testing.T) {
			s := factory(t)
			ctx := context.Background()
			key := "batch_finally:contract"

			first, err := s.MarkCallbackInvoked(ctx, key)
			if err != nil {
				t.Fatalf("first callback marker: %v", err)
			}
			if !first {
				t.Fatal("expected first callback marker=true")
			}

			second, err := s.MarkCallbackInvoked(ctx, key)
			if err != nil {
				t.Fatalf("second callback marker: %v", err)
			}
			if second {
				t.Fatal("expected second callback marker=false")
			}
		})
	}
}

func TestStoreContract_PruneClearsOldCallbackMarkers(t *testing.T) {
	for name, factory := range testStoreFactories(t) {
		t.Run(name, func(t *testing.T) {
			s := factory(t)
			ctx := context.Background()
			key := "batch_finally:contract-prune"

			first, err := s.MarkCallbackInvoked(ctx, key)
			if err != nil {
				t.Fatalf("first callback marker: %v", err)
			}
			if !first {
				t.Fatal("expected first callback marker=true")
			}

			// Future cutoff ensures just-inserted marker is considered old.
			if err := s.Prune(ctx, time.Now().Add(1*time.Minute)); err != nil {
				t.Fatalf("prune markers: %v", err)
			}

			again, err := s.MarkCallbackInvoked(ctx, key)
			if err != nil {
				t.Fatalf("callback marker after prune: %v", err)
			}
			if !again {
				t.Fatal("expected callback marker to be insertable again after prune")
			}
		})
	}
}

func TestStoreContract_BatchAllowFailuresContinues(t *testing.T) {
	for name, factory := range testStoreFactories(t) {
		t.Run(name, func(t *testing.T) {
			s := factory(t)
			ctx := context.Background()
			batchID := "batch-allow-fail-contract"

			if err := s.CreateBatch(ctx, BatchRecord{
				BatchID:     batchID,
				DispatchID:  "d1",
				Name:        "allow-fail",
				Queue:       "default",
				AllowFailed: true,
				Jobs: []BatchJob{
					{JobID: "j1", Job: wireJob{Type: "monitor:poll"}},
					{JobID: "j2", Job: wireJob{Type: "monitor:downsample"}},
					{JobID: "j3", Job: wireJob{Type: "monitor:alert"}},
				},
				CreatedAt: time.Now(),
			}); err != nil {
				t.Fatalf("create batch: %v", err)
			}

			st, done, err := s.MarkBatchJobFailed(ctx, batchID, "j1", errors.New("boom"))
			if err != nil {
				t.Fatalf("mark first failed: %v", err)
			}
			if done {
				t.Fatal("expected batch to continue when allow_failed=true")
			}
			if st.Cancelled {
				t.Fatal("expected batch not cancelled when allow_failed=true")
			}

			st, done, err = s.MarkBatchJobSucceeded(ctx, batchID, "j2")
			if err != nil {
				t.Fatalf("mark second success: %v", err)
			}
			if done {
				t.Fatal("expected batch still not done after second job")
			}

			st, done, err = s.MarkBatchJobSucceeded(ctx, batchID, "j3")
			if err != nil {
				t.Fatalf("mark third success: %v", err)
			}
			if !done || !st.Completed {
				t.Fatalf("expected batch completed, done=%v state=%+v", done, st)
			}
			if st.Failed != 1 || st.Processed != 3 || st.Pending != 0 {
				t.Fatalf("unexpected final counters: %+v", st)
			}
		})
	}
}

func TestStoreContract_BatchDuplicateTerminalUpdateDoesNotDoubleCount(t *testing.T) {
	for name, factory := range testStoreFactories(t) {
		t.Run(name, func(t *testing.T) {
			s := factory(t)
			ctx := context.Background()
			batchID := "batch-dup-contract"

			if err := s.CreateBatch(ctx, BatchRecord{
				BatchID:     batchID,
				DispatchID:  "d1",
				Name:        "dup",
				Queue:       "default",
				AllowFailed: true,
				Jobs: []BatchJob{
					{JobID: "j1", Job: wireJob{Type: "monitor:poll"}},
				},
				CreatedAt: time.Now(),
			}); err != nil {
				t.Fatalf("create batch: %v", err)
			}

			st, done, err := s.MarkBatchJobSucceeded(ctx, batchID, "j1")
			if err != nil {
				t.Fatalf("mark success first: %v", err)
			}
			if !done || !st.Completed {
				t.Fatalf("expected completed after first success, done=%v state=%+v", done, st)
			}

			st, done, err = s.MarkBatchJobSucceeded(ctx, batchID, "j1")
			if err != nil {
				t.Fatalf("mark success duplicate: %v", err)
			}
			if !done || !st.Completed {
				t.Fatalf("expected completed after duplicate success, done=%v state=%+v", done, st)
			}
			if st.Processed != 1 || st.Pending != 0 || st.Failed != 0 {
				t.Fatalf("expected counters unchanged after duplicate terminal update, got %+v", st)
			}
		})
	}
}
