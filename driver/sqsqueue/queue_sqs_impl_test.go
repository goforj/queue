package sqsqueue

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/goforj/queue"
)

// TestSQSQueueAmbiguousDispatchRetainsUniqueClaim verifies a lost send response fails closed against duplicate retries.
func TestSQSQueueAmbiguousDispatchRetainsUniqueClaim(t *testing.T) {
	sendErr := errors.New("send response lost")
	client := &sqsWorkerClientStub{sendErr: sendErr}
	q := newSQSQueue(Config{})
	q.client = client
	q.queueURLs["default"] = "https://example.local/queue/default"
	job := queue.NewJob("reports:build").Payload([]byte(`{"id":1}`)).OnQueue("default").UniqueFor(time.Minute)

	if err := q.Dispatch(context.Background(), job); !errors.Is(err, sendErr) {
		t.Fatalf("first dispatch error = %v, want send rejection", err)
	}
	if err := q.Dispatch(context.Background(), job); !errors.Is(err, queue.ErrDuplicate) {
		t.Fatalf("ambiguous dispatch did not retain claim: %v", err)
	}
}

// TestSQSQueueMissingReceiptRetainsUniqueClaim verifies a receipt-less response cannot admit an immediate duplicate retry.
func TestSQSQueueMissingReceiptRetainsUniqueClaim(t *testing.T) {
	client := &sqsWorkerClientStub{sendNil: true}
	q := newSQSQueue(Config{})
	q.client = client
	q.queueURLs["default"] = "https://example.local/queue/default"
	job := queue.NewJob("reports:receipt").OnQueue("default").UniqueFor(time.Minute)
	if err := q.Dispatch(context.Background(), job); err == nil {
		t.Fatal("missing SQS message id was accepted")
	}
	client.sendNil = false
	if err := q.Dispatch(context.Background(), job); !errors.Is(err, queue.ErrDuplicate) {
		t.Fatalf("missing receipt did not retain uniqueness claim: %v", err)
	}
}

// TestSQSQueueCanceledDispatchStopsBeforeClaim verifies cancellation cannot send or consume uniqueness state.
func TestSQSQueueCanceledDispatchStopsBeforeClaim(t *testing.T) {
	client := &sqsWorkerClientStub{}
	q := newSQSQueue(Config{})
	q.client = client
	q.queueURLs["default"] = "https://example.local/queue/default"
	job := queue.NewJob("reports:canceled").OnQueue("default").UniqueFor(time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := q.Dispatch(ctx, job); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled dispatch = %v, want context.Canceled", err)
	}
	key, token, ok := q.claimUnique(job, "default", time.Minute)
	if !ok {
		t.Fatal("canceled dispatch consumed uniqueness state")
	}
	q.unique.Release(key, token)
	if len(client.sendInputs) != 0 {
		t.Fatalf("canceled dispatch sent %d messages", len(client.sendInputs))
	}
}
