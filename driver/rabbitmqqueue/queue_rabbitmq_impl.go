package rabbitmqqueue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/goforj/queue"
	"github.com/goforj/queue/queuecore"
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
	dialTimeout  time.Duration

	mu     sync.Mutex
	conn   *amqp.Connection
	ch     *amqp.Channel
	unique map[string]time.Time
}

func newRabbitMQQueue(url string, defaultQueue string) *rabbitMQQueue {
	if defaultQueue == "" {
		defaultQueue = "default"
	}
	return &rabbitMQQueue{
		url:          url,
		defaultQueue: defaultQueue,
		unique:       make(map[string]time.Time),
	}
}

func (q *rabbitMQQueue) Driver() queue.Driver {
	return queue.DriverRabbitMQ
}

func (q *rabbitMQQueue) Shutdown(_ context.Context) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.closeLocked()
	return nil
}

func (q *rabbitMQQueue) Dispatch(ctx context.Context, job queue.Job) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := queuecore.ValidateDriverJob(job); err != nil {
		return err
	}
	parsed := queuecore.DriverOptions(job)
	if parsed.QueueName == "" {
		return fmt.Errorf("job queue is required")
	}
	if parsed.UniqueTTL > 0 && !q.claimUnique(job, parsed.QueueName, parsed.UniqueTTL) {
		return queuecore.ErrDuplicate
	}

	message := rabbitMQMessage{
		Type:          job.Type,
		Payload:       job.PayloadBytes(),
		Queue:         parsed.QueueName,
		PublishedAtMS: time.Now().UnixMilli(),
	}
	if parsed.MaxRetry != nil {
		message.MaxRetry = *parsed.MaxRetry
	}
	if parsed.Backoff != nil && *parsed.Backoff > 0 {
		message.BackoffMillis = parsed.Backoff.Milliseconds()
	}
	if parsed.Timeout != nil && *parsed.Timeout > 0 {
		message.TimeoutMillis = parsed.Timeout.Milliseconds()
	}
	if parsed.Delay > 0 {
		message.AvailableAtMS = time.Now().Add(parsed.Delay).UnixMilli()
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
	targetQueue := rabbitPhysicalQueueName(q.defaultQueue, parsed.QueueName)
	if err := q.enqueueLocked(ctx, targetQueue, body); err != nil {
		if !isRabbitConnectionClosed(err) {
			return err
		}
		q.closeLocked()
		if reconnectErr := q.ensureConnectedLocked(); reconnectErr != nil {
			return reconnectErr
		}
		return q.enqueueLocked(ctx, targetQueue, body)
	}
	return nil
}

func (q *rabbitMQQueue) claimUnique(job queue.Job, queueName string, ttl time.Duration) bool {
	now := time.Now()
	key := queueName + ":" + job.Type + ":" + string(job.PayloadBytes())

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
	dialTimeout := q.dialTimeout
	if dialTimeout <= 0 {
		dialTimeout = 10 * time.Second
	}
	conn, err := dialRabbitMQWithRetry(q.url, dialTimeout)
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

func rabbitPhysicalQueueName(defaultQueue, messageQueue string) string {
	if messageQueue != "" {
		return messageQueue
	}
	if defaultQueue != "" {
		return defaultQueue
	}
	return "default"
}
