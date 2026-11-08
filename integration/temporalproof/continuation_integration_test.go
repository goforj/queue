//go:build integration

package temporalproof

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/history/v1"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

const proofLedgerKey = "queue-proof-ledger"

type proofSnapshot struct {
	Processed int  `json:"processed"`
	Completed bool `json:"completed"`
}

type proofIntent struct {
	ID       string        `json:"id"`
	Snapshot proofSnapshot `json:"snapshot"`
}

type proofLedgerDelta struct {
	Sequence int         `json:"sequence"`
	Kind     string      `json:"kind"`
	Intent   proofIntent `json:"intent"`
}

// continuationProofWorkflow models serial continuation drain while admitted
// business activities keep settling, without freezing a production protocol.
func continuationProofWorkflow(ctx workflow.Context) error {
	business := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 10 * time.Second, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 1}})
	first := workflow.ExecuteActivity(business, "proof-business", "first")
	second := workflow.ExecuteActivity(business, "proof-business", "second")
	var intents []proofIntent
	sequence := 0
	businessDone, drained := false, false
	emit := func(kind string, intent proofIntent) error {
		sequence++
		return workflow.UpsertMemo(ctx, map[string]any{proofLedgerKey: proofLedgerDelta{Sequence: sequence, Kind: kind, Intent: intent}})
	}
	var runnerErr error
	workflow.Go(ctx, func(ctx workflow.Context) {
		defer func() { drained = true }()
		for head := 0; ; head++ {
			if err := workflow.Await(ctx, func() bool { return head < len(intents) || businessDone }); err != nil {
				runnerErr = err
				return
			}
			if head == len(intents) {
				return
			}
			intent := intents[head]
			if err := emit("scheduled", intent); err != nil {
				runnerErr = err
				return
			}
			activityCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{ActivityID: "continuation/" + intent.ID, StartToCloseTimeout: 10 * time.Second, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 1}})
			err := workflow.ExecuteActivity(activityCtx, "proof-continuation", intent).Get(activityCtx, nil)
			kind := "succeeded"
			if err != nil {
				kind = "exhausted"
			}
			if err := emit(kind, intent); err != nil {
				runnerErr = err
				return
			}
		}
	})
	for index, future := range []workflow.Future{first, second} {
		if err := future.Get(ctx, nil); err != nil {
			return err
		}
		snapshot := proofSnapshot{Processed: index + 1, Completed: index == 1}
		if err := emit("business-settled", proofIntent{ID: fmt.Sprintf("member-%d", index), Snapshot: snapshot}); err != nil {
			return err
		}
		intent := proofIntent{ID: fmt.Sprintf("progress-%d", index), Snapshot: snapshot}
		intents = append(intents, intent)
		if err := emit("pending", intent); err != nil {
			return err
		}
		if snapshot.Completed {
			completion := proofIntent{ID: "completion", Snapshot: snapshot}
			intents = append(intents, completion)
			if err := emit("pending", completion); err != nil {
				return err
			}
		}
	}
	businessDone = true
	if err := workflow.Await(ctx, func() bool { return drained }); err != nil {
		return err
	}
	return runnerErr
}

// decodeLedgerDelta ignores unrelated event types but never ignores bad ledger data.
func decodeLedgerDelta(event *history.HistoryEvent) (proofLedgerDelta, bool, error) {
	attributes := event.GetWorkflowPropertiesModifiedEventAttributes()
	if attributes == nil {
		return proofLedgerDelta{}, false, nil
	}
	payload, ok := attributes.UpsertedMemo.Fields[proofLedgerKey]
	if !ok {
		return proofLedgerDelta{}, false, nil
	}
	var delta proofLedgerDelta
	err := converter.GetDefaultDataConverter().FromPayload(payload, &delta)
	return delta, true, err
}

// validateContinuationSchedules rejects overlapping heads, duplicate commands,
// and incomplete drain even when execution and outcome slices retain their expected order.
func validateContinuationSchedules(recorded *history.History, expected []proofIntent) error {
	scheduled := make(map[string]int64)
	activities := make(map[string]bool)
	active := ""
	next := 0
	for _, event := range recorded.Events {
		delta, ok, err := decodeLedgerDelta(event)
		if err != nil {
			return fmt.Errorf("decode continuation ledger: %w", err)
		}
		if ok {
			id := delta.Intent.ID
			switch delta.Kind {
			case "scheduled":
				if _, duplicate := scheduled[id]; duplicate {
					return fmt.Errorf("duplicate continuation schedule for %q", id)
				}
				if active != "" {
					return fmt.Errorf("continuation %q scheduled before %q settled", id, active)
				}
				if next >= len(expected) || delta.Intent != expected[next] {
					return fmt.Errorf("unexpected continuation head %q", id)
				}
				commit := event.GetWorkflowPropertiesModifiedEventAttributes().WorkflowTaskCompletedEventId
				if commit == 0 {
					return fmt.Errorf("continuation %q has no workflow-task commit", id)
				}
				scheduled[id] = commit
				active = id
			case "succeeded", "exhausted":
				if active == "" || id != active || delta.Intent != expected[next] {
					return fmt.Errorf("continuation %q settled without its active head", id)
				}
				if !activities[id] {
					return fmt.Errorf("continuation %q settled before its activity schedule", id)
				}
				active = ""
				next++
			}
		}
		if attributes := event.GetActivityTaskScheduledEventAttributes(); attributes != nil && strings.HasPrefix(attributes.ActivityId, "continuation/") {
			id := strings.TrimPrefix(attributes.ActivityId, "continuation/")
			if active == "" || id != active {
				return fmt.Errorf("continuation activity %q scheduled without its active head", id)
			}
			if activities[id] {
				return fmt.Errorf("duplicate continuation activity for %q", id)
			}
			if scheduled[id] != attributes.WorkflowTaskCompletedEventId {
				return fmt.Errorf("continuation %q activity uses a different workflow-task commit", id)
			}
			activities[id] = true
		}
	}
	if active != "" {
		return fmt.Errorf("continuation %q remains unsettled", active)
	}
	if next != len(expected) || len(scheduled) != len(expected) || len(activities) != len(expected) {
		return fmt.Errorf("missing continuation schedules or settlements: got %d, want %d", next, len(expected))
	}
	return nil
}

// awaitBusinessTerminal observes an actual committed journal record, using the
// SDK's long-poll history iterator rather than sleeps or worker-side markers.
func awaitBusinessTerminal(ctx context.Context, c client.Client, id, runID string) error {
	iterator := c.GetWorkflowHistory(ctx, id, runID, true, enums.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
	for events := 0; events < 128 && iterator.HasNext(); events++ {
		event, err := iterator.Next()
		if err != nil {
			return err
		}
		delta, ok, err := decodeLedgerDelta(event)
		if err != nil {
			return err
		}
		if ok && delta.Kind == "business-settled" && delta.Intent.Snapshot.Completed {
			return nil
		}
	}
	return errors.New("business-terminal proof record not found within bounded history")
}

// TestTemporalProofContinuationLedger proves pending/scheduled commands commit
// atomically, trigger snapshots stay immutable, and an exhausted head does not
// block later completion work or business-result collection.
func TestTemporalProofContinuationLedger(t *testing.T) {
	requireProofServer(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	producer := newProofClient(t, client.Options{})
	poller := newProofClient(t, client.Options{})
	reader := newProofClient(t, client.Options{})
	secondRelease := make(chan struct{})
	continuationRelease := make(chan struct{})
	firstContinuationStarted := make(chan struct{})
	var secondOnce, continuationOnce sync.Once
	defer secondOnce.Do(func() { close(secondRelease) })
	defer continuationOnce.Do(func() { close(continuationRelease) })
	var lock sync.Mutex
	var executions []proofIntent
	id := fmt.Sprintf("queue-continuation-proof-%d", proofSequence.Add(1))
	w := worker.New(poller, id, worker.Options{})
	w.RegisterWorkflow(continuationProofWorkflow)
	w.RegisterActivityWithOptions(func(ctx context.Context, member string) error {
		if member == "second" {
			select {
			case <-secondRelease:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}, activity.RegisterOptions{Name: "proof-business"})
	w.RegisterActivityWithOptions(func(ctx context.Context, intent proofIntent) error {
		lock.Lock()
		executions = append(executions, intent)
		lock.Unlock()
		if intent.ID == "progress-0" {
			close(firstContinuationStarted)
			select {
			case <-continuationRelease:
				return errors.New("synthetic terminal continuation failure")
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}, activity.RegisterOptions{Name: "proof-continuation"})
	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	defer w.Stop()
	run, err := producer.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: id, TaskQueue: id}, continuationProofWorkflow)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-firstContinuationStarted:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	secondOnce.Do(func() { close(secondRelease) })
	if err := awaitBusinessTerminal(ctx, reader, id, run.GetRunID()); err != nil {
		t.Fatal(err)
	}
	intermediate, _, err := readProofHistory(ctx, reader, id, run.GetRunID())
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range intermediate.Events {
		delta, ok, err := decodeLedgerDelta(event)
		if err != nil {
			t.Fatal(err)
		}
		if ok && delta.Kind == "scheduled" && delta.Intent.ID != "progress-0" {
			t.Fatal("later ledger head scheduled before predecessor settlement")
		}
	}
	continuationOnce.Do(func() { close(continuationRelease) })
	if err := run.Get(ctx, nil); err != nil {
		t.Fatal(err)
	}
	retained, _, err := readProofHistory(ctx, reader, id, run.GetRunID())
	if err != nil {
		t.Fatal(err)
	}
	var outcomes []string
	var pending []proofIntent
	sequence := 0
	lastBusinessCommit := int64(0)
	for _, event := range retained.Events {
		delta, ok, err := decodeLedgerDelta(event)
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			sequence++
			if delta.Sequence != sequence {
				t.Fatalf("ledger sequence=%d, want %d", delta.Sequence, sequence)
			}
			commit := event.GetWorkflowPropertiesModifiedEventAttributes().WorkflowTaskCompletedEventId
			switch delta.Kind {
			case "business-settled":
				lastBusinessCommit = commit
			case "pending":
				if commit != lastBusinessCommit {
					t.Fatal("trigger and pending intent did not commit atomically")
				}
				pending = append(pending, delta.Intent)
			case "succeeded", "exhausted":
				outcomes = append(outcomes, delta.Intent.ID+":"+delta.Kind)
			}
		}
	}
	wantIntents := []proofIntent{{"progress-0", proofSnapshot{1, false}}, {"progress-1", proofSnapshot{2, true}}, {"completion", proofSnapshot{2, true}}}
	if err := validateContinuationSchedules(retained, wantIntents); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(pending, wantIntents) {
		t.Fatalf("immutable trigger snapshots=%+v", pending)
	}
	if !reflect.DeepEqual(outcomes, []string{"progress-0:exhausted", "progress-1:succeeded", "completion:succeeded"}) {
		t.Fatalf("ledger outcomes=%v", outcomes)
	}
	lock.Lock()
	defer lock.Unlock()
	if !reflect.DeepEqual(executions, wantIntents) {
		t.Fatalf("continuation executions=%+v", executions)
	}
	t.Log("business reached terminal state while first continuation was blocked; immutable intents committed with their triggers; only eligible head scheduled; exhaustion recorded before later completion succeeded")
}
