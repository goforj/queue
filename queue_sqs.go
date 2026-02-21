package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

type sqsMessage struct {
	Type          string `json:"type"`
	Payload       []byte `json:"payload,omitempty"`
	Queue         string `json:"queue"`
	Attempt       int    `json:"attempt,omitempty"`
	MaxRetry      int    `json:"max_retry,omitempty"`
	BackoffMillis int64  `json:"backoff_millis,omitempty"`
	TimeoutMillis int64  `json:"timeout_millis,omitempty"`
	AvailableAtMS int64  `json:"available_at_ms,omitempty"`
	PublishedAtMS int64  `json:"published_at_ms,omitempty"`
}

type sqsClient interface {
	GetQueueUrl(ctx context.Context, params *sqs.GetQueueUrlInput, optFns ...func(*sqs.Options)) (*sqs.GetQueueUrlOutput, error)
	CreateQueue(ctx context.Context, params *sqs.CreateQueueInput, optFns ...func(*sqs.Options)) (*sqs.CreateQueueOutput, error)
	SendMessage(ctx context.Context, params *sqs.SendMessageInput, optFns ...func(*sqs.Options)) (*sqs.SendMessageOutput, error)
	ReceiveMessage(ctx context.Context, params *sqs.ReceiveMessageInput, optFns ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error)
	DeleteMessage(ctx context.Context, params *sqs.DeleteMessageInput, optFns ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error)
}

type sqsQueue struct {
	cfg Config

	mu        sync.Mutex
	client    sqsClient
	queueURLs map[string]string
	unique    map[string]time.Time
}

func (q *sqsQueue) physicalQueueName() string {
	if q.cfg.DefaultQueue != "" {
		return q.cfg.DefaultQueue
	}
	return "default"
}

func newSQSQueue(cfg Config) queueBackend {
	return &sqsQueue{
		cfg:       cfg,
		queueURLs: make(map[string]string),
		unique:    make(map[string]time.Time),
	}
}

func (q *sqsQueue) Driver() Driver {
	return DriverSQS
}

func (q *sqsQueue) ensureClient(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.client != nil {
		return nil
	}
	client, err := newSQSClient(ctx, q.cfg)
	if err != nil {
		return err
	}
	q.client = client
	return nil
}

func (q *sqsQueue) Shutdown(_ context.Context) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.client = nil
	q.queueURLs = make(map[string]string)
	return nil
}

func (q *sqsQueue) Dispatch(ctx context.Context, task Job) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := task.validate(); err != nil {
		return err
	}
	parsed := task.jobOptions()
	if parsed.queueName == "" {
		return fmt.Errorf("job queue is required")
	}
	if err := q.ensureClient(ctx); err != nil {
		return err
	}
	if parsed.uniqueTTL > 0 && !q.claimUnique(task, parsed.queueName, parsed.uniqueTTL) {
		return ErrDuplicate
	}

	msg := sqsMessage{
		Type:          task.Type,
		Payload:       task.PayloadBytes(),
		Queue:         parsed.queueName,
		PublishedAtMS: time.Now().UnixMilli(),
	}
	if parsed.maxRetry != nil {
		msg.MaxRetry = *parsed.maxRetry
	}
	if parsed.backoff != nil && *parsed.backoff > 0 {
		msg.BackoffMillis = parsed.backoff.Milliseconds()
	}
	if parsed.timeout != nil && *parsed.timeout > 0 {
		msg.TimeoutMillis = parsed.timeout.Milliseconds()
	}
	if parsed.delay > 0 {
		msg.AvailableAtMS = time.Now().Add(parsed.delay).UnixMilli()
	}
	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	queueURL, err := q.ensureQueue(ctx, q.physicalQueueName())
	if err != nil {
		return err
	}
	input := &sqs.SendMessageInput{
		QueueUrl:    &queueURL,
		MessageBody: aws.String(string(body)),
	}
	if parsed.delay > 0 {
		seconds := int32(parsed.delay / time.Second)
		if seconds > 900 {
			seconds = 900
		}
		if seconds > 0 {
			input.DelaySeconds = seconds
		}
	}
	_, err = q.client.SendMessage(ctx, input)
	return err
}

func (q *sqsQueue) ensureQueue(ctx context.Context, queueName string) (string, error) {
	q.mu.Lock()
	if url, ok := q.queueURLs[queueName]; ok && url != "" {
		q.mu.Unlock()
		return url, nil
	}
	client := q.client
	q.mu.Unlock()

	url, err := getOrCreateSQSQueue(ctx, client, queueName)
	if err != nil {
		return "", err
	}
	q.mu.Lock()
	q.queueURLs[queueName] = url
	q.mu.Unlock()
	return url, nil
}

func getOrCreateSQSQueue(ctx context.Context, client sqsClient, queueName string) (string, error) {
	out, err := client.GetQueueUrl(ctx, &sqs.GetQueueUrlInput{QueueName: &queueName})
	if err == nil && out.QueueUrl != nil && *out.QueueUrl != "" {
		return *out.QueueUrl, nil
	}
	var notFound *types.QueueDoesNotExist
	if err != nil && !isQueueDoesNotExist(err, &notFound) {
		return "", err
	}
	createOut, createErr := client.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: &queueName})
	if createErr != nil {
		return "", createErr
	}
	if createOut.QueueUrl == nil || *createOut.QueueUrl == "" {
		return "", fmt.Errorf("created queue %q but no queue url returned", queueName)
	}
	return *createOut.QueueUrl, nil
}

func isQueueDoesNotExist(err error, target **types.QueueDoesNotExist) bool {
	if err == nil {
		return false
	}
	var notFound *types.QueueDoesNotExist
	if ok := errors.As(err, &notFound); ok {
		if target != nil {
			*target = notFound
		}
		return true
	}
	return false
}

func (q *sqsQueue) claimUnique(task Job, queueName string, ttl time.Duration) bool {
	now := time.Now()
	key := queueName + ":" + task.Type + ":" + string(task.PayloadBytes())
	q.mu.Lock()
	defer q.mu.Unlock()
	for candidate, expiresAt := range q.unique {
		if expiresAt.Before(now) {
			delete(q.unique, candidate)
		}
	}
	if expiresAt, ok := q.unique[key]; ok && expiresAt.After(now) {
		return false
	}
	q.unique[key] = now.Add(ttl)
	return true
}

func newSQSClient(ctx context.Context, cfg Config) (sqsClient, error) {
	load := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(cfg.SQSRegion),
	}
	if cfg.SQSEndpoint != "" {
		load = append(load, awsconfig.WithBaseEndpoint(cfg.SQSEndpoint))
	}
	if cfg.SQSAccessKey != "" || cfg.SQSSecretKey != "" {
		load = append(load, awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(cfg.SQSAccessKey, cfg.SQSSecretKey, "")))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, load...)
	if err != nil {
		return nil, err
	}
	return sqs.NewFromConfig(awsCfg), nil
}
