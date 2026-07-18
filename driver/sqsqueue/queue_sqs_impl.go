package sqsqueue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/goforj/queue"
	"github.com/goforj/queue/internal/uniqueness"
	"github.com/goforj/queue/queuecore"
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
	unique    uniqueness.MemoryStore
}

func (q *sqsQueue) physicalQueueName() string {
	if q.cfg.DefaultQueue != "" {
		return q.cfg.DefaultQueue
	}
	return "default"
}

func newSQSQueue(cfg Config) *sqsQueue {
	return &sqsQueue{
		cfg:       normalizeConfig(cfg),
		queueURLs: make(map[string]string),
	}
}

func (q *sqsQueue) Driver() queue.Driver {
	return queue.DriverSQS
}

func (q *sqsQueue) Preflight(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := q.ensureClient(ctx); err != nil {
		return err
	}
	_, err := q.ensureQueue(ctx, q.physicalQueueName())
	return err
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

// Dispatch requires a service message identifier before reporting SQS acceptance.
func (q *sqsQueue) Dispatch(ctx context.Context, job queue.Job) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := queuecore.ValidateDriverJob(job); err != nil {
		return err
	}
	parsed := queuecore.DriverOptions(job)
	if parsed.QueueName == "" {
		return fmt.Errorf("job queue is required")
	}
	if err := q.ensureClient(ctx); err != nil {
		return err
	}
	var (
		uniqueKey   string
		uniqueToken uint64
	)
	if parsed.UniqueTTL > 0 {
		var acquired bool
		uniqueKey, uniqueToken, acquired = q.claimUnique(job, parsed.QueueName, parsed.UniqueTTL)
		if !acquired {
			return queuecore.ErrDuplicate
		}
	}

	msg := sqsMessage{
		Type:          job.Type,
		Payload:       job.PayloadBytes(),
		Queue:         parsed.QueueName,
		PublishedAtMS: time.Now().UnixMilli(),
	}
	if parsed.MaxRetry != nil {
		msg.MaxRetry = *parsed.MaxRetry
	}
	if parsed.Backoff != nil && *parsed.Backoff > 0 {
		msg.BackoffMillis = parsed.Backoff.Milliseconds()
	}
	if parsed.Timeout != nil && *parsed.Timeout > 0 {
		msg.TimeoutMillis = parsed.Timeout.Milliseconds()
	}
	if parsed.Delay > 0 {
		msg.AvailableAtMS = time.Now().Add(parsed.Delay).UnixMilli()
	}
	body, err := json.Marshal(msg)
	if err != nil {
		q.unique.Release(uniqueKey, uniqueToken)
		return err
	}
	queueURL, err := q.ensureQueue(ctx, parsed.QueueName)
	if err != nil {
		q.unique.Release(uniqueKey, uniqueToken)
		return err
	}
	q.mu.Lock()
	client := q.client
	q.mu.Unlock()
	if client == nil {
		q.unique.Release(uniqueKey, uniqueToken)
		return fmt.Errorf("sqs client unavailable during dispatch")
	}
	input := &sqs.SendMessageInput{
		QueueUrl:    &queueURL,
		MessageBody: aws.String(string(body)),
	}
	if parsed.Delay > 0 {
		seconds := int32(parsed.Delay / time.Second)
		if seconds > 900 {
			seconds = 900
		}
		if seconds > 0 {
			input.DelaySeconds = seconds
		}
	}
	output, err := client.SendMessage(ctx, input)
	if err == nil {
		err = sqsSendAccepted(output)
	}
	// Send failures and missing receipts are ambiguous: the service may have committed before its response was lost.
	return err
}

// sqsSendAccepted requires the service-generated receipt that proves SQS accepted the message.
func sqsSendAccepted(output *sqs.SendMessageOutput) error {
	if output == nil || output.MessageId == nil || strings.TrimSpace(*output.MessageId) == "" {
		return fmt.Errorf("sqs send message returned no message id")
	}
	return nil
}

// ensureQueue resolves one queue through a stable client snapshot so concurrent shutdown cannot dereference nil.
func (q *sqsQueue) ensureQueue(ctx context.Context, queueName string) (string, error) {
	q.mu.Lock()
	if url, ok := q.queueURLs[queueName]; ok && url != "" {
		q.mu.Unlock()
		return url, nil
	}
	client := q.client
	q.mu.Unlock()
	if client == nil {
		return "", fmt.Errorf("sqs client unavailable while resolving queue")
	}

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

// claimUnique returns the ownership token needed to compensate a rejected send.
func (q *sqsQueue) claimUnique(job queue.Job, queueName string, ttl time.Duration) (string, uint64, bool) {
	key := queuecore.UniqueKey(job, queueName)
	token, ok := q.unique.Acquire(key, ttl)
	return key, token, ok
}

func newSQSClient(ctx context.Context, cfg Config) (sqsClient, error) {
	cfg = normalizeConfig(cfg)
	load := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(cfg.Region),
	}
	if cfg.Endpoint != "" {
		load = append(load, awsconfig.WithBaseEndpoint(cfg.Endpoint))
	}
	if cfg.AccessKey != "" || cfg.SecretKey != "" {
		load = append(load, awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, "")))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, load...)
	if err != nil {
		return nil, err
	}
	return sqs.NewFromConfig(awsCfg), nil
}
