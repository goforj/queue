package queue

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

// DatabaseConfig configures the SQL-backed database dispatcher.
// @group Config
type DatabaseConfig struct {
	DB           *sql.DB
	DriverName   string
	DSN          string
	Workers      int
	PollInterval time.Duration
	DefaultQueue string
	AutoMigrate  bool
}

func (c DatabaseConfig) normalize() DatabaseConfig {
	if c.Workers <= 0 {
		c.Workers = runtime.NumCPU()
	}
	if c.Workers <= 0 {
		c.Workers = 1
	}
	if c.PollInterval <= 0 {
		c.PollInterval = 50 * time.Millisecond
	}
	if c.DefaultQueue == "" {
		c.DefaultQueue = "default"
	}
	if !c.AutoMigrate {
		c.AutoMigrate = true
	}
	return c
}

type databaseDispatcher struct {
	cfg DatabaseConfig
	db  *sql.DB

	ownsDB bool

	mu       sync.RWMutex
	handlers map[string]Handler

	startOnce    sync.Once
	shutdownOnce sync.Once
	workerWG     sync.WaitGroup
	shutdownCh   chan struct{}

	started      atomic.Bool
	shuttingDown atomic.Bool
}

type dbJob struct {
	id             int64
	queueName      string
	taskType       string
	payload        []byte
	timeoutSeconds sql.NullInt64
	maxRetry       int
	backoffMillis  int64
	attempt        int
}

// NewDatabaseDispatcher creates a durable SQL-backed dispatcher.
// @group Constructors
func NewDatabaseDispatcher(cfg DatabaseConfig) (Dispatcher, error) {
	cfg = cfg.normalize()
	if cfg.DB == nil {
		if cfg.DriverName == "" {
			return nil, fmt.Errorf("database driver name is required")
		}
		if cfg.DSN == "" {
			return nil, fmt.Errorf("database dsn is required")
		}
		db, err := sql.Open(cfg.DriverName, cfg.DSN)
		if err != nil {
			return nil, fmt.Errorf("open database failed: %w", err)
		}
		cfg.DB = db
	}

	d := &databaseDispatcher{
		cfg:        cfg,
		db:         cfg.DB,
		handlers:   make(map[string]Handler),
		shutdownCh: make(chan struct{}),
		ownsDB:     cfg.DB != nil && cfg.DriverName != "" && cfg.DSN != "",
	}
	if cfg.DriverName == "sqlite" {
		d.db.SetMaxOpenConns(1)
		d.db.SetMaxIdleConns(1)
		_, _ = d.db.Exec(`PRAGMA journal_mode=WAL`)
		_, _ = d.db.Exec(`PRAGMA busy_timeout=5000`)
	}
	return d, nil
}

func (d *databaseDispatcher) Driver() Driver {
	return DriverDatabase
}

func (d *databaseDispatcher) Register(taskType string, handler Handler) {
	if taskType == "" || handler == nil {
		return
	}
	d.mu.Lock()
	d.handlers[taskType] = handler
	d.mu.Unlock()
}

func (d *databaseDispatcher) Start(ctx context.Context) error {
	if d.started.Load() {
		return nil
	}
	var startErr error
	d.startOnce.Do(func() {
		if d.cfg.AutoMigrate {
			if err := d.ensureSchema(ctx); err != nil {
				startErr = err
				return
			}
		}
		for i := 0; i < d.cfg.Workers; i++ {
			d.workerWG.Add(1)
			go d.workerLoop()
		}
		d.started.Store(true)
	})
	return startErr
}

func (d *databaseDispatcher) Shutdown(ctx context.Context) error {
	d.shutdownOnce.Do(func() {
		d.shuttingDown.Store(true)
		close(d.shutdownCh)
	})
	if err := waitGroupWithContext(ctx, &d.workerWG); err != nil {
		return err
	}
	if d.ownsDB {
		_ = d.db.Close()
	}
	return nil
}

func (d *databaseDispatcher) Enqueue(ctx context.Context, task Task, opts ...Option) error {
	if d.shuttingDown.Load() {
		return ErrDispatcherShuttingDown
	}
	if task.Type == "" {
		return fmt.Errorf("task type is required")
	}
	if _, ok := d.lookup(task.Type); !ok {
		return fmt.Errorf("no handler registered for task type %q", task.Type)
	}
	if !d.started.Load() {
		if err := d.Start(context.Background()); err != nil {
			return err
		}
	}
	parsed := resolveOptions(opts...)
	payload := task.Payload
	if payload == nil {
		payload = []byte{}
	}
	queueName := parsed.queueName
	if queueName == "" {
		queueName = d.cfg.DefaultQueue
	}

	now := time.Now()
	availableAt := now
	if parsed.delay > 0 {
		availableAt = availableAt.Add(parsed.delay)
	}

	if parsed.uniqueTTL > 0 {
		ok, err := d.acquireUnique(ctx, task, now.Add(parsed.uniqueTTL))
		if err != nil {
			return err
		}
		if !ok {
			return ErrDuplicate
		}
	}

	maxRetry := 0
	if parsed.maxRetry != nil {
		maxRetry = *parsed.maxRetry
	}
	backoffMillis := int64(0)
	if parsed.backoff != nil && *parsed.backoff > 0 {
		backoffMillis = parsed.backoff.Milliseconds()
	}

	var timeoutSeconds any
	if parsed.timeout != nil {
		timeoutSeconds = int64(parsed.timeout.Seconds())
	}

	query := d.rebind(
		`INSERT INTO queue_jobs
        (queue_name, task_type, payload, timeout_seconds, max_retry, backoff_millis, attempt, available_at, state, created_at, updated_at)
        VALUES (?, ?, ?, ?, ?, ?, 0, ?, 'pending', ?, ?)`,
	)
	_, err := d.db.ExecContext(
		ctx,
		query,
		queueName,
		task.Type,
		payload,
		timeoutSeconds,
		maxRetry,
		backoffMillis,
		availableAt.UnixMilli(),
		now.UnixMilli(),
		now.UnixMilli(),
	)
	return err
}

func (d *databaseDispatcher) lookup(taskType string) (Handler, bool) {
	d.mu.RLock()
	handler, ok := d.handlers[taskType]
	d.mu.RUnlock()
	return handler, ok
}

func (d *databaseDispatcher) workerLoop() {
	defer d.workerWG.Done()
	for {
		select {
		case <-d.shutdownCh:
			return
		default:
		}

		job, err := d.claimOne(context.Background())
		if err != nil {
			time.Sleep(d.cfg.PollInterval)
			continue
		}
		if job == nil {
			time.Sleep(d.cfg.PollInterval)
			continue
		}
		d.processJob(job)
	}
}

func (d *databaseDispatcher) processJob(job *dbJob) {
	handler, ok := d.lookup(job.taskType)
	if !ok {
		_ = d.markFailed(context.Background(), job, fmt.Errorf("no handler registered for task type %q", job.taskType))
		return
	}
	ctx := context.Background()
	if job.timeoutSeconds.Valid && job.timeoutSeconds.Int64 > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(job.timeoutSeconds.Int64)*time.Second)
		defer cancel()
	}
	err := handler(ctx, Task{Type: job.taskType, Payload: job.payload})
	if err == nil {
		_ = d.markDone(context.Background(), job)
		return
	}
	_ = d.markFailed(context.Background(), job, err)
}

func (d *databaseDispatcher) claimOne(ctx context.Context) (*dbJob, error) {
	now := time.Now().UnixMilli()
	maxAttempts := 1
	if d.usesOptimisticClaimLoop() {
		maxAttempts = 5
	}
	for i := 0; i < maxAttempts; i++ {
		tx, err := d.db.BeginTx(ctx, nil)
		if err != nil {
			return nil, err
		}
		job, err := d.selectPendingJob(ctx, tx, now)
		if err != nil {
			_ = tx.Rollback()
			return nil, err
		}
		if job == nil {
			_ = tx.Rollback()
			return nil, nil
		}
		update := d.rebind(`UPDATE queue_jobs SET state='processing', processing_started_at=?, updated_at=? WHERE id=? AND state='pending'`)
		res, err := tx.ExecContext(ctx, update, now, now, job.id)
		if err != nil {
			_ = tx.Rollback()
			return nil, err
		}
		rows, _ := res.RowsAffected()
		if rows == 0 {
			_ = tx.Rollback()
			continue
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return job, nil
	}
	return nil, nil
}

func (d *databaseDispatcher) selectPendingJob(ctx context.Context, tx *sql.Tx, now int64) (*dbJob, error) {
	query := `SELECT id, queue_name, task_type, payload, timeout_seconds, max_retry, backoff_millis, attempt
FROM queue_jobs
WHERE state='pending' AND available_at <= ?
ORDER BY id ASC
LIMIT 1`
	if !d.usesOptimisticClaimLoop() {
		query += ` FOR UPDATE SKIP LOCKED`
	}
	query = d.rebind(query)
	row := tx.QueryRowContext(ctx, query, now)
	job := &dbJob{}
	if err := row.Scan(
		&job.id,
		&job.queueName,
		&job.taskType,
		&job.payload,
		&job.timeoutSeconds,
		&job.maxRetry,
		&job.backoffMillis,
		&job.attempt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return job, nil
}

func (d *databaseDispatcher) usesOptimisticClaimLoop() bool {
	return d.cfg.DriverName == "sqlite"
}

func (d *databaseDispatcher) markDone(ctx context.Context, job *dbJob) error {
	query := d.rebind(`DELETE FROM queue_jobs WHERE id=?`)
	_, err := d.db.ExecContext(ctx, query, job.id)
	return err
}

func (d *databaseDispatcher) markFailed(ctx context.Context, job *dbJob, runErr error) error {
	nextAttempt := job.attempt + 1
	now := time.Now().UnixMilli()
	if nextAttempt > job.maxRetry {
		query := d.rebind(`UPDATE queue_jobs SET state='dead', attempt=?, last_error=?, updated_at=? WHERE id=?`)
		_, err := d.db.ExecContext(ctx, query, nextAttempt, runErr.Error(), now, job.id)
		return err
	}
	nextAt := now
	if job.backoffMillis > 0 {
		nextAt += job.backoffMillis
	}
	query := d.rebind(`UPDATE queue_jobs
SET state='pending', attempt=?, available_at=?, last_error=?, processing_started_at=NULL, updated_at=?
WHERE id=?`)
	_, err := d.db.ExecContext(ctx, query, nextAttempt, nextAt, runErr.Error(), now, job.id)
	return err
}

func (d *databaseDispatcher) acquireUnique(ctx context.Context, task Task, expiresAt time.Time) (bool, error) {
	now := time.Now().UnixMilli()
	expiresAtMillis := expiresAt.UnixMilli()
	key := uniqueTaskKey(task)
	insert := d.rebind(`INSERT INTO queue_unique_locks(lock_key, expires_at) VALUES(?, ?)`)
	_, err := d.db.ExecContext(ctx, insert, key, expiresAtMillis)
	if err == nil {
		return true, nil
	}
	if !isUniqueConstraintErr(err) {
		return false, err
	}

	update := d.rebind(`UPDATE queue_unique_locks SET expires_at=? WHERE lock_key=? AND expires_at <= ?`)
	res, err := d.db.ExecContext(ctx, update, expiresAtMillis, key, now)
	if err != nil {
		return false, err
	}
	rows, _ := res.RowsAffected()
	return rows == 1, nil
}

func uniqueTaskKey(task Task) string {
	hash := sha256.Sum256(append([]byte(task.Type+":"), task.Payload...))
	return hex.EncodeToString(hash[:])
}

func isUniqueConstraintErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "duplicate") ||
		strings.Contains(msg, "unique constraint") ||
		strings.Contains(msg, "unique violation")
}

func (d *databaseDispatcher) ensureSchema(ctx context.Context) error {
	stmts := d.schemaStatements()
	for _, stmt := range stmts {
		if _, err := d.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("ensure queue schema failed: %w", err)
		}
	}
	return nil
}

func (d *databaseDispatcher) schemaStatements() []string {
	switch d.cfg.DriverName {
	case "pgx", "postgres":
		return []string{
			`CREATE TABLE IF NOT EXISTS queue_jobs (
                id BIGSERIAL PRIMARY KEY,
                queue_name TEXT NOT NULL,
                task_type TEXT NOT NULL,
                payload BYTEA NOT NULL,
                timeout_seconds BIGINT NULL,
                max_retry INTEGER NOT NULL DEFAULT 0,
                backoff_millis BIGINT NOT NULL DEFAULT 0,
                attempt INTEGER NOT NULL DEFAULT 0,
                available_at BIGINT NOT NULL,
                processing_started_at BIGINT NULL,
                last_error TEXT NULL,
                state TEXT NOT NULL,
                created_at BIGINT NOT NULL,
                updated_at BIGINT NOT NULL
            )`,
			`CREATE INDEX IF NOT EXISTS idx_queue_jobs_ready ON queue_jobs(state, available_at, id)`,
			`CREATE TABLE IF NOT EXISTS queue_unique_locks (
                lock_key TEXT PRIMARY KEY,
                expires_at BIGINT NOT NULL
            )`,
		}
	case "sqlite":
		return []string{
			`CREATE TABLE IF NOT EXISTS queue_jobs (
                id INTEGER PRIMARY KEY AUTOINCREMENT,
                queue_name TEXT NOT NULL,
                task_type TEXT NOT NULL,
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
            )`,
			`CREATE INDEX IF NOT EXISTS idx_queue_jobs_ready ON queue_jobs(state, available_at, id)`,
			`CREATE TABLE IF NOT EXISTS queue_unique_locks (
                lock_key TEXT PRIMARY KEY,
                expires_at INTEGER NOT NULL
            )`,
		}
	default:
		return []string{
			`CREATE TABLE IF NOT EXISTS queue_jobs (
                id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
                queue_name VARCHAR(191) NOT NULL,
                task_type VARCHAR(191) NOT NULL,
                payload LONGBLOB NOT NULL,
                timeout_seconds BIGINT NULL,
                max_retry INT NOT NULL DEFAULT 0,
                backoff_millis BIGINT NOT NULL DEFAULT 0,
                attempt INT NOT NULL DEFAULT 0,
                available_at BIGINT NOT NULL,
                processing_started_at BIGINT NULL,
                last_error TEXT NULL,
                state VARCHAR(16) NOT NULL,
                created_at BIGINT NOT NULL,
                updated_at BIGINT NOT NULL,
                KEY idx_queue_jobs_ready (state, available_at, id)
            )`,
			`CREATE TABLE IF NOT EXISTS queue_unique_locks (
                lock_key VARCHAR(255) NOT NULL PRIMARY KEY,
                expires_at BIGINT NOT NULL
            )`,
		}
	}
}

func (d *databaseDispatcher) rebind(query string) string {
	if d.cfg.DriverName != "pgx" && d.cfg.DriverName != "postgres" {
		return query
	}
	var b strings.Builder
	arg := 1
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			b.WriteString(fmt.Sprintf("$%d", arg))
			arg++
			continue
		}
		b.WriteByte(query[i])
	}
	return b.String()
}
