package sqlqueuecore

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

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

// LastInsertId returns an unused identifier for the sql.Result contract.
func (r databaseResultStub) LastInsertId() (int64, error) { return 0, nil }

// RowsAffected returns the configured settlement evidence.
func (r databaseResultStub) RowsAffected() (int64, error) { return r.rows, r.err }

// TestDatabaseDeliveryJobRestoresAttemptMetadata verifies SQL persistence reaches the shared orchestration context intact.
func TestDatabaseDeliveryJobRestoresAttemptMetadata(t *testing.T) {
	wantPayload := []byte(`{"report_id":42}`)
	job := databaseDeliveryJob(&dbJob{
		jobType:   "reports:build",
		payload:   wantPayload,
		queueName: "critical",
		attempt:   2,
		maxRetry:  4,
	})
	opts := queuecore.DriverOptions(job)
	if job.Type != "reports:build" || !bytes.Equal(job.PayloadBytes(), wantPayload) {
		t.Fatalf("delivery job = type:%q payload:%q", job.Type, job.PayloadBytes())
	}
	if opts.QueueName != "critical" || opts.Attempt != 2 || opts.MaxRetry == nil || *opts.MaxRetry != 4 {
		t.Fatalf("delivery options = %+v", opts)
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
	decoded, err := hex.DecodeString(token)
	if err != nil {
		t.Fatalf("decode processing token %q: %v", token, err)
	}
	if len(decoded) != databaseProcessingTokenBytes {
		t.Fatalf("processing token bytes = %d, want %d", len(decoded), databaseProcessingTokenBytes)
	}
}

// TestDatabaseSchemaStatementsIncludeProcessingToken verifies fresh schemas never depend on the additive migration pass.
func TestDatabaseSchemaStatementsIncludeProcessingToken(t *testing.T) {
	for _, driverName := range []string{"sqlite", "pgx", "mysql"} {
		t.Run(driverName, func(t *testing.T) {
			statements := (&databaseQueue{cfg: localDatabaseConfig{DriverName: driverName}}).schemaStatements()
			if len(statements) == 0 || !strings.Contains(statements[0], "processing_token") {
				t.Fatalf("fresh %s queue schema does not include processing_token", driverName)
			}
		})
	}
}
