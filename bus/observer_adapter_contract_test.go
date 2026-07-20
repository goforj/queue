package bus

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/goforj/queue/internal/workflow"
)

// TestLegacyObserverAdapterTranslatesEveryField verifies the deprecated bus
// observer shape remains an exact compatibility projection of engine facts.
func TestLegacyObserverAdapterTranslatesEveryField(t *testing.T) {
	assertWorkflowEventShape(t)

	type contextKey struct{}
	ctx := context.WithValue(context.Background(), contextKey{}, "legacy-observer-context")
	wantErr := errors.New("chain failed")
	wantTime := time.Date(2026, time.July, 20, 14, 0, 0, 0, time.UTC)
	internalEvent := workflow.Event{
		SchemaVersion: 9,
		EventID:       "evt_legacy_adapter",
		Kind:          workflow.EventChainFailed,
		DispatchID:    "dsp_legacy_adapter",
		JobID:         "job_legacy_adapter",
		ChainID:       "chn_legacy_adapter",
		BatchID:       "bat_legacy_adapter",
		Attempt:       4,
		JobType:       "reports:archive",
		JobKey:        "job-key-legacy-adapter",
		Queue:         "critical",
		Duration:      73 * time.Millisecond,
		Time:          wantTime,
		Err:           wantErr,
	}

	var (
		gotContext context.Context
		gotEvent   Event
	)
	adapter := legacyObserverAdapter{observer: ObserverFunc(func(observedContext context.Context, event Event) {
		gotContext = observedContext
		gotEvent = event
	})}
	adapter.Observe(ctx, internalEvent)

	wantEvent := Event{
		SchemaVersion: internalEvent.SchemaVersion,
		EventID:       internalEvent.EventID,
		Kind:          EventChainFailed,
		DispatchID:    internalEvent.DispatchID,
		JobID:         internalEvent.JobID,
		ChainID:       internalEvent.ChainID,
		BatchID:       internalEvent.BatchID,
		Attempt:       internalEvent.Attempt,
		JobType:       internalEvent.JobType,
		JobKey:        internalEvent.JobKey,
		Queue:         internalEvent.Queue,
		Duration:      internalEvent.Duration,
		Time:          internalEvent.Time,
		Err:           internalEvent.Err,
	}
	if !reflect.DeepEqual(gotEvent, wantEvent) {
		t.Fatalf("translated legacy event = %+v, want %+v", gotEvent, wantEvent)
	}
	if gotContext == nil || gotContext.Value(contextKey{}) != "legacy-observer-context" {
		t.Fatalf("legacy observer context = %v, want original workflow context", gotContext)
	}
}

// assertWorkflowEventShape pins the internal adapter input so adding a field
// requires an explicit decision about its legacy projection.
func assertWorkflowEventShape(t *testing.T) {
	t.Helper()
	wantFields := []string{
		"SchemaVersion",
		"EventID",
		"Kind",
		"DispatchID",
		"JobID",
		"ChainID",
		"BatchID",
		"Attempt",
		"JobType",
		"JobKey",
		"Queue",
		"Duration",
		"Time",
		"Err",
	}
	eventType := reflect.TypeOf(workflow.Event{})
	if eventType.NumField() != len(wantFields) {
		t.Fatalf("workflow.Event fields = %d, want %d; update the legacy observer adapter contract", eventType.NumField(), len(wantFields))
	}
	for index, wantField := range wantFields {
		if gotField := eventType.Field(index).Name; gotField != wantField {
			t.Fatalf("workflow.Event field %d = %s, want %s; update the legacy observer adapter contract", index, gotField, wantField)
		}
	}
}
