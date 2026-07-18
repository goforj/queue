//go:build integration

package all_test

import (
	"context"
	"errors"
	"testing"
	"time"

	. "github.com/goforj/queue"
	"github.com/goforj/queue/integration/testenv"
)

// TestRedisIntegration_CanonicalUniqueAcrossClientsAndRestart verifies workflow uniqueness is shared by independent Redis producers and outlives their clients.
func TestRedisIntegration_CanonicalUniqueAcrossClientsAndRestart(t *testing.T) {
	if !integrationBackendEnabled(testenv.BackendRedis) {
		t.Skip("redis integration backend not selected")
	}

	inspector := newRedisInspector(t)
	queueName := uniqueQueueName("redis-canonical-unique")
	cfg := withDefaultQueue(redisCfg(integrationRedis.addr), queueName)
	first, err := newQueue(cfg)
	if err != nil {
		t.Fatalf("new first redis producer: %v", err)
	}
	second, err := newQueue(cfg)
	if err != nil {
		_ = first.Shutdown(context.Background())
		t.Fatalf("new second redis producer: %v", err)
	}
	t.Cleanup(func() {
		_ = first.Shutdown(context.Background())
		_ = second.Shutdown(context.Background())
	})

	type payload struct {
		AccountID string `json:"account_id"`
	}
	ttl := 2 * time.Second
	jobType := uniqueQueueJobType("queue:redis:canonical-unique")
	newUniqueJob := func() Job {
		return NewJob(jobType).
			Payload(payload{AccountID: "account-123"}).
			OnQueue(queueName).
			UniqueFor(ttl)
	}

	start := make(chan struct{})
	results := make(chan error, 2)
	for _, producer := range []*Queue{first, second} {
		producer := producer
		go func() {
			<-start
			_, dispatchErr := producer.Dispatch(newUniqueJob())
			results <- dispatchErr
		}()
	}
	dispatchStarted := time.Now()
	close(start)

	accepted := 0
	duplicates := 0
	for range 2 {
		dispatchErr := <-results
		switch {
		case dispatchErr == nil:
			accepted++
		case errors.Is(dispatchErr, ErrDuplicate):
			duplicates++
		default:
			t.Fatalf("cross-client dispatch returned unexpected error: %v", dispatchErr)
		}
	}
	if accepted != 1 || duplicates != 1 {
		t.Fatalf("cross-client accepted/duplicate results = %d/%d, want 1/1", accepted, duplicates)
	}

	pending, err := inspector.ListPendingTasks(queueName)
	if err != nil {
		t.Fatalf("list pending tasks after cross-client dispatch: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending tasks after cross-client dispatch = %d, want 1", len(pending))
	}

	if err := first.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown first redis producer: %v", err)
	}
	if err := second.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown second redis producer: %v", err)
	}
	restarted, err := newQueue(cfg)
	if err != nil {
		t.Fatalf("restart redis producer: %v", err)
	}
	t.Cleanup(func() { _ = restarted.Shutdown(context.Background()) })

	if _, err := restarted.Dispatch(newUniqueJob()); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("restarted producer dispatch error = %v, want ErrDuplicate", err)
	}

	deadline := time.Now().Add(ttl + 3*time.Second)
	for {
		_, err = restarted.Dispatch(newUniqueJob())
		if err == nil {
			break
		}
		if !errors.Is(err, ErrDuplicate) {
			t.Fatalf("dispatch while waiting for uniqueness expiry: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("canonical uniqueness claim did not expire within %s", ttl+3*time.Second)
		}
		time.Sleep(25 * time.Millisecond)
	}
	if elapsed := time.Since(dispatchStarted); elapsed < ttl {
		t.Fatalf("canonical uniqueness claim expired after %s, before TTL %s", elapsed, ttl)
	}

	pending, err = inspector.ListPendingTasks(queueName)
	if err != nil {
		t.Fatalf("list pending tasks after uniqueness expiry: %v", err)
	}
	if len(pending) != 2 {
		t.Fatalf("pending tasks after uniqueness expiry = %d, want 2", len(pending))
	}
}
