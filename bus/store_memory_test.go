package bus

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMemoryStorePruneRemovesTerminalRecordsOnly(t *testing.T) {
	s := NewMemoryStore()
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
		t.Fatalf("advance old done chain: %v", err)
	}

	if err := s.CreateChain(ctx, ChainRecord{
		ChainID:    "chain-old-failed",
		DispatchID: "d2",
		Queue:      "default",
		Nodes:      []ChainNode{{NodeID: "n1", Job: wireJob{Type: "monitor:downsample"}}},
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
		Jobs:        []BatchJob{{JobID: "j1", Job: wireJob{Type: "monitor:poll"}}},
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
			{NodeID: "n1", Job: wireJob{Type: "monitor:poll"}},
			{NodeID: "n2", Job: wireJob{Type: "monitor:alert"}},
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
