package queue

import (
	"context"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

func TestNATSQueue_EnsureConnShortCircuitsWhenPresent(t *testing.T) {
	q := newNATSQueue("nats://127.0.0.1:1").(*natsQueue)
	q.nc = &nats.Conn{}
	if err := q.ensureConn(); err != nil {
		t.Fatalf("expected ensureConn to short-circuit when conn already present, got %v", err)
	}
}

func TestNATSQueue_DispatchValidationAndConnectionFailure(t *testing.T) {
	q := newNATSQueue("://bad-url").(*natsQueue)

	if err := q.Dispatch(context.Background(), NewJob("")); err == nil {
		t.Fatal("expected validation error for empty type")
	}

	if err := q.Dispatch(context.Background(), NewJob("job:nats")); err == nil {
		t.Fatal("expected queue required error")
	}

	// Valid task should proceed to ensureConn and fail for invalid URL.
	err := q.Dispatch(context.Background(), NewJob("job:nats").OnQueue("default"))
	if err == nil {
		t.Fatal("expected connection/parse error")
	}
}

func TestNATSQueue_ShutdownNilConnAndHelpers(t *testing.T) {
	q := newNATSQueue("nats://127.0.0.1:1").(*natsQueue)
	if err := q.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown with nil conn failed: %v", err)
	}

	task := NewJob("job:nats").Payload(map[string]any{"id": 1}).OnQueue("default")
	if !q.claimUnique(task, "default", time.Minute) {
		t.Fatal("expected first unique claim to succeed")
	}
	if q.claimUnique(task, "default", time.Minute) {
		t.Fatal("expected duplicate unique claim to fail")
	}

	if got := natsSubject("critical"); got != "queue.critical" {
		t.Fatalf("unexpected nats subject: %q", got)
	}
}

func TestNATSQueue_EnsureConnFailure(t *testing.T) {
	q := newNATSQueue("://bad-url").(*natsQueue)
	if err := q.ensureConn(); err == nil {
		t.Fatal("expected ensureConn to fail for invalid URL")
	}
	if q.nc != nil {
		t.Fatal("expected no connection on ensureConn failure")
	}
}

func TestNATSQueue_Driver(t *testing.T) {
	q := newNATSQueue("nats://127.0.0.1:1").(*natsQueue)
	if q.Driver() != DriverNATS {
		t.Fatalf("expected driver %q, got %q", DriverNATS, q.Driver())
	}
}
