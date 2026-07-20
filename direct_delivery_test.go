package queue

import (
	"bytes"
	"context"
	"testing"

	"github.com/goforj/queue/busruntime"
)

// TestQueueDirectDeliveryUsesApplicationTypeAndPayload pins the canonical
// physical boundary independently of synchronous handler execution.
func TestQueueDirectDeliveryUsesApplicationTypeAndPayload(t *testing.T) {
	inner := &queueBackendRecorder{}
	runtime := &nativeQueueRuntime{
		common: &queueCommon{
			inner:  inner,
			cfg:    Config{DefaultQueue: "billing_default"},
			driver: DriverSync,
		},
		runtime: &runtimeBackendStub{},
		nativeQueueRuntimeState: &nativeQueueRuntimeState{
			registered: make(map[string]Handler),
		},
	}
	q, err := newQueueFromRuntime(runtime)
	if err != nil {
		t.Fatalf("new queue: %v", err)
	}
	q.Register("reports:build", func(context.Context, Message) error { return nil })

	payload := []byte{0, 1, 2, '{', 0xff}
	result, err := q.Dispatch(
		NewJob("reports:build").
			Payload(payload).
			OnQueue("critical"),
	)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if len(inner.dispatched) != 1 {
		t.Fatalf("physical dispatch count = %d, want 1", len(inner.dispatched))
	}
	delivery := inner.dispatched[0]
	if delivery.Type != "reports:build" {
		t.Fatalf("physical type = %q, want reports:build", delivery.Type)
	}
	if !bytes.Equal(delivery.PayloadBytes(), payload) {
		t.Fatalf("physical payload = %v, want %v", delivery.PayloadBytes(), payload)
	}
	metadata := DriverMetadata(delivery)
	if metadata.SchemaVersion != DriverJobMetadataVersion || metadata.DispatchID != result.DispatchID || metadata.JobID == "" {
		t.Fatalf("physical metadata = %+v, receipt = %+v", metadata, result)
	}
	if metadata.Queue != "critical" {
		t.Fatalf("logical metadata queue = %q, want critical", metadata.Queue)
	}
	if got := DriverOptions(delivery).QueueName; got != "billing_critical" {
		t.Fatalf("physical queue = %q, want billing_critical", got)
	}
	if _, ok := runtime.registered["reports:build"]; !ok {
		t.Fatal("application type was not registered for direct delivery")
	}
	if _, ok := runtime.registered["bus:job"]; !ok {
		t.Fatal("legacy direct-envelope handler was not retained")
	}

	payload[0] = 99
	if bytes.Equal(delivery.PayloadBytes(), payload) {
		t.Fatal("physical delivery retained the caller payload buffer")
	}
}

// TestQueueDirectDeliveryPreservesRawHandlerPayload proves the public message
// sees the canonical bytes even when they are not JSON and cannot be bound.
func TestQueueDirectDeliveryPreservesRawHandlerPayload(t *testing.T) {
	q, err := NewSync()
	if err != nil {
		t.Fatalf("new sync queue: %v", err)
	}
	t.Cleanup(func() {
		if shutdownErr := q.Shutdown(context.Background()); shutdownErr != nil {
			t.Errorf("shutdown: %v", shutdownErr)
		}
	})

	payload := []byte{0, 1, 2, 0xff}
	var message Message
	q.Register("reports:raw", func(_ context.Context, incoming Message) error {
		message = incoming
		return nil
	})
	if err := q.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers: %v", err)
	}
	result, err := q.Dispatch(NewJob("reports:raw").Payload(payload))
	if err != nil {
		t.Fatalf("dispatch raw payload: %v", err)
	}
	if !bytes.Equal(message.PayloadBytes(), payload) {
		t.Fatalf("handler payload = %v, want %v", message.PayloadBytes(), payload)
	}
	if message.DispatchID != result.DispatchID || message.JobID == "" || message.JobType != "reports:raw" {
		t.Fatalf("handler correlation = %+v, receipt = %+v", message, result)
	}
}

// TestQueueDirectDeliveryReservedTypesUseLegacyEnvelope prevents application
// names from replacing the frozen workflow protocol handlers.
func TestQueueDirectDeliveryReservedTypesUseLegacyEnvelope(t *testing.T) {
	for _, jobType := range []string{"bus:job", "bus:chain:node", "bus:batch:job", "bus:callback"} {
		t.Run(jobType, func(t *testing.T) {
			inner := &queueBackendRecorder{}
			runtime := &nativeQueueRuntime{
				common:  &queueCommon{inner: inner, cfg: Config{DefaultQueue: "default"}, driver: DriverSync},
				runtime: &runtimeBackendStub{},
				nativeQueueRuntimeState: &nativeQueueRuntimeState{
					registered: make(map[string]Handler),
				},
			}
			q, err := newQueueFromRuntime(runtime)
			if err != nil {
				t.Fatalf("new queue: %v", err)
			}
			q.Register(jobType, func(context.Context, Message) error { return nil })

			result, err := q.Dispatch(NewJob(jobType).Payload([]byte(`{"application":true}`)))
			if err != nil {
				t.Fatalf("dispatch reserved type: %v", err)
			}
			if len(inner.dispatched) != 1 {
				t.Fatalf("physical dispatch count = %d, want 1", len(inner.dispatched))
			}
			delivery := inner.dispatched[0]
			if delivery.Type != "bus:job" {
				t.Fatalf("physical type = %q, want retained bus:job", delivery.Type)
			}
			if DriverMetadata(delivery).SchemaVersion != 0 {
				t.Fatalf("reserved delivery unexpectedly used direct metadata: %+v", DriverMetadata(delivery))
			}
			observed := ResolveObservedJobMetadata(delivery.Type, delivery.PayloadBytes())
			if observed.DispatchID != result.DispatchID || observed.JobID == "" || observed.JobType != jobType {
				t.Fatalf("legacy envelope metadata = %+v, receipt = %+v", observed, result)
			}
		})
	}
}

// TestLegacyDirectEnvelopeOptionSupportsWorkerFirstRollout proves upgraded
// producers can keep emitting the frozen route until every worker is replaced.
func TestLegacyDirectEnvelopeOptionSupportsWorkerFirstRollout(t *testing.T) {
	inner := &queueBackendRecorder{}
	runtime := &nativeQueueRuntime{
		common:  &queueCommon{inner: inner, cfg: Config{DefaultQueue: "default"}, driver: DriverSync},
		runtime: &runtimeBackendStub{},
		nativeQueueRuntimeState: &nativeQueueRuntimeState{
			registered: make(map[string]Handler),
		},
	}
	q, err := newQueueFromRuntime(runtime, WithLegacyDirectEnvelope())
	if err != nil {
		t.Fatalf("new queue: %v", err)
	}
	q.Register("reports:legacy-rollout", func(context.Context, Message) error { return nil })

	result, err := q.Dispatch(NewJob("reports:legacy-rollout").Payload([]byte(`{"id":1}`)))
	if err != nil {
		t.Fatalf("dispatch legacy rollout: %v", err)
	}
	if len(inner.dispatched) != 1 || inner.dispatched[0].Type != "bus:job" {
		t.Fatalf("physical deliveries = %+v, want one bus:job", inner.dispatched)
	}
	if DriverMetadata(inner.dispatched[0]).SchemaVersion != 0 {
		t.Fatalf("legacy delivery unexpectedly carried direct metadata: %+v", DriverMetadata(inner.dispatched[0]))
	}
	metadata := ResolveObservedJobMetadata(inner.dispatched[0].Type, inner.dispatched[0].PayloadBytes())
	if metadata.JobType != "reports:legacy-rollout" || metadata.DispatchID != result.DispatchID || metadata.JobID == "" {
		t.Fatalf("legacy rollout metadata = %+v, receipt = %+v", metadata, result)
	}
}

// TestLegacyDirectEnvelopeExecutesOnUpgradedWorker proves an upgraded worker
// consumes old-envelope backlog through the same application handler and message.
func TestLegacyDirectEnvelopeExecutesOnUpgradedWorker(t *testing.T) {
	q, err := NewSync(WithLegacyDirectEnvelope())
	if err != nil {
		t.Fatalf("new legacy-emitting queue: %v", err)
	}
	t.Cleanup(func() {
		if shutdownErr := q.Shutdown(context.Background()); shutdownErr != nil {
			t.Errorf("shutdown: %v", shutdownErr)
		}
	})

	received := make(chan Message, 1)
	q.Register("reports:legacy-backlog", func(_ context.Context, message Message) error {
		received <- message
		return nil
	})
	if err := q.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start upgraded worker: %v", err)
	}
	result, err := q.Dispatch(NewJob("reports:legacy-backlog").Payload([]byte(`{"id":7}`)))
	if err != nil {
		t.Fatalf("dispatch legacy backlog job: %v", err)
	}
	message := <-received
	if message.DispatchID != result.DispatchID || message.JobID == "" || message.JobType != "reports:legacy-backlog" {
		t.Fatalf("legacy backlog message = %+v, receipt = %+v", message, result)
	}
	if !bytes.Equal(message.PayloadBytes(), []byte(`{"id":7}`)) {
		t.Fatalf("legacy backlog payload = %q", message.PayloadBytes())
	}
}

// TestNestedLegacyDeliveryShadowsParentMetadata prevents an inline reserved
// delivery from inheriting the direct job correlation that dispatched it.
func TestNestedLegacyDeliveryShadowsParentMetadata(t *testing.T) {
	q, err := NewSync()
	if err != nil {
		t.Fatalf("new sync queue: %v", err)
	}
	t.Cleanup(func() {
		if shutdownErr := q.Shutdown(context.Background()); shutdownErr != nil {
			t.Errorf("shutdown: %v", shutdownErr)
		}
	})

	metadataSeen := make(chan busruntime.DeliveryMetadata, 1)
	q.Register("bus:callback", func(ctx context.Context, _ Message) error {
		metadata, _ := busruntime.DeliveryMetadataFromContext(ctx)
		metadataSeen <- metadata
		return nil
	})
	q.Register("reports:parent", func(ctx context.Context, _ Message) error {
		_, dispatchErr := q.WithContext(ctx).Dispatch(NewJob("bus:callback").Payload([]byte(`{"nested":true}`)))
		return dispatchErr
	})
	if err := q.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers: %v", err)
	}
	if _, err := q.Dispatch(NewJob("reports:parent")); err != nil {
		t.Fatalf("dispatch parent: %v", err)
	}
	if metadata := <-metadataSeen; metadata != (busruntime.DeliveryMetadata{}) {
		t.Fatalf("nested legacy delivery inherited parent metadata: %+v", metadata)
	}
}

// TestDriverJobMetadataRejectsUnknownVersions ensures future transport metadata
// cannot spoof correlation on a worker that does not understand its semantics.
func TestDriverJobMetadataRejectsUnknownVersions(t *testing.T) {
	job := DriverWithMetadata(NewJob("reports:build"), DriverJobMetadata{
		SchemaVersion: DriverJobMetadataVersion + 1,
		DispatchID:    "spoofed",
		JobID:         "spoofed",
	})
	if metadata := DriverMetadata(job); metadata != (DriverJobMetadata{}) {
		t.Fatalf("unknown metadata = %+v, want zero value", metadata)
	}
	observed := ResolveObservedJobMetadataFromJob(job)
	if observed.DispatchID != "" || observed.JobID != "" || observed.JobType != "reports:build" {
		t.Fatalf("unknown metadata affected observation: %+v", observed)
	}
}
