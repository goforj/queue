package sqlqueuecore

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/goforj/queue"
	"github.com/goforj/queue/busruntime"
)

// TestDatabaseDispatchPropagatesUniqueTransactionFailures verifies a unique
// dispatch never reports success when its transaction or database-clock claim
// cannot start.
func TestDatabaseDispatchPropagatesUniqueTransactionFailures(t *testing.T) {
	beginErr := errors.New("begin unavailable")
	clockErr := errors.New("clock unavailable")
	tests := []struct {
		name string
		conn *databaseConnStub
		want error
	}{
		{
			name: "begin transaction",
			conn: &databaseConnStub{beginErr: beginErr},
			want: beginErr,
		},
		{
			name: "read database clock",
			conn: &databaseConnStub{
				query: func(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
					return nil, clockErr
				},
			},
			want: clockErr,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := newDatabaseStub(test.conn)
			defer db.Close()
			database := &databaseQueue{
				cfg: localDatabaseConfig{DriverName: "mysql", DefaultQueue: "default"},
				db:  db,
			}
			job := queue.NewJob("reports:build").
				Payload([]byte(`{"report_id":42}`)).
				OnQueue("default").
				UniqueFor(time.Second)
			if err := database.Dispatch(context.Background(), job); !errors.Is(err, test.want) {
				t.Fatalf("unique dispatch error = %v, want %v", err, test.want)
			}
		})
	}
}

// TestDatabaseClaimRejectsAmbiguousUpdateResults verifies a worker rolls back
// when the database cannot prove that exactly one pending row was fenced.
func TestDatabaseClaimRejectsAmbiguousUpdateResults(t *testing.T) {
	rowsErr := errors.New("rows affected unavailable")
	tests := []struct {
		name    string
		result  driver.Result
		wantErr error
		want    string
	}{
		{
			name:    "rows affected failure",
			result:  databaseResultStub{err: rowsErr},
			wantErr: rowsErr,
			want:    rowsErr.Error(),
		},
		{
			name:   "multiple rows affected",
			result: databaseResultStub{rows: 2},
			want:   "database claim affected 2 rows, want 1",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			execCalls := 0
			conn := &databaseConnStub{
				exec: func(_ context.Context, _ string, _ []driver.NamedValue) (driver.Result, error) {
					execCalls++
					if execCalls == 1 {
						return driver.RowsAffected(0), nil
					}
					return test.result, nil
				},
				query: func(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
					return &databaseRowsStub{
						columns: []string{
							"id", "queue_name", "job_type", "payload", "metadata_json",
							"timeout_seconds", "max_retry", "backoff_millis", "attempt", "processing_token",
						},
						values: [][]driver.Value{{
							int64(42), "default", "reports:build", []byte(`{"report_id":42}`), nil,
							nil, int64(0), int64(0), int64(0), nil,
						}},
					}, nil
				},
			}
			db := newDatabaseStub(conn)
			defer db.Close()
			database := &databaseQueue{
				cfg: localDatabaseConfig{
					DriverName:   "mysql",
					DefaultQueue: "default",
				},
				db: db,
			}

			job, err := database.claimOne(context.Background())
			if job != nil || err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ambiguous claim = (%+v, %v), want nil job and %q", job, err, test.want)
			}
			if test.wantErr != nil && !errors.Is(err, test.wantErr) {
				t.Fatalf("ambiguous claim error = %v, want wrapped %v", err, test.wantErr)
			}
			if conn.rollbackCalls != 1 {
				t.Fatalf("claim rollback calls = %d, want 1", conn.rollbackCalls)
			}
		})
	}
}

// TestDatabaseSettlementFailureIncludesLineageRepairError verifies telemetry
// retains both the original finalization failure and a failed recovery repair.
func TestDatabaseSettlementFailureIncludesLineageRepairError(t *testing.T) {
	settlementErr := errors.New("settlement unavailable")
	var events []queue.Event
	var observedCtx context.Context
	database := &databaseQueue{
		observer: queue.ObserverFunc(func(ctx context.Context, event queue.Event) {
			observedCtx = ctx
			events = append(events, event)
		}),
	}
	job := &dbJob{
		id:              7,
		jobType:         "reports:build",
		queueName:       "critical",
		recovered:       true,
		recoveryToken:   "malformed",
		processingToken: strings.Repeat("b", databaseProcessingTokenBytes*2),
	}

	settlementCtx, _ := busruntime.WithDeliverySettlement(context.Background())
	wantIdentity, ok := busruntime.DeliverySettlementIdentityFromContext(settlementCtx)
	if !ok {
		t.Fatal("settlement context did not expose an identity")
	}
	database.handleSettlementFailure(settlementCtx, job, settlementErr)
	if len(events) != 1 {
		t.Fatalf("settlement failure events = %d, want 1", len(events))
	}
	event := events[0]
	if event.Kind != queue.EventSettlementFailed || !errors.Is(event.Err, settlementErr) {
		t.Fatalf("settlement event = %+v, want original failure", event)
	}
	if !strings.Contains(event.Err.Error(), "restore recovered database settlement lineage") ||
		!strings.Contains(event.Err.Error(), "malformed") {
		t.Fatalf("settlement event error = %v, want recovery repair context", event.Err)
	}
	if observed, observedOK := busruntime.DeliverySettlementIdentityFromContext(observedCtx); !observedOK || observed != wantIdentity {
		t.Fatal("settlement observer did not retain the handler's delivery identity")
	}

	database.observeSettlementFailure(context.Background(), nil, settlementErr)
	if len(events) != 1 {
		t.Fatalf("nil settlement job emitted an event: %+v", events[1:])
	}
}

// TestDatabaseSettlementRejectsInvalidAndUnpersistableOutcomes verifies final
// state changes require a fenced job, a failed attempt, and a durable update.
func TestDatabaseSettlementRejectsInvalidAndUnpersistableOutcomes(t *testing.T) {
	database := &databaseQueue{}
	if err := database.markDone(context.Background(), nil); err == nil {
		t.Fatal("markDone accepted a nil settlement job")
	}
	if err := database.markFailed(context.Background(), nil, errors.New("handler failed")); err == nil {
		t.Fatal("markFailed accepted a nil settlement job")
	}
	job := &dbJob{
		id:              9,
		processingToken: strings.Repeat("c", databaseProcessingTokenBytes*2),
		maxRetry:        1,
	}
	if err := database.markFailed(context.Background(), job, nil); err == nil ||
		!strings.Contains(err.Error(), "successful attempt") {
		t.Fatalf("successful failure settlement error = %v", err)
	}

	updateErr := errors.New("pending settlement unavailable")
	conn := &databaseConnStub{
		exec: func(context.Context, string, []driver.NamedValue) (driver.Result, error) {
			return nil, updateErr
		},
	}
	db := newDatabaseStub(conn)
	defer db.Close()
	database.db = db
	database.cfg.DriverName = "mysql"
	if err := database.markFailed(context.Background(), job, errors.New("handler failed")); !errors.Is(err, updateErr) {
		t.Fatalf("pending settlement error = %v, want %v", err, updateErr)
	}
}

// TestDatabaseUniqueClaimBoundsPersistentState verifies the periodic prune,
// minimum TTL, and lock-insert failures remain part of the surrounding
// transaction outcome.
func TestDatabaseUniqueClaimBoundsPersistentState(t *testing.T) {
	job := queue.NewJob("reports:build").Payload([]byte(`{"report_id":42}`))

	t.Run("periodic prune failure", func(t *testing.T) {
		pruneErr := errors.New("prune unavailable")
		conn := &databaseConnStub{
			query: func(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
				return databaseCountRows(1_000), nil
			},
			exec: func(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
				if !strings.Contains(query, "DELETE FROM queue_unique_locks") {
					return nil, fmt.Errorf("unexpected query after prune failure: %s", query)
				}
				return nil, pruneErr
			},
		}
		db := newDatabaseStub(conn)
		defer db.Close()
		tx, err := db.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatalf("begin unique transaction: %v", err)
		}
		defer tx.Rollback()
		database := &databaseQueue{cfg: localDatabaseConfig{DriverName: "mysql"}}
		database.uniqueClaims.Store(databaseUniquePruneInterval - 1)
		if _, err := database.acquireUnique(context.Background(), tx, job, "default", time.Second); !errors.Is(err, pruneErr) {
			t.Fatalf("periodic prune error = %v, want %v", err, pruneErr)
		}
	})

	t.Run("sub-millisecond ttl", func(t *testing.T) {
		var insertArgs []driver.NamedValue
		conn := &databaseConnStub{
			query: func(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
				return databaseCountRows(1_000), nil
			},
			exec: func(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
				if !strings.Contains(query, "INSERT IGNORE INTO queue_unique_locks") {
					return nil, fmt.Errorf("unexpected uniqueness query: %s", query)
				}
				insertArgs = append([]driver.NamedValue(nil), args...)
				return driver.RowsAffected(1), nil
			},
		}
		db := newDatabaseStub(conn)
		defer db.Close()
		tx, err := db.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatalf("begin unique transaction: %v", err)
		}
		defer tx.Rollback()
		database := &databaseQueue{cfg: localDatabaseConfig{DriverName: "mysql"}}
		acquired, err := database.acquireUnique(context.Background(), tx, job, "default", time.Nanosecond)
		if err != nil || !acquired {
			t.Fatalf("sub-millisecond unique claim = %t, %v", acquired, err)
		}
		if len(insertArgs) != 2 || insertArgs[1].Value != int64(1_001) {
			t.Fatalf("unique insert arguments = %#v, want expiry 1001", insertArgs)
		}
	})

	t.Run("insert failure", func(t *testing.T) {
		insertErr := errors.New("lock insert unavailable")
		conn := &databaseConnStub{
			query: func(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
				return databaseCountRows(1_000), nil
			},
			exec: func(context.Context, string, []driver.NamedValue) (driver.Result, error) {
				return nil, insertErr
			},
		}
		db := newDatabaseStub(conn)
		defer db.Close()
		tx, err := db.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatalf("begin unique transaction: %v", err)
		}
		defer tx.Rollback()
		database := &databaseQueue{cfg: localDatabaseConfig{DriverName: "mysql"}}
		if _, err := database.acquireUnique(context.Background(), tx, job, "default", time.Second); !errors.Is(err, insertErr) {
			t.Fatalf("unique insert error = %v, want %v", err, insertErr)
		}
	})
}

// TestDatabaseAdditiveColumnMigrationHandlesInspectionAndRaces verifies a
// concurrent migration is accepted only after the required column becomes
// visible, while inspection and persistent ALTER failures remain fatal.
func TestDatabaseAdditiveColumnMigrationHandlesInspectionAndRaces(t *testing.T) {
	migrations := []struct {
		name       string
		driverName string
		columnName string
		migrate    func(*databaseQueue, context.Context) error
	}{
		{
			name:       "processing token",
			driverName: "postgres",
			columnName: "processing_token",
			migrate:    (*databaseQueue).ensureProcessingTokenColumn,
		},
		{
			name:       "job metadata",
			driverName: "mysql",
			columnName: "metadata_json",
			migrate:    (*databaseQueue).ensureMetadataJSONColumn,
		},
	}
	inspectionErr := errors.New("column inspection unavailable")
	alterErr := errors.New("alter lost migration race")

	for _, migration := range migrations {
		t.Run(migration.name, func(t *testing.T) {
			scenarios := []struct {
				name          string
				inspectionErr error
				recheckCount  int64
				wantErr       bool
				wantExec      int
			}{
				{name: "inspection failure", inspectionErr: inspectionErr, wantErr: true},
				{name: "concurrent migration", recheckCount: 1, wantExec: 1},
				{name: "persistent alter failure", wantErr: true, wantExec: 1},
			}
			for _, scenario := range scenarios {
				t.Run(scenario.name, func(t *testing.T) {
					queryCalls := 0
					execCalls := 0
					conn := &databaseConnStub{
						query: func(_ context.Context, _ string, args []driver.NamedValue) (driver.Rows, error) {
							queryCalls++
							if len(args) != 1 || args[0].Value != migration.columnName {
								return nil, fmt.Errorf("inspected column arguments = %#v", args)
							}
							if scenario.inspectionErr != nil {
								return nil, scenario.inspectionErr
							}
							if queryCalls == 1 {
								return databaseCountRows(0), nil
							}
							return databaseCountRows(scenario.recheckCount), nil
						},
						exec: func(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
							execCalls++
							if !strings.Contains(query, migration.columnName) {
								return nil, fmt.Errorf("migration query %q does not add %s", query, migration.columnName)
							}
							return nil, alterErr
						},
					}
					db := newDatabaseStub(conn)
					defer db.Close()
					database := &databaseQueue{
						cfg: localDatabaseConfig{DriverName: migration.driverName},
						db:  db,
					}
					err := migration.migrate(database, context.Background())
					if (err != nil) != scenario.wantErr {
						t.Fatalf("migration error = %v, wantErr %t", err, scenario.wantErr)
					}
					if execCalls != scenario.wantExec {
						t.Fatalf("migration executions = %d, want %d", execCalls, scenario.wantExec)
					}
				})
			}
		})
	}
}

type databaseManagedSchemaFault struct {
	missingTable      string
	missingColumn     string
	errorTable        string
	errorColumnsTable string
	err               error
}

// databaseManagedQueueJobColumns is an independent test inventory of every
// queue_jobs field used by runtime SQL.
var databaseManagedQueueJobColumns = []string{
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

// databaseManagedQueueUniqueLockColumns is an independent test inventory of
// every queue_unique_locks field used by runtime SQL.
var databaseManagedQueueUniqueLockColumns = []string{
	"lock_key",
	"expires_at",
}

// databaseManagedSchemaQuery returns a complete dialect-neutral schema
// inspection script with one optional structural or query failure.
func databaseManagedSchemaQuery(fault databaseManagedSchemaFault) func(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return func(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
		if strings.Contains(query, "sqlite_master") || strings.Contains(query, "information_schema.tables") || strings.Contains(query, "FROM pg_class") {
			if len(args) != 1 {
				return nil, fmt.Errorf("table query arguments = %#v", args)
			}
			tableName := fmt.Sprint(args[0].Value)
			if tableName == fault.errorTable {
				return nil, fault.err
			}
			if tableName == fault.missingTable {
				return databaseCountRows(0), nil
			}
			return databaseCountRows(1), nil
		}

		if len(args) != 1 {
			return nil, fmt.Errorf("column query arguments = %#v", args)
		}
		tableName := fmt.Sprint(args[0].Value)
		if tableName == fault.errorColumnsTable {
			return nil, fault.err
		}
		columns := databaseManagedQueueUniqueLockColumns
		if tableName == "queue_jobs" {
			columns = databaseManagedQueueJobColumns
		}
		values := make([][]driver.Value, 0, len(columns))
		for _, columnName := range columns {
			if tableName+"."+columnName != fault.missingColumn {
				values = append(values, []driver.Value{columnName})
			}
		}
		return &databaseRowsStub{
			columns: []string{"column_name"},
			values:  values,
		}, nil
	}
}

// TestDatabaseManagedSchemaValidationRejectsIncompleteBackends verifies worker
// startup validates every table and column used by runtime SQL before polling.
func TestDatabaseManagedSchemaValidationRejectsIncompleteBackends(t *testing.T) {
	validationErr := errors.New("schema inspection unavailable")
	type managedSchemaCase struct {
		name       string
		driverName string
		fault      databaseManagedSchemaFault
		want       string
	}
	tests := []managedSchemaCase{
		{
			name:       "mysql table inspection failure",
			driverName: "mysql",
			fault: databaseManagedSchemaFault{
				errorTable: "queue_jobs",
				err:        validationErr,
			},
			want: "validate caller-managed queue_jobs table",
		},
		{
			name:       "postgres missing queue jobs table",
			driverName: "postgres",
			fault: databaseManagedSchemaFault{
				missingTable: "queue_jobs",
			},
			want: "missing required queue_jobs table",
		},
		{
			name:       "mysql base column inspection failure",
			driverName: "mysql",
			fault: databaseManagedSchemaFault{
				errorColumnsTable: "queue_jobs",
				err:               validationErr,
			},
			want: "validate caller-managed queue_jobs columns",
		},
		{
			name:       "postgres uniqueness column inspection failure",
			driverName: "postgres",
			fault: databaseManagedSchemaFault{
				errorColumnsTable: "queue_unique_locks",
				err:               validationErr,
			},
			want: "validate caller-managed queue_unique_locks columns",
		},
		{
			name:       "postgres uniqueness table inspection failure",
			driverName: "postgres",
			fault: databaseManagedSchemaFault{
				errorTable: "queue_unique_locks",
				err:        validationErr,
			},
			want: "validate caller-managed queue_unique_locks table",
		},
		{
			name:       "mysql missing uniqueness table",
			driverName: "mysql",
			fault: databaseManagedSchemaFault{
				missingTable: "queue_unique_locks",
			},
			want: "missing required queue_unique_locks table",
		},
	}

	for _, columnName := range databaseManagedQueueJobColumns {
		tests = append(tests, managedSchemaCase{
			name:       "mysql missing queue jobs " + columnName,
			driverName: "mysql",
			fault: databaseManagedSchemaFault{
				missingColumn: "queue_jobs." + columnName,
			},
			want: "missing required " + columnName + " column",
		})
	}
	for _, columnName := range databaseManagedQueueUniqueLockColumns {
		tests = append(tests, managedSchemaCase{
			name:       "postgres missing uniqueness " + columnName,
			driverName: "postgres",
			fault: databaseManagedSchemaFault{
				missingColumn: "queue_unique_locks." + columnName,
			},
			want: "missing required " + columnName + " column",
		})
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			execCalls := 0
			conn := &databaseConnStub{
				query: databaseManagedSchemaQuery(test.fault),
				exec: func(context.Context, string, []driver.NamedValue) (driver.Result, error) {
					execCalls++
					return nil, errors.New("managed validation performed DDL")
				},
			}
			db := newDatabaseStub(conn)
			defer db.Close()
			database := &databaseQueue{
				cfg: localDatabaseConfig{
					DriverName:  test.driverName,
					AutoMigrate: false,
				},
				db:         db,
				shutdownCh: make(chan struct{}),
			}
			err := database.StartWorkers(context.Background())
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("managed schema startup error = %v, want %q", err, test.want)
			}
			if test.fault.err != nil && !errors.Is(err, test.fault.err) {
				t.Fatalf("managed schema startup error = %v, want wrapped %v", err, test.fault.err)
			}
			if database.started.Load() {
				t.Fatal("workers started after caller-managed schema validation failed")
			}
			if execCalls != 0 {
				t.Fatalf("managed schema validation executed %d statements, want read-only inspection", execCalls)
			}
		})
	}
}

// TestDatabaseManagedSchemaPreflightAndStartupRetry verifies readiness and
// worker startup share read-only validation and recover after schema repair.
func TestDatabaseManagedSchemaPreflightAndStartupRetry(t *testing.T) {
	missingUniqueTable := true
	execCalls := 0
	queryCalls := 0
	conn := &databaseConnStub{
		query: func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			queryCalls++
			fault := databaseManagedSchemaFault{}
			if missingUniqueTable {
				fault.missingTable = "queue_unique_locks"
			}
			return databaseManagedSchemaQuery(fault)(ctx, query, args)
		},
		exec: func(context.Context, string, []driver.NamedValue) (driver.Result, error) {
			execCalls++
			return nil, errors.New("managed validation performed DDL")
		},
	}
	db := newDatabaseStub(conn)
	defer db.Close()
	database := &databaseQueue{
		cfg: localDatabaseConfig{
			DriverName:         "mysql",
			AutoMigrate:        true,
			DisableAutoMigrate: true,
		},
		db:         db,
		shutdownCh: make(chan struct{}),
	}

	preflightErr := database.Preflight(context.Background())
	startErr := database.StartWorkers(context.Background())
	if preflightErr == nil || startErr == nil || preflightErr.Error() != startErr.Error() {
		t.Fatalf("managed validation errors = (preflight: %v, startup: %v), want matching failures", preflightErr, startErr)
	}
	if database.started.Load() {
		t.Fatal("failed managed startup latched the queue as started")
	}
	if queryCalls != 6 {
		t.Fatalf("failed managed validation queries = %d, want three per attempt", queryCalls)
	}

	missingUniqueTable = false
	queryCalls = 0
	if err := database.Preflight(context.Background()); err != nil {
		t.Fatalf("managed preflight after schema repair: %v", err)
	}
	if err := database.StartWorkers(context.Background()); err != nil {
		t.Fatalf("managed startup after schema repair: %v", err)
	}
	if !database.started.Load() {
		t.Fatal("managed startup did not become ready after schema repair")
	}
	if queryCalls != 8 {
		t.Fatalf("successful managed validation queries = %d, want four per attempt", queryCalls)
	}
	if execCalls != 0 {
		t.Fatalf("managed validation executed %d statements, want no DDL", execCalls)
	}
}

// TestDatabaseManagedSchemaColumnInspectionReportsFailures verifies catalog
// conversion and iteration errors remain visible instead of resembling absent columns.
func TestDatabaseManagedSchemaColumnInspectionReportsFailures(t *testing.T) {
	iterationErr := errors.New("catalog iteration unavailable")
	tests := []struct {
		name string
		rows driver.Rows
		want string
	}{
		{
			name: "scan failure",
			rows: &databaseRowsStub{
				columns: []string{"column_name"},
				values:  [][]driver.Value{{nil}},
			},
			want: "scan queue_jobs column",
		},
		{
			name: "iteration failure",
			rows: &databaseRowsStub{
				columns: []string{"column_name"},
				err:     iterationErr,
			},
			want: "inspect queue_jobs columns",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			conn := &databaseConnStub{
				query: func(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
					return test.rows, nil
				},
			}
			db := newDatabaseStub(conn)
			defer db.Close()
			database := &databaseQueue{
				cfg: localDatabaseConfig{DriverName: "mysql"},
				db:  db,
			}
			_, err := database.managedQueueTableColumns(context.Background(), managedQueueJobsTable)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("managed column inspection error = %v, want %q", err, test.want)
			}
			if test.name == "iteration failure" && !errors.Is(err, iterationErr) {
				t.Fatalf("managed column inspection error = %v, want wrapped %v", err, iterationErr)
			}
		})
	}
}

// TestDatabaseManagedSchemaSQLiteCatalog verifies SQLite readiness uses the
// same complete, bounded structural inspection as the server SQL dialects.
func TestDatabaseManagedSchemaSQLiteCatalog(t *testing.T) {
	queryCalls := 0
	conn := &databaseConnStub{
		query: func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			queryCalls++
			return databaseManagedSchemaQuery(databaseManagedSchemaFault{})(ctx, query, args)
		},
	}
	db := newDatabaseStub(conn)
	defer db.Close()
	database := &databaseQueue{
		cfg: localDatabaseConfig{DriverName: "sqlite"},
		db:  db,
	}
	if err := database.requireManagedQueueSchema(context.Background()); err != nil {
		t.Fatalf("sqlite managed schema validation: %v", err)
	}
	if queryCalls != 4 {
		t.Fatalf("sqlite managed schema queries = %d, want 4", queryCalls)
	}
}

// TestDatabaseManagedSchemaRejectsNonBaseRelations verifies views cannot pass
// a readiness gate whose runtime contract requires writes and row locking.
func TestDatabaseManagedSchemaRejectsNonBaseRelations(t *testing.T) {
	tests := []struct {
		name       string
		driverName string
		baseFilter string
	}{
		{
			name:       "mysql view",
			driverName: "mysql",
			baseFilter: "table_type = 'BASE TABLE'",
		},
		{
			name:       "postgres view",
			driverName: "postgres",
			baseFilter: "relkind IN ('r', 'p')",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			conn := &databaseConnStub{
				query: func(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
					if len(args) != 1 || fmt.Sprint(args[0].Value) != "queue_jobs" {
						return nil, fmt.Errorf("table query arguments = %#v", args)
					}
					if !strings.Contains(query, test.baseFilter) {
						return databaseCountRows(1), nil
					}
					return databaseCountRows(0), nil
				},
			}
			db := newDatabaseStub(conn)
			defer db.Close()
			database := &databaseQueue{
				cfg: localDatabaseConfig{DriverName: test.driverName},
				db:  db,
			}
			err := database.requireManagedQueueSchema(context.Background())
			if err == nil || !strings.Contains(err.Error(), "missing required queue_jobs table") {
				t.Fatalf("managed view validation error = %v, want missing base-table diagnostic", err)
			}
		})
	}
}

// TestDatabasePreflightSkipsManagedValidationWhenMigrationsAreEnabled verifies
// readiness does not require a schema that worker startup is configured to create.
func TestDatabasePreflightSkipsManagedValidationWhenMigrationsAreEnabled(t *testing.T) {
	queryCalls := 0
	conn := &databaseConnStub{
		query: func(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
			queryCalls++
			return nil, errors.New("unexpected schema inspection")
		},
	}
	db := newDatabaseStub(conn)
	defer db.Close()
	database := &databaseQueue{
		cfg: localDatabaseConfig{AutoMigrate: true},
		db:  db,
	}
	if err := database.Preflight(context.Background()); err != nil {
		t.Fatalf("auto-migrating preflight: %v", err)
	}
	if queryCalls != 0 {
		t.Fatalf("auto-migrating preflight schema queries = %d, want 0", queryCalls)
	}
}

// TestDatabasePreflightPropagatesPingFailureBeforeManagedValidation verifies a
// connectivity failure remains the primary readiness diagnostic.
func TestDatabasePreflightPropagatesPingFailureBeforeManagedValidation(t *testing.T) {
	pingErr := errors.New("database unavailable")
	queryCalls := 0
	conn := &databaseConnStub{
		pingErr: pingErr,
		query: func(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
			queryCalls++
			return nil, errors.New("unexpected schema inspection")
		},
	}
	db := newDatabaseStub(conn)
	defer db.Close()
	database := &databaseQueue{db: db}
	if err := database.Preflight(context.Background()); !errors.Is(err, pingErr) {
		t.Fatalf("database preflight = %v, want %v", err, pingErr)
	}
	if queryCalls != 0 {
		t.Fatalf("database preflight queries after failed ping = %d, want 0", queryCalls)
	}
}

// TestDatabaseSQLiteColumnInspectionReportsFailures verifies caller-managed
// startup can distinguish query, row conversion, and row iteration failures.
func TestDatabaseSQLiteColumnInspectionReportsFailures(t *testing.T) {
	queryErr := errors.New("pragma unavailable")
	rowsErr := errors.New("pragma iteration failed")
	tests := []struct {
		name string
		rows driver.Rows
		err  error
		want string
	}{
		{name: "query failure", err: queryErr, want: "inspect sqlite queue job column"},
		{
			name: "scan failure",
			rows: &databaseRowsStub{
				columns: []string{"cid", "name", "type", "notnull", "default", "pk"},
				values:  [][]driver.Value{{"invalid", "metadata_json", "TEXT", int64(0), nil, int64(0)}},
			},
			want: "scan sqlite queue column",
		},
		{
			name: "iteration failure",
			rows: &databaseRowsStub{
				columns: []string{"cid", "name", "type", "notnull", "default", "pk"},
				err:     rowsErr,
			},
			want: "inspect sqlite queue columns",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			conn := &databaseConnStub{
				query: func(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
					return test.rows, test.err
				},
			}
			db := newDatabaseStub(conn)
			defer db.Close()
			database := &databaseQueue{
				cfg: localDatabaseConfig{DriverName: "sqlite"},
				db:  db,
			}
			_, err := database.queueJobColumnExists(context.Background(), "metadata_json")
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("sqlite column inspection error = %v, want %q", err, test.want)
			}
		})
	}
}

// TestDatabaseSchemaStartupPropagatesCompatibilityFailures verifies schema
// initialization stops at each additive compatibility dependency.
func TestDatabaseSchemaStartupPropagatesCompatibilityFailures(t *testing.T) {
	stageErr := errors.New("compatibility dependency unavailable")
	for _, stage := range []string{"processing column", "metadata column", "mysql index", "database clock"} {
		t.Run(stage, func(t *testing.T) {
			conn := &databaseConnStub{
				exec: func(context.Context, string, []driver.NamedValue) (driver.Result, error) {
					return driver.RowsAffected(0), nil
				},
				query: func(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
					if len(args) == 1 {
						switch args[0].Value {
						case "processing_token":
							if stage == "processing column" {
								return nil, stageErr
							}
							return databaseCountRows(1), nil
						case "metadata_json":
							if stage == "metadata column" {
								return nil, stageErr
							}
							return databaseCountRows(1), nil
						case "idx_queue_unique_locks_expires":
							if stage == "mysql index" {
								return nil, stageErr
							}
							return databaseCountRows(1), nil
						}
					}
					if stage == "database clock" && strings.Contains(query, "UNIX_TIMESTAMP") {
						return nil, stageErr
					}
					return nil, fmt.Errorf("unexpected schema query at %s: %s", stage, query)
				},
			}
			db := newDatabaseStub(conn)
			defer db.Close()
			database := &databaseQueue{
				cfg: localDatabaseConfig{DriverName: "mysql"},
				db:  db,
			}
			if err := database.ensureSchema(context.Background()); !errors.Is(err, stageErr) {
				t.Fatalf("schema startup error = %v, want %v", err, stageErr)
			}
		})
	}
}

// TestDatabaseMySQLIndexMigrationHandlesInspectionAndRaces verifies the
// additive expiry index accepts a concurrent winner but not an unverifiable
// ALTER failure.
func TestDatabaseMySQLIndexMigrationHandlesInspectionAndRaces(t *testing.T) {
	inspectionErr := errors.New("index inspection unavailable")
	alterErr := errors.New("index alter failed")
	tests := []struct {
		name          string
		inspectionErr error
		recheckCount  int64
		wantErr       bool
		wantExec      int
	}{
		{name: "inspection failure", inspectionErr: inspectionErr, wantErr: true},
		{name: "concurrent migration", recheckCount: 1, wantExec: 1},
		{name: "persistent alter failure", wantErr: true, wantExec: 1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			queryCalls := 0
			execCalls := 0
			conn := &databaseConnStub{
				query: func(_ context.Context, _ string, args []driver.NamedValue) (driver.Rows, error) {
					queryCalls++
					if len(args) != 1 || args[0].Value != "idx_queue_unique_locks_expires" {
						return nil, fmt.Errorf("index query arguments = %#v", args)
					}
					if test.inspectionErr != nil {
						return nil, test.inspectionErr
					}
					if queryCalls == 1 {
						return databaseCountRows(0), nil
					}
					return databaseCountRows(test.recheckCount), nil
				},
				exec: func(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
					execCalls++
					if !strings.Contains(query, "ADD INDEX idx_queue_unique_locks_expires") {
						return nil, fmt.Errorf("unexpected index migration: %s", query)
					}
					return nil, alterErr
				},
			}
			db := newDatabaseStub(conn)
			defer db.Close()
			database := &databaseQueue{
				cfg: localDatabaseConfig{DriverName: "mysql"},
				db:  db,
			}
			err := database.ensureMySQLUniqueExpiryIndex(context.Background())
			if (err != nil) != test.wantErr {
				t.Fatalf("index migration error = %v, wantErr %t", err, test.wantErr)
			}
			if execCalls != test.wantExec {
				t.Fatalf("index migration executions = %d, want %d", execCalls, test.wantExec)
			}
		})
	}
}
