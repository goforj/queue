package sqlqueuecore

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"

	"github.com/goforj/queue"
	"github.com/goforj/queue/busruntime"
)

// TestRecoverStaleProcessingEmitsCountFacts verifies bulk recovery reports one
// normalized, identity-free fact for every row the fenced update changed.
func TestRecoverStaleProcessingEmitsCountFacts(t *testing.T) {
	connection := &databaseConnStub{
		exec: func(context.Context, string, []driver.NamedValue) (driver.Result, error) {
			return driver.RowsAffected(2), nil
		},
	}
	db := newDatabaseStub(connection)
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close database stub: %v", err)
		}
	})

	var events []queue.Event
	database := &databaseQueue{
		db: db,
		cfg: localDatabaseConfig{
			DriverName:   "sqlite",
			DefaultQueue: "default",
		},
		observer: queue.ObserverFunc(func(_ context.Context, event queue.Event) {
			events = append(events, event)
		}),
	}
	if err := database.recoverStaleProcessing(context.Background(), 1_000); err != nil {
		t.Fatalf("recover stale processing: %v", err)
	}

	if len(events) != 2 {
		t.Fatalf("recovery events = %+v, want one fact per recovered row", events)
	}
	seenIDs := make(map[string]struct{}, len(events))
	for index, event := range events {
		if event.Kind != queue.EventProcessRecovered || event.Layer != queue.EventLayerWorker || event.Driver != queue.DriverDatabase {
			t.Errorf("recovery event %d = %+v, want normalized database worker fact", index, event)
		}
		if event.SchemaVersion == 0 || event.EventID == "" || event.Time.IsZero() {
			t.Errorf("recovery event %d has incomplete envelope metadata: %+v", index, event)
		}
		if event.Queue != "" || event.JobType != "" || event.JobKey != "" || event.DispatchID != "" || event.JobID != "" || event.ChainID != "" || event.BatchID != "" {
			t.Errorf("recovery event %d invented identity unavailable from the bulk update: %+v", index, event)
		}
		if _, duplicate := seenIDs[event.EventID]; duplicate {
			t.Errorf("recovery event %d reused event ID %q", index, event.EventID)
		}
		seenIDs[event.EventID] = struct{}{}
	}
}

// TestRecoverStaleProcessingRemainsSilentWithoutConfirmedRows verifies the
// recovery observer never invents a fact when execution fails or row evidence
// is absent or unavailable.
func TestRecoverStaleProcessingRemainsSilentWithoutConfirmedRows(t *testing.T) {
	execErr := errors.New("recovery update failed")
	rowsErr := errors.New("recovery row count unavailable")
	tests := []struct {
		name    string
		result  driver.Result
		execErr error
		wantErr error
	}{
		{name: "no stale rows", result: driver.RowsAffected(0)},
		{name: "execution failure", execErr: execErr, wantErr: execErr},
		{name: "row count unavailable", result: databaseResultStub{err: rowsErr}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			connection := &databaseConnStub{
				exec: func(context.Context, string, []driver.NamedValue) (driver.Result, error) {
					return test.result, test.execErr
				},
			}
			db := newDatabaseStub(connection)
			t.Cleanup(func() {
				if err := db.Close(); err != nil {
					t.Errorf("close database stub: %v", err)
				}
			})

			var events []queue.Event
			database := &databaseQueue{
				db: db,
				cfg: localDatabaseConfig{
					DriverName:   "sqlite",
					DefaultQueue: "default",
				},
				observer: queue.ObserverFunc(func(_ context.Context, event queue.Event) {
					events = append(events, event)
				}),
			}
			err := database.recoverStaleProcessing(context.Background(), 1_000)
			if test.wantErr != nil && !errors.Is(err, test.wantErr) {
				t.Fatalf("recover stale processing error = %v, want %v", err, test.wantErr)
			}
			if test.wantErr == nil && err != nil {
				t.Fatalf("recover stale processing error = %v, want nil", err)
			}
			if len(events) != 0 {
				t.Fatalf("unconfirmed recovery emitted facts: %+v", events)
			}
		})
	}
}

// TestDatabaseProcessArchivedRequiresConfirmedTerminalState verifies the SQL
// driver emits archive facts only after a fenced dead-state transition succeeds.
func TestDatabaseProcessArchivedRequiresConfirmedTerminalState(t *testing.T) {
	tests := []struct {
		name        string
		attempt     int
		maxRetry    int
		register    bool
		wantArchive bool
		wantState   string
	}{
		{name: "exhausted handler failure", attempt: 2, maxRetry: 2, register: true, wantArchive: true, wantState: "state='dead'"},
		{name: "permanent handler failure", maxRetry: 3, register: true, wantArchive: true, wantState: "state='dead'"},
		{name: "missing handler terminal failure", wantArchive: true, wantState: "state='dead'"},
		{name: "retry remains pending", maxRetry: 1, register: true, wantState: "state='pending'"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var settlementQuery string
			connection := &databaseConnStub{
				exec: func(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
					settlementQuery = query
					return driver.RowsAffected(1), nil
				},
			}
			db := newDatabaseStub(connection)
			t.Cleanup(func() {
				if err := db.Close(); err != nil {
					t.Errorf("close database stub: %v", err)
				}
			})

			var events []queue.Event
			database := &databaseQueue{
				db:           db,
				cfg:          localDatabaseConfig{DriverName: "sqlite", DefaultQueue: "default"},
				handlers:     make(map[string]queue.Handler),
				continuation: busruntime.NewContinuationScope(),
				observer: queue.ObserverFunc(func(_ context.Context, event queue.Event) {
					events = append(events, event)
				}),
			}
			if test.register {
				database.handlers["bus:job"] = func(context.Context, queue.Job) error {
					if test.name == "permanent handler failure" {
						return busruntime.Permanent(errors.New("invalid report"))
					}
					return errors.New("report failed")
				}
			}
			payload := []byte(`{"schema_version":1,"dispatch_id":"dsp_sql_archive","job_id":"job_sql_archive","job":{"type":"reports:build","payload":"eyJpZCI6MX0="}}`)
			database.processJob(&dbJob{
				id:              42,
				processingToken: "owned-generation",
				queueName:       "critical",
				jobType:         "bus:job",
				payload:         payload,
				attempt:         test.attempt,
				maxRetry:        test.maxRetry,
			})

			if !strings.Contains(settlementQuery, test.wantState) {
				t.Fatalf("settlement query = %q, want %q transition", settlementQuery, test.wantState)
			}
			if !test.wantArchive {
				if len(events) != 0 {
					t.Fatalf("retryable settlement emitted archive facts: %+v", events)
				}
				return
			}
			if len(events) != 1 {
				t.Fatalf("archive events = %+v, want exactly one", events)
			}
			archive := events[0]
			if archive.Kind != queue.EventProcessArchived || archive.Layer != queue.EventLayerWorker || archive.Driver != queue.DriverDatabase {
				t.Fatalf("archive event = %+v, want normalized database worker fact", archive)
			}
			if archive.Queue != "critical" || archive.JobType != "reports:build" || archive.DispatchID != "dsp_sql_archive" || archive.JobID != "job_sql_archive" {
				t.Fatalf("archive correlation = %+v", archive)
			}
			if archive.Attempt != test.attempt || archive.MaxRetry != test.maxRetry || archive.Err == nil || archive.SchemaVersion == 0 || archive.EventID == "" || archive.Time.IsZero() {
				t.Fatalf("archive attempt or envelope metadata = %+v", archive)
			}
		})
	}
}

// TestDatabaseTerminalSettlementFailureNeverArchives verifies an archive fact
// requires positive evidence that exactly one fenced row reached dead state.
func TestDatabaseTerminalSettlementFailureNeverArchives(t *testing.T) {
	execErr := errors.New("terminal update failed")
	rowsErr := errors.New("terminal row count unavailable")
	settlements := []struct {
		name       string
		result     driver.Result
		execErr    error
		wantErr    error
		wantDetail string
	}{
		{name: "execution failure", execErr: execErr, wantErr: execErr},
		{name: "lost fence", result: driver.RowsAffected(0), wantDetail: "affected 0 rows, want 1"},
		{name: "ambiguous update", result: driver.RowsAffected(2), wantDetail: "affected 2 rows, want 1"},
		{name: "row count unavailable", result: databaseResultStub{err: rowsErr}, wantErr: rowsErr},
	}
	deliveries := []struct {
		name     string
		register bool
		attempt  int
		maxRetry int
	}{
		{name: "exhausted handler failure", register: true, attempt: 2, maxRetry: 2},
		{name: "missing handler", attempt: 0, maxRetry: 0},
	}
	for _, delivery := range deliveries {
		for _, settlement := range settlements {
			t.Run(delivery.name+"/"+settlement.name, func(t *testing.T) {
				var settlementQueries []string
				connection := &databaseConnStub{
					exec: func(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
						settlementQueries = append(settlementQueries, query)
						return settlement.result, settlement.execErr
					},
				}
				db := newDatabaseStub(connection)
				t.Cleanup(func() {
					if err := db.Close(); err != nil {
						t.Errorf("close database stub: %v", err)
					}
				})

				var events []queue.Event
				database := &databaseQueue{
					db:           db,
					cfg:          localDatabaseConfig{DriverName: "sqlite", DefaultQueue: "default"},
					handlers:     make(map[string]queue.Handler),
					continuation: busruntime.NewContinuationScope(),
					observer: queue.ObserverFunc(func(_ context.Context, event queue.Event) {
						events = append(events, event)
					}),
				}
				if delivery.register {
					database.handlers["bus:job"] = func(context.Context, queue.Job) error {
						return errors.New("report failed")
					}
				}
				payload := []byte(`{"schema_version":1,"dispatch_id":"dsp_sql_failed_archive","job_id":"job_sql_failed_archive","job":{"type":"reports:build","payload":"eyJpZCI6MX0="}}`)
				database.processJob(&dbJob{
					id:              42,
					processingToken: "owned-generation",
					queueName:       "critical",
					jobType:         "bus:job",
					payload:         payload,
					attempt:         delivery.attempt,
					maxRetry:        delivery.maxRetry,
				})

				if len(settlementQueries) != databaseFinalizeRetryCount {
					t.Fatalf("terminal settlement attempts = %d, want %d", len(settlementQueries), databaseFinalizeRetryCount)
				}
				for index, query := range settlementQueries {
					if !strings.Contains(query, "state='dead'") {
						t.Fatalf("settlement query %d = %q, want terminal dead-state update", index, query)
					}
				}
				if len(events) != 1 {
					t.Fatalf("terminal settlement failure events = %+v, want exactly one", events)
				}
				event := events[0]
				if event.Kind != queue.EventSettlementFailed {
					t.Fatalf("terminal settlement event = %+v, want settlement_failed without process_archived", event)
				}
				if settlement.wantErr != nil && !errors.Is(event.Err, settlement.wantErr) {
					t.Fatalf("terminal settlement error = %v, want wrapped %v", event.Err, settlement.wantErr)
				}
				if settlement.wantDetail != "" && !strings.Contains(event.Err.Error(), settlement.wantDetail) {
					t.Fatalf("terminal settlement error = %v, want %q", event.Err, settlement.wantDetail)
				}
			})
		}
	}
}
