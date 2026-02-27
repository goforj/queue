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
