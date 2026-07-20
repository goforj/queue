//go:build integration

package root_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goforj/queue"
	"github.com/goforj/queue/integration/testenv"
)

// newWorkflowStoreIntegration opens a caller-owned handle so each dialect
// contract can exercise multiple real connections without leaking ownership.
func newWorkflowStoreIntegration(t *testing.T, driverName, dsn string) queue.WorkflowStore {
	t.Helper()
	db, err := sql.Open(driverName, dsn)
	if err != nil {
		t.Fatalf("open workflow database: %v", err)
	}
	db.SetMaxOpenConns(32)
	t.Cleanup(func() { _ = db.Close() })
	store, err := queue.NewSQLStore(queue.SQLStoreConfig{
		DB:          db,
		DriverName:  driverName,
		AutoMigrate: true,
	})
	if err != nil {
		t.Fatalf("new workflow store: %v", err)
	}
	return store
}

// waitWorkflowStoreOperations bounds concurrent database probes so a locking
// regression fails near its source instead of consuming the global test timeout.
func waitWorkflowStoreOperations(t *testing.T, wg *sync.WaitGroup) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(35 * time.Second):
		t.Fatal("timed out waiting for concurrent workflow-store operations")
	}
}

// retryWorkflowStoreConflict models broker redelivery for database-selected
// deadlock victims while preserving every non-transient error verbatim.
func retryWorkflowStoreConflict(ctx context.Context, operation func() error) error {
	var lastErr error
	for range 10 {
		lastErr = operation()
		if lastErr == nil {
			return nil
		}
		message := strings.ToLower(lastErr.Error())
		if !strings.Contains(message, "deadlock") {
			return lastErr
		}
		select {
		case <-ctx.Done():
			return errors.Join(lastErr, ctx.Err())
		default:
		}
	}
	return lastErr
}

// settleWorkflowStoreBatchConcurrently creates one batch and races mixed
// outcomes so callers can assert both continuation and fail-fast policies.
func settleWorkflowStoreBatchConcurrently(t *testing.T, ctx context.Context, store queue.WorkflowStore, prefix string, allowFailures bool) queue.BatchState {
	t.Helper()
	const jobCount = 32
	jobs := make([]queue.BatchJob, jobCount)
	for i := range jobs {
		jobs[i] = queue.BatchJob{
			JobID: fmt.Sprintf("%s-member-%02d", prefix, i),
			Job:   queue.StoredJob{Type: "reports:member"},
		}
	}
	batchID := prefix + "-batch"
	if err := store.CreateBatch(ctx, queue.BatchRecord{
		BatchID:     batchID,
		DispatchID:  prefix + "-dispatch",
		AllowFailed: allowFailures,
		Jobs:        jobs,
		CreatedAt:   time.Now(),
	}); err != nil {
		t.Fatalf("create concurrent batch: %v", err)
	}
	start := make(chan struct{})
	errs := make(chan error, jobCount)
	var wg sync.WaitGroup
	for i, job := range jobs {
		wg.Add(1)
		go func(index int, member queue.BatchJob) {
			defer wg.Done()
			<-start
			var err error
			if index%2 == 0 {
				_, _, err = store.MarkBatchJobSucceeded(ctx, batchID, member.JobID)
			} else {
				_, _, err = store.MarkBatchJobFailed(ctx, batchID, member.JobID, errors.New("member failed"))
			}
			errs <- err
		}(i, job)
	}
	close(start)
	waitWorkflowStoreOperations(t, &wg)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("settle concurrent batch members: %v", err)
		}
	}
	state, err := store.GetBatch(ctx, batchID)
	if err != nil {
		t.Fatalf("get concurrent batch: %v", err)
	}
	return state
}

// settleWorkflowStoreTerminalMemberConcurrently races contradictory outcomes
// through aggregate completion and optional fail-fast cancellation.
func settleWorkflowStoreTerminalMemberConcurrently(t *testing.T, ctx context.Context, store queue.WorkflowStore, outcomes queue.WorkflowOutcomeStore, prefix string, allowFailures bool) queue.BatchState {
	t.Helper()
	batchID := prefix + "-batch"
	jobID := prefix + "-member"
	if err := store.CreateBatch(ctx, queue.BatchRecord{
		BatchID:     batchID,
		AllowFailed: allowFailures,
		Jobs:        []queue.BatchJob{{JobID: jobID}},
		CreatedAt:   time.Now(),
	}); err != nil {
		t.Fatalf("create terminal batch: %v", err)
	}
	start := make(chan struct{})
	errs := make(chan error, 32)
	var wg sync.WaitGroup
	for delivery := range 32 {
		outcome := queue.BatchJobSucceeded
		if delivery%2 == 0 {
			outcome = queue.BatchJobFailed
		}
		wg.Add(1)
		go func(outcome queue.BatchJobOutcome) {
			defer wg.Done()
			<-start
			errs <- retryWorkflowStoreConflict(ctx, func() error {
				_, _, err := outcomes.SettleBatchJob(ctx, batchID, jobID, outcome, errors.New("raced terminal outcome"))
				return err
			})
		}(outcome)
	}
	close(start)
	waitWorkflowStoreOperations(t, &wg)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("race terminal batch outcome: %v", err)
		}
	}
	state, err := store.GetBatch(ctx, batchID)
	if err != nil {
		t.Fatalf("get terminal batch: %v", err)
	}
	_, successOwned, err := outcomes.SettleBatchJob(ctx, batchID, jobID, queue.BatchJobSucceeded, nil)
	if err != nil {
		t.Fatalf("replay terminal success: %v", err)
	}
	_, failureOwned, err := outcomes.SettleBatchJob(ctx, batchID, jobID, queue.BatchJobFailed, errors.New("replayed terminal failure"))
	if err != nil {
		t.Fatalf("replay terminal failure: %v", err)
	}
	if successOwned == failureOwned || successOwned != (state.Failed == 0) {
		t.Fatalf("terminal outcome ownership = success:%t failure:%t state:%+v", successOwned, failureOwned, state)
	}
	return state
}

// runWorkflowStoreConcurrencyContract proves each supported SQL dialect uses
// the same atomic chain, batch, and callback state transitions.
func runWorkflowStoreConcurrencyContract(t *testing.T, driverName, dsn string) {
	t.Helper()
	store := newWorkflowStoreIntegration(t, driverName, dsn)
	outcomes, ok := store.(queue.WorkflowOutcomeStore)
	if !ok {
		t.Fatalf("built-in SQL store %T does not implement queue.WorkflowOutcomeStore", store)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	prefix := fmt.Sprintf("workflow-store-%d", time.Now().UnixNano())

	const jobCount = 32
	distinctState := settleWorkflowStoreBatchConcurrently(t, ctx, store, prefix+"-allow-failures", true)
	if distinctState.Pending != 0 || distinctState.Processed != jobCount || distinctState.Failed != jobCount/2 || !distinctState.Completed || distinctState.Cancelled {
		t.Fatalf("distinct batch state = %+v, want exact aggregate counters", distinctState)
	}
	failFastState := settleWorkflowStoreBatchConcurrently(t, ctx, store, prefix+"-fail-fast", false)
	if failFastState.Pending != 0 || failFastState.Processed != jobCount || failFastState.Failed != jobCount/2 || !failFastState.Completed || !failFastState.Cancelled {
		t.Fatalf("fail-fast batch state = %+v, want exact counters and cancellation", failFastState)
	}

	duplicateBatchID := prefix + "-duplicate-batch"
	duplicateJobID := prefix + "-duplicate-member"
	if err := store.CreateBatch(ctx, queue.BatchRecord{
		BatchID:    duplicateBatchID,
		DispatchID: prefix + "-duplicate-dispatch",
		Jobs: []queue.BatchJob{
			{JobID: duplicateJobID, Job: queue.StoredJob{Type: "reports:shared"}},
			{JobID: prefix + "-pending-member", Job: queue.StoredJob{Type: "reports:pending"}},
		},
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create duplicate batch: %v", err)
	}
	if err := store.MarkBatchJobStarted(ctx, duplicateBatchID, prefix+"-missing-member"); !errors.Is(err, queue.ErrWorkflowNotFound) {
		t.Fatalf("start unknown batch member = %v, want ErrWorkflowNotFound", err)
	}
	if err := store.MarkBatchJobStarted(ctx, duplicateBatchID, duplicateJobID); err != nil {
		t.Fatalf("start duplicate batch member: %v", err)
	}
	if err := store.MarkBatchJobStarted(ctx, duplicateBatchID, duplicateJobID); err != nil {
		t.Fatalf("replay duplicate batch member start: %v", err)
	}
	start := make(chan struct{})
	errs := make(chan error, jobCount)
	var wg sync.WaitGroup
	for range jobCount {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, _, err := store.MarkBatchJobSucceeded(ctx, duplicateBatchID, duplicateJobID)
			errs <- err
		}()
	}
	close(start)
	waitWorkflowStoreOperations(t, &wg)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("settle duplicate batch member: %v", err)
		}
	}
	duplicateState, owned, err := outcomes.SettleBatchJob(ctx, duplicateBatchID, duplicateJobID, queue.BatchJobFailed, errors.New("inconsistent duplicate"))
	if err != nil {
		t.Fatalf("reclassify duplicate batch member: %v", err)
	}
	if owned || duplicateState.Pending != 1 || duplicateState.Processed != 1 || duplicateState.Failed != 0 || duplicateState.Cancelled || duplicateState.Completed {
		t.Fatalf("duplicate batch state = %+v owned:%t, want first outcome retained", duplicateState, owned)
	}
	if _, owned, err := outcomes.SettleBatchJob(ctx, duplicateBatchID, duplicateJobID, queue.BatchJobSucceeded, nil); err != nil || !owned {
		t.Fatalf("replay winning batch outcome = owned:%t err:%v", owned, err)
	}

	racedBatchID := prefix + "-raced-outcome-batch"
	racedJobID := prefix + "-raced-outcome-member"
	if err := store.CreateBatch(ctx, queue.BatchRecord{
		BatchID:     racedBatchID,
		AllowFailed: true,
		Jobs: []queue.BatchJob{
			{JobID: racedJobID},
			{JobID: prefix + "-raced-pending-member"},
		},
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create raced batch: %v", err)
	}
	start = make(chan struct{})
	errs = make(chan error, jobCount)
	wg = sync.WaitGroup{}
	for delivery := range jobCount {
		outcome := queue.BatchJobSucceeded
		if delivery%2 == 0 {
			outcome = queue.BatchJobFailed
		}
		wg.Add(1)
		go func(outcome queue.BatchJobOutcome) {
			defer wg.Done()
			<-start
			errs <- retryWorkflowStoreConflict(ctx, func() error {
				_, _, err := outcomes.SettleBatchJob(ctx, racedBatchID, racedJobID, outcome, errors.New("raced member outcome"))
				return err
			})
		}(outcome)
	}
	close(start)
	waitWorkflowStoreOperations(t, &wg)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("race batch outcome: %v", err)
		}
	}
	racedBatchState, err := store.GetBatch(ctx, racedBatchID)
	if err != nil {
		t.Fatalf("get raced batch: %v", err)
	}
	if racedBatchState.Pending != 1 || racedBatchState.Processed != 1 || (racedBatchState.Failed != 0 && racedBatchState.Failed != 1) || racedBatchState.Completed {
		t.Fatalf("raced batch state = %+v", racedBatchState)
	}
	_, successOwned, err := outcomes.SettleBatchJob(ctx, racedBatchID, racedJobID, queue.BatchJobSucceeded, nil)
	if err != nil {
		t.Fatalf("replay raced success: %v", err)
	}
	_, failureOwned, err := outcomes.SettleBatchJob(ctx, racedBatchID, racedJobID, queue.BatchJobFailed, errors.New("replayed failure"))
	if err != nil {
		t.Fatalf("replay raced failure: %v", err)
	}
	if successOwned == failureOwned || successOwned != (racedBatchState.Failed == 0) {
		t.Fatalf("raced batch ownership = success:%t failure:%t state:%+v", successOwned, failureOwned, racedBatchState)
	}
	for _, policy := range []struct {
		name          string
		allowFailures bool
	}{
		{name: "allow-failures", allowFailures: true},
		{name: "fail-fast", allowFailures: false},
	} {
		state := settleWorkflowStoreTerminalMemberConcurrently(t, ctx, store, outcomes, prefix+"-terminal-"+policy.name, policy.allowFailures)
		if state.Pending != 0 || state.Processed != 1 || !state.Completed || (state.Failed != 0 && state.Failed != 1) {
			t.Fatalf("%s terminal batch state = %+v", policy.name, state)
		}
		wantCancelled := state.Failed == 1 && !policy.allowFailures
		if state.Cancelled != wantCancelled {
			t.Fatalf("%s terminal cancellation = %t, want %t for state %+v", policy.name, state.Cancelled, wantCancelled, state)
		}
	}

	chainID := prefix + "-chain"
	firstNodeID := prefix + "-first-node"
	secondNodeID := prefix + "-second-node"
	if err := store.CreateChain(ctx, queue.ChainRecord{
		ChainID:    chainID,
		DispatchID: prefix + "-chain-dispatch",
		Nodes: []queue.ChainNode{
			{NodeID: firstNodeID, Job: queue.StoredJob{Type: "reports:first"}},
			{NodeID: secondNodeID, Job: queue.StoredJob{Type: "reports:second"}},
		},
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create chain: %v", err)
	}
	start = make(chan struct{})
	errs = make(chan error, jobCount)
	wg = sync.WaitGroup{}
	for range jobCount {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			next, done, err := store.AdvanceChain(ctx, chainID, firstNodeID)
			if err == nil && (done || next == nil || next.NodeID != secondNodeID) {
				err = fmt.Errorf("next = %+v done:%t, want second node", next, done)
			}
			errs <- err
		}()
	}
	close(start)
	waitWorkflowStoreOperations(t, &wg)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("advance duplicate chain node: %v", err)
		}
	}
	chainState, err := store.GetChain(ctx, chainID)
	if err != nil {
		t.Fatalf("get chain: %v", err)
	}
	if chainState.NextIndex != 1 || chainState.Completed || chainState.Failed {
		t.Fatalf("chain state = %+v, want one committed node", chainState)
	}
	chainState, owned, err = outcomes.FailChainNode(ctx, chainID, firstNodeID, errors.New("late node failure"))
	if err != nil || owned || chainState.NextIndex != 1 || chainState.Failed {
		t.Fatalf("late first-node failure = owned:%t state:%+v err:%v", owned, chainState, err)
	}
	if next, done, err := store.AdvanceChain(ctx, chainID, secondNodeID); err != nil || !done || next != nil {
		t.Fatalf("complete second chain node = next:%+v done:%t err:%v", next, done, err)
	}
	if err := store.FailChain(ctx, chainID, errors.New("late competing failure")); err != nil {
		t.Fatalf("fail completed chain: %v", err)
	}
	chainState, err = store.GetChain(ctx, chainID)
	if err != nil {
		t.Fatalf("get completed chain: %v", err)
	}
	if !chainState.Completed || chainState.Failed || chainState.Failure != "" {
		t.Fatalf("late failure changed completed chain: %+v", chainState)
	}

	failureFirstChainID := prefix + "-failure-first-chain"
	if err := store.CreateChain(ctx, queue.ChainRecord{
		ChainID: failureFirstChainID,
		Nodes: []queue.ChainNode{
			{NodeID: prefix + "-failure-first-node"},
			{NodeID: prefix + "-failure-pending-node"},
		},
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create failure-first chain: %v", err)
	}
	failureNodeID := prefix + "-failure-first-node"
	firstCause := errors.New("first chain-node failure")
	chainState, owned, err = outcomes.FailChainNode(ctx, failureFirstChainID, failureNodeID, firstCause)
	if err != nil || !owned || !chainState.Failed || chainState.NextIndex != 0 {
		t.Fatalf("fail current chain node = owned:%t state:%+v err:%v", owned, chainState, err)
	}
	chainState, owned, err = outcomes.FailChainNode(ctx, failureFirstChainID, failureNodeID, errors.New("replacement cause"))
	if err != nil || !owned || chainState.Failure != firstCause.Error() {
		t.Fatalf("replay failed chain node = owned:%t state:%+v err:%v", owned, chainState, err)
	}
	if _, done, err := store.AdvanceChain(ctx, failureFirstChainID, failureNodeID); err != nil || !done {
		t.Fatalf("advance failed chain node = done:%t err:%v", done, err)
	}

	racedChainID := prefix + "-raced-outcome-chain"
	racedNodeID := prefix + "-raced-outcome-node"
	if err := store.CreateChain(ctx, queue.ChainRecord{
		ChainID:   racedChainID,
		Nodes:     []queue.ChainNode{{NodeID: racedNodeID}},
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create raced chain: %v", err)
	}
	start = make(chan struct{})
	errs = make(chan error, jobCount)
	wg = sync.WaitGroup{}
	for delivery := range jobCount {
		wg.Add(1)
		go func(fail bool) {
			defer wg.Done()
			<-start
			errs <- retryWorkflowStoreConflict(ctx, func() error {
				if fail {
					_, _, err := outcomes.FailChainNode(ctx, racedChainID, racedNodeID, errors.New("raced node failure"))
					return err
				}
				_, _, err := store.AdvanceChain(ctx, racedChainID, racedNodeID)
				return err
			})
		}(delivery%2 == 0)
	}
	close(start)
	waitWorkflowStoreOperations(t, &wg)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("race chain outcome: %v", err)
		}
	}
	chainState, err = store.GetChain(ctx, racedChainID)
	if err != nil {
		t.Fatalf("get raced chain: %v", err)
	}
	successWon := chainState.NextIndex == 1 && !chainState.Failed && chainState.Completed
	failureWon := chainState.NextIndex == 0 && chainState.Failed && !chainState.Completed
	if !successWon && !failureWon {
		t.Fatalf("raced chain state = %+v", chainState)
	}

	for _, callbackKey := range []string{prefix + "-Callback", prefix + "-callback"} {
		type claimResult struct {
			claimed bool
			err     error
		}
		start = make(chan struct{})
		results := make(chan claimResult, jobCount)
		wg = sync.WaitGroup{}
		for range jobCount {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				claimed, err := store.MarkCallbackInvoked(ctx, callbackKey)
				results <- claimResult{claimed: claimed, err: err}
			}()
		}
		close(start)
		waitWorkflowStoreOperations(t, &wg)
		close(results)
		claims := 0
		for result := range results {
			if result.err != nil {
				t.Fatalf("claim callback %q: %v", callbackKey, result.err)
			}
			if result.claimed {
				claims++
			}
		}
		if claims != 1 {
			t.Fatalf("callback %q winning claims = %d, want 1", callbackKey, claims)
		}
	}
}

// TestWorkflowStoreIntegration_SQLite runs the atomic workflow-store contract
// against multiple real SQLite connections.
func TestWorkflowStoreIntegration_SQLite(t *testing.T) {
	if !integrationBackendEnabled(testenv.BackendSQLite) {
		t.Skip("sqlite integration backend not selected")
	}
	dsn := filepath.Join(t.TempDir(), "workflow-store.db") + "?_pragma=busy_timeout%3d10000"
	runWorkflowStoreConcurrencyContract(t, testenv.BackendSQLite, dsn)
}

// TestWorkflowStoreIntegration_MySQL runs the same workflow-store contract on
// MySQL so dialect-specific schema and locking behavior remain executable.
func TestWorkflowStoreIntegration_MySQL(t *testing.T) {
	if !integrationBackendEnabled(testenv.BackendMySQL) {
		t.Skip("mysql integration backend not selected")
	}
	ensureMySQLDB(t)
	dsn := fmt.Sprintf("queue:queue@tcp(%s)/queue_test?parseTime=true", integrationMySQL.addr)
	runWorkflowStoreConcurrencyContract(t, testenv.BackendMySQL, dsn)
}

// TestWorkflowStoreIntegration_MySQLAutoMigratesMissingReceiptAtLegacyWidths
// proves ordinary startup preserves wide legacy state when introducing receipts.
func TestWorkflowStoreIntegration_MySQLAutoMigratesMissingReceiptAtLegacyWidths(t *testing.T) {
	if !integrationBackendEnabled(testenv.BackendMySQL) {
		t.Skip("mysql integration backend not selected")
	}
	ensureMySQLDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	dsn := fmt.Sprintf("queue:queue@tcp(%s)/queue_test?parseTime=true", integrationMySQL.addr)
	db, err := sql.Open(testenv.BackendMySQL, dsn)
	if err != nil {
		t.Fatalf("open MySQL workflow database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	bootstrap, err := queue.NewSQLStore(queue.SQLStoreConfig{DB: db, DriverName: testenv.BackendMySQL})
	if err != nil {
		t.Fatalf("bootstrap workflow store: %v", err)
	}
	bootstrapKey := fmt.Sprintf("workflow-receipt-upgrade-bootstrap-%d", time.Now().UnixNano())
	if _, err := bootstrap.MarkCallbackInvoked(ctx, bootstrapKey); err != nil {
		t.Fatalf("bootstrap legacy workflow schema: %v", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM bus_callback_invocations WHERE callback_key=?`, bootstrapKey); err != nil {
		t.Fatalf("remove bootstrap callback: %v", err)
	}

	chainID := strings.Repeat("c", 320)
	nodeID := strings.Repeat("n", 321)
	batchID := strings.Repeat("b", 322)
	jobID := strings.Repeat("j", 323)
	callbackKey := strings.Repeat("k", 700)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		for _, statement := range []struct {
			query string
			args  []any
		}{
			{query: `DELETE FROM bus_chain_completed_nodes WHERE chain_id=?`, args: []any{chainID}},
			{query: `DELETE FROM bus_chains WHERE chain_id=?`, args: []any{chainID}},
			{query: `DELETE FROM bus_batch_jobs WHERE batch_id=?`, args: []any{batchID}},
			{query: `DELETE FROM bus_batches WHERE batch_id=?`, args: []any{batchID}},
			{query: `DELETE FROM bus_callback_invocations WHERE callback_key=?`, args: []any{callbackKey}},
		} {
			if _, cleanupErr := db.ExecContext(cleanupCtx, statement.query, statement.args...); cleanupErr != nil {
				t.Errorf("clean upgraded workflow rows: %v", cleanupErr)
			}
		}
		if _, cleanupErr := db.ExecContext(cleanupCtx, `DROP TABLE IF EXISTS bus_workflow_transition_receipts`); cleanupErr != nil {
			t.Errorf("drop derived workflow receipt table: %v", cleanupErr)
		}
		for _, statement := range []string{
			`ALTER TABLE bus_chains MODIFY chain_id VARBINARY(255) NOT NULL`,
			`ALTER TABLE bus_chain_completed_nodes MODIFY chain_id VARBINARY(255) NOT NULL, MODIFY node_id VARBINARY(255) NOT NULL`,
			`ALTER TABLE bus_batches MODIFY batch_id VARBINARY(255) NOT NULL`,
			`ALTER TABLE bus_batch_jobs MODIFY batch_id VARBINARY(255) NOT NULL, MODIFY job_id VARBINARY(255) NOT NULL`,
			`ALTER TABLE bus_callback_invocations MODIFY callback_key VARBINARY(512) NOT NULL`,
		} {
			if _, cleanupErr := db.ExecContext(cleanupCtx, statement); cleanupErr != nil {
				t.Errorf("restore legacy workflow schema width: %v", cleanupErr)
			}
		}
		restored, restoreErr := queue.NewSQLStore(queue.SQLStoreConfig{DB: db, DriverName: testenv.BackendMySQL})
		if restoreErr != nil {
			t.Errorf("new workflow store for receipt restoration: %v", restoreErr)
			return
		}
		restoreKey := fmt.Sprintf("workflow-receipt-upgrade-restore-%d", time.Now().UnixNano())
		if _, restoreErr := restored.MarkCallbackInvoked(cleanupCtx, restoreKey); restoreErr != nil {
			t.Errorf("restore default workflow receipt table: %v", restoreErr)
			return
		}
		if _, restoreErr := db.ExecContext(cleanupCtx, `DELETE FROM bus_callback_invocations WHERE callback_key=?`, restoreKey); restoreErr != nil {
			t.Errorf("remove receipt restoration callback: %v", restoreErr)
		}
	})

	if _, err := db.ExecContext(ctx, `DROP TABLE bus_workflow_transition_receipts`); err != nil {
		t.Fatalf("remove receipt table from legacy schema: %v", err)
	}
	for _, statement := range []string{
		`ALTER TABLE bus_chains MODIFY chain_id VARBINARY(512) NOT NULL`,
		`ALTER TABLE bus_chain_completed_nodes MODIFY chain_id VARBINARY(512) NOT NULL, MODIFY node_id VARBINARY(512) NOT NULL`,
		`ALTER TABLE bus_batches MODIFY batch_id VARBINARY(512) NOT NULL`,
		`ALTER TABLE bus_batch_jobs MODIFY batch_id VARBINARY(512) NOT NULL, MODIFY job_id VARBINARY(512) NOT NULL`,
		`ALTER TABLE bus_callback_invocations MODIFY callback_key VARBINARY(1024) NOT NULL`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("widen legacy workflow schema: %v", err)
		}
	}

	store, err := queue.NewSQLStore(queue.SQLStoreConfig{DB: db, DriverName: testenv.BackendMySQL})
	if err != nil {
		t.Fatalf("new auto-migrating store over legacy schema: %v", err)
	}
	if err := store.CreateChain(ctx, queue.ChainRecord{
		ChainID:    chainID,
		DispatchID: "legacy-wide-chain-dispatch",
		Nodes:      []queue.ChainNode{{NodeID: nodeID}},
		CreatedAt:  time.Now(),
	}); err != nil {
		t.Fatalf("create chain through upgraded schema: %v", err)
	}

	rows, err := db.QueryContext(ctx, `SELECT column_name, data_type, character_maximum_length
		FROM information_schema.columns
		WHERE table_schema=DATABASE() AND table_name='bus_workflow_transition_receipts'
		AND column_name IN ('workflow_id', 'member_id')`)
	if err != nil {
		t.Fatalf("read derived receipt widths: %v", err)
	}
	derivedWidths := make(map[string]int64, 2)
	for rows.Next() {
		var columnName, dataType string
		var width int64
		if err := rows.Scan(&columnName, &dataType, &width); err != nil {
			_ = rows.Close()
			t.Fatalf("scan derived receipt width: %v", err)
		}
		if !strings.EqualFold(dataType, "varbinary") {
			_ = rows.Close()
			t.Fatalf("derived receipt column %s type = %s, want VARBINARY", columnName, dataType)
		}
		derivedWidths[columnName] = width
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		t.Fatalf("iterate derived receipt widths: %v", err)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close derived receipt width rows: %v", err)
	}
	if derivedWidths["workflow_id"] != 512 || derivedWidths["member_id"] != 512 {
		t.Fatalf("derived receipt widths = %+v, want workflow_id:512 member_id:512", derivedWidths)
	}

	if next, done, err := store.AdvanceChain(ctx, chainID, nodeID); err != nil || !done || next != nil {
		t.Fatalf("complete chain through upgraded schema = next:%+v done:%t err:%v", next, done, err)
	}
	if err := store.CreateBatch(ctx, queue.BatchRecord{
		BatchID:    batchID,
		DispatchID: "legacy-wide-batch-dispatch",
		Jobs:       []queue.BatchJob{{JobID: jobID}},
		CreatedAt:  time.Now(),
	}); err != nil {
		t.Fatalf("create batch through upgraded schema: %v", err)
	}
	outcomes, ok := store.(queue.WorkflowOutcomeStore)
	if !ok {
		t.Fatalf("upgraded SQL store %T does not implement WorkflowOutcomeStore", store)
	}
	if state, owned, err := outcomes.SettleBatchJob(ctx, batchID, jobID, queue.BatchJobSucceeded, nil); err != nil || !owned || !state.Completed {
		t.Fatalf("settle batch through upgraded schema = state:%+v owned:%t err:%v", state, owned, err)
	}
	if claimed, err := store.MarkCallbackInvoked(ctx, callbackKey); err != nil || !claimed {
		t.Fatalf("claim wide callback through upgraded schema = claimed:%t err:%v", claimed, err)
	}
	if claimed, err := store.MarkCallbackInvoked(ctx, callbackKey); err != nil || claimed {
		t.Fatalf("reclaim wide callback through upgraded schema = claimed:%t err:%v", claimed, err)
	}

	receiptWorkflowID := strings.Repeat("r", 324)
	receiptMemberID := strings.Repeat("m", 325)
	result, err := db.ExecContext(ctx, `INSERT INTO bus_workflow_transition_receipts
		(workflow_kind, receipt_version, event_schema_version, workflow_id, member_id, workflow_dispatch_id,
		workflow_created_at_ms, outcome, owner_delivery_id, owner_attempt, job_dispatch_id, job_id,
		job_fingerprint, aggregate_completed, aggregate_cancelled, created_at_ms)
		VALUES ('chain', 1, 1, ?, ?, 'legacy-wide-receipt-dispatch', ?, 'succeeded',
		'legacy-wide-receipt-owner', 1, 'legacy-wide-job-dispatch', 'legacy-wide-job',
		'legacy-wide-job-fingerprint', 0, 0, ?)`, receiptWorkflowID, receiptMemberID, time.Now().UnixMilli(), time.Now().UnixMilli())
	if err != nil {
		t.Fatalf("insert wide transition receipt: %v", err)
	}
	if inserted, err := result.RowsAffected(); err != nil || inserted != 1 {
		t.Fatalf("wide transition receipt rows = %d err:%v", inserted, err)
	}
	result, err = db.ExecContext(ctx, `DELETE FROM bus_workflow_transition_receipts WHERE workflow_kind='chain' AND workflow_id=? AND member_id=?`, receiptWorkflowID, receiptMemberID)
	if err != nil {
		t.Fatalf("delete wide transition receipt: %v", err)
	}
	if deleted, err := result.RowsAffected(); err != nil || deleted != 1 {
		t.Fatalf("deleted wide transition receipt rows = %d err:%v", deleted, err)
	}
}

// TestWorkflowStoreIntegration_MySQLManagedWideKeys proves validation follows
// an existing wider binary schema instead of imposing fresh-schema defaults.
func TestWorkflowStoreIntegration_MySQLManagedWideKeys(t *testing.T) {
	if !integrationBackendEnabled(testenv.BackendMySQL) {
		t.Skip("mysql integration backend not selected")
	}
	ensureMySQLDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dsn := fmt.Sprintf("queue:queue@tcp(%s)/queue_test?parseTime=true", integrationMySQL.addr)
	db, err := sql.Open(testenv.BackendMySQL, dsn)
	if err != nil {
		t.Fatalf("open MySQL workflow database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	bootstrap, err := queue.NewSQLStore(queue.SQLStoreConfig{DB: db, DriverName: testenv.BackendMySQL, AutoMigrate: true})
	if err != nil {
		t.Fatalf("bootstrap workflow store: %v", err)
	}
	bootstrapKey := fmt.Sprintf("workflow-wide-bootstrap-%d", time.Now().UnixNano())
	if _, err := bootstrap.MarkCallbackInvoked(ctx, bootstrapKey); err != nil {
		t.Fatalf("bootstrap workflow schema: %v", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM bus_callback_invocations WHERE callback_key=?`, bootstrapKey); err != nil {
		t.Fatalf("remove bootstrap callback: %v", err)
	}
	freshOverlongID := strings.Repeat("i", 256)
	if err := bootstrap.CreateChain(ctx, queue.ChainRecord{ChainID: freshOverlongID, Nodes: []queue.ChainNode{{NodeID: "fresh-node"}}}); err == nil || !strings.Contains(err.Error(), "255 bytes") {
		t.Fatalf("fresh schema overlong chain error = %v", err)
	}
	if claimed, err := bootstrap.MarkCallbackInvoked(ctx, strings.Repeat("k", 513)); err == nil || claimed || !strings.Contains(err.Error(), "512 bytes") {
		t.Fatalf("fresh schema overlong callback = claimed:%t err:%v", claimed, err)
	}

	widen := []string{
		`ALTER TABLE bus_chains MODIFY chain_id VARBINARY(512) NOT NULL`,
		`ALTER TABLE bus_chain_completed_nodes MODIFY chain_id VARBINARY(512) NOT NULL, MODIFY node_id VARBINARY(512) NOT NULL`,
		`ALTER TABLE bus_batches MODIFY batch_id VARBINARY(512) NOT NULL`,
		`ALTER TABLE bus_batch_jobs MODIFY batch_id VARBINARY(512) NOT NULL, MODIFY job_id VARBINARY(512) NOT NULL`,
		`ALTER TABLE bus_callback_invocations MODIFY callback_key VARBINARY(1024) NOT NULL`,
		`ALTER TABLE bus_workflow_transition_receipts MODIFY workflow_id VARBINARY(512) NOT NULL, MODIFY member_id VARBINARY(512) NOT NULL`,
	}
	chainPrefix := strings.Repeat("chain", 59)
	chainIDs := []string{chainPrefix + "A", chainPrefix + "B"}
	nodePrefix := strings.Repeat("node", 74)
	nodeIDs := []string{nodePrefix + "A", nodePrefix + "B"}
	batchID := strings.Repeat("batch", 59) + "A"
	jobID := strings.Repeat("member", 49) + "A"
	callbackPrefix := strings.Repeat("callback", 87)
	callbackKeys := []string{callbackPrefix + "A", callbackPrefix + "B"}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cleanupCancel()
		for _, statement := range []struct {
			query string
			args  []any
		}{
			{query: `DELETE FROM bus_chain_completed_nodes WHERE chain_id IN (?, ?)`, args: []any{chainIDs[0], chainIDs[1]}},
			{query: `DELETE FROM bus_chains WHERE chain_id IN (?, ?)`, args: []any{chainIDs[0], chainIDs[1]}},
			{query: `DELETE FROM bus_batch_jobs WHERE batch_id=?`, args: []any{batchID}},
			{query: `DELETE FROM bus_batches WHERE batch_id=?`, args: []any{batchID}},
			{query: `DELETE FROM bus_callback_invocations WHERE callback_key IN (?, ?)`, args: []any{callbackKeys[0], callbackKeys[1]}},
		} {
			if _, err := db.ExecContext(cleanupCtx, statement.query, statement.args...); err != nil {
				t.Errorf("clean wide workflow rows: %v", err)
			}
		}
		for _, statement := range []string{
			`ALTER TABLE bus_chains MODIFY chain_id VARBINARY(255) NOT NULL`,
			`ALTER TABLE bus_chain_completed_nodes MODIFY chain_id VARBINARY(255) NOT NULL, MODIFY node_id VARBINARY(255) NOT NULL`,
			`ALTER TABLE bus_batches MODIFY batch_id VARBINARY(255) NOT NULL`,
			`ALTER TABLE bus_batch_jobs MODIFY batch_id VARBINARY(255) NOT NULL, MODIFY job_id VARBINARY(255) NOT NULL`,
			`ALTER TABLE bus_callback_invocations MODIFY callback_key VARBINARY(512) NOT NULL`,
			`ALTER TABLE bus_workflow_transition_receipts MODIFY workflow_id VARBINARY(255) NOT NULL, MODIFY member_id VARBINARY(255) NOT NULL`,
		} {
			if _, err := db.ExecContext(cleanupCtx, statement); err != nil {
				t.Errorf("restore generated workflow schema width: %v", err)
			}
		}
	})
	for _, statement := range widen {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("widen managed workflow schema: %v", err)
		}
	}

	store, err := queue.NewSQLStoreWithManagedSchema(queue.SQLStoreConfig{DB: db, DriverName: testenv.BackendMySQL})
	if err != nil {
		t.Fatalf("new store over managed schema: %v", err)
	}
	for index := range chainIDs {
		if err := store.CreateChain(ctx, queue.ChainRecord{ChainID: chainIDs[index], Nodes: []queue.ChainNode{{NodeID: nodeIDs[index]}}}); err != nil {
			t.Fatalf("create wide chain %d: %v", index, err)
		}
		if next, done, err := store.AdvanceChain(ctx, chainIDs[index], nodeIDs[index]); err != nil || !done || next != nil {
			t.Fatalf("complete wide chain %d = next:%+v done:%t err:%v", index, next, done, err)
		}
	}
	if err := store.CreateBatch(ctx, queue.BatchRecord{BatchID: batchID, Jobs: []queue.BatchJob{{JobID: jobID}}}); err != nil {
		t.Fatalf("create wide batch: %v", err)
	}
	outcomes, ok := store.(queue.WorkflowOutcomeStore)
	if !ok {
		t.Fatalf("managed SQL store %T does not implement WorkflowOutcomeStore", store)
	}
	if state, owned, err := outcomes.SettleBatchJob(ctx, batchID, jobID, queue.BatchJobSucceeded, nil); err != nil || !owned || !state.Completed {
		t.Fatalf("settle wide batch = state:%+v owned:%t err:%v", state, owned, err)
	}
	for _, key := range callbackKeys {
		claimed, err := store.MarkCallbackInvoked(ctx, key)
		if err != nil || !claimed {
			t.Fatalf("claim wide callback = claimed:%t err:%v", claimed, err)
		}
	}
	if claimed, err := store.MarkCallbackInvoked(ctx, callbackKeys[0]); err != nil || claimed {
		t.Fatalf("reclaim wide callback = claimed:%t err:%v", claimed, err)
	}
}

// TestWorkflowStoreIntegration_MySQLRejectsNonVARBINARYKeys proves managed
// schemas cannot silently weaken byte-exact workflow identity comparisons.
func TestWorkflowStoreIntegration_MySQLRejectsNonVARBINARYKeys(t *testing.T) {
	if !integrationBackendEnabled(testenv.BackendMySQL) {
		t.Skip("mysql integration backend not selected")
	}
	ensureMySQLDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dsn := fmt.Sprintf("queue:queue@tcp(%s)/queue_test?parseTime=true", integrationMySQL.addr)
	db, err := sql.Open(testenv.BackendMySQL, dsn)
	if err != nil {
		t.Fatalf("open MySQL workflow database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	bootstrap, err := queue.NewSQLStore(queue.SQLStoreConfig{DB: db, DriverName: testenv.BackendMySQL, AutoMigrate: true})
	if err != nil {
		t.Fatalf("bootstrap workflow store: %v", err)
	}
	bootstrapKey := fmt.Sprintf("workflow-type-bootstrap-%d", time.Now().UnixNano())
	if _, err := bootstrap.MarkCallbackInvoked(ctx, bootstrapKey); err != nil {
		t.Fatalf("bootstrap workflow schema: %v", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM bus_callback_invocations WHERE callback_key=?`, bootstrapKey); err != nil {
		t.Fatalf("remove bootstrap callback: %v", err)
	}

	if _, err := db.ExecContext(ctx, `ALTER TABLE bus_chain_completed_nodes MODIFY node_id VARCHAR(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL`); err != nil {
		t.Fatalf("install comparison-unsafe managed key: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cleanupCancel()
		if _, err := db.ExecContext(cleanupCtx, `ALTER TABLE bus_chain_completed_nodes MODIFY node_id VARBINARY(255) NOT NULL`); err != nil {
			t.Errorf("restore workflow node key type: %v", err)
		}
	})

	store, err := queue.NewSQLStoreWithManagedSchema(queue.SQLStoreConfig{DB: db, DriverName: testenv.BackendMySQL})
	if err != nil {
		t.Fatalf("new store over managed schema: %v", err)
	}
	if _, err := store.GetChain(ctx, "managed-type-check"); err == nil || !strings.Contains(err.Error(), "bus_chain_completed_nodes.node_id must use VARBINARY") {
		t.Fatalf("managed VARCHAR key error = %v", err)
	}
}

// TestWorkflowStoreIntegration_Postgres runs the same workflow-store contract
// on PostgreSQL so its byte and placeholder types cannot silently drift.
func TestWorkflowStoreIntegration_Postgres(t *testing.T) {
	if !integrationBackendEnabled(testenv.BackendPostgres) {
		t.Skip("postgres integration backend not selected")
	}
	ensurePostgresDB(t)
	dsn := fmt.Sprintf("postgres://queue:queue@%s/queue_test?sslmode=disable", integrationPostgres.addr)
	runWorkflowStoreConcurrencyContract(t, "pgx", dsn)
}
