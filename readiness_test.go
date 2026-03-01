package queue

import (
	"context"
	"errors"
	"testing"
)

func TestQueueReady(t *testing.T) {
	q, err := NewSync()
	if err != nil {
		t.Fatalf("new sync queue: %v", err)
	}
	if err := q.Ready(context.Background()); err != nil {
		t.Fatalf("queue ready failed: %v", err)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := q.Ready(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled ready context, got %v", err)
	}
}

func TestQueueReadyNilRuntime(t *testing.T) {
	var q *Queue
	if err := q.Ready(context.Background()); err == nil {
		t.Fatal("expected ready error for nil runtime")
	}
}
