package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// mysqlWorkflowIdentifierLimit keeps both columns of each composite primary
// key below MySQL's portable InnoDB index budget while retaining exact bytes.
const mysqlWorkflowIdentifierLimit = 255

// mysqlWorkflowCallbackKeyLimit accommodates a callback-kind prefix plus the
// maximum workflow identifier without weakening exact-key comparison.
const mysqlWorkflowCallbackKeyLimit = 512

// SQLStoreConfig configures connection ownership, dialect binding, and schema setup for a SQL store.
type SQLStoreConfig struct {
	DB          *sql.DB
	DriverName  string
	DSN         string
	AutoMigrate bool
}

// NewSQLStore creates a SQL-backed orchestration store.
func NewSQLStore(cfg SQLStoreConfig) (Store, error) {
	if cfg.DB == nil {
		if cfg.DriverName == "" {
			return nil, fmt.Errorf("sql store driver name is required")
		}
		if cfg.DSN == "" {
			return nil, fmt.Errorf("sql store dsn is required")
		}
		db, err := sql.Open(cfg.DriverName, cfg.DSN)
		if err != nil {
			return nil, err
		}
		cfg.DB = db
	}
	if cfg.DriverName == "" {
		cfg.DriverName = "sqlite"
	}
	if !cfg.AutoMigrate {
		cfg.AutoMigrate = true
	}
	return &sqlStore{
		db:          cfg.DB,
		driverName:  cfg.DriverName,
		autoMigrate: cfg.AutoMigrate,
	}, nil
}

type sqlStore struct {
	db            *sql.DB
	driverName    string
	autoMigrate   bool
	mysqlKeyLimit mysqlWorkflowKeyLimits

	ensureOnce sync.Once
	ensureErr  error
}

var _ Store = (*sqlStore)(nil)

type mysqlColumnCapacity struct {
	dataType   string
	characters int64
	bytes      int64
}

type mysqlWorkflowKeyLimits struct {
	chainID   mysqlColumnCapacity
	chainNode mysqlColumnCapacity
	batchID   mysqlColumnCapacity
	batchJob  mysqlColumnCapacity
	callback  mysqlColumnCapacity
}

// ensureSchema runs schema creation once so concurrent first use cannot race the DDL sequence.
func (s *sqlStore) ensureSchema(ctx context.Context) error {
	s.ensureOnce.Do(func() {
		if !s.autoMigrate {
			if s.driverName == "mysql" {
				s.mysqlKeyLimit, s.ensureErr = s.loadMySQLWorkflowKeyLimits(ctx)
			}
			return
		}
		stmts := s.schemaStatements()
		for _, stmt := range stmts {
			if _, err := s.db.ExecContext(ctx, s.rebind(stmt)); err != nil {
				s.ensureErr = err
				return
			}
		}
		if s.driverName == "mysql" {
			s.mysqlKeyLimit, s.ensureErr = s.loadMySQLWorkflowKeyLimits(ctx)
		}
	})
	return s.ensureErr
}

// schemaStatements selects only the physical SQL types that differ by
// dialect while keeping one legacy table and column contract.
func (s *sqlStore) schemaStatements() []string {
	idType := "TEXT"
	callbackKeyType := "TEXT"
	payloadType := "BLOB"
	switch s.driverName {
	case "pgx", "postgres":
		payloadType = "BYTEA"
	case "mysql":
		idType = fmt.Sprintf("VARBINARY(%d)", mysqlWorkflowIdentifierLimit)
		callbackKeyType = fmt.Sprintf("VARBINARY(%d)", mysqlWorkflowCallbackKeyLimit)
		payloadType = "LONGBLOB"
	}
	return []string{
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS bus_chains (
				chain_id %s PRIMARY KEY,
				dispatch_id TEXT NOT NULL,
				queue_name TEXT NOT NULL,
				nodes_json %s NOT NULL,
				next_index INTEGER NOT NULL,
				completed INTEGER NOT NULL,
				failed INTEGER NOT NULL,
				failure TEXT NOT NULL,
				created_at_ms BIGINT NOT NULL,
				updated_at_ms BIGINT NOT NULL
			)`, idType, payloadType),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS bus_chain_completed_nodes (
				chain_id %s NOT NULL,
				node_id %s NOT NULL,
				created_at_ms BIGINT NOT NULL,
				PRIMARY KEY (chain_id, node_id)
			)`, idType, idType),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS bus_batches (
				batch_id %s PRIMARY KEY,
				dispatch_id TEXT NOT NULL,
				name TEXT NOT NULL,
				queue_name TEXT NOT NULL,
				allow_failed INTEGER NOT NULL,
				total_jobs INTEGER NOT NULL,
				pending_jobs INTEGER NOT NULL,
				processed_jobs INTEGER NOT NULL,
				failed_jobs INTEGER NOT NULL,
				cancelled INTEGER NOT NULL,
				completed INTEGER NOT NULL,
				created_at_ms BIGINT NOT NULL,
				updated_at_ms BIGINT NOT NULL
			)`, idType),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS bus_batch_jobs (
				batch_id %s NOT NULL,
				job_id %s NOT NULL,
				started INTEGER NOT NULL,
				done INTEGER NOT NULL,
				failed INTEGER NOT NULL,
				PRIMARY KEY (batch_id, job_id)
			)`, idType, idType),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS bus_callback_invocations (
				callback_key %s PRIMARY KEY,
				created_at_ms BIGINT NOT NULL
			)`, callbackKeyType),
	}
}

// loadMySQLWorkflowKeyLimits derives validation from the connected schema so
// wider caller-managed binary columns retain their established capacity.
func (s *sqlStore) loadMySQLWorkflowKeyLimits(ctx context.Context) (mysqlWorkflowKeyLimits, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT table_name, column_name, data_type, character_maximum_length, character_octet_length
		FROM information_schema.columns
		WHERE table_schema=DATABASE() AND (
			(table_name='bus_chains' AND column_name='chain_id') OR
			(table_name='bus_chain_completed_nodes' AND column_name IN ('chain_id', 'node_id')) OR
			(table_name='bus_batches' AND column_name='batch_id') OR
			(table_name='bus_batch_jobs' AND column_name IN ('batch_id', 'job_id')) OR
			(table_name='bus_callback_invocations' AND column_name='callback_key')
		)`)
	if err != nil {
		return mysqlWorkflowKeyLimits{}, err
	}
	defer rows.Close()
	columns := make(map[string]mysqlColumnCapacity, 7)
	for rows.Next() {
		var (
			tableName, columnName, dataType string
			characters, bytes               sql.NullInt64
		)
		if err := rows.Scan(&tableName, &columnName, &dataType, &characters, &bytes); err != nil {
			return mysqlWorkflowKeyLimits{}, err
		}
		if !characters.Valid || !bytes.Valid || characters.Int64 <= 0 || bytes.Int64 <= 0 {
			return mysqlWorkflowKeyLimits{}, fmt.Errorf("MySQL workflow column %s.%s has no character capacity", tableName, columnName)
		}
		columns[tableName+"."+columnName] = mysqlColumnCapacity{dataType: dataType, characters: characters.Int64, bytes: bytes.Int64}
	}
	if err := rows.Err(); err != nil {
		return mysqlWorkflowKeyLimits{}, err
	}
	return mysqlWorkflowKeyLimitsFromColumns(columns)
}

// mysqlWorkflowKeyLimitsFromColumns intersects IDs stored in multiple tables
// and rejects schemas whose comparison semantics can conflate identities.
func mysqlWorkflowKeyLimitsFromColumns(columns map[string]mysqlColumnCapacity) (mysqlWorkflowKeyLimits, error) {
	required := []string{
		"bus_chains.chain_id",
		"bus_chain_completed_nodes.chain_id",
		"bus_chain_completed_nodes.node_id",
		"bus_batches.batch_id",
		"bus_batch_jobs.batch_id",
		"bus_batch_jobs.job_id",
		"bus_callback_invocations.callback_key",
	}
	for _, column := range required {
		capacity, ok := columns[column]
		if !ok || capacity.characters <= 0 || capacity.bytes <= 0 {
			return mysqlWorkflowKeyLimits{}, fmt.Errorf("MySQL workflow schema is missing key capacity for %s", column)
		}
		if !strings.EqualFold(capacity.dataType, "varbinary") {
			return mysqlWorkflowKeyLimits{}, fmt.Errorf("MySQL workflow column %s must use VARBINARY for byte-exact identity; found %s", column, capacity.dataType)
		}
	}
	return mysqlWorkflowKeyLimits{
		chainID:   intersectMySQLColumnCapacity(columns["bus_chains.chain_id"], columns["bus_chain_completed_nodes.chain_id"]),
		chainNode: columns["bus_chain_completed_nodes.node_id"],
		batchID:   intersectMySQLColumnCapacity(columns["bus_batches.batch_id"], columns["bus_batch_jobs.batch_id"]),
		batchJob:  columns["bus_batch_jobs.job_id"],
		callback:  columns["bus_callback_invocations.callback_key"],
	}, nil
}

// intersectMySQLColumnCapacity uses the narrowest representation because one
// logical ID must round-trip through every column in which it participates.
func intersectMySQLColumnCapacity(left, right mysqlColumnCapacity) mysqlColumnCapacity {
	capacity := left
	if right.characters < capacity.characters {
		capacity.characters = right.characters
	}
	if right.bytes < capacity.bytes {
		capacity.bytes = right.bytes
	}
	return capacity
}

// validateMySQLKey rejects values the connected schema could truncate into a
// false duplicate while allowing wider established schemas to keep working.
func (s *sqlStore) validateMySQLKey(label, value string, capacity mysqlColumnCapacity) error {
	if s.driverName != "mysql" {
		return nil
	}
	if int64(len(value)) > capacity.bytes {
		return fmt.Errorf("%s exceeds MySQL schema limit of %d bytes", label, capacity.bytes)
	}
	if int64(len([]rune(value))) > capacity.characters {
		return fmt.Errorf("%s exceeds MySQL schema limit of %d characters", label, capacity.characters)
	}
	return nil
}

// CreateChain persists the complete encoded chain as one durable initial state.
func (s *sqlStore) CreateChain(ctx context.Context, rec ChainRecord) error {
	if err := validateChainRecord(rec); err != nil {
		return err
	}
	if err := s.ensureSchema(ctx); err != nil {
		return err
	}
	if err := s.validateMySQLKey("chain id", rec.ChainID, s.mysqlKeyLimit.chainID); err != nil {
		return err
	}
	for _, node := range rec.Nodes {
		if err := s.validateMySQLKey("chain node id", node.NodeID, s.mysqlKeyLimit.chainNode); err != nil {
			return err
		}
	}
	nodesJSON, err := json.Marshal(rec.Nodes)
	if err != nil {
		return err
	}
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = time.Now()
	}
	now := rec.CreatedAt.UnixMilli()
	_, err = s.db.ExecContext(ctx, s.rebind(`INSERT INTO bus_chains
		(chain_id, dispatch_id, queue_name, nodes_json, next_index, completed, failed, failure, created_at_ms, updated_at_ms)
		VALUES (?, ?, ?, ?, 0, 0, 0, '', ?, ?)`),
		rec.ChainID, rec.DispatchID, rec.Queue, nodesJSON, now, now,
	)
	return err
}

// AdvanceChain claims the completed node before atomically incrementing the
// parent so concurrent redelivery cannot return a stale successor.
func (s *sqlStore) AdvanceChain(ctx context.Context, chainID string, completedNode string) (next *ChainNode, done bool, err error) {
	state, err := s.GetChain(ctx, chainID)
	if err != nil {
		return nil, false, err
	}
	next, done, claimable, err := chainNodeAdvanceDisposition(state, completedNode)
	if err != nil || !claimable {
		return next, done, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := s.insertChainCompletedNode(ctx, tx, chainID, completedNode); err != nil {
		return nil, false, err
	}
	advancedAt := time.Now()
	result, err := tx.ExecContext(ctx, s.rebind(`UPDATE bus_chains
		SET next_index=next_index+1, updated_at_ms=?
		WHERE chain_id=? AND next_index=? AND completed=0 AND failed=0`), advancedAt.UnixMilli(), chainID, state.NextIndex)
	if err != nil {
		return nil, false, err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return nil, false, err
	}
	if updated > 1 {
		return nil, false, fmt.Errorf("chain %q advancement updated %d rows", chainID, updated)
	}
	if updated == 0 {
		if err := tx.Rollback(); err != nil {
			return nil, false, err
		}
		state, err := s.GetChain(ctx, chainID)
		if err != nil {
			return nil, false, err
		}
		next, done, claimable, err := chainNodeAdvanceDisposition(state, completedNode)
		if err != nil || !claimable {
			return next, done, err
		}
		return nil, false, fmt.Errorf("chain %q node %q could not claim success", chainID, completedNode)
	}

	state.NextIndex++
	state.UpdatedAt = advancedAt
	if state.NextIndex >= len(state.Nodes) {
		completedAt := time.Now()
		result, err := tx.ExecContext(ctx, s.rebind(`UPDATE bus_chains
			SET completed=1, updated_at_ms=?
			WHERE chain_id=? AND next_index=? AND completed=0 AND failed=0`), completedAt.UnixMilli(), chainID, state.NextIndex)
		if err != nil {
			return nil, false, err
		}
		completed, err := result.RowsAffected()
		if err != nil {
			return nil, false, err
		}
		if completed != 1 {
			return nil, false, fmt.Errorf("chain %q completion updated %d rows", chainID, completed)
		}
		state.Completed = true
		state.UpdatedAt = completedAt
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	if state.Completed {
		return nil, true, nil
	}
	n := state.Nodes[state.NextIndex]
	return &n, false, nil
}

// FailChainNode conditionally fails only the current node so a success that
// already advanced the chain cannot be reclassified by a late redelivery.
func (s *sqlStore) FailChainNode(ctx context.Context, chainID, nodeID string, cause error) (ChainState, bool, error) {
	state, err := s.GetChain(ctx, chainID)
	if err != nil {
		return ChainState{}, false, err
	}
	owned, claimable, err := chainNodeFailureDisposition(state, nodeID)
	if err != nil {
		return ChainState{}, false, err
	}
	if !claimable {
		return state, owned, nil
	}

	message := ""
	if cause != nil {
		message = cause.Error()
	}
	failedAt := time.Now()
	result, err := s.db.ExecContext(ctx, s.rebind(`UPDATE bus_chains
		SET failed=1, failure=?, updated_at_ms=?
		WHERE chain_id=? AND next_index=? AND completed=0 AND failed=0`), message, failedAt.UnixMilli(), chainID, state.NextIndex)
	if err != nil {
		return ChainState{}, false, err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return ChainState{}, false, err
	}
	if updated == 1 {
		state.Failed = true
		state.Failure = message
		state.UpdatedAt = failedAt
		return state, true, nil
	}
	if updated > 1 {
		return ChainState{}, false, fmt.Errorf("chain %q failure updated %d rows", chainID, updated)
	}

	state, err = s.GetChain(ctx, chainID)
	if err != nil {
		return ChainState{}, false, err
	}
	owned, claimable, err = chainNodeFailureDisposition(state, nodeID)
	if err != nil {
		return ChainState{}, false, err
	}
	if claimable {
		return ChainState{}, false, fmt.Errorf("chain %q node %q could not claim failure", chainID, nodeID)
	}
	return state, owned, nil
}

// FailChain preserves an already committed completion while recording a
// terminal cause only for an unfinished chain.
func (s *sqlStore) FailChain(ctx context.Context, chainID string, cause error) error {
	if err := s.ensureSchema(ctx); err != nil {
		return err
	}
	msg := ""
	if cause != nil {
		msg = cause.Error()
	}
	_, err := s.db.ExecContext(ctx, s.rebind(`UPDATE bus_chains SET failed=1, failure=?, updated_at_ms=? WHERE chain_id=? AND completed=0`), msg, time.Now().UnixMilli(), chainID)
	return err
}

// GetChain decodes the stored node payload and normalizes missing rows to ErrNotFound.
func (s *sqlStore) GetChain(ctx context.Context, chainID string) (ChainState, error) {
	if err := s.ensureSchema(ctx); err != nil {
		return ChainState{}, err
	}
	row := s.db.QueryRowContext(ctx, s.rebind(`SELECT dispatch_id, queue_name, nodes_json, next_index, completed, failed, failure, created_at_ms, updated_at_ms
		FROM bus_chains WHERE chain_id=?`), chainID)
	var (
		dispatchID, queueName, failure string
		nodesJSON                      []byte
		nextIndex, completed, failed   int
		createdMS, updatedMS           int64
	)
	if err := row.Scan(&dispatchID, &queueName, &nodesJSON, &nextIndex, &completed, &failed, &failure, &createdMS, &updatedMS); err != nil {
		if err == sql.ErrNoRows {
			return ChainState{}, ErrNotFound
		}
		return ChainState{}, err
	}
	var nodes []ChainNode
	if err := json.Unmarshal(nodesJSON, &nodes); err != nil {
		return ChainState{}, err
	}
	return ChainState{
		ChainID:    chainID,
		DispatchID: dispatchID,
		Queue:      queueName,
		Nodes:      nodes,
		NextIndex:  nextIndex,
		Completed:  completed == 1,
		Failed:     failed == 1,
		Failure:    failure,
		CreatedAt:  time.UnixMilli(createdMS),
		UpdatedAt:  time.UnixMilli(updatedMS),
	}, nil
}

// CreateBatch inserts aggregate and member rows in one transaction to prevent partial batches.
func (s *sqlStore) CreateBatch(ctx context.Context, rec BatchRecord) error {
	if err := validateBatchRecord(rec); err != nil {
		return err
	}
	if err := s.ensureSchema(ctx); err != nil {
		return err
	}
	if err := s.validateMySQLKey("batch id", rec.BatchID, s.mysqlKeyLimit.batchID); err != nil {
		return err
	}
	for _, job := range rec.Jobs {
		if err := s.validateMySQLKey("batch job id", job.JobID, s.mysqlKeyLimit.batchJob); err != nil {
			return err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = time.Now()
	}
	now := rec.CreatedAt.UnixMilli()
	allow := 0
	if rec.AllowFailed {
		allow = 1
	}
	_, err = tx.ExecContext(ctx, s.rebind(`INSERT INTO bus_batches
		(batch_id, dispatch_id, name, queue_name, allow_failed, total_jobs, pending_jobs, processed_jobs, failed_jobs, cancelled, completed, created_at_ms, updated_at_ms)
		VALUES (?, ?, ?, ?, ?, ?, ?, 0, 0, 0, 0, ?, ?)`),
		rec.BatchID, rec.DispatchID, rec.Name, rec.Queue, allow, len(rec.Jobs), len(rec.Jobs), now, now,
	)
	if err != nil {
		return err
	}
	for _, job := range rec.Jobs {
		if _, err := tx.ExecContext(ctx, s.rebind(`INSERT INTO bus_batch_jobs (batch_id, job_id, started, done, failed) VALUES (?, ?, 0, 0, 0)`), rec.BatchID, job.JobID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// MarkBatchJobStarted idempotently records that a member has begun without changing settlement counters.
func (s *sqlStore) MarkBatchJobStarted(ctx context.Context, batchID, jobID string) error {
	if err := s.ensureSchema(ctx); err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, s.rebind(`UPDATE bus_batch_jobs SET started=1 WHERE batch_id=? AND job_id=?`), batchID, jobID)
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if updated > 0 {
		return nil
	}
	// MySQL reports changed rows by default, so an already-started member and
	// a missing member both return zero until existence is checked explicitly.
	var exists int
	err = s.db.QueryRowContext(ctx, s.rebind(`SELECT 1 FROM bus_batch_jobs WHERE batch_id=? AND job_id=?`), batchID, jobID).Scan(&exists)
	if err == sql.ErrNoRows {
		return ErrNotFound
	}
	return err
}

// MarkBatchJobSucceeded delegates successful settlement to the shared transactional counter path.
func (s *sqlStore) MarkBatchJobSucceeded(ctx context.Context, batchID, jobID string) (BatchState, bool, error) {
	state, done, _, err := s.markBatchTerminal(ctx, batchID, jobID, false)
	return state, done, err
}

// MarkBatchJobFailed delegates failed settlement to the shared transactional counter path.
func (s *sqlStore) MarkBatchJobFailed(ctx context.Context, batchID, jobID string, _ error) (BatchState, bool, error) {
	state, done, _, err := s.markBatchTerminal(ctx, batchID, jobID, true)
	return state, done, err
}

// SettleBatchJob returns whether the requested outcome owns the member while
// preserving the established aggregate state returned by compatibility APIs.
func (s *sqlStore) SettleBatchJob(ctx context.Context, batchID, jobID string, outcome BatchJobOutcome, _ error) (BatchState, bool, error) {
	switch outcome {
	case BatchJobSucceeded:
		state, _, owned, err := s.markBatchTerminal(ctx, batchID, jobID, false)
		return state, owned, err
	case BatchJobFailed:
		state, _, owned, err := s.markBatchTerminal(ctx, batchID, jobID, true)
		return state, owned, err
	default:
		return BatchState{}, false, fmt.Errorf("unsupported batch job outcome %q", outcome)
	}
}

// CancelBatch persists cancellation and completion together so readers cannot observe an intermediate state.
func (s *sqlStore) CancelBatch(ctx context.Context, batchID string) error {
	if err := s.ensureSchema(ctx); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, s.rebind(`UPDATE bus_batches SET cancelled=1, completed=1, updated_at_ms=? WHERE batch_id=?`), time.Now().UnixMilli(), batchID)
	return err
}

// GetBatch reconstructs aggregate state and normalizes missing rows to ErrNotFound.
func (s *sqlStore) GetBatch(ctx context.Context, batchID string) (BatchState, error) {
	if err := s.ensureSchema(ctx); err != nil {
		return BatchState{}, err
	}
	row := s.db.QueryRowContext(ctx, s.rebind(`SELECT dispatch_id, name, queue_name, allow_failed, total_jobs, pending_jobs, processed_jobs, failed_jobs, cancelled, completed, created_at_ms, updated_at_ms
		FROM bus_batches WHERE batch_id=?`), batchID)
	var (
		dispatchID, name, queueName              string
		allow, total, pending, processed, failed int
		cancelled, completed                     int
		createdMS, updatedMS                     int64
	)
	if err := row.Scan(&dispatchID, &name, &queueName, &allow, &total, &pending, &processed, &failed, &cancelled, &completed, &createdMS, &updatedMS); err != nil {
		if err == sql.ErrNoRows {
			return BatchState{}, ErrNotFound
		}
		return BatchState{}, err
	}
	return BatchState{
		BatchID:     batchID,
		DispatchID:  dispatchID,
		Name:        name,
		Queue:       queueName,
		AllowFailed: allow == 1,
		Total:       total,
		Pending:     pending,
		Processed:   processed,
		Failed:      failed,
		Cancelled:   cancelled == 1,
		Completed:   completed == 1,
		CreatedAt:   time.UnixMilli(createdMS),
		UpdatedAt:   time.UnixMilli(updatedMS),
	}, nil
}

// MarkCallbackInvoked uses dialect-specific conflict suppression to claim each callback key once.
func (s *sqlStore) MarkCallbackInvoked(ctx context.Context, key string) (bool, error) {
	if err := s.ensureSchema(ctx); err != nil {
		return false, err
	}
	if err := s.validateMySQLKey("callback key", key, s.mysqlKeyLimit.callback); err != nil {
		return false, err
	}
	now := time.Now().UnixMilli()
	switch s.driverName {
	case "pgx", "postgres":
		res, err := s.db.ExecContext(ctx, `INSERT INTO bus_callback_invocations (callback_key, created_at_ms) VALUES ($1, $2) ON CONFLICT (callback_key) DO NOTHING`, key, now)
		if err != nil {
			return false, err
		}
		n, _ := res.RowsAffected()
		return n > 0, nil
	case "mysql":
		res, err := s.db.ExecContext(ctx, `INSERT IGNORE INTO bus_callback_invocations (callback_key, created_at_ms) VALUES (?, ?)`, key, now)
		if err != nil {
			return false, err
		}
		n, _ := res.RowsAffected()
		return n > 0, nil
	default:
		res, err := s.db.ExecContext(ctx, `INSERT INTO bus_callback_invocations (callback_key, created_at_ms) VALUES (?, ?) ON CONFLICT(callback_key) DO NOTHING`, key, now)
		if err != nil {
			return false, err
		}
		n, _ := res.RowsAffected()
		return n > 0, nil
	}
}

// Prune deletes dependent rows and terminal parents in one transaction to avoid orphaned state.
func (s *sqlStore) Prune(ctx context.Context, before time.Time) error {
	if err := s.ensureSchema(ctx); err != nil {
		return err
	}
	cutoff := before.UnixMilli()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	// Remove chain node-idempotency rows for terminal chains before pruning chains.
	if _, err := tx.ExecContext(ctx, s.rebind(`DELETE FROM bus_chain_completed_nodes WHERE chain_id IN (
		SELECT chain_id FROM bus_chains WHERE updated_at_ms < ? AND (completed=1 OR failed=1)
	)`), cutoff); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.rebind(`DELETE FROM bus_chains WHERE updated_at_ms < ? AND (completed=1 OR failed=1)`), cutoff); err != nil {
		return err
	}

	// Remove per-job state for terminal batches before pruning batches.
	if _, err := tx.ExecContext(ctx, s.rebind(`DELETE FROM bus_batch_jobs WHERE batch_id IN (
		SELECT batch_id FROM bus_batches WHERE updated_at_ms < ? AND completed=1
	)`), cutoff); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.rebind(`DELETE FROM bus_batches WHERE updated_at_ms < ? AND completed=1`), cutoff); err != nil {
		return err
	}

	// Callback markers are safe to prune independently.
	if _, err := tx.ExecContext(ctx, s.rebind(`DELETE FROM bus_callback_invocations WHERE created_at_ms < ?`), cutoff); err != nil {
		return err
	}

	return tx.Commit()
}

// markBatchTerminal conditionally claims one member and updates aggregate
// counters arithmetically so concurrent settlements cannot overwrite state.
func (s *sqlStore) markBatchTerminal(ctx context.Context, batchID, jobID string, isFailure bool) (BatchState, bool, bool, error) {
	if err := s.ensureSchema(ctx); err != nil {
		return BatchState{}, false, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return BatchState{}, false, false, err
	}
	defer func() { _ = tx.Rollback() }()

	failed := 0
	if isFailure {
		failed = 1
	}
	result, err := tx.ExecContext(ctx, s.rebind(`UPDATE bus_batch_jobs
		SET done=1, failed=?, started=1
		WHERE batch_id=? AND job_id=? AND done=0`), failed, batchID, jobID)
	if err != nil {
		return BatchState{}, false, false, err
	}
	claimedRows, err := result.RowsAffected()
	if err != nil {
		return BatchState{}, false, false, err
	}
	claimed := claimedRows > 0
	owned := claimed
	if !claimed {
		var committedFailure int
		row := tx.QueryRowContext(ctx, s.rebind(`SELECT failed FROM bus_batch_jobs WHERE batch_id=? AND job_id=?`), batchID, jobID)
		if err := row.Scan(&committedFailure); err != nil {
			if err == sql.ErrNoRows {
				return BatchState{}, false, false, ErrNotFound
			}
			return BatchState{}, false, false, err
		}
		owned = (committedFailure == 1) == isFailure
	}

	now := time.Now().UnixMilli()
	if claimed {
		// MySQL evaluates assignments left-to-right, so completion must read
		// the pre-settlement pending count before that count is decremented.
		result, err = tx.ExecContext(ctx, s.rebind(`UPDATE bus_batches SET
			cancelled=CASE WHEN ?=1 AND allow_failed=0 THEN 1 ELSE cancelled END,
			completed=CASE WHEN pending_jobs <= 1 OR (?=1 AND allow_failed=0) THEN 1 ELSE completed END,
			pending_jobs=CASE WHEN pending_jobs > 0 THEN pending_jobs-1 ELSE 0 END,
			processed_jobs=processed_jobs+1,
			failed_jobs=failed_jobs+?,
			updated_at_ms=?
			WHERE batch_id=?`), failed, failed, failed, now, batchID)
		if err != nil {
			return BatchState{}, false, false, err
		}
		updated, rowsErr := result.RowsAffected()
		if rowsErr != nil {
			return BatchState{}, false, false, rowsErr
		}
		if updated == 0 {
			return BatchState{}, false, false, ErrNotFound
		}
	} else if _, err := tx.ExecContext(ctx, s.rebind(`UPDATE bus_batches SET updated_at_ms=? WHERE batch_id=?`), now, batchID); err != nil {
		return BatchState{}, false, false, err
	}
	st, err := s.getBatchTx(ctx, tx, batchID)
	if err != nil {
		return BatchState{}, false, false, err
	}
	if err := tx.Commit(); err != nil {
		return BatchState{}, false, false, err
	}
	return st, st.Completed, owned, nil
}

// getChainTx reads chain state through the caller's transaction so advancement uses one consistent view.
func (s *sqlStore) getChainTx(ctx context.Context, tx *sql.Tx, chainID string) (ChainState, error) {
	row := tx.QueryRowContext(ctx, s.rebind(`SELECT dispatch_id, queue_name, nodes_json, next_index, completed, failed, failure, created_at_ms, updated_at_ms FROM bus_chains WHERE chain_id=?`), chainID)
	var (
		dispatchID, queueName, failure string
		nodesJSON                      []byte
		nextIndex, completed, failed   int
		createdMS, updatedMS           int64
	)
	if err := row.Scan(&dispatchID, &queueName, &nodesJSON, &nextIndex, &completed, &failed, &failure, &createdMS, &updatedMS); err != nil {
		if err == sql.ErrNoRows {
			return ChainState{}, ErrNotFound
		}
		return ChainState{}, err
	}
	var nodes []ChainNode
	if err := json.Unmarshal(nodesJSON, &nodes); err != nil {
		return ChainState{}, err
	}
	return ChainState{
		ChainID:    chainID,
		DispatchID: dispatchID,
		Queue:      queueName,
		Nodes:      nodes,
		NextIndex:  nextIndex,
		Completed:  completed == 1,
		Failed:     failed == 1,
		Failure:    failure,
		CreatedAt:  time.UnixMilli(createdMS),
		UpdatedAt:  time.UnixMilli(updatedMS),
	}, nil
}

// getBatchTx reads aggregate state through the caller's transaction so settlement uses one consistent view.
func (s *sqlStore) getBatchTx(ctx context.Context, tx *sql.Tx, batchID string) (BatchState, error) {
	row := tx.QueryRowContext(ctx, s.rebind(`SELECT dispatch_id, name, queue_name, allow_failed, total_jobs, pending_jobs, processed_jobs, failed_jobs, cancelled, completed, created_at_ms, updated_at_ms FROM bus_batches WHERE batch_id=?`), batchID)
	var (
		dispatchID, name, queueName              string
		allow, total, pending, processed, failed int
		cancelled, completed                     int
		createdMS, updatedMS                     int64
	)
	if err := row.Scan(&dispatchID, &name, &queueName, &allow, &total, &pending, &processed, &failed, &cancelled, &completed, &createdMS, &updatedMS); err != nil {
		if err == sql.ErrNoRows {
			return BatchState{}, ErrNotFound
		}
		return BatchState{}, err
	}
	return BatchState{
		BatchID:     batchID,
		DispatchID:  dispatchID,
		Name:        name,
		Queue:       queueName,
		AllowFailed: allow == 1,
		Total:       total,
		Pending:     pending,
		Processed:   processed,
		Failed:      failed,
		Cancelled:   cancelled == 1,
		Completed:   completed == 1,
		CreatedAt:   time.UnixMilli(createdMS),
		UpdatedAt:   time.UnixMilli(updatedMS),
	}, nil
}

// insertChainCompletedNode uses dialect-specific conflict suppression to detect the first completion atomically.
func (s *sqlStore) insertChainCompletedNode(ctx context.Context, tx *sql.Tx, chainID, nodeID string) (bool, error) {
	if err := s.validateMySQLKey("chain id", chainID, s.mysqlKeyLimit.chainID); err != nil {
		return false, err
	}
	if err := s.validateMySQLKey("chain node id", nodeID, s.mysqlKeyLimit.chainNode); err != nil {
		return false, err
	}
	now := time.Now().UnixMilli()
	switch s.driverName {
	case "pgx", "postgres":
		res, err := tx.ExecContext(ctx, `INSERT INTO bus_chain_completed_nodes (chain_id, node_id, created_at_ms) VALUES ($1, $2, $3) ON CONFLICT (chain_id, node_id) DO NOTHING`, chainID, nodeID, now)
		if err != nil {
			return false, err
		}
		n, _ := res.RowsAffected()
		return n > 0, nil
	case "mysql":
		res, err := tx.ExecContext(ctx, `INSERT IGNORE INTO bus_chain_completed_nodes (chain_id, node_id, created_at_ms) VALUES (?, ?, ?)`, chainID, nodeID, now)
		if err != nil {
			return false, err
		}
		n, _ := res.RowsAffected()
		return n > 0, nil
	default:
		res, err := tx.ExecContext(ctx, `INSERT INTO bus_chain_completed_nodes (chain_id, node_id, created_at_ms) VALUES (?, ?, ?) ON CONFLICT(chain_id, node_id) DO NOTHING`, chainID, nodeID, now)
		if err != nil {
			return false, err
		}
		n, _ := res.RowsAffected()
		return n > 0, nil
	}
}

// rebind converts portable question-mark placeholders to PostgreSQL positional parameters when required.
func (s *sqlStore) rebind(query string) string {
	if s.driverName != "pgx" && s.driverName != "postgres" {
		return query
	}
	var b strings.Builder
	b.Grow(len(query) + 8)
	i := 1
	for _, r := range query {
		if r == '?' {
			b.WriteString(fmt.Sprintf("$%d", i))
			i++
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
