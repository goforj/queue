package redisqueue

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/goforj/queue"
	backend "github.com/hibiken/asynq"
)

type redisInspectorStub struct {
	pauseQueueArg   string
	unpauseQueueArg string
	queues          []string
	queueInfos      map[string]*backend.QueueInfo
	queuesErr       error
	infoErr         error
	pauseErr        error
	unpauseErr      error
	tasksByQueue    map[string]map[string][]*backend.TaskInfo
	history         []*backend.DailyStats
	historyErr      error
	canceledJobID   string
	deletedTaskID   string
	runTaskID       string
	archivedTaskID  string
	deleteAllErr    error
}

type redisEnqueueClientStub struct {
	enqueueErr error
	enqueueN   int
	closeN     int
}

func (s *redisEnqueueClientStub) Enqueue(*backend.Task, ...backend.Option) (*backend.TaskInfo, error) {
	s.enqueueN++
	return &backend.TaskInfo{}, s.enqueueErr
}

func (s *redisEnqueueClientStub) Close() error {
	s.closeN++
	return nil
}

func (s *redisInspectorStub) Queues() ([]string, error) {
	if s.queuesErr != nil {
		return nil, s.queuesErr
	}
	return s.queues, nil
}

func (s *redisInspectorStub) GetQueueInfo(string) (*backend.QueueInfo, error) {
	if s.infoErr != nil {
		return nil, s.infoErr
	}
	if s.queueInfos == nil {
		return nil, nil
	}
	for _, info := range s.queueInfos {
		return info, nil
	}
	return nil, nil
}

func (s *redisInspectorStub) PauseQueue(queueName string) error {
	s.pauseQueueArg = queueName
	return s.pauseErr
}

func (s *redisInspectorStub) UnpauseQueue(queueName string) error {
	s.unpauseQueueArg = queueName
	return s.unpauseErr
}

func (s *redisInspectorStub) tasks(queueName string, state backend.TaskState) []*backend.TaskInfo {
	if s.tasksByQueue == nil {
		return nil
	}
	if perQueue, ok := s.tasksByQueue[queueName]; ok {
		return perQueue[state.String()]
	}
	return nil
}

func (s *redisInspectorStub) ListPendingTasks(queue string, _ ...backend.ListOption) ([]*backend.TaskInfo, error) {
	return s.tasks(queue, backend.TaskStatePending), nil
}

func (s *redisInspectorStub) ListActiveTasks(queue string, _ ...backend.ListOption) ([]*backend.TaskInfo, error) {
	return s.tasks(queue, backend.TaskStateActive), nil
}

func (s *redisInspectorStub) ListScheduledTasks(queue string, _ ...backend.ListOption) ([]*backend.TaskInfo, error) {
	return s.tasks(queue, backend.TaskStateScheduled), nil
}

func (s *redisInspectorStub) ListRetryTasks(queue string, _ ...backend.ListOption) ([]*backend.TaskInfo, error) {
	return s.tasks(queue, backend.TaskStateRetry), nil
}

func (s *redisInspectorStub) ListArchivedTasks(queue string, _ ...backend.ListOption) ([]*backend.TaskInfo, error) {
	return s.tasks(queue, backend.TaskStateArchived), nil
}

func (s *redisInspectorStub) ListCompletedTasks(queue string, _ ...backend.ListOption) ([]*backend.TaskInfo, error) {
	return s.tasks(queue, backend.TaskStateCompleted), nil
}

func (s *redisInspectorStub) CancelProcessing(id string) error {
	s.canceledJobID = id
	return nil
}

func (s *redisInspectorStub) DeleteTask(_ string, id string) error {
	s.deletedTaskID = id
	return nil
}

func (s *redisInspectorStub) RunTask(_ string, id string) error {
	s.runTaskID = id
	return nil
}

func (s *redisInspectorStub) ArchiveTask(_ string, id string) error {
	s.archivedTaskID = id
	return nil
}

func (s *redisInspectorStub) DeleteAllPendingTasks(_ string) (int, error) {
	return 0, s.deleteAllErr
}

func (s *redisInspectorStub) DeleteAllScheduledTasks(_ string) (int, error) {
	return 0, s.deleteAllErr
}

func (s *redisInspectorStub) DeleteAllRetryTasks(_ string) (int, error) {
	return 0, s.deleteAllErr
}

func (s *redisInspectorStub) DeleteAllArchivedTasks(_ string) (int, error) {
	return 0, s.deleteAllErr
}

func (s *redisInspectorStub) DeleteAllCompletedTasks(_ string) (int, error) {
	return 0, s.deleteAllErr
}

func (s *redisInspectorStub) History(_ string, _ int) ([]*backend.DailyStats, error) {
	return s.history, s.historyErr
}

func (s *redisInspectorStub) GetTaskInfo(queueName, id string) (*backend.TaskInfo, error) {
	states := []backend.TaskState{
		backend.TaskStatePending,
		backend.TaskStateActive,
		backend.TaskStateScheduled,
		backend.TaskStateRetry,
		backend.TaskStateArchived,
		backend.TaskStateCompleted,
	}
	for _, state := range states {
		for _, task := range s.tasks(queueName, state) {
			if task != nil && task.ID == id {
				return task, nil
			}
		}
	}
	return nil, backend.ErrTaskNotFound
}

func TestRedisQueue_PauseResumeNormalization(t *testing.T) {
	inspector := &redisInspectorStub{}
	r := &redisQueue{inspector: inspector}
	if err := r.Pause(context.Background(), ""); err != nil {
		t.Fatalf("pause failed: %v", err)
	}
	if inspector.pauseQueueArg != "default" {
		t.Fatalf("expected pause default queue normalization, got %q", inspector.pauseQueueArg)
	}
	if err := r.Resume(context.Background(), "critical"); err != nil {
		t.Fatalf("resume failed: %v", err)
	}
	if inspector.unpauseQueueArg != "critical" {
		t.Fatalf("expected resume explicit queue, got %q", inspector.unpauseQueueArg)
	}
}

func TestRedisQueue_PauseResumeErrors(t *testing.T) {
	pauseErr := errors.New("pause failed")
	resumeErr := errors.New("resume failed")
	inspector := &redisInspectorStub{pauseErr: pauseErr, unpauseErr: resumeErr}
	r := &redisQueue{inspector: inspector}

	if err := r.Pause(context.Background(), "default"); !errors.Is(err, pauseErr) {
		t.Fatalf("expected pause error %v, got %v", pauseErr, err)
	}
	if err := r.Resume(context.Background(), "default"); !errors.Is(err, resumeErr) {
		t.Fatalf("expected resume error %v, got %v", resumeErr, err)
	}
}

func TestRedisQueue_StatsBranches(t *testing.T) {
	t.Run("inspector unavailable", func(t *testing.T) {
		r := &redisQueue{}
		if _, err := r.Stats(context.Background()); err == nil {
			t.Fatal("expected stats error when inspector unavailable")
		}
	})

	t.Run("queues error", func(t *testing.T) {
		r := &redisQueue{inspector: &redisInspectorStub{queuesErr: errors.New("queues failed")}}
		if _, err := r.Stats(context.Background()); err == nil {
			t.Fatal("expected queues error")
		}
	})

	t.Run("success", func(t *testing.T) {
		inspector := &redisInspectorStub{
			queues: []string{"default"},
			queueInfos: map[string]*backend.QueueInfo{
				"default": {
					Queue:     "default",
					Pending:   1,
					Active:    2,
					Scheduled: 3,
					Retry:     4,
					Archived:  5,
					Processed: 6,
					Failed:    7,
					Paused:    true,
				},
			},
		}
		r := &redisQueue{inspector: inspector}
		snap, err := r.Stats(context.Background())
		if err != nil {
			t.Fatalf("stats failed: %v", err)
		}
		if got := snap.Pending("default"); got != 1 {
			t.Fatalf("expected pending=1, got %d", got)
		}
		if got := snap.Paused("default"); got != 1 {
			t.Fatalf("expected paused=1, got %d", got)
		}
	})
}

func TestRedisQueue_AdminBranches(t *testing.T) {
	inspector := &redisInspectorStub{
		queues: []string{"default"},
		queueInfos: map[string]*backend.QueueInfo{
			"default": {
				Queue:     "default",
				Processed: 3,
				Failed:    1,
			},
		},
		tasksByQueue: map[string]map[string][]*backend.TaskInfo{
			"default": {
				backend.TaskStatePending.String(): {
					{ID: "job-pending", Queue: "default", Type: "job:pending", Payload: []byte("payload"), State: backend.TaskStatePending},
				},
			},
		},
		history: []*backend.DailyStats{{Queue: "default", Processed: 3, Failed: 1, Date: time.Now()}},
	}
	r := &redisQueue{inspector: inspector}

	list, err := r.ListJobs(context.Background(), queue.ListJobsOptions{Queue: "default", State: queue.JobStatePending})
	if err != nil {
		t.Fatalf("list jobs failed: %v", err)
	}
	if list.Total != 1 || len(list.Jobs) != 1 {
		t.Fatalf("expected one job, got total=%d len=%d", list.Total, len(list.Jobs))
	}

	if err := r.CancelJob(context.Background(), "job-pending"); err != nil {
		t.Fatalf("cancel job failed: %v", err)
	}
	if inspector.archivedTaskID != "job-pending" {
		t.Fatalf("expected archived task id to be set, got %q", inspector.archivedTaskID)
	}

	if err := r.RetryJob(context.Background(), "default", "job-pending"); err != nil {
		t.Fatalf("retry job failed: %v", err)
	}
	if inspector.runTaskID != "job-pending" {
		t.Fatalf("expected run task id to be set, got %q", inspector.runTaskID)
	}

	if err := r.DeleteJob(context.Background(), "default", "job-pending"); err != nil {
		t.Fatalf("delete job failed: %v", err)
	}
	if inspector.deletedTaskID != "job-pending" {
		t.Fatalf("expected deleted task id to be set, got %q", inspector.deletedTaskID)
	}

	if err := r.ClearQueue(context.Background(), "default"); err != nil {
		t.Fatalf("clear queue failed: %v", err)
	}

	history, err := r.History(context.Background(), "default", queue.QueueHistoryDay)
	if err != nil {
		t.Fatalf("history failed: %v", err)
	}
	if len(history) != 1 || history[0].Processed != 3 || history[0].Failed != 1 {
		t.Fatalf("unexpected history payload: %+v", history)
	}
}

func TestRedisQueue_DispatchBranches(t *testing.T) {
	t.Run("client unavailable", func(t *testing.T) {
		r := &redisQueue{}
		if err := r.Dispatch(context.Background(), queue.NewJob("job:redis").OnQueue("default")); err == nil {
			t.Fatal("expected client unavailable error")
		}
	})

	t.Run("validation and queue required", func(t *testing.T) {
		r := &redisQueue{client: &redisEnqueueClientStub{}}
		if err := r.Dispatch(context.Background(), queue.NewJob("")); err == nil {
			t.Fatal("expected validation error")
		}
		if err := r.Dispatch(context.Background(), queue.NewJob("job:redis")); err == nil {
			t.Fatal("expected queue required error")
		}
	})

	t.Run("backoff unsupported", func(t *testing.T) {
		r := &redisQueue{client: &redisEnqueueClientStub{}}
		if err := r.Dispatch(context.Background(), queue.NewJob("job:redis").OnQueue("default").Backoff(time.Second)); !errors.Is(err, queue.ErrBackoffUnsupported) {
			t.Fatalf("expected backoff unsupported, got %v", err)
		}
	})

	t.Run("duplicate mapping", func(t *testing.T) {
		client := &redisEnqueueClientStub{enqueueErr: backend.ErrDuplicateTask}
		r := &redisQueue{client: client}
		if err := r.Dispatch(context.Background(), queue.NewJob("job:redis").OnQueue("default")); !errors.Is(err, queue.ErrDuplicate) {
			t.Fatalf("expected duplicate mapping error, got %v", err)
		}
	})

	t.Run("success", func(t *testing.T) {
		client := &redisEnqueueClientStub{}
		r := &redisQueue{client: client}
		err := r.Dispatch(context.Background(), queue.NewJob("job:redis").
			OnQueue("default").
			Retry(2).
			Timeout(5*time.Second).
			UniqueFor(time.Minute))
		if err != nil {
			t.Fatalf("dispatch success path failed: %v", err)
		}
		if client.enqueueN != 1 {
			t.Fatalf("expected one enqueue call, got %d", client.enqueueN)
		}
	})
}

func TestRedisQueue_ShutdownOwnsClientCloseOnce(t *testing.T) {
	client := &redisEnqueueClientStub{}
	r := &redisQueue{client: client, ownsClient: true}
	if err := r.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown failed: %v", err)
	}
	if err := r.Shutdown(context.Background()); err != nil {
		t.Fatalf("second shutdown failed: %v", err)
	}
	if client.closeN != 1 {
		t.Fatalf("expected close once, got %d", client.closeN)
	}
}
