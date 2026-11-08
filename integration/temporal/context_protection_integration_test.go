//go:build integration

package temporal

import (
	"bytes"
	"context"
	"crypto/cipher"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goforj/queue"
	"github.com/goforj/queue/driver/temporalqueue"
	"github.com/goforj/queue/queueconfig"
	"go.temporal.io/api/common/v1"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/taskqueue/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
)

// contextFixtureKeys models retained decoding keys and a temporary key-service outage using public synthetic keys.
type contextFixtureKeys struct {
	keys        map[byte]cipher.AEAD
	active      atomic.Uint32
	unavailable atomic.Bool
}

// contextFixtureCodec authenticates namespace and workflow identity without placing them in payload metadata.
type contextFixtureCodec struct {
	keys *contextFixtureKeys
	aad  []byte
}

// Encode bounds every plaintext before authenticated encoding with the current fixture key.
func (c *contextFixtureCodec) Encode(payloads []*common.Payload) ([]*common.Payload, error) {
	if c.keys.unavailable.Load() {
		return nil, temporalqueue.ErrAdapterUnavailable
	}
	if len(c.aad) == 0 || len(payloads) > 4 {
		return nil, errors.New("context fixture input rejected")
	}
	result := make([]*common.Payload, len(payloads))
	keyID := byte(c.keys.active.Load())
	aead := c.keys.keys[keyID]
	for i, payload := range payloads {
		if payload == nil || proto.Size(payload) > protectedTestLimit {
			return nil, errors.New("context fixture bytes rejected")
		}
		raw, err := proto.Marshal(payload)
		if err != nil {
			return nil, err
		}
		result[i] = &common.Payload{Metadata: map[string][]byte{"encoding": []byte("queue-test/context-aead"), "key": {keyID}}, Data: aead.Seal(nil, nil, raw, c.aad)}
	}
	return result, nil
}

// Decode authenticates caller context and enforces expanded-byte bounds before protobuf parsing.
func (c *contextFixtureCodec) Decode(payloads []*common.Payload) ([]*common.Payload, error) {
	if c.keys.unavailable.Load() {
		return nil, temporalqueue.ErrAdapterUnavailable
	}
	if len(c.aad) == 0 || len(payloads) > 4 {
		return nil, errors.New("context fixture input rejected")
	}
	result := make([]*common.Payload, len(payloads))
	for i, payload := range payloads {
		if payload == nil || string(payload.Metadata["encoding"]) != "queue-test/context-aead" || len(payload.Metadata["key"]) != 1 || proto.Size(payload) > protectedTestLimit+256 {
			return nil, errors.New("context fixture protection rejected")
		}
		aead, ok := c.keys.keys[payload.Metadata["key"][0]]
		if !ok {
			return nil, errors.New("context fixture key rejected")
		}
		raw, err := aead.Open(nil, nil, payload.Data, c.aad)
		if err != nil || len(raw) > protectedTestLimit {
			return nil, errors.New("context fixture authentication rejected")
		}
		result[i] = &common.Payload{}
		if err := proto.Unmarshal(raw, result[i]); err != nil {
			return nil, errors.New("context fixture plaintext rejected")
		}
	}
	return result, nil
}

// contextFixtureConverter retains authoritative SDK serialization context in every bounded converter copy.
type contextFixtureConverter struct {
	converter.DataConverter
	keys *contextFixtureKeys
}

// DecodingLimits declares bounds independently enforced by the fixture codec.
func (*contextFixtureConverter) DecodingLimits() temporalqueue.SerializationLimits {
	return temporalqueue.SerializationLimits{MaxPayloads: 4, MaxPayloadBytes: 64 << 10, MaxTotalBytes: 256 << 10}
}

// MaxEncodedOverhead reserves protobuf metadata, nonce and authentication tag bytes.
func (*contextFixtureConverter) MaxEncodedOverhead() int { return 256 }

// ValidateProtectedPayload rejects absent or plaintext native failure attributes before decoding.
func (c *contextFixtureConverter) ValidateProtectedPayload(payload *common.Payload) error {
	if c.keys.unavailable.Load() {
		return temporalqueue.ErrAdapterUnavailable
	}
	if payload == nil || string(payload.Metadata["encoding"]) != "queue-test/context-aead" || proto.Size(payload) > protectedTestLimit+256 {
		return errors.New("context fixture protection rejected")
	}
	return nil
}

// WithSerializationContext binds both native and portable SDK paths to the same namespace and workflow authority.
func (c *contextFixtureConverter) WithSerializationContext(ctx converter.SerializationContext) converter.DataConverter {
	var namespace, id string
	switch ctx := ctx.(type) {
	case converter.WorkflowSerializationContext:
		namespace, id = ctx.Namespace, ctx.WorkflowID
	case converter.ActivitySerializationContext:
		namespace, id = ctx.Namespace, ctx.WorkflowID
	}
	var aad []byte
	if namespace != "" && id != "" {
		aad = []byte(namespace + "\x00" + id)
	}
	return &contextFixtureConverter{keys: c.keys, DataConverter: converter.NewCodecDataConverter(converter.GetDefaultDataConverter(), &contextFixtureCodec{keys: c.keys, aad: aad})}
}

// contextEchoWorkflow retains native typed activity and workflow results under the configured authenticated converter.
func contextEchoWorkflow(ctx workflow.Context, input string) (string, error) {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 5 * time.Second})
	var value string
	err := workflow.ExecuteActivity(ctx, "context-echo", input).Get(ctx, &value)
	return value, err
}

// TestTemporalDriverContextBoundProtection qualifies authenticated context, rotation, availability and native pre-entry rejection.
func TestTemporalDriverContextBoundProtection(t *testing.T) {
	requireTemporalServer(t)
	keys := &contextFixtureKeys{keys: map[byte]cipher.AEAD{1: newProtectedTestCodec(t, 1).aead, 2: newProtectedTestCodec(t, 2).aead}}
	keys.active.Store(1)
	dc := &contextFixtureConverter{keys: keys}
	dc.DataConverter = converter.NewCodecDataConverter(converter.GetDefaultDataConverter(), &contextFixtureCodec{keys: keys})
	failures, err := temporalqueue.NewProtectedFailureConverter(dc)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("driver-context-%d", time.Now().UnixNano())
	logger := &secretLogger{}
	var nativeCalls atomic.Int32
	q, err := temporalqueue.NewWithConfig(temporalqueue.Config{DriverBaseConfig: queueconfig.DriverBaseConfig{DefaultQueue: name}, ClientOptions: client.Options{HostPort: temporalAddress, DataConverter: dc, FailureConverter: failures, Logger: logger}, DefaultActivityTimeout: 5 * time.Second, ShutdownTimeout: time.Second, Register: func(r worker.Registry) error {
		r.RegisterWorkflow(contextEchoWorkflow)
		r.RegisterActivityWithOptions(func(_ context.Context, input string) (string, error) { nativeCalls.Add(1); return input, nil }, activity.RegisterOptions{Name: "context-echo"})
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer q.Shutdown(context.Background())
	const secret = "synthetic-context-private-value-792513"
	entered := make(chan struct{})
	release := make(chan struct{})
	q.Register("business", func(_ context.Context, m queue.Message) error {
		if string(m.PayloadBytes()) != secret {
			t.Error("context-bound business input lost")
		}
		close(entered)
		<-release
		return nil
	})
	q.Register("hook", func(_ context.Context, m queue.Message) error {
		if string(m.PayloadBytes()) != secret {
			t.Error("context-bound hook input lost")
		}
		return nil
	})
	if err := q.StartWorkers(t.Context()); err != nil {
		t.Fatal(err)
	}
	id, err := queue.Continue(q.Chain(queue.NewJob("business").Payload([]byte(secret)))).OnCompletion(queue.NewJob("hook").Payload([]byte(secret))).Dispatch(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("context-bound business never entered")
	}
	keys.active.Store(2)
	close(release)
	sdk, _ := temporalqueue.Client(q)
	if err := sdk.GetWorkflow(t.Context(), id, "").Get(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	state, err := q.FindChain(t.Context(), id)
	if err != nil || !state.Completed || !bytes.Equal(state.Nodes[0].Job.Payload, []byte(secret)) {
		t.Fatalf("rotated retained state %+v %v", state, err)
	}
	outcomes, err := q.FindContinuations(t.Context(), id)
	if err != nil || len(outcomes) != 1 || outcomes[0].Status != "succeeded" {
		t.Fatalf("rotated continuation %+v %v", outcomes, err)
	}
	keys.unavailable.Store(true)
	_, readErr := q.FindChain(t.Context(), id)
	var application *temporal.ApplicationError
	if !errors.As(readErr, &application) || application.Type() != "goforj.adapter-unavailable.v1" || application.NonRetryable() {
		t.Fatalf("key outage lost availability category: %v", readErr)
	}
	keys.unavailable.Store(false)
	if state, err := q.FindChain(t.Context(), id); err != nil || !state.Completed {
		t.Fatalf("key recovery failed %+v %v", state, err)
	}
	nativeID := name + "-native"
	run, err := sdk.ExecuteWorkflow(t.Context(), client.StartWorkflowOptions{ID: nativeID, TaskQueue: name}, contextEchoWorkflow, secret)
	if err != nil {
		t.Fatal(err)
	}
	var result string
	if err := run.Get(t.Context(), &result); err != nil || result != secret {
		t.Fatalf("context-bound native result %q %v", result, err)
	}
	replay, err := temporalqueue.NewWorkflowReplayer(q)
	if err != nil {
		t.Fatal(err)
	}
	if err := replay.ReplayWorkflowExecution(t.Context(), sdk.WorkflowService(), nil, "default", workflow.Execution{ID: id}); err != nil {
		t.Fatal("context-bound portable replay", err)
	}
	if err := replay.ReplayWorkflowExecution(t.Context(), sdk.WorkflowService(), nil, "default", workflow.Execution{ID: nativeID, RunID: run.GetRunID()}); err != nil {
		t.Fatal("context-bound native replay", err)
	}
	iterator := sdk.GetWorkflowHistory(t.Context(), nativeID, run.GetRunID(), false, enums.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
	first, err := iterator.Next()
	if err != nil {
		t.Fatal(err)
	}
	copied := first.GetWorkflowExecutionStartedEventAttributes().Input
	wrongID := nativeID + "-wrong-aad"
	_, err = sdk.WorkflowService().StartWorkflowExecution(t.Context(), &workflowservice.StartWorkflowExecutionRequest{Namespace: "default", WorkflowId: wrongID, WorkflowType: &common.WorkflowType{Name: "contextEchoWorkflow"}, TaskQueue: &taskqueue.TaskQueue{Name: name}, Input: copied, RequestId: wrongID, WorkflowExecutionTimeout: durationpb.New(10 * time.Second), RetryPolicy: &common.RetryPolicy{MaximumAttempts: 3, InitialInterval: durationpb.New(time.Second)}})
	if err != nil {
		t.Fatal(err)
	}
	err = sdk.GetWorkflow(t.Context(), wrongID, "").Get(t.Context(), nil)
	if !errors.As(err, &application) || !application.NonRetryable() {
		t.Fatalf("malformed context input did not close terminally: %v", err)
	}
	description, err := sdk.DescribeWorkflowExecution(t.Context(), wrongID, "")
	if err != nil {
		t.Fatal(err)
	}
	wrongHistory := sdk.GetWorkflowHistory(t.Context(), wrongID, description.WorkflowExecutionInfo.Execution.RunId, false, enums.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
	start, err := wrongHistory.Next()
	if err != nil {
		t.Fatal(err)
	}
	if start.GetWorkflowExecutionStartedEventAttributes().Attempt != 1 || nativeCalls.Load() != 1 {
		t.Fatalf("malformed native input reached retries or handler: attempt=%d calls=%d", start.GetWorkflowExecutionStartedEventAttributes().Attempt, nativeCalls.Load())
	}
	logger.mu.Lock()
	defer logger.mu.Unlock()
	if strings.Contains(logger.text.String(), secret) {
		t.Fatal("context protection leaked SDK diagnostics")
	}
}
