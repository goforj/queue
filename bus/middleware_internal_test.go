package bus

import (
	"context"
	"errors"
	"testing"

	"github.com/goforj/queue/busruntime"
)

// TestFailOnErrorPreservesCause verifies the shared marker retains standard error-chain behavior.
func TestFailOnErrorPreservesCause(t *testing.T) {
	base := errors.New("boom")
	err := (FailOnError{}).Handle(context.Background(), Context{}, func(context.Context, Context) error {
		return base
	})
	if !busruntime.IsPermanent(err) || !errors.Is(err, base) {
		t.Fatalf("expected permanent error preserving cause, got %v", err)
	}
}
