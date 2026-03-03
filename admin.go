package queue

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// JobState identifies queue job state used by queue admin APIs.
// @group Admin
type JobState string

const (
	JobStatePending   JobState = "pending"
	JobStateActive    JobState = "active"
	JobStateScheduled JobState = "scheduled"
	JobStateRetry     JobState = "retry"
	JobStateArchived  JobState = "archived"
	JobStateCompleted JobState = "completed"
)

// QueueHistoryWindow identifies queue history horizon.
// @group Admin
type QueueHistoryWindow string

const (
	QueueHistoryHour QueueHistoryWindow = "hour"
	QueueHistoryDay  QueueHistoryWindow = "day"
	QueueHistoryWeek QueueHistoryWindow = "week"
)

// ListJobsOptions configures queue admin list jobs calls.
// @group Admin
type ListJobsOptions struct {
	Queue    string
	State    JobState
	Page     int
	PageSize int
}

// Normalize returns a safe options payload with defaults applied.
// @group Admin
//
// Example: normalize list options
//
//	opts := queue.ListJobsOptions{Queue: "", State: "", Page: 0, PageSize: 1000}
//	normalized := opts.Normalize()
//	fmt.Println(normalized.Queue, normalized.State, normalized.Page, normalized.PageSize)
//	// Output: default pending 1 500
func (o ListJobsOptions) Normalize() ListJobsOptions {
	if o.Queue == "" {
		o.Queue = "default"
	}
	if o.State == "" {
		o.State = JobStatePending
	}
	if o.Page <= 0 {
		o.Page = 1
	}
	if o.PageSize <= 0 {
		o.PageSize = 50
	}
	if o.PageSize > 500 {
		o.PageSize = 500
	}
	return o
}

// JobSnapshot describes an admin-facing queue job record.
// @group Admin
type JobSnapshot struct {
	ID            string
	Queue         string
	State         JobState
	Type          string
	Payload       string
	Attempt       int
	MaxRetry      int
	LastError     string
	NextProcessAt *time.Time
	CompletedAt   *time.Time
}

// ListJobsResult contains queue admin job listing output.
// @group Admin
type ListJobsResult struct {
	Jobs  []JobSnapshot
	Total int64
}

// QueueHistoryPoint represents processed/failed totals at a point in time.
// @group Admin
type QueueHistoryPoint struct {
	At        time.Time
	Processed int64
	Failed    int64
}

// QueueAdmin exposes optional queue admin capabilities.
// @group Admin
type QueueAdmin interface {
	ListJobs(ctx context.Context, opts ListJobsOptions) (ListJobsResult, error)
	RetryJob(ctx context.Context, queueName, jobID string) error
	CancelJob(ctx context.Context, jobID string) error
	DeleteJob(ctx context.Context, queueName, jobID string) error
	ClearQueue(ctx context.Context, queueName string) error
	History(ctx context.Context, queueName string, window QueueHistoryWindow) ([]QueueHistoryPoint, error)
}

// QueueHistoryProvider exposes queue history capability independently from full queue admin support.
// Drivers like sync/workerpool can provide history without supporting per-job admin operations.
// @group Admin
type QueueHistoryProvider interface {
	History(ctx context.Context, queueName string, window QueueHistoryWindow) ([]QueueHistoryPoint, error)
}

// ErrQueueAdminUnsupported indicates queue admin operations are unsupported by a driver.
// @group Admin
var ErrQueueAdminUnsupported = errors.New("queue admin is not supported by this driver")

// SupportsQueueAdmin reports whether queue admin operations are available.
// @group Admin
//
// Example: detect admin support
//
//	q, err := redisqueue.New("127.0.0.1:6379")
//	if err != nil {
//		return
//	}
//	fmt.Println(queue.SupportsQueueAdmin(q))
//	// Output: true
func SupportsQueueAdmin(q any) bool {
	return resolveQueueAdmin(q) != nil
}

// ListJobs lists jobs for a queue and state when supported.
// @group Admin
//
// Example: list jobs via helper
//
//	q, err := redisqueue.New("127.0.0.1:6379")
//	if err != nil {
//		return
//	}
//	_, err = queue.ListJobs(context.Background(), q, queue.ListJobsOptions{
//		Queue: "default",
//		State: queue.JobStatePending,
//	})
//	_ = err
func ListJobs(ctx context.Context, q any, opts ListJobsOptions) (ListJobsResult, error) {
	admin := resolveQueueAdmin(q)
	if admin == nil {
		return ListJobsResult{}, ErrQueueAdminUnsupported
	}
	return admin.ListJobs(ctx, opts)
}

// RetryJob retries (runs now) a job when supported.
// @group Admin
//
// Example: retry a job via helper
//
//	q, err := redisqueue.New("127.0.0.1:6379")
//	if err != nil {
//		return
//	}
//	err = queue.RetryJob(context.Background(), q, "default", "job-id")
//	_ = err
func RetryJob(ctx context.Context, q any, queueName, jobID string) error {
	admin := resolveQueueAdmin(q)
	if admin == nil {
		return ErrQueueAdminUnsupported
	}
	return admin.RetryJob(ctx, queueName, jobID)
}

// CancelJob cancels a job when supported.
// @group Admin
//
// Example: cancel a job via helper
//
//	q, err := redisqueue.New("127.0.0.1:6379")
//	if err != nil {
//		return
//	}
//	err = queue.CancelJob(context.Background(), q, "job-id")
//	_ = err
func CancelJob(ctx context.Context, q any, jobID string) error {
	admin := resolveQueueAdmin(q)
	if admin == nil {
		return ErrQueueAdminUnsupported
	}
	return admin.CancelJob(ctx, jobID)
}

// DeleteJob deletes a job when supported.
// @group Admin
//
// Example: delete a job via helper
//
//	q, err := redisqueue.New("127.0.0.1:6379")
//	if err != nil {
//		return
//	}
//	err = queue.DeleteJob(context.Background(), q, "default", "job-id")
//	_ = err
func DeleteJob(ctx context.Context, q any, queueName, jobID string) error {
	admin := resolveQueueAdmin(q)
	if admin == nil {
		return ErrQueueAdminUnsupported
	}
	return admin.DeleteJob(ctx, queueName, jobID)
}

// ClearQueue clears queue jobs when supported.
// @group Admin
//
// Example: clear queue via helper
//
//	q, err := redisqueue.New("127.0.0.1:6379")
//	if err != nil {
//		return
//	}
//	err = queue.ClearQueue(context.Background(), q, "default")
//	_ = err
func ClearQueue(ctx context.Context, q any, queueName string) error {
	admin := resolveQueueAdmin(q)
	if admin == nil {
		return ErrQueueAdminUnsupported
	}
	return admin.ClearQueue(ctx, queueName)
}

// QueueHistory returns queue history points when supported.
// @group Admin
//
// Example: history via helper
//
//	q, err := redisqueue.New("127.0.0.1:6379")
//	if err != nil {
//		return
//	}
//	_, err = queue.QueueHistory(context.Background(), q, "default", queue.QueueHistoryHour)
//	_ = err
func QueueHistory(ctx context.Context, q any, queueName string, window QueueHistoryWindow) ([]QueueHistoryPoint, error) {
	history := resolveQueueHistory(q)
	if history == nil {
		return nil, ErrQueueAdminUnsupported
	}
	return history.History(ctx, queueName, window)
}

func resolveQueueAdmin(v any) QueueAdmin {
	raw := resolveQueueRuntime(v)
	if raw == nil {
		return nil
	}
	if admin, ok := raw.(QueueAdmin); ok {
		return admin
	}
	switch rt := raw.(type) {
	case *nativeQueueRuntime:
		if rt == nil || rt.common == nil {
			return nil
		}
		if admin, ok := rt.common.inner.(QueueAdmin); ok {
			return admin
		}
	case *externalQueueRuntime:
		if rt == nil || rt.common == nil {
			return nil
		}
		if admin, ok := rt.common.inner.(QueueAdmin); ok {
			return admin
		}
	}
	return nil
}

func resolveQueueHistory(v any) QueueHistoryProvider {
	raw := resolveQueueRuntime(v)
	if raw == nil {
		return nil
	}
	if history, ok := raw.(QueueHistoryProvider); ok {
		return history
	}
	switch rt := raw.(type) {
	case *nativeQueueRuntime:
		if rt == nil || rt.common == nil {
			return nil
		}
		if history, ok := rt.common.inner.(QueueHistoryProvider); ok {
			return history
		}
	case *externalQueueRuntime:
		if rt == nil || rt.common == nil {
			return nil
		}
		if history, ok := rt.common.inner.(QueueHistoryProvider); ok {
			return history
		}
	}
	return nil
}

// ListJobs lists jobs via queue admin capability when supported.
// @group Admin
//
// Example: queue method list jobs
//
//	q, err := redisqueue.New("127.0.0.1:6379")
//	if err != nil {
//		return
//	}
//	_, err = q.ListJobs(context.Background(), queue.ListJobsOptions{
//		Queue: "default",
//		State: queue.JobStatePending,
//	})
//	_ = err
func (r *Queue) ListJobs(ctx context.Context, opts ListJobsOptions) (ListJobsResult, error) {
	if r == nil || r.q == nil {
		return ListJobsResult{}, fmt.Errorf("runtime is nil")
	}
	admin := resolveQueueAdmin(r)
	if admin == nil {
		return ListJobsResult{}, ErrQueueAdminUnsupported
	}
	return admin.ListJobs(ctx, opts)
}

// RetryJob retries (runs now) a job via queue admin capability when supported.
// @group Admin
//
// Example: queue method retry job
//
//	q, err := redisqueue.New("127.0.0.1:6379")
//	if err != nil {
//		return
//	}
//	if !queue.SupportsQueueAdmin(q) {
//		return
//	}
//	err = q.RetryJob(context.Background(), "default", "job-id")
//	_ = err
func (r *Queue) RetryJob(ctx context.Context, queueName, jobID string) error {
	if r == nil || r.q == nil {
		return fmt.Errorf("runtime is nil")
	}
	admin := resolveQueueAdmin(r)
	if admin == nil {
		return ErrQueueAdminUnsupported
	}
	return admin.RetryJob(ctx, queueName, jobID)
}

// CancelJob cancels a job via queue admin capability when supported.
// @group Admin
//
// Example: queue method cancel job
//
//	q, err := redisqueue.New("127.0.0.1:6379")
//	if err != nil {
//		return
//	}
//	if !queue.SupportsQueueAdmin(q) {
//		return
//	}
//	err = q.CancelJob(context.Background(), "job-id")
//	_ = err
func (r *Queue) CancelJob(ctx context.Context, jobID string) error {
	if r == nil || r.q == nil {
		return fmt.Errorf("runtime is nil")
	}
	admin := resolveQueueAdmin(r)
	if admin == nil {
		return ErrQueueAdminUnsupported
	}
	return admin.CancelJob(ctx, jobID)
}

// DeleteJob deletes a job via queue admin capability when supported.
// @group Admin
//
// Example: queue method delete job
//
//	q, err := redisqueue.New("127.0.0.1:6379")
//	if err != nil {
//		return
//	}
//	if !queue.SupportsQueueAdmin(q) {
//		return
//	}
//	err = q.DeleteJob(context.Background(), "default", "job-id")
//	_ = err
func (r *Queue) DeleteJob(ctx context.Context, queueName, jobID string) error {
	if r == nil || r.q == nil {
		return fmt.Errorf("runtime is nil")
	}
	admin := resolveQueueAdmin(r)
	if admin == nil {
		return ErrQueueAdminUnsupported
	}
	return admin.DeleteJob(ctx, queueName, jobID)
}

// ClearQueue clears queue jobs via queue admin capability when supported.
// @group Admin
//
// Example: queue method clear queue
//
//	q, err := redisqueue.New("127.0.0.1:6379")
//	if err != nil {
//		return
//	}
//	if !queue.SupportsQueueAdmin(q) {
//		return
//	}
//	err = q.ClearQueue(context.Background(), "default")
//	_ = err
func (r *Queue) ClearQueue(ctx context.Context, queueName string) error {
	if r == nil || r.q == nil {
		return fmt.Errorf("runtime is nil")
	}
	admin := resolveQueueAdmin(r)
	if admin == nil {
		return ErrQueueAdminUnsupported
	}
	return admin.ClearQueue(ctx, queueName)
}

// History returns queue history points via queue admin capability when supported.
// @group Admin
//
// Example: queue method history
//
//	q, err := redisqueue.New("127.0.0.1:6379")
//	if err != nil {
//		return
//	}
//	points, err := q.History(context.Background(), "default", queue.QueueHistoryHour)
//	_ = points
//	_ = err
func (r *Queue) History(ctx context.Context, queueName string, window QueueHistoryWindow) ([]QueueHistoryPoint, error) {
	if r == nil || r.q == nil {
		return nil, fmt.Errorf("runtime is nil")
	}
	history := resolveQueueHistory(r)
	if history == nil {
		return nil, ErrQueueAdminUnsupported
	}
	return history.History(ctx, queueName, window)
}

func singlePointHistory(snapshot StatsSnapshot, queueName string) []QueueHistoryPoint {
	queueName = normalizeQueueName(queueName)
	counters, ok := snapshot.Queue(queueName)
	if !ok {
		return nil
	}
	return []QueueHistoryPoint{{
		At:        time.Now(),
		Processed: counters.Processed,
		Failed:    counters.Failed,
	}}
}

// SinglePointHistory converts a snapshot into a single current-history point.
// This helper is intended for driver modules that do not expose historical buckets.
// @group Admin
//
// Example: single-point history
//
//	snapshot := queue.StatsSnapshot{
//		ByQueue: map[string]queue.QueueCounters{
//			"default": {Processed: 12, Failed: 1},
//		},
//	}
//	points := queue.SinglePointHistory(snapshot, "default")
//	fmt.Println(len(points), points[0].Processed, points[0].Failed)
//	// Output: 1 12 1
func SinglePointHistory(snapshot StatsSnapshot, queueName string) []QueueHistoryPoint {
	return singlePointHistory(snapshot, queueName)
}
