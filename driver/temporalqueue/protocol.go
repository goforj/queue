package temporalqueue

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/goforj/queue"
	"github.com/goforj/queue/busruntime"
	engine "github.com/goforj/queue/internal/workflow"

	"go.temporal.io/sdk/activity"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

const (
	jobWorkflowType         = "goforj.queue.job.v1"
	coordinatorWorkflowType = "goforj.queue.coordinator.v1"
	deliveryActivityType    = "goforj.queue.delivery.v1"
	journalKey              = "goforj.queue.journal.v1"
	maxMembers              = 256
)

// delivery carries canonical application bytes independently of SDK type registration.
type delivery struct {
	DefaultTimeout time.Duration
	Resolved       activityPolicy
	WorkflowQueue  string
	ActivityQueue  string
	Version        int
	Type           string
	Payload        []byte
	Metadata       queue.DriverJobMetadata
	Timeout        time.Duration
	Retry          int
	Backoff        time.Duration
	Delay          time.Duration
}

// validate checks untrusted persisted workflow input before scheduling application code.
func (d delivery) validate() error {
	for _, value := range []string{d.Type, d.WorkflowQueue, d.ActivityQueue, d.Metadata.DispatchID, d.Metadata.JobID, d.Metadata.ChainID, d.Metadata.BatchID, d.Metadata.Queue} {
		if !utf8.ValidString(value) {
			return errors.New("Temporal delivery identifiers must be valid UTF-8")
		}
	}
	expected, err := resolvePolicy(d.Timeout, d.Delay, d.Backoff, d.Retry, d.DefaultTimeout)
	if err != nil || expected != d.Resolved {
		return errors.New("unresolved or invalid Temporal delivery policy")
	}
	if d.WorkflowQueue == "" || d.ActivityQueue == "" || len(d.WorkflowQueue) > 255 || len(d.ActivityQueue) > 255 || d.Version != 1 || d.Type == "" || len(d.Type) > 256 || strings.HasPrefix(d.Type, "bus:") || strings.HasPrefix(d.Type, "goforj.queue.") || len(d.Payload) > 128<<10 || d.Timeout < 0 || d.Retry < 0 || d.Backoff < 0 || d.Delay < 0 {
		return errors.New("invalid Temporal queue delivery")
	}
	if d.Metadata.SchemaVersion != busruntime.DeliveryMetadataVersion || d.Metadata.DispatchID == "" || d.Metadata.JobID == "" || len(d.Metadata.DispatchID) > 256 || len(d.Metadata.JobID) > 256 || len(d.Metadata.ChainID) > 256 || len(d.Metadata.BatchID) > 256 || len(d.Metadata.Queue) > 255 {
		return errors.New("invalid Temporal queue correlation")
	}
	return nil
}

// Dispatch durably accepts a single-activity wrapper without waiting for its result.
func (b *backend) Dispatch(ctx context.Context, job queue.Job) error {
	release, err := b.entry.enter()
	if err != nil {
		return err
	}
	defer release()
	opts := queue.DriverOptions(job)
	if opts.UniqueTTL != 0 {
		return errors.New("Temporal portable unique delivery is unsupported")
	}
	target := opts.QueueName
	if target == "" {
		target = b.taskQueue
	}
	if target != b.taskQueue {
		return errors.New("Temporal queue route is not configured")
	}
	timeout := time.Duration(0)
	if opts.Timeout != nil && *opts.Timeout != 0 {
		timeout = *opts.Timeout
	}
	retry := 0
	if opts.MaxRetry != nil {
		retry = *opts.MaxRetry
	}
	backoff := time.Duration(0)
	if opts.Backoff != nil {
		backoff = *opts.Backoff
	}
	d := delivery{Version: 1, Type: job.Type, Payload: job.PayloadBytes(), Metadata: queue.DriverMetadata(job), WorkflowQueue: b.workflowQueue, ActivityQueue: b.activityQueue, Timeout: timeout, Retry: retry, Backoff: backoff, Delay: opts.Delay}
	if err := d.resolve(b.cfg.DefaultActivityTimeout); err != nil {
		return err
	}
	d.Metadata.Queue = target
	if err := d.validate(); err != nil {
		return err
	}
	return b.acceptWorkflow(ctx, d.Metadata.DispatchID, jobWorkflowType, d)
}

// activityContext resolves queue policy into deterministic SDK commands.
func activityContext(ctx workflow.Context, d delivery) workflow.Context {
	retry := &temporal.RetryPolicy{MaximumAttempts: d.Resolved.MaximumAttempts, InitialInterval: d.Resolved.Backoff, BackoffCoefficient: 1, MaximumInterval: d.Resolved.Backoff}
	return workflow.WithActivityOptions(ctx, workflow.ActivityOptions{ActivityID: d.Metadata.JobID, TaskQueue: d.ActivityQueue, StartToCloseTimeout: d.Resolved.Timeout, RetryPolicy: retry, WaitForCancellation: true})
}

// jobWorkflow executes one portable job with server-owned delay and retry state.
func jobWorkflow(ctx workflow.Context, d delivery) error {
	if d.Metadata.ChainID != "" || d.Metadata.BatchID != "" || d.Metadata.Continuation != nil {
		return invalidAdapterError()
	}
	if err := validatePortableExecution(ctx, d.Metadata.DispatchID, d.WorkflowQueue, jobWorkflowType); err != nil {
		return err
	}
	if err := d.validate(); err != nil {
		return temporal.NewNonRetryableApplicationError("invalid queue input", "goforj.invalid.v1", nil)
	}
	if d.Resolved.Delay > 0 {
		if err := workflow.Sleep(ctx, d.Resolved.Delay); err != nil {
			return err
		}
	}
	return workflow.ExecuteActivity(activityContext(ctx, d), deliveryActivityType, d).Get(ctx, nil)
}

// executeDelivery invokes the shared queue executor with the original bytes and attempt.
func (b *backend) executeDelivery(ctx context.Context, d delivery) (err error) {
	if err = d.validate(); err != nil {
		return temporal.NewNonRetryableApplicationError("invalid queue input", "goforj.invalid.v1", nil)
	}
	if d.Metadata.Queue != b.taskQueue || d.ActivityQueue != b.activityQueue || activity.GetInfo(ctx).TaskQueue != b.activityQueue {
		return temporal.NewNonRetryableApplicationError("delivery worker association rejected", "goforj.invalid.v1", nil)
	}
	info := activity.GetInfo(ctx)
	if d.Metadata.ChainID != "" && d.Metadata.BatchID != "" {
		return invalidAdapterError()
	}
	parent := d.Metadata.DispatchID
	kind := jobWorkflowType
	if d.Metadata.ChainID != "" {
		parent = d.Metadata.ChainID
		kind = coordinatorWorkflowType
	}
	if d.Metadata.BatchID != "" {
		parent = d.Metadata.BatchID
		kind = coordinatorWorkflowType
	}
	if info.WorkflowExecution.ID != parent || info.WorkflowType.Name != kind {
		return invalidAdapterError()
	}
	release, err := b.gate.enter()
	if err != nil {
		return err
	}
	defer release()
	if err := b.verifyDeliveryParent(ctx, d); err != nil {
		if errors.Is(err, errAssociationRejected) {
			return temporal.NewNonRetryableApplicationError("continuation association rejected", "goforj.invalid.v1", nil)
		}
		return temporal.NewApplicationError("continuation authority unavailable", "goforj.unavailable.v1")
	}
	// Only portable callbacks use the queue error translator; native activities retain SDK semantics.
	defer func() {
		if recover() != nil {
			err = temporal.NewNonRetryableApplicationError("queue handler panicked", "goforj.panic.v1", nil)
		}
	}()
	b.mu.Lock()
	handler := b.handlers[d.Type]
	b.mu.Unlock()
	if handler == nil {
		err = b.binding.Jobs().ExecuteDirect(ctx, engine.StoredJob{Type: d.Type, Payload: d.Payload, Options: engine.JobOptions{Queue: d.Metadata.Queue, Timeout: d.Timeout, Retry: d.Retry, Backoff: d.Backoff}}, d.Metadata, busruntime.DeliveryAttempt{Number: int(activity.GetInfo(ctx).Attempt) - 1, MaxRetry: d.Retry})
		if errors.Is(err, engine.ErrHandlerNotRegistered) {
			return temporal.NewNonRetryableApplicationError("queue handler is not registered", "goforj.missing-handler.v1", nil)
		}
	} else {
		job := queue.NewJob(d.Type).Payload(d.Payload).Timeout(d.Timeout).Retry(d.Retry)
		job = queue.DriverWithMetadata(job, d.Metadata)
		job = queue.DriverWithAttempt(job, int(activity.GetInfo(ctx).Attempt)-1)
		err = handler(ctx, job)
	}
	if errors.Is(err, errUnsupportedOutcome) {
		return temporal.NewNonRetryableApplicationError("unsupported queue handler outcome", "goforj.unsupported-outcome.v1", nil)
	}
	if errors.Is(err, errQueuePanic) {
		return temporal.NewNonRetryableApplicationError("queue handler panicked", "goforj.panic.v1", nil)
	}
	if queue.IsPermanent(err) {
		return temporal.NewNonRetryableApplicationError("queue execution failed", "goforj.permanent.v1", nil)
	}
	if err != nil {
		return fmt.Errorf("queue execution failed")
	}
	return nil
}

// validatePortableExecution prevents direct SDK starts from multiplying portable attempt budgets.
func validatePortableExecution(ctx workflow.Context, id, taskQueue, workflowType string) error {
	return validatePortableInfo(workflow.GetInfo(ctx), id, taskQueue, workflowType)
}

// validatePortableInfo binds persisted protocol input to the actual server execution policy.
func validatePortableInfo(info *workflow.Info, id, taskQueue, workflowType string) error {
	if info.WorkflowExecution.ID != id || info.TaskQueueName != taskQueue || info.WorkflowType.Name != workflowType || info.Attempt != 1 || info.RetryPolicy != nil || info.CronSchedule != "" || info.ContinuedExecutionRunID != "" || info.ParentWorkflowExecution != nil || info.OriginalRunID != info.FirstRunID || info.WorkflowExecution.RunID != info.OriginalRunID {
		return temporal.NewNonRetryableApplicationError("unsupported portable workflow execution policy", "goforj.invalid.v1", nil)
	}
	return nil
}
