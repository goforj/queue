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
	"github.com/goforj/queue/internal/uniqueness"
	"github.com/goforj/queue/queuecore"
	amqp "github.com/rabbitmq/amqp091-go"
)

const rabbitPublishConfirmationTimeout = 15 * time.Second

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
	unique uniqueness.MemoryStore
}

func newRabbitMQQueue(url string, defaultQueue string) *rabbitMQQueue {
	if defaultQueue == "" {
		defaultQueue = "default"
	}
	return &rabbitMQQueue{
		url:          url,
		defaultQueue: defaultQueue,
	}
}

func (q *rabbitMQQueue) Driver() queue.Driver {
	return queue.DriverRabbitMQ
}

func (q *rabbitMQQueue) Preflight(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.ensureConnectedLocked()
}

func (q *rabbitMQQueue) Shutdown(_ context.Context) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.closeLocked()
	return nil
}

// Dispatch requires a positive publisher confirmation before reporting acceptance.
func (q *rabbitMQQueue) Dispatch(ctx context.Context, job queue.Job) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := queuecore.ValidateDriverJob(job); err != nil {
		return err
	}
	parsed := queuecore.DriverOptions(job)
	if parsed.QueueName == "" {
		return fmt.Errorf("job queue is required")
	}
	var (
		uniqueKey   string
		uniqueToken uint64
	)
	if parsed.UniqueTTL > 0 {
		var acquired bool
		uniqueKey, uniqueToken, acquired = q.claimUnique(job, parsed.QueueName, parsed.UniqueTTL)
		if !acquired {
			return queuecore.ErrDuplicate
		}
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
		q.unique.Release(uniqueKey, uniqueToken)
		return err
	}

	q.mu.Lock()
	targetQueue := rabbitPhysicalQueueName(q.defaultQueue, parsed.QueueName)
	err = q.enqueueWithReconnectLocked(ctx, targetQueue, body)
	q.mu.Unlock()
	if err != nil && !isRabbitPublishAmbiguous(err) {
		q.unique.Release(uniqueKey, uniqueToken)
	}
	return err
}

// claimUnique returns the ownership token needed to compensate a rejected publish.
func (q *rabbitMQQueue) claimUnique(job queue.Job, queueName string, ttl time.Duration) (string, uint64, bool) {
	key := queuecore.UniqueKey(job, queueName)
	token, ok := q.unique.Acquire(key, ttl)
	return key, token, ok
}

// enqueueWithReconnectLocked retries one publish after replacing a closed connection.
func (q *rabbitMQQueue) enqueueWithReconnectLocked(ctx context.Context, queueName string, body []byte) error {
	if err := q.ensureConnectedLocked(); err != nil {
		return err
	}
	if err := q.enqueueLocked(ctx, queueName, body); err != nil {
		if isRabbitPublishAmbiguous(err) {
			return err
		}
		if !isRabbitConnectionClosed(err) {
			return err
		}
		q.closeLocked()
		if reconnectErr := q.ensureConnectedLocked(); reconnectErr != nil {
			return reconnectErr
		}
		return q.enqueueLocked(ctx, queueName, body)
	}
	return nil
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
	if err := ch.Confirm(false); err != nil {
		_ = ch.Close()
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
	return publishRabbitConfirmed(ctx, q.ch, "", queueName, amqp.Publishing{
		ContentType:  "application/json",
		Body:         body,
		DeliveryMode: amqp.Persistent,
	})
}

type rabbitPublishConfirmation interface {
	WaitContext(ctx context.Context) (bool, error)
}

type rabbitPublishAmbiguousError struct {
	cause error
}

// Error describes a publish whose broker outcome could not be determined.
func (e rabbitPublishAmbiguousError) Error() string { return e.cause.Error() }

// Unwrap preserves the network or context cause for diagnostics.
func (e rabbitPublishAmbiguousError) Unwrap() error { return e.cause }

// publishRabbitConfirmed waits for the broker to accept a persistent publish before its caller commits acceptance.
func publishRabbitConfirmed(ctx context.Context, ch *amqp.Channel, exchange, queueName string, message amqp.Publishing) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if ch == nil {
		return amqp.ErrClosed
	}
	confirmation, err := ch.PublishWithDeferredConfirmWithContext(ctx, exchange, queueName, false, false, message)
	if err != nil {
		return rabbitPublishAmbiguousError{cause: err}
	}
	if confirmation == nil {
		return rabbitPublishAmbiguousError{cause: fmt.Errorf("rabbitmq publish returned no confirmation")}
	}
	return awaitRabbitConfirmation(ctx, confirmation)
}

// rabbitPublishContext caps broker-confirmation latency while preserving any shorter caller deadline.
func rabbitPublishContext(ctx context.Context) (context.Context, context.CancelFunc, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	bounded, cancel := context.WithTimeout(ctx, rabbitPublishConfirmationTimeout)
	return bounded, cancel, nil
}

// awaitRabbitConfirmation rejects negative and ambiguous broker acknowledgements.
func awaitRabbitConfirmation(ctx context.Context, confirmation rabbitPublishConfirmation) error {
	bounded, cancel, err := rabbitPublishContext(ctx)
	if err != nil {
		return err
	}
	defer cancel()
	if confirmation == nil {
		return rabbitPublishAmbiguousError{cause: fmt.Errorf("rabbitmq publish returned no confirmation")}
	}
	acked, err := confirmation.WaitContext(bounded)
	if err != nil {
		return rabbitPublishAmbiguousError{cause: fmt.Errorf("wait for rabbitmq publish confirmation: %w", err)}
	}
	if !acked {
		return fmt.Errorf("rabbitmq broker rejected publish")
	}
	return nil
}

// isRabbitPublishAmbiguous identifies failures that may have occurred after the broker accepted a publish.
func isRabbitPublishAmbiguous(err error) bool {
	var ambiguous rabbitPublishAmbiguousError
	return errors.As(err, &ambiguous)
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
