package sqsqueue

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/goforj/queue"
)

// TestSQSDirectDeliveryMetadataRoundTrip verifies producer framing, worker
// reconstruction, retry preservation, and legacy-envelope observation.
func TestSQSDirectDeliveryMetadataRoundTrip(t *testing.T) {
	wantMetadata := queue.DriverJobMetadata{
		SchemaVersion: queue.DriverJobMetadataVersion,
		DispatchID:    "dsp_sqs_direct",
		JobID:         "job_sqs_direct",
		Queue:         "critical",
	}
	wantPayload := []byte(`{"report_id":42}`)
	job := queue.DriverWithMetadata(
		queue.NewJob("reports:build").Payload(wantPayload).OnQueue("critical").Retry(3),
		wantMetadata,
	)
	message, err := sqsMessageForJob(job, queue.DriverOptions(job))
	if err != nil {
		t.Fatalf("build direct message: %v", err)
	}
	var wireMetadata queue.DriverJobMetadata
	if err := json.Unmarshal(message.Metadata, &wireMetadata); err != nil || wireMetadata != wantMetadata {
		t.Fatalf("wire metadata = %+v, want %+v (err=%v)", wireMetadata, wantMetadata, err)
	}

	wire, err := json.Marshal(message)
	if err != nil {
		t.Fatalf("marshal direct message: %v", err)
	}
	var decoded sqsMessage
	if err := json.Unmarshal(wire, &decoded); err != nil {
		t.Fatalf("unmarshal direct message: %v", err)
	}
	delivery := sqsDeliveryJob(decoded)
	if delivery.Type != "reports:build" || !bytes.Equal(delivery.PayloadBytes(), wantPayload) {
		t.Fatalf("delivery = type:%q payload:%q", delivery.Type, delivery.PayloadBytes())
	}
	if got := queue.DriverMetadata(delivery); got != wantMetadata {
		t.Fatalf("reconstructed metadata = %+v, want %+v", got, wantMetadata)
	}
	observed := queue.ResolveObservedJobMetadataFromJob(delivery)
	if observed.DispatchID != wantMetadata.DispatchID || observed.JobID != wantMetadata.JobID || observed.JobType != job.Type {
		t.Fatalf("direct observation = %+v", observed)
	}
	var events []queue.Event
	worker := &sqsWorker{observer: queue.ObserverFunc(func(_ context.Context, event queue.Event) {
		events = append(events, event)
	})}
	worker.observeRepublishFailure(context.Background(), decoded, errors.New("republish failed"))
	if len(events) != 1 || events[0].DispatchID != wantMetadata.DispatchID || events[0].JobID != wantMetadata.JobID {
		t.Fatalf("direct republish observation = %+v", events)
	}

	decoded.Attempt++
	client := &sqsWorkerClientStub{}
	worker.client = client
	worker.queueURL = "https://example.local/queue/critical"
	if err := worker.republish(decoded); err != nil {
		t.Fatalf("republish direct message: %v", err)
	}
	if len(client.sendInputs) != 1 {
		t.Fatalf("republished messages = %d, want 1", len(client.sendInputs))
	}
	retry := decodeSQSBody(t, client.sendInputs[0])
	retryJob := sqsDeliveryJob(retry)
	if got := queue.DriverMetadata(retryJob); got != wantMetadata {
		t.Fatalf("retry metadata = %+v, want %+v", got, wantMetadata)
	}
	if got := queue.DriverOptions(retryJob).Attempt; got != 1 {
		t.Fatalf("retry attempt = %d, want 1", got)
	}

	legacyPayload := []byte(`{"schema_version":1,"dispatch_id":"dsp_sqs_legacy","job_id":"job_sqs_legacy","job":{"type":"reports:legacy","payload":"e30="}}`)
	legacy := queue.ResolveObservedJobMetadataFromJob(sqsDeliveryJob(sqsMessage{Type: "bus:job", Payload: legacyPayload}))
	if legacy.JobType != "reports:legacy" || legacy.DispatchID != "dsp_sqs_legacy" || legacy.JobID != "job_sqs_legacy" {
		t.Fatalf("legacy observation = %+v", legacy)
	}

	plainJob := queue.NewJob("reports:plain").OnQueue("default")
	plain, err := sqsMessageForJob(plainJob, queue.DriverOptions(plainJob))
	if err != nil {
		t.Fatalf("build metadata-absent message: %v", err)
	}
	plainWire, err := json.Marshal(plain)
	if err != nil {
		t.Fatalf("marshal metadata-absent message: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(plainWire, &fields); err != nil {
		t.Fatalf("inspect metadata-absent message: %v", err)
	}
	if _, ok := fields["metadata"]; ok {
		t.Fatalf("metadata-absent wire unexpectedly contains metadata: %s", plainWire)
	}
}

// TestSQSUntrustedMetadataRemainsAnOpaqueRetrySidecar verifies valid
// application bytes survive malformed metadata and future fields survive republish.
func TestSQSUntrustedMetadataRemainsAnOpaqueRetrySidecar(t *testing.T) {
	for _, raw := range []string{`"malformed"`, `{"schema_version":"bad","dispatch_id":"spoofed"}`} {
		wire := []byte(`{"type":"reports:build","payload":"AQI=","queue":"critical","metadata":` + raw + `}`)
		var message sqsMessage
		if err := json.Unmarshal(wire, &message); err != nil {
			t.Fatalf("decode message with metadata %s: %v", raw, err)
		}
		delivery := sqsDeliveryJob(message)
		if delivery.Type != "reports:build" || !bytes.Equal(delivery.PayloadBytes(), []byte{1, 2}) {
			t.Fatalf("delivery with metadata %s = type:%q payload:%v", raw, delivery.Type, delivery.PayloadBytes())
		}
		if metadata := queue.DriverMetadata(delivery); metadata != (queue.DriverJobMetadata{}) {
			t.Fatalf("untrusted metadata %s became trusted: %+v", raw, metadata)
		}
	}

	future := json.RawMessage(`{"schema_version":99,"dispatch_id":"future","future_field":{"id":7}}`)
	client := &sqsWorkerClientStub{}
	worker := &sqsWorker{client: client, queueURL: "https://example.local/queue/critical"}
	if err := worker.republish(sqsMessage{Type: "reports:build", Queue: "critical", Metadata: future}); err != nil {
		t.Fatalf("republish future metadata: %v", err)
	}
	if len(client.sendInputs) != 1 {
		t.Fatalf("republished messages = %d, want 1", len(client.sendInputs))
	}
	retry := decodeSQSBody(t, client.sendInputs[0])
	if !bytes.Equal(retry.Metadata, future) {
		t.Fatalf("future retry metadata = %s, want %s", retry.Metadata, future)
	}
	if metadata := queue.DriverMetadata(sqsDeliveryJob(retry)); metadata != (queue.DriverJobMetadata{}) {
		t.Fatalf("future metadata became trusted: %+v", metadata)
	}
}

// TestSQSQueueAmbiguousDispatchRetainsUniqueClaim verifies a lost send response fails closed against duplicate retries.
func TestSQSQueueAmbiguousDispatchRetainsUniqueClaim(t *testing.T) {
	sendErr := errors.New("send response lost")
	client := &sqsWorkerClientStub{sendErr: sendErr}
	q := newSQSQueue(Config{})
	q.client = client
	q.queueURLs["default"] = "https://example.local/queue/default"
	job := queue.NewJob("reports:build").Payload([]byte(`{"id":1}`)).OnQueue("default").UniqueFor(time.Minute)

	if err := q.Dispatch(context.Background(), job); !errors.Is(err, sendErr) {
		t.Fatalf("first dispatch error = %v, want send rejection", err)
	}
	if err := q.Dispatch(context.Background(), job); !errors.Is(err, queue.ErrDuplicate) {
		t.Fatalf("ambiguous dispatch did not retain claim: %v", err)
	}
}

// TestSQSQueueMissingReceiptRetainsUniqueClaim verifies a receipt-less response cannot admit an immediate duplicate retry.
func TestSQSQueueMissingReceiptRetainsUniqueClaim(t *testing.T) {
	client := &sqsWorkerClientStub{sendNil: true}
	q := newSQSQueue(Config{})
	q.client = client
	q.queueURLs["default"] = "https://example.local/queue/default"
	job := queue.NewJob("reports:receipt").OnQueue("default").UniqueFor(time.Minute)
	if err := q.Dispatch(context.Background(), job); err == nil {
		t.Fatal("missing SQS message id was accepted")
	}
	client.sendNil = false
	if err := q.Dispatch(context.Background(), job); !errors.Is(err, queue.ErrDuplicate) {
		t.Fatalf("missing receipt did not retain uniqueness claim: %v", err)
	}
}

// TestSQSQueueCanceledDispatchStopsBeforeClaim verifies cancellation cannot send or consume uniqueness state.
func TestSQSQueueCanceledDispatchStopsBeforeClaim(t *testing.T) {
	client := &sqsWorkerClientStub{}
	q := newSQSQueue(Config{})
	q.client = client
	q.queueURLs["default"] = "https://example.local/queue/default"
	job := queue.NewJob("reports:canceled").OnQueue("default").UniqueFor(time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := q.Dispatch(ctx, job); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled dispatch = %v, want context.Canceled", err)
	}
	key, token, ok := q.claimUnique(job, "default", time.Minute)
	if !ok {
		t.Fatal("canceled dispatch consumed uniqueness state")
	}
	q.unique.Release(key, token)
	if len(client.sendInputs) != 0 {
		t.Fatalf("canceled dispatch sent %d messages", len(client.sendInputs))
	}
}
