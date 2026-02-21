package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"
)

// Job is a pure queue payload value plus enqueue metadata.
// @group Job
//
// Example: task
//
//	job := queue.NewJob("emails:send").
//		PayloadJSON(map[string]string{"to": "user@example.com"}).
//		OnQueue("critical")
//	_ = task
type Job struct {
	Type string

	payload  []byte
	options  taskOptions
	buildErr error
}

type taskOptions struct {
	queueName string
	timeout   *time.Duration
	maxRetry  *int
	attempt   int
	backoff   *time.Duration
	delay     time.Duration
	uniqueTTL time.Duration
}

// NewJob creates a task value with a required task type.
// @group Job
//
// Example: new task
//
//	job := queue.NewJob("emails:send")
//	_ = task
func NewJob(taskType string) Job {
	return Job{Type: taskType}
}

// Payload sets task payload from common value types.
// @group Job
//
// Example: payload bytes
//
//	taskBytes := queue.NewJob("emails:send").Payload([]byte(`{"id":1}`))
//	_ = taskBytes
//
// Example: payload struct
//
//	type Meta struct {
//		Nested bool `json:"nested"`
//	}
//	type EmailPayload struct {
//		ID   int    `json:"id"`
//		To   string `json:"to"`
//		Meta Meta   `json:"meta"`
//	}
//	taskStruct := queue.NewJob("emails:send").Payload(EmailPayload{
//		ID:   1,
//		To:   "user@example.com",
//		Meta: Meta{Nested: true},
//	})
//	_ = taskStruct
//
// Example: payload map
//
//	taskMap := queue.NewJob("emails:send").Payload(map[string]any{
//		"id":  1,
//		"to":  "user@example.com",
//		"meta": map[string]any{"nested": true},
//	})
//	_ = taskMap
func (t Job) Payload(payload any) Job {
	encoded, err := encodePayload(payload)
	if err != nil {
		t.buildErr = err
		return t
	}
	t.payload = encoded
	return t
}

// PayloadJSON marshals payload as JSON.
// @group Job
//
// Example: payload json
//
//	job := queue.NewJob("emails:send").PayloadJSON(map[string]int{"id": 1})
//	_ = task
func (t Job) PayloadJSON(v any) Job {
	payload, err := encodePayload(v)
	if err != nil {
		t.buildErr = err
		return t
	}
	t.payload = payload
	return t
}

// OnQueue sets the target queue name.
// @group Job
//
// Example: on queue
//
//	job := queue.NewJob("emails:send").OnQueue("critical")
//	_ = task
func (t Job) OnQueue(name string) Job {
	t.options.queueName = name
	return t
}

// Timeout sets per-task execution timeout.
// @group Job
//
// Example: timeout
//
//	job := queue.NewJob("emails:send").Timeout(10 * time.Second)
//	_ = task
func (t Job) Timeout(timeout time.Duration) Job {
	if timeout < 0 {
		return t.withBuildErr(fmt.Errorf("timeout must be >= 0"))
	}
	t.options.timeout = &timeout
	return t
}

// Retry sets max retry attempts.
// @group Job
//
// Example: retry
//
//	job := queue.NewJob("emails:send").Retry(4)
//	_ = task
func (t Job) Retry(maxRetry int) Job {
	if maxRetry < 0 {
		return t.withBuildErr(fmt.Errorf("retry must be >= 0"))
	}
	t.options.maxRetry = &maxRetry
	return t
}

// Backoff sets delay between retries.
// @group Job
//
// Example: backoff
//
//	job := queue.NewJob("emails:send").Backoff(500 * time.Millisecond)
//	_ = task
func (t Job) Backoff(backoff time.Duration) Job {
	if backoff < 0 {
		return t.withBuildErr(fmt.Errorf("backoff must be >= 0"))
	}
	t.options.backoff = &backoff
	return t
}

// Delay defers execution by duration.
// @group Job
//
// Example: delay
//
//	job := queue.NewJob("emails:send").Delay(300 * time.Millisecond)
//	_ = task
func (t Job) Delay(delay time.Duration) Job {
	if delay < 0 {
		return t.withBuildErr(fmt.Errorf("delay must be >= 0"))
	}
	t.options.delay = delay
	return t
}

// UniqueFor enables uniqueness dedupe within the given TTL.
// @group Job
//
// Example: unique for
//
//	job := queue.NewJob("emails:send").UniqueFor(45 * time.Second)
//	_ = task
func (t Job) UniqueFor(ttl time.Duration) Job {
	if ttl < 0 {
		return t.withBuildErr(fmt.Errorf("unique ttl must be >= 0"))
	}
	t.options.uniqueTTL = ttl
	return t
}

// PayloadBytes returns a copy of task payload bytes.
// @group Job
//
// Example: payload bytes read
//
//	job := queue.NewJob("emails:send").Payload([]byte(`{"id":1}`))
//	payload := task.PayloadBytes()
//	_ = payload
func (t Job) PayloadBytes() []byte {
	return append([]byte(nil), t.payload...)
}

// Bind unmarshals task payload JSON into dst.
// @group Job
//
// Example: bind payload
//
//	type EmailPayload struct {
//		ID int `json:"id"`
//	}
//	job := queue.NewJob("emails:send").Payload(EmailPayload{ID: 1})
//	var payload EmailPayload
//	_ = task.Bind(&payload)
func (t Job) Bind(dst any) error {
	if dst == nil {
		return fmt.Errorf("bind destination is required")
	}
	v := reflect.ValueOf(dst)
	if v.Kind() != reflect.Pointer || v.IsNil() {
		return fmt.Errorf("bind destination must be a non-nil pointer")
	}
	if err := json.Unmarshal(t.PayloadBytes(), dst); err != nil {
		return fmt.Errorf("bind payload: %w", err)
	}
	return nil
}

func encodePayload(payload any) ([]byte, error) {
	if payload == nil {
		return nil, nil
	}
	switch v := payload.(type) {
	case []byte:
		return append([]byte(nil), v...), nil
	case string:
		return []byte(v), nil
	case json.RawMessage:
		return append([]byte(nil), v...), nil
	}
	if marshaler, ok := payload.(interface{ MarshalJSON() ([]byte, error) }); ok {
		encoded, err := marshaler.MarshalJSON()
		if err != nil {
			return nil, fmt.Errorf("marshal payload json: %w", err)
		}
		return encoded, nil
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal payload json: %w", err)
	}
	return encoded, nil
}

func (t Job) validate() error {
	if t.buildErr != nil {
		return t.buildErr
	}
	if t.Type == "" {
		return fmt.Errorf("task type is required")
	}
	return nil
}

func (t Job) enqueueOptions() taskOptions {
	return t.options
}

func (t Job) withBuildErr(err error) Job {
	if t.buildErr == nil {
		t.buildErr = err
	}
	return t
}

func (t Job) withAttempt(attempt int) Job {
	t.options.attempt = attempt
	return t
}

// Handler processes a task.
// @group Job
//
// Example: handler
//
//	handler := func(ctx context.Context, task queue.Job) error { return nil }
//	_ = handler
type Handler func(ctx context.Context, task Job) error

// ErrDuplicate indicates a duplicate unique task enqueue.
var ErrDuplicate = errors.New("duplicate task")

// ErrQueuerShuttingDown indicates enqueue was rejected during shutdown.
var ErrQueuerShuttingDown = errors.New("queue is shutting down")

// ErrWorkerpoolQueueNotInitialized indicates workerpool queue is unavailable.
var ErrWorkerpoolQueueNotInitialized = errors.New("workerpool queue not initialized")

// ErrBackoffUnsupported indicates requested backoff is unsupported by a driver.
var ErrBackoffUnsupported = errors.New("backoff option is not supported by this driver")

// ErrQueuePaused indicates enqueue was rejected because queue is paused.
var ErrQueuePaused = errors.New("queue is paused")
