//go:build integration

package all_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	. "github.com/goforj/queue"
	"github.com/goforj/queue/integration/testenv"
)

// TestSQLiteIntegrationCanonicalUniqueAcrossPublicClients verifies SQL claiming and logical envelope identity in one public composition.
func TestSQLiteIntegrationCanonicalUniqueAcrossPublicClients(t *testing.T) {
	if !integrationBackendEnabled(testenv.BackendSQLite) {
		t.Skip("sqlite integration backend not selected")
	}

	queueName := uniqueQueueName("sqlite-canonical-unique")
	cfg := withDefaultQueue(sqliteCfg(fmt.Sprintf("%s/queue-public-unique.db", t.TempDir())), queueName)
	first := newStartedUniqueSQLiteQueue(t, cfg, queueName)
	second := newStartedUniqueSQLiteQueue(t, cfg, queueName)

	type payload struct {
		AccountID string `json:"account_id"`
	}
	jobType := uniqueQueueJobType("queue:sqlite:canonical-unique")
	first.Register(jobType, func(context.Context, Message) error { return nil })
	second.Register(jobType, func(context.Context, Message) error { return nil })
	newUniqueJob := func() Job {
		return NewJob(jobType).
			Payload(payload{AccountID: "account-123"}).
			OnQueue(queueName).
			UniqueFor(time.Minute)
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

	shutdownQueue(t, first)
	shutdownQueue(t, second)
	restarted := newStartedUniqueSQLiteQueue(t, cfg, queueName)
	restarted.Register(jobType, func(context.Context, Message) error { return nil })
	if _, err := restarted.Dispatch(newUniqueJob()); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("restarted public producer dispatch error = %v, want ErrDuplicate", err)
	}
}

// newStartedUniqueSQLiteQueue constructs an independent public producer/worker over one shared SQLite file.
func newStartedUniqueSQLiteQueue(t *testing.T, cfg any, queueName string) *Queue {
	t.Helper()
	q, err := newQueue(cfg, WithWorkers(1))
	if err != nil {
		t.Fatalf("new sqlite public queue: %v", err)
	}
	if err := q.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start sqlite public queue %q: %v", queueName, err)
	}
	t.Cleanup(func() { shutdownQueue(t, q) })
	return q
}

// shutdownQueue drains one public queue with a bounded test deadline.
func shutdownQueue(t *testing.T, q *Queue) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := q.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown queue: %v", err)
	}
}
