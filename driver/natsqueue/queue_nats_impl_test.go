package natsqueue

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/goforj/queue"
	"github.com/nats-io/nats.go"
)

type natsConnectionStub struct {
	publishErr error
	flushErr   error
	publishN   int
	flushN     int
	closeN     int
}

// Publish returns the configured acceptance result.
func (s *natsConnectionStub) Publish(string, []byte) error {
	s.publishN++
	return s.publishErr
}

// FlushWithContext reports successful readiness for the stub connection.
func (s *natsConnectionStub) FlushWithContext(context.Context) error {
	s.flushN++
	return s.flushErr
}

// Drain reports successful shutdown for the stub connection.
func (s *natsConnectionStub) Drain() error { return nil }

// Close records producer resource cleanup.
func (s *natsConnectionStub) Close() { s.closeN++ }

func TestNATSQueue_EnsureConnShortCircuitsWhenPresent(t *testing.T) {
	q := newNATSQueue("nats://127.0.0.1:1")
	q.nc = &nats.Conn{}
	if err := q.ensureConn(); err != nil {
		t.Fatalf("expected ensureConn to short-circuit when conn already present, got %v", err)
	}
}

func TestNATSQueue_DispatchValidationAndConnectionFailure(t *testing.T) {
	q := newNATSQueue("://bad-url")

	if err := q.Dispatch(context.Background(), queue.NewJob("")); err == nil {
		t.Fatal("expected validation error for empty type")
	}

	if err := q.Dispatch(context.Background(), queue.NewJob("job:nats")); err == nil {
		t.Fatal("expected queue required error")
	}

	// Valid job should proceed to ensureConn and fail for invalid URL.
	err := q.Dispatch(context.Background(), queue.NewJob("job:nats").OnQueue("default"))
	if err == nil {
		t.Fatal("expected connection/parse error")
	}
}

func TestNATSQueue_ShutdownNilConnAndHelpers(t *testing.T) {
	q := newNATSQueue("nats://127.0.0.1:1")
	if err := q.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown with nil conn failed: %v", err)
	}

	job := queue.NewJob("job:nats").Payload(map[string]any{"id": 1}).OnQueue("default")
	if _, _, ok := q.claimUnique(job, "default", time.Minute); !ok {
		t.Fatal("expected first unique claim to succeed")
	}
	if _, _, ok := q.claimUnique(job, "default", time.Minute); ok {
		t.Fatal("expected duplicate unique claim to fail")
	}

	if got := natsSubject("critical"); got != "queue.critical" {
		t.Fatalf("unexpected nats subject: %q", got)
	}
}

// TestNATSQueueShutdownClosesWithoutAsynchronousDrain verifies producer cleanup cannot outlive the public shutdown deadline.
func TestNATSQueueShutdownClosesWithoutAsynchronousDrain(t *testing.T) {
	connection := &natsConnectionStub{}
	q := newNATSQueue("nats://example")
	q.nc = connection
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := q.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if connection.closeN != 1 || q.nc != nil {
		t.Fatalf("producer cleanup = closes:%d retained:%T, want 1/nil", connection.closeN, q.nc)
	}
}

func TestNATSQueue_EnsureConnFailure(t *testing.T) {
	q := newNATSQueue("://bad-url")
	if err := q.ensureConn(); err == nil {
		t.Fatal("expected ensureConn to fail for invalid URL")
	}
	if q.nc != nil {
		t.Fatal("expected no connection on ensureConn failure")
	}
}

func TestNATSQueue_Driver(t *testing.T) {
	q := newNATSQueue("nats://127.0.0.1:1")
	if q.Driver() != queue.DriverNATS {
		t.Fatalf("expected driver %q, got %q", queue.DriverNATS, q.Driver())
	}
}

// TestNATSQueueRejectedPublishReleasesUniqueClaim verifies a failed publish cannot poison the TTL window.
func TestNATSQueueRejectedPublishReleasesUniqueClaim(t *testing.T) {
	publishErr := errors.New("publish rejected")
	connection := &natsConnectionStub{publishErr: publishErr}
	q := newNATSQueue("nats://example")
	q.nc = connection
	job := queue.NewJob("reports:build").Payload([]byte(`{"id":1}`)).OnQueue("default").UniqueFor(time.Minute)
	if err := q.Dispatch(context.Background(), job); !errors.Is(err, publishErr) {
		t.Fatalf("first dispatch error = %v, want publish rejection", err)
	}
	connection.publishErr = nil
	if err := q.Dispatch(context.Background(), job); err != nil {
		t.Fatalf("second dispatch remained poisoned: %v", err)
	}
	if err := q.Dispatch(context.Background(), job); !errors.Is(err, queue.ErrDuplicate) {
		t.Fatalf("accepted publish did not retain claim: %v", err)
	}
}

// TestNATSQueueFlushFailureRetainsUniqueClaim verifies an ambiguous server-roundtrip error fails closed against duplicates.
func TestNATSQueueFlushFailureRetainsUniqueClaim(t *testing.T) {
	flushErr := errors.New("flush response lost")
	connection := &natsConnectionStub{flushErr: flushErr}
	q := newNATSQueue("nats://example")
	q.nc = connection
	job := queue.NewJob("reports:build").OnQueue("default").UniqueFor(time.Minute)
	if err := q.Dispatch(context.Background(), job); !errors.Is(err, flushErr) {
		t.Fatalf("flush failure = %v, want %v", err, flushErr)
	}
	connection.flushErr = nil
	if err := q.Dispatch(context.Background(), job); !errors.Is(err, queue.ErrDuplicate) {
		t.Fatalf("ambiguous publish did not retain uniqueness claim: %v", err)
	}
	if connection.publishN != 1 || connection.flushN != 1 {
		t.Fatalf("publish/flush calls = %d/%d, want 1/1", connection.publishN, connection.flushN)
	}
}

// TestNATSQueueCanceledDispatchStopsBeforeClaim verifies cancellation cannot publish or consume instance uniqueness state.
func TestNATSQueueCanceledDispatchStopsBeforeClaim(t *testing.T) {
	connection := &natsConnectionStub{}
	q := newNATSQueue("nats://example")
	q.nc = connection
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
	if connection.publishN != 0 || connection.flushN != 0 {
		t.Fatalf("canceled dispatch touched NATS: publish=%d flush=%d", connection.publishN, connection.flushN)
	}
}
