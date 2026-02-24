package sqlqueuecore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/goforj/queue"
	"github.com/goforj/queue/queuecore"
)

const (
	defaultProcessingRecoveryGrace  = 2 * time.Second
	defaultProcessingLeaseNoTimeout = 5 * time.Minute
	databaseFinalizeRetryCount      = 3
	databaseFinalizeRetryDelay      = 25 * time.Millisecond
)

// DatabaseConfig configures the SQL-backed database q.
// @group Config
type DatabaseConfig = queue.DatabaseConfig

type localDatabaseConfig struct {
	DB                       *sql.DB
	DriverName               string
	DSN                      string
	Workers                  int
	PollInterval             time.Duration
	DefaultQueue             string
	AutoMigrate              bool
	ProcessingRecoveryGrace  time.Duration
	ProcessingLeaseNoTimeout time.Duration
	Observer                 queue.Observer
}

func (c localDatabaseConfig) normalize() localDatabaseConfig {
	c.Workers = defaultWorkerCount(c.Workers)
	if c.PollInterval <= 0 {
		c.PollInterval = 50 * time.Millisecond
	}
	if c.DefaultQueue == "" {
		c.DefaultQueue = "default"
	}
	if !c.AutoMigrate {
		c.AutoMigrate = true
	}
	if c.ProcessingRecoveryGrace <= 0 {
		c.ProcessingRecoveryGrace = defaultProcessingRecoveryGrace
	}
	if c.ProcessingLeaseNoTimeout <= 0 {
		c.ProcessingLeaseNoTimeout = defaultProcessingLeaseNoTimeout
	}
	return c
}

type databaseQueue struct {
	cfg localDatabaseConfig
	db  *sql.DB

	ownsDB bool

	mu       sync.RWMutex
	handlers map[string]queue.Handler

	startOnce    sync.Once
	shutdownOnce sync.Once
	workerWG     sync.WaitGroup
	shutdownCh   chan struct{}

	started      atomic.Bool
	shuttingDown atomic.Bool
	observer     queue.Observer
}

type dbJob struct {
	id             int64
	queueName      string
	jobType        string
	payload        []byte
	timeoutSeconds sql.NullInt64
	maxRetry       int
	backoffMillis  int64
	attempt        int
}

func New(cfg queue.DatabaseConfig) (*databaseQueue, error) {
	local := localDatabaseConfig{
		DB:                       cfg.DB,
		DriverName:               cfg.DriverName,
		DSN:                      cfg.DSN,
		Workers:                  cfg.Workers,
		PollInterval:             cfg.PollInterval,
		DefaultQueue:             cfg.DefaultQueue,
		AutoMigrate:              cfg.AutoMigrate,
		ProcessingRecoveryGrace:  cfg.ProcessingRecoveryGrace,
		ProcessingLeaseNoTimeout: cfg.ProcessingLeaseNoTimeout,
		Observer:                 cfg.Observer,
	}.normalize()
	cfg = queue.DatabaseConfig{
		DB:                       local.DB,
		DriverName:               local.DriverName,
		DSN:                      local.DSN,
		Workers:                  local.Workers,
		PollInterval:             local.PollInterval,
		DefaultQueue:             local.DefaultQueue,
		AutoMigrate:              local.AutoMigrate,
		ProcessingRecoveryGrace:  local.ProcessingRecoveryGrace,
		ProcessingLeaseNoTimeout: local.ProcessingLeaseNoTimeout,
		Observer:                 local.Observer,
	}
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

	d := &databaseQueue{
		cfg:        local,
		db:         cfg.DB,
		handlers:   make(map[string]queue.Handler),
		shutdownCh: make(chan struct{}),
		ownsDB:     cfg.DB != nil && cfg.DriverName != "" && cfg.DSN != "",
		observer:   cfg.Observer,
	}
	if cfg.DriverName == "sqlite" {
		d.db.SetMaxOpenConns(1)
		d.db.SetMaxIdleConns(1)
		_, _ = d.db.Exec(`PRAGMA journal_mode=WAL`)
		_, _ = d.db.Exec(`PRAGMA busy_timeout=5000`)
	}
	return d, nil
}

func (d *databaseQueue) Driver() queue.Driver {
	return queue.DriverDatabase
}

func (d *databaseQueue) Register(jobType string, handler queue.Handler) {
	if jobType == "" || handler == nil {
		return
	}
	d.mu.Lock()
	d.handlers[jobType] = handler
	d.mu.Unlock()
}

func (d *databaseQueue) StartWorkers(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
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

func (d *databaseQueue) Shutdown(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
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

func (d *databaseQueue) Dispatch(ctx context.Context, job queue.Job) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if d.shuttingDown.Load() {
		return queue.ErrQueuerShuttingDown
	}
	if err := queuecore.ValidateDriverJob(job); err != nil {
		return err
	}
	if !d.started.Load() && d.hasHandlers() {
		if err := d.StartWorkers(context.Background()); err != nil {
			return err
		}
	}
	parsed := queuecore.DriverOptions(job)
	payloadBytes := job.PayloadBytes()
	if payloadBytes == nil {
		payloadBytes = []byte{}
	}
	queueName := parsed.QueueName
	if queueName == "" {
		return fmt.Errorf("job queue is required")
	}

	now := time.Now()
	availableAt := now
	if parsed.Delay > 0 {
		availableAt = availableAt.Add(parsed.Delay)
	}

	if parsed.UniqueTTL > 0 {
		ok, err := d.acquireUnique(ctx, job, queueName, now.Add(parsed.UniqueTTL))
		if err != nil {
			return err
		}
		if !ok {
			return queuecore.ErrDuplicate
		}
	}

	maxRetry := 0
	if parsed.MaxRetry != nil {
		maxRetry = *parsed.MaxRetry
	}
	backoffMillis := int64(0)
	if parsed.Backoff != nil && *parsed.Backoff > 0 {
		backoffMillis = parsed.Backoff.Milliseconds()
	}

	var timeoutSeconds any
	if parsed.Timeout != nil {
		seconds := int64(math.Ceil(parsed.Timeout.Seconds()))
		if seconds < 1 {
			seconds = 1
		}
		timeoutSeconds = seconds
	}

	query := d.rebind(
		`INSERT INTO queue_jobs
        (queue_name, job_type, payload, timeout_seconds, max_retry, backoff_millis, attempt, available_at, state, created_at, updated_at)
        VALUES (?, ?, ?, ?, ?, ?, 0, ?, 'pending', ?, ?)`,
	)
	_, err := d.db.ExecContext(
		ctx,
		query,
		queueName,
		job.Type,
		payloadBytes,
		timeoutSeconds,
		maxRetry,
		backoffMillis,
		availableAt.UnixMilli(),
		now.UnixMilli(),
		now.UnixMilli(),
	)
	return err
}

func (d *databaseQueue) Stats(ctx context.Context) (queue.StatsSnapshot, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	query := d.rebind(`SELECT queue_name, state, COUNT(*) FROM queue_jobs GROUP BY queue_name, state`)
	rows, err := d.db.QueryContext(ctx, query)
	if err != nil {
		return queue.StatsSnapshot{}, err
	}
	defer rows.Close()

	byQueue := make(map[string]queue.QueueCounters)
	for rows.Next() {
		var queueName string
		var state string
		var count int64
		if scanErr := rows.Scan(&queueName, &state, &count); scanErr != nil {
			return queue.StatsSnapshot{}, scanErr
		}
		counters := byQueue[queueName]
		switch state {
		case "pending":
			counters.Pending += count
		case "processing":
			counters.Active += count
		case "dead":
			counters.Archived += count
			counters.Failed += count
		}
		byQueue[queueName] = counters
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return queue.StatsSnapshot{}, rowsErr
	}
	throughput := make(map[string]queue.QueueThroughput, len(byQueue))
	for queueName := range byQueue {
		throughput[queueName] = queue.QueueThroughput{}
	}
	return queue.StatsSnapshot{ByQueue: byQueue, ThroughputByQueue: throughput}, nil
}

func (d *databaseQueue) lookup(jobType string) (queue.Handler, bool) {
	d.mu.RLock()
	handler, ok := d.handlers[jobType]
	d.mu.RUnlock()
	return handler, ok
}

func (d *databaseQueue) hasHandlers() bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return len(d.handlers) > 0
}

func (d *databaseQueue) workerLoop() {
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

func (d *databaseQueue) processJob(job *dbJob) {
	handler, ok := d.lookup(job.jobType)
	if !ok {
		d.markFailedWithRetry(job, fmt.Errorf("no handler registered for job type %q", job.jobType))
		return
	}
	ctx := context.Background()
	if job.timeoutSeconds.Valid && job.timeoutSeconds.Int64 > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(job.timeoutSeconds.Int64)*time.Second)
		defer cancel()
	}
	err := handler(
		ctx,
		queuecore.DriverWithAttempt(
			queue.NewJob(job.jobType).
				Payload(job.payload).
				OnQueue(job.queueName).
				Retry(job.maxRetry),
			job.attempt,
		),
	)
	if err == nil {
		d.markDoneWithRetry(job)
		return
	}
	d.markFailedWithRetry(job, err)
}

func (d *databaseQueue) markDoneWithRetry(job *dbJob) {
	for i := 0; i < databaseFinalizeRetryCount; i++ {
		if err := d.markDone(context.Background(), job); err == nil {
			return
		} else if i < databaseFinalizeRetryCount-1 {
			time.Sleep(databaseFinalizeRetryDelay)
		}
	}
}

func (d *databaseQueue) markFailedWithRetry(job *dbJob, runErr error) {
	for i := 0; i < databaseFinalizeRetryCount; i++ {
		if err := d.markFailed(context.Background(), job, runErr); err == nil {
			return
		} else if i < databaseFinalizeRetryCount-1 {
			time.Sleep(databaseFinalizeRetryDelay)
		}
	}
}

func (d *databaseQueue) claimOne(ctx context.Context) (*dbJob, error) {
	now := time.Now().UnixMilli()
	if err := d.recoverStaleProcessing(ctx, now); err != nil {
		return nil, err
	}
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

func (d *databaseQueue) recoverStaleProcessing(ctx context.Context, nowMillis int64) error {
	graceMillis := d.cfg.ProcessingRecoveryGrace.Milliseconds()
	noTimeoutCutoff := nowMillis - d.cfg.ProcessingLeaseNoTimeout.Milliseconds()
	if noTimeoutCutoff < 0 {
		noTimeoutCutoff = 0
	}
	query := d.rebind(`UPDATE queue_jobs
SET state='pending', available_at=?, processing_started_at=NULL, updated_at=?, last_error=?
WHERE state='processing' AND processing_started_at IS NOT NULL AND (
    (timeout_seconds IS NOT NULL AND timeout_seconds > 0 AND (processing_started_at + (timeout_seconds * 1000) + ?) <= ?)
    OR
    ((timeout_seconds IS NULL OR timeout_seconds <= 0) AND processing_started_at <= ?)
)`)
	res, err := d.db.ExecContext(
		ctx,
		query,
		nowMillis,
		nowMillis,
		"recovered stale processing job",
		graceMillis,
		nowMillis,
		noTimeoutCutoff,
	)
	if err != nil {
		return err
	}
	if d.observer != nil {
		if rows, rowsErr := res.RowsAffected(); rowsErr == nil && rows > 0 {
			for i := int64(0); i < rows; i++ {
				queuecore.SafeObserve(d.observer, queue.Event{
					Kind:   queue.EventProcessRecovered,
					Driver: queue.DriverDatabase,
					Time:   time.Now(),
				})
			}
		}
	}
	return nil
}

func (d *databaseQueue) selectPendingJob(ctx context.Context, tx *sql.Tx, now int64) (*dbJob, error) {
	query := `SELECT id, queue_name, job_type, payload, timeout_seconds, max_retry, backoff_millis, attempt
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
		&job.jobType,
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

func (d *databaseQueue) usesOptimisticClaimLoop() bool {
	return d.cfg.DriverName == "sqlite"
}

func (d *databaseQueue) markDone(ctx context.Context, job *dbJob) error {
	query := d.rebind(`DELETE FROM queue_jobs WHERE id=?`)
	_, err := d.db.ExecContext(ctx, query, job.id)
	return err
}

func (d *databaseQueue) markFailed(ctx context.Context, job *dbJob, runErr error) error {
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

func (d *databaseQueue) acquireUnique(ctx context.Context, job queue.Job, queueName string, expiresAt time.Time) (bool, error) {
	now := time.Now().UnixMilli()
	expiresAtMillis := expiresAt.UnixMilli()
	key := uniqueJobKey(job, queueName)
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

func uniqueJobKey(job queue.Job, queueName string) string {
	hash := sha256.Sum256(append([]byte(queueName+":"+job.Type+":"), job.PayloadBytes()...))
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

func (d *databaseQueue) ensureSchema(ctx context.Context) error {
	stmts := d.schemaStatements()
	for _, stmt := range stmts {
		if _, err := d.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("ensure queue schema failed: %w", err)
		}
	}
	return nil
}

func (d *databaseQueue) schemaStatements() []string {
	switch d.cfg.DriverName {
	case "pgx", "postgres":
		return []string{
			`CREATE TABLE IF NOT EXISTS queue_jobs (
                id BIGSERIAL PRIMARY KEY,
                queue_name TEXT NOT NULL,
                job_type TEXT NOT NULL,
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
                job_type VARCHAR(191) NOT NULL,
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

func defaultWorkerCount(n int) int {
	if n <= 0 {
		return 1
	}
	return n
}

func waitGroupWithContext(ctx context.Context, wg *sync.WaitGroup) error {
	if wg == nil {
		return nil
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		wg.Wait()
	}()
	if ctx == nil {
		<-done
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (d *databaseQueue) rebind(query string) string {
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
