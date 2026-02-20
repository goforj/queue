package bus_test

import (
	"testing"
	"time"

	"github.com/goforj/queue/bus"
)

func TestJobFluentOptions(t *testing.T) {
	base := bus.NewJob("monitor:poll", map[string]string{"url": "https://goforj.dev/health"})
	job := base.
		OnQueue("monitor-critical").
		Delay(2 * time.Second).
		Timeout(10 * time.Second).
		Retry(5).
		Backoff(500 * time.Millisecond).
		UniqueFor(30 * time.Second)

	if base.Options.Queue != "" {
		t.Fatalf("expected base job unchanged, got %+v", base.Options)
	}
	if job.Options.Queue != "monitor-critical" {
		t.Fatalf("expected queue monitor-critical, got %q", job.Options.Queue)
	}
	if job.Options.Delay != 2*time.Second {
		t.Fatalf("expected delay 2s, got %v", job.Options.Delay)
	}
	if job.Options.Timeout != 10*time.Second {
		t.Fatalf("expected timeout 10s, got %v", job.Options.Timeout)
	}
	if job.Options.Retry != 5 {
		t.Fatalf("expected retry 5, got %d", job.Options.Retry)
	}
	if job.Options.Backoff != 500*time.Millisecond {
		t.Fatalf("expected backoff 500ms, got %v", job.Options.Backoff)
	}
	if job.Options.UniqueFor != 30*time.Second {
		t.Fatalf("expected unique_for 30s, got %v", job.Options.UniqueFor)
	}
}
