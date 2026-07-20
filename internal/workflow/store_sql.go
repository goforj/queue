package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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
	DB         *sql.DB
	DriverName string
	DSN        string
	// AutoMigrate is retained for compatibility with the established config
	// shape. NewSQLStore keeps startup schema creation enabled by default; use
	// NewSQLStoreWithManagedSchema when deployment tooling owns the schema.
	AutoMigrate bool
}

// NewSQLStore creates a SQL-backed orchestration store.
func NewSQLStore(cfg SQLStoreConfig) (Store, error) {
	return newSQLStore(cfg, true)
}

// NewSQLStoreWithManagedSchema creates a SQL-backed orchestration store that
// executes no schema DDL because deployment tooling owns the required tables.
func NewSQLStoreWithManagedSchema(cfg SQLStoreConfig) (Store, error) {
	return newSQLStore(cfg, false)
}

// newSQLStore centralizes connection setup while keeping migration policy an
// explicit constructor choice instead of overloading a false zero value.
func newSQLStore(cfg SQLStoreConfig, autoMigrate bool) (Store, error) {
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
	return &sqlStore{
		db:          cfg.DB,
		driverName:  cfg.DriverName,
		autoMigrate: autoMigrate,
	}, nil
}

// sqlStore persists workflow state and transition ownership in one database.
type sqlStore struct {
	db            *sql.DB
	driverName    string
	autoMigrate   bool
	mysqlKeyLimit mysqlWorkflowKeyLimits

	ensureMu    sync.Mutex
	schemaReady bool
}

// transitionReceiptQueryer lets receipt reads share one implementation across
// committed database state and an in-flight workflow transaction.
type transitionReceiptQueryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

var (
	_ Store                  = (*sqlStore)(nil)
	_ chainAdvanceStore      = (*sqlStore)(nil)
	_ chainFailureStore      = (*sqlStore)(nil)
	_ batchSettlementStore   = (*sqlStore)(nil)
	_ transitionReceiptStore = (*sqlStore)(nil)
)

// mysqlColumnCapacity retains both limits because multibyte managed schemas may
// constrain exact identifiers by characters before they run out of bytes.
type mysqlColumnCapacity struct {
	dataType   string
	characters int64
	bytes      int64
}

// mysqlWorkflowKeyLimits records each workflow model's effective capacity
// across every table that persists its identifiers.
type mysqlWorkflowKeyLimits struct {
	chainID   mysqlColumnCapacity
	chainNode mysqlColumnCapacity
	batchID   mysqlColumnCapacity
	batchJob  mysqlColumnCapacity
	callback  mysqlColumnCapacity
}

// mysqlTransitionReceiptWidths records the binary widths needed for the one
// receipt table shared by chain and batch workflows.
type mysqlTransitionReceiptWidths struct {
	workflowID int64
	memberID   int64
}

// ensureSchema serializes schema creation and caches only a successful result
// so transient first-use failures remain retryable without racing the DDL sequence.
func (s *sqlStore) ensureSchema(ctx context.Context) error {
	s.ensureMu.Lock()
	defer s.ensureMu.Unlock()
	if s.schemaReady {
		return nil
	}

	var limits mysqlWorkflowKeyLimits
	if !s.autoMigrate {
		if s.driverName == "mysql" {
			loaded, err := s.loadMySQLWorkflowKeyLimits(ctx)
			if err != nil {
				return err
			}
			limits = loaded
		}
	} else if s.driverName == "mysql" {
		loaded, err := s.ensureMySQLSchema(ctx)
		if err != nil {
			return err
		}
		limits = loaded
	} else {
		for _, stmt := range s.schemaStatements() {
			if _, err := s.db.ExecContext(ctx, s.rebind(stmt)); err != nil {
				return err
			}
		}
	}

	s.mysqlKeyLimit = limits
	s.schemaReady = true
	return nil
}

// ensureMySQLSchema creates legacy state tables before deriving a missing
// receipt table from their live widths, leaving every existing table unchanged.
func (s *sqlStore) ensureMySQLSchema(ctx context.Context) (mysqlWorkflowKeyLimits, error) {
	for _, stmt := range s.workflowStateSchemaStatements() {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return mysqlWorkflowKeyLimits{}, err
		}
	}

	receiptExists, err := s.mysqlTransitionReceiptTableExists(ctx)
	if err != nil {
		return mysqlWorkflowKeyLimits{}, err
	}
	if !receiptExists {
		columns, columnsErr := s.loadMySQLWorkflowColumns(ctx)
		if columnsErr != nil {
			return mysqlWorkflowKeyLimits{}, columnsErr
		}
		widths, widthsErr := mysqlTransitionReceiptWidthsFromColumns(columns)
		if widthsErr != nil {
			return mysqlWorkflowKeyLimits{}, widthsErr
		}
		statement := s.transitionReceiptSchemaStatement(
			"VARBINARY(16)",
			fmt.Sprintf("VARBINARY(%d)", widths.workflowID),
			fmt.Sprintf("VARBINARY(%d)", widths.memberID),
		)
		if _, createErr := s.db.ExecContext(ctx, statement); createErr != nil {
			return mysqlWorkflowKeyLimits{}, fmt.Errorf(
				"create missing MySQL workflow transition receipt table with derived workflow_id VARBINARY(%d) and member_id VARBINARY(%d): %w; existing legacy tables were not altered, so pre-create bus_workflow_transition_receipts with compatible indexed widths before restarting",
				widths.workflowID,
				widths.memberID,
				createErr,
			)
		}
	}
	return s.loadMySQLWorkflowKeyLimits(ctx)
}

// mysqlTransitionReceiptTableExists distinguishes a missing receipt table from
// an existing malformed table that automatic startup must never alter.
func (s *sqlStore) mysqlTransitionReceiptTableExists(ctx context.Context) (bool, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.tables
		WHERE table_schema=DATABASE() AND table_name='bus_workflow_transition_receipts'`).Scan(&count); err != nil {
		return false, err
	}
	return count > 0, nil
}

// schemaStatements selects only the physical SQL types that differ by
// dialect while keeping one legacy table and column contract.
func (s *sqlStore) schemaStatements() []string {
	statements := s.workflowStateSchemaStatements()
	idType := "TEXT"
	receiptKindType := "TEXT"
	switch s.driverName {
	case "mysql":
		idType = fmt.Sprintf("VARBINARY(%d)", mysqlWorkflowIdentifierLimit)
		receiptKindType = "VARBINARY(16)"
	}
	return append(statements, s.transitionReceiptSchemaStatement(receiptKindType, idType, idType))
}

// workflowStateSchemaStatements returns the five established state and
// callback tables whose live MySQL widths govern a newly introduced receipt.
func (s *sqlStore) workflowStateSchemaStatements() []string {
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

// transitionReceiptSchemaStatement builds the shared immutable-receipt table
// with caller-selected key types while retaining one dialect-neutral layout.
func (s *sqlStore) transitionReceiptSchemaStatement(kindType, workflowIDType, memberIDType string) string {
	return fmt.Sprintf(`CREATE TABLE IF NOT EXISTS bus_workflow_transition_receipts (
			workflow_kind %s NOT NULL,
			receipt_version INTEGER NOT NULL,
			event_schema_version INTEGER NOT NULL,
			workflow_id %s NOT NULL,
			member_id %s NOT NULL,
			workflow_dispatch_id TEXT NOT NULL,
			workflow_created_at_ms BIGINT NOT NULL,
			outcome VARCHAR(16) NOT NULL,
			owner_delivery_id TEXT NOT NULL,
			owner_attempt BIGINT NOT NULL,
			job_dispatch_id TEXT NOT NULL,
			job_id TEXT NOT NULL,
			job_fingerprint TEXT NOT NULL,
			aggregate_completed INTEGER NOT NULL,
			aggregate_cancelled INTEGER NOT NULL,
			created_at_ms BIGINT NOT NULL,
			PRIMARY KEY (workflow_kind, workflow_id, member_id)
		)`, kindType, workflowIDType, memberIDType)
}

// loadMySQLWorkflowKeyLimits derives validation from the connected schema so
// wider caller-managed binary columns retain their established capacity.
func (s *sqlStore) loadMySQLWorkflowKeyLimits(ctx context.Context) (mysqlWorkflowKeyLimits, error) {
	columns, err := s.loadMySQLWorkflowColumns(ctx)
	if err != nil {
		return mysqlWorkflowKeyLimits{}, err
	}
	return mysqlWorkflowKeyLimitsFromColumns(columns)
}

// loadMySQLWorkflowColumns reads every key-bearing workflow column so startup
// can validate existing schemas and derive only genuinely missing structures.
func (s *sqlStore) loadMySQLWorkflowColumns(ctx context.Context) (map[string]mysqlColumnCapacity, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT table_name, column_name, data_type, character_maximum_length, character_octet_length
		FROM information_schema.columns
		WHERE table_schema=DATABASE() AND (
			(table_name='bus_chains' AND column_name='chain_id') OR
			(table_name='bus_chain_completed_nodes' AND column_name IN ('chain_id', 'node_id')) OR
			(table_name='bus_batches' AND column_name='batch_id') OR
			(table_name='bus_batch_jobs' AND column_name IN ('batch_id', 'job_id')) OR
			(table_name='bus_callback_invocations' AND column_name='callback_key') OR
			(table_name='bus_workflow_transition_receipts' AND column_name IN ('workflow_id', 'member_id'))
		)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns := make(map[string]mysqlColumnCapacity, 9)
	for rows.Next() {
		var (
			tableName, columnName, dataType string
			characters, bytes               sql.NullInt64
		)
		if err := rows.Scan(&tableName, &columnName, &dataType, &characters, &bytes); err != nil {
			return nil, err
		}
		if !characters.Valid || !bytes.Valid || characters.Int64 <= 0 || bytes.Int64 <= 0 {
			return nil, fmt.Errorf("MySQL workflow column %s.%s has no character capacity", tableName, columnName)
		}
		columns[tableName+"."+columnName] = mysqlColumnCapacity{dataType: dataType, characters: characters.Int64, bytes: bytes.Int64}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return columns, nil
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
		"bus_workflow_transition_receipts.workflow_id",
		"bus_workflow_transition_receipts.member_id",
	}
	if err := validateMySQLWorkflowColumns(columns, required); err != nil {
		return mysqlWorkflowKeyLimits{}, err
	}
	return mysqlWorkflowKeyLimits{
		chainID: intersectMySQLColumnCapacity(
			intersectMySQLColumnCapacity(columns["bus_chains.chain_id"], columns["bus_chain_completed_nodes.chain_id"]),
			columns["bus_workflow_transition_receipts.workflow_id"],
		),
		chainNode: intersectMySQLColumnCapacity(columns["bus_chain_completed_nodes.node_id"], columns["bus_workflow_transition_receipts.member_id"]),
		batchID: intersectMySQLColumnCapacity(
			intersectMySQLColumnCapacity(columns["bus_batches.batch_id"], columns["bus_batch_jobs.batch_id"]),
			columns["bus_workflow_transition_receipts.workflow_id"],
		),
		batchJob: intersectMySQLColumnCapacity(columns["bus_batch_jobs.job_id"], columns["bus_workflow_transition_receipts.member_id"]),
		callback: columns["bus_callback_invocations.callback_key"],
	}, nil
}

// mysqlTransitionReceiptWidthsFromColumns derives one shared table wide enough
// for the narrowest effective chain and batch identifiers in the legacy state.
func mysqlTransitionReceiptWidthsFromColumns(columns map[string]mysqlColumnCapacity) (mysqlTransitionReceiptWidths, error) {
	required := []string{
		"bus_chains.chain_id",
		"bus_chain_completed_nodes.chain_id",
		"bus_chain_completed_nodes.node_id",
		"bus_batches.batch_id",
		"bus_batch_jobs.batch_id",
		"bus_batch_jobs.job_id",
		"bus_callback_invocations.callback_key",
	}
	if err := validateMySQLWorkflowColumns(columns, required); err != nil {
		return mysqlTransitionReceiptWidths{}, err
	}
	chainID := intersectMySQLColumnCapacity(columns["bus_chains.chain_id"], columns["bus_chain_completed_nodes.chain_id"])
	batchID := intersectMySQLColumnCapacity(columns["bus_batches.batch_id"], columns["bus_batch_jobs.batch_id"])
	return mysqlTransitionReceiptWidths{
		workflowID: maxMySQLVARBINARYWidth(chainID, batchID),
		memberID:   maxMySQLVARBINARYWidth(columns["bus_chain_completed_nodes.node_id"], columns["bus_batch_jobs.job_id"]),
	}, nil
}

// validateMySQLWorkflowColumns rejects incomplete or comparison-unsafe key
// columns before automatic startup creates a dependent receipt table.
func validateMySQLWorkflowColumns(columns map[string]mysqlColumnCapacity, required []string) error {
	for _, column := range required {
		capacity, ok := columns[column]
		if !ok || capacity.characters <= 0 || capacity.bytes <= 0 {
			return fmt.Errorf("MySQL workflow schema is missing key capacity for %s", column)
		}
		if !strings.EqualFold(capacity.dataType, "varbinary") {
			return fmt.Errorf("MySQL workflow column %s must use VARBINARY for byte-exact identity; found %s", column, capacity.dataType)
		}
	}
	return nil
}

// maxMySQLVARBINARYWidth returns the physical width that can represent either
// effective capacity in the receipt table's byte-exact VARBINARY column.
func maxMySQLVARBINARYWidth(left, right mysqlColumnCapacity) int64 {
	width := left.bytes
	if left.characters > width {
		width = left.characters
	}
	if right.bytes > width {
		width = right.bytes
	}
	if right.characters > width {
		width = right.characters
	}
	return width
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

// validateTransitionReceiptKeys applies the capacity of the workflow model
// that owns the receipt so a wider batch schema is not constrained by chain
// columns, or vice versa.
func (s *sqlStore) validateTransitionReceiptKeys(receipt transitionReceipt) error {
	var workflowCapacity, memberCapacity mysqlColumnCapacity
	var workflowLabel, memberLabel string
	switch receipt.workflowKind {
	case chainTransitionKind:
		workflowCapacity = s.mysqlKeyLimit.chainID
		memberCapacity = s.mysqlKeyLimit.chainNode
		workflowLabel = "chain receipt id"
		memberLabel = "chain receipt node id"
	case batchTransitionKind:
		workflowCapacity = s.mysqlKeyLimit.batchID
		memberCapacity = s.mysqlKeyLimit.batchJob
		workflowLabel = "batch receipt id"
		memberLabel = "batch receipt job id"
	default:
		return fmt.Errorf("unsupported workflow transition receipt kind %q", receipt.workflowKind)
	}
	if err := s.validateMySQLKey(workflowLabel, receipt.workflowID, workflowCapacity); err != nil {
		return err
	}
	return s.validateMySQLKey(memberLabel, receipt.memberID, memberCapacity)
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
	result, err := s.advanceChainOutcome(ctx, chainID, completedNode, transitionClaim{})
	return result.next, result.done, err
}

// advanceChainOutcome couples the compare-and-swap result with the state that
// won it so a recovered delivery cannot mistake an earlier success for its own.
func (s *sqlStore) advanceChainOutcome(ctx context.Context, chainID string, completedNode string, claim transitionClaim) (chainAdvanceResult, error) {
	state, err := s.GetChain(ctx, chainID)
	if err != nil {
		return chainAdvanceResult{}, err
	}
	successOwned, err := chainNodeSuccessDisposition(state, completedNode)
	if err != nil {
		return chainAdvanceResult{}, err
	}
	next, done, claimable, err := chainNodeAdvanceDisposition(state, completedNode)
	if err != nil {
		return chainAdvanceResult{}, err
	}
	if !claimable {
		if state.DispatchID != "" && claim.dispatchID != "" && state.DispatchID != claim.dispatchID {
			return chainAdvanceResult{state: state}, nil
		}
		receipt, receiptKnown, receiptErr := s.chainTransitionReceipt(ctx, chainID, completedNode)
		if receiptErr != nil {
			return chainAdvanceResult{}, receiptErr
		}
		return chainAdvanceResult{state: state, next: next, done: done, successOwned: successOwned, receipt: receipt, receiptKnown: receiptKnown}, nil
	}
	if state.DispatchID != "" && claim.dispatchID != "" && state.DispatchID != claim.dispatchID {
		return chainAdvanceResult{}, fmt.Errorf("chain %q dispatch mismatch", chainID)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return chainAdvanceResult{}, err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := s.insertChainCompletedNode(ctx, tx, chainID, completedNode); err != nil {
		return chainAdvanceResult{}, err
	}
	advancedAt := time.Now()
	result, err := tx.ExecContext(ctx, s.rebind(`UPDATE bus_chains
		SET next_index=next_index+1, updated_at_ms=?
		WHERE chain_id=? AND next_index=? AND completed=0 AND failed=0`), advancedAt.UnixMilli(), chainID, state.NextIndex)
	if err != nil {
		return chainAdvanceResult{}, err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return chainAdvanceResult{}, err
	}
	if updated > 1 {
		return chainAdvanceResult{}, fmt.Errorf("chain %q advancement updated %d rows", chainID, updated)
	}
	if updated == 0 {
		if err := tx.Rollback(); err != nil {
			return chainAdvanceResult{}, err
		}
		state, err := s.GetChain(ctx, chainID)
		if err != nil {
			return chainAdvanceResult{}, err
		}
		successOwned, err := chainNodeSuccessDisposition(state, completedNode)
		if err != nil {
			return chainAdvanceResult{}, err
		}
		next, done, claimable, err := chainNodeAdvanceDisposition(state, completedNode)
		if err != nil {
			return chainAdvanceResult{}, err
		}
		if !claimable {
			if state.DispatchID != "" && claim.dispatchID != "" && state.DispatchID != claim.dispatchID {
				return chainAdvanceResult{state: state}, nil
			}
			receipt, receiptKnown, receiptErr := s.chainTransitionReceipt(ctx, chainID, completedNode)
			if receiptErr != nil {
				return chainAdvanceResult{}, receiptErr
			}
			return chainAdvanceResult{state: state, next: next, done: done, successOwned: successOwned, receipt: receipt, receiptKnown: receiptKnown}, nil
		}
		if state.DispatchID != "" && claim.dispatchID != "" && state.DispatchID != claim.dispatchID {
			return chainAdvanceResult{}, fmt.Errorf("chain %q dispatch mismatch", chainID)
		}
		return chainAdvanceResult{}, fmt.Errorf("chain %q node %q could not claim success", chainID, completedNode)
	}

	state.NextIndex++
	state.UpdatedAt = advancedAt
	if state.NextIndex >= len(state.Nodes) {
		completedAt := time.Now()
		result, err := tx.ExecContext(ctx, s.rebind(`UPDATE bus_chains
			SET completed=1, updated_at_ms=?
			WHERE chain_id=? AND next_index=? AND completed=0 AND failed=0`), completedAt.UnixMilli(), chainID, state.NextIndex)
		if err != nil {
			return chainAdvanceResult{}, err
		}
		completed, err := result.RowsAffected()
		if err != nil {
			return chainAdvanceResult{}, err
		}
		if completed != 1 {
			return chainAdvanceResult{}, fmt.Errorf("chain %q completion updated %d rows", chainID, completed)
		}
		state.Completed = true
		state.UpdatedAt = completedAt
	}
	receipt, receiptKnown, err := s.insertTransitionReceipt(ctx, tx, transitionReceipt{
		workflowKind:       chainTransitionKind,
		workflowID:         chainID,
		workflowDispatchID: state.DispatchID,
		workflowCreatedAt:  state.CreatedAt,
		memberID:           completedNode,
		outcome:            BatchJobSucceeded,
		owner:              claim,
		aggregateCompleted: state.Completed,
		createdAt:          state.UpdatedAt,
	})
	if err != nil {
		return chainAdvanceResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return s.readCommittedChainAdvance(ctx, chainID, completedNode, claim, err)
	}
	if state.Completed {
		return chainAdvanceResult{state: state, done: true, successOwned: true, claimedNow: true, receipt: receipt, receiptKnown: receiptKnown}, nil
	}
	n := state.Nodes[state.NextIndex]
	return chainAdvanceResult{state: state, next: &n, successOwned: true, claimedNow: true, receipt: receipt, receiptKnown: receiptKnown}, nil
}

// FailChainNode conditionally fails only the current node so a success that
// already advanced the chain cannot be reclassified by a late redelivery.
func (s *sqlStore) FailChainNode(ctx context.Context, chainID, nodeID string, cause error) (ChainState, bool, error) {
	result, err := s.failChainOutcome(ctx, chainID, nodeID, cause, transitionClaim{})
	return result.state, result.owned, err
}

// failChainOutcome commits terminal state and immutable delivery provenance in
// one transaction so a later settlement recovery can trust the failure owner.
func (s *sqlStore) failChainOutcome(ctx context.Context, chainID, nodeID string, cause error, claim transitionClaim) (chainFailureResult, error) {
	state, err := s.GetChain(ctx, chainID)
	if err != nil {
		return chainFailureResult{}, err
	}
	owned, claimable, err := chainNodeFailureDisposition(state, nodeID)
	if err != nil {
		return chainFailureResult{}, err
	}
	if !claimable {
		if state.DispatchID != "" && claim.dispatchID != "" && state.DispatchID != claim.dispatchID {
			return chainFailureResult{state: state}, nil
		}
		receipt, receiptKnown, receiptErr := s.chainTransitionReceipt(ctx, chainID, nodeID)
		if receiptErr != nil {
			return chainFailureResult{}, receiptErr
		}
		return chainFailureResult{state: state, owned: owned, receipt: receipt, receiptKnown: receiptKnown}, nil
	}
	if state.DispatchID != "" && claim.dispatchID != "" && state.DispatchID != claim.dispatchID {
		return chainFailureResult{}, fmt.Errorf("chain %q dispatch mismatch", chainID)
	}

	message := ""
	if cause != nil {
		message = cause.Error()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return chainFailureResult{}, err
	}
	defer func() { _ = tx.Rollback() }()

	failedAt := time.Now()
	result, err := tx.ExecContext(ctx, s.rebind(`UPDATE bus_chains
		SET failed=1, failure=?, updated_at_ms=?
		WHERE chain_id=? AND next_index=? AND completed=0 AND failed=0`), message, failedAt.UnixMilli(), chainID, state.NextIndex)
	if err != nil {
		return chainFailureResult{}, err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return chainFailureResult{}, err
	}
	if updated == 1 {
		state.Failed = true
		state.Failure = message
		state.UpdatedAt = failedAt
		receipt, receiptKnown, receiptErr := s.insertTransitionReceipt(ctx, tx, transitionReceipt{
			workflowKind:       chainTransitionKind,
			workflowID:         chainID,
			workflowDispatchID: state.DispatchID,
			workflowCreatedAt:  state.CreatedAt,
			memberID:           nodeID,
			outcome:            BatchJobFailed,
			owner:              claim,
			createdAt:          failedAt,
		})
		if receiptErr != nil {
			return chainFailureResult{}, receiptErr
		}
		if err := tx.Commit(); err != nil {
			return s.readCommittedChainFailure(ctx, chainID, nodeID, claim, err)
		}
		return chainFailureResult{state: state, owned: true, claimedNow: true, receipt: receipt, receiptKnown: receiptKnown}, nil
	}
	if updated > 1 {
		return chainFailureResult{}, fmt.Errorf("chain %q failure updated %d rows", chainID, updated)
	}
	if err := tx.Rollback(); err != nil {
		return chainFailureResult{}, err
	}

	state, err = s.GetChain(ctx, chainID)
	if err != nil {
		return chainFailureResult{}, err
	}
	owned, claimable, err = chainNodeFailureDisposition(state, nodeID)
	if err != nil {
		return chainFailureResult{}, err
	}
	if state.DispatchID != "" && claim.dispatchID != "" && state.DispatchID != claim.dispatchID {
		if claimable {
			return chainFailureResult{}, fmt.Errorf("chain %q dispatch mismatch", chainID)
		}
		return chainFailureResult{state: state}, nil
	}
	if claimable {
		return chainFailureResult{}, fmt.Errorf("chain %q node %q could not claim failure", chainID, nodeID)
	}
	receipt, receiptKnown, receiptErr := s.chainTransitionReceipt(ctx, chainID, nodeID)
	if receiptErr != nil {
		return chainFailureResult{}, receiptErr
	}
	return chainFailureResult{state: state, owned: owned, receipt: receipt, receiptKnown: receiptKnown}, nil
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
	_, err := s.db.ExecContext(ctx, s.rebind(`UPDATE bus_chains SET failed=1, failure=?, updated_at_ms=? WHERE chain_id=? AND completed=0 AND failed=0`), msg, time.Now().UnixMilli(), chainID)
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
	state, done, _, _, _, _, err := s.markBatchTerminal(ctx, batchID, jobID, false, transitionClaim{})
	return state, done, err
}

// MarkBatchJobFailed delegates failed settlement to the shared transactional counter path.
func (s *sqlStore) MarkBatchJobFailed(ctx context.Context, batchID, jobID string, _ error) (BatchState, bool, error) {
	state, done, _, _, _, _, err := s.markBatchTerminal(ctx, batchID, jobID, true, transitionClaim{})
	return state, done, err
}

// SettleBatchJob returns whether the requested outcome owns the member while
// preserving the established aggregate state returned by compatibility APIs.
func (s *sqlStore) SettleBatchJob(ctx context.Context, batchID, jobID string, outcome BatchJobOutcome, _ error) (BatchState, bool, error) {
	result, err := s.settleBatchOutcome(ctx, batchID, jobID, outcome, nil, transitionClaim{})
	return result.state, result.owned, err
}

// settleBatchOutcome returns both durable category ownership and the exact
// transaction's counter claim so recovery can keep aggregate ownership honest.
func (s *sqlStore) settleBatchOutcome(ctx context.Context, batchID, jobID string, outcome BatchJobOutcome, _ error, claim transitionClaim) (batchSettlementResult, error) {
	switch outcome {
	case BatchJobSucceeded:
		state, _, owned, claimed, receipt, receiptKnown, err := s.markBatchTerminal(ctx, batchID, jobID, false, claim)
		return batchSettlementResult{state: state, owned: owned, claimedNow: claimed, receipt: receipt, receiptKnown: receiptKnown}, err
	case BatchJobFailed:
		state, _, owned, claimed, receipt, receiptKnown, err := s.markBatchTerminal(ctx, batchID, jobID, true, claim)
		return batchSettlementResult{state: state, owned: owned, claimedNow: claimed, receipt: receipt, receiptKnown: receiptKnown}, err
	default:
		return batchSettlementResult{}, fmt.Errorf("unsupported batch job outcome %q", outcome)
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

	if _, err := tx.ExecContext(ctx, s.rebind(`DELETE FROM bus_workflow_transition_receipts WHERE workflow_kind=? AND workflow_id IN (
		SELECT chain_id FROM bus_chains WHERE updated_at_ms < ? AND (completed=1 OR failed=1)
	)`), chainTransitionKind, cutoff); err != nil {
		return err
	}

	// Remove chain node-idempotency rows for terminal chains before pruning chains.
	if _, err := tx.ExecContext(ctx, s.rebind(`DELETE FROM bus_chain_completed_nodes WHERE chain_id IN (
		SELECT chain_id FROM bus_chains WHERE updated_at_ms < ? AND (completed=1 OR failed=1)
	)`), cutoff); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.rebind(`DELETE FROM bus_chains WHERE updated_at_ms < ? AND (completed=1 OR failed=1)`), cutoff); err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, s.rebind(`DELETE FROM bus_workflow_transition_receipts WHERE workflow_kind=? AND workflow_id IN (
		SELECT batch_id FROM bus_batches WHERE updated_at_ms < ? AND completed=1
	)`), batchTransitionKind, cutoff); err != nil {
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
func (s *sqlStore) markBatchTerminal(ctx context.Context, batchID, jobID string, isFailure bool, claim transitionClaim) (BatchState, bool, bool, bool, transitionReceipt, bool, error) {
	if err := s.ensureSchema(ctx); err != nil {
		return BatchState{}, false, false, false, transitionReceipt{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return BatchState{}, false, false, false, transitionReceipt{}, false, err
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
		return BatchState{}, false, false, false, transitionReceipt{}, false, err
	}
	claimedRows, err := result.RowsAffected()
	if err != nil {
		return BatchState{}, false, false, false, transitionReceipt{}, false, err
	}
	claimed := claimedRows > 0
	owned := claimed
	// Reading the parent after the member CAS avoids two SQLite deferred
	// transactions deadlocking while both try to upgrade a shared read lock.
	initialState, err := s.getBatchTx(ctx, tx, batchID)
	if err != nil {
		return BatchState{}, false, false, false, transitionReceipt{}, false, err
	}
	if !claimed {
		var committedFailure int
		row := tx.QueryRowContext(ctx, s.rebind(`SELECT failed FROM bus_batch_jobs WHERE batch_id=? AND job_id=?`), batchID, jobID)
		if err := row.Scan(&committedFailure); err != nil {
			if err == sql.ErrNoRows {
				return BatchState{}, false, false, false, transitionReceipt{}, false, ErrNotFound
			}
			return BatchState{}, false, false, false, transitionReceipt{}, false, err
		}
		owned = (committedFailure == 1) == isFailure
	}
	if initialState.DispatchID != "" && claim.dispatchID != "" && initialState.DispatchID != claim.dispatchID {
		if claimed {
			return BatchState{}, false, false, false, transitionReceipt{}, false, fmt.Errorf("batch %q dispatch mismatch", batchID)
		}
		return initialState, initialState.Completed, false, false, transitionReceipt{}, false, nil
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
			return BatchState{}, false, false, false, transitionReceipt{}, false, err
		}
		updated, rowsErr := result.RowsAffected()
		if rowsErr != nil {
			return BatchState{}, false, false, false, transitionReceipt{}, false, rowsErr
		}
		if updated == 0 {
			return BatchState{}, false, false, false, transitionReceipt{}, false, ErrNotFound
		}
	} else if _, err := tx.ExecContext(ctx, s.rebind(`UPDATE bus_batches SET updated_at_ms=? WHERE batch_id=?`), now, batchID); err != nil {
		return BatchState{}, false, false, false, transitionReceipt{}, false, err
	}
	st, err := s.getBatchTx(ctx, tx, batchID)
	if err != nil {
		return BatchState{}, false, false, false, transitionReceipt{}, false, err
	}
	receipt, receiptKnown := transitionReceipt{}, false
	if claimed {
		outcome := BatchJobSucceeded
		if isFailure {
			outcome = BatchJobFailed
		}
		receipt, receiptKnown, err = s.insertTransitionReceipt(ctx, tx, transitionReceipt{
			workflowKind:       batchTransitionKind,
			workflowID:         batchID,
			workflowDispatchID: st.DispatchID,
			workflowCreatedAt:  st.CreatedAt,
			memberID:           jobID,
			outcome:            outcome,
			owner:              claim,
			createdAt:          time.UnixMilli(now),
		})
		if err != nil {
			return BatchState{}, false, false, false, transitionReceipt{}, false, err
		}
		if !initialState.Completed && st.Completed && receiptKnown {
			aggregate, aggregateKnown, aggregateErr := s.insertTransitionReceipt(ctx, tx, transitionReceipt{
				workflowKind:       batchTransitionKind,
				workflowID:         batchID,
				workflowDispatchID: st.DispatchID,
				workflowCreatedAt:  st.CreatedAt,
				memberID:           "",
				outcome:            outcome,
				owner:              claim,
				aggregateCompleted: true,
				aggregateCancelled: st.Cancelled,
				createdAt:          time.UnixMilli(now),
			})
			if aggregateErr != nil {
				return BatchState{}, false, false, false, transitionReceipt{}, false, aggregateErr
			}
			if aggregateKnown && aggregate.owner == claim {
				receipt.aggregateCompleted = true
				receipt.aggregateCancelled = st.Cancelled
			}
		}
	} else {
		receipt, receiptKnown, err = s.getBatchTransitionReceipt(ctx, tx, batchID, jobID)
		if err != nil {
			return BatchState{}, false, false, false, transitionReceipt{}, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return s.readCommittedBatchSettlement(ctx, batchID, jobID, isFailure, claim, err)
	}
	return st, st.Completed, owned, claimed, receipt, receiptKnown, nil
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

// getBatchTx locks the aggregate row before reading its current state so only
// the member whose parent update crosses into completion can own the terminal
// receipt. SQLite already holds its writer lock after the member claim, while
// PostgreSQL and MySQL need an explicit current read rather than an MVCC
// snapshot that may predate a concurrent settlement.
func (s *sqlStore) getBatchTx(ctx context.Context, tx *sql.Tx, batchID string) (BatchState, error) {
	query := `SELECT dispatch_id, name, queue_name, allow_failed, total_jobs, pending_jobs, processed_jobs, failed_jobs, cancelled, completed, created_at_ms, updated_at_ms FROM bus_batches WHERE batch_id=?`
	if s.driverName == "mysql" || s.driverName == "pgx" || s.driverName == "postgres" {
		query += ` FOR UPDATE`
	}
	row := tx.QueryRowContext(ctx, s.rebind(query), batchID)
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

// insertTransitionReceipt writes immutable owner identity in the same
// transaction as its workflow state mutation.
func (s *sqlStore) insertTransitionReceipt(ctx context.Context, tx *sql.Tx, receipt transitionReceipt) (transitionReceipt, bool, error) {
	if !receipt.owner.valid() {
		return transitionReceipt{}, false, nil
	}
	if receipt.version == 0 {
		receipt.version = transitionReceiptVersion
	}
	if receipt.eventSchemaVersion == 0 {
		receipt.eventSchemaVersion = eventSchemaVersion
	}
	if err := validateTransitionReceiptSupport(receipt); err != nil {
		return transitionReceipt{}, false, err
	}
	if err := s.validateTransitionReceiptKeys(receipt); err != nil {
		return transitionReceipt{}, false, err
	}
	if receipt.createdAt.IsZero() {
		receipt.createdAt = time.Now()
	}
	if _, err := tx.ExecContext(ctx, s.rebind(`DELETE FROM bus_workflow_transition_receipts
		WHERE workflow_kind=? AND workflow_id=? AND member_id=?
		AND (workflow_dispatch_id<>? OR workflow_created_at_ms<>?)`),
		receipt.workflowKind,
		receipt.workflowID,
		receipt.memberID,
		receipt.workflowDispatchID,
		receipt.workflowCreatedAt.UnixMilli(),
	); err != nil {
		return transitionReceipt{}, false, err
	}
	completed := 0
	if receipt.aggregateCompleted {
		completed = 1
	}
	cancelled := 0
	if receipt.aggregateCancelled {
		cancelled = 1
	}
	query := `INSERT INTO bus_workflow_transition_receipts
		(workflow_kind, receipt_version, event_schema_version, workflow_id, member_id, workflow_dispatch_id, workflow_created_at_ms, outcome,
		owner_delivery_id, owner_attempt, job_dispatch_id, job_id, job_fingerprint,
		aggregate_completed, aggregate_cancelled, created_at_ms)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(workflow_kind, workflow_id, member_id) DO NOTHING`
	if s.driverName == "mysql" {
		query = `INSERT INTO bus_workflow_transition_receipts
			(workflow_kind, receipt_version, event_schema_version, workflow_id, member_id, workflow_dispatch_id, workflow_created_at_ms, outcome,
			owner_delivery_id, owner_attempt, job_dispatch_id, job_id, job_fingerprint,
			aggregate_completed, aggregate_cancelled, created_at_ms)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON DUPLICATE KEY UPDATE workflow_kind=workflow_kind`
	}
	_, err := tx.ExecContext(ctx, s.rebind(query),
		receipt.workflowKind,
		receipt.version,
		receipt.eventSchemaVersion,
		receipt.workflowID,
		receipt.memberID,
		receipt.workflowDispatchID,
		receipt.workflowCreatedAt.UnixMilli(),
		string(receipt.outcome),
		receipt.owner.deliveryID,
		receipt.owner.attempt,
		receipt.owner.dispatchID,
		receipt.owner.jobID,
		receipt.owner.jobFingerprint,
		completed,
		cancelled,
		receipt.createdAt.UnixMilli(),
	)
	if err != nil {
		return transitionReceipt{}, false, err
	}
	persisted, known, err := s.getTransitionReceipt(ctx, tx, receipt.workflowKind, receipt.workflowID, receipt.memberID)
	if err != nil {
		return transitionReceipt{}, false, err
	}
	if !known {
		return transitionReceipt{}, false, errors.New("workflow transition receipt insert was not readable")
	}
	if !sameTransitionReceipt(receipt, persisted) {
		return transitionReceipt{}, false, fmt.Errorf("workflow transition receipt for %s %q member %q conflicts with its persisted owner", receipt.workflowKind, receipt.workflowID, receipt.memberID)
	}
	return persisted, true, nil
}

// sameTransitionReceipt compares every immutable ownership field while
// allowing storage to canonicalize the receipt timestamp to milliseconds.
func sameTransitionReceipt(want, persisted transitionReceipt) bool {
	return want.version == persisted.version &&
		want.eventSchemaVersion == persisted.eventSchemaVersion &&
		want.workflowKind == persisted.workflowKind &&
		want.workflowID == persisted.workflowID &&
		want.workflowDispatchID == persisted.workflowDispatchID &&
		want.workflowCreatedAt.Equal(persisted.workflowCreatedAt) &&
		want.memberID == persisted.memberID &&
		want.outcome == persisted.outcome &&
		want.owner == persisted.owner &&
		want.aggregateCompleted == persisted.aggregateCompleted &&
		want.aggregateCancelled == persisted.aggregateCancelled
}

// getTransitionReceipt reads one immutable receipt through either a database
// or the transaction currently mutating its parent workflow.
func (s *sqlStore) getTransitionReceipt(ctx context.Context, queryer transitionReceiptQueryer, kind, workflowID, memberID string) (transitionReceipt, bool, error) {
	row := queryer.QueryRowContext(ctx, s.rebind(`SELECT receipt_version, event_schema_version, workflow_dispatch_id, workflow_created_at_ms, outcome,
		owner_delivery_id, owner_attempt, job_dispatch_id, job_id, job_fingerprint,
		aggregate_completed, aggregate_cancelled, created_at_ms
		FROM bus_workflow_transition_receipts
		WHERE workflow_kind=? AND workflow_id=? AND member_id=?`), kind, workflowID, memberID)
	var (
		workflowDispatchID, outcome                    string
		ownerDeliveryID, jobDispatchID, jobID, jobHash string
		receiptVersion, storedEventSchemaVersion       int64
		workflowCreatedMS, ownerAttempt, createdMS     int64
		aggregateCompleted, aggregateCancelled         int
	)
	if err := row.Scan(
		&receiptVersion,
		&storedEventSchemaVersion,
		&workflowDispatchID,
		&workflowCreatedMS,
		&outcome,
		&ownerDeliveryID,
		&ownerAttempt,
		&jobDispatchID,
		&jobID,
		&jobHash,
		&aggregateCompleted,
		&aggregateCancelled,
		&createdMS,
	); err != nil {
		if err == sql.ErrNoRows {
			return transitionReceipt{}, false, nil
		}
		return transitionReceipt{}, false, err
	}
	if receiptVersion != int64(transitionReceiptVersion) || storedEventSchemaVersion != int64(eventSchemaVersion) {
		return transitionReceipt{}, false, fmt.Errorf("%w: receipt version %d, event schema %d", errUnsupportedTransitionReceipt, receiptVersion, storedEventSchemaVersion)
	}
	if ownerAttempt < 0 || int64(int(ownerAttempt)) != ownerAttempt {
		return transitionReceipt{}, false, fmt.Errorf("workflow transition receipt has invalid attempt %d", ownerAttempt)
	}
	if aggregateCompleted < 0 || aggregateCompleted > 1 || aggregateCancelled < 0 || aggregateCancelled > 1 {
		return transitionReceipt{}, false, fmt.Errorf("workflow transition receipt has invalid aggregate flags completed=%d cancelled=%d", aggregateCompleted, aggregateCancelled)
	}
	if aggregateCancelled == 1 && aggregateCompleted == 0 {
		return transitionReceipt{}, false, errors.New("workflow transition receipt cancellation is not completed")
	}
	receiptOutcome := BatchJobOutcome(outcome)
	if receiptOutcome != BatchJobSucceeded && receiptOutcome != BatchJobFailed {
		return transitionReceipt{}, false, fmt.Errorf("workflow transition receipt has invalid outcome %q", outcome)
	}
	owner := transitionClaim{
		deliveryID:     ownerDeliveryID,
		attempt:        int(ownerAttempt),
		dispatchID:     jobDispatchID,
		jobID:          jobID,
		jobFingerprint: jobHash,
	}
	if !owner.valid() {
		return transitionReceipt{}, false, errors.New("workflow transition receipt has incomplete owner identity")
	}
	return transitionReceipt{
		version:            int(receiptVersion),
		eventSchemaVersion: int(storedEventSchemaVersion),
		workflowKind:       kind,
		workflowID:         workflowID,
		workflowDispatchID: workflowDispatchID,
		workflowCreatedAt:  time.UnixMilli(workflowCreatedMS),
		memberID:           memberID,
		outcome:            receiptOutcome,
		owner:              owner,
		aggregateCompleted: aggregateCompleted == 1,
		aggregateCancelled: aggregateCancelled == 1,
		createdAt:          time.UnixMilli(createdMS),
	}, true, nil
}

// chainTransitionReceipt distinguishes corrupt cross-incarnation provenance
// from a genuinely absent receipt so recovery always fails closed.
func (s *sqlStore) chainTransitionReceipt(ctx context.Context, chainID, nodeID string) (transitionReceipt, bool, error) {
	if err := s.ensureSchema(ctx); err != nil {
		return transitionReceipt{}, false, err
	}
	state, err := s.GetChain(ctx, chainID)
	if err != nil {
		return transitionReceipt{}, false, err
	}
	receipt, known, err := s.getTransitionReceipt(ctx, s.db, chainTransitionKind, chainID, nodeID)
	if err != nil || !known {
		return receipt, known, err
	}
	if receipt.workflowDispatchID != state.DispatchID || !receipt.workflowCreatedAt.Equal(state.CreatedAt) {
		return transitionReceipt{}, false, fmt.Errorf("chain %q transition receipt does not match current workflow incarnation", chainID)
	}
	return receipt, true, nil
}

// batchTransitionReceipt returns member and aggregate writer identity only for
// the current durable batch incarnation.
func (s *sqlStore) batchTransitionReceipt(ctx context.Context, batchID, jobID string) (transitionReceipt, bool, error) {
	if err := s.ensureSchema(ctx); err != nil {
		return transitionReceipt{}, false, err
	}
	state, err := s.GetBatch(ctx, batchID)
	if err != nil {
		return transitionReceipt{}, false, err
	}
	receipt, known, err := s.getBatchTransitionReceipt(ctx, s.db, batchID, jobID)
	if err != nil || !known {
		return receipt, known, err
	}
	if receipt.workflowDispatchID != state.DispatchID || !receipt.workflowCreatedAt.Equal(state.CreatedAt) {
		return transitionReceipt{}, false, fmt.Errorf("batch %q transition receipt does not match current workflow incarnation", batchID)
	}
	return receipt, true, nil
}

// getBatchTransitionReceipt merges aggregate ownership into the immutable
// member receipt so first claims, replays, and recovery expose one store-neutral
// result even though SQL keeps the aggregate owner in a separate row.
func (s *sqlStore) getBatchTransitionReceipt(ctx context.Context, queryer transitionReceiptQueryer, batchID, jobID string) (transitionReceipt, bool, error) {
	receipt, known, err := s.getTransitionReceipt(ctx, queryer, batchTransitionKind, batchID, jobID)
	if err != nil {
		return receipt, known, err
	}
	aggregate, aggregateKnown, err := s.getTransitionReceipt(ctx, queryer, batchTransitionKind, batchID, "")
	if err != nil {
		return transitionReceipt{}, false, err
	}
	if aggregateKnown && !aggregate.aggregateCompleted {
		return transitionReceipt{}, false, fmt.Errorf("batch %q aggregate transition receipt does not own completion", batchID)
	}
	if aggregateKnown && aggregate.aggregateCancelled && aggregate.outcome != BatchJobFailed {
		return transitionReceipt{}, false, fmt.Errorf("batch %q aggregate transition receipt cancellation does not own failure", batchID)
	}
	if aggregateKnown {
		if err := s.validateBatchAggregateReceiptOwner(ctx, queryer, aggregate); err != nil {
			return transitionReceipt{}, false, err
		}
	}
	if !known {
		return receipt, false, nil
	}
	if aggregateKnown && (aggregate.workflowDispatchID != receipt.workflowDispatchID || !aggregate.workflowCreatedAt.Equal(receipt.workflowCreatedAt)) {
		return transitionReceipt{}, false, fmt.Errorf("batch %q aggregate transition receipt does not match member workflow incarnation", batchID)
	}
	if aggregateKnown && aggregate.owner == receipt.owner {
		receipt.aggregateCompleted = aggregate.aggregateCompleted
		receipt.aggregateCancelled = aggregate.aggregateCancelled
	}
	return receipt, true, nil
}

// validateBatchAggregateReceiptOwner proves the separate aggregate row maps to
// exactly one member receipt written by the same physical settlement claim.
func (s *sqlStore) validateBatchAggregateReceiptOwner(ctx context.Context, queryer transitionReceiptQueryer, aggregate transitionReceipt) error {
	row := queryer.QueryRowContext(ctx, s.rebind(`SELECT COUNT(*), COALESCE(MAX(outcome), '')
		FROM bus_workflow_transition_receipts
		WHERE workflow_kind=? AND workflow_id=? AND member_id<>?
		AND workflow_dispatch_id=? AND workflow_created_at_ms=?
		AND owner_delivery_id=? AND owner_attempt=? AND job_dispatch_id=? AND job_id=? AND job_fingerprint=?`),
		aggregate.workflowKind,
		aggregate.workflowID,
		"",
		aggregate.workflowDispatchID,
		aggregate.workflowCreatedAt.UnixMilli(),
		aggregate.owner.deliveryID,
		aggregate.owner.attempt,
		aggregate.owner.dispatchID,
		aggregate.owner.jobID,
		aggregate.owner.jobFingerprint,
	)
	var (
		matches int
		outcome string
	)
	if err := row.Scan(&matches, &outcome); err != nil {
		return err
	}
	if matches != 1 {
		return fmt.Errorf("batch %q aggregate transition receipt owner matches %d member receipts, want exactly one", aggregate.workflowID, matches)
	}
	if BatchJobOutcome(outcome) != aggregate.outcome {
		return fmt.Errorf("batch %q aggregate transition receipt does not match member outcome", aggregate.workflowID)
	}
	return nil
}

// readCommittedChainAdvance resolves an ambiguous commit response only when
// the durable receipt names the same physical generation.
func (s *sqlStore) readCommittedChainAdvance(ctx context.Context, chainID, nodeID string, claim transitionClaim, commitErr error) (chainAdvanceResult, error) {
	if !claim.valid() {
		return chainAdvanceResult{}, commitErr
	}
	receipt, known, err := s.chainTransitionReceipt(ctx, chainID, nodeID)
	if err != nil || !known || receipt.owner != claim || receipt.outcome != BatchJobSucceeded {
		return chainAdvanceResult{}, commitErr
	}
	state, err := s.GetChain(ctx, chainID)
	if err != nil {
		return chainAdvanceResult{}, commitErr
	}
	if receipt.workflowDispatchID != state.DispatchID || !receipt.workflowCreatedAt.Equal(state.CreatedAt) {
		return chainAdvanceResult{}, commitErr
	}
	successOwned, err := chainNodeSuccessDisposition(state, nodeID)
	if err != nil || !successOwned {
		return chainAdvanceResult{}, commitErr
	}
	next, done, _, err := chainNodeAdvanceDisposition(state, nodeID)
	if err != nil {
		return chainAdvanceResult{}, commitErr
	}
	return chainAdvanceResult{state: state, next: next, done: done, successOwned: true, claimedNow: true, receipt: receipt, receiptKnown: true}, nil
}

// readCommittedChainFailure resolves an ambiguous commit response only when
// terminal state and its immutable failed receipt name the same generation.
func (s *sqlStore) readCommittedChainFailure(ctx context.Context, chainID, nodeID string, claim transitionClaim, commitErr error) (chainFailureResult, error) {
	if !claim.valid() {
		return chainFailureResult{}, commitErr
	}
	receipt, known, err := s.chainTransitionReceipt(ctx, chainID, nodeID)
	if err != nil || !known || receipt.owner != claim || receipt.outcome != BatchJobFailed || receipt.aggregateCompleted || receipt.aggregateCancelled {
		return chainFailureResult{}, commitErr
	}
	state, err := s.GetChain(ctx, chainID)
	if err != nil {
		return chainFailureResult{}, commitErr
	}
	if receipt.workflowDispatchID != state.DispatchID || !receipt.workflowCreatedAt.Equal(state.CreatedAt) {
		return chainFailureResult{}, commitErr
	}
	owned, _, err := chainNodeFailureDisposition(state, nodeID)
	if err != nil || !owned || !state.Failed || state.Completed {
		return chainFailureResult{}, commitErr
	}
	return chainFailureResult{state: state, owned: true, claimedNow: true, receipt: receipt, receiptKnown: true}, nil
}

// readCommittedBatchSettlement resolves an ambiguous commit response only when
// the durable member receipt names the same physical generation and outcome.
// The reloaded state may include a later member's completion, so callers must
// use the receipt's aggregate flags when attributing terminal effects.
func (s *sqlStore) readCommittedBatchSettlement(ctx context.Context, batchID, jobID string, isFailure bool, claim transitionClaim, commitErr error) (BatchState, bool, bool, bool, transitionReceipt, bool, error) {
	if !claim.valid() {
		return BatchState{}, false, false, false, transitionReceipt{}, false, commitErr
	}
	receipt, known, err := s.batchTransitionReceipt(ctx, batchID, jobID)
	wantOutcome := BatchJobSucceeded
	if isFailure {
		wantOutcome = BatchJobFailed
	}
	if err != nil || !known || receipt.owner != claim || receipt.outcome != wantOutcome {
		return BatchState{}, false, false, false, transitionReceipt{}, false, commitErr
	}
	state, err := s.GetBatch(ctx, batchID)
	if err != nil {
		return BatchState{}, false, false, false, transitionReceipt{}, false, commitErr
	}
	return state, state.Completed, true, true, receipt, true, nil
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
