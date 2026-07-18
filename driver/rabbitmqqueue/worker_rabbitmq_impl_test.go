package rabbitmqqueue

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/goforj/queue"
	"github.com/goforj/queue/busruntime"
	"github.com/goforj/queue/queuecore"
	amqp "github.com/rabbitmq/amqp091-go"
)

type ackRecorder struct {
	acks        int
	nacks       int
	nackRequeue bool
}

func (a *ackRecorder) Ack(_ uint64, _ bool) error {
	a.acks++
	return nil
}

// Nack records whether the worker requested broker redelivery.
func (a *ackRecorder) Nack(_ uint64, _ bool, requeue bool) error {
	a.nacks++
	a.nackRequeue = requeue
	return nil
}

func (a *ackRecorder) Reject(_ uint64, _ bool) error { return nil }

func TestRabbitMQWorker_NewRegisterAndShutdown(t *testing.T) {
	w := newRabbitMQWorker(rabbitMQWorkerConfig{})
	if w.cfg.DefaultQueue != "default" {
		t.Fatalf("expected default queue fallback, got %q", w.cfg.DefaultQueue)
	}

	w.Register("", func(context.Context, queue.Job) error { return nil })
	w.Register("job:nil", nil)
	if len(w.handlers) != 0 {
		t.Fatalf("expected ignored registrations, got %d", len(w.handlers))
	}
	w.Register("job:ok", func(context.Context, queue.Job) error { return nil })
	if len(w.handlers) != 1 {
		t.Fatalf("expected one handler, got %d", len(w.handlers))
	}

	if err := w.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown no-op failed: %v", err)
	}
}

func TestRabbitMQWorker_StartWorkersFastPaths(t *testing.T) {
	w := newRabbitMQWorker(rabbitMQWorkerConfig{})
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
		cfg:      rabbitMQWorkerConfig{RabbitMQURL: "://bad-url", DialTimeout: 5 * time.Millisecond},
		handlers: map[string]queue.Handler{},
	}
	if err := w.StartWorkers(nil); err == nil {
		t.Fatal("expected dial failure for invalid url")
	}
}

func TestRabbitMQWorker_ProcessDeliveryBranches(t *testing.T) {
	t.Run("invalid json ack", func(t *testing.T) {
		acks := &ackRecorder{}
		w := &rabbitMQWorker{handlers: map[string]queue.Handler{}}
		w.processDelivery(context.Background(), amqp.Delivery{Body: []byte("{"), Acknowledger: acks, DeliveryTag: 1})
		if acks.acks != 1 || acks.nacks != 0 {
			t.Fatalf("expected ack once, nack never; got ack=%d nack=%d", acks.acks, acks.nacks)
		}
	})

	t.Run("missing handler ack", func(t *testing.T) {
		acks := &ackRecorder{}
		w := &rabbitMQWorker{handlers: map[string]queue.Handler{}}
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
		w := &rabbitMQWorker{handlers: map[string]queue.Handler{
			"job:ok": func(ctx context.Context, job queue.Job) error {
				called++
				if _, ok := ctx.Deadline(); !ok {
					t.Fatal("expected timeout context")
				}
				opts := queuecore.DriverOptions(job)
				if job.Type != "job:ok" || opts.QueueName != "critical" || opts.Attempt != 1 {
					t.Fatalf("unexpected job fields: type=%q queue=%q attempt=%d", job.Type, opts.QueueName, opts.Attempt)
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

	t.Run("future delivery publish path with nil channel nacks", func(t *testing.T) {
		acks := &ackRecorder{}
		var events []queue.Event
		w := &rabbitMQWorker{
			handlers: map[string]queue.Handler{},
			cfg:      rabbitMQWorkerConfig{DefaultQueue: "default"},
			observer: queue.ObserverFunc(func(_ context.Context, e queue.Event) { events = append(events, e) }),
		}
		body, err := json.Marshal(rabbitMQMessage{Type: "job:future", Queue: "default", AvailableAtMS: time.Now().Add(2 * time.Second).UnixMilli()})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		w.processDelivery(context.Background(), amqp.Delivery{Body: body, Acknowledger: acks, DeliveryTag: 4})
		if acks.acks != 0 || acks.nacks != 1 {
			t.Fatalf("expected nack once, got ack=%d nack=%d", acks.acks, acks.nacks)
		}
		if len(events) == 0 || events[0].Kind != queue.EventRepublishFailed || events[0].Driver != queue.DriverRabbitMQ {
			t.Fatalf("expected republish_failed rabbitmq event, got %+v", events)
		}
		if events[0].Layer != queue.EventLayerWorker {
			t.Fatalf("republish_failed layer = %q, want worker", events[0].Layer)
		}
	})

	t.Run("republish failure unwraps bus envelope job type", func(t *testing.T) {
		acks := &ackRecorder{}
		var events []queue.Event
		w := &rabbitMQWorker{
			handlers: map[string]queue.Handler{},
			cfg:      rabbitMQWorkerConfig{DefaultQueue: "default"},
			observer: queue.ObserverFunc(func(_ context.Context, e queue.Event) { events = append(events, e) }),
		}
		body, err := json.Marshal(rabbitMQMessage{
			Type:          "bus:job",
			Queue:         "default",
			AvailableAtMS: time.Now().Add(2 * time.Second).UnixMilli(),
			Payload:       []byte(`{"schema_version":1,"dispatch_id":"dsp_rabbit","job_id":"job_rabbit","batch_id":"bat_rabbit","job":{"type":"monitoring:check"}}`),
		})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		w.processDelivery(context.Background(), amqp.Delivery{Body: body, Acknowledger: acks, DeliveryTag: 44})
		if len(events) == 0 {
			t.Fatal("expected republish failure event")
		}
		if events[0].JobType != "monitoring:check" {
			t.Fatalf("expected unwrapped observed job type, got %q", events[0].JobType)
		}
		if events[0].DispatchID != "dsp_rabbit" || events[0].JobID != "job_rabbit" || events[0].BatchID != "bat_rabbit" {
			t.Fatalf("expected correlated rabbitmq event, got %+v", events[0])
		}
	})

	t.Run("failed handler retries then nacks when republish fails", func(t *testing.T) {
		acks := &ackRecorder{}
		w := &rabbitMQWorker{
			handlers: map[string]queue.Handler{
				"job:retry": func(context.Context, queue.Job) error { return errors.New("boom") },
			},
			cfg: rabbitMQWorkerConfig{DefaultQueue: "default"},
		}
		body, err := json.Marshal(rabbitMQMessage{Type: "job:retry", Queue: "default", Attempt: 0, MaxRetry: 2, BackoffMillis: 10})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		w.processDelivery(context.Background(), amqp.Delivery{Body: body, Acknowledger: acks, DeliveryTag: 5})
		if acks.acks != 0 || acks.nacks != 1 {
			t.Fatalf("expected nack once, got ack=%d nack=%d", acks.acks, acks.nacks)
		}
	})

	t.Run("failed handler terminal acks", func(t *testing.T) {
		acks := &ackRecorder{}
		w := &rabbitMQWorker{
			handlers: map[string]queue.Handler{
				"job:terminal": func(context.Context, queue.Job) error { return errors.New("boom") },
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

// TestRabbitMQWorker_AttemptDecisionSettlement verifies terminal and uncommitted outcomes choose acknowledgement behavior without consuming retries.
func TestRabbitMQWorker_AttemptDecisionSettlement(t *testing.T) {
	t.Run("permanent failure acks without republishing", func(t *testing.T) {
		acks := &ackRecorder{}
		var events []queue.Event
		w := &rabbitMQWorker{
			handlers: map[string]queue.Handler{
				"job:permanent": func(ctx context.Context, _ queue.Job) error {
					attempt, ok := busruntime.DeliveryAttemptFromContext(ctx)
					if !ok || attempt.Number != 0 || attempt.MaxRetry != 3 {
						t.Fatalf("unexpected delivery attempt: %+v, present=%t", attempt, ok)
					}
					return busruntime.Permanent(errors.New("invalid job"))
				},
			},
			cfg:      rabbitMQWorkerConfig{DefaultQueue: "default"},
			observer: queue.ObserverFunc(func(_ context.Context, event queue.Event) { events = append(events, event) }),
		}
		body, err := json.Marshal(rabbitMQMessage{Type: "job:permanent", Queue: "default", MaxRetry: 3})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}

		w.processDelivery(context.Background(), amqp.Delivery{Body: body, Acknowledger: acks, DeliveryTag: 7})

		if acks.acks != 1 || acks.nacks != 0 {
			t.Fatalf("permanent failure must ack once, got ack=%d nack=%d", acks.acks, acks.nacks)
		}
		if len(events) != 0 {
			t.Fatalf("permanent failure must not reach the republish path, got %+v", events)
		}
	})

	t.Run("uncommitted failure nacks with requeue", func(t *testing.T) {
		acks := &ackRecorder{}
		var events []queue.Event
		w := &rabbitMQWorker{
			handlers: map[string]queue.Handler{
				"job:uncommitted": func(ctx context.Context, _ queue.Job) error {
					attempt, ok := busruntime.DeliveryAttemptFromContext(ctx)
					if !ok || attempt.Number != 1 || attempt.MaxRetry != 4 {
						t.Fatalf("unexpected delivery attempt: %+v, present=%t", attempt, ok)
					}
					return busruntime.Uncommitted(errors.New("store unavailable"))
				},
			},
			cfg:      rabbitMQWorkerConfig{DefaultQueue: "default"},
			observer: queue.ObserverFunc(func(_ context.Context, event queue.Event) { events = append(events, event) }),
		}
		body, err := json.Marshal(rabbitMQMessage{
			Type:          "job:uncommitted",
			Queue:         "default",
			Attempt:       1,
			MaxRetry:      4,
			BackoffMillis: 1_000,
		})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}

		w.processDelivery(context.Background(), amqp.Delivery{Body: body, Acknowledger: acks, DeliveryTag: 8})

		if acks.acks != 0 || acks.nacks != 1 || !acks.nackRequeue {
			t.Fatalf("uncommitted failure must nack with requeue, got ack=%d nack=%d requeue=%t", acks.acks, acks.nacks, acks.nackRequeue)
		}
		if len(events) != 0 {
			t.Fatalf("uncommitted failure must not publish a replacement, got %+v", events)
		}
	})
}

func TestRabbitMQWorker_PublishNilChannelAndImmediateDelay(t *testing.T) {
	w := &rabbitMQWorker{cfg: rabbitMQWorkerConfig{DefaultQueue: ""}}
	if err := w.publish(rabbitMQMessage{Type: "job:nilch", Queue: "default"}); !errors.Is(err, amqp.ErrClosed) {
		t.Fatalf("publish with nil channel should return amqp.ErrClosed, got %v", err)
	}
	if err := w.publish(rabbitMQMessage{
		Type:          "job:past",
		Queue:         "default",
		AvailableAtMS: time.Now().Add(-10 * time.Millisecond).UnixMilli(),
	}); !errors.Is(err, amqp.ErrClosed) {
		t.Fatalf("publish past delay with nil channel should return amqp.ErrClosed, got %v", err)
	}
}
