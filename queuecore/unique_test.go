package queuecore

import (
	"testing"

	"github.com/goforj/queue"
)

// TestUniqueKeyDelegatesCanonicalIdentity verifies the driver bridge keeps queue scope intact.
func TestUniqueKeyDelegatesCanonicalIdentity(t *testing.T) {
	directKey := UniqueKey(queue.NewJob("reports:build").Payload([]byte(`{"id":1}`)), "critical")
	if directKey == "" || directKey != queue.DriverUniqueKey(queue.NewJob("reports:build").Payload([]byte(`{"id":1}`)), "critical") {
		t.Fatalf("bridge key does not match root key: %q", directKey)
	}
	if directKey == UniqueKey(queue.NewJob("reports:build").Payload([]byte(`{"id":1}`)), "default") {
		t.Fatalf("direct key must be stable and queue scoped: %q", directKey)
	}
}
