//go:build integration

package root_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/goforj/queue"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

func newDatabaseQueueIntegration(t *testing.T, cfg queue.DatabaseConfig) queue.QueueRuntime {
	t.Helper()
	q, err := newQueueRuntime(queue.Config{
		Driver:         queue.DriverDatabase,
		Database:       cfg.DB,
		DatabaseDriver: cfg.DriverName,
		DatabaseDSN:    cfg.DSN,
		DefaultQueue:   cfg.DefaultQueue,
	})
	if err != nil {
		t.Fatalf("new database queue failed: %v", err)
	}
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = q.Shutdown(shutdownCtx)
	})
	return q
}

func runDatabaseIntegrationSuite(t *testing.T, name string, cfg queue.DatabaseConfig) {
	t.Run(name+"_dispatch_and_process", func(t *testing.T) {
		d := newDatabaseQueueIntegration(t, cfg)
		triggered := make(chan struct{}, 1)
		d.Register("job:db:basic", func(_ context.Context, _ queue.Job) error {
			triggered <- struct{}{}
			return nil
		})
		if err := d.StartWorkers(context.Background()); err != nil {
			t.Fatalf("start failed: %v", err)
		}
		resetQueueTables(t, cfg)
		if err := d.DispatchCtx(context.Background(), queue.NewJob("job:db:basic").Payload([]byte("hello")).OnQueue("default")); err != nil {
			t.Fatalf("dispatch failed: %v", err)
		}
		select {
		case <-triggered:
		case <-time.After(15 * time.Second):
			logDatabaseQueueState(t, cfg, "dispatch_and_process timeout")
			t.Fatal("expected job to be processed")
		}
	})

	t.Run(name+"_delay", func(t *testing.T) {
		d := newDatabaseQueueIntegration(t, cfg)
		triggered := make(chan time.Time, 1)
		d.Register("job:db:delay", func(_ context.Context, _ queue.Job) error {
			triggered <- time.Now()
			return nil
		})
		if err := d.StartWorkers(context.Background()); err != nil {
			t.Fatalf("start failed: %v", err)
		}
		resetQueueTables(t, cfg)
		start := time.Now()
		delay := 300 * time.Millisecond
		if err := d.DispatchCtx(context.Background(), queue.NewJob("job:db:delay").OnQueue("default").Delay(delay)); err != nil {
			t.Fatalf("dispatch failed: %v", err)
		}
		select {
		case at := <-triggered:
			if at.Sub(start) < delay-100*time.Millisecond {
				t.Fatalf("expected delay >= %s, got %s", delay, at.Sub(start))
			}
		case <-time.After(15 * time.Second):
			logDatabaseQueueState(t, cfg, "delay timeout")
			t.Fatal("expected delayed job to run")
		}
	})

	t.Run(name+"_unique", func(t *testing.T) {
		d := newDatabaseQueueIntegration(t, cfg)
		d.Register("job:db:unique", func(_ context.Context, _ queue.Job) error { return nil })
		if err := d.StartWorkers(context.Background()); err != nil {
			t.Fatalf("start failed: %v", err)
		}
		resetQueueTables(t, cfg)
		jobType := "job:db:unique"
		payload := []byte("same")
		err := d.DispatchCtx(context.Background(), queue.NewJob(jobType).Payload(payload).OnQueue("default").UniqueFor(500*time.Millisecond))
		if err != nil {
			t.Fatalf("first dispatch failed: %v", err)
		}
		err = d.DispatchCtx(context.Background(), queue.NewJob(jobType).Payload(payload).OnQueue("default").UniqueFor(500*time.Millisecond))
		if !errors.Is(err, queue.ErrDuplicate) {
			t.Fatalf("expected ErrDuplicate, got %v", err)
		}
	})

	t.Run(name+"_retry_backoff", func(t *testing.T) {
		d := newDatabaseQueueIntegration(t, cfg)
		triggered := make(chan struct{}, 1)
		var calls atomic.Int64
		d.Register("job:db:retry", func(_ context.Context, _ queue.Job) error {
			if calls.Add(1) < 3 {
				return fmt.Errorf("transient")
			}
			triggered <- struct{}{}
			return nil
		})
		if err := d.StartWorkers(context.Background()); err != nil {
			t.Fatalf("start failed: %v", err)
		}
		resetQueueTables(t, cfg)
		if err := d.DispatchCtx(context.Background(), queue.NewJob("job:db:retry").OnQueue("default").Retry(2).Backoff(50*time.Millisecond)); err != nil {
			t.Fatalf("dispatch failed: %v", err)
		}
		select {
		case <-triggered:
		case <-time.After(20 * time.Second):
			logDatabaseQueueState(t, cfg, "retry timeout")
			t.Fatal("expected retry flow to succeed")
		}
		if calls.Load() != 3 {
			t.Fatalf("expected 3 calls, got %d", calls.Load())
		}
	})
}

func logDatabaseQueueState(t *testing.T, cfg queue.DatabaseConfig, reason string) {
	t.Helper()
	db, err := sql.Open(cfg.DriverName, cfg.DSN)
	if err != nil {
		t.Logf("%s: open failed: %v", reason, err)
		return
	}
	defer db.Close()

	rows, err := db.Query(`SELECT id, state, available_at, attempt, max_retry, backoff_millis, last_error FROM queue_jobs ORDER BY id`)
	if err != nil {
		t.Logf("%s: query queue_jobs failed: %v", reason, err)
		return
	}
	defer rows.Close()

	now := time.Now().UnixMilli()
	for rows.Next() {
		var (
			id            int64
			state         string
			availableAt   int64
			attempt       int
			maxRetry      int
			backoffMillis int64
			lastError     sql.NullString
		)
		if scanErr := rows.Scan(&id, &state, &availableAt, &attempt, &maxRetry, &backoffMillis, &lastError); scanErr != nil {
			t.Logf("%s: scan failed: %v", reason, scanErr)
			return
		}
		t.Logf(
			"%s: job id=%d state=%s available_at=%d delta_ms=%d attempt=%d max_retry=%d backoff_ms=%d last_error=%q",
			reason,
			id,
			state,
			availableAt,
			availableAt-now,
			attempt,
			maxRetry,
			backoffMillis,
			lastError.String,
		)
	}
}

func TestDatabaseIntegration_SQLite(t *testing.T) {
	if !integrationBackendEnabled("sqlite") {
		t.Skip("sqlite integration backend not selected")
	}
	cfg := queue.DatabaseConfig{
		DriverName:   "sqlite",
		DSN:          fmt.Sprintf("%s/queue-%d.db", t.TempDir(), time.Now().UnixNano()),
		Workers:      1,
		PollInterval: 10 * time.Millisecond,
	}
	runDatabaseIntegrationSuite(t, "sqlite", cfg)
}

func TestDatabaseIntegration_MySQL(t *testing.T) {
	if !integrationBackendEnabled("mysql") {
		t.Skip("mysql integration backend not selected")
	}
	ensureMySQLDB(t)
	cfg := queue.DatabaseConfig{
		DriverName:   "mysql",
		DSN:          fmt.Sprintf("queue:queue@tcp(%s)/queue_test?parseTime=true", integrationMySQL.addr),
		Workers:      1,
		PollInterval: 10 * time.Millisecond,
	}
	runDatabaseIntegrationSuite(t, "mysql", cfg)
}

func TestDatabaseIntegration_Postgres(t *testing.T) {
	if !integrationBackendEnabled("postgres") {
		t.Skip("postgres integration backend not selected")
	}
	ensurePostgresDB(t)
	cfg := queue.DatabaseConfig{
		DriverName:   "pgx",
		DSN:          fmt.Sprintf("postgres://queue:queue@%s/queue_test?sslmode=disable", integrationPostgres.addr),
		Workers:      1,
		PollInterval: 10 * time.Millisecond,
	}
	runDatabaseIntegrationSuite(t, "postgres", cfg)
}
