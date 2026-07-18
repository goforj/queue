package rabbitmqqueue

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/goforj/queue"
	"github.com/goforj/queue/busruntime"
	"github.com/goforj/queue/queuecore"
	amqp "github.com/rabbitmq/amqp091-go"
)

type rabbitMQWorker struct {
	cfg rabbitMQWorkerConfig

	mu       sync.RWMutex
	handlers map[string]queue.Handler

	startStop sync.Mutex
	started   bool
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	stopDone  chan struct{}

	conn *amqp.Connection
	ch   *amqp.Channel

	pubMu           sync.Mutex
	observer        queue.Observer
	publishOverride func(context.Context, rabbitMQMessage) error
}

type rabbitMQWorkerConfig struct {
	DefaultQueue string
	RabbitMQURL  string
	Workers      int
	Observer     queue.Observer
	DialTimeout  time.Duration
}

func newRabbitMQWorker(cfg rabbitMQWorkerConfig) *rabbitMQWorker {
	if cfg.DefaultQueue == "" {
		cfg.DefaultQueue = "default"
	}
	cfg.Workers = defaultWorkerCount(cfg.Workers)
	return &rabbitMQWorker{
		cfg:      cfg,
		handlers: make(map[string]queue.Handler),
		observer: cfg.Observer,
	}
}

func (w *rabbitMQWorker) Register(jobType string, handler queue.Handler) {
	if jobType == "" || handler == nil {
		return
	}
	w.mu.Lock()
	w.handlers[jobType] = handler
	w.mu.Unlock()
}

func (w *rabbitMQWorker) StartWorkers(ctx context.Context) error {
	w.startStop.Lock()
	defer w.startStop.Unlock()
	if w.started {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	dialTimeout := w.cfg.DialTimeout
	if dialTimeout <= 0 {
		dialTimeout = 15 * time.Second
	}
	conn, err := dialRabbitMQWithRetry(w.cfg.RabbitMQURL, dialTimeout)
	if err != nil {
		return err
	}
	ch, err := conn.Channel()
	if err != nil {
		_ = conn.Close()
		return err
	}
	if err := ch.Confirm(false); err != nil {
		_ = ch.Close()
		_ = conn.Close()
		return err
	}
	if _, err := ch.QueueDeclare(w.cfg.DefaultQueue, true, false, false, false, nil); err != nil {
		_ = ch.Close()
		_ = conn.Close()
		return err
	}
	_ = ch.Qos(w.cfg.Workers, 0, false)
	deliveries, err := ch.Consume(w.cfg.DefaultQueue, "", false, false, false, false, nil)
	if err != nil {
		_ = ch.Close()
		_ = conn.Close()
		return err
	}
	loopCtx, cancel := context.WithCancel(ctx)
	w.cancel = cancel
	w.conn = conn
	w.ch = ch
	w.started = true

	for i := 0; i < w.cfg.Workers; i++ {
		w.wg.Add(1)
		go w.loop(loopCtx, deliveries)
	}
	return nil
}

// Shutdown stops intake and keeps settlement resources open until in-flight deliveries drain or the caller deadline expires.
func (w *rabbitMQWorker) Shutdown(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	w.startStop.Lock()
	if !w.started {
		w.startStop.Unlock()
		return nil
	}
	if w.stopDone == nil {
		w.stopDone = make(chan struct{})
		cancel := w.cancel
		ch := w.ch
		conn := w.conn
		done := w.stopDone
		if cancel != nil {
			cancel()
		}
		go func() {
			w.wg.Wait()
			closeRabbitResources(ch, conn)
			w.startStop.Lock()
			if w.ch == ch {
				w.ch = nil
			}
			if w.conn == conn {
				w.conn = nil
			}
			w.started = false
			w.stopDone = nil
			w.startStop.Unlock()
			close(done)
		}()
	}
	done := w.stopDone
	ch := w.ch
	conn := w.conn
	w.startStop.Unlock()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		closeRabbitResources(ch, conn)
		return ctx.Err()
	}
}

func (w *rabbitMQWorker) loop(ctx context.Context, deliveries <-chan amqp.Delivery) {
	defer w.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case delivery, ok := <-deliveries:
			if !ok {
				return
			}
			w.processDelivery(ctx, delivery)
		}
	}
}

// processDelivery commits positive facts only after the original RabbitMQ delivery is acknowledged.
func (w *rabbitMQWorker) processDelivery(ctx context.Context, delivery amqp.Delivery) {
	var incoming rabbitMQMessage
	if err := json.Unmarshal(delivery.Body, &incoming); err != nil {
		w.ack(ctx, delivery, incoming)
		return
	}

	if incoming.AvailableAtMS > 0 {
		remaining := time.Until(time.UnixMilli(incoming.AvailableAtMS))
		if remaining > 0 {
			if err := w.publish(context.Background(), incoming); err != nil {
				w.observeRepublishFailure(ctx, incoming, err)
				w.nack(ctx, delivery, incoming, true)
				return
			}
			w.ack(ctx, delivery, incoming)
			return
		}
		incoming.AvailableAtMS = 0
	}

	w.mu.RLock()
	handler, ok := w.handlers[incoming.Type]
	w.mu.RUnlock()
	if !ok {
		w.ack(ctx, delivery, incoming)
		return
	}

	attempt := busruntime.DeliveryAttempt{Number: incoming.Attempt, MaxRetry: incoming.MaxRetry}
	runCtx := busruntime.WithDeliveryAttempt(context.Background(), attempt)
	runCtx, settlement := busruntime.WithDeliverySettlement(runCtx)
	if incoming.TimeoutMillis > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(runCtx, time.Duration(incoming.TimeoutMillis)*time.Millisecond)
		defer cancel()
	}
	err := handler(
		runCtx,
		queuecore.DriverWithAttempt(
			queue.NewJob(incoming.Type).
				Payload(incoming.Payload).
				OnQueue(incoming.Queue).
				Retry(incoming.MaxRetry),
			incoming.Attempt,
		),
	)
	switch busruntime.ClassifyAttempt(attempt, err) {
	case busruntime.AttemptSucceeded, busruntime.AttemptFailed:
		if w.ack(ctx, delivery, incoming) {
			settlement.Commit()
		}
		return
	case busruntime.AttemptRedeliver:
		w.nack(ctx, delivery, incoming, true)
		return
	case busruntime.AttemptRetry:
	}
	settledMessage := incoming
	incoming.Attempt++
	if incoming.BackoffMillis > 0 {
		incoming.AvailableAtMS = time.Now().Add(time.Duration(incoming.BackoffMillis) * time.Millisecond).UnixMilli()
	} else {
		incoming.AvailableAtMS = 0
	}
	if err := w.publish(context.Background(), incoming); err != nil {
		w.observeRepublishFailure(runCtx, incoming, err)
		w.nack(ctx, delivery, incoming, true)
		return
	}
	if w.ack(ctx, delivery, settledMessage) {
		settlement.Commit()
	}
}

// ack reports a failed positive settlement and returns whether the broker accepted the acknowledgement.
func (w *rabbitMQWorker) ack(ctx context.Context, delivery amqp.Delivery, message rabbitMQMessage) bool {
	if err := delivery.Ack(false); err != nil {
		w.observeSettlementFailure(ctx, message, fmt.Errorf("ack rabbitmq delivery: %w", err))
		return false
	}
	return true
}

// nack reports a failed negative settlement because redelivery intent did not reach the broker.
func (w *rabbitMQWorker) nack(ctx context.Context, delivery amqp.Delivery, message rabbitMQMessage, requeue bool) {
	if err := delivery.Nack(false, requeue); err != nil {
		w.observeSettlementFailure(ctx, message, fmt.Errorf("nack rabbitmq delivery: %w", err))
	}
}

func (w *rabbitMQWorker) observeRepublishFailure(ctx context.Context, message rabbitMQMessage, err error) {
	metadata := queue.ResolveObservedJobMetadata(message.Type, message.Payload)
	queuecore.SafeObserve(ctx, w.observer, queue.Event{
		Kind:       queue.EventRepublishFailed,
		Driver:     queue.DriverRabbitMQ,
		Queue:      queuecore.NormalizeQueueName(message.Queue),
		JobType:    metadata.JobType,
		JobKey:     metadata.JobKey,
		DispatchID: metadata.DispatchID,
		JobID:      metadata.JobID,
		ChainID:    metadata.ChainID,
		BatchID:    metadata.BatchID,
		Attempt:    message.Attempt,
		MaxRetry:   message.MaxRetry,
		Err:        err,
		Time:       time.Now(),
	})
}

// observeSettlementFailure emits the canonical worker fact for an uncommitted RabbitMQ acknowledgement.
func (w *rabbitMQWorker) observeSettlementFailure(ctx context.Context, message rabbitMQMessage, err error) {
	metadata := queue.ResolveObservedJobMetadata(message.Type, message.Payload)
	queuecore.SafeObserve(ctx, w.observer, queue.Event{
		Kind:       queue.EventSettlementFailed,
		Driver:     queue.DriverRabbitMQ,
		Queue:      queuecore.NormalizeQueueName(message.Queue),
		JobType:    metadata.JobType,
		JobKey:     metadata.JobKey,
		DispatchID: metadata.DispatchID,
		JobID:      metadata.JobID,
		ChainID:    metadata.ChainID,
		BatchID:    metadata.BatchID,
		Attempt:    message.Attempt,
		MaxRetry:   message.MaxRetry,
		Err:        err,
		Time:       time.Now(),
	})
}

// publish declares the destination and waits for broker confirmation before reporting success.
func (w *rabbitMQWorker) publish(ctx context.Context, message rabbitMQMessage) error {
	settlementCtx, cancel, err := rabbitPublishContext(ctx)
	if err != nil {
		return err
	}
	defer cancel()
	if w.publishOverride != nil {
		return w.publishOverride(settlementCtx, message)
	}
	w.startStop.Lock()
	ch := w.ch
	w.startStop.Unlock()
	if ch == nil {
		return amqp.ErrClosed
	}
	body, err := json.Marshal(message)
	if err != nil {
		return err
	}
	queueName := rabbitPhysicalQueueName(w.cfg.DefaultQueue, message.Queue)
	delay := time.Duration(0)
	if message.AvailableAtMS > 0 {
		delay = time.Until(time.UnixMilli(message.AvailableAtMS))
		if delay <= 0 {
			message.AvailableAtMS = 0
			delay = 0
		}
	}
	w.pubMu.Lock()
	defer w.pubMu.Unlock()
	if _, err := ch.QueueDeclare(queueName, true, false, false, false, nil); err != nil {
		return err
	}
	if delay <= 0 {
		return publishRabbitConfirmed(settlementCtx, ch, "", queueName, amqp.Publishing{
			ContentType:  "application/json",
			Body:         body,
			DeliveryMode: amqp.Persistent,
		})
	}

	delayQueue := queueName + ".delay"
	delayMS := delay.Milliseconds()
	if delayMS < 1 {
		delayMS = 1
	}
	args := amqp.Table{
		"x-dead-letter-exchange":    "",
		"x-dead-letter-routing-key": queueName,
	}
	if _, err := ch.QueueDeclare(delayQueue, true, false, false, false, args); err != nil {
		return err
	}
	return publishRabbitConfirmed(settlementCtx, ch, "", delayQueue, amqp.Publishing{
		ContentType:  "application/json",
		Body:         body,
		Expiration:   strconv.FormatInt(delayMS, 10),
		DeliveryMode: amqp.Persistent,
	})
}

func defaultWorkerCount(n int) int {
	if n <= 0 {
		return 1
	}
	return n
}

// closeRabbitResources closes settlement resources only after a graceful drain or an expired shutdown deadline.
func closeRabbitResources(ch *amqp.Channel, conn *amqp.Connection) {
	if ch != nil {
		_ = ch.Close()
	}
	if conn != nil {
		_ = conn.Close()
	}
}
