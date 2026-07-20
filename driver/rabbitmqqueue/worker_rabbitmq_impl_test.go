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
	ackErr      error
	nackErr     error
}

type rabbitConfirmationStub struct {
	acked bool
	err   error
}

type rabbitContextConfirmationStub struct{}

// WaitContext returns the configured broker confirmation.
func (s rabbitConfirmationStub) WaitContext(context.Context) (bool, error) {
	return s.acked, s.err
}

// WaitContext exposes cancellation from the caller's publish boundary.
func (rabbitContextConfirmationStub) WaitContext(ctx context.Context) (bool, error) {
	<-ctx.Done()
	return false, ctx.Err()
}

func (a *ackRecorder) Ack(_ uint64, _ bool) error {
	a.acks++
	return a.ackErr
}

// Nack records whether the worker requested broker redelivery.
func (a *ackRecorder) Nack(_ uint64, _ bool, requeue bool) error {
	a.nacks++
	a.nackRequeue = requeue
	return a.nackErr
}

// TestRabbitMQWorkerSettlementFailuresAreObserved verifies Ack and Nack errors become correlated worker facts.
func TestRabbitMQWorkerSettlementFailuresAreObserved(t *testing.T) {
	tests := []struct {
		name       string
		handlerErr error
		maxRetry   int
		acks       *ackRecorder
	}{
		{name: "ack", maxRetry: 0, acks: &ackRecorder{ackErr: errors.New("ack failed")}},
		{name: "nack", handlerErr: busruntime.Uncommitted(errors.New("store failed")), maxRetry: 2, acks: &ackRecorder{nackErr: errors.New("nack failed")}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var events []queue.Event
			committed := false
			var handlerSettlement busruntime.DeliverySettlementIdentity
			var handlerSettlementOK bool
			var observedSettlement busruntime.DeliverySettlementIdentity
			var observedSettlementOK bool
			w := &rabbitMQWorker{
				handlers: map[string]queue.Handler{"bus:job": func(ctx context.Context, _ queue.Job) error {
					handlerSettlement, handlerSettlementOK = busruntime.DeliverySettlementIdentityFromContext(ctx)
					if !busruntime.DeferUntilDeliveryCommitted(ctx, func() { committed = true }) {
						t.Fatal("handler context did not carry a settlement boundary")
					}
					return test.handlerErr
				}},
				observer: queue.ObserverFunc(func(ctx context.Context, event queue.Event) {
					observedSettlement, observedSettlementOK = busruntime.DeliverySettlementIdentityFromContext(ctx)
					events = append(events, event)
				}),
			}
			payload := []byte(`{"schema_version":1,"dispatch_id":"dsp_rabbit_settle","job_id":"job_rabbit_settle","job":{"type":"reports:build","payload":"eyJpZCI6MX0="}}`)
			body, err := json.Marshal(rabbitMQMessage{Type: "bus:job", Queue: "critical", Payload: payload, MaxRetry: test.maxRetry})
			if err != nil {
				t.Fatalf("marshal body: %v", err)
			}
			w.processDelivery(context.Background(), amqp.Delivery{Body: body, Acknowledger: test.acks, DeliveryTag: 70})
			if len(events) != 1 || events[0].Kind != queue.EventSettlementFailed {
				t.Fatalf("settlement events = %+v, want one failure", events)
			}
			if events[0].Layer != queue.EventLayerWorker || events[0].JobType != "reports:build" || events[0].DispatchID != "dsp_rabbit_settle" {
				t.Fatalf("settlement correlation = %+v", events[0])
			}
			if committed {
				t.Fatal("failed acknowledgement committed deferred handler outcome")
			}
			if !handlerSettlementOK || !observedSettlementOK || observedSettlement != handlerSettlement {
				t.Fatal("settlement observer did not retain the handler's delivery identity")
			}
		})
	}
}

// TestRabbitMQWorkerRetrySettlementFailureUsesDeliveredAttempt verifies replacement metadata does not overwrite the unsettled delivery's correlation.
func TestRabbitMQWorkerRetrySettlementFailureUsesDeliveredAttempt(t *testing.T) {
	acks := &ackRecorder{ackErr: errors.New("ack failed")}
	var events []queue.Event
	var handlerSettlement busruntime.DeliverySettlementIdentity
	var handlerSettlementOK bool
	var observedSettlement busruntime.DeliverySettlementIdentity
	var observedSettlementOK bool
	w := &rabbitMQWorker{
		handlers: map[string]queue.Handler{"job:retry:settlement": func(ctx context.Context, _ queue.Job) error {
			handlerSettlement, handlerSettlementOK = busruntime.DeliverySettlementIdentityFromContext(ctx)
			return errors.New("retry me")
		}},
		cfg: rabbitMQWorkerConfig{DefaultQueue: "default"},
		observer: queue.ObserverFunc(func(ctx context.Context, event queue.Event) {
			observedSettlement, observedSettlementOK = busruntime.DeliverySettlementIdentityFromContext(ctx)
			events = append(events, event)
		}),
		publishOverride: func(context.Context, rabbitMQMessage) error {
			return nil
		},
	}
	body, err := json.Marshal(rabbitMQMessage{Type: "job:retry:settlement", Queue: "critical", Attempt: 1, MaxRetry: 3})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	w.processDelivery(context.Background(), amqp.Delivery{Body: body, Acknowledger: acks, DeliveryTag: 71})
	if len(events) != 1 || events[0].Kind != queue.EventSettlementFailed || events[0].Attempt != 1 {
		t.Fatalf("settlement events = %+v, want original attempt 1", events)
	}
	if !handlerSettlementOK || !observedSettlementOK || observedSettlement != handlerSettlement {
		t.Fatal("retry settlement observer did not retain the handler's delivery identity")
	}
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

// TestRabbitMQWorkerShutdownHonorsDeadline verifies a stuck in-flight handler cannot block the caller forever.
func TestRabbitMQWorkerShutdownHonorsDeadline(t *testing.T) {
	w := newRabbitMQWorker(rabbitMQWorkerConfig{})
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
		committed := false
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
				if !busruntime.DeferUntilDeliveryCommitted(ctx, func() { committed = true }) {
					t.Fatal("handler context did not carry a settlement boundary")
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
		if !committed {
			t.Fatal("successful acknowledgement did not commit deferred handler success")
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

	t.Run("republish and nack failures retain their physical attempts", func(t *testing.T) {
		acks := &ackRecorder{nackErr: errors.New("nack failed")}
		var events []queue.Event
		w := &rabbitMQWorker{
			handlers: map[string]queue.Handler{
				"job:retry:failed-settlement": func(context.Context, queue.Job) error { return errors.New("retry") },
			},
			cfg: rabbitMQWorkerConfig{DefaultQueue: "default"},
			observer: queue.ObserverFunc(func(_ context.Context, event queue.Event) {
				events = append(events, event)
			}),
			publishOverride: func(context.Context, rabbitMQMessage) error {
				return errors.New("publish failed")
			},
		}
		body, err := json.Marshal(rabbitMQMessage{
			Type:     "job:retry:failed-settlement",
			Queue:    "critical",
			Attempt:  2,
			MaxRetry: 4,
		})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		w.processDelivery(context.Background(), amqp.Delivery{Body: body, Acknowledger: acks, DeliveryTag: 55})
		if acks.acks != 0 || acks.nacks != 1 {
			t.Fatalf("publish failure ack/nack = %d/%d, want 0/1", acks.acks, acks.nacks)
		}
		if len(events) != 2 {
			t.Fatalf("failure events = %+v, want republish and settlement failures", events)
		}
		if events[0].Kind != queue.EventRepublishFailed || events[0].Attempt != 3 {
			t.Fatalf("republish failure = %+v, want replacement attempt 3", events[0])
		}
		if events[1].Kind != queue.EventSettlementFailed || events[1].Attempt != 2 {
			t.Fatalf("settlement failure = %+v, want original receipt attempt 2", events[1])
		}
	})

	t.Run("failed handler acks only after confirmed replacement", func(t *testing.T) {
		acks := &ackRecorder{}
		published := 0
		w := &rabbitMQWorker{
			handlers: map[string]queue.Handler{
				"job:retry:confirmed": func(context.Context, queue.Job) error { return errors.New("boom") },
			},
			cfg: rabbitMQWorkerConfig{DefaultQueue: "default"},
			publishOverride: func(ctx context.Context, message rabbitMQMessage) error {
				published++
				if ctx.Err() != nil {
					t.Fatalf("replacement publish context is already canceled: %v", ctx.Err())
				}
				if _, ok := ctx.Deadline(); !ok {
					t.Fatal("replacement publish context has no settlement deadline")
				}
				if acks.acks != 0 {
					t.Fatal("original delivery was acknowledged before replacement confirmation")
				}
				if message.Attempt != 1 {
					t.Fatalf("replacement attempt = %d, want 1", message.Attempt)
				}
				return nil
			},
		}
		body, err := json.Marshal(rabbitMQMessage{Type: "job:retry:confirmed", Queue: "default", MaxRetry: 2})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		w.processDelivery(context.Background(), amqp.Delivery{Body: body, Acknowledger: acks, DeliveryTag: 55})
		if published != 1 || acks.acks != 1 || acks.nacks != 0 {
			t.Fatalf("publish/ack/nack = %d/%d/%d, want 1/1/0", published, acks.acks, acks.nacks)
		}
	})

	t.Run("expired handler timeout does not cancel replacement settlement", func(t *testing.T) {
		acks := &ackRecorder{}
		published := 0
		w := &rabbitMQWorker{
			handlers: map[string]queue.Handler{
				"job:retry:timeout": func(ctx context.Context, _ queue.Job) error {
					<-ctx.Done()
					return ctx.Err()
				},
			},
			cfg: rabbitMQWorkerConfig{DefaultQueue: "default"},
			publishOverride: func(ctx context.Context, message rabbitMQMessage) error {
				published++
				if ctx.Err() != nil {
					t.Fatalf("expired handler context leaked into settlement: %v", ctx.Err())
				}
				if _, ok := ctx.Deadline(); !ok {
					t.Fatal("replacement settlement context has no deadline")
				}
				if message.Attempt != 1 {
					t.Fatalf("replacement attempt = %d, want 1", message.Attempt)
				}
				return nil
			},
		}
		body, err := json.Marshal(rabbitMQMessage{
			Type:          "job:retry:timeout",
			Queue:         "default",
			MaxRetry:      1,
			TimeoutMillis: 1,
		})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		w.processDelivery(context.Background(), amqp.Delivery{Body: body, Acknowledger: acks, DeliveryTag: 56})
		if published != 1 || acks.acks != 1 || acks.nacks != 0 {
			t.Fatalf("publish/ack/nack = %d/%d/%d, want 1/1/0", published, acks.acks, acks.nacks)
		}
	})

	t.Run("canceled delivery context does not cancel delayed replacement settlement", func(t *testing.T) {
		acks := &ackRecorder{}
		published := 0
		w := &rabbitMQWorker{
			handlers: map[string]queue.Handler{},
			cfg:      rabbitMQWorkerConfig{DefaultQueue: "default"},
			publishOverride: func(ctx context.Context, _ rabbitMQMessage) error {
				published++
				if ctx.Err() != nil {
					t.Fatalf("delivery cancellation leaked into delayed settlement: %v", ctx.Err())
				}
				if _, ok := ctx.Deadline(); !ok {
					t.Fatal("delayed settlement context has no deadline")
				}
				return nil
			},
		}
		body, err := json.Marshal(rabbitMQMessage{
			Type:          "job:future:canceled",
			Queue:         "default",
			AvailableAtMS: time.Now().Add(time.Second).UnixMilli(),
		})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		deliveryCtx, cancel := context.WithCancel(context.Background())
		cancel()
		w.processDelivery(deliveryCtx, amqp.Delivery{Body: body, Acknowledger: acks, DeliveryTag: 57})
		if published != 1 || acks.acks != 1 || acks.nacks != 0 {
			t.Fatalf("publish/ack/nack = %d/%d/%d, want 1/1/0", published, acks.acks, acks.nacks)
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
	if err := w.publish(context.Background(), rabbitMQMessage{Type: "job:nilch", Queue: "default"}); !errors.Is(err, amqp.ErrClosed) {
		t.Fatalf("publish with nil channel should return amqp.ErrClosed, got %v", err)
	}
	if err := w.publish(context.Background(), rabbitMQMessage{
		Type:          "job:past",
		Queue:         "default",
		AvailableAtMS: time.Now().Add(-10 * time.Millisecond).UnixMilli(),
	}); !errors.Is(err, amqp.ErrClosed) {
		t.Fatalf("publish past delay with nil channel should return amqp.ErrClosed, got %v", err)
	}
}

// TestAwaitRabbitConfirmation verifies only a positive confirmation commits a publish.
func TestAwaitRabbitConfirmation(t *testing.T) {
	cause := errors.New("confirmation channel closed")
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	tests := []struct {
		name         string
		ctx          context.Context
		confirmation rabbitPublishConfirmation
		wantErr      bool
	}{
		{name: "missing", wantErr: true},
		{name: "nack", confirmation: rabbitConfirmationStub{}, wantErr: true},
		{name: "wait error", confirmation: rabbitConfirmationStub{err: cause}, wantErr: true},
		{name: "context canceled", ctx: canceledCtx, confirmation: rabbitContextConfirmationStub{}, wantErr: true},
		{name: "ack", confirmation: rabbitConfirmationStub{acked: true}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := test.ctx
			if ctx == nil {
				ctx = context.Background()
			}
			err := awaitRabbitConfirmation(ctx, test.confirmation)
			if (err != nil) != test.wantErr {
				t.Fatalf("awaitRabbitConfirmation() error = %v, wantErr %t", err, test.wantErr)
			}
		})
	}
}

// TestRabbitPublishContextBoundsBackground verifies producer confirmation cannot wait forever without a caller deadline.
func TestRabbitPublishContextBoundsBackground(t *testing.T) {
	ctx, cancel, err := rabbitPublishContext(context.Background())
	if err != nil {
		t.Fatalf("rabbit publish context: %v", err)
	}
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("bounded publish context has no deadline")
	}
	remaining := time.Until(deadline)
	if remaining <= 0 || remaining > rabbitPublishConfirmationTimeout {
		t.Fatalf("publish confirmation deadline remaining = %v", remaining)
	}

	canceled, cancelCanceled := context.WithCancel(context.Background())
	cancelCanceled()
	if err := publishRabbitConfirmed(canceled, nil, "", "default", amqp.Publishing{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled publish error = %v, want context.Canceled", err)
	}
}

// TestRabbitPublishAmbiguityClassification verifies lost confirmations cannot be treated as safe pre-publish rejection.
func TestRabbitPublishAmbiguityClassification(t *testing.T) {
	waitErr := errors.New("confirmation lost")
	err := awaitRabbitConfirmation(context.Background(), rabbitConfirmationStub{err: waitErr})
	if !isRabbitPublishAmbiguous(err) || !errors.Is(err, waitErr) {
		t.Fatalf("confirmation error = %v, want ambiguous wrapped cause", err)
	}
	publishErr := errors.New("publish response lost")
	err = completeRabbitPublish(context.Background(), nil, publishErr)
	if !isRabbitPublishAmbiguous(err) || !errors.Is(err, publishErr) {
		t.Fatalf("publish error = %v, want ambiguous wrapped cause", err)
	}
	err = completeRabbitPublish(context.Background(), nil, nil)
	if !isRabbitPublishAmbiguous(err) {
		t.Fatalf("missing deferred confirmation error = %v, want ambiguous", err)
	}
	if err := completeRabbitPublish(context.Background(), rabbitConfirmationStub{acked: true}, nil); err != nil {
		t.Fatalf("completed publish: %v", err)
	}
	if isRabbitPublishAmbiguous(errors.New("dial rejected")) {
		t.Fatal("pre-publish failure classified as ambiguous")
	}
}

// TestRabbitMQWorkerNilShutdownAndCanceledPublish verifies lifecycle context
// normalization and pre-publish cancellation without opening broker resources.
func TestRabbitMQWorkerNilShutdownAndCanceledPublish(t *testing.T) {
	w := &rabbitMQWorker{}
	if err := w.Shutdown(nil); err != nil {
		t.Fatalf("nil-context shutdown: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := w.publish(ctx, rabbitMQMessage{Type: "job:canceled", Queue: "default"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled worker publish error = %v, want context.Canceled", err)
	}
}
