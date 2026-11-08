package temporalqueue

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"go.temporal.io/sdk/activity"
	"reflect"

	"github.com/goforj/queue/busruntime"
	engine "github.com/goforj/queue/internal/workflow"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/workflow"
)

// verifyDeliveryParent rejects copied reset tasks and verifies original immutable business membership.
func (b *backend) verifyDeliveryParent(ctx context.Context, d delivery) error {
	if d.Metadata.Continuation != nil {
		return b.verifyContinuation(ctx, d)
	}
	info := activity.GetInfo(ctx)
	pages := &historyPages{ctx: ctx, client: b.clientBinding.client, namespace: b.clientBinding.Namespace(), id: info.WorkflowExecution.ID, runID: info.WorkflowExecution.RunID}
	event, err := pages.Next()
	if err != nil {
		return err
	}
	start := event.GetWorkflowExecutionStartedEventAttributes()
	if event.EventId != 1 || start == nil || start.OriginalExecutionRunId != info.WorkflowExecution.RunID || start.FirstExecutionRunId != info.WorkflowExecution.RunID || start.Attempt != 1 || start.RetryPolicy != nil || start.CronSchedule != "" || start.ParentWorkflowExecution != nil || start.ContinuedExecutionRunId != "" || start.TaskQueue.GetName() != b.workflowQueue {
		return errAssociationRejected
	}
	dc := converter.WithDataConverterSerializationContext(b.clientBinding.options.DataConverter, converter.WorkflowSerializationContext{Namespace: b.clientBinding.Namespace(), WorkflowID: info.WorkflowExecution.ID})
	if start.WorkflowType.GetName() == jobWorkflowType {
		var expected delivery
		if err := dc.FromPayloads(start.Input, &expected); err != nil {
			return err
		}
		if expected.Metadata.DispatchID != info.WorkflowExecution.ID || info.ActivityID != expected.Metadata.JobID || !reflect.DeepEqual(expected, d) {
			return errAssociationRejected
		}
		return nil
	}
	if start.WorkflowType.GetName() != coordinatorWorkflowType {
		return errAssociationRejected
	}
	var def workflowDefinition
	if err := dc.FromPayloads(start.Input, &def); err != nil {
		return err
	}
	if err := def.validate(); err != nil {
		return errAssociationRejected
	}
	if def.ID != info.WorkflowExecution.ID || def.Resource != b.clientBinding.Namespace()+":"+b.taskQueue || def.WorkflowQueue != b.workflowQueue {
		return errAssociationRejected
	}
	for _, expected := range def.Jobs {
		if expected.Metadata.JobID == info.ActivityID && reflect.DeepEqual(expected, d) {
			return nil
		}
	}
	return errAssociationRejected
}

// hookDefinition persists a resolved named job policy alongside initial business input.
type hookDefinition struct {
	Phase string
	Job   delivery
}

// continuationInvocation captures one immutable safe snapshot with its original application job.
type continuationInvocation struct {
	Outcome busruntime.ContinuationOutcome
	Job     delivery `json:"-"`
}

// copyJobs snapshots root adapter payloads without interpreting their application encoding.
func copyJobs(jobs []engine.Job) ([]engine.Job, error) {
	copied := make([]engine.Job, len(jobs))
	for i, job := range jobs {
		copied[i] = job
		switch value := job.Payload.(type) {
		case nil:
		case json.RawMessage:
			copied[i].Payload = append(json.RawMessage(nil), value...)
		case []byte:
			copied[i].Payload = append([]byte(nil), value...)
		default:
			raw, err := json.Marshal(value)
			if err != nil {
				return nil, errors.New("workflow snapshot encoding failed")
			}
			copied[i].Payload = json.RawMessage(raw)
		}
	}
	return copied, nil
}

// BindContinuations snapshots a native chain and rejects mixed ephemeral closure semantics.
func (b *chainBuilder) BindContinuations(definitions []engine.ContinuationDefinition) (any, error) {
	if b.closure {
		return nil, errors.New("ephemeral callbacks cannot be mixed with durable continuations")
	}
	copy := *b
	var err error
	copy.jobs, err = copyJobs(b.jobs)
	if err != nil {
		return nil, err
	}
	copy.hooks = copyHooks(definitions)
	return &copy, nil
}

// BindContinuations snapshots a native batch without expanding the established public interface.
func (b *batchBuilder) BindContinuations(definitions []engine.ContinuationDefinition) (any, error) {
	if b.closure {
		return nil, errors.New("ephemeral callbacks cannot be mixed with durable continuations")
	}
	copy := *b
	var err error
	copy.jobs, err = copyJobs(b.jobs)
	if err != nil {
		return nil, err
	}
	copy.hooks = copyHooks(definitions)
	return &copy, nil
}

// copyHooks isolates every configured continuation payload from subsequent caller mutation.
func copyHooks(definitions []engine.ContinuationDefinition) []engine.ContinuationDefinition {
	copied := append([]engine.ContinuationDefinition(nil), definitions...)
	for i := range copied {
		copied[i].Job.Payload = append([]byte(nil), copied[i].Job.Payload...)
	}
	return copied
}

// triggerInvocations assigns stable idempotency keys without including unrelated business payloads.
func triggerInvocations(ctx workflow.Context, def workflowDefinition, summary busruntime.ContinuationSummary, firstFailure, terminal bool, sequence *int) []continuationInvocation {
	phases := make([]string, 0, 4)
	if def.Kind == "batch" {
		phases = append(phases, "progress")
	}
	if firstFailure {
		phases = append(phases, "failure")
	}
	if terminal && !summary.Cancelled && (def.Kind == "batch" || summary.Failed == 0) {
		phases = append(phases, "success")
	}
	if terminal {
		phases = append(phases, "completion")
	}
	var intents []continuationInvocation
	for _, phase := range phases {
		for index, hook := range def.Hooks {
			if phase != hook.Phase {
				continue
			}
			*sequence++
			id := continuationID(def.Resource, def.ID, workflow.GetInfo(ctx).OriginalRunID, def.DispatchID, phase, summary.MemberID, *sequence, index)
			invocation := busruntime.Continuation{ID: id, WorkflowID: def.ID, DispatchID: def.DispatchID, Phase: phase, Sequence: *sequence, Declaration: index, Trigger: summary}
			job := hook.Job
			job.Metadata.JobID = invocation.ID
			job.Metadata.Continuation = &invocation
			intents = append(intents, continuationInvocation{Job: job, Outcome: busruntime.ContinuationOutcome{Invocation: invocation, Status: "pending", CreatedAt: workflow.Now(ctx)}})
		}
	}
	return intents
}

// FindContinuations reconstructs only triggered intents and their committed execution status.
func (c *coordinator) FindContinuations(ctx context.Context, id string) ([]busruntime.ContinuationOutcome, error) {
	_, deltas, _, err := c.readState(ctx, id, "")
	if err != nil {
		return nil, err
	}
	var outcomes []busruntime.ContinuationOutcome
	index := make(map[string]int)
	for _, delta := range deltas {
		if delta.Status == "workflow-interrupted" {
			for i := range outcomes {
				if outcomes[i].Status == "pending" || outcomes[i].Status == "scheduled" {
					outcomes[i].Status = "interrupted"
					outcomes[i].FailureCategory = "workflow_interrupted"
					outcomes[i].SettledAt = delta.At
				}
			}
			continue
		}
		for _, intent := range delta.Intents {
			invocation := intent.Outcome.Invocation
			if _, exists := index[invocation.ID]; exists {
				return nil, errors.New("duplicate continuation journal intent")
			}
			index[invocation.ID] = len(outcomes)
			outcomes = append(outcomes, intent.Outcome)
		}
		if delta.InvocationID != "" {
			position, ok := index[delta.InvocationID]
			if !ok {
				return nil, errors.New("continuation journal settlement has no intent")
			}
			outcome := &outcomes[position]
			switch delta.Status {
			case "scheduled":
				if outcome.Status != "pending" {
					return nil, errors.New("continuation scheduled more than once")
				}
				outcome.ScheduledAt = delta.At
			case "succeeded", "exhausted", "interrupted":
				if outcome.Status != "scheduled" && outcome.Status != "pending" {
					return nil, errors.New("continuation settled more than once")
				}
				outcome.SettledAt = delta.At
				if delta.Status != "succeeded" {
					outcome.FailureCategory = "execution_failed"
				}
			default:
				return nil, errors.New("unsupported continuation journal status")
			}
			outcome.Status = delta.Status
		}
	}
	return outcomes, nil
}

// errAssociationRejected distinguishes definite association mismatches from unavailable authority.
var errAssociationRejected = errors.New("continuation association rejected")

// verifyContinuation validates the immutable parent association before any application callback.
func (b *backend) verifyContinuation(ctx context.Context, d delivery) error {
	invocation := d.Metadata.Continuation
	if invocation == nil {
		return nil
	}
	info := activity.GetInfo(ctx)
	if info.WorkflowExecution.ID != invocation.WorkflowID || info.ActivityID != invocation.ID || d.Metadata.JobID != invocation.ID {
		return fmt.Errorf("%w: continuation execution association rejected", errAssociationRejected)
	}
	coordinator := &coordinator{backend: b}
	def, deltas, _, err := coordinator.readStateRunAdmitted(ctx, invocation.WorkflowID, "", info.WorkflowExecution.RunID)
	if err != nil {
		return err
	}
	if def.Resource != b.clientBinding.Namespace()+":"+b.taskQueue || invocation.DispatchID != def.DispatchID {
		return fmt.Errorf("%w: continuation resource association rejected", errAssociationRejected)
	}
	var expected *delivery
	scheduled := false
	for _, delta := range deltas {
		for _, intent := range delta.Intents {
			if intent.Outcome.Invocation.ID == invocation.ID {
				copy := intent.Job
				expected = &copy
			}
		}
		if delta.InvocationID == invocation.ID {
			if delta.Status == "scheduled" {
				scheduled = true
			} else {
				scheduled = false
			}
		}
	}
	if expected == nil || !scheduled || !reflect.DeepEqual(*expected, d) {
		return fmt.Errorf("%w: continuation intent association rejected", errAssociationRejected)
	}
	return nil
}

// continuationID binds an invocation to original lineage, resource and declaration without hashing payloads.
func continuationID(resource, id, run, dispatch, phase, member string, sequence, declaration int) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%d\x00%d", resource, id, run, dispatch, phase, member, sequence, declaration)))
	return "cnt_" + hex.EncodeToString(sum[:])
}
