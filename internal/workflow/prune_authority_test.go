package workflow

import (
	"context"
	"errors"
	"fmt"
	goruntime "runtime"
	"strings"
	"testing"
	"time"
)

// createPruneFixture settles a chain or batch with optional immutable named obligations.
func createPruneFixture(t *testing.T, store *sqlStore, kind, id string, named bool) {
	t.Helper()
	var plan *continuationPlan
	if named {
		plan = &continuationPlan{Resource: "resource", Definitions: []ContinuationDefinition{{Phase: "success", Job: StoredJob{Type: "hook"}}, {Phase: "completion", Job: StoredJob{Type: "complete"}}}}
	}
	if kind == "chain" {
		record := namedChainRecord(id)
		record.continuation = plan
		if err := store.CreateChain(t.Context(), record); err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.AdvanceChain(t.Context(), id, "member"); err != nil {
			t.Fatal(err)
		}
	} else {
		if err := store.CreateBatch(t.Context(), BatchRecord{BatchID: id, DispatchID: "dispatch-" + id, Jobs: []BatchJob{{JobID: "member", Job: StoredJob{Type: "business"}}}, continuation: plan}); err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.MarkBatchJobSucceeded(t.Context(), id, "member"); err != nil {
			t.Fatal(err)
		}
	}
}

// pruneFixtureError reads actual parent authority separately from its continuation ledger.
func pruneFixtureError(ctx context.Context, store *sqlStore, kind, id string) error {
	if kind == "chain" {
		_, err := store.GetChain(ctx, id)
		return err
	}
	_, err := store.GetBatch(ctx, id)
	return err
}

// TestSQLPruneDiscoversConcurrentNamedSchema covers a second store introducing the first named workflow after pruning's initial schema probe.
func TestSQLPruneDiscoversConcurrentNamedSchema(t *testing.T) {
	for _, kind := range []string{"chain", "batch"} {
		t.Run(kind, func(t *testing.T) {
			pruner := newSQLiteStore(t).(*sqlStore)
			pruner.db.SetMaxOpenConns(1)
			producer := &sqlStore{db: pruner.db, driverName: "sqlite", autoMigrate: true}
			createPruneFixture(t, pruner, kind, "legacy", false)
			pruner.sqliteWriteMu.Lock()
			locked := true
			defer func() {
				if locked {
					pruner.sqliteWriteMu.Unlock()
				}
			}()
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- pruner.Prune(ctx, time.Now().Add(time.Hour)) }()
			trace := make([]byte, 1<<16)
			tick := time.NewTicker(time.Millisecond)
			defer tick.Stop()
			for {
				n := goruntime.Stack(trace, true)
				stack := string(trace[:n])
				if strings.Contains(stack, "workflow.(*sqlStore).lockWriteTransaction") && strings.Contains(stack, "workflow.(*sqlStore).Prune") {
					break
				}
				select {
				case <-tick.C:
				case <-ctx.Done():
					t.Fatal("pruner never reached transaction boundary", ctx.Err())
				}
			}
			if err := producer.PrepareContinuations(ctx); err != nil {
				t.Fatal(err)
			}
			createPruneFixture(t, producer, kind, "named", true)
			pruner.sqliteWriteMu.Unlock()
			locked = false
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if err := pruneFixtureError(ctx, producer, kind, "named"); err != nil {
				t.Fatal("pruned named recovery authority", err)
			}
			if outcomes, err := producer.ContinuationOutcomes(ctx, "named"); err != nil || len(outcomes) != 2 {
				t.Fatal("named obligations changed", outcomes, err)
			}
			if err := pruneFixtureError(ctx, producer, kind, "legacy"); !errors.Is(err, ErrNotFound) {
				t.Fatal("legacy candidate was not pruned", err)
			}
		})
	}
}

// TestSQLPruneAdvancesPastRetainedNamedPage prevents a full retained page from stopping later legacy pruning.
func TestSQLPruneAdvancesPastRetainedNamedPage(t *testing.T) {
	for _, kind := range []string{"chain", "batch"} {
		t.Run(kind, func(t *testing.T) {
			store := newSQLiteStore(t).(*sqlStore)
			store.db.SetMaxOpenConns(1)
			if err := store.PrepareContinuations(t.Context()); err != nil {
				t.Fatal(err)
			}
			for i := range 256 {
				createPruneFixture(t, store, kind, fmt.Sprintf("named-%03d", i), true)
			}
			createPruneFixture(t, store, kind, "z-legacy", false)
			if err := store.Prune(t.Context(), time.Now().Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
			if err := pruneFixtureError(t.Context(), store, kind, "z-legacy"); !errors.Is(err, ErrNotFound) {
				t.Fatal("legacy workflow beyond retained page survived pruning", err)
			}
			for i := range 256 {
				id := fmt.Sprintf("named-%03d", i)
				if err := pruneFixtureError(t.Context(), store, kind, id); err != nil {
					t.Fatal("retained named authority changed", id, err)
				}
			}
		})
	}
}

// TestSQLManagedPruneWithoutContinuations preserves ordinary pruning without creating additive schema.
func TestSQLManagedPruneWithoutContinuations(t *testing.T) {
	base := newSQLiteStore(t).(*sqlStore)
	createPruneFixture(t, base, "chain", "legacy-chain", false)
	createPruneFixture(t, base, "batch", "legacy-batch", false)
	managed := &sqlStore{db: base.db, driverName: "sqlite", autoMigrate: false}
	managed.db.SetMaxOpenConns(1)
	if err := managed.Prune(t.Context(), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"chain", "batch"} {
		if err := pruneFixtureError(t.Context(), managed, kind, "legacy-"+kind); !errors.Is(err, ErrNotFound) {
			t.Fatal("managed legacy workflow survived pruning", kind, err)
		}
	}
	var count int
	if err := managed.db.QueryRowContext(t.Context(), managed.continuationTableQuery()).Scan(&count); err != nil || count != 0 {
		t.Fatal("managed pruning created continuation schema", count, err)
	}
}

// TestSQLPruneRollbackRetainsParentAndMembers prevents partial pruning from destroying recovery evidence on a later delete failure.
func TestSQLPruneRollbackRetainsParentAndMembers(t *testing.T) {
	for _, kind := range []string{"chain", "batch"} {
		t.Run(kind, func(t *testing.T) {
			store := newSQLiteStore(t).(*sqlStore)
			createPruneFixture(t, store, kind, "legacy", false)
			parent, children, column := "bus_chains", "bus_chain_completed_nodes", "chain_id"
			if kind == "batch" {
				parent, children, column = "bus_batches", "bus_batch_jobs", "batch_id"
			}
			if _, err := store.db.ExecContext(t.Context(), `CREATE TRIGGER fail_prune BEFORE DELETE ON `+parent+` BEGIN SELECT RAISE(ABORT,'prune transaction fault'); END`); err != nil {
				t.Fatal(err)
			}
			if err := store.Prune(t.Context(), time.Now().Add(time.Hour)); err == nil {
				t.Fatal("parent deletion failure was hidden")
			}
			if err := pruneFixtureError(t.Context(), store, kind, "legacy"); err != nil {
				t.Fatal("failed pruning removed parent", err)
			}
			var count int
			if err := store.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM `+children+` WHERE `+column+`='legacy'`).Scan(&count); err != nil || count != 1 {
				t.Fatal("failed pruning removed member recovery state", count, err)
			}
			if _, err := store.db.ExecContext(t.Context(), `DROP TRIGGER fail_prune`); err != nil {
				t.Fatal(err)
			}
			if err := store.Prune(t.Context(), time.Now().Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
			if err := pruneFixtureError(t.Context(), store, kind, "legacy"); !errors.Is(err, ErrNotFound) {
				t.Fatal("pruning could not recover after rollback", err)
			}
		})
	}
}
