package queue

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/goforj/queue/internal/workflow"
)

// TestWorkflowPublicModelsAreRootOwned prevents private engine types from leaking back into the public API.
func TestWorkflowPublicModelsAreRootOwned(t *testing.T) {
	types := []reflect.Type{
		reflect.TypeOf(Message{}),
		reflect.TypeOf(DispatchResult{}),
		reflect.TypeOf(StoredJobOptions{}),
		reflect.TypeOf(StoredJob{}),
		reflect.TypeOf(ChainNode{}),
		reflect.TypeOf(ChainRecord{}),
		reflect.TypeOf(ChainState{}),
		reflect.TypeOf(BatchJob{}),
		reflect.TypeOf(BatchJobOutcome("")),
		reflect.TypeOf(BatchRecord{}),
		reflect.TypeOf(BatchState{}),
		reflect.TypeOf(SQLStoreConfig{}),
		reflect.TypeOf(RetryPolicy{}),
		reflect.TypeOf(SkipWhen{}),
		reflect.TypeOf(FailOnError{}),
		reflect.TypeOf(RateLimit{}),
		reflect.TypeOf(WithoutOverlapping{}),
		reflect.TypeOf((*Next)(nil)).Elem(),
		reflect.TypeOf((*Middleware)(nil)).Elem(),
		reflect.TypeOf((*MiddlewareFunc)(nil)).Elem(),
		reflect.TypeOf((*RateLimiter)(nil)).Elem(),
		reflect.TypeOf((*Lock)(nil)).Elem(),
		reflect.TypeOf((*Locker)(nil)).Elem(),
		reflect.TypeOf((*WorkflowStore)(nil)).Elem(),
		reflect.TypeOf((*WorkflowOutcomeStore)(nil)).Elem(),
	}
	for _, modelType := range types {
		if got := modelType.PkgPath(); got != "github.com/goforj/queue" {
			t.Errorf("%s package = %q, want root queue package", modelType, got)
		}
	}
}

// TestNewMessageCopiesPayload verifies both constructor and accessor isolation across adapter boundaries.
func TestNewMessageCopiesPayload(t *testing.T) {
	payload := []byte(`{"id":7}`)
	message := NewMessage("emails:send", payload)
	payload[0] = 'X'

	var decoded struct {
		ID int `json:"id"`
	}
	if err := message.Bind(&decoded); err != nil {
		t.Fatalf("bind message: %v", err)
	}
	if decoded.ID != 7 {
		t.Fatalf("bound id = %d, want 7", decoded.ID)
	}

	returned := message.PayloadBytes()
	returned[0] = 'Y'
	if got := string(message.PayloadBytes()); got != `{"id":7}` {
		t.Fatalf("message payload = %s, want isolated original", got)
	}
}

// TestMessageWorkflowRoundTrip preserves metadata and payload while crossing the private engine boundary.
func TestMessageWorkflowRoundTrip(t *testing.T) {
	message := NewMessage("reports:build", []byte(`{"month":7}`))
	message.SchemaVersion = 1
	message.DispatchID = "dsp_1"
	message.JobID = "job_1"
	message.ChainID = "chn_1"
	message.BatchID = "bat_1"
	message.Attempt = 3

	roundTrip := messageFromWorkflow(messageToWorkflow(message))
	if !reflect.DeepEqual(roundTrip, message) {
		t.Fatalf("message round trip = %+v, want %+v", roundTrip, message)
	}
}

// TestStoredJobJSONV1 pins the root-owned model to the established workflow wire representation.
func TestStoredJobJSONV1(t *testing.T) {
	encoded, err := json.Marshal(StoredJob{
		Type:    "reports:build",
		Payload: []byte(`{"month":7}`),
		Options: StoredJobOptions{
			Queue:     "critical",
			Delay:     2 * time.Second,
			Timeout:   3 * time.Second,
			Retry:     4,
			Backoff:   500 * time.Millisecond,
			UniqueFor: 30 * time.Second,
		},
	})
	if err != nil {
		t.Fatalf("marshal stored job: %v", err)
	}
	want := `{"type":"reports:build","payload":"eyJtb250aCI6N30=","options":{"Queue":"critical","Delay":2000000000,"Timeout":3000000000,"Retry":4,"Backoff":500000000,"UniqueFor":30000000000}}`
	if got := string(encoded); got != want {
		t.Fatalf("stored job JSON = %s, want %s", got, want)
	}
}

// TestWorkflowMiddlewareAdapterPreservesMessageReplacement proves public middleware remains free to replace message values.
func TestWorkflowMiddlewareAdapterPreservesMessageReplacement(t *testing.T) {
	adapter := workflowMiddlewareAdapter{middleware: MiddlewareFunc(func(ctx context.Context, message Message, next Next) error {
		replacement := NewMessage("replacement:type", []byte(`{"replacement":true}`))
		replacement.SchemaVersion = message.SchemaVersion
		replacement.DispatchID = message.DispatchID
		replacement.JobID = message.JobID
		replacement.ChainID = message.ChainID
		replacement.BatchID = message.BatchID
		replacement.Attempt = message.Attempt + 1
		return next(ctx, replacement)
	})}

	input := workflow.NewContext(1, "dsp_1", "job_1", "chn_1", "bat_1", 2, "original:type", []byte(`{"original":true}`))
	var received workflow.Context
	if err := adapter.Handle(context.Background(), input, func(_ context.Context, message workflow.Context) error {
		received = message
		return nil
	}); err != nil {
		t.Fatalf("run middleware adapter: %v", err)
	}

	if received.JobType != "replacement:type" || received.Attempt != 3 {
		t.Fatalf("received metadata = %+v, want replacement type and incremented attempt", received)
	}
	if got := string(received.PayloadBytes()); got != `{"replacement":true}` {
		t.Fatalf("received payload = %s, want replacement payload", got)
	}
}
