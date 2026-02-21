package queue

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/hibiken/asynq"
)

type redisEnqueueClient interface {
	Enqueue(task *asynq.Task, opts ...asynq.Option) (*asynq.TaskInfo, error)
	Close() error
}

type redisInspector interface {
	Queues() ([]string, error)
	GetQueueInfo(queue string) (*asynq.QueueInfo, error)
	PauseQueue(queue string) error
	UnpauseQueue(queue string) error
}

type redisQueue struct {
	client    redisEnqueueClient
	inspector redisInspector

	ownsClient bool
	closeOnce  sync.Once
}

const redisDefaultTaskTimeout = 30 * time.Second

func newRedisQueue(client redisEnqueueClient, inspector redisInspector, ownsClient bool) queueBackend {
	return &redisQueue{client: client, inspector: inspector, ownsClient: ownsClient}
}

func newAsynqClient(cfg Config) redisEnqueueClient {
	return asynq.NewClient(asynq.RedisClientOpt{
		Addr:     cfg.RedisAddr,
		Password: cfg.RedisPassword,
		DB:       cfg.RedisDB,
	})
}

func newAsynqInspector(cfg Config) redisInspector {
	return asynq.NewInspector(asynq.RedisClientOpt{
		Addr:     cfg.RedisAddr,
		Password: cfg.RedisPassword,
		DB:       cfg.RedisDB,
	})
}

func (d *redisQueue) Driver() Driver {
	return DriverRedis
}

func (d *redisQueue) Shutdown(_ context.Context) error {
	if d.ownsClient && d.client != nil {
		d.closeOnce.Do(func() {
			_ = d.client.Close()
		})
	}
	return nil
}

func (d *redisQueue) Dispatch(_ context.Context, task Job) error {
	if d.client == nil {
		return fmt.Errorf("queue client unavailable for redis driver")
	}
	if err := task.validate(); err != nil {
		return err
	}
	parsed := task.enqueueOptions()
	if parsed.queueName == "" {
		return fmt.Errorf("task queue is required")
	}
	asynqOpts := make([]asynq.Option, 0, 5)
	asynqOpts = append(asynqOpts, asynq.Queue(parsed.queueName))
	if parsed.timeout != nil {
		asynqOpts = append(asynqOpts, asynq.Timeout(*parsed.timeout))
	} else {
		asynqOpts = append(asynqOpts, asynq.Timeout(redisDefaultTaskTimeout))
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
	_, err := d.client.Enqueue(asynq.NewTask(task.Type, task.PayloadBytes()), asynqOpts...)
	if errors.Is(err, asynq.ErrDuplicateTask) {
		return ErrDuplicate
	}
	return err
}

func (d *redisQueue) Pause(_ context.Context, queueName string) error {
	if d.inspector == nil {
		return ErrPauseUnsupported
	}
	return d.inspector.PauseQueue(normalizeQueueName(queueName))
}

func (d *redisQueue) Resume(_ context.Context, queueName string) error {
	if d.inspector == nil {
		return ErrPauseUnsupported
	}
	return d.inspector.UnpauseQueue(normalizeQueueName(queueName))
}

func (d *redisQueue) Stats(_ context.Context) (StatsSnapshot, error) {
	if d.inspector == nil {
		return StatsSnapshot{}, fmt.Errorf("redis inspector is unavailable")
	}
	names, err := d.inspector.Queues()
	if err != nil {
		return StatsSnapshot{}, err
	}
	byQueue := make(map[string]QueueCounters, len(names))
	throughput := make(map[string]QueueThroughput, len(names))
	for _, name := range names {
		info, infoErr := d.inspector.GetQueueInfo(name)
		if infoErr != nil {
			return StatsSnapshot{}, infoErr
		}
		byQueue[name] = QueueCounters{
			Pending:   int64(info.Pending),
			Active:    int64(info.Active),
			Scheduled: int64(info.Scheduled),
			Retry:     int64(info.Retry),
			Archived:  int64(info.Archived),
			Processed: int64(info.Processed),
			Failed:    int64(info.Failed),
			Paused:    boolToInt64(info.Paused),
			AvgWait:   info.Latency,
		}
		throughput[name] = QueueThroughput{
			Hour: ThroughputWindow{Processed: int64(info.Processed), Failed: int64(info.Failed)},
			Day:  ThroughputWindow{Processed: int64(info.Processed), Failed: int64(info.Failed)},
			Week: ThroughputWindow{Processed: int64(info.Processed), Failed: int64(info.Failed)},
		}
	}
	return StatsSnapshot{ByQueue: byQueue, ThroughputByQueue: throughput}, nil
}

func boolToInt64(v bool) int64 {
	if v {
		return 1
	}
	return 0
}
