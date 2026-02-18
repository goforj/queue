package queue

import (
	"context"
	"errors"
)

// Task is the backend-agnostic queue payload envelope.
// @group Task
//
// Example: task
//
//	task := queue.Task{Type: "emails:send", Payload: []byte(`{"to":"user@example.com"}`)}
//	_ = task
type Task struct {
	Type    string
	Payload []byte
}

// Handler processes a task.
// @group Task
//
// Example: handler
//
//	handler := func(ctx context.Context, task queue.Task) error { return nil }
//	_ = handler
type Handler func(ctx context.Context, task Task) error

// ErrDuplicate indicates a duplicate unique task enqueue.
var ErrDuplicate = errors.New("duplicate task")

// ErrQueuerShuttingDown indicates dispatch was rejected during shutdown.
var ErrQueuerShuttingDown = errors.New("queuer is shutting down")

// ErrWorkerpoolQueueNotInitialized indicates workerpool queue is unavailable.
var ErrWorkerpoolQueueNotInitialized = errors.New("workerpool queue not initialized")

// ErrBackoffUnsupported indicates requested backoff is unsupported by a driver.
var ErrBackoffUnsupported = errors.New("backoff option is not supported by this driver")
