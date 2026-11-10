package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"
)

// namedSQLAuthority creates an independently recoverable obligation before injecting database failures.
func namedSQLAuthority(t *testing.T) (*sqlStore, continuationClaim) {
	t.Helper()
	store := newSQLiteStore(t).(*sqlStore)
	ctx := t.Context()
	if err := store.PrepareContinuations(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateChain(ctx, namedChainRecord("parent")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.AdvanceChain(ctx, "parent", "member"); err != nil {
		t.Fatal(err)
	}
	claim, ok, err := store.ClaimContinuation(ctx, "resource", "publisher", time.Now(), time.Minute)
	if err != nil || !ok {
		t.Fatalf("setup claim: %t %v", ok, err)
	}
	return store, claim
}

// TestSQLContinuationAuthorityWriteFailuresRollback proves failed begin, renewal and settlement writes preserve durable ownership and outcomes.
func TestSQLContinuationAuthorityWriteFailuresRollback(t *testing.T) {
	for _, operation := range []string{"begin", "renew", "settle"} {
		t.Run(operation, func(t *testing.T) {
			store, publication := namedSQLAuthority(t)
			ctx := t.Context()
			claim := publication
			if operation != "begin" {
				var ok bool
				var err error
				claim, ok, err = store.BeginContinuation(ctx, publication, "worker", time.Now())
				if err != nil || !ok {
					t.Fatal(ok, err)
				}
			}
			before, err := store.ContinuationOutcomes(ctx, "parent")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.ExecContext(ctx, `CREATE TRIGGER fail_authority BEFORE UPDATE ON bus_continuations_v1 BEGIN SELECT RAISE(ABORT,'synthetic write failure'); END`); err != nil {
				t.Fatal(err)
			}
			switch operation {
			case "begin":
				_, _, err = store.BeginContinuation(ctx, publication, "worker", time.Now())
			case "renew":
				err = store.RenewContinuation(ctx, claim, time.Now())
			case "settle":
				err = store.SettleContinuation(ctx, claim, "succeeded", time.Now())
			}
			if err == nil {
				t.Fatal("faulted authority write reported success")
			}
			after, err := store.ContinuationOutcomes(ctx, "parent")
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("failed write changed outcomes: before=%+v after=%+v error=%v", before, after, err)
			}
			if _, err := store.db.ExecContext(ctx, `DROP TRIGGER fail_authority`); err != nil {
				t.Fatal(err)
			}
			if operation == "begin" {
				var ok bool
				claim, ok, err = store.BeginContinuation(ctx, publication, "worker", time.Now())
				if err != nil || !ok || claim.Attempt != 0 {
					t.Fatal("failed begin consumed attempt", claim, ok, err)
				}
			}
			if err := store.RenewContinuation(ctx, claim, time.Now()); err != nil {
				t.Fatal("winning owner lost authority", err)
			}
			if err := store.SettleContinuation(ctx, claim, "succeeded", time.Now()); err != nil {
				t.Fatal("authority did not recover", err)
			}
		})
	}
}

// TestContinuationMissingAuthorityCannotExecuteOrSettle checks memory and SQL reject phantom physical deliveries consistently.
func TestContinuationMissingAuthorityCannotExecuteOrSettle(t *testing.T) {
	for name, store := range continuationStores(t) {
		t.Run(name, func(t *testing.T) {
			capable := store.(ContinuationStore)
			ctx := t.Context()
			if err := capable.PrepareContinuations(ctx); err != nil {
				t.Fatal(err)
			}
			claim := continuationClaim{WorkflowID: "absent", Resource: "resource"}
			if valid, err := capable.ValidateContinuation(ctx, claim); err != nil || valid {
				t.Fatal("phantom delivery validated", valid, err)
			}
			if _, ok, err := capable.BeginContinuation(ctx, claim, "worker", time.Now()); err != nil || ok {
				t.Fatal("phantom delivery began", ok, err)
			}
			if err := capable.SettleContinuation(ctx, claim, "succeeded", time.Now()); err == nil {
				t.Fatal("phantom settlement accepted")
			}
			if err := capable.RenewContinuation(ctx, claim, time.Now()); err == nil {
				t.Fatal("phantom execution renewed")
			}
			if _, err := capable.ContinuationOutcomes(ctx, "absent"); !errors.Is(err, ErrNotFound) {
				t.Fatal("phantom history did not report absence", err)
			}
		})
	}
}

// TestSQLContinuationMalformedLedgerFailsClosed exercises every authority operation against retained corruption without invoking business work.
func TestSQLContinuationMalformedLedgerFailsClosed(t *testing.T) {
	for _, scenario := range []string{"invalid JSON", "version", "identity"} {
		t.Run(scenario, func(t *testing.T) {
			store, claim := namedSQLAuthority(t)
			ctx := t.Context()
			raw := []byte("{")
			if scenario != "invalid JSON" {
				ledger := continuationLedger{Version: 1, WorkflowID: "parent"}
				if scenario == "version" {
					ledger.Version = 99
				} else {
					ledger.WorkflowID = "different"
				}
				var err error
				raw, err = json.Marshal(ledger)
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err := store.db.ExecContext(ctx, `UPDATE bus_continuations_v1 SET ledger_json=? WHERE workflow_id='parent'`, raw); err != nil {
				t.Fatal(err)
			}
			operations := map[string]func() error{
				"read":     func() error { _, err := store.ContinuationOutcomes(ctx, "parent"); return err },
				"validate": func() error { _, err := store.ValidateContinuation(ctx, claim); return err },
				"begin":    func() error { _, _, err := store.BeginContinuation(ctx, claim, "worker", time.Now()); return err },
				"renew":    func() error { return store.RenewContinuation(ctx, claim, time.Now()) },
				"settle":   func() error { return store.SettleContinuation(ctx, claim, "succeeded", time.Now()) },
				"claim": func() error {
					_, _, err := store.ClaimContinuation(ctx, "resource", "new publisher", time.Now().Add(time.Hour), time.Minute)
					return err
				},
			}
			for name, operation := range operations {
				t.Run(name, func(t *testing.T) {
					if err := operation(); !errors.Is(err, ErrContinuationProtocol) {
						t.Fatal("corrupt authority not rejected as protocol failure", err)
					}
				})
			}
		})
	}
}

// TestSQLContinuationCanceledOperationsPreserveCancellation checks transient infrastructure errors never masquerade as missing authority.
func TestSQLContinuationCanceledOperationsPreserveCancellation(t *testing.T) {
	store, claim := namedSQLAuthority(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	operations := map[string]func() error{
		"read": func() error { _, err := store.ContinuationOutcomes(ctx, "parent"); return err },
		"claim": func() error {
			_, _, err := store.ClaimContinuation(ctx, "resource", "publisher", time.Now(), time.Minute)
			return err
		},
		"begin":  func() error { _, _, err := store.BeginContinuation(ctx, claim, "worker", time.Now()); return err },
		"settle": func() error { return store.SettleContinuation(ctx, claim, "succeeded", time.Now()) },
		"renew":  func() error { return store.RenewContinuation(ctx, claim, time.Now()) },
	}
	for name, operation := range operations {
		t.Run(name, func(t *testing.T) {
			if err := operation(); !errors.Is(err, context.Canceled) {
				t.Fatal("cancellation lost", err)
			}
		})
	}
}

// TestSQLContinuationManagedSchemaRejectsMissingColumns performs no repair DDL when the declared recovery table is incompatible.
func TestSQLContinuationManagedSchemaRejectsMissingColumns(t *testing.T) {
	store := newSQLiteStore(t).(*sqlStore)
	ctx := t.Context()
	if err := store.ensureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `CREATE TABLE bus_continuations_v1 (workflow_id TEXT, resource_key TEXT, active INTEGER, available_at_ms BIGINT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `CREATE INDEX bus_continuations_v1_ready ON bus_continuations_v1(resource_key,active,available_at_ms)`); err != nil {
		t.Fatal(err)
	}
	managed := &sqlStore{db: store.db, driverName: "sqlite", autoMigrate: false}
	if err := managed.PrepareContinuations(ctx); !errors.Is(err, ErrContinuationProtocol) {
		t.Fatal("incompatible managed schema accepted", err)
	}
}
