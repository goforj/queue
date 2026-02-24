package rabbitmqqueue

import (
	"context"
	"encoding/json"
	"strconv"
	"sync"
	"time"

	"github.com/goforj/queue"
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

	conn *amqp.Connection
	ch   *amqp.Channel

	pubMu    sync.Mutex
	observer queue.Observer
}

type rabbitMQWorkerConfig struct {
	DefaultQueue string
	RabbitMQURL  string
	Workers      int
	Observer     queue.Observer
	DialTimeout  time.Duration
}

func newRabbitMQWorker(cfg rabbitMQWorkerConfig) queue.DriverWorkerBackend {
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

func (w *rabbitMQWorker) Shutdown(_ context.Context) error {
	w.startStop.Lock()
	if !w.started {
		w.startStop.Unlock()
		return nil
	}
	cancel := w.cancel
	w.started = false
	ch := w.ch
	conn := w.conn
	w.startStop.Unlock()

	if cancel != nil {
		cancel()
	}
	if ch != nil {
		_ = ch.Close()
	}
	if conn != nil {
		_ = conn.Close()
	}
	w.wg.Wait()
	return nil
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

func (w *rabbitMQWorker) processDelivery(ctx context.Context, delivery amqp.Delivery) {
	var incoming rabbitMQMessage
	if err := json.Unmarshal(delivery.Body, &incoming); err != nil {
		_ = delivery.Ack(false)
		return
	}

	if incoming.AvailableAtMS > 0 {
		remaining := time.Until(time.UnixMilli(incoming.AvailableAtMS))
		if remaining > 0 {
			if err := w.publish(incoming); err != nil {
				w.observeRepublishFailure(incoming, err)
				_ = delivery.Nack(false, true)
				return
			}
			_ = delivery.Ack(false)
			return
		}
		incoming.AvailableAtMS = 0
	}

	w.mu.RLock()
	handler, ok := w.handlers[incoming.Type]
	w.mu.RUnlock()
	if !ok {
		_ = delivery.Ack(false)
		return
	}

	runCtx := context.Background()
	if incoming.TimeoutMillis > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(runCtx, time.Duration(incoming.TimeoutMillis)*time.Millisecond)
		defer cancel()
	}
	err := handler(
		runCtx,
		queue.DriverWithAttempt(
			queue.NewJob(incoming.Type).
				Payload(incoming.Payload).
				OnQueue(incoming.Queue).
				Retry(incoming.MaxRetry),
			incoming.Attempt,
		),
	)
	if err == nil {
		_ = delivery.Ack(false)
		return
	}
	if incoming.Attempt >= incoming.MaxRetry {
		_ = delivery.Ack(false)
		return
	}
	incoming.Attempt++
	if incoming.BackoffMillis > 0 {
		incoming.AvailableAtMS = time.Now().Add(time.Duration(incoming.BackoffMillis) * time.Millisecond).UnixMilli()
	} else {
		incoming.AvailableAtMS = 0
	}
	if err := w.publish(incoming); err != nil {
		w.observeRepublishFailure(incoming, err)
		_ = delivery.Nack(false, true)
		return
	}
	_ = delivery.Ack(false)
}

func (w *rabbitMQWorker) observeRepublishFailure(message rabbitMQMessage, err error) {
	queuecore.SafeObserve(w.observer, queue.Event{
		Kind:     queue.EventRepublishFailed,
		Driver:   queue.DriverRabbitMQ,
		Queue:    queuecore.NormalizeQueueName(message.Queue),
		JobType:  message.Type,
		Attempt:  message.Attempt,
		MaxRetry: message.MaxRetry,
		Err:      err,
		Time:     time.Now(),
	})
}

func (w *rabbitMQWorker) publish(message rabbitMQMessage) error {
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
		return ch.PublishWithContext(context.Background(), "", queueName, false, false, amqp.Publishing{
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
	return ch.PublishWithContext(context.Background(), "", delayQueue, false, false, amqp.Publishing{
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
