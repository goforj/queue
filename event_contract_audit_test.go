package queue

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/goforj/queue/internal/workflow"
)

// TestInternalWorkflowKindsMapToPublicCatalog prevents the private engine from
// casting an undocumented kind or an incorrectly layered fact into queue.Event.
func TestInternalWorkflowKindsMapToPublicCatalog(t *testing.T) {
	t.Parallel()

	root := eventReferenceRoot(t)
	publicDefinitions := parsePackageEventKindDefinitions(t, root)
	publicByKind := make(map[EventKind]string, len(publicDefinitions))
	for _, definition := range publicDefinitions {
		publicByKind[definition.kind] = definition.name
	}

	workflowDefinitions := parsePackageEventKindDefinitions(t, filepath.Join(root, "internal", "workflow"))
	for _, definition := range workflowDefinitions {
		publicName, ok := publicByKind[definition.kind]
		if !ok {
			t.Errorf("internal workflow kind %s (%q) has no public EventKind", definition.name, definition.kind)
			continue
		}
		if publicName != definition.name {
			t.Errorf("internal workflow kind %s (%q) maps to public identifier %s", definition.name, definition.kind, publicName)
		}
		wantLayer := EventLayerWorkflow
		switch definition.kind {
		case EventDispatchStarted, EventDispatchSucceeded, EventDispatchFailed:
			wantLayer = EventLayerQueue
		}
		if gotLayer := eventLayerForKind(definition.kind); gotLayer != wantLayer {
			t.Errorf("internal workflow kind %s (%q) maps to layer %q, want %q", definition.name, definition.kind, gotLayer, wantLayer)
		}
	}
}

// TestWorkflowObserverAdapterTranslatesEveryField verifies the internal engine
// cannot lose correlation or application identity at the unified public boundary.
func TestWorkflowObserverAdapterTranslatesEveryField(t *testing.T) {
	assertWorkflowEventShape(t)

	type contextKey struct{}
	ctx := context.WithValue(context.Background(), contextKey{}, "observer-context")
	wantErr := errors.New("batch failed")
	wantTime := time.Date(2026, time.July, 20, 12, 30, 0, 0, time.UTC)
	internalEvent := workflow.Event{
		SchemaVersion: 7,
		EventID:       "evt_adapter_contract",
		Kind:          workflow.EventBatchFailed,
		DispatchID:    "dsp_adapter_contract",
		JobID:         "job_adapter_contract",
		ChainID:       "chn_adapter_contract",
		BatchID:       "bat_adapter_contract",
		Attempt:       3,
		JobType:       "reports:build",
		JobKey:        "job-key-adapter-contract",
		Queue:         "critical",
		Duration:      42 * time.Millisecond,
		Time:          wantTime,
		Err:           wantErr,
	}

	var (
		gotContext context.Context
		gotEvent   Event
	)
	adapter := workflowObserverAdapter{
		driver: DriverSQS,
		resolveQueueName: func(queueName string) string {
			return "billing_" + queueName
		},
		observer: ObserverFunc(func(observedContext context.Context, event Event) {
			gotContext = observedContext
			gotEvent = event
		}),
	}
	adapter.Observe(ctx, internalEvent)

	wantEvent := Event{
		SchemaVersion: internalEvent.SchemaVersion,
		EventID:       internalEvent.EventID,
		Layer:         EventLayerWorkflow,
		Kind:          EventBatchFailed,
		Driver:        DriverSQS,
		Queue:         "billing_critical",
		JobType:       internalEvent.JobType,
		JobKey:        internalEvent.JobKey,
		DispatchID:    internalEvent.DispatchID,
		JobID:         internalEvent.JobID,
		ChainID:       internalEvent.ChainID,
		BatchID:       internalEvent.BatchID,
		Attempt:       internalEvent.Attempt,
		Duration:      internalEvent.Duration,
		Err:           internalEvent.Err,
		Time:          internalEvent.Time,
	}
	if !reflect.DeepEqual(gotEvent, wantEvent) {
		t.Fatalf("translated workflow event = %+v, want %+v", gotEvent, wantEvent)
	}
	if gotContext == nil || gotContext.Value(contextKey{}) != "observer-context" {
		t.Fatalf("observer context = %v, want original workflow context", gotContext)
	}
}

// assertWorkflowEventShape pins the internal adapter input so adding a field
// requires an explicit decision about its public projection.
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
		t.Fatalf("workflow.Event fields = %d, want %d; update the public observer adapter contract", eventType.NumField(), len(wantFields))
	}
	for index, wantField := range wantFields {
		if gotField := eventType.Field(index).Name; gotField != wantField {
			t.Fatalf("workflow.Event field %d = %s, want %s; update the public observer adapter contract", index, gotField, wantField)
		}
	}
}

// TestEventLayerForKindCoversPublicKinds pins every currently exported event
// kind to one semantic layer so future edits cannot silently conflate scopes.
func TestEventLayerForKindCoversPublicKinds(t *testing.T) {
	kindsByLayer := map[EventLayer][]EventKind{
		EventLayerQueue: {
			EventDispatchStarted,
			EventDispatchSucceeded,
			EventDispatchFailed,
			EventEnqueueAccepted,
			EventEnqueueRejected,
			EventEnqueueDuplicate,
			EventEnqueueCanceled,
			EventQueuePaused,
			EventQueueResumed,
		},
		EventLayerWorker: {
			EventProcessStarted,
			EventProcessSucceeded,
			EventProcessFailed,
			EventProcessRetried,
			EventProcessArchived,
			EventProcessRecovered,
			EventRepublishFailed,
			EventSettlementFailed,
		},
		EventLayerWorkflow: {
			EventJobStarted,
			EventJobSucceeded,
			EventJobFailed,
			EventChainStarted,
			EventChainAdvanced,
			EventChainCompleted,
			EventChainFailed,
			EventBatchStarted,
			EventBatchProgressed,
			EventBatchCompleted,
			EventBatchFailed,
			EventBatchCancelled,
			EventCallbackStarted,
			EventCallbackSucceeded,
			EventCallbackFailed,
		},
	}

	seen := make(map[EventKind]EventLayer)
	for wantLayer, kinds := range kindsByLayer {
		for _, kind := range kinds {
			if priorLayer, exists := seen[kind]; exists {
				t.Fatalf("event kind %q appears in both %q and %q", kind, priorLayer, wantLayer)
			}
			seen[kind] = wantLayer
			if gotLayer := eventLayerForKind(kind); gotLayer != wantLayer {
				t.Errorf("event kind %q layer = %q, want %q", kind, gotLayer, wantLayer)
			}
		}
	}
	if len(seen) != 32 {
		t.Fatalf("covered event kinds = %d, want 32", len(seen))
	}
	if gotLayer := eventLayerForKind(EventKind("future_queue_fact")); gotLayer != EventLayerQueue {
		t.Fatalf("unknown event layer = %q, want queue compatibility default", gotLayer)
	}
}

// TestStatsCollectorIngestsReservedProcessArchived pins compatibility for
// external drivers that already emit a confirmed terminal-settlement fact.
func TestStatsCollectorIngestsReservedProcessArchived(t *testing.T) {
	collector := NewStatsCollector()
	collector.Observe(context.Background(), Event{
		Kind:   EventProcessArchived,
		Layer:  EventLayerWorker,
		Driver: DriverDatabase,
		Queue:  "critical",
		Time:   time.Date(2026, time.July, 20, 13, 0, 0, 0, time.UTC),
	})

	counters, ok := collector.Snapshot().Queue("critical")
	if !ok {
		t.Fatal("reserved archive fact did not create queue counters")
	}
	if counters.Archived != 1 {
		t.Fatalf("archived count = %d, want 1", counters.Archived)
	}
	if counters.Pending != 0 || counters.Active != 0 || counters.Retry != 0 || counters.Processed != 0 || counters.Failed != 0 {
		t.Fatalf("archive fact mutated unrelated counters: %+v", counters)
	}
}
