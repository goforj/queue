package bus

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

type SQLStoreConfig struct {
	DB          *sql.DB
	DriverName  string
	DSN         string
	AutoMigrate bool
}

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
	db          *sql.DB
	driverName  string
	autoMigrate bool

	ensureOnce sync.Once
	ensureErr  error
}

var _ Store = (*sqlStore)(nil)

func (s *sqlStore) ensureSchema(ctx context.Context) error {
	s.ensureOnce.Do(func() {
		if !s.autoMigrate {
			return
		}
		stmts := []string{
			`CREATE TABLE IF NOT EXISTS bus_chains (
				chain_id TEXT PRIMARY KEY,
				dispatch_id TEXT NOT NULL,
				queue_name TEXT NOT NULL,
				nodes_json BLOB NOT NULL,
				next_index INTEGER NOT NULL,
				completed INTEGER NOT NULL,
				failed INTEGER NOT NULL,
				failure TEXT NOT NULL,
				created_at_ms BIGINT NOT NULL,
				updated_at_ms BIGINT NOT NULL
			)`,
			`CREATE TABLE IF NOT EXISTS bus_chain_completed_nodes (
				chain_id TEXT NOT NULL,
				node_id TEXT NOT NULL,
				created_at_ms BIGINT NOT NULL,
				PRIMARY KEY (chain_id, node_id)
			)`,
			`CREATE TABLE IF NOT EXISTS bus_batches (
				batch_id TEXT PRIMARY KEY,
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
			)`,
			`CREATE TABLE IF NOT EXISTS bus_batch_jobs (
				batch_id TEXT NOT NULL,
				job_id TEXT NOT NULL,
				started INTEGER NOT NULL,
				done INTEGER NOT NULL,
				failed INTEGER NOT NULL,
				PRIMARY KEY (batch_id, job_id)
			)`,
			`CREATE TABLE IF NOT EXISTS bus_callback_invocations (
				callback_key TEXT PRIMARY KEY,
				created_at_ms BIGINT NOT NULL
			)`,
		}
		for _, stmt := range stmts {
			if _, err := s.db.ExecContext(ctx, s.rebind(stmt)); err != nil {
				s.ensureErr = err
				return
			}
		}
	})
	return s.ensureErr
}

func (s *sqlStore) CreateChain(ctx context.Context, rec ChainRecord) error {
	if err := s.ensureSchema(ctx); err != nil {
		return err
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

func (s *sqlStore) AdvanceChain(ctx context.Context, chainID string, completedNode string) (next *ChainNode, done bool, err error) {
	if err := s.ensureSchema(ctx); err != nil {
		return nil, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = tx.Rollback() }()

	st, err := s.getChainTx(ctx, tx, chainID)
	if err != nil {
		return nil, false, err
	}
	if st.Completed || st.Failed {
		if err := tx.Commit(); err != nil {
			return nil, false, err
		}
		return nil, true, nil
	}
	inserted, err := s.insertChainCompletedNode(ctx, tx, chainID, completedNode)
	if err != nil {
		return nil, false, err
	}
	if inserted {
		st.NextIndex++
		if st.NextIndex >= len(st.Nodes) {
			st.Completed = true
		}
		if err := s.updateChainStateTx(ctx, tx, st); err != nil {
			return nil, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	if st.Completed || st.NextIndex >= len(st.Nodes) {
		return nil, true, nil
	}
	n := st.Nodes[st.NextIndex]
	return &n, false, nil
}

func (s *sqlStore) FailChain(ctx context.Context, chainID string, cause error) error {
	if err := s.ensureSchema(ctx); err != nil {
		return err
	}
	msg := ""
	if cause != nil {
		msg = cause.Error()
	}
	_, err := s.db.ExecContext(ctx, s.rebind(`UPDATE bus_chains SET failed=1, failure=?, updated_at_ms=? WHERE chain_id=?`), msg, time.Now().UnixMilli(), chainID)
	return err
}

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

func (s *sqlStore) CreateBatch(ctx context.Context, rec BatchRecord) error {
	if err := s.ensureSchema(ctx); err != nil {
		return err
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

func (s *sqlStore) MarkBatchJobStarted(ctx context.Context, batchID, jobID string) error {
	if err := s.ensureSchema(ctx); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, s.rebind(`UPDATE bus_batch_jobs SET started=1 WHERE batch_id=? AND job_id=?`), batchID, jobID)
	return err
}

func (s *sqlStore) MarkBatchJobSucceeded(ctx context.Context, batchID, jobID string) (BatchState, bool, error) {
	return s.markBatchTerminal(ctx, batchID, jobID, false)
}

func (s *sqlStore) MarkBatchJobFailed(ctx context.Context, batchID, jobID string, _ error) (BatchState, bool, error) {
	return s.markBatchTerminal(ctx, batchID, jobID, true)
}

func (s *sqlStore) CancelBatch(ctx context.Context, batchID string) error {
	if err := s.ensureSchema(ctx); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, s.rebind(`UPDATE bus_batches SET cancelled=1, completed=1, updated_at_ms=? WHERE batch_id=?`), time.Now().UnixMilli(), batchID)
	return err
}

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

func (s *sqlStore) MarkCallbackInvoked(ctx context.Context, key string) (bool, error) {
	if err := s.ensureSchema(ctx); err != nil {
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

func (s *sqlStore) markBatchTerminal(ctx context.Context, batchID, jobID string, isFailure bool) (BatchState, bool, error) {
	if err := s.ensureSchema(ctx); err != nil {
		return BatchState{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return BatchState{}, false, err
	}
	defer func() { _ = tx.Rollback() }()

	var done int
	row := tx.QueryRowContext(ctx, s.rebind(`SELECT done FROM bus_batch_jobs WHERE batch_id=? AND job_id=?`), batchID, jobID)
	if err := row.Scan(&done); err != nil {
		if err == sql.ErrNoRows {
			return BatchState{}, false, ErrNotFound
		}
		return BatchState{}, false, err
	}
	if done == 0 {
		failed := 0
		if isFailure {
			failed = 1
		}
		if _, err := tx.ExecContext(ctx, s.rebind(`UPDATE bus_batch_jobs SET done=1, failed=?, started=1 WHERE batch_id=? AND job_id=?`), failed, batchID, jobID); err != nil {
			return BatchState{}, false, err
		}
	}

	st, err := s.getBatchTx(ctx, tx, batchID)
	if err != nil {
		return BatchState{}, false, err
	}
	if done == 0 {
		st.Pending--
		st.Processed++
		if isFailure {
			st.Failed++
		}
	}
	if isFailure && !st.AllowFailed {
		st.Cancelled = true
		st.Completed = true
	}
	if st.Pending <= 0 {
		st.Completed = true
	}
	if err := s.updateBatchStateTx(ctx, tx, st); err != nil {
		return BatchState{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return BatchState{}, false, err
	}
	return st, st.Completed, nil
}

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

func (s *sqlStore) updateChainStateTx(ctx context.Context, tx *sql.Tx, st ChainState) error {
	completed := 0
	if st.Completed {
		completed = 1
	}
	failed := 0
	if st.Failed {
		failed = 1
	}
	_, err := tx.ExecContext(ctx, s.rebind(`UPDATE bus_chains SET next_index=?, completed=?, failed=?, failure=?, updated_at_ms=? WHERE chain_id=?`),
		st.NextIndex, completed, failed, st.Failure, time.Now().UnixMilli(), st.ChainID,
	)
	return err
}

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

func (s *sqlStore) updateBatchStateTx(ctx context.Context, tx *sql.Tx, st BatchState) error {
	cancelled := 0
	if st.Cancelled {
		cancelled = 1
	}
	completed := 0
	if st.Completed {
		completed = 1
	}
	_, err := tx.ExecContext(ctx, s.rebind(`UPDATE bus_batches SET pending_jobs=?, processed_jobs=?, failed_jobs=?, cancelled=?, completed=?, updated_at_ms=? WHERE batch_id=?`),
		st.Pending, st.Processed, st.Failed, cancelled, completed, time.Now().UnixMilli(), st.BatchID,
	)
	return err
}

func (s *sqlStore) insertChainCompletedNode(ctx context.Context, tx *sql.Tx, chainID, nodeID string) (bool, error) {
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
