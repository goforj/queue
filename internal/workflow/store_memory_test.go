package workflow

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestMemoryStoreDiscardIsExactAndIdempotent pins the recording-only cleanup
// capability without making it part of the durable Store contract.
func TestMemoryStoreDiscardIsExactAndIdempotent(t *testing.T) {
	store := NewMemoryStore()
	discarder, ok := store.(interface {
		DiscardChain(string)
		DiscardBatch(string)
	})
	if !ok {
		t.Fatal("memory store does not expose exact discard capability")
	}
	ctx := context.Background()
	for _, chainID := range []string{"chain-discard", "chain-keep"} {
		if err := store.CreateChain(ctx, ChainRecord{
			ChainID: chainID,
			Nodes:   []ChainNode{{NodeID: chainID + "-node", Job: StoredJob{Type: "chain:job"}}},
		}); err != nil {
			t.Fatalf("create chain %q: %v", chainID, err)
		}
	}
	for _, batchID := range []string{"batch-discard", "batch-keep"} {
		if err := store.CreateBatch(ctx, BatchRecord{
			BatchID: batchID,
			Jobs:    []BatchJob{{JobID: batchID + "-job", Job: StoredJob{Type: "batch:job"}}},
		}); err != nil {
			t.Fatalf("create batch %q: %v", batchID, err)
		}
	}
	for _, chainID := range []string{"chain-discard", "chain-keep"} {
		if err := store.FailChain(ctx, chainID, errors.New("rejected")); err != nil {
			t.Fatalf("fail chain %q: %v", chainID, err)
		}
	}
	for _, batchID := range []string{"batch-discard", "batch-keep"} {
		if err := store.CancelBatch(ctx, batchID); err != nil {
			t.Fatalf("cancel batch %q: %v", batchID, err)
		}
	}

	discarder.DiscardChain("chain-discard")
	discarder.DiscardChain("chain-discard")
	discarder.DiscardBatch("batch-discard")
	discarder.DiscardBatch("batch-discard")
	if _, err := store.GetChain(ctx, "chain-discard"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("discarded chain error = %v, want ErrNotFound", err)
	}
	if _, err := store.GetBatch(ctx, "batch-discard"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("discarded batch error = %v, want ErrNotFound", err)
	}
	if _, err := store.GetChain(ctx, "chain-keep"); err != nil {
		t.Fatalf("unrelated chain was discarded: %v", err)
	}
	if _, err := store.GetBatch(ctx, "batch-keep"); err != nil {
		t.Fatalf("unrelated batch was discarded: %v", err)
	}
}

func TestMemoryStorePruneRemovesTerminalRecordsOnly(t *testing.T) {
	s := NewMemoryStore()
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
		t.Fatalf("advance old done chain: %v", err)
	}

	if err := s.CreateChain(ctx, ChainRecord{
		ChainID:    "chain-old-failed",
		DispatchID: "d2",
		Queue:      "default",
		Nodes:      []ChainNode{{NodeID: "n1", Job: StoredJob{Type: "monitor:downsample"}}},
		CreatedAt:  old,
	}); err != nil {
		t.Fatalf("create chain old failed: %v", err)
	}
	if err := s.FailChain(ctx, "chain-old-failed", errors.New("boom")); err != nil {
		t.Fatalf("fail old chain: %v", err)
	}

	if err := s.CreateBatch(ctx, BatchRecord{
		BatchID:     "batch-old-done",
		DispatchID:  "d3",
		Name:        "old",
		Queue:       "default",
		AllowFailed: true,
		Jobs:        []BatchJob{{JobID: "j1", Job: StoredJob{Type: "monitor:poll"}}},
		CreatedAt:   old,
	}); err != nil {
		t.Fatalf("create batch old done: %v", err)
	}
	if _, _, err := s.MarkBatchJobSucceeded(ctx, "batch-old-done", "j1"); err != nil {
		t.Fatalf("mark batch old done: %v", err)
	}

	if err := s.CreateChain(ctx, ChainRecord{
		ChainID:    "chain-active",
		DispatchID: "d4",
		Queue:      "default",
		Nodes: []ChainNode{
			{NodeID: "n1", Job: StoredJob{Type: "monitor:poll"}},
			{NodeID: "n2", Job: StoredJob{Type: "monitor:alert"}},
		},
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create active chain: %v", err)
	}

	if err := s.Prune(ctx, cutoff); err != nil {
		t.Fatalf("prune: %v", err)
	}

	if _, err := s.GetChain(ctx, "chain-old-done"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected old completed chain pruned, got err=%v", err)
	}
	if _, err := s.GetChain(ctx, "chain-old-failed"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected old failed chain pruned, got err=%v", err)
	}
	if _, err := s.GetBatch(ctx, "batch-old-done"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected old completed batch pruned, got err=%v", err)
	}
	if _, err := s.GetChain(ctx, "chain-active"); err != nil {
		t.Fatalf("expected active chain retained, got err=%v", err)
	}
}
