package bus

import (
	"context"
	"testing"

	"github.com/goforj/queue"
)

func TestRuntimeCleansChainCallbacksAfterFinally(t *testing.T) {
	q, err := queue.NewSync()
	if err != nil {
		t.Fatalf("new sync queue: %v", err)
	}
	bi, err := New(q)
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}
	r := bi.(*runtime)
	if err := r.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers: %v", err)
	}

	r.Register("monitor:poll", func(context.Context, Context) error { return nil })
	if _, err := r.Chain(NewJob("monitor:poll", nil)).
		Finally(func(context.Context, ChainState) error { return nil }).
		Dispatch(context.Background()); err != nil {
		t.Fatalf("dispatch chain: %v", err)
	}

	r.mu.RLock()
	n := len(r.chainCallbacks)
	r.mu.RUnlock()
	if n != 0 {
		t.Fatalf("expected chain callbacks map cleaned, got len=%d", n)
	}
}

func TestRuntimeCleansBatchCallbacksAfterFinally(t *testing.T) {
	q, err := queue.NewSync()
	if err != nil {
		t.Fatalf("new sync queue: %v", err)
	}
	bi, err := New(q)
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}
	r := bi.(*runtime)
	if err := r.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers: %v", err)
	}

	r.Register("monitor:poll", func(context.Context, Context) error { return nil })
	if _, err := r.Batch(NewJob("monitor:poll", nil)).
		Finally(func(context.Context, BatchState) error { return nil }).
		Dispatch(context.Background()); err != nil {
		t.Fatalf("dispatch batch: %v", err)
	}

	r.mu.RLock()
	n := len(r.batchCallbacks)
	r.mu.RUnlock()
	if n != 0 {
		t.Fatalf("expected batch callbacks map cleaned, got len=%d", n)
	}
}
