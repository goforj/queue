package natsqueue

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/goforj/queue"
	"github.com/goforj/queue/internal/uniqueness"
	"github.com/goforj/queue/queuecore"
	"github.com/nats-io/nats.go"
)

const natsPublishFlushTimeout = 5 * time.Second

type natsMessage struct {
	Type          string          `json:"type"`
	Payload       []byte          `json:"payload,omitempty"`
	Queue         string          `json:"queue"`
	Metadata      json.RawMessage `json:"metadata,omitempty"`
	Attempt       int             `json:"attempt,omitempty"`
	MaxRetry      int             `json:"max_retry,omitempty"`
	BackoffMillis int64           `json:"backoff_millis,omitempty"`
	TimeoutMillis int64           `json:"timeout_millis,omitempty"`
	AvailableAtMS int64           `json:"available_at_ms,omitempty"`
	PublishedAtMS int64           `json:"published_at_ms,omitempty"`
}

type natsConnection interface {
	Publish(subject string, data []byte) error
	FlushWithContext(ctx context.Context) error
	Drain() error
	Close()
}

type synchronousNATSConnection struct {
	*nats.Conn
}

// Drain waits for the asynchronous Core NATS drain to close the connection.
func (c *synchronousNATSConnection) Drain() error {
	if c == nil || c.Conn == nil || c.IsClosed() {
		return nil
	}
	closed := c.StatusChanged(nats.CLOSED)
	defer c.RemoveStatusListener(closed)
	if err := c.Conn.Drain(); err != nil {
		return err
	}
	for status := range closed {
		if status == nats.CLOSED {
			return nil
		}
	}
	return nil
}

type natsQueue struct {
	url string

	mu sync.Mutex
	nc natsConnection

	unique uniqueness.MemoryStore
}

func (q *natsQueue) Driver() queue.Driver {
	return queue.DriverNATS
}

func (q *natsQueue) Preflight(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	nc, err := q.connection()
	if err != nil {
		return err
	}
	return nc.FlushWithContext(ctx)
}

func newNATSQueue(url string) *natsQueue {
	return &natsQueue{url: url}
}

// ensureConn establishes at most one shared Core NATS connection.
func (q *natsQueue) ensureConn() error {
	_, err := q.connection()
	return err
}

// connection returns the connection established while holding the same lock used by Shutdown.
func (q *natsQueue) connection() (natsConnection, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.nc != nil {
		return q.nc, nil
	}
	nc, err := nats.Connect(q.url)
	if err != nil {
		return nil, err
	}
	q.nc = &synchronousNATSConnection{Conn: nc}
	return q.nc, nil
}

func (q *natsQueue) Shutdown(_ context.Context) error {
	q.mu.Lock()
	nc := q.nc
	q.nc = nil
	q.mu.Unlock()
	if nc != nil {
		// Every accepted publish already completed a flush, so producer shutdown only needs to prevent reuse and close the socket.
		nc.Close()
	}
	return nil
}

// Dispatch flushes initial publication so acceptance includes a Core NATS server roundtrip.
func (q *natsQueue) Dispatch(ctx context.Context, job queue.Job) error {
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
	nc, err := q.connection()
	if err != nil {
		return err
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

	msg, err := natsMessageForJob(job, parsed)
	if err != nil {
		q.unique.Release(uniqueKey, uniqueToken)
		return err
	}

	payload, err := json.Marshal(msg)
	if err != nil {
		q.unique.Release(uniqueKey, uniqueToken)
		return err
	}
	err = nc.Publish(natsSubject(parsed.QueueName), payload)
	if err != nil {
		q.unique.Release(uniqueKey, uniqueToken)
		return err
	}
	flushCtx, cancel := natsPublishContext(ctx)
	defer cancel()
	// A flush proves only that the Core NATS server observed this ephemeral publish, not durable storage.
	return nc.FlushWithContext(flushCtx)
}

// natsMessageForJob converts one validated queue job into the stable NATS wire
// representation while keeping direct-delivery metadata optional.
func natsMessageForJob(job queue.Job, options queue.DriverJobOptions) (natsMessage, error) {
	message := natsMessage{
		Type:          job.Type,
		Payload:       job.PayloadBytes(),
		Queue:         options.QueueName,
		PublishedAtMS: time.Now().UnixMilli(),
	}
	metadata := queue.DriverMetadata(job)
	if metadata.SchemaVersion != 0 {
		encoded, err := json.Marshal(metadata)
		if err != nil {
			return natsMessage{}, fmt.Errorf("encode NATS driver job metadata: %w", err)
		}
		message.Metadata = encoded
	}
	if options.MaxRetry != nil {
		message.MaxRetry = *options.MaxRetry
	}
	if options.Backoff != nil && *options.Backoff > 0 {
		message.BackoffMillis = options.Backoff.Milliseconds()
	}
	if options.Timeout != nil && *options.Timeout > 0 {
		message.TimeoutMillis = options.Timeout.Milliseconds()
	}
	if options.Delay > 0 {
		message.AvailableAtMS = time.Now().Add(options.Delay).UnixMilli()
	}
	return message, nil
}

// claimUnique returns the ownership token needed to compensate a rejected publish.
func (q *natsQueue) claimUnique(job queue.Job, queueName string, ttl time.Duration) (string, uint64, bool) {
	key := queuecore.UniqueKey(job, queueName)
	token, ok := q.unique.Acquire(key, ttl)
	return key, token, ok
}

// natsSubject maps one physical queue onto its Core NATS subject.
func natsSubject(queueName string) string {
	return "queue." + queueName
}

// natsPublishContext bounds server-roundtrip latency while retaining a shorter caller deadline.
func natsPublishContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(ctx, natsPublishFlushTimeout)
}
