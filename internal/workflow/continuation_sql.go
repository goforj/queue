package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/goforj/queue/busruntime"
)

// continuationSchema checks additive capability without requiring it for legacy workflows.
func (s *sqlStore) continuationSchema(ctx context.Context, required bool) error {
	s.continuationMu.Lock()
	defer s.continuationMu.Unlock()
	if s.continuationReady.Load() {
		return nil
	}
	var count int
	query := s.continuationTableQuery()
	if err := s.db.QueryRowContext(ctx, query).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		if !required {
			return nil
		}
		if !s.autoMigrate {
			return errors.New("managed workflow schema lacks bus_continuations_v1")
		}
		blob := "BLOB"
		key := "VARCHAR(255)"
		if s.driverName == "postgres" || s.driverName == "pgx" {
			blob = "BYTEA"
		}
		if s.driverName == "mysql" {
			blob = "LONGBLOB"
			key = "VARBINARY(255)"
		}
		ddl := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS bus_continuations_v1 (workflow_id %s PRIMARY KEY, resource_key %s NOT NULL, ledger_json %s NOT NULL, revision BIGINT NOT NULL, active INTEGER NOT NULL, available_at_ms BIGINT NOT NULL)`, key, key, blob)
		if _, err := s.db.ExecContext(ctx, ddl); err != nil {
			return err
		}
	}
	indexQuery := `SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='bus_continuations_v1_ready'`
	switch s.driverName {
	case "mysql":
		indexQuery = `SELECT COUNT(*) FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name='bus_continuations_v1' AND index_name='bus_continuations_v1_ready'`
	case "postgres", "pgx":
		indexQuery = `SELECT COUNT(*) FROM pg_indexes WHERE schemaname=current_schema() AND tablename='bus_continuations_v1' AND indexname='bus_continuations_v1_ready'`
	}
	var indexed int
	if err := s.db.QueryRowContext(ctx, indexQuery).Scan(&indexed); err != nil {
		return err
	}
	if indexed == 0 {
		if !s.autoMigrate {
			return fmt.Errorf("%w: continuation recovery index is absent", ErrContinuationProtocol)
		}
		create := `CREATE INDEX IF NOT EXISTS bus_continuations_v1_ready ON bus_continuations_v1(resource_key,active,available_at_ms)`
		if s.driverName == "mysql" {
			create = `CREATE INDEX bus_continuations_v1_ready ON bus_continuations_v1(resource_key,active,available_at_ms)`
		}
		if _, err := s.db.ExecContext(ctx, create); err != nil {
			var exists int
			if check := s.db.QueryRowContext(ctx, indexQuery).Scan(&exists); check != nil || exists == 0 {
				return err
			}
		}
	}
	// Validate every required column in managed mode before accepting any named workflow.
	rows, err := s.db.QueryContext(ctx, `SELECT workflow_id,resource_key,ledger_json,revision,active,available_at_ms FROM bus_continuations_v1 WHERE 1=0`)
	if err != nil {
		return fmt.Errorf("%w: incompatible continuation schema", ErrContinuationProtocol)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	s.continuationReady.Store(true)
	return nil
}

// PrepareContinuations creates only the additive schema in automatic mode and validates managed mode.
func (s *sqlStore) PrepareContinuations(ctx context.Context) error {
	if err := s.ensureSchema(ctx); err != nil {
		return err
	}
	return s.continuationSchema(ctx, true)
}

// hasContinuations safely observes capability discovered before opening a parent transaction.
func (s *sqlStore) hasContinuations() bool { return s.continuationReady.Load() }

// insertLedger commits definitions in the same transaction as initial parent state.
func (s *sqlStore) insertLedger(ctx context.Context, tx *sql.Tx, ledger *continuationLedger) error {
	if ledger == nil {
		return nil
	}
	if len(ledger.WorkflowID) > 255 || len(ledger.Resource) > 255 {
		return errors.New("workflow continuation identity exceeds schema capacity")
	}
	raw, err := json.Marshal(ledger)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, s.rebind(`INSERT INTO bus_continuations_v1 (workflow_id,resource_key,ledger_json,revision,active,available_at_ms) VALUES (?,?,?,1,0,0)`), ledger.WorkflowID, ledger.Resource, raw)
	return err
}

// loadLedger bounds persisted JSON before interpretation and optionally locks the authority row.
func (s *sqlStore) loadLedger(ctx context.Context, queryer transitionReceiptQueryer, id string, lock bool) (*continuationLedger, int64, error) {
	query := `SELECT ledger_json,revision FROM bus_continuations_v1 WHERE workflow_id=?`
	if lock && s.driverName != "sqlite" {
		query += " FOR UPDATE"
	}
	var raw []byte
	var revision int64
	if err := queryer.QueryRowContext(ctx, s.rebind(query), id).Scan(&raw, &revision); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, 0, nil
		}
		return nil, 0, err
	}
	if len(raw) > 16<<20 {
		return nil, 0, fmt.Errorf("%w: ledger exceeds read budget", ErrContinuationProtocol)
	}
	var ledger continuationLedger
	if err := json.Unmarshal(raw, &ledger); err != nil {
		return nil, 0, fmt.Errorf("%w: malformed ledger", ErrContinuationProtocol)
	}
	if ledger.Version != 1 || ledger.WorkflowID != id {
		return nil, 0, fmt.Errorf("%w: unsupported ledger", ErrContinuationProtocol)
	}
	return &ledger, revision, nil
}

// saveLedger advances a compare-and-swap revision so concurrent publisher and parent writes cannot be lost.
func (s *sqlStore) saveLedger(ctx context.Context, tx *sql.Tx, ledger *continuationLedger, revision int64) error {
	raw, err := json.Marshal(ledger)
	if err != nil {
		return err
	}
	if len(raw) > 16<<20 {
		return fmt.Errorf("%w: ledger exceeds write budget", ErrContinuationProtocol)
	}
	active := 0
	if ledger.head() != nil {
		active = 1
	}
	available := int64(0)
	if ready := ledger.availableAt(); !ready.IsZero() {
		available = ready.UnixMilli()
	}
	result, err := tx.ExecContext(ctx, s.rebind(`UPDATE bus_continuations_v1 SET ledger_json=?,revision=revision+1,active=?,available_at_ms=? WHERE workflow_id=? AND revision=?`), raw, active, available, ledger.WorkflowID, revision)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return errors.New("workflow continuation revision changed")
	}
	return nil
}

// triggerSQLContinuations commits safe snapshots under the parent's state transaction.
func (s *sqlStore) triggerSQLContinuations(ctx context.Context, tx *sql.Tx, id string, summary busruntime.ContinuationSummary, firstFailure, terminal bool, at time.Time) error {
	if !s.hasContinuations() {
		return nil
	}
	ledger, revision, err := s.loadLedger(ctx, tx, id, true)
	if err != nil || ledger == nil {
		return err
	}
	ledger.trigger(summary, firstFailure, terminal, at)
	return s.saveLedger(ctx, tx, ledger, revision)
}

// ContinuationOutcomes reads triggered outcomes without a recovery worker or application callback.
func (s *sqlStore) ContinuationOutcomes(ctx context.Context, id string) ([]busruntime.ContinuationOutcome, error) {
	if err := s.continuationSchema(ctx, false); err != nil {
		return nil, err
	}
	if !s.hasContinuations() {
		return nil, ErrNotFound
	}
	ledger, _, err := s.loadLedger(ctx, s.db, id, false)
	if err != nil {
		return nil, err
	}
	if ledger == nil {
		return nil, ErrNotFound
	}
	result := make([]busruntime.ContinuationOutcome, len(ledger.Invocations))
	for i, intent := range ledger.Invocations {
		result[i] = intent.Outcome
	}
	return result, nil
}

// ClaimContinuation recovers persisted eligible heads even when the original business delivery is gone.
func (s *sqlStore) ClaimContinuation(ctx context.Context, resource, owner string, now time.Time, lease time.Duration) (continuationClaim, bool, error) {
	if err := s.continuationSchema(ctx, false); err != nil {
		return continuationClaim{}, false, err
	}
	if !s.hasContinuations() {
		return continuationClaim{}, false, nil
	}
	rows, err := s.db.QueryContext(ctx, s.rebind(`SELECT workflow_id FROM bus_continuations_v1 WHERE resource_key=? AND active=1 AND available_at_ms<=? ORDER BY workflow_id LIMIT 32`), resource, now.UnixMilli())
	if err != nil {
		return continuationClaim{}, false, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return continuationClaim{}, false, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	closeErr := rows.Close()
	if err != nil || closeErr != nil {
		return continuationClaim{}, false, errors.Join(err, closeErr)
	}
	for _, id := range ids {
		claim, ok, err := s.claimLedger(ctx, id, owner, now, lease)
		if err != nil || ok {
			return claim, ok, err
		}
	}
	return continuationClaim{}, false, nil
}

// claimLedger arbitrates one lease using the same row authority as settlement and trigger writes.
func (s *sqlStore) claimLedger(ctx context.Context, id, owner string, now time.Time, lease time.Duration) (continuationClaim, bool, error) {
	defer s.lockWriteTransaction()()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return continuationClaim{}, false, err
	}
	defer tx.Rollback()
	ledger, revision, err := s.loadLedger(ctx, tx, id, true)
	if err != nil || ledger == nil {
		return continuationClaim{}, false, err
	}
	claim, ok := ledger.claim(owner, now, lease)
	if !ok {
		return continuationClaim{}, false, nil
	}
	if err := s.saveLedger(ctx, tx, ledger, revision); err != nil {
		return continuationClaim{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return continuationClaim{}, false, err
	}
	return claim, true, nil
}

// ValidateContinuation checks the authoritative head and full immutable job association before execution.
func (s *sqlStore) ValidateContinuation(ctx context.Context, claim continuationClaim) (bool, error) {
	ledger, _, err := s.loadLedger(ctx, s.db, claim.WorkflowID, false)
	return ledger != nil && ledger.owns(claim), err
}

// SettleContinuation fences stale workers from releasing a newer head or lease generation.
func (s *sqlStore) SettleContinuation(ctx context.Context, claim continuationClaim, status string, at time.Time) error {
	defer s.lockWriteTransaction()()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ledger, revision, err := s.loadLedger(ctx, tx, claim.WorkflowID, true)
	if err != nil {
		return err
	}
	if ledger == nil {
		return ErrNotFound
	}
	if err := ledger.settle(claim, status, at); err != nil {
		return err
	}
	if err := s.saveLedger(ctx, tx, ledger, revision); err != nil {
		return err
	}
	return tx.Commit()
}

// BeginContinuation allocates one durable application attempt without trusting publisher liveness.
func (s *sqlStore) BeginContinuation(ctx context.Context, publication continuationClaim, owner string, now time.Time) (continuationClaim, bool, error) {
	defer s.lockWriteTransaction()()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return continuationClaim{}, false, err
	}
	defer tx.Rollback()
	ledger, revision, err := s.loadLedger(ctx, tx, publication.WorkflowID, true)
	if err != nil || ledger == nil {
		return continuationClaim{}, false, err
	}
	claim, ok := ledger.begin(publication, owner, now)
	if err := s.saveLedger(ctx, tx, ledger, revision); err != nil {
		return continuationClaim{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return continuationClaim{}, false, err
	}
	return claim, ok, nil
}

// RenewContinuation prevents lease expiry while an admitted callback still uses application dependencies.
func (s *sqlStore) RenewContinuation(ctx context.Context, claim continuationClaim, now time.Time) error {
	defer s.lockWriteTransaction()()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ledger, revision, err := s.loadLedger(ctx, tx, claim.WorkflowID, true)
	if err != nil {
		return err
	}
	if ledger == nil || !ledger.owns(claim) {
		return errors.New("stale continuation execution")
	}
	ledger.ExecutionUntil = now.Add(time.Minute)
	if err := s.saveLedger(ctx, tx, ledger, revision); err != nil {
		return err
	}
	return tx.Commit()
}

// continuationTableQuery lets pruning discover additive authority through its own transaction snapshot.
func (s *sqlStore) continuationTableQuery() string {
	switch s.driverName {
	case "mysql":
		return `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='bus_continuations_v1'`
	case "postgres", "pgx":
		return `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=current_schema() AND table_name='bus_continuations_v1'`
	default:
		return `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='bus_continuations_v1'`
	}
}
