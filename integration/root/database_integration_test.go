//go:build integration

package root_test

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
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/goforj/queue"
	"github.com/goforj/queue/busruntime"
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

// execSQLiteIntegrationEventually tolerates the worker's short polling lock
// while keeping deterministic fault-fixture schema changes bounded.
func execSQLiteIntegrationEventually(db *sql.DB, query string, args ...any) (sql.Result, error) {
	deadline := time.Now().Add(5 * time.Second)
	for {
		result, err := db.Exec(query, args...)
		if err == nil {
			return result, nil
		}
		message := strings.ToLower(err.Error())
		if (!strings.Contains(message, "busy") && !strings.Contains(message, "locked")) || time.Now().After(deadline) {
			return nil, err
		}
		time.Sleep(10 * time.Millisecond)
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

// first returns one matching event so integration assertions can verify its
// correlation fields instead of relying only on aggregate counts.
func (r *databaseSettlementRecorder) first(kind queue.EventKind, jobType string) (queue.Event, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, event := range r.events {
		if event.Kind == kind && event.JobType == jobType {
			return event, true
		}
	}
	return queue.Event{}, false
}

// runSQLiteWorkflowWinnerFactRecovery proves receipt-backed recovery publishes
// the workflow facts already committed by the winning generation without
// executing application code a second time.
func runSQLiteWorkflowWinnerFactRecovery(t *testing.T, workflowKind string) {
	t.Helper()
	queueDSN := fmt.Sprintf("%s/queue-workflow-recovery-%s-%d.db", t.TempDir(), workflowKind, time.Now().UnixNano())
	workflowDSN := fmt.Sprintf("%s/workflow-recovery-%s-%d.db", t.TempDir(), workflowKind, time.Now().UnixNano())
	prepareSQLiteIntegrationSchema(t, queueDSN)

	workflowDB, err := sql.Open(testenv.BackendSQLite, workflowDSN)
	if err != nil {
		t.Fatalf("open workflow recovery store: %v", err)
	}
	t.Cleanup(func() { _ = workflowDB.Close() })
	store, err := queue.NewSQLStore(queue.SQLStoreConfig{
		DB:          workflowDB,
		DriverName:  testenv.BackendSQLite,
		AutoMigrate: true,
	})
	if err != nil {
		t.Fatalf("new workflow recovery store: %v", err)
	}

	recorder := &databaseSettlementRecorder{settlement: make(chan struct{})}
	queueName := "workflow-recovery-" + workflowKind
	runtimeCfg := withDBRecoveryPolicy(withDefaultQueue(sqliteCfg(queueDSN), queueName), 10*time.Millisecond, 30*time.Second)
	runtimeCfg.DisableAutoMigrate = true
	runtime, err := testenv.NewQueue(runtimeCfg, queue.WithStore(store), queue.WithObserver(recorder), queue.WithWorkers(1))
	if err != nil {
		t.Fatalf("new workflow recovery runtime: %v", err)
	}
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = runtime.Shutdown(shutdownCtx)
	})

	chainPredecessor := workflowKind == "chain_predecessor"
	batchPredecessor := workflowKind == "batch_predecessor"
	jobType := "job:db:workflow-recovery:" + workflowKind
	successorJobType := ""
	if chainPredecessor || batchPredecessor {
		jobType += ":first"
		successorJobType = "job:db:workflow-recovery:" + workflowKind + ":final"
	}
	var handlerCalls, successorCalls atomic.Int64
	runtime.Register(jobType, func(context.Context, queue.Message) error {
		handlerCalls.Add(1)
		return nil
	})
	if successorJobType != "" {
		runtime.Register(successorJobType, func(context.Context, queue.Message) error {
			successorCalls.Add(1)
			return nil
		})
	}

	queueDB, err := sql.Open(testenv.BackendSQLite, queueDSN)
	if err != nil {
		t.Fatalf("open workflow recovery queue database: %v", err)
	}
	defer queueDB.Close()
	triggerName := "reject_" + workflowKind + "_workflow_finalization"
	trigger := fmt.Sprintf(`CREATE TRIGGER %s
BEFORE DELETE ON queue_jobs
WHEN OLD.queue_name = '%s' AND OLD.id = 1
BEGIN
    SELECT RAISE(ABORT, 'forced workflow finalization failure');
END`, triggerName, queueName)
	if _, err := queueDB.Exec(trigger); err != nil {
		t.Fatalf("create workflow finalization trigger: %v", err)
	}
	if err := runtime.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workflow recovery runtime: %v", err)
	}

	var workflowID string
	primaryJob := queue.NewJob(jobType).OnQueue(queueName)
	switch workflowKind {
	case "chain":
		workflowID, err = runtime.Chain(primaryJob).Dispatch(context.Background())
	case "chain_predecessor":
		workflowID, err = runtime.Chain(
			primaryJob,
			queue.NewJob(successorJobType).OnQueue(queueName),
		).Dispatch(context.Background())
	case "batch":
		workflowID, err = runtime.Batch(primaryJob).OnQueue(queueName).Dispatch(context.Background())
	case "batch_predecessor":
		workflowID, err = runtime.Batch(primaryJob, queue.NewJob(successorJobType).OnQueue(queueName)).OnQueue(queueName).Dispatch(context.Background())
	default:
		t.Fatalf("unsupported workflow kind %q", workflowKind)
	}
	if err != nil {
		t.Fatalf("dispatch %s recovery workflow: %v", workflowKind, err)
	}

	select {
	case <-recorder.settlement:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for workflow settlement failure")
	}
	if handlerCalls.Load() != 1 {
		t.Fatalf("handler calls before recovery = %d, want 1", handlerCalls.Load())
	}
	if recorder.count(queue.EventJobSucceeded, jobType) != 0 {
		t.Fatal("job success published before durable queue settlement")
	}
	switch workflowKind {
	case "chain":
		state, stateErr := store.GetChain(context.Background(), workflowID)
		if stateErr != nil {
			t.Fatalf("get committed chain: %v", stateErr)
		}
		if !state.Completed || state.Failed || recorder.count(queue.EventChainCompleted, jobType) != 0 {
			t.Fatalf("chain before recovery = state:%+v completed facts:%d", state, recorder.count(queue.EventChainCompleted, jobType))
		}
	case "chain_predecessor":
		waitForObservabilityScenario(t, "sqlite_workflow_successor_settlement", 5*time.Second, func() bool {
			state, stateErr := store.GetChain(context.Background(), workflowID)
			return stateErr == nil && state.Completed && !state.Failed && successorCalls.Load() == 1 && recorder.count(queue.EventChainCompleted, successorJobType) == 1
		})
		if recorder.count(queue.EventChainAdvanced, jobType) != 0 || recorder.count(queue.EventChainCompleted, jobType) != 0 {
			t.Fatalf("predecessor facts before recovery = advanced:%d completed:%d", recorder.count(queue.EventChainAdvanced, jobType), recorder.count(queue.EventChainCompleted, jobType))
		}
	case "batch":
		state, stateErr := store.GetBatch(context.Background(), workflowID)
		if stateErr != nil {
			t.Fatalf("get committed batch: %v", stateErr)
		}
		if !state.Completed || state.Cancelled || state.Failed != 0 || recorder.count(queue.EventBatchProgressed, jobType) != 0 || recorder.count(queue.EventBatchCompleted, jobType) != 0 {
			t.Fatalf("batch before recovery = state:%+v progress/completed facts:%d/%d", state, recorder.count(queue.EventBatchProgressed, jobType), recorder.count(queue.EventBatchCompleted, jobType))
		}
	case "batch_predecessor":
		waitForObservabilityScenario(t, "sqlite_batch_terminal_member_settlement", 5*time.Second, func() bool {
			state, stateErr := store.GetBatch(context.Background(), workflowID)
			return stateErr == nil && state.Completed && !state.Cancelled && successorCalls.Load() == 1 && recorder.count(queue.EventBatchCompleted, successorJobType) == 1
		})
		if recorder.count(queue.EventBatchProgressed, jobType) != 0 || recorder.count(queue.EventBatchCompleted, jobType) != 0 {
			t.Fatalf("stale batch member facts before recovery = progress:%d completed:%d", recorder.count(queue.EventBatchProgressed, jobType), recorder.count(queue.EventBatchCompleted, jobType))
		}
	}

	if _, err := execSQLiteIntegrationEventually(queueDB, "DROP TRIGGER "+triggerName); err != nil {
		t.Fatalf("drop workflow finalization trigger: %v", err)
	}
	if _, err := execSQLiteIntegrationEventually(queueDB, `UPDATE queue_jobs SET processing_started_at=1 WHERE queue_name=? AND state='processing'`, queueName); err != nil {
		t.Fatalf("age workflow delivery for recovery: %v", err)
	}
	waitForObservabilityScenario(t, "sqlite_workflow_winner_fact_recovery_"+workflowKind, 5*time.Second, func() bool {
		if recorder.count(queue.EventJobSucceeded, jobType) != 1 {
			return false
		}
		if workflowKind == "chain" {
			return recorder.count(queue.EventChainCompleted, jobType) == 1
		}
		if chainPredecessor {
			return recorder.count(queue.EventChainAdvanced, jobType) == 1 && recorder.count(queue.EventChainCompleted, jobType) == 0
		}
		if batchPredecessor {
			return recorder.count(queue.EventBatchProgressed, jobType) == 1 && recorder.count(queue.EventBatchCompleted, jobType) == 0 && recorder.count(queue.EventBatchCompleted, successorJobType) == 1
		}
		return recorder.count(queue.EventBatchProgressed, jobType) == 1 && recorder.count(queue.EventBatchCompleted, jobType) == 1
	})
	if handlerCalls.Load() != 1 {
		t.Fatalf("handler calls after receipt-backed recovery = %d, want 1", handlerCalls.Load())
	}
	succeeded, ok := recorder.first(queue.EventJobSucceeded, jobType)
	if !ok || succeeded.Attempt != 0 || succeeded.EventID == "" {
		t.Fatalf("recovered attempt-zero success = %+v present:%t", succeeded, ok)
	}
	if (chainPredecessor || batchPredecessor) && successorCalls.Load() != 1 {
		t.Fatalf("successor calls after predecessor recovery = %d, want 1", successorCalls.Load())
	}
	var remaining int
	if err := queueDB.QueryRow(`SELECT COUNT(*) FROM queue_jobs WHERE queue_name=?`, queueName).Scan(&remaining); err != nil {
		t.Fatalf("count recovered workflow deliveries: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("recovered workflow deliveries = %d, want 0", remaining)
	}
	if recorder.count(queue.EventJobFailed, jobType) != 0 || recorder.count(queue.EventChainFailed, jobType) != 0 || recorder.count(queue.EventBatchFailed, jobType) != 0 || recorder.count(queue.EventBatchCancelled, jobType) != 0 {
		t.Fatal("contradictory replay published losing workflow facts")
	}
}

// runSQLiteRepeatedWorkflowSettlementRecovery proves multiple recovery
// finalization failures retain the original receipt owner until one later
// generation positively settles the physical row and releases deferred facts.
func runSQLiteRepeatedWorkflowSettlementRecovery(t *testing.T) {
	t.Helper()
	queueDSN := fmt.Sprintf("%s/queue-repeated-workflow-recovery-%d.db", t.TempDir(), time.Now().UnixNano())
	workflowDSN := fmt.Sprintf("%s/workflow-repeated-recovery-%d.db", t.TempDir(), time.Now().UnixNano())
	prepareSQLiteIntegrationSchema(t, queueDSN)

	workflowDB, err := sql.Open(testenv.BackendSQLite, workflowDSN)
	if err != nil {
		t.Fatalf("open repeated-recovery workflow store: %v", err)
	}
	t.Cleanup(func() { _ = workflowDB.Close() })
	store, err := queue.NewSQLStore(queue.SQLStoreConfig{
		DB:          workflowDB,
		DriverName:  testenv.BackendSQLite,
		AutoMigrate: true,
	})
	if err != nil {
		t.Fatalf("new repeated-recovery workflow store: %v", err)
	}

	const (
		queueName = "repeated-workflow-recovery"
		jobType   = "job:db:repeated-workflow-recovery"
	)
	recorder := &databaseSettlementRecorder{settlement: make(chan struct{})}
	runtimeCfg := withDBRecoveryPolicy(withDefaultQueue(sqliteCfg(queueDSN), queueName), 10*time.Millisecond, 30*time.Second)
	runtimeCfg.DisableAutoMigrate = true
	firstRuntime, err := testenv.NewQueue(runtimeCfg, queue.WithStore(store), queue.WithObserver(recorder), queue.WithWorkers(1))
	if err != nil {
		t.Fatalf("new first repeated-recovery runtime: %v", err)
	}
	firstStopped := false
	t.Cleanup(func() {
		if firstStopped {
			return
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = firstRuntime.Shutdown(shutdownCtx)
	})

	var handlerCalls atomic.Int64
	firstRuntime.Register(jobType, func(context.Context, queue.Message) error {
		handlerCalls.Add(1)
		return nil
	})

	queueDB, err := sql.Open(testenv.BackendSQLite, queueDSN)
	if err != nil {
		t.Fatalf("open repeated-recovery queue database: %v", err)
	}
	defer queueDB.Close()
	const triggerName = "reject_repeated_workflow_finalization"
	trigger := fmt.Sprintf(`CREATE TRIGGER %s
BEFORE DELETE ON queue_jobs
WHEN OLD.queue_name = '%s'
BEGIN
    SELECT RAISE(ABORT, 'forced repeated workflow finalization failure');
END`, triggerName, queueName)
	if _, err := queueDB.Exec(trigger); err != nil {
		t.Fatalf("create repeated-recovery finalization trigger: %v", err)
	}
	triggerInstalled := true
	defer func() {
		if triggerInstalled {
			_, _ = execSQLiteIntegrationEventually(queueDB, "DROP TRIGGER "+triggerName)
		}
	}()
	if err := firstRuntime.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start first repeated-recovery runtime: %v", err)
	}
	chainID, err := firstRuntime.Chain(queue.NewJob(jobType).OnQueue(queueName)).Dispatch(context.Background())
	if err != nil {
		t.Fatalf("dispatch repeated-recovery chain: %v", err)
	}
	select {
	case <-recorder.settlement:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for initial repeated-recovery settlement failure")
	}
	if recorder.count(queue.EventSettlementFailed, jobType) != 1 {
		t.Fatalf("initial settlement failures = %d, want 1 before stale recovery", recorder.count(queue.EventSettlementFailed, jobType))
	}
	if handlerCalls.Load() != 1 || recorder.count(queue.EventJobSucceeded, jobType) != 0 || recorder.count(queue.EventChainCompleted, jobType) != 0 || recorder.count(queue.EventProcessSucceeded, jobType) != 0 {
		t.Fatalf("initial calls/job/chain/process facts = %d/%d/%d/%d, want 1/0/0/0", handlerCalls.Load(), recorder.count(queue.EventJobSucceeded, jobType), recorder.count(queue.EventChainCompleted, jobType), recorder.count(queue.EventProcessSucceeded, jobType))
	}

	var (
		rowID              int64
		rowState           string
		rowAttempt         int
		originalGeneration string
	)
	if err := queueDB.QueryRow(`SELECT id, state, attempt, processing_token FROM queue_jobs WHERE queue_name=?`, queueName).Scan(&rowID, &rowState, &rowAttempt, &originalGeneration); err != nil {
		t.Fatalf("read initial repeated-recovery delivery: %v", err)
	}
	if rowState != "processing" || rowAttempt != 0 || originalGeneration == "" {
		t.Fatalf("initial repeated-recovery delivery = id:%d state:%q attempt:%d generation:%q", rowID, rowState, rowAttempt, originalGeneration)
	}
	var receiptOwner string
	if err := workflowDB.QueryRow(`SELECT owner_delivery_id FROM bus_workflow_transition_receipts WHERE workflow_kind='chain' AND workflow_id=?`, chainID).Scan(&receiptOwner); err != nil {
		t.Fatalf("read repeated-recovery receipt owner: %v", err)
	}
	if receiptOwner != originalGeneration {
		t.Fatalf("receipt owner = %q, want initial generation %q", receiptOwner, originalGeneration)
	}

	if _, err := execSQLiteIntegrationEventually(queueDB, `UPDATE queue_jobs SET processing_started_at=1 WHERE id=? AND state='processing'`, rowID); err != nil {
		t.Fatalf("age repeated-recovery delivery: %v", err)
	}
	waitForObservabilityScenario(t, "sqlite_repeated_workflow_settlement_failures", 5*time.Second, func() bool {
		return recorder.count(queue.EventSettlementFailed, jobType) >= 3
	})
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := firstRuntime.Shutdown(shutdownCtx); err != nil {
		cancel()
		t.Fatalf("shutdown repeated-recovery fault runtime: %v", err)
	}
	cancel()
	firstStopped = true

	var (
		pendingState      string
		pendingAttempt    int
		pendingGeneration string
		processingStarted sql.NullInt64
	)
	if err := queueDB.QueryRow(`SELECT state, attempt, processing_token, processing_started_at FROM queue_jobs WHERE id=?`, rowID).Scan(&pendingState, &pendingAttempt, &pendingGeneration, &processingStarted); err != nil {
		t.Fatalf("read repaired repeated-recovery delivery: %v", err)
	}
	if pendingState != "pending" || pendingAttempt != 0 || pendingGeneration != receiptOwner || processingStarted.Valid {
		t.Fatalf("repaired delivery = state:%q attempt:%d generation:%q started:%#v, want pending/0/%q/NULL", pendingState, pendingAttempt, pendingGeneration, processingStarted, receiptOwner)
	}
	if handlerCalls.Load() != 1 || recorder.count(queue.EventJobSucceeded, jobType) != 0 || recorder.count(queue.EventChainCompleted, jobType) != 0 || recorder.count(queue.EventProcessSucceeded, jobType) != 0 {
		t.Fatalf("pre-final calls/job/chain/process facts = %d/%d/%d/%d, want 1/0/0/0", handlerCalls.Load(), recorder.count(queue.EventJobSucceeded, jobType), recorder.count(queue.EventChainCompleted, jobType), recorder.count(queue.EventProcessSucceeded, jobType))
	}
	if _, err := execSQLiteIntegrationEventually(queueDB, "DROP TRIGGER "+triggerName); err != nil {
		t.Fatalf("drop repeated-recovery finalization trigger: %v", err)
	}
	triggerInstalled = false

	finalRuntime, err := testenv.NewQueue(runtimeCfg, queue.WithStore(store), queue.WithObserver(recorder), queue.WithWorkers(1))
	if err != nil {
		t.Fatalf("new final repeated-recovery runtime: %v", err)
	}
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = finalRuntime.Shutdown(shutdownCtx)
	})
	finalRuntime.Register(jobType, func(context.Context, queue.Message) error {
		handlerCalls.Add(1)
		return queue.Permanent(errors.New("receipt recovery re-executed application code"))
	})
	if err := finalRuntime.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start final repeated-recovery runtime: %v", err)
	}
	waitForObservabilityScenario(t, "sqlite_repeated_workflow_final_settlement", 5*time.Second, func() bool {
		var remaining int
		rowErr := queueDB.QueryRow(`SELECT COUNT(*) FROM queue_jobs WHERE id=?`, rowID).Scan(&remaining)
		return rowErr == nil && remaining == 0 && recorder.count(queue.EventJobSucceeded, jobType) == 1 && recorder.count(queue.EventChainCompleted, jobType) == 1 && recorder.count(queue.EventProcessSucceeded, jobType) == 1
	})
	if handlerCalls.Load() != 1 {
		t.Fatalf("handler calls after repeated recovery = %d, want 1", handlerCalls.Load())
	}
	if recorder.count(queue.EventJobSucceeded, jobType) != 1 || recorder.count(queue.EventChainCompleted, jobType) != 1 || recorder.count(queue.EventProcessSucceeded, jobType) != 1 {
		t.Fatalf("final job/chain/process facts = %d/%d/%d, want 1/1/1", recorder.count(queue.EventJobSucceeded, jobType), recorder.count(queue.EventChainCompleted, jobType), recorder.count(queue.EventProcessSucceeded, jobType))
	}
	if recorder.count(queue.EventJobFailed, jobType) != 0 || recorder.count(queue.EventChainFailed, jobType) != 0 {
		t.Fatal("repeated settlement recovery published contradictory failure facts")
	}
}

// runSQLiteTerminalBatchOwnerRecovery proves the terminal member's receipt,
// rather than an earlier settled member, owns recovered aggregate completion.
func runSQLiteTerminalBatchOwnerRecovery(t *testing.T) {
	t.Helper()
	queueDSN := fmt.Sprintf("%s/queue-terminal-batch-owner-%d.db", t.TempDir(), time.Now().UnixNano())
	workflowDSN := fmt.Sprintf("%s/workflow-terminal-batch-owner-%d.db", t.TempDir(), time.Now().UnixNano())
	prepareSQLiteIntegrationSchema(t, queueDSN)

	workflowDB, err := sql.Open(testenv.BackendSQLite, workflowDSN)
	if err != nil {
		t.Fatalf("open terminal batch workflow store: %v", err)
	}
	t.Cleanup(func() { _ = workflowDB.Close() })
	store, err := queue.NewSQLStore(queue.SQLStoreConfig{
		DB:          workflowDB,
		DriverName:  testenv.BackendSQLite,
		AutoMigrate: true,
	})
	if err != nil {
		t.Fatalf("new terminal batch workflow store: %v", err)
	}

	const (
		queueName       = "terminal-batch-owner"
		firstJobType    = "job:db:terminal-batch-owner:first"
		terminalJobType = "job:db:terminal-batch-owner:terminal"
	)
	recorder := &databaseSettlementRecorder{settlement: make(chan struct{})}
	runtimeCfg := withDBRecoveryPolicy(withDefaultQueue(sqliteCfg(queueDSN), queueName), 10*time.Millisecond, 30*time.Second)
	runtimeCfg.DisableAutoMigrate = true
	runtime, err := testenv.NewQueue(runtimeCfg, queue.WithStore(store), queue.WithObserver(recorder), queue.WithWorkers(1))
	if err != nil {
		t.Fatalf("new terminal batch recovery runtime: %v", err)
	}
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = runtime.Shutdown(shutdownCtx)
	})

	var firstCalls, terminalCalls atomic.Int64
	runtime.Register(firstJobType, func(context.Context, queue.Message) error {
		firstCalls.Add(1)
		return nil
	})
	runtime.Register(terminalJobType, func(context.Context, queue.Message) error {
		terminalCalls.Add(1)
		return nil
	})

	queueDB, err := sql.Open(testenv.BackendSQLite, queueDSN)
	if err != nil {
		t.Fatalf("open terminal batch queue database: %v", err)
	}
	defer queueDB.Close()
	const triggerName = "reject_terminal_batch_finalization"
	trigger := fmt.Sprintf(`CREATE TRIGGER %s
BEFORE DELETE ON queue_jobs
WHEN OLD.queue_name = '%s' AND OLD.id = 2
BEGIN
    SELECT RAISE(ABORT, 'forced terminal batch finalization failure');
END`, triggerName, queueName)
	if _, err := queueDB.Exec(trigger); err != nil {
		t.Fatalf("create terminal batch finalization trigger: %v", err)
	}
	if err := runtime.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start terminal batch recovery runtime: %v", err)
	}

	batchID, err := runtime.Batch(
		queue.NewJob(firstJobType).OnQueue(queueName),
		queue.NewJob(terminalJobType).OnQueue(queueName),
	).OnQueue(queueName).Dispatch(context.Background())
	if err != nil {
		t.Fatalf("dispatch terminal batch recovery workflow: %v", err)
	}
	select {
	case <-recorder.settlement:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for terminal batch settlement failure")
	}

	state, err := store.GetBatch(context.Background(), batchID)
	if err != nil {
		t.Fatalf("get committed terminal batch: %v", err)
	}
	if !state.Completed || state.Cancelled || state.Failed != 0 || state.Processed != 2 || state.Pending != 0 {
		t.Fatalf("terminal batch state before recovery = %+v", state)
	}
	if firstCalls.Load() != 1 || terminalCalls.Load() != 1 {
		t.Fatalf("first/terminal handler calls before recovery = %d/%d, want 1/1", firstCalls.Load(), terminalCalls.Load())
	}
	if recorder.count(queue.EventJobSucceeded, firstJobType) != 1 || recorder.count(queue.EventBatchProgressed, firstJobType) != 1 {
		t.Fatalf("settled first-member job/progress facts = %d/%d, want 1/1", recorder.count(queue.EventJobSucceeded, firstJobType), recorder.count(queue.EventBatchProgressed, firstJobType))
	}
	if recorder.count(queue.EventJobSucceeded, terminalJobType) != 0 || recorder.count(queue.EventBatchProgressed, terminalJobType) != 0 || recorder.count(queue.EventBatchCompleted, terminalJobType) != 0 {
		t.Fatalf("terminal facts before recovery = success/progress/completed %d/%d/%d, want 0/0/0", recorder.count(queue.EventJobSucceeded, terminalJobType), recorder.count(queue.EventBatchProgressed, terminalJobType), recorder.count(queue.EventBatchCompleted, terminalJobType))
	}
	if recorder.count(queue.EventBatchCompleted, firstJobType) != 0 {
		t.Fatal("earlier member was incorrectly credited with terminal batch completion")
	}
	var remainingID int64
	var remainingState string
	var processingToken string
	if err := queueDB.QueryRow(`SELECT id, state, processing_token FROM queue_jobs WHERE queue_name=?`, queueName).Scan(&remainingID, &remainingState, &processingToken); err != nil {
		t.Fatalf("read terminal delivery before recovery: %v", err)
	}
	if remainingID != 2 || remainingState != "processing" || processingToken == "" {
		t.Fatalf("remaining terminal delivery = id:%d state:%q token:%q, want id:2 state:processing with token", remainingID, remainingState, processingToken)
	}
	var receiptOwner, receiptJobID string
	var receiptCompleted int
	if err := workflowDB.QueryRow(`SELECT owner_delivery_id, job_id, aggregate_completed
		FROM bus_workflow_transition_receipts
		WHERE workflow_kind='batch' AND workflow_id=? AND member_id=''`, batchID).Scan(&receiptOwner, &receiptJobID, &receiptCompleted); err != nil {
		t.Fatalf("read terminal batch aggregate receipt: %v", err)
	}
	if receiptOwner != processingToken || receiptJobID == "" || receiptCompleted != 1 {
		t.Fatalf("terminal aggregate receipt = owner:%q job:%q completed:%d, want owner:%q with job and completion", receiptOwner, receiptJobID, receiptCompleted, processingToken)
	}
	var terminalMemberReceipts int
	if err := workflowDB.QueryRow(`SELECT COUNT(*) FROM bus_workflow_transition_receipts
		WHERE workflow_kind='batch' AND workflow_id=? AND member_id=? AND owner_delivery_id=? AND outcome='succeeded'`, batchID, receiptJobID, receiptOwner).Scan(&terminalMemberReceipts); err != nil {
		t.Fatalf("read terminal batch member receipt: %v", err)
	}
	if terminalMemberReceipts != 1 {
		t.Fatalf("terminal member receipts = %d, want 1", terminalMemberReceipts)
	}

	if _, err := execSQLiteIntegrationEventually(queueDB, "DROP TRIGGER "+triggerName); err != nil {
		t.Fatalf("drop terminal batch finalization trigger: %v", err)
	}
	if _, err := execSQLiteIntegrationEventually(queueDB, `UPDATE queue_jobs SET processing_started_at=1 WHERE id=? AND queue_name=? AND state='processing'`, remainingID, queueName); err != nil {
		t.Fatalf("age terminal batch delivery for recovery: %v", err)
	}
	waitForObservabilityScenario(t, "sqlite_terminal_batch_owner_recovery", 5*time.Second, func() bool {
		return recorder.count(queue.EventJobSucceeded, terminalJobType) == 1 &&
			recorder.count(queue.EventBatchProgressed, terminalJobType) == 1 &&
			recorder.count(queue.EventBatchCompleted, terminalJobType) == 1
	})
	if firstCalls.Load() != 1 || terminalCalls.Load() != 1 {
		t.Fatalf("first/terminal handler calls after recovery = %d/%d, want 1/1", firstCalls.Load(), terminalCalls.Load())
	}
	if recorder.count(queue.EventJobSucceeded, firstJobType) != 1 || recorder.count(queue.EventBatchProgressed, firstJobType) != 1 || recorder.count(queue.EventBatchCompleted, firstJobType) != 0 {
		t.Fatalf("first-member facts after recovery = success/progress/completed %d/%d/%d, want 1/1/0", recorder.count(queue.EventJobSucceeded, firstJobType), recorder.count(queue.EventBatchProgressed, firstJobType), recorder.count(queue.EventBatchCompleted, firstJobType))
	}
	firstProgressed, firstOK := recorder.first(queue.EventBatchProgressed, firstJobType)
	terminalSucceeded, successOK := recorder.first(queue.EventJobSucceeded, terminalJobType)
	terminalCompleted, completedOK := recorder.first(queue.EventBatchCompleted, terminalJobType)
	if !firstOK || !successOK || !completedOK || firstProgressed.JobID == "" || firstProgressed.JobID == receiptJobID || terminalSucceeded.JobID != receiptJobID || terminalCompleted.JobID != receiptJobID || terminalCompleted.BatchID != batchID || terminalCompleted.DispatchID != state.DispatchID {
		t.Fatalf("first progress/terminal success/terminal completion ownership = %+v present:%t / %+v present:%t / %+v present:%t", firstProgressed, firstOK, terminalSucceeded, successOK, terminalCompleted, completedOK)
	}
	var remaining int
	if err := queueDB.QueryRow(`SELECT COUNT(*) FROM queue_jobs WHERE queue_name=?`, queueName).Scan(&remaining); err != nil {
		t.Fatalf("count terminal batch deliveries after recovery: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("terminal batch deliveries after recovery = %d, want 0", remaining)
	}
	if recorder.count(queue.EventJobFailed, terminalJobType) != 0 || recorder.count(queue.EventBatchFailed, terminalJobType) != 0 || recorder.count(queue.EventBatchCancelled, terminalJobType) != 0 {
		t.Fatal("terminal batch recovery published contradictory failure facts")
	}
}

// runSQLiteFailedChainSettlementRecovery proves a terminal failure receipt
// survives repeated archive faults without replaying its application occurrence.
func runSQLiteFailedChainSettlementRecovery(t *testing.T) {
	t.Helper()
	queueDSN := fmt.Sprintf("%s/queue-failed-chain-recovery-%d.db", t.TempDir(), time.Now().UnixNano())
	workflowDSN := fmt.Sprintf("%s/workflow-failed-chain-recovery-%d.db", t.TempDir(), time.Now().UnixNano())
	prepareSQLiteIntegrationSchema(t, queueDSN)

	workflowDB, err := sql.Open(testenv.BackendSQLite, workflowDSN)
	if err != nil {
		t.Fatalf("open failed chain workflow store: %v", err)
	}
	t.Cleanup(func() { _ = workflowDB.Close() })
	store, err := queue.NewSQLStore(queue.SQLStoreConfig{DB: workflowDB, DriverName: testenv.BackendSQLite, AutoMigrate: true})
	if err != nil {
		t.Fatalf("new failed chain workflow store: %v", err)
	}

	const (
		queueName = "failed-chain-recovery"
		jobType   = "job:db:failed-chain-recovery"
	)
	recorder := &databaseSettlementRecorder{settlement: make(chan struct{})}
	runtimeCfg := withDBRecoveryPolicy(withDefaultQueue(sqliteCfg(queueDSN), queueName), 10*time.Millisecond, 30*time.Second)
	runtimeCfg.DisableAutoMigrate = true
	runtime, err := testenv.NewQueue(runtimeCfg, queue.WithStore(store), queue.WithObserver(recorder), queue.WithWorkers(1))
	if err != nil {
		t.Fatalf("new failed chain recovery runtime: %v", err)
	}
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = runtime.Shutdown(shutdownCtx)
	})

	originalCause := errors.New("original failed chain application cause")
	var handlerCalls atomic.Int64
	runtime.Register(jobType, func(context.Context, queue.Message) error {
		handlerCalls.Add(1)
		return queue.Permanent(originalCause)
	})

	queueDB, err := sql.Open(testenv.BackendSQLite, queueDSN)
	if err != nil {
		t.Fatalf("open failed chain queue database: %v", err)
	}
	defer queueDB.Close()
	const triggerName = "reject_failed_chain_terminal_settlement"
	trigger := fmt.Sprintf(`CREATE TRIGGER %s
BEFORE UPDATE OF state ON queue_jobs
WHEN OLD.queue_name = '%s' AND NEW.state = 'dead'
BEGIN
    SELECT RAISE(ABORT, 'forced failed chain finalization failure');
END`, triggerName, queueName)
	if _, err := queueDB.Exec(trigger); err != nil {
		t.Fatalf("create failed chain finalization trigger: %v", err)
	}
	triggerInstalled := true
	defer func() {
		if triggerInstalled {
			_, _ = execSQLiteIntegrationEventually(queueDB, "DROP TRIGGER "+triggerName)
		}
	}()
	if err := runtime.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start failed chain recovery runtime: %v", err)
	}
	chainID, err := runtime.Chain(queue.NewJob(jobType).OnQueue(queueName).Retry(3)).Dispatch(context.Background())
	if err != nil {
		t.Fatalf("dispatch failed chain recovery workflow: %v", err)
	}
	select {
	case <-recorder.settlement:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for failed chain settlement failure")
	}

	state, err := store.GetChain(context.Background(), chainID)
	if err != nil || !state.Failed || state.Completed || state.Failure != originalCause.Error() {
		t.Fatalf("failed chain before recovery = %+v err:%v", state, err)
	}
	if handlerCalls.Load() != 1 || recorder.count(queue.EventJobStarted, jobType) != 1 || recorder.count(queue.EventJobFailed, jobType) != 1 || recorder.count(queue.EventChainFailed, jobType) != 1 {
		t.Fatalf("initial chain calls/started/job-failed/chain-failed = %d/%d/%d/%d, want 1/1/1/1", handlerCalls.Load(), recorder.count(queue.EventJobStarted, jobType), recorder.count(queue.EventJobFailed, jobType), recorder.count(queue.EventChainFailed, jobType))
	}

	var (
		rowID           int64
		rowState        string
		rowAttempt      int
		processingToken string
	)
	if err := queueDB.QueryRow(`SELECT id, state, attempt, processing_token FROM queue_jobs WHERE queue_name=?`, queueName).Scan(&rowID, &rowState, &rowAttempt, &processingToken); err != nil {
		t.Fatalf("read failed chain delivery: %v", err)
	}
	if rowState != "processing" || rowAttempt != 0 || processingToken == "" {
		t.Fatalf("failed chain delivery = id:%d state:%q attempt:%d token:%q, want processing attempt 0", rowID, rowState, rowAttempt, processingToken)
	}
	var (
		receiptOwner, receiptDispatch, receiptJobID, receiptOutcome string
		receiptAttempt                                              int
		receiptCompleted, receiptCancelled                          int
	)
	if err := workflowDB.QueryRow(`SELECT owner_delivery_id, owner_attempt, job_dispatch_id, job_id, outcome, aggregate_completed, aggregate_cancelled
		FROM bus_workflow_transition_receipts WHERE workflow_kind='chain' AND workflow_id=?`, chainID).Scan(
		&receiptOwner, &receiptAttempt, &receiptDispatch, &receiptJobID, &receiptOutcome, &receiptCompleted, &receiptCancelled,
	); err != nil {
		t.Fatalf("read failed chain receipt: %v", err)
	}
	if receiptOwner != processingToken || receiptAttempt != 0 || receiptDispatch != state.DispatchID || receiptJobID == "" || receiptOutcome != "failed" || receiptCompleted != 0 || receiptCancelled != 0 {
		t.Fatalf("failed chain receipt = owner:%q attempt:%d dispatch:%q job:%q outcome:%q completed:%d cancelled:%d", receiptOwner, receiptAttempt, receiptDispatch, receiptJobID, receiptOutcome, receiptCompleted, receiptCancelled)
	}

	if _, err := execSQLiteIntegrationEventually(queueDB, `UPDATE queue_jobs SET processing_started_at=1 WHERE id=? AND state='processing'`, rowID); err != nil {
		t.Fatalf("age failed chain delivery: %v", err)
	}
	waitForObservabilityScenario(t, "sqlite_repeated_failed_chain_settlement_failures", 5*time.Second, func() bool {
		return recorder.count(queue.EventSettlementFailed, jobType) >= 3
	})
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := runtime.Shutdown(shutdownCtx); err != nil {
		cancel()
		t.Fatalf("shutdown failed-chain fault runtime: %v", err)
	}
	cancel()

	var (
		repairedState   string
		repairedAttempt int
		repairedToken   string
		repairedStarted sql.NullInt64
	)
	if err := queueDB.QueryRow(`SELECT state, attempt, processing_token, processing_started_at FROM queue_jobs WHERE id=?`, rowID).Scan(&repairedState, &repairedAttempt, &repairedToken, &repairedStarted); err != nil {
		t.Fatalf("read repaired failed chain delivery: %v", err)
	}
	if repairedState != "pending" || repairedAttempt != 0 || repairedToken != receiptOwner || repairedStarted.Valid {
		t.Fatalf("repaired failed chain delivery = state:%q attempt:%d token:%q started:%#v, want pending/0/%q/NULL", repairedState, repairedAttempt, repairedToken, repairedStarted, receiptOwner)
	}
	state, err = store.GetChain(context.Background(), chainID)
	if err != nil || state.Failure != originalCause.Error() {
		t.Fatalf("failed chain cause after repeated recovery = %+v err:%v", state, err)
	}
	if handlerCalls.Load() != 1 || recorder.count(queue.EventJobStarted, jobType) != 1 || recorder.count(queue.EventJobFailed, jobType) != 1 || recorder.count(queue.EventChainFailed, jobType) != 1 {
		t.Fatalf("pre-archive chain calls/started/job-failed/chain-failed = %d/%d/%d/%d, want 1/1/1/1", handlerCalls.Load(), recorder.count(queue.EventJobStarted, jobType), recorder.count(queue.EventJobFailed, jobType), recorder.count(queue.EventChainFailed, jobType))
	}
	if _, err := execSQLiteIntegrationEventually(queueDB, "DROP TRIGGER "+triggerName); err != nil {
		t.Fatalf("drop failed chain finalization trigger: %v", err)
	}
	triggerInstalled = false

	finalRuntime, err := testenv.NewQueue(runtimeCfg, queue.WithStore(store), queue.WithObserver(recorder), queue.WithWorkers(1))
	if err != nil {
		t.Fatalf("new final failed chain recovery runtime: %v", err)
	}
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = finalRuntime.Shutdown(shutdownCtx)
	})
	finalRuntime.Register(jobType, func(context.Context, queue.Message) error {
		handlerCalls.Add(1)
		return queue.Permanent(errors.New("failed-chain receipt recovery re-executed application code"))
	})
	if err := finalRuntime.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start final failed chain recovery runtime: %v", err)
	}
	waitForObservabilityScenario(t, "sqlite_failed_chain_terminal_settlement_recovery", 5*time.Second, func() bool {
		var archived string
		return queueDB.QueryRow(`SELECT state FROM queue_jobs WHERE id=?`, rowID).Scan(&archived) == nil && archived == "dead"
	})
	if handlerCalls.Load() != 1 || recorder.count(queue.EventJobStarted, jobType) != 1 || recorder.count(queue.EventJobFailed, jobType) != 1 || recorder.count(queue.EventChainFailed, jobType) != 1 {
		t.Fatalf("archived chain calls/started/job-failed/chain-failed = %d/%d/%d/%d, want 1/1/1/1", handlerCalls.Load(), recorder.count(queue.EventJobStarted, jobType), recorder.count(queue.EventJobFailed, jobType), recorder.count(queue.EventChainFailed, jobType))
	}
	var (
		archivedState   string
		archivedAttempt int
		archivedToken   sql.NullString
		archivedError   sql.NullString
	)
	if err := queueDB.QueryRow(`SELECT state, attempt, processing_token, last_error FROM queue_jobs WHERE id=?`, rowID).Scan(&archivedState, &archivedAttempt, &archivedToken, &archivedError); err != nil {
		t.Fatalf("read archived failed chain delivery: %v", err)
	}
	if archivedState != "dead" || archivedAttempt != 1 || archivedToken.Valid || !archivedError.Valid || archivedError.String != originalCause.Error() {
		t.Fatalf("archived failed chain delivery = state:%q attempt:%d token:%#v error:%q, want dead attempt 1 with persisted cause", archivedState, archivedAttempt, archivedToken, archivedError.String)
	}
}

// runSQLiteFailedBatchSettlementRecovery proves a receipt-backed failed member
// remains archived after finalization recovery without executing its handler or
// fabricating the application cause omitted from durable receipt state.
func runSQLiteFailedBatchSettlementRecovery(t *testing.T) {
	t.Helper()
	queueDSN := fmt.Sprintf("%s/queue-failed-batch-recovery-%d.db", t.TempDir(), time.Now().UnixNano())
	workflowDSN := fmt.Sprintf("%s/workflow-failed-batch-recovery-%d.db", t.TempDir(), time.Now().UnixNano())
	prepareSQLiteIntegrationSchema(t, queueDSN)

	workflowDB, err := sql.Open(testenv.BackendSQLite, workflowDSN)
	if err != nil {
		t.Fatalf("open failed batch workflow store: %v", err)
	}
	t.Cleanup(func() { _ = workflowDB.Close() })
	store, err := queue.NewSQLStore(queue.SQLStoreConfig{
		DB:          workflowDB,
		DriverName:  testenv.BackendSQLite,
		AutoMigrate: true,
	})
	if err != nil {
		t.Fatalf("new failed batch workflow store: %v", err)
	}

	const (
		queueName     = "failed-batch-recovery"
		firstJobType  = "job:db:failed-batch-recovery:first"
		failedJobType = "job:db:failed-batch-recovery:failed"
	)
	recorder := &databaseSettlementRecorder{settlement: make(chan struct{})}
	runtimeCfg := withDBRecoveryPolicy(withDefaultQueue(sqliteCfg(queueDSN), queueName), 10*time.Millisecond, 30*time.Second)
	runtimeCfg.DisableAutoMigrate = true
	runtime, err := testenv.NewQueue(runtimeCfg, queue.WithStore(store), queue.WithObserver(recorder), queue.WithWorkers(1))
	if err != nil {
		t.Fatalf("new failed batch recovery runtime: %v", err)
	}
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = runtime.Shutdown(shutdownCtx)
	})

	originalCause := errors.New("original failed batch application cause")
	var firstCalls, failedCalls atomic.Int64
	runtime.Register(firstJobType, func(context.Context, queue.Message) error {
		firstCalls.Add(1)
		return nil
	})
	runtime.Register(failedJobType, func(context.Context, queue.Message) error {
		failedCalls.Add(1)
		return queue.Permanent(originalCause)
	})

	queueDB, err := sql.Open(testenv.BackendSQLite, queueDSN)
	if err != nil {
		t.Fatalf("open failed batch queue database: %v", err)
	}
	defer queueDB.Close()
	const triggerName = "reject_failed_batch_terminal_settlement"
	trigger := fmt.Sprintf(`CREATE TRIGGER %s
BEFORE UPDATE OF state ON queue_jobs
WHEN OLD.queue_name = '%s' AND NEW.state = 'dead'
BEGIN
    SELECT RAISE(ABORT, 'forced failed batch finalization failure');
END`, triggerName, queueName)
	if _, err := queueDB.Exec(trigger); err != nil {
		t.Fatalf("create failed batch finalization trigger: %v", err)
	}
	triggerInstalled := true
	defer func() {
		if triggerInstalled {
			_, _ = execSQLiteIntegrationEventually(queueDB, "DROP TRIGGER "+triggerName)
		}
	}()
	if err := runtime.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start failed batch recovery runtime: %v", err)
	}

	batchID, err := runtime.Batch(
		queue.NewJob(firstJobType).OnQueue(queueName),
		queue.NewJob(failedJobType).OnQueue(queueName).Retry(3),
	).AllowFailures().OnQueue(queueName).Dispatch(context.Background())
	if err != nil {
		t.Fatalf("dispatch failed batch recovery workflow: %v", err)
	}
	select {
	case <-recorder.settlement:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for failed batch settlement failure")
	}

	state, err := store.GetBatch(context.Background(), batchID)
	if err != nil {
		t.Fatalf("get committed failed batch: %v", err)
	}
	if !state.Completed || state.Cancelled || state.Failed != 1 || state.Processed != 2 || state.Pending != 0 || !state.AllowFailed {
		t.Fatalf("failed batch state before recovery = %+v", state)
	}
	if firstCalls.Load() != 1 || failedCalls.Load() != 1 {
		t.Fatalf("first/failed handler calls before recovery = %d/%d, want 1/1", firstCalls.Load(), failedCalls.Load())
	}
	if recorder.count(queue.EventJobSucceeded, firstJobType) != 1 || recorder.count(queue.EventBatchProgressed, firstJobType) != 1 {
		t.Fatalf("first-member success/progress facts = %d/%d, want 1/1", recorder.count(queue.EventJobSucceeded, firstJobType), recorder.count(queue.EventBatchProgressed, firstJobType))
	}
	if recorder.count(queue.EventJobFailed, failedJobType) != 1 || recorder.count(queue.EventBatchProgressed, failedJobType) != 0 || recorder.count(queue.EventBatchCompleted, failedJobType) != 0 {
		t.Fatalf("failed-member facts before recovery = failed/progress/completed %d/%d/%d, want 1/0/0", recorder.count(queue.EventJobFailed, failedJobType), recorder.count(queue.EventBatchProgressed, failedJobType), recorder.count(queue.EventBatchCompleted, failedJobType))
	}

	var (
		rowID           int64
		rowState        string
		rowAttempt      int
		processingToken string
	)
	if err := queueDB.QueryRow(`SELECT id, state, attempt, processing_token FROM queue_jobs WHERE queue_name=?`, queueName).Scan(&rowID, &rowState, &rowAttempt, &processingToken); err != nil {
		t.Fatalf("read failed delivery before recovery: %v", err)
	}
	if rowState != "processing" || rowAttempt != 0 || processingToken == "" {
		t.Fatalf("failed delivery before recovery = id:%d state:%q attempt:%d token:%q, want retained processing attempt 0", rowID, rowState, rowAttempt, processingToken)
	}
	var receiptOwner, receiptJobID, receiptOutcome string
	var receiptCompleted int
	if err := workflowDB.QueryRow(`SELECT owner_delivery_id, job_id, outcome, aggregate_completed
		FROM bus_workflow_transition_receipts
		WHERE workflow_kind='batch' AND workflow_id=? AND member_id=''`, batchID).Scan(&receiptOwner, &receiptJobID, &receiptOutcome, &receiptCompleted); err != nil {
		t.Fatalf("read failed batch aggregate receipt: %v", err)
	}
	if receiptOwner != processingToken || receiptJobID == "" || receiptOutcome != "failed" || receiptCompleted != 1 {
		t.Fatalf("failed aggregate receipt = owner:%q job:%q outcome:%q completed:%d, want owner:%q failed completion", receiptOwner, receiptJobID, receiptOutcome, receiptCompleted, processingToken)
	}

	if _, err := execSQLiteIntegrationEventually(queueDB, `UPDATE queue_jobs SET processing_started_at=1 WHERE id=? AND state='processing'`, rowID); err != nil {
		t.Fatalf("age failed batch delivery for recovery: %v", err)
	}
	waitForObservabilityScenario(t, "sqlite_repeated_failed_batch_settlement_failures", 5*time.Second, func() bool {
		return recorder.count(queue.EventSettlementFailed, failedJobType) >= 3
	})
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := runtime.Shutdown(shutdownCtx); err != nil {
		cancel()
		t.Fatalf("shutdown repeated failed-batch fault runtime: %v", err)
	}
	cancel()
	var (
		repairedState   string
		repairedAttempt int
		repairedToken   string
		repairedStarted sql.NullInt64
	)
	if err := queueDB.QueryRow(`SELECT state, attempt, processing_token, processing_started_at FROM queue_jobs WHERE id=?`, rowID).Scan(&repairedState, &repairedAttempt, &repairedToken, &repairedStarted); err != nil {
		t.Fatalf("read repeatedly repaired failed delivery: %v", err)
	}
	if repairedState != "pending" || repairedAttempt != 0 || repairedToken != receiptOwner || repairedStarted.Valid {
		t.Fatalf("repaired failed delivery = state:%q attempt:%d token:%q started:%#v, want pending/0/%q/NULL", repairedState, repairedAttempt, repairedToken, repairedStarted, receiptOwner)
	}
	if firstCalls.Load() != 1 || failedCalls.Load() != 1 || recorder.count(queue.EventBatchCompleted, failedJobType) != 0 {
		t.Fatalf("pre-archive calls/completion = %d/%d/%d, want 1/1/0", firstCalls.Load(), failedCalls.Load(), recorder.count(queue.EventBatchCompleted, failedJobType))
	}
	if _, err := execSQLiteIntegrationEventually(queueDB, "DROP TRIGGER "+triggerName); err != nil {
		t.Fatalf("drop failed batch finalization trigger: %v", err)
	}
	triggerInstalled = false

	finalRuntime, err := testenv.NewQueue(runtimeCfg, queue.WithStore(store), queue.WithObserver(recorder), queue.WithWorkers(1))
	if err != nil {
		t.Fatalf("new final failed-batch recovery runtime: %v", err)
	}
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = finalRuntime.Shutdown(shutdownCtx)
	})
	finalRuntime.Register(firstJobType, func(context.Context, queue.Message) error {
		firstCalls.Add(1)
		return queue.Permanent(errors.New("failed-batch recovery re-executed the first member"))
	})
	finalRuntime.Register(failedJobType, func(context.Context, queue.Message) error {
		failedCalls.Add(1)
		return queue.Permanent(errors.New("failed-batch receipt recovery re-executed application code"))
	})
	if err := finalRuntime.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start final failed-batch recovery runtime: %v", err)
	}
	waitForObservabilityScenario(t, "sqlite_failed_batch_terminal_settlement_recovery", 5*time.Second, func() bool {
		var state string
		stateErr := queueDB.QueryRow(`SELECT state FROM queue_jobs WHERE id=?`, rowID).Scan(&state)
		return stateErr == nil && state == "dead" && recorder.count(queue.EventBatchCompleted, failedJobType) == 1
	})
	if firstCalls.Load() != 1 || failedCalls.Load() != 1 {
		t.Fatalf("first/failed handler calls after recovery = %d/%d, want 1/1", firstCalls.Load(), failedCalls.Load())
	}
	var (
		archivedState   string
		archivedAttempt int
		archivedToken   sql.NullString
		archivedError   sql.NullString
	)
	if err := queueDB.QueryRow(`SELECT state, attempt, processing_token, last_error FROM queue_jobs WHERE id=?`, rowID).Scan(&archivedState, &archivedAttempt, &archivedToken, &archivedError); err != nil {
		t.Fatalf("read archived failed delivery: %v", err)
	}
	if archivedState != "dead" || archivedAttempt != 1 || archivedToken.Valid || !archivedError.Valid || !strings.Contains(archivedError.String, "original cause was not persisted") || strings.Contains(archivedError.String, originalCause.Error()) {
		t.Fatalf("archived failed delivery = state:%q attempt:%d token:%#v error:%q, want dead attempt 1 with generic recovered cause", archivedState, archivedAttempt, archivedToken, archivedError.String)
	}
	if recorder.count(queue.EventJobFailed, failedJobType) != 1 || recorder.count(queue.EventBatchProgressed, failedJobType) != 0 || recorder.count(queue.EventBatchCompleted, failedJobType) != 1 {
		t.Fatalf("failed-member facts after recovery = failed/progress/completed %d/%d/%d, want 1/0/1", recorder.count(queue.EventJobFailed, failedJobType), recorder.count(queue.EventBatchProgressed, failedJobType), recorder.count(queue.EventBatchCompleted, failedJobType))
	}
	completed, completedOK := recorder.first(queue.EventBatchCompleted, failedJobType)
	if !completedOK || completed.BatchID != batchID || completed.JobID != receiptJobID || completed.DispatchID != state.DispatchID {
		t.Fatalf("failed terminal completion = %+v present:%t, want batch:%q job:%q dispatch:%q", completed, completedOK, batchID, receiptJobID, state.DispatchID)
	}
}

type sqliteWorkflowHandlerObservation struct {
	call       int64
	attempt    int
	provenance busruntime.DeliveryProvenance
	present    bool
}

// runSQLiteLaterWorkflowWinnerRecovery proves recovery follows the generation
// that actually committed the workflow transition after an earlier generation
// was reclaimed and retried.
func runSQLiteLaterWorkflowWinnerRecovery(t *testing.T) {
	t.Helper()
	queueDSN := fmt.Sprintf("%s/queue-later-workflow-winner-%d.db", t.TempDir(), time.Now().UnixNano())
	workflowDSN := fmt.Sprintf("%s/later-workflow-winner-%d.db", t.TempDir(), time.Now().UnixNano())
	prepareSQLiteIntegrationSchema(t, queueDSN)

	workflowDB, err := sql.Open(testenv.BackendSQLite, workflowDSN)
	if err != nil {
		t.Fatalf("open later-winner workflow store: %v", err)
	}
	t.Cleanup(func() { _ = workflowDB.Close() })
	store, err := queue.NewSQLStore(queue.SQLStoreConfig{
		DB:          workflowDB,
		DriverName:  testenv.BackendSQLite,
		AutoMigrate: true,
	})
	if err != nil {
		t.Fatalf("new later-winner workflow store: %v", err)
	}

	const (
		queueName = "later-workflow-winner"
		jobType   = "job:db:later-workflow-winner"
	)
	recorder := &databaseSettlementRecorder{settlement: make(chan struct{})}
	runtimeCfg := withDBRecoveryPolicy(withDefaultQueue(sqliteCfg(queueDSN), queueName), 10*time.Millisecond, 30*time.Second)
	runtimeCfg.DisableAutoMigrate = true
	firstRuntime, err := testenv.NewQueue(runtimeCfg, queue.WithStore(store), queue.WithObserver(recorder), queue.WithWorkers(1))
	if err != nil {
		t.Fatalf("new first later-winner recovery runtime: %v", err)
	}
	secondRuntime, err := testenv.NewQueue(runtimeCfg, queue.WithStore(store), queue.WithObserver(recorder), queue.WithWorkers(1))
	if err != nil {
		t.Fatalf("new second later-winner recovery runtime: %v", err)
	}

	firstStarted := make(chan sqliteWorkflowHandlerObservation, 1)
	recoveredAttemptZero := make(chan sqliteWorkflowHandlerObservation, 1)
	attemptOneWinner := make(chan sqliteWorkflowHandlerObservation, 1)
	firstReturned := make(chan struct{})
	releaseFirst := make(chan struct{})
	unexpectedCall := make(chan sqliteWorkflowHandlerObservation, 1)
	var (
		handlerCalls     atomic.Int64
		releaseFirstOnce sync.Once
	)
	t.Cleanup(func() {
		releaseFirstOnce.Do(func() { close(releaseFirst) })
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = firstRuntime.Shutdown(shutdownCtx)
		_ = secondRuntime.Shutdown(shutdownCtx)
	})

	handler := func(ctx context.Context, message queue.Message) error {
		provenance, present := busruntime.DeliveryProvenanceFromContext(ctx)
		observation := sqliteWorkflowHandlerObservation{
			call:       handlerCalls.Add(1),
			attempt:    message.Attempt,
			provenance: provenance,
			present:    present,
		}
		switch observation.call {
		case 1:
			firstStarted <- observation
			<-releaseFirst
			close(firstReturned)
			return nil
		case 2:
			recoveredAttemptZero <- observation
			return errors.New("transient error from reclaimed attempt zero")
		case 3:
			attemptOneWinner <- observation
			return nil
		default:
			select {
			case unexpectedCall <- observation:
			default:
			}
			return queue.Permanent(errors.New("receipt recovery re-executed application code"))
		}
	}
	firstRuntime.Register(jobType, handler)
	secondRuntime.Register(jobType, handler)

	queueDB, err := sql.Open(testenv.BackendSQLite, queueDSN)
	if err != nil {
		t.Fatalf("open later-winner queue database: %v", err)
	}
	defer queueDB.Close()
	const triggerName = "reject_later_workflow_winner_finalization"
	const trigger = `CREATE TRIGGER reject_later_workflow_winner_finalization
BEFORE DELETE ON queue_jobs
WHEN OLD.queue_name = 'later-workflow-winner'
BEGIN
    SELECT RAISE(ABORT, 'forced later-winner finalization failure');
END`
	if _, err := queueDB.Exec(trigger); err != nil {
		t.Fatalf("create later-winner finalization trigger: %v", err)
	}
	if err := firstRuntime.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start first later-winner recovery runtime: %v", err)
	}
	if err := secondRuntime.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start second later-winner recovery runtime: %v", err)
	}
	workflowID, err := firstRuntime.Chain(queue.NewJob(jobType).OnQueue(queueName).Retry(1)).Dispatch(context.Background())
	if err != nil {
		t.Fatalf("dispatch later-winner chain: %v", err)
	}

	var initial sqliteWorkflowHandlerObservation
	select {
	case initial = <-firstStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("initial workflow generation did not start")
	}
	if !initial.present || initial.attempt != 0 || initial.provenance.GenerationID == "" || initial.provenance.Recovered || initial.provenance.RecoveredGenerationID != "" {
		t.Fatalf("initial generation observation = %+v, want ordinary attempt-zero provenance", initial)
	}
	result, err := execSQLiteIntegrationEventually(queueDB, `UPDATE queue_jobs SET processing_started_at=1 WHERE queue_name=? AND state='processing' AND attempt=0`, queueName)
	if err != nil {
		t.Fatalf("age initial workflow generation: %v", err)
	}
	if rows, rowsErr := result.RowsAffected(); rowsErr != nil || rows != 1 {
		t.Fatalf("aged initial workflow rows = %d, error %v; want 1", rows, rowsErr)
	}

	var reclaimed sqliteWorkflowHandlerObservation
	select {
	case reclaimed = <-recoveredAttemptZero:
	case observation := <-unexpectedCall:
		t.Fatalf("unexpected workflow handler call before attempt-zero recovery: %+v", observation)
	case <-time.After(5 * time.Second):
		t.Fatal("stale attempt zero was not reclaimed")
	}
	if !reclaimed.present || reclaimed.attempt != 0 || !reclaimed.provenance.Recovered || reclaimed.provenance.GenerationID == "" || reclaimed.provenance.GenerationID == initial.provenance.GenerationID || reclaimed.provenance.RecoveredGenerationID != initial.provenance.GenerationID {
		t.Fatalf("reclaimed generation observation = %+v, initial = %+v", reclaimed, initial)
	}

	var winner sqliteWorkflowHandlerObservation
	select {
	case winner = <-attemptOneWinner:
	case observation := <-unexpectedCall:
		t.Fatalf("unexpected workflow handler call before attempt-one winner: %+v", observation)
	case <-time.After(5 * time.Second):
		t.Fatal("application retry did not reach attempt-one winner")
	}
	if !winner.present || winner.attempt != 1 || winner.provenance.Recovered || winner.provenance.RecoveredGenerationID != "" || winner.provenance.GenerationID == "" || winner.provenance.GenerationID == reclaimed.provenance.GenerationID {
		t.Fatalf("attempt-one winner observation = %+v, reclaimed = %+v", winner, reclaimed)
	}
	select {
	case <-recorder.settlement:
	case observation := <-unexpectedCall:
		t.Fatalf("unexpected workflow handler call before winner settlement failure: %+v", observation)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for attempt-one finalization failure")
	}
	if handlerCalls.Load() != 3 {
		t.Fatalf("handler calls before winner recovery = %d, want 3", handlerCalls.Load())
	}
	state, err := store.GetChain(context.Background(), workflowID)
	if err != nil {
		t.Fatalf("get later-winner chain before recovery: %v", err)
	}
	if !state.Completed || state.Failed {
		t.Fatalf("later-winner chain before recovery = %+v, want completed success", state)
	}
	if recorder.count(queue.EventJobSucceeded, jobType) != 0 || recorder.count(queue.EventChainCompleted, jobType) != 0 {
		t.Fatal("winner facts published before durable queue finalization")
	}
	var (
		queueState      string
		processingToken sql.NullString
		attempt         int
	)
	if err := queueDB.QueryRow(`SELECT state, processing_token, attempt FROM queue_jobs WHERE queue_name=?`, queueName).Scan(&queueState, &processingToken, &attempt); err != nil {
		t.Fatalf("query attempt-one winner row: %v", err)
	}
	if queueState != "processing" || !processingToken.Valid || processingToken.String != winner.provenance.GenerationID || attempt != 1 {
		t.Fatalf("attempt-one winner row = state:%q token:%q valid:%t attempt:%d, want processing generation %q at attempt 1", queueState, processingToken.String, processingToken.Valid, attempt, winner.provenance.GenerationID)
	}

	if _, err := execSQLiteIntegrationEventually(queueDB, "DROP TRIGGER "+triggerName); err != nil {
		t.Fatalf("drop later-winner finalization trigger: %v", err)
	}
	result, err = execSQLiteIntegrationEventually(queueDB, `UPDATE queue_jobs SET processing_started_at=1 WHERE queue_name=? AND state='processing' AND attempt=1`, queueName)
	if err != nil {
		t.Fatalf("age attempt-one winner for recovery: %v", err)
	}
	if rows, rowsErr := result.RowsAffected(); rowsErr != nil || rows != 1 {
		t.Fatalf("aged attempt-one winner rows = %d, error %v; want 1", rows, rowsErr)
	}
	waitForObservabilityScenario(t, "sqlite_later_workflow_winner_receipt_recovery", 5*time.Second, func() bool {
		return recorder.count(queue.EventJobSucceeded, jobType) == 1 && recorder.count(queue.EventChainCompleted, jobType) == 1
	})
	if handlerCalls.Load() != 3 {
		t.Fatalf("handler calls after receipt-backed winner recovery = %d, want 3", handlerCalls.Load())
	}
	select {
	case observation := <-unexpectedCall:
		t.Fatalf("receipt-backed winner recovery executed application code: %+v", observation)
	default:
	}
	succeeded, ok := recorder.first(queue.EventJobSucceeded, jobType)
	if !ok || succeeded.Attempt != 1 || succeeded.EventID == "" {
		t.Fatalf("recovered attempt-one success = %+v present:%t", succeeded, ok)
	}
	var remaining int
	if err := queueDB.QueryRow(`SELECT COUNT(*) FROM queue_jobs WHERE queue_name=?`, queueName).Scan(&remaining); err != nil {
		t.Fatalf("count recovered later-winner rows: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("recovered later-winner rows = %d, want 0", remaining)
	}
	if recorder.count(queue.EventJobFailed, jobType) != 0 || recorder.count(queue.EventChainFailed, jobType) != 0 {
		t.Fatal("later-winner recovery published contradictory failure facts")
	}

	releaseFirstOnce.Do(func() { close(releaseFirst) })
	select {
	case <-firstReturned:
	case <-time.After(5 * time.Second):
		t.Fatal("superseded initial handler did not return")
	}
	waitForObservabilityScenario(t, "sqlite_superseded_initial_workflow_settlement", 5*time.Second, func() bool {
		return recorder.count(queue.EventSettlementFailed, jobType) == 2
	})
	if handlerCalls.Load() != 3 || recorder.count(queue.EventJobSucceeded, jobType) != 1 || recorder.count(queue.EventChainCompleted, jobType) != 1 || recorder.count(queue.EventJobFailed, jobType) != 0 || recorder.count(queue.EventChainFailed, jobType) != 0 {
		t.Fatalf("facts changed after superseded generation returned: calls:%d succeeded:%d completed:%d job_failed:%d chain_failed:%d", handlerCalls.Load(), recorder.count(queue.EventJobSucceeded, jobType), recorder.count(queue.EventChainCompleted, jobType), recorder.count(queue.EventJobFailed, jobType), recorder.count(queue.EventChainFailed, jobType))
	}
}

// installDatabaseFinalizationFailure installs a queue-scoped delete fault and
// returns an idempotent cleanup closure for the selected SQL dialect.
func installDatabaseFinalizationFailure(t *testing.T, backend string, db *sql.DB, queueName string) func() error {
	t.Helper()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	triggerName := "queue_receipt_delete_" + suffix
	functionName := "queue_receipt_delete_fn_" + suffix
	switch backend {
	case testenv.BackendMySQL:
		blockerTable := "queue_receipt_block_" + suffix
		constraintName := blockerTable + "_fk"
		statement := fmt.Sprintf(`CREATE TABLE %s (
    queue_job_id BIGINT NOT NULL PRIMARY KEY,
    CONSTRAINT %s FOREIGN KEY (queue_job_id) REFERENCES queue_jobs(id)
) ENGINE=InnoDB`, blockerTable, constraintName)
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("create MySQL workflow receipt blocker: %v", err)
		}
		result, err := db.Exec(fmt.Sprintf(`INSERT INTO %s (queue_job_id)
SELECT id FROM queue_jobs WHERE queue_name=?`, blockerTable), queueName)
		if err != nil {
			_, _ = db.Exec("DROP TABLE IF EXISTS " + blockerTable)
			t.Fatalf("attach MySQL workflow receipt blocker: %v", err)
		}
		if rows, rowsErr := result.RowsAffected(); rowsErr != nil || rows != 1 {
			_, _ = db.Exec("DROP TABLE IF EXISTS " + blockerTable)
			t.Fatalf("attached MySQL workflow receipt blockers = %d, error %v; want 1", rows, rowsErr)
		}
		return func() error {
			_, err := db.Exec("DROP TABLE IF EXISTS " + blockerTable)
			return err
		}
	case testenv.BackendPostgres:
		function := fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.queue_name = '%s' THEN
        RAISE EXCEPTION 'forced workflow receipt finalization failure';
    END IF;
    RETURN OLD;
END;
$$`, functionName, queueName)
		if _, err := db.Exec(function); err != nil {
			t.Fatalf("create PostgreSQL workflow receipt trigger function: %v", err)
		}
		trigger := fmt.Sprintf(`CREATE TRIGGER %s BEFORE DELETE ON queue_jobs FOR EACH ROW EXECUTE FUNCTION %s()`, triggerName, functionName)
		if _, err := db.Exec(trigger); err != nil {
			_, _ = db.Exec("DROP FUNCTION IF EXISTS " + functionName + "()")
			t.Fatalf("create PostgreSQL workflow receipt trigger: %v", err)
		}
		return func() error {
			if _, err := db.Exec("DROP TRIGGER IF EXISTS " + triggerName + " ON queue_jobs"); err != nil {
				return err
			}
			_, err := db.Exec("DROP FUNCTION IF EXISTS " + functionName + "()")
			return err
		}
	default:
		t.Fatalf("unsupported workflow receipt fault backend %q", backend)
		return func() error { return nil }
	}
}

// runDatabaseWorkflowReceiptRecovery proves MySQL and PostgreSQL preserve the
// same generation-to-transition receipt contract already exercised on SQLite.
func runDatabaseWorkflowReceiptRecovery[T any](t *testing.T, backend, driverName, dsn string, runtimeCfg T) {
	t.Helper()
	db, err := sql.Open(driverName, dsn)
	if err != nil {
		t.Fatalf("open %s workflow receipt database: %v", backend, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := queue.NewSQLStore(queue.SQLStoreConfig{DB: db, DriverName: driverName, AutoMigrate: true})
	if err != nil {
		t.Fatalf("new %s workflow receipt store: %v", backend, err)
	}

	queueName := fmt.Sprintf("workflow_receipt_%s_%d", backend, time.Now().UnixNano())
	jobType := "job:db:workflow-receipt:" + backend
	recorder := &databaseSettlementRecorder{settlement: make(chan struct{})}
	runtimeCfg = withDefaultQueue(withDBRecoveryPolicy(runtimeCfg, 10*time.Millisecond, 30*time.Second), queueName)
	runtime, err := testenv.NewQueue(runtimeCfg, queue.WithStore(store), queue.WithObserver(recorder), queue.WithWorkers(1))
	if err != nil {
		t.Fatalf("new %s workflow receipt runtime: %v", backend, err)
	}
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = runtime.Shutdown(shutdownCtx)
	})

	var (
		handlerCalls       atomic.Int64
		handlerStartedOnce sync.Once
		releaseHandlerOnce sync.Once
	)
	handlerStarted := make(chan struct{})
	releaseHandler := make(chan struct{})
	t.Cleanup(func() { releaseHandlerOnce.Do(func() { close(releaseHandler) }) })
	runtime.Register(jobType, func(context.Context, queue.Message) error {
		handlerCalls.Add(1)
		handlerStartedOnce.Do(func() { close(handlerStarted) })
		<-releaseHandler
		return nil
	})
	if err := runtime.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start %s workflow receipt runtime: %v", backend, err)
	}
	workflowID, err := runtime.Chain(queue.NewJob(jobType).OnQueue(queueName)).Dispatch(context.Background())
	if err != nil {
		t.Fatalf("dispatch %s workflow receipt chain: %v", backend, err)
	}
	select {
	case <-handlerStarted:
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s workflow handler before installing finalization fault", backend)
	}
	dropFault := installDatabaseFinalizationFailure(t, backend, db, queueName)
	t.Cleanup(func() { _ = dropFault() })
	releaseHandlerOnce.Do(func() { close(releaseHandler) })
	select {
	case <-recorder.settlement:
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s workflow finalization failure", backend)
	}
	state, err := store.GetChain(context.Background(), workflowID)
	if err != nil || !state.Completed || state.Failed {
		t.Fatalf("%s committed workflow state = %+v, err:%v", backend, state, err)
	}
	if handlerCalls.Load() != 1 || recorder.count(queue.EventJobSucceeded, jobType) != 0 || recorder.count(queue.EventChainCompleted, jobType) != 0 {
		t.Fatalf("%s pre-recovery calls/success/completion = %d/%d/%d, want 1/0/0", backend, handlerCalls.Load(), recorder.count(queue.EventJobSucceeded, jobType), recorder.count(queue.EventChainCompleted, jobType))
	}
	if err := dropFault(); err != nil {
		t.Fatalf("drop %s workflow receipt fault: %v", backend, err)
	}

	placeholder := "?"
	if backend == testenv.BackendPostgres {
		placeholder = "$1"
	}
	ageQuery := `UPDATE queue_jobs SET processing_started_at=1 WHERE queue_name=` + placeholder + ` AND state='processing'`
	result, err := db.Exec(ageQuery, queueName)
	if err != nil {
		t.Fatalf("age %s workflow receipt row: %v", backend, err)
	}
	if rows, rowsErr := result.RowsAffected(); rowsErr != nil || rows != 1 {
		t.Fatalf("aged %s workflow rows = %d, error %v; want 1", backend, rows, rowsErr)
	}
	deadline := time.Now().Add(10 * time.Second)
	recovered := false
	for time.Now().Before(deadline) {
		if recorder.count(queue.EventJobSucceeded, jobType) == 1 && recorder.count(queue.EventChainCompleted, jobType) == 1 {
			recovered = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !recovered {
		var (
			queueState      string
			processingToken sql.NullString
			attempt         int
			receiptCount    int
			receiptOwner    sql.NullString
		)
		rowQuery := `SELECT state, processing_token, attempt FROM queue_jobs WHERE queue_name=` + placeholder
		rowErr := db.QueryRow(rowQuery, queueName).Scan(&queueState, &processingToken, &attempt)
		receiptQuery := `SELECT COUNT(*), MAX(owner_delivery_id) FROM bus_workflow_transition_receipts WHERE workflow_kind='chain' AND workflow_id=` + placeholder
		receiptErr := db.QueryRow(receiptQuery, workflowID).Scan(&receiptCount, &receiptOwner)
		recorder.mu.Lock()
		events := append([]queue.Event(nil), recorder.events...)
		recorder.mu.Unlock()
		t.Fatalf("%s receipt recovery timed out: handler_calls=%d queue_state=%q processing_token=%q attempt=%d row_error=%v receipt_count=%d receipt_owner=%q receipt_error=%v events=%+v",
			backend, handlerCalls.Load(), queueState, processingToken.String, attempt, rowErr, receiptCount, receiptOwner.String, receiptErr, events)
	}
	if handlerCalls.Load() != 1 {
		t.Fatalf("%s receipt recovery handler calls = %d, want 1", backend, handlerCalls.Load())
	}
	succeeded, ok := recorder.first(queue.EventJobSucceeded, jobType)
	if !ok || succeeded.Attempt != 0 || succeeded.EventID == "" {
		t.Fatalf("%s recovered success = %+v present:%t", backend, succeeded, ok)
	}
	var remaining int
	countQuery := `SELECT COUNT(*) FROM queue_jobs WHERE queue_name=` + placeholder
	if err := db.QueryRow(countQuery, queueName).Scan(&remaining); err != nil {
		t.Fatalf("count %s recovered workflow rows: %v", backend, err)
	}
	if remaining != 0 {
		t.Fatalf("%s recovered workflow rows = %d, want 0", backend, remaining)
	}
}

// runDatabaseConcurrentBatchReceiptOwnership races distinct receipt-backed
// members through fail-fast completion so only the parent transition winner can
// own terminal facts across the server SQL dialects.
func runDatabaseConcurrentBatchReceiptOwnership[T any](t *testing.T, backend, driverName, dsn string, runtimeCfg T) {
	t.Helper()
	db, err := sql.Open(driverName, dsn)
	if err != nil {
		t.Fatalf("open %s concurrent batch receipt database: %v", backend, err)
	}
	db.SetMaxOpenConns(32)
	t.Cleanup(func() { _ = db.Close() })
	store, err := queue.NewSQLStore(queue.SQLStoreConfig{DB: db, DriverName: driverName, AutoMigrate: true})
	if err != nil {
		t.Fatalf("new %s concurrent batch receipt store: %v", backend, err)
	}

	const memberCount = 12
	queueName := fmt.Sprintf("batch_receipt_race_%s_%d", backend, time.Now().UnixNano())
	jobType := "job:db:batch-receipt-race:" + backend
	recorder := &databaseSettlementRecorder{settlement: make(chan struct{})}
	runtimeCfg = withDefaultQueue(runtimeCfg, queueName)
	started := make(chan struct{})
	release := make(chan struct{})
	var (
		handlerCalls atomic.Int64
		startedOnce  sync.Once
		releaseOnce  sync.Once
	)
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	handler := func(context.Context, queue.Message) error {
		if handlerCalls.Add(1) == memberCount {
			startedOnce.Do(func() { close(started) })
		}
		<-release
		return queue.Permanent(errors.New("concurrent fail-fast member failure"))
	}
	runtimes := make([]*queue.Queue, memberCount)
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		for _, runtime := range runtimes {
			if runtime != nil {
				_ = runtime.Shutdown(shutdownCtx)
			}
		}
	})
	for worker := range memberCount {
		runtimes[worker], err = testenv.NewQueue(runtimeCfg, queue.WithStore(store), queue.WithObserver(recorder), queue.WithWorkers(1))
		if err != nil {
			t.Fatalf("new %s concurrent batch receipt runtime %d: %v", backend, worker, err)
		}
		runtimes[worker].Register(jobType, handler)
		if err := runtimes[worker].StartWorkers(context.Background()); err != nil {
			t.Fatalf("start %s concurrent batch receipt runtime %d: %v", backend, worker, err)
		}
	}
	jobs := make([]queue.Job, memberCount)
	for member := range memberCount {
		jobs[member] = queue.NewJob(jobType).Payload(map[string]int{"member": member}).OnQueue(queueName)
	}
	batchID, err := runtimes[0].Batch(jobs...).OnQueue(queueName).Dispatch(context.Background())
	if err != nil {
		t.Fatalf("dispatch %s concurrent receipt batch: %v", backend, err)
	}
	select {
	case <-started:
	case <-time.After(20 * time.Second):
		t.Fatalf("timed out waiting for %s concurrent batch members; calls=%d", backend, handlerCalls.Load())
	}
	releaseOnce.Do(func() { close(release) })

	deadline := time.Now().Add(20 * time.Second)
	var state queue.BatchState
	for time.Now().Before(deadline) {
		state, err = store.GetBatch(context.Background(), batchID)
		if err == nil && state.Pending == 0 && state.Processed == memberCount && recorder.count(queue.EventBatchFailed, jobType) == 1 && recorder.count(queue.EventBatchCancelled, jobType) == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil || state.Pending != 0 || state.Processed != memberCount || state.Failed != memberCount || !state.Completed || !state.Cancelled {
		t.Fatalf("%s concurrent receipt batch state = %+v, err:%v", backend, state, err)
	}
	if handlerCalls.Load() != memberCount {
		t.Fatalf("%s concurrent receipt handler calls = %d, want %d without receipt-conflict redelivery", backend, handlerCalls.Load(), memberCount)
	}
	if failed, cancelled, completed := recorder.count(queue.EventBatchFailed, jobType), recorder.count(queue.EventBatchCancelled, jobType), recorder.count(queue.EventBatchCompleted, jobType); failed != 1 || cancelled != 1 || completed != 0 {
		t.Fatalf("%s terminal batch facts = failed:%d cancelled:%d completed:%d, want 1/1/0", backend, failed, cancelled, completed)
	}

	placeholder := "?"
	if backend == testenv.BackendPostgres {
		placeholder = "$1"
	}
	var receiptCount int
	countQuery := `SELECT COUNT(*) FROM bus_workflow_transition_receipts WHERE workflow_kind='batch' AND workflow_id=` + placeholder
	if err := db.QueryRow(countQuery, batchID).Scan(&receiptCount); err != nil {
		t.Fatalf("count %s concurrent batch receipts: %v", backend, err)
	}
	if receiptCount != memberCount+1 {
		t.Fatalf("%s concurrent batch receipts = %d, want %d member plus aggregate rows", backend, receiptCount, memberCount+1)
	}
	var aggregateOwner, aggregateJob string
	var aggregateCompleted, aggregateCancelled int
	aggregateQuery := `SELECT owner_delivery_id, job_id, aggregate_completed, aggregate_cancelled FROM bus_workflow_transition_receipts WHERE workflow_kind='batch' AND member_id='' AND workflow_id=` + placeholder
	if err := db.QueryRow(aggregateQuery, batchID).Scan(&aggregateOwner, &aggregateJob, &aggregateCompleted, &aggregateCancelled); err != nil {
		t.Fatalf("read %s aggregate batch receipt: %v", backend, err)
	}
	if aggregateOwner == "" || aggregateJob == "" || aggregateCompleted != 1 || aggregateCancelled != 1 {
		t.Fatalf("%s aggregate receipt = owner:%q job:%q completed:%d cancelled:%d", backend, aggregateOwner, aggregateJob, aggregateCompleted, aggregateCancelled)
	}
	memberPlaceholder := "?"
	jobPlaceholder := "?"
	if backend == testenv.BackendPostgres {
		memberPlaceholder = "$2"
		jobPlaceholder = "$3"
	}
	var matchingMemberReceipts int
	memberQuery := `SELECT COUNT(*) FROM bus_workflow_transition_receipts WHERE workflow_kind='batch' AND member_id<>'' AND workflow_id=` + placeholder + ` AND owner_delivery_id=` + memberPlaceholder + ` AND job_id=` + jobPlaceholder
	if err := db.QueryRow(memberQuery, batchID, aggregateOwner, aggregateJob).Scan(&matchingMemberReceipts); err != nil {
		t.Fatalf("match %s aggregate receipt owner to member: %v", backend, err)
	}
	if matchingMemberReceipts != 1 {
		t.Fatalf("%s member receipts matching aggregate owner = %d, want 1", backend, matchingMemberReceipts)
	}
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

// historicalDatabaseUniqueKey reproduces the pre-version SQL lock identity so
// integration coverage can seed records exactly as an older producer did.
func historicalDatabaseUniqueKey(job queue.Job, queueName string) string {
	digest := sha256.Sum256(append([]byte(queueName+":"+job.Type+":"), job.PayloadBytes()...))
	return hex.EncodeToString(digest[:])
}

// rebindHistoricalDatabaseQuery preserves the placeholder behavior used by
// the pre-version SQL producer without importing driver internals into the test.
func rebindHistoricalDatabaseQuery(query, driverName string) string {
	if driverName != "pgx" && driverName != testenv.BackendPostgres {
		return query
	}
	var rebound strings.Builder
	argument := 1
	for _, char := range query {
		if char == '?' {
			fmt.Fprintf(&rebound, "$%d", argument)
			argument++
			continue
		}
		rebound.WriteRune(char)
	}
	return rebound.String()
}

// historicalDatabaseUniqueConflict matches the constraint classification used
// by the pre-version SQL producer so the integration path is behaviorally exact.
func historicalDatabaseUniqueConflict(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "duplicate") ||
		strings.Contains(message, "unique constraint") ||
		strings.Contains(message, "unique violation")
}

// dispatchHistoricalDatabaseJob reproduces origin/main's legacy claim and
// queue-row writes on a dedicated connection for mixed-version race coverage.
func dispatchHistoricalDatabaseJob(ctx context.Context, conn *sql.Conn, driverName string, job queue.Job) error {
	options := queue.DriverOptions(job)
	now := time.Now()
	expiresAt := now.Add(options.UniqueTTL).UnixMilli()
	legacyKey := historicalDatabaseUniqueKey(job, options.QueueName)
	insertLock := rebindHistoricalDatabaseQuery(
		`INSERT INTO queue_unique_locks(lock_key, expires_at) VALUES(?, ?)`,
		driverName,
	)
	if _, err := conn.ExecContext(ctx, insertLock, legacyKey, expiresAt); err != nil {
		if !historicalDatabaseUniqueConflict(err) {
			return err
		}
		updateLock := rebindHistoricalDatabaseQuery(
			`UPDATE queue_unique_locks SET expires_at=? WHERE lock_key=? AND expires_at <= ?`,
			driverName,
		)
		result, updateErr := conn.ExecContext(ctx, updateLock, expiresAt, legacyKey, now.UnixMilli())
		if updateErr != nil {
			return updateErr
		}
		rows, _ := result.RowsAffected()
		if rows != 1 {
			return queue.ErrDuplicate
		}
	}

	payload := job.PayloadBytes()
	if payload == nil {
		payload = []byte{}
	}
	availableAt := now.Add(options.Delay)
	maxRetry := 0
	if options.MaxRetry != nil {
		maxRetry = *options.MaxRetry
	}
	backoffMillis := int64(0)
	if options.Backoff != nil && *options.Backoff > 0 {
		backoffMillis = options.Backoff.Milliseconds()
	}
	var timeoutSeconds any
	if options.Timeout != nil {
		timeoutSeconds = max(1, int64(math.Ceil(options.Timeout.Seconds())))
	}
	insertJob := rebindHistoricalDatabaseQuery(
		`INSERT INTO queue_jobs
        (queue_name, job_type, payload, timeout_seconds, max_retry, backoff_millis, attempt, available_at, state, created_at, updated_at)
        VALUES (?, ?, ?, ?, ?, ?, 0, ?, 'pending', ?, ?)`,
		driverName,
	)
	_, err := conn.ExecContext(
		ctx,
		insertJob,
		options.QueueName,
		job.Type,
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

// runDatabaseUniqueKeyTransitionIntegration proves every SQL dialect honors
// outstanding historical claims and atomically rolls back the companion key
// when the canonical identity collides.
func runDatabaseUniqueKeyTransitionIntegration(t *testing.T, cfg queue.DatabaseConfig) {
	t.Helper()
	provisionDatabaseIntegrationSchema(t, cfg)
	db, err := sql.Open(cfg.DriverName, cfg.DSN)
	if err != nil {
		t.Fatalf("open %s uniqueness transition database: %v", cfg.DriverName, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, table := range []string{"queue_jobs", "queue_unique_locks"} {
		if _, err := db.Exec("DELETE FROM " + table); err != nil {
			t.Fatalf("clear %s before uniqueness transition: %v", table, err)
		}
	}
	t.Cleanup(func() {
		for _, table := range []string{"queue_jobs", "queue_unique_locks"} {
			_, _ = db.Exec("DELETE FROM " + table)
		}
	})

	firstPlaceholder := "?"
	secondPlaceholder := "?"
	if cfg.DriverName == "pgx" || cfg.DriverName == testenv.BackendPostgres {
		firstPlaceholder = "$1"
		secondPlaceholder = "$2"
	}
	insertLock := func(key string, expiresAt int64) {
		t.Helper()
		query := `INSERT INTO queue_unique_locks(lock_key, expires_at) VALUES (` + firstPlaceholder + `, ` + secondPlaceholder + `)`
		if _, err := db.Exec(query, key, expiresAt); err != nil {
			t.Fatalf("seed uniqueness transition lock %q: %v", key, err)
		}
	}
	lockCount := func(key string) int {
		t.Helper()
		var count int
		query := `SELECT COUNT(*) FROM queue_unique_locks WHERE lock_key=` + firstPlaceholder
		if err := db.QueryRow(query, key).Scan(&count); err != nil {
			t.Fatalf("count uniqueness transition lock %q: %v", key, err)
		}
		return count
	}
	jobCount := func(jobType string) int {
		t.Helper()
		var count int
		query := `SELECT COUNT(*) FROM queue_jobs WHERE job_type=` + firstPlaceholder
		if err := db.QueryRow(query, jobType).Scan(&count); err != nil {
			t.Fatalf("count uniqueness transition jobs %q: %v", jobType, err)
		}
		return count
	}
	producer := newDatabaseQueueIntegration(t, cfg)

	t.Run("legacy_outstanding", func(t *testing.T) {
		job := queue.NewJob("job:db:unique:legacy-outstanding").
			Payload([]byte("same logical work")).
			OnQueue("default").
			UniqueFor(time.Minute)
		legacyKey := historicalDatabaseUniqueKey(job, "default")
		canonicalKey := queue.DriverUniqueKey(job, "default")
		insertLock(legacyKey, time.Now().Add(time.Hour).UnixMilli())

		if err := producer.Dispatch(job); !errors.Is(err, queue.ErrDuplicate) {
			t.Fatalf("dispatch with outstanding legacy lock = %v, want ErrDuplicate", err)
		}
		if lockCount(canonicalKey) != 0 || jobCount(job.Type) != 0 {
			t.Fatal("legacy collision committed a canonical lock or queue row")
		}
	})

	t.Run("legacy_expired", func(t *testing.T) {
		job := queue.NewJob("job:db:unique:legacy-expired").
			Payload([]byte("same logical work")).
			OnQueue("default").
			UniqueFor(time.Minute)
		legacyKey := historicalDatabaseUniqueKey(job, "default")
		canonicalKey := queue.DriverUniqueKey(job, "default")
		insertLock(legacyKey, 0)

		if err := producer.Dispatch(job); err != nil {
			t.Fatalf("dispatch with expired legacy lock: %v", err)
		}
		if lockCount(legacyKey) != 1 || lockCount(canonicalKey) != 1 || jobCount(job.Type) != 1 {
			t.Fatal("expired legacy lock did not commit both identities with the queue row")
		}
		var expiresAt int64
		query := `SELECT expires_at FROM queue_unique_locks WHERE lock_key=` + firstPlaceholder
		if err := db.QueryRow(query, legacyKey).Scan(&expiresAt); err != nil {
			t.Fatalf("read renewed legacy lock: %v", err)
		}
		if expiresAt <= 0 {
			t.Fatalf("renewed legacy lock expiry = %d, want positive database time", expiresAt)
		}
	})

	t.Run("canonical_outstanding", func(t *testing.T) {
		job := queue.NewJob("job:db:unique:canonical-outstanding").
			Payload([]byte("same logical work")).
			OnQueue("default").
			UniqueFor(time.Minute)
		legacyKey := historicalDatabaseUniqueKey(job, "default")
		canonicalKey := queue.DriverUniqueKey(job, "default")
		insertLock(canonicalKey, time.Now().Add(time.Hour).UnixMilli())

		if err := producer.Dispatch(job); !errors.Is(err, queue.ErrDuplicate) {
			t.Fatalf("dispatch with outstanding canonical lock = %v, want ErrDuplicate", err)
		}
		if lockCount(legacyKey) != 0 || jobCount(job.Type) != 0 {
			t.Fatal("canonical collision committed the preceding legacy lock or queue row")
		}
	})

	t.Run("mixed_producer_race", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		legacyConn, err := db.Conn(ctx)
		if err != nil {
			t.Fatalf("open dedicated %s legacy producer connection: %v", cfg.DriverName, err)
		}
		defer legacyConn.Close()
		if cfg.DriverName == testenv.BackendSQLite {
			if _, err := legacyConn.ExecContext(ctx, `PRAGMA busy_timeout=5000`); err != nil {
				t.Fatalf("configure legacy SQLite producer lock wait: %v", err)
			}
		}

		const rounds = 8
		for round := range rounds {
			job := queue.NewJob(fmt.Sprintf("job:db:unique:mixed-producer:%d", round)).
				Payload([]byte("same logical work")).
				OnQueue("default").
				UniqueFor(time.Minute)
			start := make(chan struct{})
			results := make(chan error, 2)
			go func() {
				<-start
				results <- dispatchHistoricalDatabaseJob(ctx, legacyConn, cfg.DriverName, job)
			}()
			go func() {
				<-start
				results <- producer.WithContext(ctx).Dispatch(job)
			}()
			close(start)

			accepted := 0
			duplicates := 0
			for range 2 {
				select {
				case dispatchErr := <-results:
					switch {
					case dispatchErr == nil:
						accepted++
					case errors.Is(dispatchErr, queue.ErrDuplicate):
						duplicates++
					default:
						t.Fatalf("round %d mixed-version dispatch error: %v", round, dispatchErr)
					}
				case <-ctx.Done():
					t.Fatalf("round %d mixed-version dispatch timed out: %v", round, ctx.Err())
				}
			}
			if accepted != 1 || duplicates != 1 {
				t.Fatalf("round %d mixed-version accepted/duplicate results = %d/%d, want 1/1", round, accepted, duplicates)
			}
			if rows := jobCount(job.Type); rows != 1 {
				t.Fatalf("round %d mixed-version queue rows = %d, want 1", round, rows)
			}
		}
	})
}

// managedDatabaseRuntimeConfig preserves each integration suite's physical
// database while making external schema ownership explicit for the runtime
// under test.
func managedDatabaseRuntimeConfig(cfg queue.DatabaseConfig, queueName string) any {
	switch cfg.DriverName {
	case testenv.BackendMySQL:
		runtimeCfg := withDefaultQueue(withDBHandle(mysqlCfg(cfg.DSN), cfg.DB), queueName)
		runtimeCfg.DisableAutoMigrate = true
		return runtimeCfg
	case "pgx", testenv.BackendPostgres:
		runtimeCfg := withDefaultQueue(withDBHandle(postgresCfg(cfg.DSN), cfg.DB), queueName)
		runtimeCfg.DisableAutoMigrate = true
		return runtimeCfg
	case testenv.BackendSQLite:
		runtimeCfg := withDefaultQueue(withDBHandle(sqliteCfg(cfg.DSN), cfg.DB), queueName)
		runtimeCfg.DisableAutoMigrate = true
		return runtimeCfg
	default:
		return nil
	}
}

// provisionDatabaseIntegrationSchema installs the canonical dialect schema
// through a distinct auto-migrating runtime before managed-mode validation.
func provisionDatabaseIntegrationSchema(t *testing.T, cfg queue.DatabaseConfig) {
	t.Helper()
	bootstrap := newDatabaseQueueIntegration(t, cfg)
	if err := bootstrap.StartWorkers(context.Background()); err != nil {
		t.Fatalf("provision %s managed schema: %v", cfg.DriverName, err)
	}
	if err := bootstrap.Shutdown(context.Background()); err != nil {
		t.Fatalf("close %s managed schema bootstrap: %v", cfg.DriverName, err)
	}
}

// runDatabaseManagedSchemaIntegration proves a canonical externally
// provisioned schema supports readiness, uniqueness, dispatch, and processing
// through the same managed runtime path on every SQL dialect.
func runDatabaseManagedSchemaIntegration(t *testing.T, name string, cfg queue.DatabaseConfig) {
	t.Helper()
	provisionDatabaseIntegrationSchema(t, cfg)
	resetQueueTables(t, cfg)
	queueName := name + "-managed-schema"
	runtimeCfg := managedDatabaseRuntimeConfig(cfg, queueName)
	if runtimeCfg == nil {
		t.Fatalf("unsupported managed database driver %q", cfg.DriverName)
	}
	runtime, err := testenv.NewQueue(runtimeCfg)
	if err != nil {
		t.Fatalf("new %s managed-schema runtime: %v", name, err)
	}
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = runtime.Shutdown(shutdownCtx)
	})

	jobType := "job:db:managed-schema:" + name
	processed := make(chan queue.Message, 2)
	runtime.Register(jobType, func(_ context.Context, message queue.Message) error {
		processed <- message
		return nil
	})
	if err := runtime.Ready(context.Background()); err != nil {
		t.Fatalf("%s managed schema readiness: %v", name, err)
	}
	if err := runtime.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start %s managed-schema runtime: %v", name, err)
	}
	job := queue.NewJob(jobType).
		Payload([]byte(`{"managed":true}`)).
		OnQueue(queueName).
		UniqueFor(time.Minute)
	if _, err := runtime.Dispatch(job); err != nil {
		t.Fatalf("dispatch through %s managed schema: %v", name, err)
	}
	if _, err := runtime.Dispatch(job); !errors.Is(err, queue.ErrDuplicate) {
		t.Fatalf("duplicate dispatch through %s managed schema = %v, want ErrDuplicate", name, err)
	}
	select {
	case delivered := <-processed:
		if delivered.JobType != jobType || string(delivered.PayloadBytes()) != `{"managed":true}` {
			t.Fatalf("%s managed-schema delivery = type:%q payload:%q", name, delivered.JobType, delivered.PayloadBytes())
		}
	case <-time.After(15 * time.Second):
		logDatabaseQueueState(t, cfg, name+" managed-schema timeout")
		t.Fatalf("%s managed-schema runtime did not consume the dispatched job", name)
	}
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
	result, err := execSQLiteIntegrationEventually(db, `UPDATE queue_jobs SET processing_started_at=1 WHERE job_type=? AND state='processing'`, jobType)
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

	t.Run(name+"_unique_rolling_upgrade", func(t *testing.T) {
		runDatabaseUniqueKeyTransitionIntegration(t, cfg)
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
	t.Run("sqlite_managed_schema_dispatch_and_process", func(t *testing.T) {
		runDatabaseManagedSchemaIntegration(t, testenv.BackendSQLite, cfg)
	})

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

	t.Run("sqlite_managed_schema_fails_closed_then_recovers_after_provisioning", func(t *testing.T) {
		dsn := fmt.Sprintf("%s/queue-no-migrate-%d.db", t.TempDir(), time.Now().UnixNano())
		runtime, err := sqlitequeue.NewWithConfig(sqlitequeue.Config{
			DSN:                dsn,
			DisableAutoMigrate: true,
		})
		if err != nil {
			t.Fatalf("new no-migrate runtime: %v", err)
		}
		t.Cleanup(func() { _ = runtime.Shutdown(context.Background()) })
		processed := make(chan struct{}, 1)
		runtime.Register("job:db:managed-schema-retry", func(context.Context, queue.Message) error {
			processed <- struct{}{}
			return nil
		})
		if err := runtime.Ready(context.Background()); err == nil {
			t.Fatal("managed runtime reported ready before external schema provisioning")
		}
		if err := runtime.StartWorkers(context.Background()); err == nil {
			t.Fatal("managed runtime started workers before external schema provisioning")
		}

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
			t.Fatalf("managed readiness or startup created %d queue tables", tables)
		}

		prepareSQLiteIntegrationSchema(t, dsn)
		if err := runtime.Ready(context.Background()); err != nil {
			t.Fatalf("managed runtime readiness after external provisioning: %v", err)
		}
		if err := runtime.StartWorkers(context.Background()); err != nil {
			t.Fatalf("start same managed runtime after external provisioning: %v", err)
		}
		if _, err := runtime.Dispatch(queue.NewJob("job:db:managed-schema-retry").OnQueue("default")); err != nil {
			t.Fatalf("dispatch after external schema provisioning: %v", err)
		}
		select {
		case <-processed:
		case <-time.After(5 * time.Second):
			t.Fatal("same managed runtime did not consume after external schema provisioning")
		}
	})

	t.Run("sqlite_managed_schema_requires_processing_token", func(t *testing.T) {
		dsn := fmt.Sprintf("%s/queue-managed-missing-processing-token-%d.db", t.TempDir(), time.Now().UnixNano())
		prepareSQLiteIntegrationSchema(t, dsn)
		db, err := sql.Open(testenv.BackendSQLite, dsn)
		if err != nil {
			t.Fatalf("open managed schema database: %v", err)
		}
		if _, err := db.Exec(`ALTER TABLE queue_jobs DROP COLUMN processing_token`); err != nil {
			_ = db.Close()
			t.Fatalf("remove managed processing token column: %v", err)
		}
		if err := db.Close(); err != nil {
			t.Fatalf("close managed schema database: %v", err)
		}
		runtime, err := sqlitequeue.NewWithConfig(sqlitequeue.Config{
			DSN:                dsn,
			DisableAutoMigrate: true,
		})
		if err != nil {
			t.Fatalf("new managed schema runtime: %v", err)
		}
		t.Cleanup(func() { _ = runtime.Shutdown(context.Background()) })
		err = runtime.StartWorkers(context.Background())
		if err == nil || !strings.Contains(err.Error(), "missing required processing_token column") {
			t.Fatalf("managed schema startup error = %v", err)
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

	t.Run("sqlite_workflow_success_facts_recover_after_finalization_failure", func(t *testing.T) {
		for _, workflowKind := range []string{
			"chain",
			"chain_predecessor",
			"batch",
			"batch_predecessor",
		} {
			t.Run(workflowKind, func(t *testing.T) {
				runSQLiteWorkflowWinnerFactRecovery(t, workflowKind)
			})
		}
	})

	t.Run("sqlite_workflow_receipt_owner_survives_repeated_finalization_failure", func(t *testing.T) {
		runSQLiteRepeatedWorkflowSettlementRecovery(t)
	})

	t.Run("sqlite_failed_chain_recovery_archives_without_reexecution", func(t *testing.T) {
		runSQLiteFailedChainSettlementRecovery(t)
	})

	t.Run("sqlite_terminal_batch_completion_recovers_from_completing_member", func(t *testing.T) {
		runSQLiteTerminalBatchOwnerRecovery(t)
	})

	t.Run("sqlite_failed_batch_recovery_archives_without_reexecution", func(t *testing.T) {
		runSQLiteFailedBatchSettlementRecovery(t)
	})

	t.Run("sqlite_later_workflow_attempt_wins_then_recovers_without_reexecution", func(t *testing.T) {
		runSQLiteLaterWorkflowWinnerRecovery(t)
	})

	t.Run("sqlite_application_error_cannot_forge_recovery_proof", func(t *testing.T) {
		dsn := fmt.Sprintf("%s/queue-recovery-proof-collision-%d.db", t.TempDir(), time.Now().UnixNano())
		prepareSQLiteIntegrationSchema(t, dsn)
		const (
			queueName = "recovery-proof-collision"
			jobType   = "job:db:recovery-proof-collision"
		)
		runtimeCfg := withDefaultQueue(sqliteCfg(dsn), queueName)
		runtimeCfg.DisableAutoMigrate = true
		runtime, err := testenv.NewQueue(runtimeCfg, queue.WithWorkers(1))
		if err != nil {
			t.Fatalf("new recovery proof collision runtime: %v", err)
		}
		t.Cleanup(func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = runtime.Shutdown(shutdownCtx)
		})
		var calls atomic.Int64
		var forged atomic.Bool
		handlerDone := make(chan struct{})
		var handlerDoneOnce sync.Once
		runtime.Register(jobType, func(ctx context.Context, _ queue.Message) error {
			if calls.Add(1) == 1 {
				return errors.New("queue:internal:stale-processing-recovery:v1")
			}
			provenance, present := busruntime.DeliveryProvenanceFromContext(ctx)
			forged.Store(present && provenance.Recovered)
			handlerDoneOnce.Do(func() { close(handlerDone) })
			return nil
		})
		if err := runtime.StartWorkers(context.Background()); err != nil {
			t.Fatalf("start recovery proof collision runtime: %v", err)
		}
		if _, err := runtime.Dispatch(queue.NewJob(jobType).OnQueue(queueName).Retry(1)); err != nil {
			t.Fatalf("dispatch recovery proof collision job: %v", err)
		}
		select {
		case <-handlerDone:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for collision replay handler")
		}
		if forged.Load() {
			t.Fatal("application error text granted stale-processing recovery authority")
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
	t.Run("mysql_managed_schema_dispatch_and_process", func(t *testing.T) {
		runDatabaseManagedSchemaIntegration(t, testenv.BackendMySQL, cfg)
	})
	t.Run("mysql_workflow_receipt_recovery", func(t *testing.T) {
		runDatabaseWorkflowReceiptRecovery(t, testenv.BackendMySQL, testenv.BackendMySQL, cfg.DSN, mysqlCfg(cfg.DSN))
	})
	t.Run("mysql_concurrent_batch_receipt_owner", func(t *testing.T) {
		runDatabaseConcurrentBatchReceiptOwnership(t, testenv.BackendMySQL, testenv.BackendMySQL, cfg.DSN, mysqlCfg(cfg.DSN))
	})
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
	t.Run("postgres_managed_schema_dispatch_and_process", func(t *testing.T) {
		runDatabaseManagedSchemaIntegration(t, testenv.BackendPostgres, cfg)
	})
	t.Run("postgres_workflow_receipt_recovery", func(t *testing.T) {
		runDatabaseWorkflowReceiptRecovery(t, testenv.BackendPostgres, "pgx", cfg.DSN, postgresCfg(cfg.DSN))
	})
	t.Run("postgres_concurrent_batch_receipt_owner", func(t *testing.T) {
		runDatabaseConcurrentBatchReceiptOwnership(t, testenv.BackendPostgres, "pgx", cfg.DSN, postgresCfg(cfg.DSN))
	})
}
