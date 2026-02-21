package queue

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/hibiken/asynq"
)

type sqsClientStub struct {
	getQueueOut    *sqs.GetQueueUrlOutput
	getQueueErr    error
	createQueueOut *sqs.CreateQueueOutput
	createQueueErr error
	sendOut        *sqs.SendMessageOutput
	sendErr        error
	sendInput      *sqs.SendMessageInput
	getCalls       int
	createCalls    int
	sendCalls      int
}

func (s *sqsClientStub) GetQueueUrl(_ context.Context, _ *sqs.GetQueueUrlInput, _ ...func(*sqs.Options)) (*sqs.GetQueueUrlOutput, error) {
	s.getCalls++
	return s.getQueueOut, s.getQueueErr
}
func (s *sqsClientStub) CreateQueue(_ context.Context, _ *sqs.CreateQueueInput, _ ...func(*sqs.Options)) (*sqs.CreateQueueOutput, error) {
	s.createCalls++
	return s.createQueueOut, s.createQueueErr
}
func (s *sqsClientStub) SendMessage(context.Context, *sqs.SendMessageInput, ...func(*sqs.Options)) (*sqs.SendMessageOutput, error) {
	s.sendCalls++
	if s.sendOut == nil {
		s.sendOut = &sqs.SendMessageOutput{}
	}
	return s.sendOut, s.sendErr
}
func (s *sqsClientStub) ReceiveMessage(context.Context, *sqs.ReceiveMessageInput, ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error) {
	return &sqs.ReceiveMessageOutput{}, nil
}
func (s *sqsClientStub) DeleteMessage(context.Context, *sqs.DeleteMessageInput, ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error) {
	return &sqs.DeleteMessageOutput{}, nil
}

type redisInspectorStub struct {
	pauseQueueArg   string
	unpauseQueueArg string
	queues          []string
	queueInfos      map[string]*asynq.QueueInfo
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

func (s *redisEnqueueClientStub) Enqueue(*asynq.Task, ...asynq.Option) (*asynq.TaskInfo, error) {
	s.enqueueN++
	return &asynq.TaskInfo{}, s.enqueueErr
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
func (s *redisInspectorStub) GetQueueInfo(string) (*asynq.QueueInfo, error) {
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
func (s *redisInspectorStub) PauseQueue(queue string) error {
	s.pauseQueueArg = queue
	return s.pauseErr
}
func (s *redisInspectorStub) UnpauseQueue(queue string) error {
	s.unpauseQueueArg = queue
	return s.unpauseErr
}

func TestSQSHelpers_PhysicalNameAndQueueDiscovery(t *testing.T) {
	q := newSQSQueue(Config{}).(*sqsQueue)
	if got := q.physicalQueueName(); got != "default" {
		t.Fatalf("expected default physical name, got %q", got)
	}
	q.cfg.DefaultQueue = "critical"
	if got := q.physicalQueueName(); got != "critical" {
		t.Fatalf("expected configured physical name, got %q", got)
	}

	if isQueueDoesNotExist(nil, nil) {
		t.Fatal("expected nil error not to match not-found")
	}
	if isQueueDoesNotExist(errors.New("boom"), nil) {
		t.Fatal("expected unrelated error not to match not-found")
	}
	var target *sqstypes.QueueDoesNotExist
	if !isQueueDoesNotExist(&sqstypes.QueueDoesNotExist{}, &target) {
		t.Fatal("expected QueueDoesNotExist to match")
	}
	if target == nil {
		t.Fatal("expected target to be populated")
	}
}

func TestGetOrCreateSQSQueue_Branches(t *testing.T) {
	t.Run("returns existing url", func(t *testing.T) {
		url := "https://example.local/q/default"
		client := &sqsClientStub{getQueueOut: &sqs.GetQueueUrlOutput{QueueUrl: &url}}
		got, err := getOrCreateSQSQueue(context.Background(), client, "default")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != url || client.createCalls != 0 {
			t.Fatalf("expected existing url path, got url=%q createCalls=%d", got, client.createCalls)
		}
	})

	t.Run("creates when queue missing", func(t *testing.T) {
		created := "https://example.local/q/new"
		client := &sqsClientStub{
			getQueueErr:    &sqstypes.QueueDoesNotExist{},
			createQueueOut: &sqs.CreateQueueOutput{QueueUrl: &created},
		}
		got, err := getOrCreateSQSQueue(context.Background(), client, "new")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != created || client.createCalls != 1 {
			t.Fatalf("expected create path with url %q, got %q (calls=%d)", created, got, client.createCalls)
		}
	})

	t.Run("returns get error when not not-found", func(t *testing.T) {
		client := &sqsClientStub{getQueueErr: errors.New("network")}
		if _, err := getOrCreateSQSQueue(context.Background(), client, "x"); err == nil {
			t.Fatal("expected passthrough get error")
		}
	})

	t.Run("errors when create returns empty url", func(t *testing.T) {
		client := &sqsClientStub{getQueueErr: &sqstypes.QueueDoesNotExist{}, createQueueOut: &sqs.CreateQueueOutput{}}
		if _, err := getOrCreateSQSQueue(context.Background(), client, "x"); err == nil {
			t.Fatal("expected create empty url error")
		}
	})
}

func TestSQSQueue_EnsureQueueUsesCache(t *testing.T) {
	client := &sqsClientStub{}
	q := &sqsQueue{client: client, queueURLs: map[string]string{"cached": "https://cached"}, unique: map[string]time.Time{}}
	got, err := q.ensureQueue(context.Background(), "cached")
	if err != nil {
		t.Fatalf("ensureQueue cached failed: %v", err)
	}
	if got != "https://cached" {
		t.Fatalf("expected cached url, got %q", got)
	}
	if client.getCalls != 0 || client.createCalls != 0 {
		t.Fatalf("expected no client calls for cached queue, got get=%d create=%d", client.getCalls, client.createCalls)
	}
}

func TestSQSQueue_EnsureClientAlreadySet(t *testing.T) {
	q := &sqsQueue{
		client:    &sqsClientStub{},
		queueURLs: map[string]string{},
		unique:    map[string]time.Time{},
	}
	if err := q.ensureClient(nil); err != nil {
		t.Fatalf("expected ensureClient with existing client to pass, got %v", err)
	}
}

func TestSQSQueue_ShutdownAndClaimUnique(t *testing.T) {
	client := &sqsClientStub{}
	q := &sqsQueue{
		client:    client,
		queueURLs: map[string]string{"default": "https://example.local/q/default"},
		unique:    map[string]time.Time{},
	}
	if err := q.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown failed: %v", err)
	}
	if q.client != nil || len(q.queueURLs) != 0 {
		t.Fatal("expected shutdown to reset client and queue cache")
	}

	job := NewJob("job:sqs").Payload(map[string]any{"id": 1}).OnQueue("default")
	if !q.claimUnique(job, "default", time.Minute) {
		t.Fatal("expected first unique claim to succeed")
	}
	if q.claimUnique(job, "default", time.Minute) {
		t.Fatal("expected duplicate unique claim to fail")
	}
}

func TestSQSQueue_DispatchSendsMessage(t *testing.T) {
	url := "https://example.local/q/default"
	client := &sqsClientStub{
		getQueueOut: &sqs.GetQueueUrlOutput{QueueUrl: &url},
	}
	q := &sqsQueue{
		cfg:       Config{DefaultQueue: "default"},
		client:    client,
		queueURLs: map[string]string{},
		unique:    map[string]time.Time{},
	}

	err := q.Dispatch(context.Background(), NewJob("job:sqs").
		Payload(map[string]any{"id": 7}).
		OnQueue("critical").
		Delay(2*time.Second).
		Retry(3).
		Timeout(5*time.Second))
	if err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}
	if client.sendCalls != 1 {
		t.Fatalf("expected one send call, got %d", client.sendCalls)
	}
}

func TestSQSQueue_DispatchValidationBranches(t *testing.T) {
	q := &sqsQueue{
		cfg:       Config{DefaultQueue: "default"},
		client:    &sqsClientStub{},
		queueURLs: map[string]string{},
		unique:    map[string]time.Time{},
	}

	if err := q.Dispatch(nil, NewJob("")); err == nil {
		t.Fatal("expected validation error for empty job type")
	}
	if err := q.Dispatch(nil, NewJob("job:sqs")); err == nil {
		t.Fatal("expected queue required error")
	}
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
			queueInfos: map[string]*asynq.QueueInfo{
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
		if err := r.Dispatch(context.Background(), NewJob("job:redis").OnQueue("default")); err == nil {
			t.Fatal("expected client unavailable error")
		}
	})

	t.Run("validation and queue required", func(t *testing.T) {
		r := &redisQueue{client: &redisEnqueueClientStub{}}
		if err := r.Dispatch(context.Background(), NewJob("")); err == nil {
			t.Fatal("expected validation error")
		}
		if err := r.Dispatch(context.Background(), NewJob("job:redis")); err == nil {
			t.Fatal("expected queue required error")
		}
	})

	t.Run("backoff unsupported", func(t *testing.T) {
		r := &redisQueue{client: &redisEnqueueClientStub{}}
		if err := r.Dispatch(context.Background(), NewJob("job:redis").OnQueue("default").Backoff(time.Second)); !errors.Is(err, ErrBackoffUnsupported) {
			t.Fatalf("expected backoff unsupported, got %v", err)
		}
	})

	t.Run("duplicate mapping", func(t *testing.T) {
		client := &redisEnqueueClientStub{enqueueErr: asynq.ErrDuplicateTask}
		r := &redisQueue{client: client}
		if err := r.Dispatch(context.Background(), NewJob("job:redis").OnQueue("default")); !errors.Is(err, ErrDuplicate) {
			t.Fatalf("expected duplicate mapping error, got %v", err)
		}
	})

	t.Run("success", func(t *testing.T) {
		client := &redisEnqueueClientStub{}
		r := &redisQueue{client: client}
		err := r.Dispatch(context.Background(), NewJob("job:redis").
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

func TestNATSWorker_StartWorkersOnceAfterFailure(t *testing.T) {
	w := newNATSWorker("nats://127.0.0.1:1").(*natsWorker)
	firstErr := w.StartWorkers(context.Background())
	if firstErr == nil {
		t.Fatal("expected first start to fail without reachable nats")
	}
	secondErr := w.StartWorkers(context.Background())
	if secondErr != nil {
		t.Fatalf("expected second start to return nil due sync.Once, got %v", secondErr)
	}
}
