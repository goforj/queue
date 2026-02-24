package queue

import (
	"testing"
	"time"
)

func TestDriverJobHelpers_MirrorJobSemantics(t *testing.T) {
	timeout := 2 * time.Second
	backoff := 150 * time.Millisecond
	job := NewJob("emails:send").
		Payload(map[string]any{"id": 1}).
		OnQueue("critical").
		Timeout(timeout).
		Retry(3).
		Backoff(backoff).
		Delay(250 * time.Millisecond).
		UniqueFor(5 * time.Second)

	if err := ValidateDriverJob(job); err != nil {
		t.Fatalf("ValidateDriverJob() error = %v", err)
	}

	opts := DriverOptions(job)
	if opts.QueueName != "critical" {
		t.Fatalf("QueueName = %q, want %q", opts.QueueName, "critical")
	}
	if opts.Timeout == nil || *opts.Timeout != timeout {
		t.Fatalf("Timeout = %v, want %v", opts.Timeout, timeout)
	}
	if opts.MaxRetry == nil || *opts.MaxRetry != 3 {
		t.Fatalf("MaxRetry = %v, want 3", opts.MaxRetry)
	}
	if opts.Backoff == nil || *opts.Backoff != backoff {
		t.Fatalf("Backoff = %v, want %v", opts.Backoff, backoff)
	}
	if opts.Delay != 250*time.Millisecond {
		t.Fatalf("Delay = %v, want %v", opts.Delay, 250*time.Millisecond)
	}
	if opts.UniqueTTL != 5*time.Second {
		t.Fatalf("UniqueTTL = %v, want %v", opts.UniqueTTL, 5*time.Second)
	}

	job2 := DriverWithAttempt(job, 2)
	if got := DriverOptions(job2).Attempt; got != 2 {
		t.Fatalf("DriverWithAttempt attempt = %d, want 2", got)
	}
}

func TestValidateDriverJob_PropagatesBuildError(t *testing.T) {
	job := NewJob("x").Backoff(-1)
	if err := ValidateDriverJob(job); err == nil {
		t.Fatalf("expected error")
	}
}
