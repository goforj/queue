package sqlqueuecore

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/goforj/queue"
	"github.com/goforj/queue/busruntime"
	"github.com/goforj/queue/queuecore"
)

// TestDatabaseStartAfterShutdownRejectsFalseRestart verifies direct core callers cannot receive success after workers and resources stopped.
func TestDatabaseStartAfterShutdownRejectsFalseRestart(t *testing.T) {
	database := &databaseQueue{}
	database.started.Store(true)
	database.shuttingDown.Store(true)
	if err := database.StartWorkers(context.Background()); !errors.Is(err, queue.ErrQueuerShuttingDown) {
		t.Fatalf("start after shutdown = %v, want ErrQueuerShuttingDown", err)
	}
}

// TestDatabaseShutdownRetriesShareOneDrain verifies caller deadlines do not
// multiply waiter goroutines and later cleanup reports close diagnostics once
// before converging.
func TestDatabaseShutdownRetriesShareOneDrain(t *testing.T) {
	closeErr := errors.New("close database")
	connection := &databaseConnStub{closeErr: closeErr}
	db := newDatabaseStub(connection)
	if err := db.PingContext(context.Background()); err != nil {
		t.Fatalf("open database connection: %v", err)
	}

	database := &databaseQueue{
		db:         db,
		ownsDB:     true,
		shutdownCh: make(chan struct{}),
	}
	releaseWorker := make(chan struct{})
	database.workerWG.Add(1)
	go func() {
		defer database.workerWG.Done()
		<-releaseWorker
	}()

	shutdownWithDeadline := func() {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
		defer cancel()
		if err := database.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("timed shutdown = %v, want context deadline exceeded", err)
		}
	}

	shutdownWithDeadline()
	sharedDone := database.shutdownDone
	if sharedDone == nil {
		t.Fatal("shutdown did not retain its drain completion channel")
	}
	goroutinesAfterFirstDeadline := runtime.NumGoroutine()
	for range 32 {
		shutdownWithDeadline()
		if database.shutdownDone != sharedDone {
			t.Fatal("shutdown retry replaced the shared drain completion channel")
		}
	}
	runtime.Gosched()
	if got := runtime.NumGoroutine(); got > goroutinesAfterFirstDeadline+2 {
		t.Fatalf("shutdown retries grew goroutines from %d to %d", goroutinesAfterFirstDeadline, got)
	}
	if connection.closeCalls != 0 {
		t.Fatalf("database close calls before worker drain = %d, want 0", connection.closeCalls)
	}

	close(releaseWorker)
	if err := database.Shutdown(context.Background()); !errors.Is(err, closeErr) {
		t.Fatalf("converged shutdown = %v, want %v", err, closeErr)
	}
	if err := database.Shutdown(context.Background()); err != nil {
		t.Fatalf("repeated converged shutdown = %v, want nil after diagnostic was reported", err)
	}
	if connection.closeCalls != 1 {
		t.Fatalf("database close calls = %d, want 1", connection.closeCalls)
	}
}

// TestLocalDatabaseConfigDisableAutoMigrate verifies the additive opt-out preserves the established default while overriding legacy true values.
func TestLocalDatabaseConfigDisableAutoMigrate(t *testing.T) {
	if normalized := (localDatabaseConfig{}).normalize(); !normalized.AutoMigrate {
		t.Fatal("default configuration no longer enables compatibility migrations")
	}
	normalized := (localDatabaseConfig{AutoMigrate: true, DisableAutoMigrate: true}).normalize()
	if normalized.AutoMigrate {
		t.Fatal("DisableAutoMigrate did not override migration startup")
	}
}

// TestDatabaseContinuationPermissionIsScopedAndEphemeral verifies only this queue's active handler may dispatch during drain.
func TestDatabaseContinuationPermissionIsScopedAndEphemeral(t *testing.T) {
	database := &databaseQueue{continuation: busruntime.NewContinuationScope()}
	database.shuttingDown.Store(true)
	invalidJob := queue.Job{}

	foreign := busruntime.NewContinuationScope()
	foreignCtx, releaseForeign := foreign.Permit(context.Background())
	defer releaseForeign()
	if err := database.Dispatch(foreignCtx, invalidJob); !errors.Is(err, queue.ErrQueuerShuttingDown) {
		t.Fatalf("foreign continuation dispatch = %v, want ErrQueuerShuttingDown", err)
	}

	var escaped context.Context
	err := database.runHandlerWithContinuationPermit(context.Background(), func(ctx context.Context, _ queue.Job) error {
		escaped = ctx
		if !database.continuation.Owns(ctx) {
			t.Fatal("active SQL handler did not own its continuation permit")
		}
		dispatchErr := database.Dispatch(ctx, invalidJob)
		if errors.Is(dispatchErr, queue.ErrQueuerShuttingDown) || dispatchErr == nil {
			t.Fatalf("owned continuation dispatch = %v, want validation error after shutdown gate", dispatchErr)
		}
		return nil
	}, invalidJob)
	if err != nil {
		t.Fatalf("run handler with continuation permit: %v", err)
	}
	if database.continuation.Owns(escaped) {
		t.Fatal("handler context retained SQL continuation ownership after return")
	}
	if err := database.Dispatch(escaped, invalidJob); !errors.Is(err, queue.ErrQueuerShuttingDown) {
		t.Fatalf("escaped continuation dispatch = %v, want ErrQueuerShuttingDown", err)
	}
}

type databaseResultStub struct {
	rows int64
	err  error
}

type databaseExecerStub struct {
	calls  int
	query  string
	args   []any
	result sql.Result
	err    error
}

// LastInsertId returns an unused identifier for the sql.Result contract.
func (r databaseResultStub) LastInsertId() (int64, error) { return 0, nil }

// RowsAffected returns the configured settlement evidence.
func (r databaseResultStub) RowsAffected() (int64, error) { return r.rows, r.err }

// ExecContext records one recovery-lineage repair without requiring a live SQL
// driver in this dependency-light core module.
func (e *databaseExecerStub) ExecContext(_ context.Context, query string, args ...any) (sql.Result, error) {
	e.calls++
	e.query = query
	e.args = append([]any(nil), args...)
	return e.result, e.err
}

// TestDatabaseDeliveryJobRestoresAttemptMetadata verifies SQL persistence reaches the shared orchestration context intact.
func TestDatabaseDeliveryJobRestoresAttemptMetadata(t *testing.T) {
	wantPayload := []byte(`{"report_id":42}`)
	wantMetadata := queue.DriverJobMetadata{
		SchemaVersion: queue.DriverJobMetadataVersion,
		DispatchID:    "dsp_sql",
		JobID:         "job_sql",
		ChainID:       "chn_sql",
		BatchID:       "bat_sql",
		Queue:         "critical",
	}
	encodedMetadata, err := json.Marshal(wantMetadata)
	if err != nil {
		t.Fatalf("marshal metadata fixture: %v", err)
	}
	job := databaseDeliveryJob(&dbJob{
		jobType:      "reports:build",
		payload:      wantPayload,
		metadataJSON: sql.NullString{String: string(encodedMetadata), Valid: true},
		queueName:    "critical",
		attempt:      2,
		maxRetry:     4,
	})
	opts := queuecore.DriverOptions(job)
	if job.Type != "reports:build" || !bytes.Equal(job.PayloadBytes(), wantPayload) {
		t.Fatalf("delivery job = type:%q payload:%q", job.Type, job.PayloadBytes())
	}
	if opts.QueueName != "critical" || opts.Attempt != 2 || opts.MaxRetry == nil || *opts.MaxRetry != 4 {
		t.Fatalf("delivery options = %+v", opts)
	}
	if metadata := queue.DriverMetadata(job); metadata != wantMetadata {
		t.Fatalf("delivery metadata = %+v, want %+v", metadata, wantMetadata)
	}
}

// TestDatabaseSettlementContextMarksOnlyRecoveredRows ensures an ordinary
// duplicate cannot request winner-fact replay without stale-processing proof.
func TestDatabaseSettlementContextMarksOnlyRecoveredRows(t *testing.T) {
	tests := []struct {
		name        string
		job         *dbJob
		want        busruntime.DeliveryProvenance
		wantPresent bool
	}{
		{name: "nil"},
		{
			name:        "ordinary",
			job:         &dbJob{processingToken: "current-generation"},
			want:        busruntime.DeliveryProvenance{GenerationID: "current-generation"},
			wantPresent: true,
		},
		{
			name: "identified recovery",
			job: &dbJob{
				processingToken: "current-generation",
				recoveryToken:   "earlier-generation",
				recovered:       true,
			},
			want: busruntime.DeliveryProvenance{
				GenerationID:          "current-generation",
				RecoveredGenerationID: "earlier-generation",
				Recovered:             true,
			},
			wantPresent: true,
		},
		{
			name:        "legacy recovery",
			job:         &dbJob{processingToken: "current-generation", recovered: true},
			want:        busruntime.DeliveryProvenance{GenerationID: "current-generation", Recovered: true},
			wantPresent: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, settlement := databaseSettlementContext(test.job)
			if settlement == nil {
				t.Fatal("database settlement context omitted commit boundary")
			}
			got, present := busruntime.DeliveryProvenanceFromContext(ctx)
			if present != test.wantPresent || got != test.want {
				t.Fatalf("delivery provenance = %+v present:%t, want %+v/%t", got, present, test.want, test.wantPresent)
			}
		})
	}
}

// TestDatabaseRecoveryProofUsesOnlyTransportState verifies application error
// text cannot collide with the internal stale-processing recovery marker.
func TestDatabaseRecoveryProofUsesOnlyTransportState(t *testing.T) {
	tests := []struct {
		name            string
		processingToken sql.NullString
		lastError       sql.NullString
		wantToken       string
		want            bool
	}{
		{
			name:      "application error matches marker",
			lastError: sql.NullString{String: databaseRecoveryMarker, Valid: true},
		},
		{
			name:            "ordinary processing token",
			processingToken: sql.NullString{String: "ordinary-claim", Valid: true},
			lastError:       sql.NullString{String: databaseRecoveryMarker, Valid: true},
		},
		{
			name:            "transport recovery marker",
			processingToken: sql.NullString{String: databaseRecoveryMarker, Valid: true},
			lastError:       sql.NullString{String: databaseRecoveryDiagnostic, Valid: true},
			want:            true,
		},
		{
			name:            "identified transport generation",
			processingToken: sql.NullString{String: strings.Repeat("a", databaseProcessingTokenBytes*2), Valid: true},
			wantToken:       strings.Repeat("a", databaseProcessingTokenBytes*2),
			want:            true,
		},
		{
			name:            "uppercase generation is not canonical",
			processingToken: sql.NullString{String: strings.Repeat("A", databaseProcessingTokenBytes*2), Valid: true},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			token, recovered := databaseRecoveryProof(test.processingToken)
			if recovered != test.want || token != test.wantToken {
				t.Fatalf("recovery proof = token:%q recovered:%t, want %q/%t (processing_token=%q, last_error=%q)", token, recovered, test.wantToken, test.want, test.processingToken.String, test.lastError.String)
			}
		})
	}
}

// TestDatabasePendingRecoveryTokenPreservesPendingRecovery verifies the exact
// durable owner survives same-attempt infrastructure redelivery.
func TestDatabasePendingRecoveryTokenPreservesPendingRecovery(t *testing.T) {
	const (
		recoveryToken = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		currentToken  = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	)
	tests := []struct {
		name       string
		job        *dbJob
		settlement databaseFailureSettlement
		wantToken  string
	}{
		{name: "nil job", settlement: databaseFailureSettlement{state: "pending"}},
		{
			name:       "ordinary same attempt redelivery",
			job:        &dbJob{attempt: 2},
			settlement: databaseFailureSettlement{state: "pending", attempt: 2},
		},
		{
			name:       "recovered same attempt redelivery",
			job:        &dbJob{attempt: 2, recovered: true, recoveryToken: recoveryToken, processingToken: currentToken},
			settlement: databaseFailureSettlement{state: "pending", attempt: 2},
			wantToken:  recoveryToken,
		},
		{
			name:       "legacy same attempt redelivery",
			job:        &dbJob{attempt: 2, recovered: true, processingToken: currentToken},
			settlement: databaseFailureSettlement{state: "pending", attempt: 2},
			wantToken:  databaseRecoveryMarker,
		},
		{
			name:       "recovered current generation committed application state",
			job:        &dbJob{attempt: 2, recovered: true, recoveryToken: recoveryToken, processingToken: currentToken, applicationStateCommitted: true},
			settlement: databaseFailureSettlement{state: "pending", attempt: 2},
			wantToken:  currentToken,
		},
		{
			name:       "ordinary current generation committed application state",
			job:        &dbJob{attempt: 2, processingToken: currentToken, applicationStateCommitted: true},
			settlement: databaseFailureSettlement{state: "pending", attempt: 2},
			wantToken:  currentToken,
		},
		{
			name:       "application retry starts a new owner",
			job:        &dbJob{attempt: 2, recovered: true, recoveryToken: recoveryToken, processingToken: currentToken, applicationStateCommitted: true},
			settlement: databaseFailureSettlement{state: "pending", attempt: 3},
		},
		{
			name:       "recovered terminal failure",
			job:        &dbJob{attempt: 2, recovered: true, recoveryToken: recoveryToken, processingToken: currentToken},
			settlement: databaseFailureSettlement{state: "dead", attempt: 3},
		},
		{
			name:       "malformed recovered generation",
			job:        &dbJob{attempt: 2, recovered: true, recoveryToken: "malformed", processingToken: currentToken},
			settlement: databaseFailureSettlement{state: "pending", attempt: 2},
		},
		{
			name:       "malformed committed current generation",
			job:        &dbJob{attempt: 2, processingToken: "malformed", applicationStateCommitted: true},
			settlement: databaseFailureSettlement{state: "pending", attempt: 2},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			token := databasePendingRecoveryToken(test.job, test.settlement)
			if token.Valid != (test.wantToken != "") || token.String != test.wantToken {
				t.Fatalf("recovery token = %#v, want %q", token, test.wantToken)
			}
		})
	}
}

// TestDatabaseSettlementRecoveryTokenRepairsOnlyInheritedLineage verifies an
// exhausted recovery settlement never replaces a receipt owner with the current
// physical generation or repairs an ordinary first delivery.
func TestDatabaseSettlementRecoveryTokenRepairsOnlyInheritedLineage(t *testing.T) {
	const (
		recoveryToken = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		currentToken  = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	)
	tests := []struct {
		name       string
		job        *dbJob
		wantToken  string
		wantRepair bool
		wantError  bool
	}{
		{name: "nil job"},
		{name: "ordinary delivery", job: &dbJob{processingToken: currentToken}},
		{
			name:       "identified inherited owner",
			job:        &dbJob{recovered: true, recoveryToken: recoveryToken, processingToken: currentToken},
			wantToken:  recoveryToken,
			wantRepair: true,
		},
		{
			name:       "legacy inherited marker",
			job:        &dbJob{recovered: true, processingToken: currentToken},
			wantToken:  databaseRecoveryMarker,
			wantRepair: true,
		},
		{
			name: "current generation superseded provenance",
			job: &dbJob{
				recovered:                 true,
				recoveryToken:             recoveryToken,
				processingToken:           currentToken,
				applicationStateCommitted: true,
			},
		},
		{
			name:      "malformed inherited owner",
			job:       &dbJob{recovered: true, recoveryToken: "malformed", processingToken: currentToken},
			wantError: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			token, repair, err := databaseSettlementRecoveryToken(test.job)
			if (err != nil) != test.wantError {
				t.Fatalf("databaseSettlementRecoveryToken() error = %v, wantError %t", err, test.wantError)
			}
			if repair != test.wantRepair || token.Valid != test.wantRepair || token.String != test.wantToken {
				t.Fatalf("databaseSettlementRecoveryToken() = %#v repair:%t, want %q/%t", token, repair, test.wantToken, test.wantRepair)
			}
		})
	}
}

// TestRestoreDatabaseSettlementLineageFencesPendingRepair verifies the repair
// targets one exact processing generation while preserving attempt and owner.
func TestRestoreDatabaseSettlementLineageFencesPendingRepair(t *testing.T) {
	const (
		query             = "fenced recovery update"
		recoveryToken     = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		currentToken      = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		nowMillis         = int64(123456)
		availableAtMillis = int64(123531)
	)
	job := &dbJob{
		id:              41,
		attempt:         3,
		recovered:       true,
		recoveryToken:   recoveryToken,
		processingToken: currentToken,
	}
	settlementErr := errors.New("delete remained unavailable")
	execer := &databaseExecerStub{result: databaseResultStub{rows: 1}}
	if err := restoreDatabaseSettlementLineage(context.Background(), execer, query, job, settlementErr, availableAtMillis, nowMillis); err != nil {
		t.Fatalf("restore database settlement lineage: %v", err)
	}
	if execer.calls != 1 || execer.query != query {
		t.Fatalf("repair execution = calls:%d query:%q, want 1/%q", execer.calls, execer.query, query)
	}
	wantArgs := []any{
		availableAtMillis,
		sql.NullString{String: recoveryToken, Valid: true},
		settlementErr.Error(),
		nowMillis,
		job.id,
		currentToken,
		job.attempt,
	}
	if len(execer.args) != len(wantArgs) {
		t.Fatalf("repair argument count = %d, want %d: %#v", len(execer.args), len(wantArgs), execer.args)
	}
	for index := range wantArgs {
		if execer.args[index] != wantArgs[index] {
			t.Fatalf("repair argument %d = %#v, want %#v", index, execer.args[index], wantArgs[index])
		}
	}
	if job.attempt != 3 || job.processingToken != currentToken || job.recoveryToken != recoveryToken {
		t.Fatalf("repair mutated in-memory claim: %+v", job)
	}
}

// TestDatabaseSettlementRecoveryDelayBoundsFaultLoop verifies repaired rows
// honor the slower of queue polling and the driver's finalization retry floor.
func TestDatabaseSettlementRecoveryDelayBoundsFaultLoop(t *testing.T) {
	tests := []struct {
		name         string
		pollInterval time.Duration
		want         time.Duration
	}{
		{name: "zero poll interval", want: databaseFinalizeRetryDelay},
		{name: "short poll interval", pollInterval: time.Millisecond, want: databaseFinalizeRetryDelay},
		{name: "equal poll interval", pollInterval: databaseFinalizeRetryDelay, want: databaseFinalizeRetryDelay},
		{name: "long poll interval", pollInterval: 150 * time.Millisecond, want: 150 * time.Millisecond},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := databaseSettlementRecoveryDelay(test.pollInterval); got != test.want {
				t.Fatalf("databaseSettlementRecoveryDelay(%s) = %s, want %s", test.pollInterval, got, test.want)
			}
		})
	}
}

// TestRestoreDatabaseSettlementLineageRejectsUnprovableRepair covers every
// failure branch that must leave the currently fenced row untouched.
func TestRestoreDatabaseSettlementLineageRejectsUnprovableRepair(t *testing.T) {
	const currentToken = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	settlementErr := errors.New("settlement failed")
	tests := []struct {
		name      string
		execer    databaseExecer
		job       *dbJob
		err       error
		wantError bool
		wantCalls int
	}{
		{
			name:   "ordinary delivery is unchanged",
			execer: &databaseExecerStub{result: databaseResultStub{rows: 1}},
			job:    &dbJob{id: 1, processingToken: currentToken},
		},
		{
			name:      "nil executor",
			job:       &dbJob{id: 1, recovered: true, processingToken: currentToken},
			wantError: true,
		},
		{
			name:      "nil settlement error",
			execer:    &databaseExecerStub{result: databaseResultStub{rows: 1}},
			job:       &dbJob{id: 1, recovered: true, processingToken: currentToken},
			wantError: true,
		},
		{
			name:      "missing fenced id",
			execer:    &databaseExecerStub{result: databaseResultStub{rows: 1}},
			job:       &dbJob{recovered: true, processingToken: currentToken},
			err:       settlementErr,
			wantError: true,
		},
		{
			name:      "execution failure",
			execer:    &databaseExecerStub{err: errors.New("database offline")},
			job:       &dbJob{id: 1, recovered: true, processingToken: currentToken},
			err:       settlementErr,
			wantError: true,
			wantCalls: 1,
		},
		{
			name:      "lost fence",
			execer:    &databaseExecerStub{result: databaseResultStub{}},
			job:       &dbJob{id: 1, recovered: true, processingToken: currentToken},
			err:       settlementErr,
			wantError: true,
			wantCalls: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repairErr := test.err
			if test.name != "nil settlement error" && repairErr == nil {
				repairErr = settlementErr
			}
			err := restoreDatabaseSettlementLineage(context.Background(), test.execer, "repair", test.job, repairErr, 2, 1)
			if (err != nil) != test.wantError {
				t.Fatalf("restoreDatabaseSettlementLineage() error = %v, wantError %t", err, test.wantError)
			}
			if execer, ok := test.execer.(*databaseExecerStub); ok && execer.calls != test.wantCalls {
				t.Fatalf("repair calls = %d, want %d", execer.calls, test.wantCalls)
			}
		})
	}
}

// TestDatabaseMetadataJSONPersistsOnlySupportedMetadata verifies legacy jobs
// store SQL NULL while direct jobs retain the exact root correlation contract.
func TestDatabaseMetadataJSONPersistsOnlySupportedMetadata(t *testing.T) {
	if got, err := databaseMetadataJSON(queue.NewJob("reports:legacy")); err != nil || got.Valid {
		t.Fatalf("legacy metadata JSON = %#v, %v; want SQL NULL", got, err)
	}

	want := queue.DriverJobMetadata{
		SchemaVersion: queue.DriverJobMetadataVersion,
		DispatchID:    "dsp_sql",
		JobID:         "job_sql",
		Queue:         "critical",
	}
	encoded, err := databaseMetadataJSON(queue.DriverWithMetadata(queue.NewJob("reports:direct"), want))
	if err != nil {
		t.Fatalf("encode direct metadata: %v", err)
	}
	if !encoded.Valid {
		t.Fatal("direct metadata unexpectedly encoded as SQL NULL")
	}
	var got queue.DriverJobMetadata
	if err := json.Unmarshal([]byte(encoded.String), &got); err != nil {
		t.Fatalf("decode direct metadata: %v", err)
	}
	if got != want {
		t.Fatalf("metadata round trip = %+v, want %+v", got, want)
	}
}

// TestDatabaseDeliveryJobRejectsUntrustedMetadata verifies nullable, malformed,
// and unknown-version rows remain deliverable without accepting spoofed IDs.
func TestDatabaseDeliveryJobRejectsUntrustedMetadata(t *testing.T) {
	tests := []struct {
		name string
		raw  sql.NullString
	}{
		{name: "null"},
		{name: "empty", raw: sql.NullString{Valid: true}},
		{name: "malformed", raw: sql.NullString{String: `{`, Valid: true}},
		{name: "unknown", raw: sql.NullString{String: `{"schema_version":99,"dispatch_id":"spoofed","job_id":"spoofed"}`, Valid: true}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			job := databaseDeliveryJob(&dbJob{
				jobType:      "reports:build",
				payload:      []byte(`{"id":7}`),
				metadataJSON: test.raw,
				queueName:    "critical",
			})
			if metadata := queue.DriverMetadata(job); metadata.SchemaVersion != 0 {
				t.Fatalf("untrusted driver metadata = %+v", metadata)
			}
			observed := queue.ResolveObservedJobMetadataFromJob(job)
			if observed.DispatchID != "" || observed.JobID != "" || observed.ChainID != "" || observed.BatchID != "" {
				t.Fatalf("untrusted observed correlation = %+v", observed)
			}
			if observed.JobType != "reports:build" || observed.JobKey == "" {
				t.Fatalf("application identity was not delivered: %+v", observed)
			}
		})
	}
}

// TestDatabaseDeliveryJobRetainsLegacyEnvelopeFallback verifies NULL metadata
// does not sever correlation for workflow envelopes already persisted by v1.
func TestDatabaseDeliveryJobRetainsLegacyEnvelopeFallback(t *testing.T) {
	payload := []byte(`{"schema_version":1,"dispatch_id":"dsp_legacy","job_id":"job_legacy","chain_id":"chn_legacy","job":{"type":"reports:build","payload":"eyJpZCI6N30="}}`)
	job := databaseDeliveryJob(&dbJob{
		jobType:   "bus:chain:node",
		payload:   payload,
		queueName: "critical",
	})
	metadata := queue.ResolveObservedJobMetadataFromJob(job)
	if metadata.JobType != "reports:build" || metadata.DispatchID != "dsp_legacy" || metadata.JobID != "job_legacy" || metadata.ChainID != "chn_legacy" {
		t.Fatalf("legacy metadata fallback = %+v", metadata)
	}
}

// TestClassifyDatabaseFailure verifies persisted attempt counters distinguish application retry from infrastructure redelivery.
func TestClassifyDatabaseFailure(t *testing.T) {
	cause := errors.New("failed")
	tests := []struct {
		name         string
		job          dbJob
		err          error
		want         databaseFailureSettlement
		wantClassErr bool
	}{
		{
			name: "application retry advances attempt",
			job:  dbJob{attempt: 1, maxRetry: 3, backoffMillis: 250},
			err:  cause,
			want: databaseFailureSettlement{state: "pending", attempt: 2, availableAt: 1250},
		},
		{
			name: "permanent failure becomes dead early",
			job:  dbJob{attempt: 0, maxRetry: 3, backoffMillis: 250},
			err:  busruntime.Permanent(cause),
			want: databaseFailureSettlement{state: "dead", attempt: 1},
		},
		{
			name: "exhausted failure becomes dead",
			job:  dbJob{attempt: 3, maxRetry: 3, backoffMillis: 250},
			err:  cause,
			want: databaseFailureSettlement{state: "dead", attempt: 4},
		},
		{
			name: "uncommitted redelivery preserves attempt",
			job:  dbJob{attempt: 2, maxRetry: 3, backoffMillis: 250},
			err:  busruntime.Uncommitted(cause),
			want: databaseFailureSettlement{state: "pending", attempt: 2, availableAt: 1250},
		},
		{
			name:         "success cannot enter failure persistence",
			job:          dbJob{attempt: 0, maxRetry: 3},
			wantClassErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := classifyDatabaseFailure(&test.job, test.err, 1000)
			if test.wantClassErr {
				if err == nil {
					t.Fatalf("classifyDatabaseFailure() = %+v, nil; want error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("classifyDatabaseFailure(): %v", err)
			}
			if got != test.want {
				t.Fatalf("settlement = %+v, want %+v", got, test.want)
			}
		})
	}
}

// TestRequireDatabaseSettlementRow verifies only one affected durable row commits a delivery outcome.
func TestRequireDatabaseSettlementRow(t *testing.T) {
	rowsErr := errors.New("rows unavailable")
	tests := []struct {
		name    string
		result  sql.Result
		wantErr bool
	}{
		{name: "nil", wantErr: true},
		{name: "zero", result: databaseResultStub{}, wantErr: true},
		{name: "one", result: databaseResultStub{rows: 1}},
		{name: "many", result: databaseResultStub{rows: 2}, wantErr: true},
		{name: "rows error", result: databaseResultStub{err: rowsErr}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := requireDatabaseSettlementRow(test.result)
			if (err != nil) != test.wantErr {
				t.Fatalf("requireDatabaseSettlementRow() error = %v, wantErr %t", err, test.wantErr)
			}
		})
	}
}

// TestDatabaseProcessingClaimRequiresGeneration verifies settlement cannot fall back to an unfenced row identifier.
func TestDatabaseProcessingClaimRequiresGeneration(t *testing.T) {
	tests := []struct {
		name    string
		job     *dbJob
		wantErr bool
	}{
		{name: "nil", wantErr: true},
		{name: "missing id", job: &dbJob{processingToken: "claim"}, wantErr: true},
		{name: "missing token", job: &dbJob{id: 7}, wantErr: true},
		{name: "fenced claim", job: &dbJob{id: 7, processingToken: "claim"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			id, token, err := databaseProcessingClaim(test.job)
			if (err != nil) != test.wantErr {
				t.Fatalf("databaseProcessingClaim() = (%d, %q, %v), wantErr %t", id, token, err, test.wantErr)
			}
			if !test.wantErr && (id != test.job.id || token != test.job.processingToken) {
				t.Fatalf("databaseProcessingClaim() = (%d, %q), want (%d, %q)", id, token, test.job.id, test.job.processingToken)
			}
		})
	}
}

// TestNewDatabaseProcessingToken verifies processing generations fit every additive dialect column.
func TestNewDatabaseProcessingToken(t *testing.T) {
	token, err := newDatabaseProcessingToken()
	if err != nil {
		t.Fatalf("newDatabaseProcessingToken(): %v", err)
	}
	if !databaseProcessingTokenValid(token) {
		t.Fatalf("processing token %q is not canonical", token)
	}
	if len(token) != databaseProcessingTokenBytes*2 || len(token) > 64 {
		t.Fatalf("processing token length = %d, want %d within additive column", len(token), databaseProcessingTokenBytes*2)
	}
	for _, malformed := range []string{
		"",
		strings.Repeat("a", databaseProcessingTokenBytes*2-1),
		strings.Repeat("a", databaseProcessingTokenBytes*2+1),
		strings.Repeat("z", databaseProcessingTokenBytes*2),
		strings.Repeat("A", databaseProcessingTokenBytes*2),
	} {
		if databaseProcessingTokenValid(malformed) {
			t.Fatalf("malformed processing token %q was accepted", malformed)
		}
	}
}

// TestDatabaseSchemaStatementsIncludeAdditiveColumns verifies fresh schemas
// never depend on either compatibility migration pass.
func TestDatabaseSchemaStatementsIncludeAdditiveColumns(t *testing.T) {
	for _, driverName := range []string{"sqlite", "pgx", "mysql"} {
		t.Run(driverName, func(t *testing.T) {
			statements := (&databaseQueue{cfg: localDatabaseConfig{DriverName: driverName}}).schemaStatements()
			if len(statements) == 0 || !strings.Contains(statements[0], "processing_token") {
				t.Fatalf("fresh %s queue schema does not include processing_token", driverName)
			}
			if !strings.Contains(statements[0], "metadata_json TEXT NULL") {
				t.Fatalf("fresh %s queue schema does not include nullable metadata_json", driverName)
			}
		})
	}
}
