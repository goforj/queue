package temporalqueue

import (
	"context"
	"errors"
	"reflect"
	"time"

	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
)

// ErrAcceptanceUnknown means the returned identity needs reconciliation before any new submission.
// Calling Dispatch again creates a new identity and can duplicate application effects.
var ErrAcceptanceUnknown = errors.New("Temporal workflow acceptance is unresolved")

// acceptWorkflow keeps one immutable identity across bounded retries and verifies original input on collision.
func (b *backend) acceptWorkflow(ctx context.Context, id, kind string, input any) error {
	options := client.StartWorkflowOptions{ID: id, TaskQueue: b.workflowQueue, WorkflowIDReusePolicy: enums.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE, WorkflowIDConflictPolicy: enums.WORKFLOW_ID_CONFLICT_POLICY_FAIL, WorkflowExecutionErrorWhenAlreadyStarted: true}
	for attempt := 0; attempt < 2; attempt++ {
		_, err := b.clientBinding.client.ExecuteWorkflow(ctx, options, kind, input)
		if err == nil {
			return nil
		}
		var duplicate *serviceerror.WorkflowExecutionAlreadyStarted
		if errors.As(err, &duplicate) {
			return b.reconcileWorkflow(ctx, id, duplicate.RunId, kind, input)
		}

		var invalid *serviceerror.InvalidArgument
		var forbidden *serviceerror.PermissionDenied
		var missing *serviceerror.NamespaceNotFound
		if errors.As(err, &invalid) || errors.As(err, &forbidden) || errors.As(err, &missing) {
			if attempt > 0 {
				return ErrAcceptanceUnknown
			}
			return errors.New("Temporal workflow start rejected")
		}

		if ctx.Err() != nil {
			return ErrAcceptanceUnknown
		}
		if attempt == 0 {
			timer := time.NewTimer(50 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ErrAcceptanceUnknown
			case <-timer.C:
			}
		}
	}
	return ErrAcceptanceUnknown
}

// reconcileWorkflow treats only a matching original start as evidence of prior acceptance.
func (b *backend) reconcileWorkflow(ctx context.Context, id, run, kind string, input any) error {
	if run == "" {
		return ErrAcceptanceUnknown
	}
	pages := &historyPages{ctx: ctx, service: b.clientBinding.client.WorkflowService(), namespace: b.clientBinding.Namespace(), id: id, runID: run}
	event, err := pages.Next()
	if err != nil {
		return ErrAcceptanceUnknown
	}
	start := event.GetWorkflowExecutionStartedEventAttributes()
	if event == nil || event.EventId != 1 || start == nil || start.WorkflowType.GetName() != kind || start.TaskQueue.GetName() != b.workflowQueue || start.OriginalExecutionRunId != run || start.FirstExecutionRunId != run || start.Attempt != 1 || start.RetryPolicy != nil || start.CronSchedule != "" || start.ContinuedExecutionRunId != "" || start.ParentWorkflowExecution != nil {
		return errors.New("Temporal workflow identity collision")
	}
	dc := converter.WithDataConverterSerializationContext(b.clientBinding.options.DataConverter, converter.WorkflowSerializationContext{Namespace: b.clientBinding.Namespace(), WorkflowID: id})
	var actual any
	switch input.(type) {
	case delivery:
		actual = &delivery{}
	case workflowDefinition:
		actual = &workflowDefinition{}
	default:
		return errors.New("unsupported Temporal acceptance protocol")
	}
	if err := dc.FromPayloads(start.Input, actual); err != nil {
		return ErrAcceptanceUnknown
	}
	if !reflect.DeepEqual(reflect.ValueOf(actual).Elem().Interface(), input) {
		return errors.New("Temporal workflow immutable input collision")
	}
	return nil
}
