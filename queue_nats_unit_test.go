package queue

import (
	"testing"

	"github.com/nats-io/nats.go"
)

func TestNATSQueue_EnsureConnShortCircuitsWhenPresent(t *testing.T) {
	q := newNATSQueue("nats://127.0.0.1:1").(*natsQueue)
	q.nc = &nats.Conn{}
	if err := q.ensureConn(); err != nil {
		t.Fatalf("expected ensureConn to short-circuit when conn already present, got %v", err)
	}
}
