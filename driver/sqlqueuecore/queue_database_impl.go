package sqlqueuecore

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/goforj/queue"
	"github.com/goforj/queue/busruntime"
	"github.com/goforj/queue/queuecore"
)

type managedQueueTable string

const (
	defaultProcessingRecoveryGrace  = 2 * time.Second
	defaultProcessingLeaseNoTimeout = 5 * time.Minute
	databaseFinalizeRetryCount      = 3
	databaseFinalizeRetryDelay      = 25 * time.Millisecond
	databaseFinalizeTimeout         = 5 * time.Second
	databaseUniquePruneInterval     = 256
	databaseProcessingTokenBytes    = 16
	databaseRecoveryMarker          = "queue:internal:stale-processing-recovery:v1"
	databaseRecoveryDiagnostic      = "recovered stale processing job"
	managedQueueJobsTable           = managedQueueTable("queue_jobs")
	managedQueueUniqueLocksTable    = managedQueueTable("queue_unique_locks")
)

// managedQueueJobColumns captures the durable fields touched by dispatch,
// polling, recovery, settlement, administration, and statistics operations.
var managedQueueJobColumns = [...]string{
	"id",
	"queue_name",
	"job_type",
	"payload",
	"metadata_json",
	"timeout_seconds",
	"max_retry",
	"backoff_millis",
	"attempt",
	"available_at",
	"processing_started_at",
	"processing_token",
	"last_error",
	"state",
	"created_at",
	"updated_at",
}

// managedQueueUniqueLockColumns captures the durable fields required by
// distributed uniqueness acquisition and expiry pruning.
var managedQueueUniqueLockColumns = [...]string{
	"lock_key",
	"expires_at",
}

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
	DisableAutoMigrate       bool
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
	if c.DisableAutoMigrate {
		c.AutoMigrate = false
	} else if !c.AutoMigrate {
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

	startMu      sync.Mutex
	shutdownOnce sync.Once
	shutdownDone chan struct{}
	shutdownErr  error
	workerWG     sync.WaitGroup
	shutdownCh   chan struct{}

	started      atomic.Bool
	shuttingDown atomic.Bool
	uniqueClaims atomic.Uint64
	continuation *busruntime.ContinuationScope
	observer     queue.Observer
}

type databaseRowQueryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

type databaseExecer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

type dbJob struct {
	id                        int64
	processingToken           string
	queueName                 string
	jobType                   string
	payload                   []byte
	metadataJSON              sql.NullString
	timeoutSeconds            sql.NullInt64
	maxRetry                  int
	backoffMillis             int64
	attempt                   int
	recovered                 bool
	recoveryToken             string
	applicationStateCommitted bool
}

type databaseFailureSettlement struct {
	state       string
	attempt     int
	availableAt int64
}

// New constructs a SQL queue while retaining caller ownership of any supplied database handle.
func New(cfg queue.DatabaseConfig) (*databaseQueue, error) {
	ownsDB := cfg.DB == nil
	local := localDatabaseConfig{
		DB:                       cfg.DB,
		DriverName:               cfg.DriverName,
		DSN:                      cfg.DSN,
		Workers:                  cfg.Workers,
		PollInterval:             cfg.PollInterval,
		DefaultQueue:             cfg.DefaultQueue,
		AutoMigrate:              cfg.AutoMigrate,
		DisableAutoMigrate:       cfg.DisableAutoMigrate,
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
		DisableAutoMigrate:       local.DisableAutoMigrate,
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
		cfg:          local,
		db:           cfg.DB,
		handlers:     make(map[string]queue.Handler),
		shutdownCh:   make(chan struct{}),
		ownsDB:       ownsDB,
		continuation: busruntime.NewContinuationScope(),
		observer:     cfg.Observer,
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

// Preflight verifies connectivity and any caller-managed schema without changing database state.
func (d *databaseQueue) Preflight(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := d.db.PingContext(ctx); err != nil {
		return err
	}
	if d.cfg.AutoMigrate && !d.cfg.DisableAutoMigrate {
		return nil
	}
	return d.requireManagedQueueSchema(ctx)
}

func (d *databaseQueue) Register(jobType string, handler queue.Handler) {
	if jobType == "" || handler == nil {
		return
	}
	d.mu.Lock()
	d.handlers[jobType] = handler
	d.mu.Unlock()
}

// StartWorkers prepares or validates durable storage before admitting the worker generation.
func (d *databaseQueue) StartWorkers(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	d.startMu.Lock()
	defer d.startMu.Unlock()
	if d.shuttingDown.Load() {
		return queue.ErrQueuerShuttingDown
	}
	if d.started.Load() {
		return nil
	}
	if d.cfg.AutoMigrate && !d.cfg.DisableAutoMigrate {
		if err := d.ensureSchema(ctx); err != nil {
			return err
		}
	} else if err := d.requireManagedQueueSchema(ctx); err != nil {
		return err
	}
	for i := 0; i < d.cfg.Workers; i++ {
		d.workerWG.Add(1)
		go d.workerLoop()
	}
	d.started.Store(true)
	return nil
}

// Shutdown drains workers and closes only database handles opened by this queue.
func (d *databaseQueue) Shutdown(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	d.startMu.Lock()
	d.shutdownOnce.Do(func() {
		d.shuttingDown.Store(true)
		close(d.shutdownCh)
		d.shutdownDone = make(chan struct{})
		go d.finishShutdown(d.shutdownDone)
	})
	done := d.shutdownDone
	d.startMu.Unlock()
	select {
	case <-done:
		return d.takeShutdownError()
	case <-ctx.Done():
		return ctx.Err()
	}
}

// finishShutdown waits once for the worker generation so callers with expired
// deadlines can retry without creating another goroutine for the same drain.
func (d *databaseQueue) finishShutdown(done chan struct{}) {
	d.workerWG.Wait()
	var closeErr error
	if d.ownsDB {
		closeErr = d.db.Close()
	}
	d.startMu.Lock()
	d.shutdownErr = closeErr
	close(done)
	d.startMu.Unlock()
}

// takeShutdownError reports completed cleanup diagnostics once so a later
// outer runtime retry can converge after the owned resource is already closed.
func (d *databaseQueue) takeShutdownError() error {
	d.startMu.Lock()
	defer d.startMu.Unlock()
	err := d.shutdownErr
	d.shutdownErr = nil
	return err
}

// Dispatch commits a uniqueness claim and its queue row in one transaction when deduplication is requested.
func (d *databaseQueue) Dispatch(ctx context.Context, job queue.Job) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if d.shuttingDown.Load() && !d.continuation.Owns(ctx) {
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
	metadataJSON, err := databaseMetadataJSON(job)
	if err != nil {
		return err
	}

	query := d.rebind(
		`INSERT INTO queue_jobs
        (queue_name, job_type, payload, metadata_json, timeout_seconds, max_retry, backoff_millis, attempt, available_at, state, created_at, updated_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, 0, ?, 'pending', ?, ?)`,
	)
	args := []any{
		queueName,
		job.Type,
		payloadBytes,
		metadataJSON,
		timeoutSeconds,
		maxRetry,
		backoffMillis,
		availableAt.UnixMilli(),
		now.UnixMilli(),
		now.UnixMilli(),
	}
	if parsed.UniqueTTL <= 0 {
		_, err := d.db.ExecContext(ctx, query, args...)
		return err
	}

	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	ok, err := d.acquireUnique(ctx, tx, job, queueName, parsed.UniqueTTL)
	if err != nil {
		return err
	}
	if !ok {
		return queuecore.ErrDuplicate
	}
	if _, err := tx.ExecContext(ctx, query, args...); err != nil {
		return err
	}
	return tx.Commit()
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

func (d *databaseQueue) ListJobs(ctx context.Context, opts queue.ListJobsOptions) (queue.ListJobsResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return queue.ListJobsResult{}, err
	}
	opts = opts.Normalize()
	if opts.State == queue.JobStateCompleted {
		return queue.ListJobsResult{}, nil
	}

	now := time.Now().UnixMilli()
	where, args, err := d.jobsWhereClause(opts, now)
	if err != nil {
		return queue.ListJobsResult{}, err
	}

	countQuery := d.rebind(fmt.Sprintf("SELECT COUNT(*) FROM queue_jobs WHERE %s", where))
	var total int64
	if err := d.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return queue.ListJobsResult{}, err
	}

	listQuery := d.rebind(fmt.Sprintf(`SELECT id, queue_name, job_type, payload, max_retry, attempt, available_at, state, last_error
FROM queue_jobs
WHERE %s
ORDER BY id DESC
LIMIT ? OFFSET ?`, where))
	queryArgs := append(append([]any{}, args...), opts.PageSize, (opts.Page-1)*opts.PageSize)
	rows, err := d.db.QueryContext(ctx, listQuery, queryArgs...)
	if err != nil {
		return queue.ListJobsResult{}, err
	}
	defer rows.Close()

	jobs := make([]queue.JobSnapshot, 0, opts.PageSize)
	for rows.Next() {
		var (
			id          int64
			queueName   string
			jobType     string
			payload     []byte
			maxRetry    int
			attempt     int
			availableAt int64
			state       string
			lastErr     sql.NullString
		)
		if scanErr := rows.Scan(&id, &queueName, &jobType, &payload, &maxRetry, &attempt, &availableAt, &state, &lastErr); scanErr != nil {
			return queue.ListJobsResult{}, scanErr
		}
		var nextProcessAt *time.Time
		if availableAt > 0 {
			t := time.UnixMilli(availableAt)
			nextProcessAt = &t
		}
		jobs = append(jobs, queue.JobSnapshot{
			ID:            strconv.FormatInt(id, 10),
			Queue:         queueName,
			State:         databaseJobState(state, attempt, availableAt, now),
			Type:          jobType,
			Payload:       string(payload),
			Attempt:       attempt,
			MaxRetry:      maxRetry,
			LastError:     lastErr.String,
			NextProcessAt: nextProcessAt,
		})
	}
	if err := rows.Err(); err != nil {
		return queue.ListJobsResult{}, err
	}
	return queue.ListJobsResult{Jobs: jobs, Total: total}, nil
}

func (d *databaseQueue) RetryJob(ctx context.Context, queueName, jobID string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	id, err := strconv.ParseInt(strings.TrimSpace(jobID), 10, 64)
	if err != nil {
		return fmt.Errorf("invalid job id %q", jobID)
	}
	now := time.Now().UnixMilli()
	query := d.rebind(`UPDATE queue_jobs
	SET state='pending', available_at=?, processing_started_at=NULL, processing_token=NULL, last_error=NULL, updated_at=?
	WHERE id=? AND queue_name=?`)
	_, execErr := d.db.ExecContext(ctx, query, now, now, id, queuecore.NormalizeQueueName(queueName))
	return execErr
}

func (d *databaseQueue) CancelJob(ctx context.Context, jobID string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	id, err := strconv.ParseInt(strings.TrimSpace(jobID), 10, 64)
	if err != nil {
		return fmt.Errorf("invalid job id %q", jobID)
	}
	now := time.Now().UnixMilli()
	query := d.rebind(`UPDATE queue_jobs
	SET state='dead', processing_started_at=NULL, processing_token=NULL, last_error=?, updated_at=?
	WHERE id=?`)
	_, execErr := d.db.ExecContext(ctx, query, "canceled from queue admin", now, id)
	return execErr
}

func (d *databaseQueue) DeleteJob(ctx context.Context, queueName, jobID string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	id, err := strconv.ParseInt(strings.TrimSpace(jobID), 10, 64)
	if err != nil {
		return fmt.Errorf("invalid job id %q", jobID)
	}
	query := d.rebind(`DELETE FROM queue_jobs WHERE id=? AND queue_name=?`)
	_, execErr := d.db.ExecContext(ctx, query, id, queuecore.NormalizeQueueName(queueName))
	return execErr
}

func (d *databaseQueue) ClearQueue(ctx context.Context, queueName string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	query := d.rebind(`DELETE FROM queue_jobs WHERE queue_name=?`)
	_, execErr := d.db.ExecContext(ctx, query, queuecore.NormalizeQueueName(queueName))
	return execErr
}

func (d *databaseQueue) History(ctx context.Context, queueName string, window queue.QueueHistoryWindow) ([]queue.QueueHistoryPoint, error) {
	snapshot, err := d.Stats(ctx)
	if err != nil {
		return nil, err
	}
	points := queue.TimelineHistoryFromSnapshot(snapshot, queueName, window)
	if len(points) > 0 {
		return points, nil
	}
	return queue.SinglePointHistory(snapshot, queueName), nil
}

func (d *databaseQueue) jobsWhereClause(opts queue.ListJobsOptions, now int64) (string, []any, error) {
	queueName := queuecore.NormalizeQueueName(opts.Queue)
	where := []string{"queue_name = ?"}
	args := []any{queueName}

	switch opts.State {
	case queue.JobStatePending:
		where = append(where, "state = 'pending'", "attempt = 0", "available_at <= ?")
		args = append(args, now)
	case queue.JobStateActive:
		where = append(where, "state = 'processing'")
	case queue.JobStateScheduled:
		where = append(where, "state = 'pending'", "attempt = 0", "available_at > ?")
		args = append(args, now)
	case queue.JobStateRetry:
		where = append(where, "state = 'pending'", "attempt > 0")
	case queue.JobStateArchived:
		where = append(where, "state = 'dead'")
	default:
		return "", nil, fmt.Errorf("unsupported queue job state %q", opts.State)
	}

	return strings.Join(where, " AND "), args, nil
}

func databaseJobState(state string, attempt int, availableAt, now int64) queue.JobState {
	switch state {
	case "processing":
		return queue.JobStateActive
	case "dead":
		return queue.JobStateArchived
	case "pending":
		if attempt > 0 {
			return queue.JobStateRetry
		}
		if availableAt > now {
			return queue.JobStateScheduled
		}
		return queue.JobStatePending
	default:
		return queue.JobStatePending
	}
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

// processJob commits deferred success facts only after the durable row reaches its final state.
func (d *databaseQueue) processJob(job *dbJob) {
	handler, ok := d.lookup(job.jobType)
	if !ok {
		if err := d.markFailedWithRetry(job, fmt.Errorf("no handler registered for job type %q", job.jobType)); err != nil {
			d.handleSettlementFailure(context.Background(), job, err)
		}
		return
	}
	ctx, settlement := databaseSettlementContext(job)
	if job.timeoutSeconds.Valid && job.timeoutSeconds.Int64 > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(job.timeoutSeconds.Int64)*time.Second)
		defer cancel()
	}
	err := d.runHandlerWithContinuationPermit(
		ctx,
		handler,
		databaseDeliveryJob(job),
	)
	job.applicationStateCommitted = settlement.ApplicationStateCommitted()
	var settlementErr error
	if err == nil {
		settlementErr = d.markDoneWithRetry(job)
	} else {
		settlementErr = d.markFailedWithRetry(job, err)
	}
	if settlementErr != nil {
		d.handleSettlementFailure(ctx, job, settlementErr)
		return
	}
	settlement.Commit()
}

// handleSettlementFailure preserves inherited recovery lineage before reporting
// an exhausted physical settlement failure. Deferred facts remain uncommitted
// until a later generation positively settles the fenced row.
func (d *databaseQueue) handleSettlementFailure(ctx context.Context, job *dbJob, settlementErr error) {
	repairCtx, cancel := context.WithTimeout(context.Background(), databaseFinalizeTimeout)
	repairErr := d.restoreRecoveredSettlementLineage(repairCtx, job, settlementErr)
	cancel()
	if repairErr != nil {
		settlementErr = errors.Join(settlementErr, fmt.Errorf("restore recovered database settlement lineage: %w", repairErr))
	}
	d.observeSettlementFailure(ctx, job, settlementErr)
}

// databaseSettlementContext exposes stale-processing evidence to orchestration
// while retaining the driver's post-handler commit boundary on every delivery.
func databaseSettlementContext(job *dbJob) (context.Context, *busruntime.DeliverySettlement) {
	ctx, settlement := busruntime.WithDeliverySettlement(context.Background())
	if job != nil {
		ctx = busruntime.WithDeliveryProvenance(ctx, busruntime.DeliveryProvenance{
			GenerationID:          job.processingToken,
			RecoveredGenerationID: job.recoveryToken,
			Recovered:             job.recovered,
		})
	}
	return ctx, settlement
}

// runHandlerWithContinuationPermit limits shutdown-time descendant dispatch permission to this queue's active handler call.
func (d *databaseQueue) runHandlerWithContinuationPermit(ctx context.Context, handler queue.Handler, job queue.Job) error {
	handlerCtx, release := d.continuation.Permit(ctx)
	defer release()
	return handler(handlerCtx, job)
}

// databaseDeliveryJob restores persisted physical attempt metadata before the root orchestration adapter runs.
func databaseDeliveryJob(job *dbJob) queue.Job {
	delivery := queuecore.DriverWithAttempt(
		queue.NewJob(job.jobType).
			Payload(job.payload).
			OnQueue(job.queueName).
			Retry(job.maxRetry),
		job.attempt,
	)
	return queue.DriverWithMetadata(delivery, databaseJobMetadata(job.metadataJSON))
}

// databaseMetadataJSON serializes only metadata versions supported by this
// root module so unknown producer state cannot become trusted SQL correlation.
func databaseMetadataJSON(job queue.Job) (sql.NullString, error) {
	metadata := queue.DriverMetadata(job)
	if metadata.SchemaVersion == 0 {
		return sql.NullString{}, nil
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return sql.NullString{}, fmt.Errorf("marshal database job metadata: %w", err)
	}
	return sql.NullString{String: string(encoded), Valid: true}, nil
}

// databaseJobMetadata accepts nullable legacy rows and ignores malformed or
// unknown-version metadata without changing application delivery.
func databaseJobMetadata(raw sql.NullString) queue.DriverJobMetadata {
	if !raw.Valid || strings.TrimSpace(raw.String) == "" {
		return queue.DriverJobMetadata{}
	}
	var metadata queue.DriverJobMetadata
	if err := json.Unmarshal([]byte(raw.String), &metadata); err != nil {
		return queue.DriverJobMetadata{}
	}
	if metadata.SchemaVersion != queue.DriverJobMetadataVersion {
		return queue.DriverJobMetadata{}
	}
	return metadata
}

// markDoneWithRetry bounds each finalization attempt and returns the last error for settlement telemetry.
func (d *databaseQueue) markDoneWithRetry(job *dbJob) error {
	var lastErr error
	for i := 0; i < databaseFinalizeRetryCount; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), databaseFinalizeTimeout)
		err := d.markDone(ctx, job)
		cancel()
		if err == nil {
			return nil
		} else if i < databaseFinalizeRetryCount-1 {
			lastErr = err
			time.Sleep(databaseFinalizeRetryDelay)
		} else {
			lastErr = err
		}
	}
	return fmt.Errorf("finalize successful database job: %w", lastErr)
}

// markFailedWithRetry persists retry or terminal state without hiding exhausted finalization attempts.
func (d *databaseQueue) markFailedWithRetry(job *dbJob, runErr error) error {
	var lastErr error
	for i := 0; i < databaseFinalizeRetryCount; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), databaseFinalizeTimeout)
		err := d.markFailed(ctx, job, runErr)
		cancel()
		if err == nil {
			return nil
		} else if i < databaseFinalizeRetryCount-1 {
			lastErr = err
			time.Sleep(databaseFinalizeRetryDelay)
		} else {
			lastErr = err
		}
	}
	return fmt.Errorf("finalize failed database job: %w", lastErr)
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
		processingToken, err := newDatabaseProcessingToken()
		if err != nil {
			_ = tx.Rollback()
			return nil, err
		}
		update := d.rebind(`UPDATE queue_jobs
		SET state='processing', processing_started_at=?, processing_token=?, updated_at=?
		WHERE id=? AND state='pending'`)
		res, err := tx.ExecContext(ctx, update, now, processingToken, now, job.id)
		if err != nil {
			_ = tx.Rollback()
			return nil, err
		}
		rows, err := res.RowsAffected()
		if err != nil {
			_ = tx.Rollback()
			return nil, fmt.Errorf("read database claim rows: %w", err)
		}
		if rows == 0 {
			_ = tx.Rollback()
			continue
		}
		if rows != 1 {
			_ = tx.Rollback()
			return nil, fmt.Errorf("database claim affected %d rows, want 1", rows)
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		job.processingToken = processingToken
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
	SET state='pending', available_at=?, processing_started_at=NULL,
	processing_token=CASE WHEN processing_token IS NOT NULL AND processing_token <> '' THEN processing_token ELSE ? END,
	updated_at=?, last_error=?
WHERE state='processing' AND processing_started_at IS NOT NULL AND (
    (timeout_seconds IS NOT NULL AND timeout_seconds > 0 AND (processing_started_at + (timeout_seconds * 1000) + ?) <= ?)
    OR
    ((timeout_seconds IS NULL OR timeout_seconds <= 0) AND processing_started_at <= ?)
)`)
	res, err := d.db.ExecContext(
		ctx,
		query,
		nowMillis,
		databaseRecoveryMarker,
		nowMillis,
		databaseRecoveryDiagnostic,
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
				queuecore.SafeObserve(ctx, d.observer, queue.Event{
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
	query := `SELECT id, queue_name, job_type, payload, metadata_json, timeout_seconds, max_retry, backoff_millis, attempt,
	processing_token
FROM queue_jobs
WHERE queue_name=? AND state='pending' AND available_at <= ?
ORDER BY id ASC
LIMIT 1`
	if !d.usesOptimisticClaimLoop() {
		query += ` FOR UPDATE SKIP LOCKED`
	}
	query = d.rebind(query)
	row := tx.QueryRowContext(ctx, query, d.cfg.DefaultQueue, now)
	job := &dbJob{}
	var pendingProcessingToken sql.NullString
	if err := row.Scan(
		&job.id,
		&job.queueName,
		&job.jobType,
		&job.payload,
		&job.metadataJSON,
		&job.timeoutSeconds,
		&job.maxRetry,
		&job.backoffMillis,
		&job.attempt,
		&pendingProcessingToken,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	job.recoveryToken, job.recovered = databaseRecoveryProof(pendingProcessingToken)
	return job, nil
}

// databaseRecoveryProof recognizes only transport-owned state and returns the
// earlier processing generation when that opaque identity survived recovery.
func databaseRecoveryProof(processingToken sql.NullString) (string, bool) {
	if !processingToken.Valid {
		return "", false
	}
	if processingToken.String == databaseRecoveryMarker {
		return "", true
	}
	if !databaseProcessingTokenValid(processingToken.String) {
		return "", false
	}
	return processingToken.String, true
}

func (d *databaseQueue) usesOptimisticClaimLoop() bool {
	return d.cfg.DriverName == "sqlite"
}

// markDone deletes exactly the processing row owned by one successful delivery.
func (d *databaseQueue) markDone(ctx context.Context, job *dbJob) error {
	id, processingToken, err := databaseProcessingClaim(job)
	if err != nil {
		return err
	}
	query := d.rebind(`DELETE FROM queue_jobs WHERE id=? AND state='processing' AND processing_token=?`)
	result, err := d.db.ExecContext(ctx, query, id, processingToken)
	if err != nil {
		return err
	}
	return requireDatabaseSettlementRow(result)
}

// markFailed writes exactly one retryable or terminal delivery transition.
func (d *databaseQueue) markFailed(ctx context.Context, job *dbJob, runErr error) error {
	id, processingToken, err := databaseProcessingClaim(job)
	if err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	settlement, err := classifyDatabaseFailure(job, runErr, now)
	if err != nil {
		return err
	}
	if settlement.state == "dead" {
		query := d.rebind(`UPDATE queue_jobs
		SET state='dead', attempt=?, processing_started_at=NULL, processing_token=NULL, last_error=?, updated_at=?
		WHERE id=? AND state='processing' AND processing_token=?`)
		result, err := d.db.ExecContext(ctx, query, settlement.attempt, runErr.Error(), now, id, processingToken)
		if err != nil {
			return err
		}
		return requireDatabaseSettlementRow(result)
	}
	query := d.rebind(`UPDATE queue_jobs
	SET state='pending', attempt=?, available_at=?, last_error=?, processing_started_at=NULL, processing_token=?, updated_at=?
	WHERE id=? AND state='processing' AND processing_token=?`)
	result, err := d.db.ExecContext(ctx, query, settlement.attempt, settlement.availableAt, runErr.Error(), databasePendingRecoveryToken(job, settlement), now, id, processingToken)
	if err != nil {
		return err
	}
	return requireDatabaseSettlementRow(result)
}

// restoreRecoveredSettlementLineage immediately returns an unsuccessfully
// finalized recovery delivery to the pending set without replacing the receipt
// owner's inherited generation or advancing the application attempt.
func (d *databaseQueue) restoreRecoveredSettlementLineage(ctx context.Context, job *dbJob, settlementErr error) error {
	query := d.rebind(`UPDATE queue_jobs
	SET state='pending', available_at=?, processing_started_at=NULL,
	processing_token=?, last_error=?, updated_at=?
	WHERE id=? AND state='processing' AND processing_token=? AND attempt=?`)
	now := time.Now()
	availableAt := now.Add(databaseSettlementRecoveryDelay(d.cfg.PollInterval))
	return restoreDatabaseSettlementLineage(ctx, d.db, query, job, settlementErr, availableAt.UnixMilli(), now.UnixMilli())
}

// restoreDatabaseSettlementLineage applies the fenced repair through an
// injectable executor so ownership, attempt, and no-op cases can be tested
// without weakening the databaseQueue's concrete connection contract.
func restoreDatabaseSettlementLineage(ctx context.Context, execer databaseExecer, query string, job *dbJob, settlementErr error, availableAtMillis, nowMillis int64) error {
	recoveryToken, repair, err := databaseSettlementRecoveryToken(job)
	if err != nil || !repair {
		return err
	}
	if execer == nil {
		return errors.New("database settlement recovery executor is nil")
	}
	if settlementErr == nil {
		return errors.New("database settlement recovery error is nil")
	}
	id, processingToken, err := databaseProcessingClaim(job)
	if err != nil {
		return err
	}
	result, err := execer.ExecContext(ctx, query, availableAtMillis, recoveryToken, settlementErr.Error(), nowMillis, id, processingToken, job.attempt)
	if err != nil {
		return err
	}
	return requireDatabaseSettlementRow(result)
}

// databaseSettlementRecoveryDelay prevents a persistent physical-settlement
// fault from immediately reclaiming the same repaired row in a tight loop.
func databaseSettlementRecoveryDelay(pollInterval time.Duration) time.Duration {
	if pollInterval > databaseFinalizeRetryDelay {
		return pollInterval
	}
	return databaseFinalizeRetryDelay
}

// databaseSettlementRecoveryToken selects inherited recovery proof only when
// the current generation did not itself commit application state.
func databaseSettlementRecoveryToken(job *dbJob) (sql.NullString, bool, error) {
	if job == nil || !job.recovered || job.applicationStateCommitted {
		return sql.NullString{}, false, nil
	}
	if job.recoveryToken == "" {
		return sql.NullString{String: databaseRecoveryMarker, Valid: true}, true, nil
	}
	if !databaseProcessingTokenValid(job.recoveryToken) {
		return sql.NullString{}, false, fmt.Errorf("recovered database settlement generation %q is invalid", job.recoveryToken)
	}
	return sql.NullString{String: job.recoveryToken, Valid: true}, true, nil
}

// databasePendingRecoveryToken preserves the current generation after it
// durably mutates application state; otherwise it retains inherited recovery
// proof only across same-attempt infrastructure redelivery.
func databasePendingRecoveryToken(job *dbJob, settlement databaseFailureSettlement) sql.NullString {
	if job == nil || settlement.state != "pending" || settlement.attempt != job.attempt {
		return sql.NullString{}
	}
	if job.applicationStateCommitted {
		if !databaseProcessingTokenValid(job.processingToken) {
			return sql.NullString{}
		}
		return sql.NullString{String: job.processingToken, Valid: true}
	}
	if !job.recovered {
		return sql.NullString{}
	}
	if job.recoveryToken == "" {
		return sql.NullString{String: databaseRecoveryMarker, Valid: true}
	}
	if !databaseProcessingTokenValid(job.recoveryToken) {
		return sql.NullString{}
	}
	return sql.NullString{String: job.recoveryToken, Valid: true}
}

// databaseProcessingClaim returns the fenced identity required to settle one exact processing generation.
func databaseProcessingClaim(job *dbJob) (int64, string, error) {
	if job == nil {
		return 0, "", fmt.Errorf("database settlement job is nil")
	}
	if job.id <= 0 {
		return 0, "", fmt.Errorf("database settlement job id must be positive")
	}
	if job.processingToken == "" {
		return 0, "", fmt.Errorf("database settlement processing token is empty")
	}
	return job.id, job.processingToken, nil
}

// newDatabaseProcessingToken creates one opaque physical generation identity
// that also fences settlement updates for the current claim.
func newDatabaseProcessingToken() (string, error) {
	var token [databaseProcessingTokenBytes]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", fmt.Errorf("create database processing token: %w", err)
	}
	return hex.EncodeToString(token[:]), nil
}

// databaseProcessingTokenValid accepts only the canonical lowercase encoding
// generated for fenced SQL claims.
func databaseProcessingTokenValid(token string) bool {
	if len(token) != databaseProcessingTokenBytes*2 || token != strings.ToLower(token) {
		return false
	}
	decoded, err := hex.DecodeString(token)
	if err != nil || len(decoded) != databaseProcessingTokenBytes {
		return false
	}
	return true
}

// requireDatabaseSettlementRow rejects stale or lost finalization updates that cannot prove ownership of one delivery.
func requireDatabaseSettlementRow(result sql.Result) error {
	if result == nil {
		return fmt.Errorf("database settlement returned no result")
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read database settlement rows: %w", err)
	}
	if rows != 1 {
		return fmt.Errorf("database settlement affected %d rows, want 1", rows)
	}
	return nil
}

// observeSettlementFailure emits the physical delivery identity whose durable row could not be finalized.
func (d *databaseQueue) observeSettlementFailure(ctx context.Context, job *dbJob, err error) {
	if job == nil {
		return
	}
	metadata := queue.ResolveObservedJobMetadataFromJob(databaseDeliveryJob(job))
	queuecore.SafeObserve(ctx, d.observer, queue.Event{
		Kind:       queue.EventSettlementFailed,
		Driver:     queue.DriverDatabase,
		Queue:      queuecore.NormalizeQueueName(job.queueName),
		JobType:    metadata.JobType,
		JobKey:     metadata.JobKey,
		DispatchID: metadata.DispatchID,
		JobID:      metadata.JobID,
		ChainID:    metadata.ChainID,
		BatchID:    metadata.BatchID,
		Attempt:    job.attempt,
		MaxRetry:   job.maxRetry,
		Err:        err,
		Time:       time.Now(),
	})
}

// classifyDatabaseFailure derives the durable state transition from the physical attempt and handler result.
func classifyDatabaseFailure(job *dbJob, runErr error, now int64) (databaseFailureSettlement, error) {
	decision := busruntime.ClassifyAttempt(busruntime.DeliveryAttempt{
		Number:   job.attempt,
		MaxRetry: job.maxRetry,
	}, runErr)

	switch decision {
	case busruntime.AttemptRetry:
		return databasePendingSettlement(job, job.attempt+1, now), nil
	case busruntime.AttemptFailed:
		return databaseFailureSettlement{state: "dead", attempt: job.attempt + 1}, nil
	case busruntime.AttemptRedeliver:
		return databasePendingSettlement(job, job.attempt, now), nil
	default:
		return databaseFailureSettlement{}, fmt.Errorf("cannot persist a successful attempt as failed")
	}
}

// databasePendingSettlement applies configured backoff without deciding whether the application retry counter advances.
func databasePendingSettlement(job *dbJob, attempt int, now int64) databaseFailureSettlement {
	availableAt := now
	if job.backoffMillis > 0 {
		availableAt += job.backoffMillis
	}
	return databaseFailureSettlement{
		state:       "pending",
		attempt:     attempt,
		availableAt: availableAt,
	}
}

// acquireUnique claims the canonical key inside the queue-row transaction using the database clock shared by every producer.
func (d *databaseQueue) acquireUnique(ctx context.Context, tx *sql.Tx, job queue.Job, queueName string, ttl time.Duration) (bool, error) {
	now, err := d.databaseNowMillis(ctx, tx)
	if err != nil {
		return false, err
	}
	if d.uniqueClaims.Add(1)%databaseUniquePruneInterval == 0 {
		if err := d.pruneExpiredUniqueLocks(ctx, tx, now); err != nil {
			return false, err
		}
	}
	ttlMillis := ttl.Milliseconds()
	if ttlMillis < 1 {
		ttlMillis = 1
	}
	return d.acquireUniqueKey(ctx, tx, uniqueJobKey(job, queueName), now, now+ttlMillis)
}

// uniqueJobKey preserves the shared versioned identity verbatim for diagnosable SQL state.
func uniqueJobKey(job queue.Job, queueName string) string {
	return queuecore.UniqueKey(job, queueName)
}

// acquireUniqueKey couples one lock claim to the surrounding queue-row transaction.
func (d *databaseQueue) acquireUniqueKey(ctx context.Context, tx *sql.Tx, key string, now, expiresAt int64) (bool, error) {
	insert := `INSERT INTO queue_unique_locks(lock_key, expires_at) VALUES(?, ?) ON CONFLICT(lock_key) DO NOTHING`
	if d.cfg.DriverName != "pgx" && d.cfg.DriverName != "postgres" && d.cfg.DriverName != "sqlite" {
		insert = `INSERT IGNORE INTO queue_unique_locks(lock_key, expires_at) VALUES(?, ?)`
	}
	res, err := tx.ExecContext(ctx, d.rebind(insert), key, expiresAt)
	if err != nil {
		return false, err
	}
	if rows, rowsErr := res.RowsAffected(); rowsErr == nil && rows == 1 {
		return true, nil
	}
	update := d.rebind(`UPDATE queue_unique_locks SET expires_at=? WHERE lock_key=? AND expires_at <= ?`)
	res, err = tx.ExecContext(ctx, update, expiresAt, key, now)
	if err != nil {
		return false, err
	}
	rows, err := res.RowsAffected()
	return rows == 1, err
}

// databaseNowMillis reads the backend clock so producer clock skew cannot shorten or extend distributed claims.
func (d *databaseQueue) databaseNowMillis(ctx context.Context, queryer databaseRowQueryer) (int64, error) {
	query := `SELECT CAST(UNIX_TIMESTAMP(CURRENT_TIMESTAMP(3)) * 1000 AS SIGNED)`
	switch d.cfg.DriverName {
	case "pgx", "postgres":
		query = `SELECT CAST(EXTRACT(EPOCH FROM clock_timestamp()) * 1000 AS BIGINT)`
	case "sqlite":
		query = `SELECT CAST((julianday('now') - 2440587.5) * 86400000 AS INTEGER)`
	}
	var now int64
	if err := queryer.QueryRowContext(ctx, query).Scan(&now); err != nil {
		return 0, fmt.Errorf("read database time for uniqueness: %w", err)
	}
	return now, nil
}

// pruneExpiredUniqueLocks bounds persistent identity state without touching live claims.
func (d *databaseQueue) pruneExpiredUniqueLocks(ctx context.Context, execer databaseExecer, now int64) error {
	query := d.rebind(`DELETE FROM queue_unique_locks WHERE expires_at <= ?`)
	if _, err := execer.ExecContext(ctx, query, now); err != nil {
		return fmt.Errorf("prune expired uniqueness claims: %w", err)
	}
	return nil
}

func (d *databaseQueue) ensureSchema(ctx context.Context) error {
	stmts := d.schemaStatements()
	for _, stmt := range stmts {
		if _, err := d.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("ensure queue schema failed: %w", err)
		}
	}
	if err := d.ensureProcessingTokenColumn(ctx); err != nil {
		return err
	}
	if err := d.ensureMetadataJSONColumn(ctx); err != nil {
		return err
	}
	if d.cfg.DriverName == "mysql" {
		if err := d.ensureMySQLUniqueExpiryIndex(ctx); err != nil {
			return err
		}
	}
	now, err := d.databaseNowMillis(ctx, d.db)
	if err != nil {
		return err
	}
	return d.pruneExpiredUniqueLocks(ctx, d.db, now)
}

// ensureProcessingTokenColumn upgrades existing queue tables additively while nullable storage keeps older binaries and rows readable.
func (d *databaseQueue) ensureProcessingTokenColumn(ctx context.Context) error {
	exists, err := d.processingTokenColumnExists(ctx)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	statement := `ALTER TABLE queue_jobs ADD COLUMN processing_token VARCHAR(64) NULL`
	switch d.cfg.DriverName {
	case "pgx", "postgres", "sqlite":
		statement = `ALTER TABLE queue_jobs ADD COLUMN processing_token TEXT NULL`
	}
	if _, err := d.db.ExecContext(ctx, statement); err != nil {
		// Concurrent startup may observe an already-completed additive migration after its own ALTER loses the race.
		exists, checkErr := d.processingTokenColumnExists(ctx)
		if checkErr == nil && exists {
			return nil
		}
		return fmt.Errorf("ensure database processing token column: %w", err)
	}
	return nil
}

// processingTokenColumnExists inspects the active dialect without relying on non-portable ALTER TABLE guards.
func (d *databaseQueue) processingTokenColumnExists(ctx context.Context) (bool, error) {
	return d.queueJobColumnExists(ctx, "processing_token")
}

// ensureMetadataJSONColumn upgrades legacy queue tables before direct jobs can
// rely on out-of-payload correlation surviving a durable delivery.
func (d *databaseQueue) ensureMetadataJSONColumn(ctx context.Context) error {
	exists, err := d.metadataJSONColumnExists(ctx)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	if _, err := d.db.ExecContext(ctx, `ALTER TABLE queue_jobs ADD COLUMN metadata_json TEXT NULL`); err != nil {
		// Concurrent startup may observe an already-completed additive migration after its own ALTER loses the race.
		exists, checkErr := d.metadataJSONColumnExists(ctx)
		if checkErr == nil && exists {
			return nil
		}
		return fmt.Errorf("ensure database job metadata column: %w", err)
	}
	return nil
}

// requireManagedQueueSchema keeps readiness and worker startup aligned with
// every table and column that runtime SQL can touch without performing DDL.
func (d *databaseQueue) requireManagedQueueSchema(ctx context.Context) error {
	tableExists, err := d.queueJobsTableExists(ctx)
	if err != nil {
		return fmt.Errorf("validate caller-managed queue_jobs table: %w", err)
	}
	if !tableExists {
		return fmt.Errorf("caller-managed schema is missing required queue_jobs table")
	}
	jobColumns, err := d.managedQueueTableColumns(ctx, managedQueueJobsTable)
	if err != nil {
		return fmt.Errorf("validate caller-managed queue_jobs columns: %w", err)
	}
	for _, columnName := range managedQueueJobColumns {
		if _, exists := jobColumns[columnName]; !exists {
			return fmt.Errorf("caller-managed queue_jobs schema is missing required %s column", columnName)
		}
	}

	tableExists, err = d.queueUniqueLocksTableExists(ctx)
	if err != nil {
		return fmt.Errorf("validate caller-managed queue_unique_locks table: %w", err)
	}
	if !tableExists {
		return fmt.Errorf("caller-managed schema is missing required queue_unique_locks table")
	}
	uniqueLockColumns, err := d.managedQueueTableColumns(ctx, managedQueueUniqueLocksTable)
	if err != nil {
		return fmt.Errorf("validate caller-managed queue_unique_locks columns: %w", err)
	}
	for _, columnName := range managedQueueUniqueLockColumns {
		if _, exists := uniqueLockColumns[columnName]; !exists {
			return fmt.Errorf("caller-managed queue_unique_locks schema is missing required %s column", columnName)
		}
	}
	return nil
}

// queueJobsTableExists reports whether the caller installed the durable job table.
func (d *databaseQueue) queueJobsTableExists(ctx context.Context) (bool, error) {
	return d.managedQueueTableExists(ctx, managedQueueJobsTable)
}

// queueUniqueLocksTableExists reports whether the caller installed the distributed uniqueness table.
func (d *databaseQueue) queueUniqueLocksTableExists(ctx context.Context) (bool, error) {
	return d.managedQueueTableExists(ctx, managedQueueUniqueLocksTable)
}

// managedQueueTableExists inspects one trusted runtime table name through the active dialect.
func (d *databaseQueue) managedQueueTableExists(ctx context.Context, tableName managedQueueTable) (bool, error) {
	var count int
	switch d.cfg.DriverName {
	case "sqlite":
		err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, string(tableName)).Scan(&count)
		return count > 0, err
	case "pgx", "postgres":
		err := d.db.QueryRowContext(ctx, d.rebind(`SELECT COUNT(*) FROM pg_class WHERE oid = to_regclass(?) AND relkind IN ('r', 'p')`), string(tableName)).Scan(&count)
		return count > 0, err
	default:
		err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = ? AND table_type = 'BASE TABLE'`, string(tableName)).Scan(&count)
		return count > 0, err
	}
}

// managedQueueTableColumns reads one complete catalog snapshot so frequent
// readiness checks do not issue a separate database roundtrip per field.
func (d *databaseQueue) managedQueueTableColumns(ctx context.Context, tableName managedQueueTable) (map[string]struct{}, error) {
	query := `SELECT column_name
	FROM information_schema.columns
	WHERE table_schema = DATABASE() AND table_name = ?`
	switch d.cfg.DriverName {
	case "sqlite":
		query = `SELECT name FROM pragma_table_info(?)`
	case "pgx", "postgres":
		query = `SELECT attname
			FROM pg_attribute
			WHERE attrelid = to_regclass(?) AND attnum > 0 AND NOT attisdropped`
	}
	rows, err := d.db.QueryContext(ctx, d.rebind(query), string(tableName))
	if err != nil {
		return nil, fmt.Errorf("inspect %s columns: %w", tableName, err)
	}
	defer rows.Close()

	columns := make(map[string]struct{})
	for rows.Next() {
		var columnName string
		if err := rows.Scan(&columnName); err != nil {
			return nil, fmt.Errorf("scan %s column: %w", tableName, err)
		}
		columns[columnName] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("inspect %s columns: %w", tableName, err)
	}
	return columns, nil
}

// metadataJSONColumnExists reports whether direct-delivery metadata has an
// additive persistence slot in the active queue table.
func (d *databaseQueue) metadataJSONColumnExists(ctx context.Context) (bool, error) {
	return d.queueJobColumnExists(ctx, "metadata_json")
}

// queueJobColumnExists inspects one trusted queue_jobs column name through the
// active dialect without depending on non-portable ALTER TABLE guards.
func (d *databaseQueue) queueJobColumnExists(ctx context.Context, columnName string) (bool, error) {
	if d.cfg.DriverName == "sqlite" {
		rows, err := d.db.QueryContext(ctx, `PRAGMA table_info(queue_jobs)`)
		if err != nil {
			return false, fmt.Errorf("inspect sqlite queue job column %q: %w", columnName, err)
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
				return false, fmt.Errorf("scan sqlite queue column: %w", err)
			}
			if name == columnName {
				return true, nil
			}
		}
		if err := rows.Err(); err != nil {
			return false, fmt.Errorf("inspect sqlite queue columns: %w", err)
		}
		return false, nil
	}

	query := `SELECT COUNT(*)
	FROM information_schema.columns
	WHERE table_schema = DATABASE() AND table_name = 'queue_jobs' AND column_name = ?`
	if d.cfg.DriverName == "pgx" || d.cfg.DriverName == "postgres" {
		query = `SELECT COUNT(*)
			FROM pg_attribute
			WHERE attrelid = to_regclass('queue_jobs') AND attname = ? AND NOT attisdropped`
	}
	var count int
	if err := d.db.QueryRowContext(ctx, d.rebind(query), columnName).Scan(&count); err != nil {
		return false, fmt.Errorf("inspect database queue job column %q: %w", columnName, err)
	}
	return count > 0, nil
}

// ensureMySQLUniqueExpiryIndex migrates existing lock tables whose original CREATE TABLE predates expiry pruning.
func (d *databaseQueue) ensureMySQLUniqueExpiryIndex(ctx context.Context) error {
	exists, err := d.mysqlIndexExists(ctx, "idx_queue_unique_locks_expires")
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	if _, err := d.db.ExecContext(ctx, `ALTER TABLE queue_unique_locks ADD INDEX idx_queue_unique_locks_expires (expires_at)`); err != nil {
		// Multiple producers may migrate concurrently, so a successful peer wins even if this ALTER observed the race.
		exists, checkErr := d.mysqlIndexExists(ctx, "idx_queue_unique_locks_expires")
		if checkErr == nil && exists {
			return nil
		}
		return fmt.Errorf("ensure mysql uniqueness expiry index: %w", err)
	}
	return nil
}

// mysqlIndexExists checks the active schema instead of relying on version-specific CREATE INDEX syntax.
func (d *databaseQueue) mysqlIndexExists(ctx context.Context, indexName string) (bool, error) {
	const query = `SELECT COUNT(*)
FROM information_schema.statistics
WHERE table_schema = DATABASE() AND table_name = 'queue_unique_locks' AND index_name = ?`
	var count int
	if err := d.db.QueryRowContext(ctx, query, indexName).Scan(&count); err != nil {
		return false, fmt.Errorf("inspect mysql uniqueness expiry index: %w", err)
	}
	return count > 0, nil
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
                metadata_json TEXT NULL,
                timeout_seconds BIGINT NULL,
                max_retry INTEGER NOT NULL DEFAULT 0,
                backoff_millis BIGINT NOT NULL DEFAULT 0,
                attempt INTEGER NOT NULL DEFAULT 0,
				available_at BIGINT NOT NULL,
				processing_started_at BIGINT NULL,
				processing_token TEXT NULL,
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
			`CREATE INDEX IF NOT EXISTS idx_queue_unique_locks_expires ON queue_unique_locks(expires_at)`,
		}
	case "sqlite":
		return []string{
			`CREATE TABLE IF NOT EXISTS queue_jobs (
                id INTEGER PRIMARY KEY AUTOINCREMENT,
                queue_name TEXT NOT NULL,
                job_type TEXT NOT NULL,
                payload BLOB NOT NULL,
                metadata_json TEXT NULL,
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
            )`,
			`CREATE INDEX IF NOT EXISTS idx_queue_jobs_ready ON queue_jobs(state, available_at, id)`,
			`CREATE TABLE IF NOT EXISTS queue_unique_locks (
                lock_key TEXT PRIMARY KEY,
                expires_at INTEGER NOT NULL
            )`,
			`CREATE INDEX IF NOT EXISTS idx_queue_unique_locks_expires ON queue_unique_locks(expires_at)`,
		}
	default:
		return []string{
			`CREATE TABLE IF NOT EXISTS queue_jobs (
                id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
                queue_name VARCHAR(191) NOT NULL,
                job_type VARCHAR(191) NOT NULL,
                payload LONGBLOB NOT NULL,
                metadata_json TEXT NULL,
                timeout_seconds BIGINT NULL,
                max_retry INT NOT NULL DEFAULT 0,
                backoff_millis BIGINT NOT NULL DEFAULT 0,
                attempt INT NOT NULL DEFAULT 0,
				available_at BIGINT NOT NULL,
				processing_started_at BIGINT NULL,
				processing_token VARCHAR(64) NULL,
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
