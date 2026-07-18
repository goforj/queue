package rabbitmqqueue

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/goforj/queue"
	amqp "github.com/rabbitmq/amqp091-go"
)

// TestRabbitMQDirectDeliveryMetadataRoundTrip verifies producer framing, worker
// reconstruction, retry preservation, and legacy-envelope observation.
func TestRabbitMQDirectDeliveryMetadataRoundTrip(t *testing.T) {
	wantMetadata := queue.DriverJobMetadata{
		SchemaVersion: queue.DriverJobMetadataVersion,
		DispatchID:    "dsp_rabbit_direct",
		JobID:         "job_rabbit_direct",
		Queue:         "critical",
	}
	wantPayload := []byte(`{"report_id":42}`)
	job := queue.DriverWithMetadata(
		queue.NewJob("reports:build").Payload(wantPayload).OnQueue("critical").Retry(3),
		wantMetadata,
	)
	message, err := rabbitMQMessageForJob(job, queue.DriverOptions(job))
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
	var decoded rabbitMQMessage
	if err := json.Unmarshal(wire, &decoded); err != nil {
		t.Fatalf("unmarshal direct message: %v", err)
	}
	delivery := rabbitMQDeliveryJob(decoded)
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
	worker := &rabbitMQWorker{observer: queue.ObserverFunc(func(_ context.Context, event queue.Event) {
		events = append(events, event)
	})}
	worker.observeRepublishFailure(context.Background(), decoded, errors.New("republish failed"))
	if len(events) != 1 || events[0].DispatchID != wantMetadata.DispatchID || events[0].JobID != wantMetadata.JobID {
		t.Fatalf("direct republish observation = %+v", events)
	}

	decoded.Attempt++
	var retry rabbitMQMessage
	worker.cfg.DefaultQueue = "default"
	worker.publishOverride = func(_ context.Context, message rabbitMQMessage) error {
		retry = message
		return nil
	}
	if err := worker.publish(context.Background(), decoded); err != nil {
		t.Fatalf("republish direct message: %v", err)
	}
	retryJob := rabbitMQDeliveryJob(retry)
	if got := queue.DriverMetadata(retryJob); got != wantMetadata {
		t.Fatalf("retry metadata = %+v, want %+v", got, wantMetadata)
	}
	if got := queue.DriverOptions(retryJob).Attempt; got != 1 {
		t.Fatalf("retry attempt = %d, want 1", got)
	}

	legacyPayload := []byte(`{"schema_version":1,"dispatch_id":"dsp_rabbit_legacy","job_id":"job_rabbit_legacy","job":{"type":"reports:legacy","payload":"e30="}}`)
	legacy := queue.ResolveObservedJobMetadataFromJob(rabbitMQDeliveryJob(rabbitMQMessage{Type: "bus:job", Payload: legacyPayload}))
	if legacy.JobType != "reports:legacy" || legacy.DispatchID != "dsp_rabbit_legacy" || legacy.JobID != "job_rabbit_legacy" {
		t.Fatalf("legacy observation = %+v", legacy)
	}

	plainJob := queue.NewJob("reports:plain").OnQueue("default")
	plain, err := rabbitMQMessageForJob(plainJob, queue.DriverOptions(plainJob))
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

// TestRabbitMQUntrustedMetadataRemainsAnOpaqueRetrySidecar verifies valid
// application bytes survive malformed metadata and future fields survive republish.
func TestRabbitMQUntrustedMetadataRemainsAnOpaqueRetrySidecar(t *testing.T) {
	for _, raw := range []string{`"malformed"`, `{"schema_version":"bad","dispatch_id":"spoofed"}`} {
		wire := []byte(`{"type":"reports:build","payload":"AQI=","queue":"critical","metadata":` + raw + `}`)
		var message rabbitMQMessage
		if err := json.Unmarshal(wire, &message); err != nil {
			t.Fatalf("decode message with metadata %s: %v", raw, err)
		}
		delivery := rabbitMQDeliveryJob(message)
		if delivery.Type != "reports:build" || !bytes.Equal(delivery.PayloadBytes(), []byte{1, 2}) {
			t.Fatalf("delivery with metadata %s = type:%q payload:%v", raw, delivery.Type, delivery.PayloadBytes())
		}
		if metadata := queue.DriverMetadata(delivery); metadata != (queue.DriverJobMetadata{}) {
			t.Fatalf("untrusted metadata %s became trusted: %+v", raw, metadata)
		}
	}

	future := json.RawMessage(`{"schema_version":99,"dispatch_id":"future","future_field":{"id":7}}`)
	var retry rabbitMQMessage
	worker := &rabbitMQWorker{publishOverride: func(_ context.Context, message rabbitMQMessage) error {
		retry = message
		return nil
	}}
	if err := worker.publish(context.Background(), rabbitMQMessage{Type: "reports:build", Queue: "critical", Metadata: future}); err != nil {
		t.Fatalf("republish future metadata: %v", err)
	}
	if !bytes.Equal(retry.Metadata, future) {
		t.Fatalf("future retry metadata = %s, want %s", retry.Metadata, future)
	}
	if metadata := queue.DriverMetadata(rabbitMQDeliveryJob(retry)); metadata != (queue.DriverJobMetadata{}) {
		t.Fatalf("future metadata became trusted: %+v", metadata)
	}
}

func TestRabbitMQQueue_HelperBranches(t *testing.T) {
	qDefault := newRabbitMQQueue("amqp://example", "")
	if qDefault.defaultQueue != "default" {
		t.Fatalf("expected default queue fallback, got %q", qDefault.defaultQueue)
	}
	qNamed := newRabbitMQQueue("amqp://example", "critical")
	if qNamed.defaultQueue != "critical" {
		t.Fatalf("expected explicit default queue, got %q", qNamed.defaultQueue)
	}

	if err := qDefault.enqueueLocked(context.Background(), "default", []byte("{}")); !errors.Is(err, amqp.ErrClosed) {
		t.Fatalf("expected amqp.ErrClosed when channel missing, got %v", err)
	}

	qDefault.closeLocked()
	if qDefault.conn != nil || qDefault.ch != nil {
		t.Fatal("expected closeLocked to nil connection/channel")
	}
	if err := qDefault.Shutdown(context.Background()); err != nil {
		t.Fatalf("expected shutdown nil-safe path, got %v", err)
	}
}

func TestRabbitMQQueue_DispatchValidationAndDuplicate(t *testing.T) {
	q := newRabbitMQQueue("amqp://example", "default")

	if err := q.Dispatch(context.Background(), queue.NewJob("")); err == nil {
		t.Fatal("expected validation error for empty job type")
	}
	if err := q.Dispatch(context.Background(), queue.NewJob("job:noqueue")); err == nil {
		t.Fatal("expected queue required error")
	}

	job := queue.NewJob("job:dup").Payload([]byte(`{"k":"v"}`)).OnQueue("default").UniqueFor(10 * time.Second)
	_, _, _ = q.claimUnique(job, "default", 10*time.Second)
	if err := q.Dispatch(context.Background(), job); !errors.Is(err, queue.ErrDuplicate) {
		t.Fatalf("expected ErrDuplicate before dial path, got %v", err)
	}
}

// TestRabbitMQQueueCanceledDispatchStopsBeforeClaim verifies cancellation cannot publish or consume uniqueness state.
func TestRabbitMQQueueCanceledDispatchStopsBeforeClaim(t *testing.T) {
	q := newRabbitMQQueue("://bad-url", "default")
	job := queue.NewJob("job:canceled").OnQueue("default").UniqueFor(time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := q.Dispatch(ctx, job); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled dispatch error = %v, want context.Canceled", err)
	}
	key, token, acquired := q.claimUnique(job, "default", time.Minute)
	if !acquired {
		t.Fatal("canceled dispatch consumed the uniqueness claim")
	}
	q.unique.Release(key, token)
}

func TestRabbitMQQueue_ClaimUniquePrunesExpired(t *testing.T) {
	q := newRabbitMQQueue("amqp://example", "default")
	job := queue.NewJob("job:unique").Payload([]byte(`{"id":1}`)).OnQueue("default")
	if _, _, ok := q.claimUnique(job, "default", time.Millisecond); !ok {
		t.Fatal("expected initial claim to succeed")
	}
	time.Sleep(2 * time.Millisecond)
	if _, _, ok := q.claimUnique(job, "default", 5*time.Second); !ok {
		t.Fatal("expected expired key to be pruned and claim to succeed")
	}
}

// TestRabbitMQQueueRejectedDispatchReleasesUniqueClaim verifies connection rejection cannot retain a false acceptance.
func TestRabbitMQQueueRejectedDispatchReleasesUniqueClaim(t *testing.T) {
	q := newRabbitMQQueue("://bad-url", "default")
	q.dialTimeout = 5 * time.Millisecond
	job := queue.NewJob("job:unique:rejected").OnQueue("default").UniqueFor(time.Minute)
	first := q.Dispatch(context.Background(), job)
	if first == nil || errors.Is(first, queue.ErrDuplicate) {
		t.Fatalf("first dispatch error = %v, want connection rejection", first)
	}
	second := q.Dispatch(context.Background(), job)
	if second == nil || errors.Is(second, queue.ErrDuplicate) {
		t.Fatalf("second dispatch error = %v, uniqueness claim was not compensated", second)
	}
}

func TestRabbitMQQueue_EnsureConnectedLockedAndErrorClassifier(t *testing.T) {
	q := newRabbitMQQueue("://bad-url", "default")
	q.dialTimeout = 5 * time.Millisecond
	if err := q.ensureConnectedLocked(); err == nil {
		t.Fatal("expected ensureConnectedLocked to fail for invalid url")
	}

	if isRabbitConnectionClosed(nil) {
		t.Fatal("expected nil error not closed")
	}
	if !isRabbitConnectionClosed(amqp.ErrClosed) {
		t.Fatal("expected amqp.ErrClosed to be treated as closed")
	}
	if !isRabbitConnectionClosed(errors.New("channel/connection is not open")) {
		t.Fatal("expected closed-message string to be treated as closed")
	}
	if isRabbitConnectionClosed(errors.New("something else")) {
		t.Fatal("expected unrelated error not to be treated as closed")
	}
}

func TestRabbitPhysicalQueueName(t *testing.T) {
	if got := rabbitPhysicalQueueName("default", "critical"); got != "critical" {
		t.Fatalf("expected message queue to win, got %q", got)
	}
	if got := rabbitPhysicalQueueName("default", ""); got != "default" {
		t.Fatalf("expected default queue fallback, got %q", got)
	}
	if got := rabbitPhysicalQueueName("", ""); got != "default" {
		t.Fatalf("expected hard default fallback, got %q", got)
	}
}
