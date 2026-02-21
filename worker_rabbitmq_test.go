package queue

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

type ackRecorder struct {
	acks  int
	nacks int
}

func (a *ackRecorder) Ack(_ uint64, _ bool) error {
	a.acks++
	return nil
}

func (a *ackRecorder) Nack(_ uint64, _ bool, _ bool) error {
	a.nacks++
	return nil
}

func (a *ackRecorder) Reject(_ uint64, _ bool) error { return nil }

func TestRabbitMQWorker_NewRegisterAndShutdown(t *testing.T) {
	w := newRabbitMQWorker(rabbitMQWorkerConfig{}).(*rabbitMQWorker)
	if w.cfg.DefaultQueue != "default" {
		t.Fatalf("expected default queue fallback, got %q", w.cfg.DefaultQueue)
	}

	w.Register("", func(context.Context, Task) error { return nil })
	w.Register("job:nil", nil)
	if len(w.handlers) != 0 {
		t.Fatalf("expected ignored registrations, got %d", len(w.handlers))
	}
	w.Register("job:ok", func(context.Context, Task) error { return nil })
	if len(w.handlers) != 1 {
		t.Fatalf("expected one handler, got %d", len(w.handlers))
	}

	if err := w.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown no-op failed: %v", err)
	}
}

func TestRabbitMQWorker_StartWorkersFastPaths(t *testing.T) {
	w := newRabbitMQWorker(rabbitMQWorkerConfig{}).(*rabbitMQWorker)
	w.started = true
	if err := w.StartWorkers(context.Background()); err != nil {
		t.Fatalf("expected started fast-path nil, got %v", err)
	}
	w.started = false

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := w.StartWorkers(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled, got %v", err)
	}
}

func TestRabbitMQWorker_StartWorkersNilContextDialFailure(t *testing.T) {
	w := &rabbitMQWorker{
		cfg:      rabbitMQWorkerConfig{RabbitMQURL: "://bad-url"},
		handlers: map[string]Handler{},
	}
	if err := w.StartWorkers(nil); err == nil {
		t.Fatal("expected dial failure for invalid url")
	}
}

func TestRabbitMQWorker_ProcessDeliveryBranches(t *testing.T) {
	t.Run("invalid json ack", func(t *testing.T) {
		acks := &ackRecorder{}
		w := &rabbitMQWorker{handlers: map[string]Handler{}}
		w.processDelivery(context.Background(), amqp.Delivery{Body: []byte("{"), Acknowledger: acks, DeliveryTag: 1})
		if acks.acks != 1 || acks.nacks != 0 {
			t.Fatalf("expected ack once, nack never; got ack=%d nack=%d", acks.acks, acks.nacks)
		}
	})

	t.Run("missing handler ack", func(t *testing.T) {
		acks := &ackRecorder{}
		w := &rabbitMQWorker{handlers: map[string]Handler{}}
		body, err := json.Marshal(rabbitMQMessage{Type: "job:none", Queue: "default"})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		w.processDelivery(context.Background(), amqp.Delivery{Body: body, Acknowledger: acks, DeliveryTag: 2})
		if acks.acks != 1 {
			t.Fatalf("expected ack once, got %d", acks.acks)
		}
	})

	t.Run("success handler ack", func(t *testing.T) {
		acks := &ackRecorder{}
		called := 0
		w := &rabbitMQWorker{handlers: map[string]Handler{
			"job:ok": func(ctx context.Context, task Task) error {
				called++
				if _, ok := ctx.Deadline(); !ok {
					t.Fatal("expected timeout context")
				}
				opts := task.enqueueOptions()
				if task.Type != "job:ok" || opts.queueName != "critical" || opts.attempt != 1 {
					t.Fatalf("unexpected task fields: type=%q queue=%q attempt=%d", task.Type, opts.queueName, opts.attempt)
				}
				return nil
			},
		}}
		body, err := json.Marshal(rabbitMQMessage{Type: "job:ok", Queue: "critical", Attempt: 1, MaxRetry: 3, TimeoutMillis: 20})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		w.processDelivery(context.Background(), amqp.Delivery{Body: body, Acknowledger: acks, DeliveryTag: 3})
		if called != 1 || acks.acks != 1 {
			t.Fatalf("expected handler once and ack once, got called=%d ack=%d", called, acks.acks)
		}
	})

	t.Run("future delivery publish path with nil channel still acks", func(t *testing.T) {
		acks := &ackRecorder{}
		w := &rabbitMQWorker{handlers: map[string]Handler{}, cfg: rabbitMQWorkerConfig{DefaultQueue: "default"}}
		body, err := json.Marshal(rabbitMQMessage{Type: "job:future", Queue: "default", AvailableAtMS: time.Now().Add(2 * time.Second).UnixMilli()})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		w.processDelivery(context.Background(), amqp.Delivery{Body: body, Acknowledger: acks, DeliveryTag: 4})
		if acks.acks != 1 || acks.nacks != 0 {
			t.Fatalf("expected ack once, got ack=%d nack=%d", acks.acks, acks.nacks)
		}
	})

	t.Run("failed handler retries then acks", func(t *testing.T) {
		acks := &ackRecorder{}
		w := &rabbitMQWorker{
			handlers: map[string]Handler{
				"job:retry": func(context.Context, Task) error { return errors.New("boom") },
			},
			cfg: rabbitMQWorkerConfig{DefaultQueue: "default"},
		}
		body, err := json.Marshal(rabbitMQMessage{Type: "job:retry", Queue: "default", Attempt: 0, MaxRetry: 2, BackoffMillis: 10})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		w.processDelivery(context.Background(), amqp.Delivery{Body: body, Acknowledger: acks, DeliveryTag: 5})
		if acks.acks != 1 || acks.nacks != 0 {
			t.Fatalf("expected ack once, got ack=%d nack=%d", acks.acks, acks.nacks)
		}
	})

	t.Run("failed handler terminal acks", func(t *testing.T) {
		acks := &ackRecorder{}
		w := &rabbitMQWorker{
			handlers: map[string]Handler{
				"job:terminal": func(context.Context, Task) error { return errors.New("boom") },
			},
			cfg: rabbitMQWorkerConfig{DefaultQueue: "default"},
		}
		body, err := json.Marshal(rabbitMQMessage{Type: "job:terminal", Queue: "default", Attempt: 2, MaxRetry: 2})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		w.processDelivery(context.Background(), amqp.Delivery{Body: body, Acknowledger: acks, DeliveryTag: 6})
		if acks.acks != 1 {
			t.Fatalf("expected ack once, got %d", acks.acks)
		}
	})
}

func TestRabbitMQWorker_PublishNilChannelAndImmediateDelay(t *testing.T) {
	w := &rabbitMQWorker{cfg: rabbitMQWorkerConfig{DefaultQueue: ""}}
	if err := w.publish(rabbitMQMessage{Type: "job:nilch", Queue: "default"}); err != nil {
		t.Fatalf("publish with nil channel should be noop, got %v", err)
	}
	if err := w.publish(rabbitMQMessage{
		Type:          "job:past",
		Queue:         "default",
		AvailableAtMS: time.Now().Add(-10 * time.Millisecond).UnixMilli(),
	}); err != nil {
		t.Fatalf("publish past delay with nil channel should be noop, got %v", err)
	}
}
