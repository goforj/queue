package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

type rabbitMQMessage struct {
	Type          string `json:"type"`
	Payload       []byte `json:"payload,omitempty"`
	Queue         string `json:"queue"`
	Attempt       int    `json:"attempt,omitempty"`
	MaxRetry      int    `json:"max_retry,omitempty"`
	BackoffMillis int64  `json:"backoff_millis,omitempty"`
	TimeoutMillis int64  `json:"timeout_millis,omitempty"`
	AvailableAtMS int64  `json:"available_at_ms,omitempty"`
	PublishedAtMS int64  `json:"published_at_ms,omitempty"`
}

type rabbitMQQueue struct {
	url          string
	defaultQueue string

	mu     sync.Mutex
	conn   *amqp.Connection
	ch     *amqp.Channel
	unique map[string]time.Time
}

func newRabbitMQQueue(url string, defaultQueue string) queueBackend {
	if defaultQueue == "" {
		defaultQueue = "default"
	}
	return &rabbitMQQueue{
		url:          url,
		defaultQueue: defaultQueue,
		unique:       make(map[string]time.Time),
	}
}

func (q *rabbitMQQueue) Driver() Driver {
	return DriverRabbitMQ
}

func (q *rabbitMQQueue) Shutdown(_ context.Context) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.closeLocked()
	return nil
}

func (q *rabbitMQQueue) Dispatch(ctx context.Context, task Job) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := task.validate(); err != nil {
		return err
	}
	parsed := task.jobOptions()
	if parsed.queueName == "" {
		return fmt.Errorf("job queue is required")
	}
	if parsed.uniqueTTL > 0 && !q.claimUnique(task, parsed.queueName, parsed.uniqueTTL) {
		return ErrDuplicate
	}

	message := rabbitMQMessage{
		Type:          task.Type,
		Payload:       task.PayloadBytes(),
		Queue:         parsed.queueName,
		PublishedAtMS: time.Now().UnixMilli(),
	}
	if parsed.maxRetry != nil {
		message.MaxRetry = *parsed.maxRetry
	}
	if parsed.backoff != nil && *parsed.backoff > 0 {
		message.BackoffMillis = parsed.backoff.Milliseconds()
	}
	if parsed.timeout != nil && *parsed.timeout > 0 {
		message.TimeoutMillis = parsed.timeout.Milliseconds()
	}
	if parsed.delay > 0 {
		message.AvailableAtMS = time.Now().Add(parsed.delay).UnixMilli()
	}
	body, err := json.Marshal(message)
	if err != nil {
		return err
	}

	q.mu.Lock()
	defer q.mu.Unlock()
	if err := q.ensureConnectedLocked(); err != nil {
		return err
	}
	if err := q.enqueueLocked(ctx, q.defaultQueue, body); err != nil {
		if !isRabbitConnectionClosed(err) {
			return err
		}
		q.closeLocked()
		if reconnectErr := q.ensureConnectedLocked(); reconnectErr != nil {
			return reconnectErr
		}
		return q.enqueueLocked(ctx, q.defaultQueue, body)
	}
	return nil
}

func (q *rabbitMQQueue) claimUnique(task Job, queueName string, ttl time.Duration) bool {
	now := time.Now()
	key := queueName + ":" + task.Type + ":" + string(task.PayloadBytes())

	q.mu.Lock()
	defer q.mu.Unlock()
	for candidate, expiresAt := range q.unique {
		if expiresAt.Before(now) {
			delete(q.unique, candidate)
		}
	}
	if expiresAt, ok := q.unique[key]; ok && expiresAt.After(now) {
		return false
	}
	q.unique[key] = now.Add(ttl)
	return true
}

func (q *rabbitMQQueue) ensureConnectedLocked() error {
	if q.conn != nil && !q.conn.IsClosed() && q.ch != nil && !q.ch.IsClosed() {
		return nil
	}
	q.closeLocked()
	conn, err := dialRabbitMQWithRetry(q.url, 10*time.Second)
	if err != nil {
		return err
	}
	ch, err := conn.Channel()
	if err != nil {
		_ = conn.Close()
		return err
	}
	q.conn = conn
	q.ch = ch
	return nil
}

func (q *rabbitMQQueue) closeLocked() {
	if q.ch != nil {
		_ = q.ch.Close()
		q.ch = nil
	}
	if q.conn != nil {
		_ = q.conn.Close()
		q.conn = nil
	}
}

func (q *rabbitMQQueue) enqueueLocked(ctx context.Context, queueName string, body []byte) error {
	if q.ch == nil || q.ch.IsClosed() {
		return amqp.ErrClosed
	}
	if _, err := q.ch.QueueDeclare(queueName, true, false, false, false, nil); err != nil {
		return err
	}
	return q.ch.PublishWithContext(ctx, "", queueName, false, false, amqp.Publishing{
		ContentType:  "application/json",
		Body:         body,
		DeliveryMode: amqp.Persistent,
	})
}

func isRabbitConnectionClosed(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, amqp.ErrClosed) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "channel/connection is not open")
}
