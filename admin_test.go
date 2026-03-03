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
