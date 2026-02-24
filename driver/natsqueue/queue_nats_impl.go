package natsqueue

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/goforj/queue"
	"github.com/goforj/queue/queuecore"
	"github.com/nats-io/nats.go"
)

type natsMessage struct {
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

type natsQueue struct {
	url string
	nc  *nats.Conn

	mu     sync.Mutex
	unique map[string]time.Time
}

func (q *natsQueue) Driver() queue.Driver {
	return queue.DriverNATS
}

func newNATSQueue(url string) *natsQueue {
	return &natsQueue{
		url:    url,
		unique: make(map[string]time.Time),
	}
}

func (q *natsQueue) ensureConn() error {
	if q.nc != nil {
		return nil
	}
	nc, err := nats.Connect(q.url)
	if err != nil {
		return err
	}
	q.nc = nc
	return nil
}

func (q *natsQueue) Shutdown(_ context.Context) error {
	if q.nc != nil {
		q.nc.Drain()
		q.nc.Close()
		q.nc = nil
	}
	return nil
}

func (q *natsQueue) Dispatch(_ context.Context, job queue.Job) error {
	if err := queuecore.ValidateDriverJob(job); err != nil {
		return err
	}
	parsed := queuecore.DriverOptions(job)
	if parsed.QueueName == "" {
		return fmt.Errorf("job queue is required")
	}
	if q.nc == nil {
		if err := q.ensureConn(); err != nil {
			return err
		}
	}
	if parsed.UniqueTTL > 0 && !q.claimUnique(job, parsed.QueueName, parsed.UniqueTTL) {
		return queuecore.ErrDuplicate
	}

	msg := natsMessage{
		Type:          job.Type,
		Payload:       job.PayloadBytes(),
		Queue:         parsed.QueueName,
		PublishedAtMS: time.Now().UnixMilli(),
	}
	if parsed.MaxRetry != nil {
		msg.MaxRetry = *parsed.MaxRetry
	}
	if parsed.Backoff != nil && *parsed.Backoff > 0 {
		msg.BackoffMillis = parsed.Backoff.Milliseconds()
	}
	if parsed.Timeout != nil && *parsed.Timeout > 0 {
		msg.TimeoutMillis = parsed.Timeout.Milliseconds()
	}
	if parsed.Delay > 0 {
		msg.AvailableAtMS = time.Now().Add(parsed.Delay).UnixMilli()
	}

	payload, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return q.nc.Publish(natsSubject(parsed.QueueName), payload)
}

func (q *natsQueue) claimUnique(job queue.Job, queueName string, ttl time.Duration) bool {
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

func natsSubject(queueName string) string {
	return "queue." + queueName
}
