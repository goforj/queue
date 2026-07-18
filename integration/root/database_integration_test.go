//go:build integration

package root_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/goforj/queue"
	"github.com/goforj/queue/driver/sqlitequeue"
	"github.com/goforj/queue/integration/testenv"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

type databaseSettlementRecorder struct {
	mu         sync.Mutex
	events     []queue.Event
	settlement chan struct{}
	once       sync.Once
}

// prepareSQLiteIntegrationSchema completes migration and worker cleanup before a fault trigger takes ownership of the database.
func prepareSQLiteIntegrationSchema(t *testing.T, dsn string) {
	t.Helper()
	bootstrap, err := sqlitequeue.New(dsn)
	if err != nil {
		t.Fatalf("new SQLite schema bootstrap: %v", err)
	}
	if err := bootstrap.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start SQLite schema bootstrap: %v", err)
	}
	if err := bootstrap.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown SQLite schema bootstrap: %v", err)
	}
}

// Observe records SQL delivery events and signals the first finalization failure.
func (r *databaseSettlementRecorder) Observe(_ context.Context, event queue.Event) {
	r.mu.Lock()
	r.events = append(r.events, event)
	r.mu.Unlock()
	if event.Kind == queue.EventSettlementFailed {
		r.once.Do(func() { close(r.settlement) })
	}
}

// has reports whether the recorder contains one matching event.
func (r *databaseSettlementRecorder) has(kind queue.EventKind, jobType string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, event := range r.events {
		if event.Kind == kind && event.JobType == jobType {
			return true
		}
	}
	return false
}

// count returns the number of recorded events matching one kind and logical job type.
func (r *databaseSettlementRecorder) count(kind queue.EventKind, jobType string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	count := 0
	for _, event := range r.events {
		if event.Kind == kind && event.JobType == jobType {
			count++
		}
	}
	return count
}

func newDatabaseQueueIntegration(t *testing.T, cfg queue.DatabaseConfig) QueueRuntime {
	t.Helper()
	var runtimeCfg any
	switch cfg.DriverName {
	case testenv.BackendMySQL:
		runtimeCfg = withDefaultQueue(withDBHandle(mysqlCfg(cfg.DSN), cfg.DB), cfg.DefaultQueue)
	case "pgx", testenv.BackendPostgres:
		runtimeCfg = withDefaultQueue(withDBHandle(postgresCfg(cfg.DSN), cfg.DB), cfg.DefaultQueue)
	case testenv.BackendSQLite:
		runtimeCfg = withDefaultQueue(withDBHandle(sqliteCfg(cfg.DSN), cfg.DB), cfg.DefaultQueue)
	default:
		t.Fatalf("unsupported database driver %q", cfg.DriverName)
	}
	q, err := newQueueRuntime(runtimeCfg)
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

// runSQLiteStaleProcessingFence proves a superseded handler cannot settle the row generation now owned by another runtime.
func runSQLiteStaleProcessingFence(t *testing.T, staleResult error) {
	t.Helper()
	dsn := fmt.Sprintf("%s/queue-processing-fence-%d.db", t.TempDir(), time.Now().UnixNano())
	recorder := &databaseSettlementRecorder{settlement: make(chan struct{})}
	runtimeCfg := withDBRecoveryPolicy(
		withObserver(withDefaultQueue(sqliteCfg(dsn), "default"), recorder),
		10*time.Millisecond,
		time.Minute,
	)
	firstRuntime, err := newQueueRuntime(runtimeCfg)
	if err != nil {
		t.Fatalf("new first fenced settlement runtime: %v", err)
	}
	secondRuntime, err := newQueueRuntime(runtimeCfg)
	if err != nil {
		t.Fatalf("new second fenced settlement runtime: %v", err)
	}

	firstStarted := make(chan struct{})
	secondStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	releaseSecond := make(chan struct{})
	unexpectedCall := make(chan int64, 1)
	var (
		calls             atomic.Int64
		releaseFirstOnce  sync.Once
		releaseSecondOnce sync.Once
	)
	t.Cleanup(func() {
		releaseFirstOnce.Do(func() { close(releaseFirst) })
		releaseSecondOnce.Do(func() { close(releaseSecond) })
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = firstRuntime.Shutdown(shutdownCtx)
		_ = secondRuntime.Shutdown(shutdownCtx)
	})

	jobType := "job:db:processing-fence:success"
	if staleResult != nil {
		jobType = "job:db:processing-fence:failure"
	}
	handler := func(context.Context, queue.Job) error {
		switch call := calls.Add(1); call {
		case 1:
			close(firstStarted)
			<-releaseFirst
			return staleResult
		case 2:
			close(secondStarted)
			<-releaseSecond
			return nil
		default:
			select {
			case unexpectedCall <- call:
			default:
			}
			return nil
		}
	}
	firstRuntime.Register(jobType, handler)
	secondRuntime.Register(jobType, handler)
	if err := firstRuntime.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start first fenced settlement runtime: %v", err)
	}
	if err := secondRuntime.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start second fenced settlement runtime: %v", err)
	}
	db, err := sql.Open(testenv.BackendSQLite, dsn)
	if err != nil {
		t.Fatalf("open fenced settlement database: %v", err)
	}
	defer db.Close()
	if err := firstRuntime.Dispatch(queue.NewJob(jobType).OnQueue("default")); err != nil {
		t.Fatalf("dispatch fenced settlement job: %v", err)
	}
	select {
	case <-firstStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("first processing generation did not start")
	}
	result, err := db.Exec(`UPDATE queue_jobs SET processing_started_at=1 WHERE job_type=? AND state='processing'`, jobType)
	if err != nil {
		t.Fatalf("age first processing generation: %v", err)
	}
	if rows, err := result.RowsAffected(); err != nil || rows != 1 {
		t.Fatalf("aged first processing rows = %d, error %v; want 1", rows, err)
	}
	select {
	case <-secondStarted:
	case call := <-unexpectedCall:
		t.Fatalf("unexpected processing generation %d started before reclaim", call)
	case <-time.After(5 * time.Second):
		t.Fatal("stale processing generation was not recovered and reclaimed")
	}

	releaseFirstOnce.Do(func() { close(releaseFirst) })
	select {
	case <-recorder.settlement:
	case call := <-unexpectedCall:
		t.Fatalf("unexpected processing generation %d started during stale settlement", call)
	case <-time.After(5 * time.Second):
		t.Fatal("stale processing generation did not report settlement failure")
	}
	if successes := recorder.count(queue.EventProcessSucceeded, jobType); successes != 0 {
		t.Fatalf("stale generation committed %d process_succeeded events", successes)
	}

	var state string
	var processingToken sql.NullString
	var attempt int
	if err := db.QueryRow(`SELECT state, processing_token, attempt FROM queue_jobs WHERE job_type=?`, jobType).Scan(&state, &processingToken, &attempt); err != nil {
		t.Fatalf("reclaimed row was deleted or overwritten by stale handler: %v", err)
	}
	if state != "processing" || !processingToken.Valid || processingToken.String == "" || attempt != 0 {
		t.Fatalf("reclaimed row = state:%q token:%q valid:%t attempt:%d, want fenced processing claim at attempt 0", state, processingToken.String, processingToken.Valid, attempt)
	}

	releaseSecondOnce.Do(func() { close(releaseSecond) })
	deadline := time.Now().Add(5 * time.Second)
	for recorder.count(queue.EventProcessSucceeded, jobType) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if successes := recorder.count(queue.EventProcessSucceeded, jobType); successes != 1 {
		t.Fatalf("current generation process_succeeded events = %d, want 1", successes)
	}
	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM queue_jobs WHERE job_type=?`, jobType).Scan(&rows); err != nil {
		t.Fatalf("count finalized fenced row: %v", err)
	}
	if rows != 0 {
		t.Fatalf("current processing generation left %d queue rows", rows)
	}
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
		if err := d.Dispatch(queue.NewJob("job:db:basic").Payload([]byte("hello")).OnQueue("default")); err != nil {
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
		if err := d.Dispatch(queue.NewJob("job:db:delay").OnQueue("default").Delay(delay)); err != nil {
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
		err := d.Dispatch(queue.NewJob(jobType).Payload(payload).OnQueue("default").UniqueFor(500 * time.Millisecond))
		if err != nil {
			t.Fatalf("first dispatch failed: %v", err)
		}
		err = d.Dispatch(queue.NewJob(jobType).Payload(payload).OnQueue("default").UniqueFor(500 * time.Millisecond))
		if !errors.Is(err, queue.ErrDuplicate) {
			t.Fatalf("expected ErrDuplicate, got %v", err)
		}
	})

	t.Run(name+"_unique_multi_producer", func(t *testing.T) {
		first := newDatabaseQueueIntegration(t, cfg)
		second := newDatabaseQueueIntegration(t, cfg)
		for _, runtime := range []QueueRuntime{first, second} {
			runtime.Register("job:db:unique:concurrent", func(_ context.Context, _ queue.Job) error { return nil })
			if err := runtime.StartWorkers(context.Background()); err != nil {
				t.Fatalf("start producer runtime failed: %v", err)
			}
		}
		resetQueueTables(t, cfg)

		start := make(chan struct{})
		results := make(chan error, 2)
		var dispatches sync.WaitGroup
		for _, runtime := range []QueueRuntime{first, second} {
			dispatches.Add(1)
			go func(runtime QueueRuntime) {
				defer dispatches.Done()
				<-start
				results <- runtime.Dispatch(
					queue.NewJob("job:db:unique:concurrent").
						Payload([]byte("same logical work")).
						OnQueue("default").
						UniqueFor(time.Minute),
				)
			}(runtime)
		}
		close(start)
		dispatches.Wait()
		close(results)

		accepted := 0
		duplicates := 0
		for err := range results {
			switch {
			case err == nil:
				accepted++
			case errors.Is(err, queue.ErrDuplicate):
				duplicates++
			default:
				t.Fatalf("concurrent unique dispatch failed: %v", err)
			}
		}
		if accepted != 1 || duplicates != 1 {
			t.Fatalf("concurrent unique results = accepted:%d duplicate:%d, want 1/1", accepted, duplicates)
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
		if err := d.Dispatch(queue.NewJob("job:db:retry").OnQueue("default").Retry(2).Backoff(50 * time.Millisecond)); err != nil {
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
	if !integrationBackendEnabled(testenv.BackendSQLite) {
		t.Skip("sqlite integration backend not selected")
	}
	cfg := queue.DatabaseConfig{
		DriverName:   testenv.BackendSQLite,
		DSN:          fmt.Sprintf("%s/queue-%d.db", t.TempDir(), time.Now().UnixNano()),
		Workers:      1,
		PollInterval: 10 * time.Millisecond,
	}
	runDatabaseIntegrationSuite(t, testenv.BackendSQLite, cfg)

	t.Run("sqlite_caller_owned_database_remains_open", func(t *testing.T) {
		dsn := fmt.Sprintf("%s/queue-caller-owned-%d.db", t.TempDir(), time.Now().UnixNano())
		db, err := sql.Open(testenv.BackendSQLite, dsn)
		if err != nil {
			t.Fatalf("open caller-owned database: %v", err)
		}
		t.Cleanup(func() { _ = db.Close() })
		runtime := newDatabaseQueueIntegration(t, queue.DatabaseConfig{
			DB:           db,
			DriverName:   testenv.BackendSQLite,
			DSN:          dsn,
			Workers:      1,
			PollInterval: 10 * time.Millisecond,
			DefaultQueue: "default",
			AutoMigrate:  true,
		})
		if err := runtime.Shutdown(context.Background()); err != nil {
			t.Fatalf("shutdown caller-owned runtime: %v", err)
		}
		if err := db.PingContext(context.Background()); err != nil {
			t.Fatalf("caller-owned database was closed: %v", err)
		}
	})

	t.Run("sqlite_disable_auto_migrate_creates_no_schema", func(t *testing.T) {
		dsn := fmt.Sprintf("%s/queue-no-migrate-%d.db", t.TempDir(), time.Now().UnixNano())
		runtime, err := sqlitequeue.NewWithConfig(sqlitequeue.Config{
			DSN:                dsn,
			DisableAutoMigrate: true,
		})
		if err != nil {
			t.Fatalf("new no-migrate runtime: %v", err)
		}
		if err := runtime.StartWorkers(context.Background()); err != nil {
			t.Fatalf("start no-migrate runtime: %v", err)
		}
		t.Cleanup(func() { _ = runtime.Shutdown(context.Background()) })

		db, err := sql.Open(testenv.BackendSQLite, dsn)
		if err != nil {
			t.Fatalf("open no-migrate database: %v", err)
		}
		defer db.Close()
		var tables int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name IN ('queue_jobs', 'queue_unique_locks')`).Scan(&tables); err != nil {
			t.Fatalf("inspect no-migrate schema: %v", err)
		}
		if tables != 0 {
			t.Fatalf("auto migration created %d queue tables while disabled", tables)
		}
	})

	t.Run("sqlite_start_retries_after_canceled_migration", func(t *testing.T) {
		runtime := newDatabaseQueueIntegration(t, queue.DatabaseConfig{
			DriverName:   testenv.BackendSQLite,
			DSN:          fmt.Sprintf("%s/queue-start-retry-%d.db", t.TempDir(), time.Now().UnixNano()),
			Workers:      1,
			PollInterval: 10 * time.Millisecond,
			AutoMigrate:  true,
		})
		processed := make(chan struct{}, 1)
		runtime.Register("job:db:start-retry", func(context.Context, queue.Job) error {
			processed <- struct{}{}
			return nil
		})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := runtime.StartWorkers(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled start error = %v, want context.Canceled", err)
		}
		if err := runtime.StartWorkers(context.Background()); err != nil {
			t.Fatalf("retry start after canceled migration: %v", err)
		}
		if err := runtime.Dispatch(queue.NewJob("job:db:start-retry").OnQueue("default")); err != nil {
			t.Fatalf("dispatch after retried start: %v", err)
		}
		select {
		case <-processed:
		case <-time.After(5 * time.Second):
			t.Fatal("retried start reported success without a running worker")
		}
	})

	t.Run("sqlite_start_retries_after_migration_lock", func(t *testing.T) {
		dsn := fmt.Sprintf("%s/queue-start-lock-%d.db", t.TempDir(), time.Now().UnixNano())
		runtime := newDatabaseQueueIntegration(t, queue.DatabaseConfig{
			DriverName:   testenv.BackendSQLite,
			DSN:          dsn,
			Workers:      1,
			PollInterval: 10 * time.Millisecond,
			AutoMigrate:  true,
		})
		processed := make(chan struct{}, 1)
		runtime.Register("job:db:start-lock", func(context.Context, queue.Job) error {
			processed <- struct{}{}
			return nil
		})

		lockDB, err := sql.Open(testenv.BackendSQLite, dsn)
		if err != nil {
			t.Fatalf("open migration lock database: %v", err)
		}
		defer lockDB.Close()
		lockConn, err := lockDB.Conn(context.Background())
		if err != nil {
			t.Fatalf("open migration lock connection: %v", err)
		}
		defer lockConn.Close()
		if _, err := lockConn.ExecContext(context.Background(), `PRAGMA busy_timeout=0`); err != nil {
			t.Fatalf("disable migration lock wait: %v", err)
		}
		if _, err := lockConn.ExecContext(context.Background(), `BEGIN EXCLUSIVE`); err != nil {
			t.Fatalf("acquire migration lock: %v", err)
		}
		locked := true
		defer func() {
			if locked {
				_, _ = lockConn.ExecContext(context.Background(), `ROLLBACK`)
			}
		}()

		startCtx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		startErr := runtime.StartWorkers(startCtx)
		cancel()
		if startErr == nil {
			t.Fatal("migration unexpectedly succeeded while SQLite schema was exclusively locked")
		}
		if _, err := lockConn.ExecContext(context.Background(), `ROLLBACK`); err != nil {
			t.Fatalf("release migration lock: %v", err)
		}
		locked = false
		if err := runtime.StartWorkers(context.Background()); err != nil {
			t.Fatalf("retry start after migration lock: %v", err)
		}
		if err := runtime.Dispatch(queue.NewJob("job:db:start-lock").OnQueue("default")); err != nil {
			t.Fatalf("dispatch after migration retry: %v", err)
		}
		select {
		case <-processed:
		case <-time.After(5 * time.Second):
			t.Fatal("migration retry reported success without a running worker")
		}
	})

	t.Run("sqlite_unique_queue_insert_rollback", func(t *testing.T) {
		rollbackCfg := queue.DatabaseConfig{
			DriverName:   testenv.BackendSQLite,
			DSN:          fmt.Sprintf("%s/queue-rollback-%d.db", t.TempDir(), time.Now().UnixNano()),
			Workers:      1,
			PollInterval: 10 * time.Millisecond,
		}
		runtime := newDatabaseQueueIntegration(t, rollbackCfg)
		runtime.Register("job:db:unique:rollback", func(_ context.Context, _ queue.Job) error { return nil })
		if err := runtime.StartWorkers(context.Background()); err != nil {
			t.Fatalf("start rollback runtime failed: %v", err)
		}

		db, err := sql.Open(testenv.BackendSQLite, rollbackCfg.DSN)
		if err != nil {
			t.Fatalf("open rollback database: %v", err)
		}
		defer db.Close()
		const trigger = `CREATE TRIGGER reject_unique_queue_insert
BEFORE INSERT ON queue_jobs
WHEN NEW.job_type = 'job:db:unique:rollback'
BEGIN
    SELECT RAISE(ABORT, 'forced queue insert failure');
END`
		if _, err := db.Exec(trigger); err != nil {
			t.Fatalf("create rollback trigger: %v", err)
		}
		job := queue.NewJob("job:db:unique:rollback").OnQueue("default").UniqueFor(time.Minute)
		if err := runtime.Dispatch(job); err == nil || errors.Is(err, queue.ErrDuplicate) {
			t.Fatalf("forced queue insert error = %v, want storage rejection", err)
		}
		if _, err := db.Exec(`DROP TRIGGER reject_unique_queue_insert`); err != nil {
			t.Fatalf("drop rollback trigger: %v", err)
		}
		if err := runtime.Dispatch(job); err != nil {
			t.Fatalf("dispatch after rolled-back claim failed: %v", err)
		}
	})

	t.Run("sqlite_processing_token_migrates_existing_rows", func(t *testing.T) {
		dsn := fmt.Sprintf("%s/queue-processing-token-migration-%d.db", t.TempDir(), time.Now().UnixNano())
		db, err := sql.Open(testenv.BackendSQLite, dsn)
		if err != nil {
			t.Fatalf("open legacy schema database: %v", err)
		}
		defer db.Close()
		const legacySchema = `CREATE TABLE queue_jobs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			queue_name TEXT NOT NULL,
			job_type TEXT NOT NULL,
			payload BLOB NOT NULL,
			timeout_seconds INTEGER NULL,
			max_retry INTEGER NOT NULL DEFAULT 0,
			backoff_millis INTEGER NOT NULL DEFAULT 0,
			attempt INTEGER NOT NULL DEFAULT 0,
			available_at INTEGER NOT NULL,
			processing_started_at INTEGER NULL,
			last_error TEXT NULL,
			state TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		)`
		if _, err := db.Exec(legacySchema); err != nil {
			t.Fatalf("create legacy queue schema: %v", err)
		}
		now := time.Now().UnixMilli()
		if _, err := db.Exec(`INSERT INTO queue_jobs
			(queue_name, job_type, payload, max_retry, backoff_millis, attempt, available_at, state, created_at, updated_at)
			VALUES ('default', 'job:db:legacy-row', X'', 0, 0, 0, ?, 'pending', ?, ?)`, now+time.Hour.Milliseconds(), now, now); err != nil {
			t.Fatalf("insert legacy queue row: %v", err)
		}

		runtime, err := sqlitequeue.New(dsn)
		if err != nil {
			t.Fatalf("new runtime over legacy schema: %v", err)
		}
		if err := runtime.StartWorkers(context.Background()); err != nil {
			t.Fatalf("migrate legacy processing token column: %v", err)
		}
		if err := runtime.Shutdown(context.Background()); err != nil {
			t.Fatalf("shutdown migrated runtime: %v", err)
		}

		var processingToken sql.NullString
		if err := db.QueryRow(`SELECT processing_token FROM queue_jobs WHERE job_type='job:db:legacy-row'`).Scan(&processingToken); err != nil {
			t.Fatalf("read migrated legacy row: %v", err)
		}
		if processingToken.Valid {
			t.Fatalf("legacy pending row received processing token %q", processingToken.String)
		}
	})

	t.Run("sqlite_stale_success_cannot_delete_or_commit_reclaimed_generation", func(t *testing.T) {
		runSQLiteStaleProcessingFence(t, nil)
	})

	t.Run("sqlite_stale_failure_cannot_overwrite_reclaimed_generation", func(t *testing.T) {
		runSQLiteStaleProcessingFence(t, errors.New("stale application failure"))
	})

	t.Run("sqlite_success_waits_for_durable_finalization", func(t *testing.T) {
		dsn := fmt.Sprintf("%s/queue-settlement-%d.db", t.TempDir(), time.Now().UnixNano())
		prepareSQLiteIntegrationSchema(t, dsn)
		recorder := &databaseSettlementRecorder{settlement: make(chan struct{})}
		runtimeCfg := withObserver(withDefaultQueue(sqliteCfg(dsn), "default"), recorder)
		runtimeCfg.DisableAutoMigrate = true
		runtime, err := newQueueRuntime(runtimeCfg)
		if err != nil {
			t.Fatalf("new settlement runtime: %v", err)
		}
		t.Cleanup(func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = runtime.Shutdown(shutdownCtx)
		})
		const jobType = "job:db:settlement"
		runtime.Register(jobType, func(context.Context, queue.Job) error { return nil })

		db, err := sql.Open(testenv.BackendSQLite, dsn)
		if err != nil {
			t.Fatalf("open settlement database: %v", err)
		}
		defer db.Close()
		const trigger = `CREATE TRIGGER reject_job_finalization
BEFORE DELETE ON queue_jobs
WHEN OLD.job_type = 'job:db:settlement'
BEGIN
    SELECT RAISE(ABORT, 'forced finalization failure');
END`
		if _, err := db.Exec(trigger); err != nil {
			t.Fatalf("create finalization trigger: %v", err)
		}
		if err := runtime.StartWorkers(context.Background()); err != nil {
			t.Fatalf("start settlement runtime: %v", err)
		}
		if err := runtime.Dispatch(queue.NewJob(jobType).OnQueue("default")); err != nil {
			t.Fatalf("dispatch settlement job: %v", err)
		}
		select {
		case <-recorder.settlement:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for settlement_failed")
		}
		if recorder.has(queue.EventProcessSucceeded, jobType) {
			t.Fatal("process_succeeded emitted before durable row deletion")
		}
		if !recorder.has(queue.EventSettlementFailed, jobType) {
			t.Fatal("missing correlated settlement_failed event")
		}
	})

	t.Run("sqlite_missing_handler_finalization_failure_is_observed", func(t *testing.T) {
		dsn := fmt.Sprintf("%s/queue-missing-handler-settlement-%d.db", t.TempDir(), time.Now().UnixNano())
		prepareSQLiteIntegrationSchema(t, dsn)
		recorder := &databaseSettlementRecorder{settlement: make(chan struct{})}
		runtimeCfg := withObserver(withDefaultQueue(sqliteCfg(dsn), "default"), recorder)
		runtimeCfg.DisableAutoMigrate = true
		runtime, err := newQueueRuntime(runtimeCfg)
		if err != nil {
			t.Fatalf("new missing-handler runtime: %v", err)
		}
		t.Cleanup(func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = runtime.Shutdown(shutdownCtx)
		})
		db, err := sql.Open(testenv.BackendSQLite, dsn)
		if err != nil {
			t.Fatalf("open missing-handler database: %v", err)
		}
		defer db.Close()
		const jobType = "job:db:missing-handler-settlement"
		const trigger = `CREATE TRIGGER reject_missing_handler_finalization
BEFORE UPDATE OF state ON queue_jobs
WHEN OLD.job_type = 'job:db:missing-handler-settlement' AND OLD.state = 'processing'
BEGIN
    SELECT RAISE(ABORT, 'forced missing-handler finalization failure');
END`
		if _, err := db.Exec(trigger); err != nil {
			t.Fatalf("create missing-handler finalization trigger: %v", err)
		}
		if err := runtime.StartWorkers(context.Background()); err != nil {
			t.Fatalf("start missing-handler runtime: %v", err)
		}
		if err := runtime.Dispatch(queue.NewJob(jobType).OnQueue("default")); err != nil {
			t.Fatalf("dispatch missing-handler job: %v", err)
		}
		select {
		case <-recorder.settlement:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for missing-handler settlement_failed")
		}
		if !recorder.has(queue.EventSettlementFailed, jobType) {
			t.Fatal("missing correlated settlement_failed event for missing handler")
		}
	})
}

func TestDatabaseIntegration_MySQL(t *testing.T) {
	if !integrationBackendEnabled(testenv.BackendMySQL) {
		t.Skip("mysql integration backend not selected")
	}
	ensureMySQLDB(t)
	cfg := queue.DatabaseConfig{
		DriverName:   testenv.BackendMySQL,
		DSN:          fmt.Sprintf("queue:queue@tcp(%s)/queue_test?parseTime=true", integrationMySQL.addr),
		Workers:      1,
		PollInterval: 10 * time.Millisecond,
	}
	runDatabaseIntegrationSuite(t, testenv.BackendMySQL, cfg)
}

func TestDatabaseIntegration_Postgres(t *testing.T) {
	if !integrationBackendEnabled(testenv.BackendPostgres) {
		t.Skip("postgres integration backend not selected")
	}
	ensurePostgresDB(t)
	cfg := queue.DatabaseConfig{
		DriverName:   "pgx",
		DSN:          fmt.Sprintf("postgres://queue:queue@%s/queue_test?sslmode=disable", integrationPostgres.addr),
		Workers:      1,
		PollInterval: 10 * time.Millisecond,
	}
	runDatabaseIntegrationSuite(t, testenv.BackendPostgres, cfg)
}
