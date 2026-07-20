package sqlitequeue

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goforj/queue"
	"github.com/goforj/queue/driver/sqlqueuecore"
)

// TestSQLiteDirectMetadataRoundTrip verifies a fresh SQL schema retains direct
// correlation independently from the application type and payload.
func TestSQLiteDirectMetadataRoundTrip(t *testing.T) {
	db := openSQLiteMetadataTestDB(t)
	backend, err := sqlqueuecore.New(queue.DatabaseConfig{
		DB:           db,
		DriverName:   "sqlite",
		DefaultQueue: "default",
		Workers:      1,
		PollInterval: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new SQL backend: %v", err)
	}
	t.Cleanup(func() { _ = backend.Shutdown(context.Background()) })

	received := make(chan queue.Job, 1)
	backend.Register("reports:build", func(_ context.Context, job queue.Job) error {
		received <- job
		return nil
	})
	if err := backend.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start SQL workers: %v", err)
	}

	want := queue.DriverJobMetadata{
		SchemaVersion: queue.DriverJobMetadataVersion,
		DispatchID:    "dsp_sqlite",
		JobID:         "job_sqlite",
		Queue:         "default",
	}
	job := queue.DriverWithMetadata(
		queue.NewJob("reports:build").Payload([]byte(`{"id":7}`)).OnQueue("default").Retry(2),
		want,
	)
	if err := backend.Dispatch(context.Background(), job); err != nil {
		t.Fatalf("dispatch direct SQL job: %v", err)
	}
	got := waitForSQLiteMetadataJob(t, received)
	if metadata := queue.DriverMetadata(got); metadata != want {
		t.Fatalf("delivered metadata = %+v, want %+v", metadata, want)
	}
	if got.Type != job.Type || string(got.PayloadBytes()) != string(job.PayloadBytes()) {
		t.Fatalf("delivered job = type:%q payload:%q, want type:%q payload:%q", got.Type, got.PayloadBytes(), job.Type, job.PayloadBytes())
	}
}

// TestSQLiteDirectMetadataSurvivesRetry verifies SQL state transitions retain
// one logical job identity while only the physical attempt number advances.
func TestSQLiteDirectMetadataSurvivesRetry(t *testing.T) {
	db := openSQLiteMetadataTestDB(t)
	backend, err := sqlqueuecore.New(queue.DatabaseConfig{
		DB:           db,
		DriverName:   "sqlite",
		DefaultQueue: "default",
		Workers:      1,
		PollInterval: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new SQL backend: %v", err)
	}
	t.Cleanup(func() { _ = backend.Shutdown(context.Background()) })

	deliveries := make(chan queue.Job, 2)
	backend.Register("reports:retry", func(_ context.Context, job queue.Job) error {
		deliveries <- job
		if queue.DriverOptions(job).Attempt == 0 {
			return errors.New("retry once")
		}
		return nil
	})
	if err := backend.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start SQL workers: %v", err)
	}

	want := queue.DriverJobMetadata{
		SchemaVersion: queue.DriverJobMetadataVersion,
		DispatchID:    "dsp_retry",
		JobID:         "job_retry",
		Queue:         "default",
	}
	job := queue.DriverWithMetadata(queue.NewJob("reports:retry").OnQueue("default").Retry(1), want)
	if err := backend.Dispatch(context.Background(), job); err != nil {
		t.Fatalf("dispatch retrying SQL job: %v", err)
	}
	first := waitForSQLiteMetadataJob(t, deliveries)
	second := waitForSQLiteMetadataJob(t, deliveries)
	if queue.DriverOptions(first).Attempt != 0 || queue.DriverOptions(second).Attempt != 1 {
		t.Fatalf("delivery attempts = %d, %d; want 0, 1", queue.DriverOptions(first).Attempt, queue.DriverOptions(second).Attempt)
	}
	if firstMetadata, secondMetadata := queue.DriverMetadata(first), queue.DriverMetadata(second); firstMetadata != want || secondMetadata != want {
		t.Fatalf("retry metadata = first:%+v second:%+v, want %+v", firstMetadata, secondMetadata, want)
	}
}

// TestSQLiteMetadataMigrationReadsLegacyAndUntrustedRows verifies additive
// migration preserves old envelopes while untrusted metadata cannot spoof IDs.
func TestSQLiteMetadataMigrationReadsLegacyAndUntrustedRows(t *testing.T) {
	db := openSQLiteMetadataTestDB(t)
	createLegacySQLiteQueueSchema(t, db)
	now := time.Now().UnixMilli()
	legacyPayload := []byte(`{"schema_version":1,"dispatch_id":"dsp_legacy","job_id":"job_legacy","job":{"type":"reports:legacy","payload":"eyJpZCI6MX0="}}`)
	insertLegacySQLiteQueueJob(t, db, "bus:job", legacyPayload, now)

	backend, err := sqlqueuecore.New(queue.DatabaseConfig{
		DB:           db,
		DriverName:   "sqlite",
		DefaultQueue: "default",
		Workers:      1,
		PollInterval: 5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new SQL backend: %v", err)
	}
	t.Cleanup(func() { _ = backend.Shutdown(context.Background()) })

	legacy := make(chan queue.Job, 1)
	backend.Register("bus:job", func(_ context.Context, job queue.Job) error {
		legacy <- job
		return nil
	})
	if err := backend.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start SQL workers: %v", err)
	}
	if !sqliteQueueColumnExists(t, db, "metadata_json") {
		t.Fatal("additive migration did not create metadata_json")
	}
	legacyJob := waitForSQLiteMetadataJob(t, legacy)
	legacyObserved := queue.ResolveObservedJobMetadataFromJob(legacyJob)
	if legacyObserved.JobType != "reports:legacy" || legacyObserved.DispatchID != "dsp_legacy" || legacyObserved.JobID != "job_legacy" {
		t.Fatalf("legacy envelope fallback = %+v", legacyObserved)
	}

	untrusted := make(chan queue.Job, 2)
	for _, jobType := range []string{"reports:malformed", "reports:unknown"} {
		backend.Register(jobType, func(_ context.Context, job queue.Job) error {
			untrusted <- job
			return nil
		})
	}
	insertSQLiteQueueJobWithMetadata(t, db, "reports:malformed", []byte(`{"id":2}`), `{`, now+1)
	insertSQLiteQueueJobWithMetadata(t, db, "reports:unknown", []byte(`{"id":3}`), `{"schema_version":99,"dispatch_id":"spoofed","job_id":"spoofed"}`, now+2)
	for range 2 {
		job := waitForSQLiteMetadataJob(t, untrusted)
		metadata := queue.ResolveObservedJobMetadataFromJob(job)
		if metadata.DispatchID != "" || metadata.JobID != "" || queue.DriverMetadata(job).SchemaVersion != 0 {
			t.Fatalf("untrusted row produced correlation: job=%q metadata=%+v", job.Type, metadata)
		}
	}
}

// TestSQLiteCallerManagedSchemaRequiresMetadataColumn verifies disabling
// migration fails before polling a schema that cannot retain direct IDs.
func TestSQLiteCallerManagedSchemaRequiresMetadataColumn(t *testing.T) {
	db := openSQLiteMetadataTestDB(t)
	createLegacySQLiteQueueSchema(t, db)
	backend, err := sqlqueuecore.New(queue.DatabaseConfig{
		DB:                 db,
		DriverName:         "sqlite",
		DefaultQueue:       "default",
		Workers:            1,
		DisableAutoMigrate: true,
	})
	if err != nil {
		t.Fatalf("new SQL backend: %v", err)
	}
	backend.Register("reports:build", func(context.Context, queue.Job) error { return nil })
	err = backend.StartWorkers(context.Background())
	if err == nil || !strings.Contains(err.Error(), "missing required metadata_json column") {
		t.Fatalf("start with legacy caller-managed schema = %v, want explicit metadata_json error", err)
	}
}

// TestSQLiteMetadataMigrationAllowsConcurrentStartup verifies two worker
// runtimes can race the additive migration without poisoning either lifecycle.
func TestSQLiteMetadataMigrationAllowsConcurrentStartup(t *testing.T) {
	db := openSQLiteMetadataTestDB(t)
	createLegacySQLiteQueueSchema(t, db)
	config := queue.DatabaseConfig{
		DB:           db,
		DriverName:   "sqlite",
		DefaultQueue: "default",
		Workers:      1,
		PollInterval: 5 * time.Millisecond,
	}
	first, err := sqlqueuecore.New(config)
	if err != nil {
		t.Fatalf("new first SQL backend: %v", err)
	}
	second, err := sqlqueuecore.New(config)
	if err != nil {
		t.Fatalf("new second SQL backend: %v", err)
	}
	t.Cleanup(func() {
		_ = first.Shutdown(context.Background())
		_ = second.Shutdown(context.Background())
	})

	start := make(chan struct{})
	results := make(chan error, 2)
	for _, backend := range []interface {
		StartWorkers(context.Context) error
	}{first, second} {
		go func() {
			<-start
			results <- backend.StartWorkers(context.Background())
		}()
	}
	close(start)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatalf("concurrent worker startup: %v", err)
		}
	}
	if !sqliteQueueColumnExists(t, db, "metadata_json") {
		t.Fatal("concurrent startup did not install metadata_json")
	}
}

// openSQLiteMetadataTestDB opens one isolated on-disk database so multiple SQL
// connections observe the same migration and durable rows.
func openSQLiteMetadataTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "metadata.db") + "?_busy_timeout=5000"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open SQLite database: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil && !errors.Is(err, sql.ErrConnDone) {
			t.Errorf("close SQLite database: %v", err)
		}
	})
	return db
}

// createLegacySQLiteQueueSchema creates the last compatible queue table shape
// before direct-delivery metadata was stored separately.
func createLegacySQLiteQueueSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.Exec(`CREATE TABLE queue_jobs (
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
        processing_token TEXT NULL,
        last_error TEXT NULL,
        state TEXT NOT NULL,
        created_at INTEGER NOT NULL,
        updated_at INTEGER NOT NULL
    )`)
	if err != nil {
		t.Fatalf("create legacy queue schema: %v", err)
	}
}

// insertLegacySQLiteQueueJob inserts one pre-metadata row using only columns an
// older producer knew how to populate.
func insertLegacySQLiteQueueJob(t *testing.T, db *sql.DB, jobType string, payload []byte, now int64) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO queue_jobs
        (queue_name, job_type, payload, timeout_seconds, max_retry, backoff_millis, attempt, available_at, state, created_at, updated_at)
        VALUES ('default', ?, ?, NULL, 0, 0, 0, ?, 'pending', ?, ?)`, jobType, payload, now, now, now)
	if err != nil {
		t.Fatalf("insert legacy queue row: %v", err)
	}
}

// insertSQLiteQueueJobWithMetadata inserts a raw metadata fixture so malformed
// and future protocol versions are tested without public helpers filtering them.
func insertSQLiteQueueJobWithMetadata(t *testing.T, db *sql.DB, jobType string, payload []byte, metadata string, now int64) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO queue_jobs
        (queue_name, job_type, payload, metadata_json, timeout_seconds, max_retry, backoff_millis, attempt, available_at, state, created_at, updated_at)
        VALUES ('default', ?, ?, ?, NULL, 0, 0, 0, ?, 'pending', ?, ?)`, jobType, payload, metadata, now, now, now)
	if err != nil {
		t.Fatalf("insert raw metadata queue row: %v", err)
	}
}

// sqliteQueueColumnExists inspects the migrated table without depending on
// implementation-private schema helpers.
func sqliteQueueColumnExists(t *testing.T, db *sql.DB, columnName string) bool {
	t.Helper()
	rows, err := db.Query(`PRAGMA table_info(queue_jobs)`)
	if err != nil {
		t.Fatalf("inspect SQLite queue columns: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			columnID     int
			name         string
			columnType   string
			notNull      int
			defaultValue sql.NullString
			primaryKey   int
		)
		if err := rows.Scan(&columnID, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatalf("scan SQLite queue column: %v", err)
		}
		if name == columnName {
			return true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate SQLite queue columns: %v", err)
	}
	return false
}

// waitForSQLiteMetadataJob waits for one polled delivery without allowing a
// broken migration to hang the test suite.
func waitForSQLiteMetadataJob(t *testing.T, jobs <-chan queue.Job) queue.Job {
	t.Helper()
	select {
	case job := <-jobs:
		return job
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for SQLite delivery")
		return queue.Job{}
	}
}
