package queue

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

type sqsWorkerClient interface {
	GetQueueUrl(ctx context.Context, params *sqs.GetQueueUrlInput, optFns ...func(*sqs.Options)) (*sqs.GetQueueUrlOutput, error)
	CreateQueue(ctx context.Context, params *sqs.CreateQueueInput, optFns ...func(*sqs.Options)) (*sqs.CreateQueueOutput, error)
	ReceiveMessage(ctx context.Context, params *sqs.ReceiveMessageInput, optFns ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error)
	DeleteMessage(ctx context.Context, params *sqs.DeleteMessageInput, optFns ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error)
	SendMessage(ctx context.Context, params *sqs.SendMessageInput, optFns ...func(*sqs.Options)) (*sqs.SendMessageOutput, error)
}

type sqsWorkerConfig struct {
	DefaultQueue string
	SQSRegion    string
	SQSEndpoint  string
	SQSAccessKey string
	SQSSecretKey string
}

type sqsWorker struct {
	cfg sqsWorkerConfig

	mu       sync.RWMutex
	handlers map[string]Handler

	client    sqsWorkerClient
	queueURL  string
	started   bool
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	startStop sync.Mutex
}

func newSQSWorker(cfg sqsWorkerConfig) runtimeWorkerBackend {
	if cfg.DefaultQueue == "" {
		cfg.DefaultQueue = "default"
	}
	return &sqsWorker{
		cfg:      cfg,
		handlers: make(map[string]Handler),
	}
}

func (w *sqsWorker) Register(taskType string, handler Handler) {
	if taskType == "" || handler == nil {
		return
	}
	w.mu.Lock()
	w.handlers[taskType] = handler
	w.mu.Unlock()
}

func (w *sqsWorker) StartWorkers(ctx context.Context) error {
	w.startStop.Lock()
	defer w.startStop.Unlock()
	if w.started {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	cfg := Config{
		SQSRegion:    w.cfg.SQSRegion,
		SQSEndpoint:  w.cfg.SQSEndpoint,
		SQSAccessKey: w.cfg.SQSAccessKey,
		SQSSecretKey: w.cfg.SQSSecretKey,
	}
	if cfg.SQSRegion == "" {
		cfg.SQSRegion = "us-east-1"
	}
	client, err := newSQSClient(ctx, cfg)
	if err != nil {
		return err
	}
	queueURL, err := getOrCreateSQSQueue(ctx, client, w.cfg.DefaultQueue)
	if err != nil {
		return err
	}
	w.client = client
	w.queueURL = queueURL
	loopCtx, cancel := context.WithCancel(ctx)
	w.cancel = cancel
	w.started = true
	w.wg.Add(1)
	go w.loop(loopCtx)
	return nil
}

func (w *sqsWorker) Shutdown(_ context.Context) error {
	w.startStop.Lock()
	if !w.started {
		w.startStop.Unlock()
		return nil
	}
	cancel := w.cancel
	w.started = false
	w.startStop.Unlock()
	if cancel != nil {
		cancel()
	}
	w.wg.Wait()
	return nil
}

func (w *sqsWorker) loop(ctx context.Context) {
	defer w.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		out, err := w.client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
			QueueUrl:            &w.queueURL,
			MaxNumberOfMessages: 5,
			WaitTimeSeconds:     1,
		})
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			time.Sleep(100 * time.Millisecond)
			continue
		}
		for _, message := range out.Messages {
			w.process(ctx, message)
		}
	}
}

func (w *sqsWorker) process(ctx context.Context, message sqstypes.Message) {
	if message.Body == nil {
		w.delete(ctx, message)
		return
	}
	var incoming sqsMessage
	if err := json.Unmarshal([]byte(*message.Body), &incoming); err != nil {
		w.delete(ctx, message)
		return
	}
	if incoming.AvailableAtMS > 0 {
		remaining := time.Until(time.UnixMilli(incoming.AvailableAtMS))
		if remaining > 0 {
			w.republish(ctx, incoming)
			w.delete(ctx, message)
			return
		}
	}

	w.mu.RLock()
	handler, ok := w.handlers[incoming.Type]
	w.mu.RUnlock()
	if !ok {
		w.delete(ctx, message)
		return
	}
	runCtx := context.Background()
	if incoming.TimeoutMillis > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(runCtx, time.Duration(incoming.TimeoutMillis)*time.Millisecond)
		defer cancel()
	}
	err := handler(
		runCtx,
		NewJob(incoming.Type).
			Payload(incoming.Payload).
			OnQueue(incoming.Queue).
			Retry(incoming.MaxRetry).
			withAttempt(incoming.Attempt),
	)
	if err == nil {
		w.delete(ctx, message)
		return
	}
	if incoming.Attempt >= incoming.MaxRetry {
		w.delete(ctx, message)
		return
	}
	incoming.Attempt++
	if incoming.BackoffMillis > 0 {
		incoming.AvailableAtMS = time.Now().Add(time.Duration(incoming.BackoffMillis) * time.Millisecond).UnixMilli()
	} else {
		incoming.AvailableAtMS = 0
	}
	w.republish(ctx, incoming)
	w.delete(ctx, message)
}

func (w *sqsWorker) republish(ctx context.Context, message sqsMessage) {
	body, err := json.Marshal(message)
	if err != nil {
		return
	}
	input := &sqs.SendMessageInput{
		QueueUrl:    &w.queueURL,
		MessageBody: aws.String(string(body)),
	}
	if message.AvailableAtMS > 0 {
		remaining := time.Until(time.UnixMilli(message.AvailableAtMS))
		seconds := int32(remaining / time.Second)
		if seconds > 900 {
			seconds = 900
		}
		if seconds > 0 {
			input.DelaySeconds = seconds
		}
	}
	_, _ = w.client.SendMessage(ctx, input)
}

func (w *sqsWorker) delete(ctx context.Context, message sqstypes.Message) {
	if message.ReceiptHandle == nil {
		return
	}
	_, _ = w.client.DeleteMessage(ctx, &sqs.DeleteMessageInput{
		QueueUrl:      &w.queueURL,
		ReceiptHandle: message.ReceiptHandle,
	})
}
