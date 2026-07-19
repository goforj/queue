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

// Close releases the inspector stub without external resources.
func (s *redisInspectorStub) Close() error { return nil }

type redisEnqueueClientStub struct {
	enqueueErr error
	enqueueN   int
	closeN     int
	task       *backend.Task
	opts       []backend.Option
}

type redisUniqueStoreStub struct {
	acquired     bool
	acquireErr   error
	releaseErr   error
	acquireKey   string
	acquireToken string
	releaseKey   string
	releaseToken string
}

// Acquire records the logical claim requested by the queue.
func (s *redisUniqueStoreStub) Acquire(_ context.Context, key, token string, _ time.Duration) (bool, error) {
	s.acquireKey = key
	s.acquireToken = token
	return s.acquired, s.acquireErr
}

// Release records the ownership token used for compensation.
func (s *redisUniqueStoreStub) Release(_ context.Context, key, token string) error {
	s.releaseKey = key
	s.releaseToken = token
	return s.releaseErr
}

// Enqueue records the task and options passed through the Redis acceptance boundary.
func (s *redisEnqueueClientStub) Enqueue(task *backend.Task, opts ...backend.Option) (*backend.TaskInfo, error) {
	s.enqueueN++
	s.task = task
	s.opts = append([]backend.Option(nil), opts...)
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
					{
						ID:       "job-pending",
						Queue:    "default",
						Type:     "job:pending",
						Payload:  []byte("payload"),
						State:    backend.TaskStatePending,
						MaxRetry: 3,
						Headers:  map[string]string{redisApplicationMaxRetryHeader: "2"},
					},
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
	if list.Jobs[0].MaxRetry != 2 {
		t.Fatalf("admin max retry = %d, want application budget 2", list.Jobs[0].MaxRetry)
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
		if got := client.task.Headers()[redisApplicationMaxRetryHeader]; got != "2" {
			t.Fatalf("application retry header = %q, want 2", got)
		}
		var transportMaxRetry int
		for _, option := range client.opts {
			if option.Type() == backend.MaxRetryOpt {
				transportMaxRetry = option.Value().(int)
			}
		}
		if transportMaxRetry != 3 {
			t.Fatalf("transport max retry = %d, want one-slot reserve 3", transportMaxRetry)
		}
	})

	t.Run("retry reserve wire boundary", func(t *testing.T) {
		client := &redisEnqueueClientStub{}
		r := &redisQueue{client: client}
		if err := r.Dispatch(context.Background(), queue.NewJob("job:redis").OnQueue("default").Retry(redisMaximumApplicationRetry)); err != nil {
			t.Fatalf("maximum application retry rejected: %v", err)
		}
		if client.enqueueN != 1 {
			t.Fatalf("maximum application retry enqueues = %d, want 1", client.enqueueN)
		}
		err := r.Dispatch(context.Background(), queue.NewJob("job:redis").OnQueue("default").Retry(redisMaximumApplicationRetry+1))
		if err == nil || client.enqueueN != 1 {
			t.Fatalf("retry reserve overflow = error:%v total enqueues:%d, want rejection after first boundary enqueue", err, client.enqueueN)
		}
	})

	t.Run("unique ttl validates before canonical claim", func(t *testing.T) {
		client := &redisEnqueueClientStub{}
		claims := &redisUniqueStoreStub{acquired: true}
		r := &redisQueue{client: client, unique: claims}
		err := r.Dispatch(context.Background(), queue.NewJob("job:redis").OnQueue("default").UniqueFor(time.Millisecond))
		if err == nil {
			t.Fatal("sub-second redis uniqueness unexpectedly passed validation")
		}
		if claims.acquireKey != "" || client.enqueueN != 0 {
			t.Fatalf("invalid uniqueness reached claim/enqueue: key=%q enqueues=%d", claims.acquireKey, client.enqueueN)
		}
	})
}

// TestRedisQueueDispatchInputAndClaimFailures verifies context normalization,
// Redis-specific type validation, and claim-store failures stop at the expected boundary.
func TestRedisQueueDispatchInputAndClaimFailures(t *testing.T) {
	t.Run("nil context", func(t *testing.T) {
		client := &redisEnqueueClientStub{}
		r := &redisQueue{client: client}
		if err := r.Dispatch(nil, queue.NewJob("job:nil-context").OnQueue("default")); err != nil {
			t.Fatalf("dispatch with nil context: %v", err)
		}
		if client.enqueueN != 1 {
			t.Fatalf("enqueue calls = %d, want 1", client.enqueueN)
		}
	})

	t.Run("whitespace type", func(t *testing.T) {
		client := &redisEnqueueClientStub{}
		r := &redisQueue{client: client}
		if err := r.Dispatch(context.Background(), queue.NewJob(" \t").OnQueue("default")); err == nil {
			t.Fatal("expected whitespace-only Redis type rejection")
		}
		if client.enqueueN != 0 {
			t.Fatalf("invalid type reached enqueue %d times", client.enqueueN)
		}
	})

	t.Run("claim store failure", func(t *testing.T) {
		acquireErr := errors.New("claim store unavailable")
		client := &redisEnqueueClientStub{}
		claims := &redisUniqueStoreStub{acquireErr: acquireErr}
		r := &redisQueue{client: client, unique: claims}
		err := r.Dispatch(context.Background(), queue.NewJob("job:claim").OnQueue("default").UniqueFor(time.Minute))
		if !errors.Is(err, acquireErr) {
			t.Fatalf("dispatch error = %v, want %v", err, acquireErr)
		}
		if claims.acquireKey == "" || claims.acquireToken == "" || client.enqueueN != 0 {
			t.Fatalf("claim failure state = key:%q token:%q enqueues:%d", claims.acquireKey, claims.acquireToken, client.enqueueN)
		}
	})
}

// TestRedisQueueLogicalUniqueFailureBoundaries verifies ambiguous failures retain claims while definite physical duplicates compensate them.
func TestRedisQueueLogicalUniqueFailureBoundaries(t *testing.T) {
	payload := []byte(`{"schema_version":1,"dispatch_id":"volatile","job_id":"job_1","job":{"type":"reports:build","payload":"eyJpZCI6MX0="}}`)
	job := queue.NewJob("bus:job").Payload(payload).OnQueue("critical").UniqueFor(time.Minute)
	enqueueErr := errors.New("redis response lost")
	client := &redisEnqueueClientStub{enqueueErr: enqueueErr}
	claims := &redisUniqueStoreStub{acquired: true}
	r := &redisQueue{client: client, unique: claims}

	err := r.Dispatch(context.Background(), job)
	if !errors.Is(err, enqueueErr) {
		t.Fatalf("dispatch error = %v, want enqueue rejection", err)
	}
	if claims.acquireKey == "" || claims.acquireToken == "" {
		t.Fatalf("logical claim was incomplete: %+v", claims)
	}
	if claims.releaseKey != "" || claims.releaseToken != "" {
		t.Fatalf("ambiguous enqueue failure released its safety claim: %+v", claims)
	}

	client = &redisEnqueueClientStub{enqueueErr: backend.ErrDuplicateTask}
	claims = &redisUniqueStoreStub{acquired: true}
	r = &redisQueue{client: client, unique: claims}
	if err := r.Dispatch(context.Background(), job); !errors.Is(err, queue.ErrDuplicate) {
		t.Fatalf("physical duplicate error = %v, want ErrDuplicate", err)
	}
	if claims.releaseKey != claims.acquireKey || claims.releaseToken != claims.acquireToken {
		t.Fatalf("physical duplicate compensation released a different owner: %+v", claims)
	}

	releaseErr := errors.New("redis release failed")
	claims = &redisUniqueStoreStub{acquired: true, releaseErr: releaseErr}
	r = &redisQueue{client: &redisEnqueueClientStub{enqueueErr: backend.ErrDuplicateTask}, unique: claims}
	err = r.Dispatch(context.Background(), job)
	if !errors.Is(err, queue.ErrDuplicate) || !errors.Is(err, releaseErr) {
		t.Fatalf("release failure error = %v, want duplicate and release causes", err)
	}

	claims = &redisUniqueStoreStub{acquired: false}
	r = &redisQueue{client: &redisEnqueueClientStub{}, unique: claims}
	if err := r.Dispatch(context.Background(), job); !errors.Is(err, queue.ErrDuplicate) {
		t.Fatalf("duplicate logical claim error = %v, want ErrDuplicate", err)
	}
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
