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
	getCalls       int
	createCalls    int
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
	return &sqs.SendMessageOutput{}, nil
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
	pauseErr        error
	unpauseErr      error
}

func (s *redisInspectorStub) Queues() ([]string, error) { return nil, nil }
func (s *redisInspectorStub) GetQueueInfo(string) (*asynq.QueueInfo, error) {
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
