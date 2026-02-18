package queue

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/hibiken/asynq"
)

type redisEnqueueClient interface {
	Enqueue(task *asynq.Task, opts ...asynq.Option) (*asynq.TaskInfo, error)
	Close() error
}

type redisDispatcher struct {
	client redisEnqueueClient

	ownsClient bool
	closeOnce  sync.Once
}

func newRedisDispatcher(client redisEnqueueClient, ownsClient bool) Queue {
	return &redisDispatcher{client: client, ownsClient: ownsClient}
}

func newAsynqClient(cfg QueueConfig) redisEnqueueClient {
	return asynq.NewClient(asynq.RedisClientOpt{
		Addr:     cfg.RedisAddr,
		Password: cfg.RedisPassword,
		DB:       cfg.RedisDB,
	})
}

func (d *redisDispatcher) Driver() Driver {
	return DriverRedis
}

func (d *redisDispatcher) Register(_ string, _ Handler) {
	// No-op for redis dispatcher; workers register handlers on server mux.
}

func (d *redisDispatcher) Start(_ context.Context) error {
	return nil
}

func (d *redisDispatcher) Shutdown(_ context.Context) error {
	if d.ownsClient && d.client != nil {
		d.closeOnce.Do(func() {
			_ = d.client.Close()
		})
	}
	return nil
}

func (d *redisDispatcher) Dispatch(taskType string, payload []byte, opts ...Option) error {
	return d.DispatchCtx(context.Background(), taskType, payload, opts...)
}

func (d *redisDispatcher) DispatchCtx(_ context.Context, taskType string, payload []byte, opts ...Option) error {
	if d.client == nil {
		return fmt.Errorf("queue client unavailable for redis driver")
	}
	if taskType == "" {
		return fmt.Errorf("task type is required")
	}
	parsed := resolveOptions(opts...)
	asynqOpts := make([]asynq.Option, 0, 5)
	if parsed.queueName != "" {
		asynqOpts = append(asynqOpts, asynq.Queue(parsed.queueName))
	}
	if parsed.timeout != nil {
		asynqOpts = append(asynqOpts, asynq.Timeout(*parsed.timeout))
	}
	if parsed.maxRetry != nil {
		asynqOpts = append(asynqOpts, asynq.MaxRetry(*parsed.maxRetry))
	}
	if parsed.backoff != nil && *parsed.backoff > 0 {
		return ErrBackoffUnsupported
	}
	if parsed.delay > 0 {
		asynqOpts = append(asynqOpts, asynq.ProcessIn(parsed.delay))
	}
	if parsed.uniqueTTL > 0 {
		asynqOpts = append(asynqOpts, asynq.Unique(parsed.uniqueTTL))
	}
	_, err := d.client.Enqueue(asynq.NewTask(taskType, payload), asynqOpts...)
	if errors.Is(err, asynq.ErrDuplicateTask) {
		return ErrDuplicate
	}
	return err
}
