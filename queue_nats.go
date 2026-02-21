package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

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

func (q *natsQueue) Driver() Driver {
	return DriverNATS
}

func newNATSQueue(url string) queueBackend {
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

func (q *natsQueue) Dispatch(_ context.Context, task Job) error {
	if err := task.validate(); err != nil {
		return err
	}
	parsed := task.enqueueOptions()
	if parsed.queueName == "" {
		return fmt.Errorf("task queue is required")
	}
	if q.nc == nil {
		if err := q.ensureConn(); err != nil {
			return err
		}
	}
	if parsed.uniqueTTL > 0 && !q.claimUnique(task, parsed.queueName, parsed.uniqueTTL) {
		return ErrDuplicate
	}

	msg := natsMessage{
		Type:          task.Type,
		Payload:       task.PayloadBytes(),
		Queue:         parsed.queueName,
		PublishedAtMS: time.Now().UnixMilli(),
	}
	if parsed.maxRetry != nil {
		msg.MaxRetry = *parsed.maxRetry
	}
	if parsed.backoff != nil && *parsed.backoff > 0 {
		msg.BackoffMillis = parsed.backoff.Milliseconds()
	}
	if parsed.timeout != nil && *parsed.timeout > 0 {
		msg.TimeoutMillis = parsed.timeout.Milliseconds()
	}
	if parsed.delay > 0 {
		msg.AvailableAtMS = time.Now().Add(parsed.delay).UnixMilli()
	}

	payload, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return q.nc.Publish(natsSubject(parsed.queueName), payload)
}

func (q *natsQueue) claimUnique(task Job, queueName string, ttl time.Duration) bool {
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

func natsSubject(queueName string) string {
	return "queue." + queueName
}
