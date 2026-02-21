package queue

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

type sqsWorkerClientStub struct {
	sendInputs   []*sqs.SendMessageInput
	deleteInputs []*sqs.DeleteMessageInput
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
	return &sqs.DeleteMessageOutput{}, nil
}

func (s *sqsWorkerClientStub) SendMessage(_ context.Context, params *sqs.SendMessageInput, _ ...func(*sqs.Options)) (*sqs.SendMessageOutput, error) {
	s.sendInputs = append(s.sendInputs, params)
	return &sqs.SendMessageOutput{}, nil
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

func TestSQSWorker_ProcessFutureMessageRepublishesAndDeletes(t *testing.T) {
	stub := &sqsWorkerClientStub{}
	w := &sqsWorker{
		handlers: map[string]Handler{},
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

func TestSQSWorker_ProcessSuccessInvokesHandlerAndDeletes(t *testing.T) {
	stub := &sqsWorkerClientStub{}
	called := 0
	w := &sqsWorker{
		handlers: map[string]Handler{
			"job:ok": func(ctx context.Context, task Task) error {
				called++
				if _, ok := ctx.Deadline(); !ok {
					t.Fatal("expected timeout context")
				}
				opts := task.enqueueOptions()
				if task.Type != "job:ok" || opts.queueName != "critical" || opts.attempt != 1 {
					t.Fatalf("unexpected task values: type=%q queue=%q attempt=%d", task.Type, opts.queueName, opts.attempt)
				}
				if opts.maxRetry == nil || *opts.maxRetry != 3 {
					t.Fatalf("expected max retry 3, got %+v", opts.maxRetry)
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
}

func TestSQSWorker_ProcessFailureRetryAndTerminal(t *testing.T) {
	t.Run("retry republish", func(t *testing.T) {
		stub := &sqsWorkerClientStub{}
		w := &sqsWorker{
			handlers: map[string]Handler{
				"job:retry": func(context.Context, Task) error { return errors.New("boom") },
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

	t.Run("terminal no republish", func(t *testing.T) {
		stub := &sqsWorkerClientStub{}
		w := &sqsWorker{
			handlers: map[string]Handler{
				"job:terminal": func(context.Context, Task) error { return errors.New("boom") },
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

func TestSQSWorker_NewRegisterAndShutdown(t *testing.T) {
	backend := newSQSWorker(sqsWorkerConfig{}).(*sqsWorker)
	if backend.cfg.DefaultQueue != "default" {
		t.Fatalf("expected default queue fallback, got %q", backend.cfg.DefaultQueue)
	}

	backend.Register("", func(context.Context, Task) error { return nil })
	backend.Register("job:nil", nil)
	if len(backend.handlers) != 0 {
		t.Fatalf("expected empty handlers for ignored registrations, got %d", len(backend.handlers))
	}
	backend.Register("job:ok", func(context.Context, Task) error { return nil })
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

func TestSQSWorker_DeleteIgnoresNilReceiptHandle(t *testing.T) {
	stub := &sqsWorkerClientStub{}
	w := &sqsWorker{
		client:   stub,
		queueURL: "https://example.local/queue/default",
	}
	w.delete(context.Background(), sqstypes.Message{})
	if len(stub.deleteInputs) != 0 {
		t.Fatalf("expected no delete call for nil receipt handle, got %d", len(stub.deleteInputs))
	}
}
