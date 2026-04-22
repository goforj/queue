package queue

import "testing"

func TestResolveObservedJobType(t *testing.T) {
	t.Run("plain job type passes through", func(t *testing.T) {
		got := ResolveObservedJobType("monitoring:check", []byte(`{"anything":"ok"}`))
		if got != "monitoring:check" {
			t.Fatalf("expected original job type, got %q", got)
		}
	})

	t.Run("bus wrapper unwraps nested job type", func(t *testing.T) {
		got := ResolveObservedJobType("bus:job", []byte(`{"job":{"type":"monitoring:check"}}`))
		if got != "monitoring:check" {
			t.Fatalf("expected unwrapped job type, got %q", got)
		}
	})

	t.Run("other bus wrappers also unwrap", func(t *testing.T) {
		got := ResolveObservedJobType("bus:batch:job", []byte(`{"job":{"type":"reports:build"}}`))
		if got != "reports:build" {
			t.Fatalf("expected unwrapped batch job type, got %q", got)
		}
	})

	t.Run("invalid payload falls back to raw type", func(t *testing.T) {
		got := ResolveObservedJobType("bus:job", []byte(`{`))
		if got != "bus:job" {
			t.Fatalf("expected fallback raw job type, got %q", got)
		}
	})

	t.Run("missing nested job type falls back to raw type", func(t *testing.T) {
		got := ResolveObservedJobType("bus:job", []byte(`{"job":{}}`))
		if got != "bus:job" {
			t.Fatalf("expected fallback raw job type, got %q", got)
		}
	})
}
