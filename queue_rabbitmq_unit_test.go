package queue

import (
	"context"
	"errors"
	"testing"
	"time"

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
}

func TestRabbitMQQueue_DispatchValidationAndDuplicate(t *testing.T) {
	q := newRabbitMQQueue("amqp://example", "default").(*rabbitMQQueue)

	if err := q.Dispatch(context.Background(), NewTask("")); err == nil {
		t.Fatal("expected validation error for empty task type")
	}
	if err := q.Dispatch(context.Background(), NewTask("job:noqueue")); err == nil {
		t.Fatal("expected queue required error")
	}

	task := NewTask("job:dup").Payload([]byte(`{"k":"v"}`)).OnQueue("default").UniqueFor(10 * time.Second)
	_ = q.claimUnique(task, "default", 10*time.Second)
	if err := q.Dispatch(context.Background(), task); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("expected ErrDuplicate before dial path, got %v", err)
	}
}

func TestRabbitMQQueue_ClaimUniquePrunesExpired(t *testing.T) {
	q := newRabbitMQQueue("amqp://example", "default").(*rabbitMQQueue)
	task := NewTask("job:unique").Payload([]byte(`{"id":1}`)).OnQueue("default")
	key := "default:" + task.Type + ":" + string(task.PayloadBytes())
	q.unique[key] = time.Now().Add(-time.Second)

	if ok := q.claimUnique(task, "default", 5*time.Second); !ok {
		t.Fatal("expected expired key to be pruned and claim to succeed")
	}
}
