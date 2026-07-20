package queue_test

import (
	"context"
	"errors"
	"testing"

	"github.com/goforj/queue"
)

// TestPublicPermanentStopsRetries verifies applications can declare terminal errors without depending on an internal runtime package.
func TestPublicPermanentStopsRetries(t *testing.T) {
	q, err := queue.NewSync()
	if err != nil {
		t.Fatalf("new sync queue: %v", err)
	}
	if err := q.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers: %v", err)
	}
	t.Cleanup(func() { _ = q.Shutdown(context.Background()) })

	cause := errors.New("payload cannot be processed")
	calls := 0
	q.Register("contract:permanent", func(context.Context, queue.Message) error {
		calls++
		return queue.Permanent(cause)
	})

	_, err = q.Dispatch(queue.NewJob("contract:permanent").Retry(5))
	if !queue.IsPermanent(err) || !errors.Is(err, cause) {
		t.Fatalf("dispatch error = %v, want permanent cause", err)
	}
	if calls != 1 {
		t.Fatalf("handler calls = %d, want 1", calls)
	}
	if queue.Permanent(nil) != nil {
		t.Fatal("Permanent(nil) must remain nil")
	}
	if marked := queue.Permanent(cause); queue.Permanent(marked) != marked {
		t.Fatal("Permanent must be idempotent")
	}
}
