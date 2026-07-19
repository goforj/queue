package sqsqueue

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
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

// TestSQSQueueDriverAndPreflight verifies driver identity, nil-context
// normalization, queue discovery, and cancellation before client activity.
func TestSQSQueueDriverAndPreflight(t *testing.T) {
	client := &sqsWorkerClientStub{queueURL: "https://example.local/queue/critical"}
	q := newSQSQueue(Config{})
	q.cfg.DefaultQueue = "critical"
	q.client = client
	if got := q.Driver(); got != queue.DriverSQS {
		t.Fatalf("driver = %q, want %q", got, queue.DriverSQS)
	}
	if got := q.physicalQueueName(); got != "critical" {
		t.Fatalf("physical queue = %q, want critical", got)
	}
	if err := q.Preflight(nil); err != nil {
		t.Fatalf("preflight with configured client: %v", err)
	}
	if len(client.getQueueInputs) != 1 || aws.ToString(client.getQueueInputs[0].QueueName) != "critical" {
		t.Fatalf("queue lookups = %+v, want one critical lookup", client.getQueueInputs)
	}
	if got := q.queueURLs["critical"]; got != client.queueURL {
		t.Fatalf("cached queue URL = %q, want %q", got, client.queueURL)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := q.Preflight(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled preflight error = %v, want context.Canceled", err)
	}
	if len(client.getQueueInputs) != 1 {
		t.Fatalf("canceled preflight performed %d queue lookups, want 0 additional lookups", len(client.getQueueInputs))
	}
	if got := newSQSQueue(Config{}).physicalQueueName(); got != "default" {
		t.Fatalf("default physical queue = %q, want default", got)
	}
}

// TestGetOrCreateSQSQueueBoundaries verifies malformed success responses and
// service errors are returned without nil dereferences.
func TestGetOrCreateSQSQueueBoundaries(t *testing.T) {
	getErr := errors.New("lookup failed")
	createErr := errors.New("create failed")
	tests := []struct {
		name        string
		client      *sqsWorkerClientStub
		wantURL     string
		wantErr     error
		wantAnyErr  bool
		wantCreates int
	}{
		{
			name:    "existing queue",
			client:  &sqsWorkerClientStub{queueURL: "https://example.local/queue/existing"},
			wantURL: "https://example.local/queue/existing",
		},
		{
			name: "missing queue is created",
			client: &sqsWorkerClientStub{
				getQueueErr:  &types.QueueDoesNotExist{},
				createOutput: &sqs.CreateQueueOutput{QueueUrl: aws.String("https://example.local/queue/created")},
			},
			wantURL:     "https://example.local/queue/created",
			wantCreates: 1,
		},
		{
			name: "nil lookup success falls back to creation",
			client: &sqsWorkerClientStub{
				getNilSuccess: true,
				createOutput:  &sqs.CreateQueueOutput{QueueUrl: aws.String("https://example.local/queue/from-nil")},
			},
			wantURL:     "https://example.local/queue/from-nil",
			wantCreates: 1,
		},
		{
			name:    "lookup rejection",
			client:  &sqsWorkerClientStub{getQueueErr: getErr},
			wantErr: getErr,
		},
		{
			name: "creation rejection",
			client: &sqsWorkerClientStub{
				getQueueErr: &types.QueueDoesNotExist{},
				createErr:   createErr,
			},
			wantErr:     createErr,
			wantCreates: 1,
		},
		{
			name: "nil creation success",
			client: &sqsWorkerClientStub{
				getQueueErr:      &types.QueueDoesNotExist{},
				createNilSuccess: true,
			},
			wantAnyErr:  true,
			wantCreates: 1,
		},
		{
			name: "empty creation URL",
			client: &sqsWorkerClientStub{
				getQueueErr:  &types.QueueDoesNotExist{},
				createOutput: &sqs.CreateQueueOutput{},
			},
			wantAnyErr:  true,
			wantCreates: 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := getOrCreateSQSQueue(context.Background(), test.client, "reports")
			if test.wantErr != nil && !errors.Is(err, test.wantErr) {
				t.Fatalf("queue resolution error = %v, want %v", err, test.wantErr)
			}
			if test.wantAnyErr && err == nil {
				t.Fatal("malformed service response unexpectedly succeeded")
			}
			if test.wantErr == nil && !test.wantAnyErr && err != nil {
				t.Fatalf("queue resolution: %v", err)
			}
			if got != test.wantURL {
				t.Fatalf("queue URL = %q, want %q", got, test.wantURL)
			}
			if len(test.client.createInputs) != test.wantCreates {
				t.Fatalf("create calls = %d, want %d", len(test.client.createInputs), test.wantCreates)
			}
		})
	}
}

// TestSQSQueueDelayEncodingBoundsServiceDelay verifies SQS receives only its
// supported delay while the wire message retains the full delivery deadline.
func TestSQSQueueDelayEncodingBoundsServiceDelay(t *testing.T) {
	client := &sqsWorkerClientStub{}
	q := newSQSQueue(Config{})
	q.client = client
	q.queueURLs["default"] = "https://example.local/queue/default"
	started := time.Now()
	job := queue.NewJob("reports:delayed").OnQueue("default").Delay(901 * time.Second)
	if err := q.Dispatch(context.Background(), job); err != nil {
		t.Fatalf("dispatch delayed job: %v", err)
	}
	finished := time.Now()
	if len(client.sendInputs) != 1 {
		t.Fatalf("send calls = %d, want 1", len(client.sendInputs))
	}
	if got := client.sendInputs[0].DelaySeconds; got != 900 {
		t.Fatalf("service delay = %d seconds, want 900", got)
	}
	message := decodeSQSBody(t, client.sendInputs[0])
	minimumDeadline := started.Add(901 * time.Second).Add(-time.Millisecond).UnixMilli()
	maximumDeadline := finished.Add(901 * time.Second).Add(time.Millisecond).UnixMilli()
	if message.AvailableAtMS < minimumDeadline || message.AvailableAtMS > maximumDeadline {
		t.Fatalf("wire availability = %d, want the original 901-second deadline in [%d, %d]", message.AvailableAtMS, minimumDeadline, maximumDeadline)
	}
}

// TestSQSQueueEnsureQueueRejectsMissingClient verifies shutdown races fail with
// a diagnostic instead of dereferencing an unavailable client.
func TestSQSQueueEnsureQueueRejectsMissingClient(t *testing.T) {
	q := newSQSQueue(Config{})
	if _, err := q.ensureQueue(context.Background(), "default"); err == nil {
		t.Fatal("queue resolution without a client unexpectedly succeeded")
	}
}

// TestSQSQueueRejectedResolutionReleasesUniqueClaim verifies a failure before
// send does not retain uniqueness state for a message SQS never accepted.
func TestSQSQueueRejectedResolutionReleasesUniqueClaim(t *testing.T) {
	q := newSQSQueue(Config{})
	q.client = &sqsWorkerClientStub{}
	job := queue.NewJob("reports:resolve").OnQueue("default").UniqueFor(time.Minute)

	if err := q.Dispatch(context.Background(), job); err == nil || errors.Is(err, queue.ErrDuplicate) {
		t.Fatalf("queue resolution error = %v, want a pre-send rejection", err)
	}
	key, token, acquired := q.claimUnique(job, "default", time.Minute)
	if !acquired {
		t.Fatal("pre-send queue resolution failure retained the uniqueness claim")
	}
	q.unique.Release(key, token)
}

// TestSQSQueueShutdownRaceBeforeSendReleasesUniqueClaim verifies a concurrent
// shutdown after queue resolution cannot retain a claim for an unsent message.
func TestSQSQueueShutdownRaceBeforeSendReleasesUniqueClaim(t *testing.T) {
	q := newSQSQueue(Config{})
	client := &sqsWorkerClientStub{queueURL: "https://example.local/queue/default"}
	client.queueURLHook = func() {
		if err := q.Shutdown(context.Background()); err != nil {
			t.Errorf("shutdown during queue resolution: %v", err)
		}
	}
	q.client = client
	job := queue.NewJob("reports:shutdown-race").OnQueue("default").UniqueFor(time.Minute)

	if err := q.Dispatch(context.Background(), job); err == nil || errors.Is(err, queue.ErrDuplicate) {
		t.Fatalf("shutdown-race dispatch error = %v, want an unavailable-client rejection", err)
	}
	if len(client.sendInputs) != 0 {
		t.Fatalf("shutdown-race sends = %d, want 0", len(client.sendInputs))
	}
	key, token, acquired := q.claimUnique(job, "default", time.Minute)
	if !acquired {
		t.Fatal("shutdown before send retained the uniqueness claim")
	}
	q.unique.Release(key, token)
}
