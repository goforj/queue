package sqsqueue

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/goforj/queue"
	"github.com/goforj/queue/busruntime"
	"github.com/goforj/queue/queuecore"
)

const sqsSettlementTimeout = 15 * time.Second

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
	Workers      int
	Observer     queue.Observer
}

type sqsWorker struct {
	cfg sqsWorkerConfig

	mu       sync.RWMutex
	handlers map[string]queue.Handler

	client    sqsWorkerClient
	queueURL  string
	started   bool
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	startStop sync.Mutex
	observer  queue.Observer
	stopDone  chan struct{}
}

func newSQSWorker(cfg sqsWorkerConfig) *sqsWorker {
	if cfg.DefaultQueue == "" {
		cfg.DefaultQueue = "default"
	}
	cfg.Workers = defaultWorkerCount(cfg.Workers)
	return &sqsWorker{
		cfg:      cfg,
		handlers: make(map[string]queue.Handler),
		observer: cfg.Observer,
	}
}

func (w *sqsWorker) Register(jobType string, handler queue.Handler) {
	if jobType == "" || handler == nil {
		return
	}
	w.mu.Lock()
	w.handlers[jobType] = handler
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
	client, err := newSQSClient(ctx, Config{
		Region:    w.cfg.SQSRegion,
		Endpoint:  w.cfg.SQSEndpoint,
		AccessKey: w.cfg.SQSAccessKey,
		SecretKey: w.cfg.SQSSecretKey,
	})
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
	for i := 0; i < w.cfg.Workers; i++ {
		w.wg.Add(1)
		go w.loop(loopCtx)
	}
	return nil
}

// Shutdown stops receive loops while allowing in-flight replacement and deletion calls to finish independently.
func (w *sqsWorker) Shutdown(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	w.startStop.Lock()
	if !w.started {
		w.startStop.Unlock()
		return nil
	}
	if w.stopDone == nil {
		w.stopDone = make(chan struct{})
		cancel := w.cancel
		done := w.stopDone
		if cancel != nil {
			cancel()
		}
		go func() {
			w.wg.Wait()
			w.startStop.Lock()
			w.started = false
			w.stopDone = nil
			w.startStop.Unlock()
			close(done)
		}()
	}
	done := w.stopDone
	w.startStop.Unlock()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
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

// process commits positive facts only after the original SQS receipt is deleted.
func (w *sqsWorker) process(ctx context.Context, message sqstypes.Message) {
	if message.Body == nil {
		w.deleteAndObserve(ctx, message, sqsMessage{})
		return
	}
	var incoming sqsMessage
	if err := json.Unmarshal([]byte(*message.Body), &incoming); err != nil {
		w.deleteAndObserve(ctx, message, sqsMessage{})
		return
	}
	if incoming.AvailableAtMS > 0 {
		remaining := time.Until(time.UnixMilli(incoming.AvailableAtMS))
		if remaining > 0 {
			if err := w.republish(incoming); err != nil {
				w.observeRepublishFailure(ctx, incoming, err)
				return
			}
			w.deleteAndObserve(ctx, message, incoming)
			return
		}
	}

	w.mu.RLock()
	handler, ok := w.handlers[incoming.Type]
	w.mu.RUnlock()
	if !ok {
		w.deleteAndObserve(ctx, message, incoming)
		return
	}
	attempt := busruntime.DeliveryAttempt{Number: incoming.Attempt, MaxRetry: incoming.MaxRetry}
	runCtx := busruntime.WithDeliveryAttempt(context.Background(), attempt)
	runCtx, settlement := busruntime.WithDeliverySettlement(runCtx)
	if incoming.TimeoutMillis > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(runCtx, time.Duration(incoming.TimeoutMillis)*time.Millisecond)
		defer cancel()
	}
	err := handler(
		runCtx,
		sqsDeliveryJob(incoming),
	)
	switch busruntime.ClassifyAttempt(attempt, err) {
	case busruntime.AttemptSucceeded, busruntime.AttemptFailed:
		if w.deleteAndObserve(ctx, message, incoming) {
			settlement.Commit()
		}
		return
	case busruntime.AttemptRedeliver:
		// Leaving the receipt undeleted lets SQS redeliver the same application attempt after its visibility timeout.
		return
	case busruntime.AttemptRetry:
	}
	settledMessage := incoming
	incoming.Attempt++
	if incoming.BackoffMillis > 0 {
		incoming.AvailableAtMS = time.Now().Add(time.Duration(incoming.BackoffMillis) * time.Millisecond).UnixMilli()
	} else {
		incoming.AvailableAtMS = 0
	}
	if err := w.republish(incoming); err != nil {
		w.observeRepublishFailure(ctx, incoming, err)
		return
	}
	if w.deleteAndObserve(ctx, message, settledMessage) {
		settlement.Commit()
	}
}

// republish creates a confirmed replacement before the original receipt can be deleted.
func (w *sqsWorker) republish(message sqsMessage) error {
	body, err := json.Marshal(message)
	if err != nil {
		return err
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
	ctx, cancel := sqsSettlementContext()
	defer cancel()
	output, err := w.client.SendMessage(ctx, input)
	if err != nil {
		return err
	}
	return sqsSendAccepted(output)
}

// delete settles one SQS receipt through a bounded context independent of receive-loop cancellation.
func (w *sqsWorker) delete(message sqstypes.Message) error {
	if message.ReceiptHandle == nil || strings.TrimSpace(*message.ReceiptHandle) == "" {
		return fmt.Errorf("sqs receipt handle is required for settlement")
	}
	ctx, cancel := sqsSettlementContext()
	defer cancel()
	_, err := w.client.DeleteMessage(ctx, &sqs.DeleteMessageInput{
		QueueUrl:      &w.queueURL,
		ReceiptHandle: message.ReceiptHandle,
	})
	return err
}

// deleteAndObserve reports deletion ambiguity and returns whether the delivery reached positive settlement.
func (w *sqsWorker) deleteAndObserve(ctx context.Context, message sqstypes.Message, incoming sqsMessage) bool {
	if err := w.delete(message); err != nil {
		w.observeSettlementFailure(ctx, incoming, fmt.Errorf("delete sqs message: %w", err))
		return false
	}
	return true
}

func (w *sqsWorker) observeRepublishFailure(ctx context.Context, message sqsMessage, err error) {
	metadata := queue.ResolveObservedJobMetadataFromJob(sqsDeliveryJob(message))
	queuecore.SafeObserve(ctx, w.observer, queue.Event{
		Kind:       queue.EventRepublishFailed,
		Driver:     queue.DriverSQS,
		Queue:      queuecore.NormalizeQueueName(message.Queue),
		JobType:    metadata.JobType,
		JobKey:     metadata.JobKey,
		DispatchID: metadata.DispatchID,
		JobID:      metadata.JobID,
		ChainID:    metadata.ChainID,
		BatchID:    metadata.BatchID,
		Attempt:    message.Attempt,
		MaxRetry:   message.MaxRetry,
		Err:        err,
		Time:       time.Now(),
	})
}

// observeSettlementFailure emits the canonical worker fact for an uncommitted SQS deletion.
func (w *sqsWorker) observeSettlementFailure(ctx context.Context, message sqsMessage, err error) {
	metadata := queue.ResolveObservedJobMetadataFromJob(sqsDeliveryJob(message))
	queuecore.SafeObserve(ctx, w.observer, queue.Event{
		Kind:       queue.EventSettlementFailed,
		Driver:     queue.DriverSQS,
		Queue:      queuecore.NormalizeQueueName(message.Queue),
		JobType:    metadata.JobType,
		JobKey:     metadata.JobKey,
		DispatchID: metadata.DispatchID,
		JobID:      metadata.JobID,
		ChainID:    metadata.ChainID,
		BatchID:    metadata.BatchID,
		Attempt:    message.Attempt,
		MaxRetry:   message.MaxRetry,
		Err:        err,
		Time:       time.Now(),
	})
}

// sqsDeliveryJob reconstructs one SQS delivery while retaining supported
// direct-delivery metadata separately from the application payload.
func sqsDeliveryJob(message sqsMessage) queue.Job {
	job := queuecore.DriverWithAttempt(
		queue.NewJob(message.Type).
			Payload(message.Payload).
			OnQueue(message.Queue).
			Retry(message.MaxRetry),
		message.Attempt,
	)
	if len(message.Metadata) > 0 {
		var metadata queue.DriverJobMetadata
		if err := json.Unmarshal(message.Metadata, &metadata); err == nil {
			job = queue.DriverWithMetadata(job, metadata)
		}
	}
	return job
}

// sqsSettlementContext lets in-flight work finish settlement after the receive loop is canceled without waiting forever.
func sqsSettlementContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), sqsSettlementTimeout)
}

func defaultWorkerCount(n int) int {
	if n <= 0 {
		return 1
	}
	return n
}
