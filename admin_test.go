package queue

import (
	"context"
	"errors"
	"testing"
)

func TestQueueAdminHelpers_UnsupportedOnSync(t *testing.T) {
	q, err := NewSync()
	if err != nil {
		t.Fatalf("new sync queue failed: %v", err)
	}

	if SupportsQueueAdmin(q) {
		t.Fatal("expected sync queue to not support queue admin")
	}

	if _, err := q.ListJobs(context.Background(), ListJobsOptions{Queue: "default", State: JobStatePending}); !errors.Is(err, ErrQueueAdminUnsupported) {
		t.Fatalf("expected queue admin unsupported for ListJobs, got %v", err)
	}
	if err := q.RetryJob(context.Background(), "default", "job-1"); !errors.Is(err, ErrQueueAdminUnsupported) {
		t.Fatalf("expected queue admin unsupported for RetryJob, got %v", err)
	}
	if err := q.CancelJob(context.Background(), "job-1"); !errors.Is(err, ErrQueueAdminUnsupported) {
		t.Fatalf("expected queue admin unsupported for CancelJob, got %v", err)
	}
	if err := q.DeleteJob(context.Background(), "default", "job-1"); !errors.Is(err, ErrQueueAdminUnsupported) {
		t.Fatalf("expected queue admin unsupported for DeleteJob, got %v", err)
	}
	if err := q.ClearQueue(context.Background(), "default"); !errors.Is(err, ErrQueueAdminUnsupported) {
		t.Fatalf("expected queue admin unsupported for ClearQueue, got %v", err)
	}
	if history, err := q.History(context.Background(), "default", QueueHistoryHour); err != nil {
		t.Fatalf("expected history to be supported on sync queue, got %v", err)
	} else if len(history) == 0 {
		t.Fatalf("expected at least one history point, got %+v", history)
	}

	if _, err := ListJobs(context.Background(), q, ListJobsOptions{Queue: "default", State: JobStatePending}); !errors.Is(err, ErrQueueAdminUnsupported) {
		t.Fatalf("expected package ListJobs unsupported, got %v", err)
	}
	if err := RetryJob(context.Background(), q, "default", "job-1"); !errors.Is(err, ErrQueueAdminUnsupported) {
		t.Fatalf("expected package RetryJob unsupported, got %v", err)
	}
	if err := CancelJob(context.Background(), q, "job-1"); !errors.Is(err, ErrQueueAdminUnsupported) {
		t.Fatalf("expected package CancelJob unsupported, got %v", err)
	}
	if err := DeleteJob(context.Background(), q, "default", "job-1"); !errors.Is(err, ErrQueueAdminUnsupported) {
		t.Fatalf("expected package DeleteJob unsupported, got %v", err)
	}
	if err := ClearQueue(context.Background(), q, "default"); !errors.Is(err, ErrQueueAdminUnsupported) {
		t.Fatalf("expected package ClearQueue unsupported, got %v", err)
	}
	if history, err := QueueHistory(context.Background(), q, "default", QueueHistoryHour); err != nil {
		t.Fatalf("expected package QueueHistory supported on sync queue, got %v", err)
	} else if len(history) == 0 {
		t.Fatalf("expected package QueueHistory points, got %+v", history)
	}
}

func TestListJobsOptionsNormalize(t *testing.T) {
	got := (ListJobsOptions{}).Normalize()
	if got.Queue != "default" || got.State != JobStatePending || got.Page != 1 || got.PageSize != 50 {
		t.Fatalf("unexpected normalized options: %+v", got)
	}
}

type adminBackendRecorder struct {
	lastListQueue    string
	lastRetryQueue   string
	lastDeleteQueue  string
	lastClearQueue   string
	lastHistoryQueue string
}

func (b *adminBackendRecorder) Driver() Driver { return DriverRedis }
func (b *adminBackendRecorder) Dispatch(context.Context, Job) error {
	return nil
}
func (b *adminBackendRecorder) Shutdown(context.Context) error {
	return nil
}
func (b *adminBackendRecorder) ListJobs(_ context.Context, opts ListJobsOptions) (ListJobsResult, error) {
	b.lastListQueue = opts.Queue
	return ListJobsResult{}, nil
}
func (b *adminBackendRecorder) RetryJob(_ context.Context, queueName, _ string) error {
	b.lastRetryQueue = queueName
	return nil
}
func (b *adminBackendRecorder) CancelJob(context.Context, string) error {
	return nil
}
func (b *adminBackendRecorder) DeleteJob(_ context.Context, queueName, _ string) error {
	b.lastDeleteQueue = queueName
	return nil
}
func (b *adminBackendRecorder) ClearQueue(_ context.Context, queueName string) error {
	b.lastClearQueue = queueName
	return nil
}
func (b *adminBackendRecorder) History(_ context.Context, queueName string, _ QueueHistoryWindow) ([]QueueHistoryPoint, error) {
	b.lastHistoryQueue = queueName
	return nil, nil
}

func TestQueueAdminHelpersPhysicalizeTargetQueues(t *testing.T) {
	inner := &adminBackendRecorder{}
	q := &Queue{
		q: &externalQueueRuntime{
			common: &queueCommon{
				inner:  inner,
				cfg:    Config{DefaultQueue: "billing_default"},
				driver: DriverRedis,
			},
		},
	}

	if _, err := ListJobs(context.Background(), q, ListJobsOptions{Queue: "reports"}); err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if err := RetryJob(context.Background(), q, "reports", "job-1"); err != nil {
		t.Fatalf("RetryJob: %v", err)
	}
	if err := DeleteJob(context.Background(), q, "reports", "job-1"); err != nil {
		t.Fatalf("DeleteJob: %v", err)
	}
	if err := ClearQueue(context.Background(), q, "reports"); err != nil {
		t.Fatalf("ClearQueue: %v", err)
	}
	if _, err := QueueHistory(context.Background(), q, "reports", QueueHistoryHour); err != nil {
		t.Fatalf("QueueHistory: %v", err)
	}

	if inner.lastListQueue != "billing_reports" || inner.lastRetryQueue != "billing_reports" ||
		inner.lastDeleteQueue != "billing_reports" || inner.lastClearQueue != "billing_reports" ||
		inner.lastHistoryQueue != "billing_reports" {
		t.Fatalf("expected admin queues to physicalize, got list=%q retry=%q delete=%q clear=%q history=%q",
			inner.lastListQueue, inner.lastRetryQueue, inner.lastDeleteQueue, inner.lastClearQueue, inner.lastHistoryQueue)
	}
}
