package rabbitmqqueue

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/goforj/queue"
	amqp "github.com/rabbitmq/amqp091-go"
)

func TestRabbitMQQueue_HelperBranches(t *testing.T) {
	qDefault := newRabbitMQQueue("amqp://example", "").(*rabbitMQQueue)
	if qDefault.defaultQueue != "default" {
		t.Fatalf("expected default queue fallback, got %q", qDefault.defaultQueue)
	}
	qNamed := newRabbitMQQueue("amqp://example", "critical").(*rabbitMQQueue)
	if qNamed.defaultQueue != "critical" {
		t.Fatalf("expected explicit default queue, got %q", qNamed.defaultQueue)
	}

	if err := qDefault.enqueueLocked(context.Background(), "default", []byte("{}")); !errors.Is(err, amqp.ErrClosed) {
		t.Fatalf("expected amqp.ErrClosed when channel missing, got %v", err)
	}

	qDefault.closeLocked()
	if qDefault.conn != nil || qDefault.ch != nil {
		t.Fatal("expected closeLocked to nil connection/channel")
	}
	if err := qDefault.Shutdown(context.Background()); err != nil {
		t.Fatalf("expected shutdown nil-safe path, got %v", err)
	}
}

func TestRabbitMQQueue_DispatchValidationAndDuplicate(t *testing.T) {
	q := newRabbitMQQueue("amqp://example", "default").(*rabbitMQQueue)

	if err := q.Dispatch(context.Background(), queue.NewJob("")); err == nil {
		t.Fatal("expected validation error for empty job type")
	}
	if err := q.Dispatch(context.Background(), queue.NewJob("job:noqueue")); err == nil {
		t.Fatal("expected queue required error")
	}

	job := queue.NewJob("job:dup").Payload([]byte(`{"k":"v"}`)).OnQueue("default").UniqueFor(10 * time.Second)
	_ = q.claimUnique(job, "default", 10*time.Second)
	if err := q.Dispatch(context.Background(), job); !errors.Is(err, queue.ErrDuplicate) {
		t.Fatalf("expected ErrDuplicate before dial path, got %v", err)
	}
}

func TestRabbitMQQueue_ClaimUniquePrunesExpired(t *testing.T) {
	q := newRabbitMQQueue("amqp://example", "default").(*rabbitMQQueue)
	job := queue.NewJob("job:unique").Payload([]byte(`{"id":1}`)).OnQueue("default")
	key := "default:" + job.Type + ":" + string(job.PayloadBytes())
	q.unique[key] = time.Now().Add(-time.Second)

	if ok := q.claimUnique(job, "default", 5*time.Second); !ok {
		t.Fatal("expected expired key to be pruned and claim to succeed")
	}
}

func TestRabbitMQQueue_EnsureConnectedLockedAndErrorClassifier(t *testing.T) {
	q := newRabbitMQQueue("://bad-url", "default").(*rabbitMQQueue)
	q.dialTimeout = 5 * time.Millisecond
	if err := q.ensureConnectedLocked(); err == nil {
		t.Fatal("expected ensureConnectedLocked to fail for invalid url")
	}

	if isRabbitConnectionClosed(nil) {
		t.Fatal("expected nil error not closed")
	}
	if !isRabbitConnectionClosed(amqp.ErrClosed) {
		t.Fatal("expected amqp.ErrClosed to be treated as closed")
	}
	if !isRabbitConnectionClosed(errors.New("channel/connection is not open")) {
		t.Fatal("expected closed-message string to be treated as closed")
	}
	if isRabbitConnectionClosed(errors.New("something else")) {
		t.Fatal("expected unrelated error not to be treated as closed")
	}
}

func TestRabbitPhysicalQueueName(t *testing.T) {
	if got := rabbitPhysicalQueueName("default", "critical"); got != "critical" {
		t.Fatalf("expected message queue to win, got %q", got)
	}
	if got := rabbitPhysicalQueueName("default", ""); got != "default" {
		t.Fatalf("expected default queue fallback, got %q", got)
	}
	if got := rabbitPhysicalQueueName("", ""); got != "default" {
		t.Fatalf("expected hard default fallback, got %q", got)
	}
}
