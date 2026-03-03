package redisqueue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/goforj/queue"
	"github.com/goforj/queue/queuecore"
	backend "github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
)

type redisEnqueueClient interface {
	Enqueue(job *backend.Task, opts ...backend.Option) (*backend.TaskInfo, error)
	Close() error
}

type redisInspector interface {
	Queues() ([]string, error)
	GetQueueInfo(queue string) (*backend.QueueInfo, error)
	PauseQueue(queue string) error
	UnpauseQueue(queue string) error
	ListPendingTasks(queue string, opts ...backend.ListOption) ([]*backend.TaskInfo, error)
	ListActiveTasks(queue string, opts ...backend.ListOption) ([]*backend.TaskInfo, error)
	ListScheduledTasks(queue string, opts ...backend.ListOption) ([]*backend.TaskInfo, error)
	ListRetryTasks(queue string, opts ...backend.ListOption) ([]*backend.TaskInfo, error)
	ListArchivedTasks(queue string, opts ...backend.ListOption) ([]*backend.TaskInfo, error)
	ListCompletedTasks(queue string, opts ...backend.ListOption) ([]*backend.TaskInfo, error)
	GetTaskInfo(queue, id string) (*backend.TaskInfo, error)
	CancelProcessing(id string) error
	DeleteTask(queue, id string) error
	RunTask(queue, id string) error
	ArchiveTask(queue, id string) error
	DeleteAllPendingTasks(queue string) (int, error)
	DeleteAllScheduledTasks(queue string) (int, error)
	DeleteAllRetryTasks(queue string) (int, error)
	DeleteAllArchivedTasks(queue string) (int, error)
	DeleteAllCompletedTasks(queue string) (int, error)
	History(queue string, n int) ([]*backend.DailyStats, error)
}

type redisQueue struct {
	client    redisEnqueueClient
	inspector redisInspector
	timeline  redisTimelineStore

	ownsClient bool
	closeOnce  sync.Once
}

type redisTimelineStore interface {
	Get(ctx context.Context, key string) (string, error)
	Set(ctx context.Context, key string, value any, expiration time.Duration) error
}

type redisTimelineClient struct {
	client *redis.Client
}

func (c *redisTimelineClient) Get(ctx context.Context, key string) (string, error) {
	if c == nil || c.client == nil {
		return "", redis.Nil
	}
	return c.client.Get(ctx, key).Result()
}

func (c *redisTimelineClient) Set(ctx context.Context, key string, value any, expiration time.Duration) error {
	if c == nil || c.client == nil {
		return nil
	}
	return c.client.Set(ctx, key, value, expiration).Err()
}

const redisDefaultJobTimeout = 30 * time.Second

func newRedisQueue(client redisEnqueueClient, inspector redisInspector, timeline redisTimelineStore, ownsClient bool) *redisQueue {
	return &redisQueue{client: client, inspector: inspector, timeline: timeline, ownsClient: ownsClient}
}

func newRedisClient(cfg Config) redisEnqueueClient {
	return backend.NewClient(backend.RedisClientOpt{
		Addr:     cfg.Addr,
		Password: cfg.Password,
		DB:       cfg.DB,
	})
}

func newRedisInspector(cfg Config) redisInspector {
	return backend.NewInspector(backend.RedisClientOpt{
		Addr:     cfg.Addr,
		Password: cfg.Password,
		DB:       cfg.DB,
	})
}

func newRedisTimelineStore(cfg Config) redisTimelineStore {
	return &redisTimelineClient{
		client: redis.NewClient(&redis.Options{
			Addr:     cfg.Addr,
			Password: cfg.Password,
			DB:       cfg.DB,
		}),
	}
}

func (d *redisQueue) Driver() queue.Driver {
	return queue.DriverRedis
}

func (d *redisQueue) Preflight(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if d.inspector == nil {
		return fmt.Errorf("redis inspector is unavailable")
	}
	_, err := d.inspector.Queues()
	return err
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
	if err := queuecore.ValidateDriverJob(job); err != nil {
		return err
	}
	parsed := queuecore.DriverOptions(job)
	if parsed.QueueName == "" {
		return fmt.Errorf("job queue is required")
	}
	backendOpts := make([]backend.Option, 0, 5)
	backendOpts = append(backendOpts, backend.Queue(parsed.QueueName))
	if parsed.Timeout != nil {
		backendOpts = append(backendOpts, backend.Timeout(*parsed.Timeout))
	} else {
		backendOpts = append(backendOpts, backend.Timeout(redisDefaultJobTimeout))
	}
	if parsed.MaxRetry != nil {
		backendOpts = append(backendOpts, backend.MaxRetry(*parsed.MaxRetry))
	}
	if parsed.Backoff != nil && *parsed.Backoff > 0 {
		return queuecore.ErrBackoffUnsupported
	}
	if parsed.Delay > 0 {
		backendOpts = append(backendOpts, backend.ProcessIn(parsed.Delay))
	}
	if parsed.UniqueTTL > 0 {
		backendOpts = append(backendOpts, backend.Unique(parsed.UniqueTTL))
	}
	_, err := d.client.Enqueue(backend.NewTask(job.Type, job.PayloadBytes()), backendOpts...)
	if errors.Is(err, backend.ErrDuplicateTask) {
		return queuecore.ErrDuplicate
	}
	return err
}

func (d *redisQueue) Pause(_ context.Context, queueName string) error {
	if d.inspector == nil {
		return queue.ErrPauseUnsupported
	}
	return d.inspector.PauseQueue(queuecore.NormalizeQueueName(queueName))
}

func (d *redisQueue) Resume(_ context.Context, queueName string) error {
	if d.inspector == nil {
		return queue.ErrPauseUnsupported
	}
	return d.inspector.UnpauseQueue(queuecore.NormalizeQueueName(queueName))
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
		if info == nil {
			continue
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

func (d *redisQueue) ListJobs(ctx context.Context, opts queue.ListJobsOptions) (queue.ListJobsResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return queue.ListJobsResult{}, err
	}
	if d.inspector == nil {
		return queue.ListJobsResult{}, queue.ErrQueueAdminUnsupported
	}
	opts = opts.Normalize()
	queueName := queuecore.NormalizeQueueName(opts.Queue)
	listOpts := []backend.ListOption{backend.Page(opts.Page), backend.PageSize(opts.PageSize)}

	var (
		tasks []*backend.TaskInfo
		err   error
	)
	switch opts.State {
	case queue.JobStatePending:
		tasks, err = d.inspector.ListPendingTasks(queueName, listOpts...)
	case queue.JobStateActive:
		tasks, err = d.inspector.ListActiveTasks(queueName, listOpts...)
	case queue.JobStateScheduled:
		tasks, err = d.inspector.ListScheduledTasks(queueName, listOpts...)
	case queue.JobStateRetry:
		tasks, err = d.inspector.ListRetryTasks(queueName, listOpts...)
	case queue.JobStateArchived:
		tasks, err = d.inspector.ListArchivedTasks(queueName, listOpts...)
	case queue.JobStateCompleted:
		tasks, err = d.inspector.ListCompletedTasks(queueName, listOpts...)
	default:
		return queue.ListJobsResult{}, fmt.Errorf("unsupported queue job state %q", opts.State)
	}
	if err != nil {
		return queue.ListJobsResult{}, err
	}

	jobs := make([]queue.JobSnapshot, 0, len(tasks))
	for _, task := range tasks {
		if task == nil {
			continue
		}
		var nextProcessAt *time.Time
		if !task.NextProcessAt.IsZero() {
			t := task.NextProcessAt
			nextProcessAt = &t
		}
		var completedAt *time.Time
		if !task.CompletedAt.IsZero() {
			t := task.CompletedAt
			completedAt = &t
		}
		jobs = append(jobs, queue.JobSnapshot{
			ID:            task.ID,
			Queue:         task.Queue,
			State:         asynqTaskStateToJobState(task.State),
			Type:          task.Type,
			Payload:       string(task.Payload),
			Attempt:       task.Retried,
			MaxRetry:      task.MaxRetry,
			LastError:     task.LastErr,
			NextProcessAt: nextProcessAt,
			CompletedAt:   completedAt,
		})
	}

	return queue.ListJobsResult{Jobs: jobs, Total: int64(len(jobs))}, nil
}

func (d *redisQueue) RetryJob(ctx context.Context, queueName, jobID string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if d.inspector == nil {
		return queue.ErrQueueAdminUnsupported
	}
	queueName = queuecore.NormalizeQueueName(queueName)
	if queueName == "" {
		resolvedQueue, _, err := d.resolveTask(jobID)
		if err != nil {
			return err
		}
		queueName = resolvedQueue
	}
	return d.inspector.RunTask(queueName, jobID)
}

func (d *redisQueue) CancelJob(ctx context.Context, jobID string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if d.inspector == nil {
		return queue.ErrQueueAdminUnsupported
	}
	queueName, task, err := d.resolveTask(jobID)
	if err != nil {
		return err
	}
	if task.State == backend.TaskStateActive {
		return d.inspector.CancelProcessing(jobID)
	}
	return d.inspector.ArchiveTask(queueName, jobID)
}

func (d *redisQueue) DeleteJob(ctx context.Context, queueName, jobID string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if d.inspector == nil {
		return queue.ErrQueueAdminUnsupported
	}
	queueName = queuecore.NormalizeQueueName(queueName)
	if queueName == "" {
		resolvedQueue, _, err := d.resolveTask(jobID)
		if err != nil {
			return err
		}
		queueName = resolvedQueue
	}
	return d.inspector.DeleteTask(queueName, jobID)
}

func (d *redisQueue) ClearQueue(ctx context.Context, queueName string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if d.inspector == nil {
		return queue.ErrQueueAdminUnsupported
	}
	queueName = queuecore.NormalizeQueueName(queueName)

	active, err := d.inspector.ListActiveTasks(queueName, backend.Page(1), backend.PageSize(1000))
	if err != nil {
		return err
	}
	for _, task := range active {
		if task == nil || task.ID == "" {
			continue
		}
		_ = d.inspector.CancelProcessing(task.ID)
	}
	if _, err := d.inspector.DeleteAllPendingTasks(queueName); err != nil {
		return err
	}
	if _, err := d.inspector.DeleteAllScheduledTasks(queueName); err != nil {
		return err
	}
	if _, err := d.inspector.DeleteAllRetryTasks(queueName); err != nil {
		return err
	}
	if _, err := d.inspector.DeleteAllArchivedTasks(queueName); err != nil {
		return err
	}
	if _, err := d.inspector.DeleteAllCompletedTasks(queueName); err != nil {
		return err
	}
	return nil
}

func (d *redisQueue) History(ctx context.Context, queueName string, window queue.QueueHistoryWindow) ([]queue.QueueHistoryPoint, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if d.inspector == nil {
		return nil, queue.ErrQueueAdminUnsupported
	}
	queueName = queuecore.NormalizeQueueName(queueName)

	// Hour-level history should come from timeline samples (counter snapshots),
	// not daily aggregates, so short windows (15m/1h) have meaningful points.
	if window == queue.QueueHistoryHour || window == "" {
		snapshot, err := d.Stats(ctx)
		if err != nil {
			return nil, err
		}
		points := d.timelineHistory(ctx, snapshot, queueName, queue.QueueHistoryHour)
		if len(points) > 0 {
			return points, nil
		}
		return queue.SinglePointHistory(snapshot, queueName), nil
	}

	days := 1
	switch window {
	case queue.QueueHistoryWeek:
		days = 7
	case queue.QueueHistoryDay:
		days = 1
	default:
		days = 1
	}
	stats, err := d.inspector.History(queueName, days)
	if err != nil {
		return nil, err
	}
	points := make([]queue.QueueHistoryPoint, 0, len(stats))
	for _, stat := range stats {
		if stat == nil {
			continue
		}
		points = append(points, queue.QueueHistoryPoint{
			At:        stat.Date,
			Processed: int64(stat.Processed),
			Failed:    int64(stat.Failed),
		})
	}
	if len(points) > 1 {
		return points, nil
	}

	snapshot, err := d.Stats(ctx)
	if err != nil {
		if len(points) > 0 {
			return points, nil
		}
		return nil, err
	}
	points = d.timelineHistory(ctx, snapshot, queueName, window)
	if len(points) > 0 {
		return points, nil
	}
	return queue.SinglePointHistory(snapshot, queueName), nil
}

const (
	redisQueueTimelineRetention = 8 * 24 * time.Hour
	redisQueueTimelineTTL       = 9 * 24 * time.Hour
	redisQueueTimelineMaxPoints = 30000
	redisQueueTimelineMinSample = 30 * time.Second
	redisQueueTimelineKeyPrefix = "queue:history:v1:"
)

func (d *redisQueue) timelineHistory(ctx context.Context, snapshot queue.StatsSnapshot, queueName string, window queue.QueueHistoryWindow) []queue.QueueHistoryPoint {
	queueName = queuecore.NormalizeQueueName(queueName)
	if queueName == "" {
		return nil
	}
	now := time.Now()
	var points []queue.QueueHistoryPoint
	if d.timeline != nil {
		body, err := d.timeline.Get(ctx, d.timelineKey(queueName))
		if err == nil && body != "" {
			_ = json.Unmarshal([]byte(body), &points)
		}
	}
	counters, ok := snapshot.Queue(queueName)
	if ok {
		entry := queue.QueueHistoryPoint{
			At:        now,
			Processed: counters.Processed,
			Failed:    counters.Failed,
		}
		if len(points) == 0 || shouldAppendRedisTimeline(points[len(points)-1], entry, now) {
			points = append(points, entry)
		}
	}
	points = trimRedisTimeline(points, now)
	if d.timeline != nil && len(points) > 0 {
		if payload, err := json.Marshal(points); err == nil {
			_ = d.timeline.Set(ctx, d.timelineKey(queueName), payload, redisQueueTimelineTTL)
		}
	}

	cutoff := now.Add(-historyWindowDuration(window))
	out := make([]queue.QueueHistoryPoint, 0, len(points))
	for _, point := range points {
		if point.At.Before(cutoff) {
			continue
		}
		out = append(out, point)
	}
	return out
}

func shouldAppendRedisTimeline(last, next queue.QueueHistoryPoint, now time.Time) bool {
	if last.Processed != next.Processed || last.Failed != next.Failed {
		return true
	}
	return now.Sub(last.At) >= redisQueueTimelineMinSample
}

func trimRedisTimeline(points []queue.QueueHistoryPoint, now time.Time) []queue.QueueHistoryPoint {
	if len(points) == 0 {
		return points
	}
	cutoff := now.Add(-redisQueueTimelineRetention)
	start := 0
	for start < len(points) && points[start].At.Before(cutoff) {
		start++
	}
	if start > 0 {
		points = append([]queue.QueueHistoryPoint(nil), points[start:]...)
	}
	if len(points) > redisQueueTimelineMaxPoints {
		points = append([]queue.QueueHistoryPoint(nil), points[len(points)-redisQueueTimelineMaxPoints:]...)
	}
	return points
}

func historyWindowDuration(window queue.QueueHistoryWindow) time.Duration {
	switch window {
	case queue.QueueHistoryWeek:
		return 7 * 24 * time.Hour
	case queue.QueueHistoryDay:
		return 24 * time.Hour
	default:
		return time.Hour
	}
}

func (d *redisQueue) timelineKey(queueName string) string {
	return redisQueueTimelineKeyPrefix + queueName
}

func (d *redisQueue) resolveTask(jobID string) (string, *backend.TaskInfo, error) {
	names, err := d.inspector.Queues()
	if err != nil {
		return "", nil, err
	}
	for _, queueName := range names {
		task, getErr := d.inspector.GetTaskInfo(queueName, jobID)
		if getErr == nil && task != nil {
			return queueName, task, nil
		}
		if getErr != nil && !errors.Is(getErr, backend.ErrTaskNotFound) {
			return "", nil, getErr
		}
	}
	return "", nil, backend.ErrTaskNotFound
}

func boolToInt64(v bool) int64 {
	if v {
		return 1
	}
	return 0
}

func asynqTaskStateToJobState(state backend.TaskState) queue.JobState {
	switch state {
	case backend.TaskStatePending:
		return queue.JobStatePending
	case backend.TaskStateActive:
		return queue.JobStateActive
	case backend.TaskStateScheduled:
		return queue.JobStateScheduled
	case backend.TaskStateRetry:
		return queue.JobStateRetry
	case backend.TaskStateArchived:
		return queue.JobStateArchived
	case backend.TaskStateCompleted:
		return queue.JobStateCompleted
	default:
		return queue.JobStatePending
	}
}
