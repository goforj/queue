package natsqueue

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/goforj/queue"
	"github.com/nats-io/nats.go"
)

// TestNATSDirectDeliveryMetadataRoundTrip verifies producer framing, worker
// reconstruction, retry preservation, and legacy-envelope observation.
func TestNATSDirectDeliveryMetadataRoundTrip(t *testing.T) {
	wantMetadata := queue.DriverJobMetadata{
		SchemaVersion: queue.DriverJobMetadataVersion,
		DispatchID:    "dsp_nats_direct",
		JobID:         "job_nats_direct",
		Queue:         "critical",
	}
	wantPayload := []byte(`{"report_id":42}`)
	job := queue.DriverWithMetadata(
		queue.NewJob("reports:build").Payload(wantPayload).OnQueue("critical").Retry(3),
		wantMetadata,
	)
	message, err := natsMessageForJob(job, queue.DriverOptions(job))
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
	var decoded natsMessage
	if err := json.Unmarshal(wire, &decoded); err != nil {
		t.Fatalf("unmarshal direct message: %v", err)
	}
	delivery := natsDeliveryJob(decoded)
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
	worker := &natsWorker{observer: queue.ObserverFunc(func(_ context.Context, event queue.Event) {
		events = append(events, event)
	})}
	worker.observeRepublishFailure(context.Background(), decoded, errors.New("republish failed"))
	if len(events) != 1 || events[0].DispatchID != wantMetadata.DispatchID || events[0].JobID != wantMetadata.JobID {
		t.Fatalf("direct republish observation = %+v", events)
	}

	decoded.Attempt++
	connection := &natsConnectionStub{}
	worker.conn = connection
	if err := worker.republish(decoded); err != nil {
		t.Fatalf("republish direct message: %v", err)
	}
	if len(connection.published) != 1 {
		t.Fatalf("republished messages = %d, want 1", len(connection.published))
	}
	var retry natsMessage
	if err := json.Unmarshal(connection.published[0], &retry); err != nil {
		t.Fatalf("unmarshal retry message: %v", err)
	}
	retryJob := natsDeliveryJob(retry)
	if got := queue.DriverMetadata(retryJob); got != wantMetadata {
		t.Fatalf("retry metadata = %+v, want %+v", got, wantMetadata)
	}
	if got := queue.DriverOptions(retryJob).Attempt; got != 1 {
		t.Fatalf("retry attempt = %d, want 1", got)
	}

	legacyPayload := []byte(`{"schema_version":1,"dispatch_id":"dsp_nats_legacy","job_id":"job_nats_legacy","job":{"type":"reports:legacy","payload":"e30="}}`)
	legacy := queue.ResolveObservedJobMetadataFromJob(natsDeliveryJob(natsMessage{Type: "bus:job", Payload: legacyPayload}))
	if legacy.JobType != "reports:legacy" || legacy.DispatchID != "dsp_nats_legacy" || legacy.JobID != "job_nats_legacy" {
		t.Fatalf("legacy observation = %+v", legacy)
	}

	plainJob := queue.NewJob("reports:plain").OnQueue("default")
	plain, err := natsMessageForJob(plainJob, queue.DriverOptions(plainJob))
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

// TestNATSUntrustedMetadataRemainsAnOpaqueRetrySidecar verifies valid
// application bytes survive malformed metadata and future fields survive republish.
func TestNATSUntrustedMetadataRemainsAnOpaqueRetrySidecar(t *testing.T) {
	for _, raw := range []string{`"malformed"`, `{"schema_version":"bad","dispatch_id":"spoofed"}`} {
		wire := []byte(`{"type":"reports:build","payload":"AQI=","queue":"critical","metadata":` + raw + `}`)
		var message natsMessage
		if err := json.Unmarshal(wire, &message); err != nil {
			t.Fatalf("decode message with metadata %s: %v", raw, err)
		}
		delivery := natsDeliveryJob(message)
		if delivery.Type != "reports:build" || !bytes.Equal(delivery.PayloadBytes(), []byte{1, 2}) {
			t.Fatalf("delivery with metadata %s = type:%q payload:%v", raw, delivery.Type, delivery.PayloadBytes())
		}
		if metadata := queue.DriverMetadata(delivery); metadata != (queue.DriverJobMetadata{}) {
			t.Fatalf("untrusted metadata %s became trusted: %+v", raw, metadata)
		}
	}

	future := json.RawMessage(`{"schema_version":99,"dispatch_id":"future","future_field":{"id":7}}`)
	connection := &natsConnectionStub{}
	worker := &natsWorker{conn: connection}
	if err := worker.republish(natsMessage{Type: "reports:build", Queue: "critical", Metadata: future}); err != nil {
		t.Fatalf("republish future metadata: %v", err)
	}
	var retry natsMessage
	if len(connection.published) != 1 {
		t.Fatalf("republished messages = %d, want 1", len(connection.published))
	}
	if err := json.Unmarshal(connection.published[0], &retry); err != nil {
		t.Fatalf("decode future retry: %v", err)
	}
	if !bytes.Equal(retry.Metadata, future) {
		t.Fatalf("future retry metadata = %s, want %s", retry.Metadata, future)
	}
	if metadata := queue.DriverMetadata(natsDeliveryJob(retry)); metadata != (queue.DriverJobMetadata{}) {
		t.Fatalf("future metadata became trusted: %+v", metadata)
	}
}

type natsConnectionStub struct {
	publishErr error
	flushErr   error
	publishN   int
	flushN     int
	closeN     int
	published  [][]byte
}

// Publish returns the configured acceptance result.
func (s *natsConnectionStub) Publish(_ string, payload []byte) error {
	s.publishN++
	s.published = append(s.published, append([]byte(nil), payload...))
	return s.publishErr
}

// FlushWithContext reports successful readiness for the stub connection.
func (s *natsConnectionStub) FlushWithContext(context.Context) error {
	s.flushN++
	return s.flushErr
}

// Drain reports successful shutdown for the stub connection.
func (s *natsConnectionStub) Drain() error { return nil }

// Close records producer resource cleanup.
func (s *natsConnectionStub) Close() { s.closeN++ }

func TestNATSQueue_EnsureConnShortCircuitsWhenPresent(t *testing.T) {
	q := newNATSQueue("nats://127.0.0.1:1")
	q.nc = &nats.Conn{}
	if err := q.ensureConn(); err != nil {
		t.Fatalf("expected ensureConn to short-circuit when conn already present, got %v", err)
	}
}

func TestNATSQueue_DispatchValidationAndConnectionFailure(t *testing.T) {
	q := newNATSQueue("://bad-url")

	if err := q.Dispatch(context.Background(), queue.NewJob("")); err == nil {
		t.Fatal("expected validation error for empty type")
	}

	if err := q.Dispatch(context.Background(), queue.NewJob("job:nats")); err == nil {
		t.Fatal("expected queue required error")
	}

	// Valid job should proceed to ensureConn and fail for invalid URL.
	err := q.Dispatch(context.Background(), queue.NewJob("job:nats").OnQueue("default"))
	if err == nil {
		t.Fatal("expected connection/parse error")
	}
}

func TestNATSQueue_ShutdownNilConnAndHelpers(t *testing.T) {
	q := newNATSQueue("nats://127.0.0.1:1")
	if err := q.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown with nil conn failed: %v", err)
	}

	job := queue.NewJob("job:nats").Payload(map[string]any{"id": 1}).OnQueue("default")
	if _, _, ok := q.claimUnique(job, "default", time.Minute); !ok {
		t.Fatal("expected first unique claim to succeed")
	}
	if _, _, ok := q.claimUnique(job, "default", time.Minute); ok {
		t.Fatal("expected duplicate unique claim to fail")
	}

	if got := natsSubject("critical"); got != "queue.critical" {
		t.Fatalf("unexpected nats subject: %q", got)
	}
}

// TestNATSQueueShutdownClosesWithoutAsynchronousDrain verifies producer cleanup cannot outlive the public shutdown deadline.
func TestNATSQueueShutdownClosesWithoutAsynchronousDrain(t *testing.T) {
	connection := &natsConnectionStub{}
	q := newNATSQueue("nats://example")
	q.nc = connection
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := q.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if connection.closeN != 1 || q.nc != nil {
		t.Fatalf("producer cleanup = closes:%d retained:%T, want 1/nil", connection.closeN, q.nc)
	}
}

func TestNATSQueue_EnsureConnFailure(t *testing.T) {
	q := newNATSQueue("://bad-url")
	if err := q.ensureConn(); err == nil {
		t.Fatal("expected ensureConn to fail for invalid URL")
	}
	if q.nc != nil {
		t.Fatal("expected no connection on ensureConn failure")
	}
}

// TestNATSQueuePreflightBoundaries verifies readiness reports both connection
// and server-roundtrip failures without requiring a live NATS server.
func TestNATSQueuePreflightBoundaries(t *testing.T) {
	t.Run("connection failure", func(t *testing.T) {
		q := newNATSQueue("://bad-url")
		if err := q.Preflight(context.Background()); err == nil {
			t.Fatal("expected preflight connection failure")
		}
	})

	t.Run("flush result", func(t *testing.T) {
		flushErr := errors.New("readiness flush failed")
		connection := &natsConnectionStub{flushErr: flushErr}
		q := newNATSQueue("nats://example")
		q.nc = connection
		if err := q.Preflight(nil); !errors.Is(err, flushErr) {
			t.Fatalf("preflight error = %v, want %v", err, flushErr)
		}
		if connection.flushN != 1 {
			t.Fatalf("preflight flush calls = %d, want 1", connection.flushN)
		}
	})
}

// TestNATSQueueNilDispatchContext verifies a nil caller context still reaches
// the bounded server acceptance roundtrip.
func TestNATSQueueNilDispatchContext(t *testing.T) {
	connection := &natsConnectionStub{}
	q := newNATSQueue("nats://example")
	q.nc = connection
	if err := q.Dispatch(nil, queue.NewJob("reports:nil-context").OnQueue("default")); err != nil {
		t.Fatalf("dispatch with nil context: %v", err)
	}
	if connection.publishN != 1 || connection.flushN != 1 {
		t.Fatalf("publish/flush calls = %d/%d, want 1/1", connection.publishN, connection.flushN)
	}
}

func TestNATSQueue_Driver(t *testing.T) {
	q := newNATSQueue("nats://127.0.0.1:1")
	if q.Driver() != queue.DriverNATS {
		t.Fatalf("expected driver %q, got %q", queue.DriverNATS, q.Driver())
	}
}

// TestNATSQueueRejectedPublishReleasesUniqueClaim verifies a failed publish cannot poison the TTL window.
func TestNATSQueueRejectedPublishReleasesUniqueClaim(t *testing.T) {
	publishErr := errors.New("publish rejected")
	connection := &natsConnectionStub{publishErr: publishErr}
	q := newNATSQueue("nats://example")
	q.nc = connection
	job := queue.NewJob("reports:build").Payload([]byte(`{"id":1}`)).OnQueue("default").UniqueFor(time.Minute)
	if err := q.Dispatch(context.Background(), job); !errors.Is(err, publishErr) {
		t.Fatalf("first dispatch error = %v, want publish rejection", err)
	}
	connection.publishErr = nil
	if err := q.Dispatch(context.Background(), job); err != nil {
		t.Fatalf("second dispatch remained poisoned: %v", err)
	}
	if err := q.Dispatch(context.Background(), job); !errors.Is(err, queue.ErrDuplicate) {
		t.Fatalf("accepted publish did not retain claim: %v", err)
	}
}

// TestNATSQueueFlushFailureRetainsUniqueClaim verifies an ambiguous server-roundtrip error fails closed against duplicates.
func TestNATSQueueFlushFailureRetainsUniqueClaim(t *testing.T) {
	flushErr := errors.New("flush response lost")
	connection := &natsConnectionStub{flushErr: flushErr}
	q := newNATSQueue("nats://example")
	q.nc = connection
	job := queue.NewJob("reports:build").OnQueue("default").UniqueFor(time.Minute)
	if err := q.Dispatch(context.Background(), job); !errors.Is(err, flushErr) {
		t.Fatalf("flush failure = %v, want %v", err, flushErr)
	}
	connection.flushErr = nil
	if err := q.Dispatch(context.Background(), job); !errors.Is(err, queue.ErrDuplicate) {
		t.Fatalf("ambiguous publish did not retain uniqueness claim: %v", err)
	}
	if connection.publishN != 1 || connection.flushN != 1 {
		t.Fatalf("publish/flush calls = %d/%d, want 1/1", connection.publishN, connection.flushN)
	}
}

// TestNATSQueueCanceledDispatchStopsBeforeClaim verifies cancellation cannot publish or consume instance uniqueness state.
func TestNATSQueueCanceledDispatchStopsBeforeClaim(t *testing.T) {
	connection := &natsConnectionStub{}
	q := newNATSQueue("nats://example")
	q.nc = connection
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
	if connection.publishN != 0 || connection.flushN != 0 {
		t.Fatalf("canceled dispatch touched NATS: publish=%d flush=%d", connection.publishN, connection.flushN)
	}
}
