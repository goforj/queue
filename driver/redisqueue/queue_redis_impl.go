package redisqueue

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/goforj/queue"
	"github.com/hibiken/asynq"
)

type redisEnqueueClient interface {
	Enqueue(job *asynq.Task, opts ...asynq.Option) (*asynq.TaskInfo, error)
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

const redisDefaultJobTimeout = 30 * time.Second

func newRedisQueue(client redisEnqueueClient, inspector redisInspector, ownsClient bool) queue.DriverQueueBackend {
	return &redisQueue{client: client, inspector: inspector, ownsClient: ownsClient}
}

func newAsynqClient(cfg queue.Config) redisEnqueueClient {
	return asynq.NewClient(asynq.RedisClientOpt{
		Addr:     cfg.RedisAddr,
		Password: cfg.RedisPassword,
		DB:       cfg.RedisDB,
	})
}

func newAsynqInspector(cfg queue.Config) redisInspector {
	return asynq.NewInspector(asynq.RedisClientOpt{
		Addr:     cfg.RedisAddr,
		Password: cfg.RedisPassword,
		DB:       cfg.RedisDB,
	})
}

func (d *redisQueue) Driver() queue.Driver {
	return queue.DriverRedis
}

func (d *redisQueue) Shutdown(_ context.Context) error {
	if d.ownsClient && d.client != nil {
		d.closeOnce.Do(func() {
			_ = d.client.Close()
		})
	}
	return nil
}

func (d *redisQueue) Dispatch(_ context.Context, job queue.Job) error {
	if d.client == nil {
		return fmt.Errorf("queue client unavailable for redis driver")
	}
	if err := queue.ValidateDriverJob(job); err != nil {
		return err
	}
	parsed := queue.DriverOptions(job)
	if parsed.QueueName == "" {
		return fmt.Errorf("job queue is required")
	}
	asynqOpts := make([]asynq.Option, 0, 5)
	asynqOpts = append(asynqOpts, asynq.Queue(parsed.QueueName))
	if parsed.Timeout != nil {
		asynqOpts = append(asynqOpts, asynq.Timeout(*parsed.Timeout))
	} else {
		asynqOpts = append(asynqOpts, asynq.Timeout(redisDefaultJobTimeout))
	}
	if parsed.MaxRetry != nil {
		asynqOpts = append(asynqOpts, asynq.MaxRetry(*parsed.MaxRetry))
	}
	if parsed.Backoff != nil && *parsed.Backoff > 0 {
		return queue.ErrBackoffUnsupported
	}
	if parsed.Delay > 0 {
		asynqOpts = append(asynqOpts, asynq.ProcessIn(parsed.Delay))
	}
	if parsed.UniqueTTL > 0 {
		asynqOpts = append(asynqOpts, asynq.Unique(parsed.UniqueTTL))
	}
	_, err := d.client.Enqueue(asynq.NewTask(job.Type, job.PayloadBytes()), asynqOpts...)
	if errors.Is(err, asynq.ErrDuplicateTask) {
		return queue.ErrDuplicate
	}
	return err
}

func (d *redisQueue) Pause(_ context.Context, queueName string) error {
	if d.inspector == nil {
		return queue.ErrPauseUnsupported
	}
	return d.inspector.PauseQueue(queue.NormalizeQueueName(queueName))
}

func (d *redisQueue) Resume(_ context.Context, queueName string) error {
	if d.inspector == nil {
		return queue.ErrPauseUnsupported
	}
	return d.inspector.UnpauseQueue(queue.NormalizeQueueName(queueName))
}

func (d *redisQueue) Stats(_ context.Context) (queue.StatsSnapshot, error) {
	if d.inspector == nil {
		return queue.StatsSnapshot{}, fmt.Errorf("redis inspector is unavailable")
	}
	names, err := d.inspector.Queues()
	if err != nil {
		return queue.StatsSnapshot{}, err
	}
	byQueue := make(map[string]queue.QueueCounters, len(names))
	throughput := make(map[string]queue.QueueThroughput, len(names))
	for _, name := range names {
		info, infoErr := d.inspector.GetQueueInfo(name)
		if infoErr != nil {
			return queue.StatsSnapshot{}, infoErr
		}
		byQueue[name] = queue.QueueCounters{
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
		throughput[name] = queue.QueueThroughput{
			Hour: queue.ThroughputWindow{Processed: int64(info.Processed), Failed: int64(info.Failed)},
			Day:  queue.ThroughputWindow{Processed: int64(info.Processed), Failed: int64(info.Failed)},
			Week: queue.ThroughputWindow{Processed: int64(info.Processed), Failed: int64(info.Failed)},
		}
	}
	return queue.StatsSnapshot{ByQueue: byQueue, ThroughputByQueue: throughput}, nil
}

func boolToInt64(v bool) int64 {
	if v {
		return 1
	}
	return 0
}
