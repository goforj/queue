//go:build integration

package root_test

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goforj/queue"
	"github.com/goforj/queue/integration/testenv"
	"github.com/goforj/queue/internal/testbridge"
)

// waitNamedOutcomes waits for all expected durable obligations under the integration deadline.
func waitNamedOutcomes(t *testing.T, q *queue.Queue, id string, count int) []queue.ContinuationOutcome {
	t.Helper()
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		outcomes, err := q.FindContinuations(t.Context(), id)
		done := err == nil && len(outcomes) == count
		for _, outcome := range outcomes {
			done = done && (outcome.Status == "succeeded" || outcome.Status == "exhausted")
		}
		if done {
			return outcomes
		}
		select {
		case <-deadline.C:
			t.Fatalf("named outcomes did not settle: %+v %v", outcomes, err)
		case <-tick.C:
		}
	}
}

// exerciseNamedSQLContinuations proves recovery without an original delivery and concurrent trigger atomicity.
func exerciseNamedSQLContinuations(t *testing.T, driver, dsn string) {
	t.Helper()
	db, err := sql.Open(driver, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	makeStore := func() queue.WorkflowStore {
		store, err := queue.NewSQLStore(queue.SQLStoreConfig{DB: db, DriverName: driver})
		if err != nil {
			t.Fatal(err)
		}
		return store
	}
	scope := fmt.Sprintf("continuation-%s-%d", driver, time.Now().UnixNano())
	producer, err := queue.NewSync(queue.WithStore(makeStore()), queue.WithResourceID(scope))
	if err != nil {
		t.Fatal(err)
	}
	producer.Register("business", func(context.Context, queue.Message) error { return nil })
	// Start only the physical runtime to exercise the crash window before the recovery participant starts.
	raw, err := testbridge.RuntimeFromQueue(producer)
	if err != nil {
		t.Fatal(err)
	}
	if err := raw.(interface{ StartWorkers(context.Context) error }).StartWorkers(t.Context()); err != nil {
		t.Fatal(err)
	}
	id, err := queue.Continue(producer.Chain(queue.NewJob("business"))).OnSuccess(queue.NewJob("hook").Payload([]byte("retained bytes")).Retry(1)).OnCompletion(queue.NewJob("hook").Payload([]byte("retained bytes"))).Dispatch(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := producer.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	// Only committed SQL state survives into this consumer; its in-memory queue has no original delivery.
	consumer, err := queue.NewWorkerpool(queue.WithStore(makeStore()), queue.WithResourceID(scope), queue.WithWorkers(16))
	if err != nil {
		t.Fatal(err)
	}
	defer consumer.Shutdown(context.Background())
	var attempts atomic.Int32
	consumer.Register("hook", func(_ context.Context, m queue.Message) error {
		invocation, ok := m.Continuation()
		if !ok || string(m.PayloadBytes()) != "retained bytes" {
			return queue.Permanent(fmt.Errorf("invalid continuation delivery"))
		}
		attempts.Add(1)
		if invocation.Phase == "success" && m.Attempt == 0 {
			return fmt.Errorf("transient")
		}
		return nil
	})
	if err := consumer.StartWorkers(t.Context()); err != nil {
		t.Fatal(err)
	}
	outcomes := waitNamedOutcomes(t, consumer, id, 2)
	if attempts.Load() != 3 || outcomes[0].Status != "succeeded" || outcomes[1].Status != "succeeded" {
		t.Fatalf("recovered outcomes %+v attempts=%d", outcomes, attempts.Load())
	}
	db.SetMaxOpenConns(16)
	// Sixteen members enter before any settles, exposing competing aggregate-terminal observations.
	entered := make(chan struct{})
	var started atomic.Int32
	consumer.Register("concurrent", func(ctx context.Context, m queue.Message) error {
		if started.Add(1) == 16 {
			close(entered)
		}
		select {
		case <-entered:
		case <-ctx.Done():
			return ctx.Err()
		}
		return queue.Permanent(fmt.Errorf("business failure"))
	})
	jobs := make([]queue.Job, 16)
	for i := range jobs {
		jobs[i] = queue.NewJob("concurrent")
	}
	batchID, err := queue.Continue(consumer.Batch(jobs...)).OnProgress(queue.NewJob("hook").Payload([]byte("retained bytes"))).OnFailure(queue.NewJob("hook").Payload([]byte("retained bytes"))).OnCompletion(queue.NewJob("hook").Payload([]byte("retained bytes"))).Dispatch(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	batchOutcomes := waitNamedOutcomes(t, consumer, batchID, 18)
	completion, failure := 0, 0
	for _, outcome := range batchOutcomes {
		switch outcome.Invocation.Phase {
		case "completion":
			completion++
		case "failure":
			failure++
		}
	}
	if completion != 1 || failure != 1 {
		t.Fatalf("duplicate aggregate hooks completion=%d failure=%d", completion, failure)
	}

	consumer.Register("timeout-business", func(context.Context, queue.Message) error { return nil })
	consumer.Register("timeout-hook", func(ctx context.Context, _ queue.Message) error { <-ctx.Done(); return ctx.Err() })
	timeoutID, err := queue.Continue(consumer.Chain(queue.NewJob("timeout-business"))).OnCompletion(queue.NewJob("timeout-hook").Timeout(20 * time.Millisecond)).Dispatch(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	timed := waitNamedOutcomes(t, consumer, timeoutID, 1)
	if timed[0].Status != "exhausted" || timed[0].FailureCategory != "execution_failed" {
		t.Fatalf("spent application context prevented settlement: %+v", timed)
	}
	if err := consumer.Prune(t.Context(), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := consumer.FindBatch(t.Context(), batchID); err != nil {
		t.Fatal("pruned named recovery authority", err)
	}
}

// TestNamedContinuationsSQLite covers one-connection recovery and concurrent terminal settlement.
func TestNamedContinuationsSQLite(t *testing.T) {
	if !integrationBackendEnabled(testenv.BackendSQLite) {
		t.Skip("sqlite backend not selected")
	}
	exerciseNamedSQLContinuations(t, "sqlite", filepath.Join(t.TempDir(), "named.db"))
}

// TestNamedContinuationsMySQL covers the real MySQL transaction and publication paths.
func TestNamedContinuationsMySQL(t *testing.T) {
	if !integrationBackendEnabled(testenv.BackendMySQL) {
		t.Skip("mysql backend not selected")
	}
	ensureMySQLDB(t)
	exerciseNamedSQLContinuations(t, "mysql", mysqlDSN(integrationMySQL.addr))
}

// TestNamedContinuationsPostgres covers the real PostgreSQL transaction and publication paths.
func TestNamedContinuationsPostgres(t *testing.T) {
	if !integrationBackendEnabled(testenv.BackendPostgres) {
		t.Skip("postgres backend not selected")
	}
	ensurePostgresDB(t)
	exerciseNamedSQLContinuations(t, "pgx", postgresDSN(integrationPostgres.addr))
}
