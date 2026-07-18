package sqsqueue

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/goforj/queue"
	"github.com/goforj/queue/busruntime"
	"github.com/goforj/queue/queuecore"
)

type sqsWorkerClientStub struct {
	sendInputs   []*sqs.SendMessageInput
	deleteInputs []*sqs.DeleteMessageInput
	sendErr      error
	deleteErr    error
	sendNil      bool
	sendEmptyID  bool
}

func (s *sqsWorkerClientStub) GetQueueUrl(context.Context, *sqs.GetQueueUrlInput, ...func(*sqs.Options)) (*sqs.GetQueueUrlOutput, error) {
	return nil, errors.New("not implemented")
}

func (s *sqsWorkerClientStub) CreateQueue(context.Context, *sqs.CreateQueueInput, ...func(*sqs.Options)) (*sqs.CreateQueueOutput, error) {
	return nil, errors.New("not implemented")
}

func (s *sqsWorkerClientStub) ReceiveMessage(context.Context, *sqs.ReceiveMessageInput, ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error) {
	return nil, errors.New("not implemented")
}

func (s *sqsWorkerClientStub) DeleteMessage(_ context.Context, params *sqs.DeleteMessageInput, _ ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error) {
	s.deleteInputs = append(s.deleteInputs, params)
	return &sqs.DeleteMessageOutput{}, s.deleteErr
}

// TestSQSWorkerDeleteFailureEmitsSettlementEvent verifies delete ambiguity is visible and retains logical correlation.
func TestSQSWorkerDeleteFailureEmitsSettlementEvent(t *testing.T) {
	deleteErr := errors.New("delete response lost")
	stub := &sqsWorkerClientStub{deleteErr: deleteErr}
	var events []queue.Event
	committed := false
	w := &sqsWorker{
		handlers: map[string]queue.Handler{"bus:job": func(ctx context.Context, _ queue.Job) error {
			if !busruntime.DeferUntilDeliveryCommitted(ctx, func() { committed = true }) {
				t.Fatal("handler context did not carry a settlement boundary")
			}
			return nil
		}},
		client:   stub,
		queueURL: "https://example.local/queue/default",
		observer: queue.ObserverFunc(func(_ context.Context, event queue.Event) { events = append(events, event) }),
	}
	payload := []byte(`{"schema_version":1,"dispatch_id":"dsp_sqs_settle","job_id":"job_sqs_settle","job":{"type":"reports:build","payload":"eyJpZCI6MX0="}}`)
	body, err := json.Marshal(sqsMessage{Type: "bus:job", Queue: "critical", Payload: payload, Attempt: 2, MaxRetry: 4})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	w.process(context.Background(), sqstypes.Message{Body: aws.String(string(body)), ReceiptHandle: aws.String("rh-settle")})
	if len(events) != 1 || events[0].Kind != queue.EventSettlementFailed || !errors.Is(events[0].Err, deleteErr) {
		t.Fatalf("settlement events = %+v, want one delete failure", events)
	}
	if events[0].Layer != queue.EventLayerWorker || events[0].JobType != "reports:build" || events[0].DispatchID != "dsp_sqs_settle" {
		t.Fatalf("settlement correlation = %+v", events[0])
	}
	if committed {
		t.Fatal("delete failure committed deferred handler success")
	}
}

// TestSQSWorkerRetrySettlementFailureUsesDeliveredAttempt verifies replacement metadata does not overwrite the unsettled receipt's correlation.
func TestSQSWorkerRetrySettlementFailureUsesDeliveredAttempt(t *testing.T) {
	stub := &sqsWorkerClientStub{deleteErr: errors.New("delete failed")}
	var events []queue.Event
	w := &sqsWorker{
		handlers: map[string]queue.Handler{"job:retry:settlement": func(context.Context, queue.Job) error {
			return errors.New("retry me")
		}},
		client:   stub,
		queueURL: "https://example.local/queue/default",
		observer: queue.ObserverFunc(func(_ context.Context, event queue.Event) { events = append(events, event) }),
	}
	body, err := json.Marshal(sqsMessage{Type: "job:retry:settlement", Queue: "critical", Attempt: 1, MaxRetry: 3})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	w.process(context.Background(), sqstypes.Message{Body: aws.String(string(body)), ReceiptHandle: aws.String("rh-retry")})
	if len(stub.sendInputs) != 1 {
		t.Fatalf("replacement sends = %d, want 1", len(stub.sendInputs))
	}
	if len(events) != 1 || events[0].Kind != queue.EventSettlementFailed || events[0].Attempt != 1 {
		t.Fatalf("settlement events = %+v, want original attempt 1", events)
	}
}

func (s *sqsWorkerClientStub) SendMessage(_ context.Context, params *sqs.SendMessageInput, _ ...func(*sqs.Options)) (*sqs.SendMessageOutput, error) {
	s.sendInputs = append(s.sendInputs, params)
	if s.sendErr != nil {
		return nil, s.sendErr
	}
	if s.sendNil {
		return nil, nil
	}
	if s.sendEmptyID {
		return &sqs.SendMessageOutput{}, nil
	}
	return &sqs.SendMessageOutput{MessageId: aws.String("msg-1")}, nil
}

func decodeSQSBody(t *testing.T, input *sqs.SendMessageInput) sqsMessage {
	t.Helper()
	if input == nil || input.MessageBody == nil {
		t.Fatal("expected send message input with body")
	}
	var out sqsMessage
	if err := json.Unmarshal([]byte(aws.ToString(input.MessageBody)), &out); err != nil {
		t.Fatalf("unmarshal send message body: %v", err)
	}
	return out
}

// TestSQSSendAcceptedRequiresMessageID verifies only a service receipt crosses the publish boundary.
func TestSQSSendAcceptedRequiresMessageID(t *testing.T) {
	tests := []struct {
		name    string
		output  *sqs.SendMessageOutput
		wantErr bool
	}{
		{name: "nil output", wantErr: true},
		{name: "missing id", output: &sqs.SendMessageOutput{}, wantErr: true},
		{name: "blank id", output: &sqs.SendMessageOutput{MessageId: aws.String(" ")}, wantErr: true},
		{name: "accepted", output: &sqs.SendMessageOutput{MessageId: aws.String("msg-1")}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := sqsSendAccepted(test.output)
			if (err != nil) != test.wantErr {
				t.Fatalf("sqsSendAccepted() error = %v, wantErr %t", err, test.wantErr)
			}
		})
	}
}

func TestSQSWorker_ProcessFutureMessageRepublishesAndDeletes(t *testing.T) {
	stub := &sqsWorkerClientStub{}
	w := &sqsWorker{
		handlers: map[string]queue.Handler{},
		client:   stub,
		queueURL: "https://example.local/queue/default",
	}

	body, err := json.Marshal(sqsMessage{
		Type:          "job:future",
		Queue:         "default",
		AvailableAtMS: time.Now().Add(2 * time.Second).UnixMilli(),
	})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	w.process(context.Background(), sqstypes.Message{
		Body:          aws.String(string(body)),
		ReceiptHandle: aws.String("rh-1"),
	})

	if len(stub.sendInputs) != 1 {
		t.Fatalf("expected one republish, got %d", len(stub.sendInputs))
	}
	if len(stub.deleteInputs) != 1 {
		t.Fatalf("expected one delete, got %d", len(stub.deleteInputs))
	}
	if got := decodeSQSBody(t, stub.sendInputs[0]); got.Type != "job:future" {
		t.Fatalf("expected republish type job:future, got %q", got.Type)
	}
}

func TestSQSWorker_ProcessFutureMessageRepublishFailureDoesNotDelete(t *testing.T) {
	stub := &sqsWorkerClientStub{sendErr: errors.New("send failed")}
	w := &sqsWorker{
		handlers: map[string]queue.Handler{},
		client:   stub,
		queueURL: "https://example.local/queue/default",
	}

	body, err := json.Marshal(sqsMessage{
		Type:          "job:future",
		Queue:         "default",
		AvailableAtMS: time.Now().Add(2 * time.Second).UnixMilli(),
	})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	w.process(context.Background(), sqstypes.Message{
		Body:          aws.String(string(body)),
		ReceiptHandle: aws.String("rh-1"),
	})

	if len(stub.sendInputs) != 1 {
		t.Fatalf("expected one republish attempt, got %d", len(stub.sendInputs))
	}
	if len(stub.deleteInputs) != 0 {
		t.Fatalf("expected no delete when republish fails, got %d", len(stub.deleteInputs))
	}
}

// TestSQSWorkerMissingSendReceiptDoesNotDelete verifies an ambiguous replacement send leaves the original redeliverable.
func TestSQSWorkerMissingSendReceiptDoesNotDelete(t *testing.T) {
	tests := []struct {
		name string
		stub *sqsWorkerClientStub
	}{
		{name: "nil output", stub: &sqsWorkerClientStub{sendNil: true}},
		{name: "empty message id", stub: &sqsWorkerClientStub{sendEmptyID: true}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			w := &sqsWorker{handlers: map[string]queue.Handler{}, client: test.stub, queueURL: "https://example.local/queue/default"}
			body, err := json.Marshal(sqsMessage{
				Type:          "job:future",
				Queue:         "default",
				AvailableAtMS: time.Now().Add(2 * time.Second).UnixMilli(),
			})
			if err != nil {
				t.Fatalf("marshal body: %v", err)
			}
			w.process(context.Background(), sqstypes.Message{Body: aws.String(string(body)), ReceiptHandle: aws.String("rh-1")})
			if len(test.stub.sendInputs) != 1 || len(test.stub.deleteInputs) != 0 {
				t.Fatalf("send/delete calls = %d/%d, want 1/0", len(test.stub.sendInputs), len(test.stub.deleteInputs))
			}
		})
	}
}

func TestSQSWorker_RepublishFailureEmitsObserverEvent(t *testing.T) {
	stub := &sqsWorkerClientStub{sendErr: errors.New("send failed")}
	var events []queue.Event
	w := &sqsWorker{
		handlers: map[string]queue.Handler{},
		client:   stub,
		queueURL: "https://example.local/queue/default",
		observer: queue.ObserverFunc(func(_ context.Context, e queue.Event) { events = append(events, e) }),
	}

	body, err := json.Marshal(sqsMessage{
		Type:          "job:future",
		Queue:         "critical",
		AvailableAtMS: time.Now().Add(2 * time.Second).UnixMilli(),
	})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	w.process(context.Background(), sqstypes.Message{Body: aws.String(string(body)), ReceiptHandle: aws.String("rh-obs")})

	if len(events) == 0 || events[0].Kind != queue.EventRepublishFailed || events[0].Driver != queue.DriverSQS || events[0].Queue != "critical" {
		t.Fatalf("expected republish_failed event for sqs, got %+v", events)
	}
	if events[0].Layer != queue.EventLayerWorker {
		t.Fatalf("republish_failed layer = %q, want worker", events[0].Layer)
	}
}

func TestSQSWorker_RepublishFailureUnwrapsBusEnvelopeJobType(t *testing.T) {
	stub := &sqsWorkerClientStub{sendErr: errors.New("send failed")}
	var events []queue.Event
	w := &sqsWorker{
		handlers: map[string]queue.Handler{},
		client:   stub,
		queueURL: "https://example.local/queue/default",
		observer: queue.ObserverFunc(func(_ context.Context, e queue.Event) { events = append(events, e) }),
	}

	body, err := json.Marshal(sqsMessage{
		Type:          "bus:job",
		Queue:         "critical",
		AvailableAtMS: time.Now().Add(2 * time.Second).UnixMilli(),
		Payload:       []byte(`{"schema_version":1,"dispatch_id":"dsp_sqs","job_id":"job_sqs","batch_id":"bat_sqs","job":{"type":"monitoring:check"}}`),
	})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	w.process(context.Background(), sqstypes.Message{Body: aws.String(string(body)), ReceiptHandle: aws.String("rh-obs")})

	if len(events) == 0 {
		t.Fatal("expected republish failure event for sqs")
	}
	if events[0].JobType != "monitoring:check" {
		t.Fatalf("expected unwrapped observed job type, got %q", events[0].JobType)
	}
	if events[0].DispatchID != "dsp_sqs" || events[0].JobID != "job_sqs" || events[0].BatchID != "bat_sqs" {
		t.Fatalf("expected correlated sqs event, got %+v", events[0])
	}
}

func TestSQSWorker_ProcessSuccessInvokesHandlerAndDeletes(t *testing.T) {
	stub := &sqsWorkerClientStub{}
	called := 0
	committed := false
	w := &sqsWorker{
		handlers: map[string]queue.Handler{
			"job:ok": func(ctx context.Context, job queue.Job) error {
				called++
				if _, ok := ctx.Deadline(); !ok {
					t.Fatal("expected timeout context")
				}
				opts := queuecore.DriverOptions(job)
				if job.Type != "job:ok" || opts.QueueName != "critical" || opts.Attempt != 1 {
					t.Fatalf("unexpected job values: type=%q queue=%q attempt=%d", job.Type, opts.QueueName, opts.Attempt)
				}
				if opts.MaxRetry == nil || *opts.MaxRetry != 3 {
					t.Fatalf("expected max retry 3, got %+v", opts.MaxRetry)
				}
				if !busruntime.DeferUntilDeliveryCommitted(ctx, func() { committed = true }) {
					t.Fatal("handler context did not carry a settlement boundary")
				}
				return nil
			},
		},
		client:   stub,
		queueURL: "https://example.local/queue/default",
	}

	body, err := json.Marshal(sqsMessage{
		Type:          "job:ok",
		Queue:         "critical",
		Payload:       []byte(`{"k":"v"}`),
		Attempt:       1,
		MaxRetry:      3,
		TimeoutMillis: 25,
	})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	w.process(context.Background(), sqstypes.Message{
		Body:          aws.String(string(body)),
		ReceiptHandle: aws.String("rh-2"),
	})

	if called != 1 {
		t.Fatalf("expected handler called once, got %d", called)
	}
	if len(stub.sendInputs) != 0 {
		t.Fatalf("expected no republish on success, got %d", len(stub.sendInputs))
	}
	if len(stub.deleteInputs) != 1 {
		t.Fatalf("expected one delete on success, got %d", len(stub.deleteInputs))
	}
	if !committed {
		t.Fatal("successful delete did not commit deferred handler success")
	}
}

func TestSQSWorker_ProcessFailureRetryAndTerminal(t *testing.T) {
	t.Run("retry republish", func(t *testing.T) {
		stub := &sqsWorkerClientStub{}
		w := &sqsWorker{
			handlers: map[string]queue.Handler{
				"job:retry": func(context.Context, queue.Job) error { return errors.New("boom") },
			},
			client:   stub,
			queueURL: "https://example.local/queue/default",
		}

		body, err := json.Marshal(sqsMessage{
			Type:          "job:retry",
			Queue:         "default",
			Attempt:       0,
			MaxRetry:      2,
			BackoffMillis: (2 * time.Second).Milliseconds(),
		})
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		w.process(context.Background(), sqstypes.Message{Body: aws.String(string(body)), ReceiptHandle: aws.String("rh-r")})

		if len(stub.sendInputs) != 1 {
			t.Fatalf("expected one republish, got %d", len(stub.sendInputs))
		}
		if len(stub.deleteInputs) != 1 {
			t.Fatalf("expected one delete, got %d", len(stub.deleteInputs))
		}
		got := decodeSQSBody(t, stub.sendInputs[0])
		if got.Attempt != 1 {
			t.Fatalf("expected incremented attempt=1, got %d", got.Attempt)
		}
		if stub.sendInputs[0].DelaySeconds <= 0 || stub.sendInputs[0].DelaySeconds > 900 {
			t.Fatalf("expected bounded delay seconds in (0,900], got %d", stub.sendInputs[0].DelaySeconds)
		}
	})

	t.Run("retry republish failure does not delete", func(t *testing.T) {
		stub := &sqsWorkerClientStub{sendErr: errors.New("send failed")}
		w := &sqsWorker{
			handlers: map[string]queue.Handler{
				"job:retry": func(context.Context, queue.Job) error { return errors.New("boom") },
			},
			client:   stub,
			queueURL: "https://example.local/queue/default",
		}

		body, err := json.Marshal(sqsMessage{
			Type:          "job:retry",
			Queue:         "default",
			Attempt:       0,
			MaxRetry:      2,
			BackoffMillis: (2 * time.Second).Milliseconds(),
		})
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		w.process(context.Background(), sqstypes.Message{Body: aws.String(string(body)), ReceiptHandle: aws.String("rh-rf")})

		if len(stub.sendInputs) != 1 {
			t.Fatalf("expected one republish attempt, got %d", len(stub.sendInputs))
		}
		if len(stub.deleteInputs) != 0 {
			t.Fatalf("expected no delete when retry republish fails, got %d", len(stub.deleteInputs))
		}
	})

	t.Run("terminal no republish", func(t *testing.T) {
		stub := &sqsWorkerClientStub{}
		w := &sqsWorker{
			handlers: map[string]queue.Handler{
				"job:terminal": func(context.Context, queue.Job) error { return errors.New("boom") },
			},
			client:   stub,
			queueURL: "https://example.local/queue/default",
		}

		body, err := json.Marshal(sqsMessage{
			Type:     "job:terminal",
			Queue:    "default",
			Attempt:  2,
			MaxRetry: 2,
		})
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		w.process(context.Background(), sqstypes.Message{Body: aws.String(string(body)), ReceiptHandle: aws.String("rh-t")})

		if len(stub.sendInputs) != 0 {
			t.Fatalf("expected no republish on terminal retry, got %d", len(stub.sendInputs))
		}
		if len(stub.deleteInputs) != 1 {
			t.Fatalf("expected one delete on terminal retry, got %d", len(stub.deleteInputs))
		}
	})
}

// TestSQSWorker_AttemptDecisionSettlement verifies terminal work is deleted while uncommitted work remains available for redelivery.
func TestSQSWorker_AttemptDecisionSettlement(t *testing.T) {
	t.Run("permanent failure deletes without republishing", func(t *testing.T) {
		stub := &sqsWorkerClientStub{}
		w := &sqsWorker{
			handlers: map[string]queue.Handler{
				"job:permanent": func(ctx context.Context, _ queue.Job) error {
					attempt, ok := busruntime.DeliveryAttemptFromContext(ctx)
					if !ok || attempt.Number != 0 || attempt.MaxRetry != 3 {
						t.Fatalf("unexpected delivery attempt: %+v, present=%t", attempt, ok)
					}
					return busruntime.Permanent(errors.New("invalid job"))
				},
			},
			client:   stub,
			queueURL: "https://example.local/queue/default",
		}
		body, err := json.Marshal(sqsMessage{Type: "job:permanent", Queue: "default", MaxRetry: 3})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}

		w.process(context.Background(), sqstypes.Message{Body: aws.String(string(body)), ReceiptHandle: aws.String("rh-permanent")})

		if len(stub.sendInputs) != 0 {
			t.Fatalf("permanent failure must not republish, got %d sends", len(stub.sendInputs))
		}
		if len(stub.deleteInputs) != 1 {
			t.Fatalf("permanent failure must delete its receipt, got %d deletes", len(stub.deleteInputs))
		}
	})

	t.Run("uncommitted failure leaves the original receipt", func(t *testing.T) {
		stub := &sqsWorkerClientStub{}
		w := &sqsWorker{
			handlers: map[string]queue.Handler{
				"job:uncommitted": func(ctx context.Context, _ queue.Job) error {
					attempt, ok := busruntime.DeliveryAttemptFromContext(ctx)
					if !ok || attempt.Number != 1 || attempt.MaxRetry != 4 {
						t.Fatalf("unexpected delivery attempt: %+v, present=%t", attempt, ok)
					}
					return busruntime.Uncommitted(errors.New("store unavailable"))
				},
			},
			client:   stub,
			queueURL: "https://example.local/queue/default",
		}
		body, err := json.Marshal(sqsMessage{
			Type:          "job:uncommitted",
			Queue:         "default",
			Attempt:       1,
			MaxRetry:      4,
			BackoffMillis: 1_000,
		})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}

		w.process(context.Background(), sqstypes.Message{Body: aws.String(string(body)), ReceiptHandle: aws.String("rh-uncommitted")})

		if len(stub.sendInputs) != 0 || len(stub.deleteInputs) != 0 {
			t.Fatalf("uncommitted failure must await SQS redelivery, got sends=%d deletes=%d", len(stub.sendInputs), len(stub.deleteInputs))
		}
	})
}

func TestSQSWorker_NewRegisterAndShutdown(t *testing.T) {
	backend := newSQSWorker(sqsWorkerConfig{})
	if backend.cfg.DefaultQueue != "default" {
		t.Fatalf("expected default queue fallback, got %q", backend.cfg.DefaultQueue)
	}

	backend.Register("", func(context.Context, queue.Job) error { return nil })
	backend.Register("job:nil", nil)
	if len(backend.handlers) != 0 {
		t.Fatalf("expected empty handlers for ignored registrations, got %d", len(backend.handlers))
	}
	backend.Register("job:ok", func(context.Context, queue.Job) error { return nil })
	if len(backend.handlers) != 1 {
		t.Fatalf("expected one handler registration, got %d", len(backend.handlers))
	}

	backend.started = true
	backend.cancel = func() {}
	if err := backend.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if backend.started {
		t.Fatal("expected shutdown to mark worker stopped")
	}
}

func TestSQSWorker_StartWorkersFastPaths(t *testing.T) {
	backend := newSQSWorker(sqsWorkerConfig{})

	backend.started = true
	if err := backend.StartWorkers(context.Background()); err != nil {
		t.Fatalf("expected started fast-path nil, got %v", err)
	}
	backend.started = false

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := backend.StartWorkers(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled, got %v", err)
	}
}

// TestSQSWorkerShutdownHonorsDeadline verifies a stuck in-flight handler cannot block the caller forever.
func TestSQSWorkerShutdownHonorsDeadline(t *testing.T) {
	w := newSQSWorker(sqsWorkerConfig{})
	w.started = true
	w.cancel = func() {}
	release := make(chan struct{})
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		<-release
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	if err := w.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown error = %v, want deadline exceeded", err)
	}
	if !w.started {
		t.Fatal("timed-out shutdown exposed the worker as restartable while work remained")
	}
	close(release)
	if err := w.Shutdown(context.Background()); err != nil {
		t.Fatalf("complete shutdown: %v", err)
	}
	if w.started {
		t.Fatal("completed shutdown retained started state")
	}
}

func TestSQSWorker_StartWorkersInvalidEndpoint(t *testing.T) {
	backend := newSQSWorker(sqsWorkerConfig{
		DefaultQueue: "default",
		SQSRegion:    "us-east-1",
		SQSEndpoint:  "://bad-endpoint",
		SQSAccessKey: "test",
		SQSSecretKey: "test",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := backend.StartWorkers(ctx); err == nil {
		t.Fatal("expected start workers error for invalid endpoint")
	}
	if backend.started {
		t.Fatal("expected worker to remain stopped after start error")
	}
}

// TestSQSWorkerDeleteRejectsNilReceiptHandle verifies missing settlement identity cannot commit handler success.
func TestSQSWorkerDeleteRejectsNilReceiptHandle(t *testing.T) {
	stub := &sqsWorkerClientStub{}
	w := &sqsWorker{
		client:   stub,
		queueURL: "https://example.local/queue/default",
	}
	if err := w.delete(sqstypes.Message{}); err == nil {
		t.Fatal("delete without receipt unexpectedly committed")
	}
	if len(stub.deleteInputs) != 0 {
		t.Fatalf("expected no delete call for nil receipt handle, got %d", len(stub.deleteInputs))
	}
}
