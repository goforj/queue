package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/goforj/queue/busruntime"
	"reflect"
	"strings"
	"testing"
	"time"
)

// continuationStores exercises the same atomic contract for memory and SQL authority.
func continuationStores(t *testing.T) map[string]Store {
	t.Helper()
	return map[string]Store{"memory": NewMemoryStore(), "sqlite": newSQLiteStore(t)}
}

// namedChainRecord describes a terminal business transition with two ordered obligations.
func namedChainRecord(id string) ChainRecord {
	return ChainRecord{ChainID: id, DispatchID: "dispatch", Nodes: []ChainNode{{NodeID: "member", Job: StoredJob{Type: "business"}}}, CreatedAt: time.Now(), continuation: &continuationPlan{Resource: "resource", Definitions: []ContinuationDefinition{{Phase: "success", Job: StoredJob{Type: "first", Payload: []byte("first bytes")}}, {Phase: "completion", Job: StoredJob{Type: "second"}}}}}
}

// TestContinuationStoreAtomicOrderAndFencing verifies deduplication, snapshots and stale-owner rejection.
func TestContinuationStoreAtomicOrderAndFencing(t *testing.T) {
	for name, store := range continuationStores(t) {
		t.Run(name, func(t *testing.T) {
			capable := store.(ContinuationStore)
			ctx := t.Context()
			if err := capable.PrepareContinuations(ctx); err != nil {
				t.Fatal(err)
			}
			if err := store.CreateChain(ctx, namedChainRecord("parent")); err != nil {
				t.Fatal(err)
			}
			if _, _, err := store.AdvanceChain(ctx, "parent", "member"); err != nil {
				t.Fatal(err)
			}
			if _, _, err := store.AdvanceChain(ctx, "parent", "member"); err != nil {
				t.Fatal(err)
			}
			outcomes, err := capable.ContinuationOutcomes(ctx, "parent")
			if err != nil || len(outcomes) != 2 {
				t.Fatalf("outcomes %+v %v", outcomes, err)
			}
			if outcomes[0].Invocation.Phase != "success" || outcomes[1].Invocation.Phase != "completion" || outcomes[0].Invocation.Trigger.Processed != 1 {
				t.Fatalf("trigger order %+v", outcomes)
			}
			now := time.Now()
			first, ok, err := capable.ClaimContinuation(ctx, "resource", "owner-one", now, time.Second)
			if err != nil || !ok {
				t.Fatalf("claim %v %v", ok, err)
			}
			if _, ok, err := capable.ClaimContinuation(ctx, "resource", "owner-two", now, time.Second); err != nil || ok {
				t.Fatalf("overlapping claim %v %v", ok, err)
			}
			forged := first
			forged.Intent.Job.Payload = []byte("forged")
			if valid, err := capable.ValidateContinuation(ctx, forged); err != nil || valid {
				t.Fatalf("forged association accepted %v %v", valid, err)
			}
			recovered, ok, err := capable.ClaimContinuation(ctx, "resource", "owner-two", now.Add(2*time.Second), time.Second)
			if err != nil || !ok || recovered.InvocationID != first.InvocationID || recovered.Generation <= first.Generation {
				t.Fatalf("recovery %+v %v %v", recovered, ok, err)
			}
			if err := capable.SettleContinuation(ctx, first, "succeeded", now); err == nil {
				t.Fatal("stale owner settled")
			}
			if err := capable.SettleContinuation(ctx, recovered, "exhausted", now); err != nil {
				t.Fatal(err)
			}

			if err := capable.SettleContinuation(ctx, recovered, "exhausted", now); err != nil {
				t.Fatal("winning settlement retry rejected", err)
			}
			if err := capable.SettleContinuation(ctx, first, "exhausted", now); err == nil {
				t.Fatal("stale settlement retry accepted")
			}
			forgedSettlement := recovered
			forgedSettlement.Resource = "other"
			if err := capable.SettleContinuation(ctx, forgedSettlement, "exhausted", now); err == nil {
				t.Fatal("forged settlement receipt accepted")
			}
			next, ok, err := capable.ClaimContinuation(ctx, "resource", "owner-two", now.Add(2*time.Second), time.Second)
			if err != nil || !ok || next.InvocationID == first.InvocationID || next.Intent.Job.Type != "second" {
				t.Fatalf("successor %+v %v %v", next, ok, err)
			}
			if err := capable.SettleContinuation(ctx, next, "succeeded", now); err != nil {
				t.Fatal(err)
			}
			if err := store.Prune(ctx, now.Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
			if _, err := store.GetChain(ctx, "parent"); err != nil {
				t.Fatal("pruned named recovery authority", err)
			}
		})
	}
}

// TestSQLContinuationTransitionRollback proves a ledger write failure cannot commit business success.
func TestSQLContinuationTransitionRollback(t *testing.T) {
	store := newSQLiteStore(t).(*sqlStore)
	ctx := t.Context()
	if err := store.PrepareContinuations(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateChain(ctx, namedChainRecord("parent")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `CREATE TRIGGER fail_ledger BEFORE UPDATE ON bus_continuations_v1 BEGIN SELECT RAISE(ABORT,'ledger unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.AdvanceChain(ctx, "parent", "member"); err == nil {
		t.Fatal("faulted transition committed")
	}
	state, err := store.GetChain(ctx, "parent")
	if err != nil || state.Completed || state.NextIndex != 0 {
		t.Fatalf("partial parent state %+v %v", state, err)
	}
	outcomes, err := store.ContinuationOutcomes(ctx, "parent")
	if err != nil || len(outcomes) != 0 {
		t.Fatalf("partial intents %+v %v", outcomes, err)
	}
	if _, err := store.db.ExecContext(ctx, `DROP TRIGGER fail_ledger`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.AdvanceChain(ctx, "parent", "member"); err != nil {
		t.Fatal(err)
	}
	// A fresh store instance discovers and recovers committed work without its original delivery.
	recovered := &sqlStore{db: store.db, driverName: "sqlite", autoMigrate: false}
	claim, ok, err := recovered.ClaimContinuation(ctx, "resource", "restarted", time.Now(), time.Second)
	if err != nil || !ok || claim.Intent.Job.Type != "first" {
		t.Fatalf("restart recovery %+v %v %v", claim, ok, err)
	}
}

// TestSQLContinuationManagedSchemaDoesNoDDL preserves legacy schema use and rejects named acceptance.
func TestSQLContinuationManagedSchemaDoesNoDDL(t *testing.T) {
	base := newSQLiteStore(t).(*sqlStore)
	ctx := t.Context()
	if err := base.ensureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	managed := &sqlStore{db: base.db, driverName: "sqlite", autoMigrate: false}
	var before, after int
	if err := base.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := managed.PrepareContinuations(ctx); err == nil {
		t.Fatal("missing managed continuation schema accepted")
	}
	if err := base.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("managed mode performed DDL")
	}
	record := namedChainRecord("legacy")
	record.continuation = nil
	if err := managed.CreateChain(ctx, record); err != nil {
		t.Fatal(err)
	}
	if _, _, err := managed.AdvanceChain(ctx, "legacy", "member"); err != nil {
		t.Fatal(err)
	}
}

// TestSQLContinuationInitialWriteRollback proves definitions and parent acceptance are atomic.
func TestSQLContinuationInitialWriteRollback(t *testing.T) {
	store := newSQLiteStore(t).(*sqlStore)
	ctx := t.Context()
	if err := store.PrepareContinuations(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `CREATE TRIGGER fail_intent BEFORE INSERT ON bus_continuations_v1 BEGIN SELECT RAISE(ABORT,'intent unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateChain(ctx, namedChainRecord("parent")); err == nil {
		t.Fatal("accepted parent without definitions")
	}
	if _, err := store.GetChain(ctx, "parent"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

// TestContinuationSummaryContainsNoBusinessPayload checks the safe exported summary remains payload-free.
func TestContinuationSummaryContainsNoBusinessPayload(t *testing.T) {
	for _, field := range reflect.VisibleFields(reflect.TypeFor[busruntime.ContinuationSummary]()) {
		if field.Type == reflect.TypeFor[[]byte]() {
			t.Fatal("summary exposes payload bytes")
		}
	}
}

// TestContinuationExecutionSurvivesPublicationExpiry preserves identity and logical retry budgets.
func TestContinuationExecutionSurvivesPublicationExpiry(t *testing.T) {
	for name, store := range continuationStores(t) {
		t.Run(name, func(t *testing.T) {
			capable := store.(ContinuationStore)
			ctx := t.Context()
			if err := capable.PrepareContinuations(ctx); err != nil {
				t.Fatal(err)
			}
			record := namedChainRecord("parent")
			record.continuation.Definitions[0].Job.Options.Retry = 1
			record.continuation.Definitions[0].Job.Options.Backoff = 2 * time.Minute
			if err := store.CreateChain(ctx, record); err != nil {
				t.Fatal(err)
			}
			if _, _, err := store.AdvanceChain(ctx, "parent", "member"); err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			old, ok, err := capable.ClaimContinuation(ctx, "resource", "publisher-one", now, time.Minute)
			if err != nil || !ok {
				t.Fatal(ok, err)
			}
			_, ok, err = capable.ClaimContinuation(ctx, "resource", "publisher-two", now.Add(10*time.Minute), time.Minute)
			if err != nil || !ok {
				t.Fatal(ok, err)
			}
			execution, ok, err := capable.BeginContinuation(ctx, old, "worker-one", now.Add(11*time.Minute))
			if err != nil || !ok || execution.Attempt != 0 {
				t.Fatalf("late publication %+v %v %v", execution, ok, err)
			}
			rejoined, joined, joinErr := capable.BeginContinuation(ctx, old, "worker-one", now.Add(11*time.Minute))
			if joinErr != nil || !joined || rejoined.Generation != execution.Generation || rejoined.Attempt != execution.Attempt {
				t.Fatalf("ambiguous begin changed attempt %+v %v", rejoined, joinErr)
			}
			if _, ok, err := capable.BeginContinuation(ctx, old, "duplicate", now.Add(11*time.Minute)); err != nil || ok {
				t.Fatal("overlapping execution", ok, err)
			}
			if err := capable.SettleContinuation(ctx, execution, "retry", now.Add(11*time.Minute)); err != nil {
				t.Fatal(err)
			}
			if err := capable.SettleContinuation(ctx, execution, "retry", now.Add(11*time.Minute)); err != nil {
				t.Fatal("ambiguous settlement was not idempotent", err)
			}
			if _, ok, err := capable.BeginContinuation(ctx, old, "too-early", now.Add(12*time.Minute)); err != nil || ok {
				t.Fatal("backoff ignored", ok, err)
			}
			second, ok, err := capable.BeginContinuation(ctx, old, "worker-two", now.Add(14*time.Minute))
			if err != nil || !ok || second.Attempt != 1 {
				t.Fatalf("retry budget reset %+v %v %v", second, ok, err)
			}
			if err := capable.SettleContinuation(ctx, execution, "succeeded", now); err == nil {
				t.Fatal("stale execution settled newer attempt")
			}
			if err := capable.SettleContinuation(ctx, second, "retry", now.Add(14*time.Minute)); err != nil {
				t.Fatal(err)
			}
			if _, ok, err := capable.BeginContinuation(ctx, old, "third", now.Add(20*time.Minute)); err != nil || ok {
				t.Fatal("exhausted attempt ran again", ok, err)
			}
			outcomes, err := capable.ContinuationOutcomes(ctx, "parent")
			if err != nil || outcomes[0].Status != "exhausted" {
				t.Fatalf("outcomes %+v %v", outcomes, err)
			}
		})
	}
}

// TestContinuationCancellationTriggersCompletionOnce covers cancellation racing late member settlement.
func TestContinuationCancellationTriggersCompletionOnce(t *testing.T) {
	for name, store := range continuationStores(t) {
		t.Run(name, func(t *testing.T) {
			capable := store.(ContinuationStore)
			ctx := t.Context()
			if err := capable.PrepareContinuations(ctx); err != nil {
				t.Fatal(err)
			}
			record := BatchRecord{BatchID: "batch", DispatchID: "dispatch", Jobs: []BatchJob{{JobID: "member", Job: StoredJob{Type: "business"}}}, continuation: &continuationPlan{Resource: "resource", Definitions: []ContinuationDefinition{{Phase: "completion", Job: StoredJob{Type: "completion"}}, {Phase: "progress", Job: StoredJob{Type: "progress"}}}}}
			if err := store.CreateBatch(ctx, record); err != nil {
				t.Fatal(err)
			}
			if err := store.CancelBatch(ctx, "batch"); err != nil {
				t.Fatal(err)
			}
			if err := store.CancelBatch(ctx, "batch"); err != nil {
				t.Fatal(err)
			}
			if _, _, err := store.MarkBatchJobSucceeded(ctx, "batch", "member"); err != nil {
				t.Fatal(err)
			}
			outcomes, err := capable.ContinuationOutcomes(ctx, "batch")
			if err != nil || len(outcomes) != 2 || outcomes[0].Invocation.Phase != "completion" || outcomes[1].Invocation.Phase != "progress" {
				t.Fatalf("outcomes %+v %v", outcomes, err)
			}
		})
	}
}

// TestContinuationAuthorityRetryPreservesEffect retries only store work and respects permanent failures and cancellation.
func TestContinuationAuthorityRetryPreservesEffect(t *testing.T) {
	calls := 0
	if err := retryContinuationStore(t.Context(), func(context.Context) error {
		calls++
		if calls < 3 {
			return errors.New("transient")
		}
		return nil
	}); err != nil || calls != 3 {
		t.Fatalf("retry calls=%d error=%v", calls, err)
	}
	calls = 0
	if err := retryContinuationStore(t.Context(), func(context.Context) error { calls++; return ErrContinuationProtocol }); !errors.Is(err, ErrContinuationProtocol) || calls != 1 {
		t.Fatalf("protocol retried calls=%d error=%v", calls, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := retryContinuationStore(ctx, func(context.Context) error { return errors.New("transient") }); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation ignored %v", err)
	}
}

// oversizedContinuationInput exposes whether oversized broker input reaches JSON decoding.
type oversizedContinuationInput struct{ decoded bool }

// PayloadBytes reports a body beyond the maximum valid named claim envelope.
func (*oversizedContinuationInput) PayloadBytes() []byte { return make([]byte, (256<<10)+1) }

// Bind records decoder entry so preflight must reject before parsing application-selected bytes.
func (i *oversizedContinuationInput) Bind(any) error { i.decoded = true; return nil }

// TestContinuationDeliveryPreflightRejectsBeforeDecode bounds untrusted internal delivery envelopes before binding.
func TestContinuationDeliveryPreflightRejectsBeforeDecode(t *testing.T) {
	input := &oversizedContinuationInput{}
	r := &runtime{}
	if err := r.handleContinuation(t.Context(), input); !busruntime.IsPermanent(err) || input.decoded {
		t.Fatalf("oversized continuation reached decoder: decoded=%t error=%v", input.decoded, err)
	}
}

// TestAcceptedContinuationCatalogFitsDeliveryBudget proves JSON escaping and base64 expansion fit the decoder ceiling.
func TestAcceptedContinuationCatalogFitsDeliveryBudget(t *testing.T) {
	definitions := cloneDefinitions([]ContinuationDefinition{{Phase: "success", Job: StoredJob{Type: strings.Repeat("\x01", 100), Payload: bytes.Repeat([]byte("x"), 90<<10), Options: JobOptions{Queue: strings.Repeat("\x02", 100)}}}})
	if err := validateDefinitions("chain", definitions); err != nil {
		t.Fatal(err)
	}
	ledger := newLedger("chain", "chn_0123456789abcdef", "dsp_0123456789abcdef", &continuationPlan{Resource: strings.Repeat("\x03", 255), Definitions: definitions})
	ledger.trigger(busruntime.ContinuationSummary{Kind: "chain", MemberID: "job_0123456789abcdef", Total: 256, Processed: 256, Completed: true}, false, true, time.Now())
	claim, ok := ledger.claim("publisher_0123456789abcdef", time.Now(), time.Minute)
	if !ok {
		t.Fatal("no valid claim")
	}
	encoded, err := json.Marshal(claim)
	if err != nil || len(encoded) > 256<<10 {
		t.Fatalf("accepted catalog exceeds delivery ceiling: %d %v", len(encoded), err)
	}
}
