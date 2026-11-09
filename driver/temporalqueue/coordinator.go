package temporalqueue

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/goforj/queue"
	"github.com/goforj/queue/busruntime"
	"github.com/goforj/queue/internal/runtimehook"
	engine "github.com/goforj/queue/internal/workflow"

	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// coordinator shares physical delivery and client authority with ordinary jobs.
type coordinator struct {
	backend *backend
	binding *runtimehook.WorkflowBinding
}

// Binding identifies the executor selected by root construction.
func (c *coordinator) Binding() *runtimehook.WorkflowBinding { return c.binding }

// Chain creates a durable sequential workflow definition.
func (c *coordinator) Chain(jobs ...engine.Job) engine.ChainBuilder {
	return &chainBuilder{c: c, jobs: append([]engine.Job(nil), jobs...)}
}

// Batch creates a durable concurrent workflow definition.
func (c *coordinator) Batch(jobs ...engine.Job) engine.BatchBuilder {
	return &batchBuilder{c: c, jobs: append([]engine.Job(nil), jobs...)}
}

// Prune leaves history retention under namespace operator control.
func (c *coordinator) Prune(context.Context, time.Time) error {
	return fmt.Errorf("%w: Temporal history retention is managed by the namespace", queue.ErrWorkflowUnsupported)
}

// workflowDefinition persists complete routing and identity before any worker runs.
type workflowDefinition struct {
	JournalBudget    int
	EncodingOverhead int
	CreatedAt        time.Time
	WorkflowQueue    string
	Version          int
	Kind             string
	ID               string
	DispatchID       string
	Queue            string
	Name             string
	AllowFailures    bool
	Resource         string
	Hooks            []hookDefinition
	Jobs             []delivery
}

// journalDelta is a bounded, monotonic reducer event stored in original workflow history.
type journalDelta struct {
	taskCommit   int64
	Version      int
	Sequence     int
	Index        int
	Failed       bool
	Cancelled    bool
	Terminal     bool
	Skipped      bool
	Intents      []continuationInvocation
	InvocationID string
	Status       string
	At           time.Time
}

// newID creates opaque workflow identifiers without hashing application payloads.
func newID(prefix string) string {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		panic("workflow identity generation failed")
	}
	return prefix + hex.EncodeToString(id[:])
}

// start validates and accepts a portable workflow without process-local callbacks.
func (c *coordinator) start(ctx context.Context, kind, name, queueName string, allow bool, jobs []engine.Job, hooks []engine.ContinuationDefinition) (string, error) {
	b := c.backend
	release, err := b.entry.enter()
	if err != nil {
		return "", err
	}
	defer release()
	if len(jobs) == 0 || len(jobs) > maxMembers {
		return "", errors.New("Temporal workflow member count is out of bounds")
	}
	if queueName == "" {
		queueName = b.taskQueue
	}
	if queueName != b.taskQueue {
		return "", errors.New("Temporal queue route is not configured")
	}
	def := workflowDefinition{JournalBudget: b.clientBinding.limits.MaxPayloadBytes, EncodingOverhead: b.clientBinding.options.DataConverter.(*guardedConverter).overhead, CreatedAt: time.Now().UTC(), WorkflowQueue: b.workflowQueue, Resource: b.clientBinding.Namespace() + ":" + b.taskQueue, Version: 1, Kind: kind, ID: newID(kind + "_"), DispatchID: newID("dsp_"), Queue: queueName, Name: name, AllowFailures: allow}
	for _, job := range jobs {
		var payload []byte
		switch raw := job.Payload.(type) {
		case nil:
		case json.RawMessage:
			payload = append([]byte(nil), raw...)
		case []byte:
			payload = append([]byte(nil), raw...)
		default:
			payload, err = json.Marshal(raw)
			if err != nil {
				return "", errors.New("workflow payload encoding failed")
			}
		}
		timeout := job.Options.Timeout
		route := job.Options.Queue
		if route == "" {
			route = queueName
		}
		if route != b.taskQueue || job.Options.UniqueFor != 0 {
			return "", errors.New("Temporal workflow member routing or uniqueness is unsupported")
		}
		d := delivery{WorkflowQueue: b.workflowQueue, ActivityQueue: b.activityQueue, Version: 1, Type: job.Type, Payload: payload, Timeout: timeout, Retry: job.Options.Retry, Backoff: job.Options.Backoff, Delay: job.Options.Delay}
		d.Metadata = busruntime.DeliveryMetadata{SchemaVersion: busruntime.DeliveryMetadataVersion, DispatchID: def.DispatchID, JobID: newID("job_"), Queue: route}
		if kind == "chain" {
			d.Metadata.ChainID = def.ID
		} else {
			d.Metadata.BatchID = def.ID
		}
		if err := d.resolve(b.cfg.DefaultActivityTimeout); err != nil {
			return "", err
		}
		if err := d.validate(); err != nil {
			return "", err
		}
		def.Jobs = append(def.Jobs, d)
	}
	for _, hook := range hooks {
		job := hook.Job
		timeout := job.Options.Timeout
		route := job.Options.Queue
		if route == "" {
			route = queueName
		}
		if route != b.taskQueue || job.Options.UniqueFor != 0 {
			return "", errors.New("unsupported continuation route or uniqueness")
		}
		d := delivery{WorkflowQueue: b.workflowQueue, ActivityQueue: b.activityQueue, Version: 1, Type: job.Type, Payload: append([]byte(nil), job.Payload...), Timeout: timeout, Retry: job.Options.Retry, Backoff: job.Options.Backoff, Delay: job.Options.Delay, Metadata: busruntime.DeliveryMetadata{SchemaVersion: busruntime.DeliveryMetadataVersion, DispatchID: def.DispatchID, JobID: "definition", Queue: route}}
		if def.Kind == "chain" {
			d.Metadata.ChainID = def.ID
		} else {
			d.Metadata.BatchID = def.ID
		}
		if err := d.resolve(b.cfg.DefaultActivityTimeout); err != nil {
			return "", err
		}
		def.Hooks = append(def.Hooks, hookDefinition{Phase: hook.Phase, Job: d})
	}
	if err := def.validate(); err != nil {
		return "", err
	}
	dc := converter.WithDataConverterSerializationContext(b.clientBinding.options.DataConverter, converter.WorkflowSerializationContext{Namespace: b.clientBinding.Namespace(), WorkflowID: def.ID})
	if _, err := dc.ToPayload(journalValue{Delta: largestJournalDelta(def)}); err != nil {
		return "", errors.New("Temporal continuation journal cannot fit the configured converter")
	}
	err = b.acceptWorkflow(ctx, def.ID, coordinatorWorkflowType, def)
	if err != nil {
		return def.ID, err
	}
	return def.ID, nil
}

// validate rejects malformed persisted definitions before application activity scheduling.
func (d workflowDefinition) validate() error {
	if d.JournalBudget < 1024 || d.JournalBudget > 2<<20 || d.EncodingOverhead < 0 || d.EncodingOverhead > 4096 {
		return errors.New("invalid Temporal journal budget")
	}
	if d.CreatedAt.IsZero() || d.Version != 1 || (d.Kind != "chain" && d.Kind != "batch") || d.ID == "" || d.DispatchID == "" || d.Queue == "" || len(d.Jobs) == 0 || len(d.Jobs) > maxMembers || len(d.Name) > 256 {
		return errors.New("invalid Temporal workflow definition")
	}
	seen := make(map[string]bool, len(d.Jobs))
	total := 0
	for _, job := range d.Jobs {
		if err := job.validate(); err != nil {
			return err
		}
		if job.WorkflowQueue != d.WorkflowQueue || job.Metadata.Continuation != nil || (d.Kind == "chain" && job.Metadata.BatchID != "") || (d.Kind == "batch" && job.Metadata.ChainID != "") || seen[job.Metadata.JobID] || job.Metadata.DispatchID != d.DispatchID || job.Metadata.Queue != d.Queue || (d.Kind == "chain" && job.Metadata.ChainID != d.ID) || (d.Kind == "batch" && job.Metadata.BatchID != d.ID) {
			return errors.New("invalid Temporal workflow member identity")
		}
		seen[job.Metadata.JobID] = true
		total += len(job.Payload) + len(job.Type) + 512
	}
	if len(d.Hooks) > 32 {
		return errors.New("too many workflow hooks")
	}
	reserved := 0
	invocations := 0
	for _, hook := range d.Hooks {
		if hook.Phase != "success" && hook.Phase != "failure" && hook.Phase != "completion" && (d.Kind != "batch" || hook.Phase != "progress") {
			return errors.New("invalid workflow continuation phase")
		}
		if err := hook.Job.validate(); err != nil {
			return err
		}
		if hook.Job.WorkflowQueue != d.WorkflowQueue || hook.Job.Metadata.DispatchID != d.DispatchID || hook.Job.Metadata.Queue != d.Queue || hook.Job.Metadata.Continuation != nil || (d.Kind == "chain" && (hook.Job.Metadata.ChainID != d.ID || hook.Job.Metadata.BatchID != "")) || (d.Kind == "batch" && (hook.Job.Metadata.BatchID != d.ID || hook.Job.Metadata.ChainID != "")) {
			return errors.New("invalid workflow continuation correlation")
		}

		total += len(hook.Job.Payload) + len(hook.Job.Type) + 512
		copies := 1
		if hook.Phase == "progress" {
			copies = len(d.Jobs)
		}
		reserved += copies * (len(hook.Job.Payload)*2 + 4096)
		invocations += copies
	}
	if reserved > 4<<20 || invocations > 4096 {
		return errors.New("workflow continuation history exceeds reserved budget")
	}
	if total > 192<<10 {
		return errors.New("Temporal workflow definition exceeds byte budget")
	}
	if raw, err := json.Marshal(largestJournalDelta(d)); err != nil || len(raw)+d.EncodingOverhead > d.JournalBudget {
		return errors.New("Temporal continuation transition exceeds its payload budget")
	}
	return nil
}

// publishJournal commits a reducer event in the same workflow task as its commands.
func publishJournal(ctx workflow.Context, delta journalDelta) error {
	state := &journalEncodingState{}
	err := workflow.UpsertMemo(ctx, map[string]interface{}{journalKey: journalValue{Delta: delta, state: state}})
	if state.err != nil {
		return state.err
	}
	return err
}

// scheduleContinuationActivity preserves old command order while rejecting SDK preflight failures before recording a new scheduling commit.
func scheduleContinuationActivity(ctx workflow.Context, intent continuationInvocation, emit func(workflow.Context, journalDelta) error) (workflow.Future, error) {
	scheduled := journalDelta{Index: -1, InvocationID: intent.Outcome.Invocation.ID, Status: "scheduled"}
	legacy := workflow.GetVersion(ctx, "goforj.queue.continuation.schedule-command", workflow.DefaultVersion, 1) == workflow.DefaultVersion
	if legacy {
		if err := emit(ctx, scheduled); err != nil {
			return nil, err
		}
		return workflow.ExecuteActivity(ctx, deliveryActivityType, intent.Job), nil
	}
	future := workflow.ExecuteActivity(ctx, deliveryActivityType, intent.Job)
	if err := activitySchedulingError(ctx, future); err != nil {
		return nil, err
	}
	if err := emit(ctx, scheduled); err != nil {
		return nil, err
	}
	return future, nil
}

// activitySchedulingError separates SDK preflight rejection from an actual scheduled activity outcome.
func activitySchedulingError(ctx workflow.Context, future workflow.Future) error {
	if future.IsReady() {
		if err := future.Get(ctx, nil); err != nil {
			var activityErr *temporal.ActivityError
			if !errors.As(err, &activityErr) {
				if adapterUnavailable(err) {
					return unavailableAdapterError()
				}
				return terminalAdapterCause(err)
			}
		}
	}
	return nil
}

// coordinatorWorkflow uses only replay-safe SDK commands and immutable input.
func coordinatorWorkflow(ctx workflow.Context, def workflowDefinition) error {
	if err := validatePortableExecution(ctx, def.ID, def.WorkflowQueue, coordinatorWorkflowType); err != nil {
		return err
	}
	if def.Resource != workflow.GetInfo(ctx).Namespace+":"+def.Queue {
		return temporal.NewNonRetryableApplicationError("invalid workflow resource", "goforj.invalid.v1", nil)
	}
	if err := def.validate(); err != nil {
		return temporal.NewNonRetryableApplicationError("invalid workflow definition", "goforj.invalid.v1", nil)
	}
	checkBusinessScheduling := workflow.GetVersion(ctx, "goforj.queue.business.schedule-preflight", workflow.DefaultVersion, 1) == 1
	journalSequence, invocationSequence := 0, 0
	var intents []continuationInvocation
	businessDone, ledgerDone := false, false
	var runnerErr error
	journalBroken := false
	failJournal := func(err error) {
		if runnerErr == nil {
			runnerErr = err
		}
		journalBroken = true
	}
	emit := func(publishCtx workflow.Context, delta journalDelta) error {
		delta.Version = 1
		delta.Sequence = journalSequence + 1
		delta.At = workflow.Now(publishCtx)
		if err := publishJournal(publishCtx, delta); err != nil {
			return err
		}
		journalSequence++
		return nil
	}
	workflow.Go(ctx, func(ledgerCtx workflow.Context) {
		defer func() { ledgerDone = true }()
		for head := 0; ; head++ {
			if err := workflow.Await(ledgerCtx, func() bool { return head < len(intents) || businessDone || journalBroken }); err != nil {
				runnerErr = err
				return
			}
			if journalBroken || head == len(intents) {
				return
			}
			intent := intents[head]
			activityCtx := activityContext(ledgerCtx, intent.Job)
			options := workflow.GetActivityOptions(activityCtx)
			options.ActivityID = intent.Outcome.Invocation.ID
			activityCtx = workflow.WithActivityOptions(activityCtx, options)
			var err error
			if intent.Job.Resolved.Delay > 0 {
				err = awaitDeliveryDelay(activityCtx, intent.Job.Resolved.Delay, func() bool { return journalBroken })
			}
			if journalBroken {
				return
			}
			if err == nil {
				future, schedulingErr := scheduleContinuationActivity(activityCtx, intent, emit)
				if schedulingErr != nil {
					failJournal(schedulingErr)
					return
				}
				err = future.Get(activityCtx, nil)
			}
			if journalBroken {
				return
			}
			status := "succeeded"
			if err != nil {
				status = "exhausted"
				if temporal.IsCanceledError(err) {
					status = "interrupted"
				}
			}
			if err := emit(activityCtx, journalDelta{Index: -1, InvocationID: intent.Outcome.Invocation.ID, Status: status}); err != nil {
				failJournal(err)
				return
			}
		}
	})
	processed, failed, collected := 0, 0, 0
	terminal, cancelled, interrupted := false, false, false
	businessCtx := ctx
	settle := func(index int, err error) error {
		collected++
		if journalBroken {
			return nil
		}
		skipped := temporal.IsCanceledError(err)
		interrupted = interrupted || skipped
		if !skipped {
			processed++
			if err != nil {
				failed++
			}
		}
		becameTerminal := !terminal && ((def.Kind == "chain" && !skipped && (err != nil || processed == len(def.Jobs))) || (def.Kind == "batch" && ((err != nil && !def.AllowFailures && !skipped) || processed == len(def.Jobs))))
		if becameTerminal {
			terminal = true
			if err != nil && !def.AllowFailures {
				cancelled = true

			}
		}
		summary := busruntime.ContinuationSummary{Kind: def.Kind, MemberID: def.Jobs[index].Metadata.JobID, Total: len(def.Jobs), Processed: processed, Pending: len(def.Jobs) - processed, Failed: failed, Completed: terminal && def.Kind == "batch" || terminal && failed == 0, Cancelled: cancelled}
		if def.Kind == "chain" {
			summary.NextIndex = processed - failed
		}
		var triggered []continuationInvocation
		if !skipped {
			triggered = triggerInvocations(ctx, def, summary, err != nil && failed == 1, becameTerminal, &invocationSequence)

		}
		if err := emit(ctx, journalDelta{Index: index, Failed: err != nil && !skipped, Skipped: skipped, Cancelled: cancelled, Terminal: becameTerminal, Intents: triggered}); err != nil {
			failJournal(err)
			return nil
		}
		intents = append(intents, triggered...)
		return nil
	}
	if def.Kind == "chain" {
		for i, d := range def.Jobs {
			var err error
			if d.Resolved.Delay > 0 {
				err = awaitDeliveryDelay(businessCtx, d.Resolved.Delay, func() bool { return journalBroken })
			}
			if err == nil {
				activityCtx := activityContext(businessCtx, d)
				future := workflow.ExecuteActivity(activityCtx, deliveryActivityType, d)
				if checkBusinessScheduling {
					if err := activitySchedulingError(activityCtx, future); err != nil {
						failJournal(err)
						break
					}
				}
				err = future.Get(businessCtx, nil)
			}
			if journalErr := settle(i, err); journalErr != nil {
				return journalErr
			}
			if err != nil || journalBroken {
				break
			}
		}

	} else {
		futures := make([]workflow.Future, len(def.Jobs))
		done := make([]bool, len(def.Jobs))
		for i, d := range def.Jobs {
			if journalBroken {
				future, settable := workflow.NewFuture(ctx)
				settable.Set(nil, runnerErr)
				futures[i] = future
				continue
			}
			if d.Resolved.Delay == 0 {
				futures[i] = workflow.ExecuteActivity(activityContext(businessCtx, d), deliveryActivityType, d)
				if checkBusinessScheduling {
					if err := activitySchedulingError(businessCtx, futures[i]); err != nil {
						failJournal(err)
					}
				}
				continue
			}
			future, settable := workflow.NewFuture(ctx)
			futures[i] = future
			d := d
			workflow.Go(ctx, func(memberCtx workflow.Context) {
				err := awaitDeliveryDelay(memberCtx, d.Resolved.Delay, func() bool { return journalBroken })
				if err == nil && !journalBroken {
					activityCtx := activityContext(memberCtx, d)
					future := workflow.ExecuteActivity(activityCtx, deliveryActivityType, d)
					if checkBusinessScheduling {
						if err := activitySchedulingError(activityCtx, future); err != nil {
							failJournal(err)
						}
					}
					err = future.Get(memberCtx, nil)
				}
				settable.Set(nil, err)
			})
		}
		join, _ := workflow.NewDisconnectedContext(ctx)
		for collected < len(def.Jobs) {
			if err := workflow.Await(join, func() bool {
				for i, future := range futures {
					if !done[i] && future.IsReady() {
						return true
					}
				}
				return false
			}); err != nil {
				return err
			}
			for i, future := range futures {
				if done[i] || !future.IsReady() {
					continue
				}
				done[i] = true
				if err := settle(i, future.Get(join, nil)); err != nil {
					return err
				}
			}
		}
	}
	businessDone = true
	join, _ := workflow.NewDisconnectedContext(ctx)
	if err := workflow.Await(join, func() bool { return ledgerDone }); err != nil {
		return err
	}
	if runnerErr != nil {
		return runnerErr
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if cancelled || (def.Kind == "chain" && failed > 0) {
		return temporal.NewNonRetryableApplicationError("queue workflow business failed", "goforj.business-failed.v1", nil)
	}
	if interrupted {
		return temporal.NewNonRetryableApplicationError("queue workflow interrupted", "goforj.workflow-interrupted.v1", nil)
	}
	return nil
}

// readState reconstructs state from accepted input and committed history without a worker query.
func (c *coordinator) readState(ctx context.Context, id, kind string) (workflowDefinition, []journalDelta, time.Time, error) {
	return c.readStateRun(ctx, id, kind, "")
}

// readStateRun binds original-history decoding to the actual activity execution run.
func (c *coordinator) readStateRun(ctx context.Context, id, kind, runID string) (workflowDefinition, []journalDelta, time.Time, error) {
	release, err := c.backend.entry.enter()
	if err != nil {
		return workflowDefinition{}, nil, time.Time{}, err
	}
	defer release()
	return c.readStateRunAdmitted(ctx, id, kind, runID)
}

// readStateRunAdmitted uses an existing activity or public-read lifetime without reopening entry admission.
func (c *coordinator) readStateRunAdmitted(ctx context.Context, id, kind, runID string) (workflowDefinition, []journalDelta, time.Time, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	description, describeErr := c.backend.clientBinding.client.DescribeWorkflowExecution(ctx, id, runID)
	if describeErr != nil {
		return workflowDefinition{}, nil, time.Time{}, historyReadError(ctx, describeErr)
	}
	actualRun := description.GetWorkflowExecutionInfo().GetExecution().GetRunId()
	watermark := description.GetWorkflowExecutionInfo().GetHistoryLength()
	if actualRun == "" || watermark <= 0 {
		return workflowDefinition{}, nil, time.Time{}, errors.New("Temporal workflow history watermark unavailable")
	}
	iter := &historyPages{ctx: ctx, service: c.backend.clientBinding.client.WorkflowService(), namespace: c.backend.clientBinding.Namespace(), id: id, runID: actualRun}
	var def workflowDefinition
	var deltas []journalDelta
	var created time.Time
	originalRun := ""
	activities := make(map[string]int64)
	dc := converter.WithDataConverterSerializationContext(c.backend.clientBinding.options.DataConverter, converter.WorkflowSerializationContext{Namespace: c.backend.clientBinding.Namespace(), WorkflowID: id})
	events := 0
	lastEventID := int64(0)
	for iter.HasNext() {
		event, err := iter.Next()
		if err != nil {
			return def, nil, created, historyReadError(ctx, err)
		}
		if event == nil || event.EventId != lastEventID+1 {
			return def, nil, created, errors.New("Temporal workflow history is incomplete")
		}
		if event.EventId > watermark {
			break
		}
		lastEventID = event.EventId
		events++
		if events > 100000 {
			return def, nil, created, errors.New("Temporal workflow history exceeds read budget")
		}
		if start := event.GetWorkflowExecutionStartedEventAttributes(); start != nil {
			if start.GetWorkflowType().GetName() != coordinatorWorkflowType || start.RetryPolicy != nil || start.CronSchedule != "" || start.Attempt != 1 || start.ContinuedExecutionRunId != "" || start.ParentWorkflowExecution != nil {
				return def, nil, created, errors.New("workflow is not a portable coordinator")
			}
			if err := dc.FromPayloads(start.Input, &def); err != nil {
				return def, nil, created, err
			}
			if actualRun != start.OriginalExecutionRunId || start.FirstExecutionRunId != start.OriginalExecutionRunId {
				return def, nil, created, errors.New("Temporal portable reset or run transition is unsupported")
			}
			if def.Resource != c.backend.clientBinding.Namespace()+":"+c.backend.taskQueue || def.Queue != c.backend.taskQueue || def.WorkflowQueue != c.backend.workflowQueue {
				return def, nil, created, errors.New("Temporal workflow belongs to another resource")
			}
			created = def.CreatedAt
			originalRun = start.OriginalExecutionRunId
			if err := def.validate(); err != nil {
				return def, nil, created, err
			}
			if def.ID != id || (kind != "" && def.Kind != kind) {
				return def, nil, created, errors.New("workflow identity does not match requested state")
			}
		}
		if event.GetWorkflowExecutionCanceledEventAttributes() != nil || event.GetWorkflowExecutionTerminatedEventAttributes() != nil || event.GetWorkflowExecutionTimedOutEventAttributes() != nil || event.GetWorkflowExecutionFailedEventAttributes() != nil {
			deltas = append(deltas, journalDelta{Version: 1, Sequence: len(deltas) + 1, Index: -2, Status: "workflow-interrupted", At: event.EventTime.AsTime()})
		}
		if activity := event.GetActivityTaskScheduledEventAttributes(); activity != nil && strings.HasPrefix(activity.ActivityId, "cnt_") {
			if _, duplicate := activities[activity.ActivityId]; duplicate {
				return def, nil, created, errors.New("duplicate continuation activity command")
			}
			activities[activity.ActivityId] = activity.WorkflowTaskCompletedEventId
		}
		if update := event.GetWorkflowPropertiesModifiedEventAttributes(); update != nil {
			if payload, ok := update.UpsertedMemo.GetFields()[journalKey]; ok {
				var delta journalDelta
				if err := dc.FromPayload(payload, &delta); err != nil {
					return def, nil, created, err
				}
				if delta.Version != 1 || delta.Sequence != len(deltas)+1 || (delta.Index < 0 && delta.InvocationID == "") || delta.Index >= len(def.Jobs) {
					return def, nil, created, errors.New("invalid Temporal journal sequence")
				}
				delta.taskCommit = update.WorkflowTaskCompletedEventId
				for i := range delta.Intents {
					invocation := delta.Intents[i].Outcome.Invocation
					if invocation.Declaration < 0 || invocation.Declaration >= len(def.Hooks) {
						return def, nil, created, errors.New("unknown continuation declaration")
					}
					job := def.Hooks[invocation.Declaration].Job
					job.Metadata.JobID = invocation.ID
					job.Metadata.Continuation = &invocation
					delta.Intents[i].Job = job
				}
				deltas = append(deltas, delta)
			}
		}
	}
	if lastEventID != watermark {
		return def, nil, created, errors.New("Temporal workflow history prefix is incomplete")
	}
	if def.ID == "" {
		return def, nil, created, errors.New("Temporal workflow accepted definition is absent")
	}
	if err := validateJournal(def, deltas, originalRun, activities); err != nil {
		return def, nil, created, err
	}
	return def, deltas, created, nil
}

// FindChain reconstructs accepted or terminal chain state from original history.
func (c *coordinator) FindChain(ctx context.Context, id string) (engine.ChainState, error) {
	def, deltas, created, err := c.readState(ctx, id, "chain")
	if err != nil {
		return engine.ChainState{}, err
	}
	st := engine.ChainState{ChainID: id, DispatchID: def.DispatchID, Queue: def.Queue, CreatedAt: created, UpdatedAt: created}
	for _, d := range def.Jobs {
		st.Nodes = append(st.Nodes, engine.ChainNode{NodeID: d.Metadata.JobID, Job: engine.StoredJob{Type: d.Type, Payload: json.RawMessage(d.Payload), Options: engine.JobOptions{Queue: d.Metadata.Queue, Timeout: d.Timeout, Retry: d.Retry, Backoff: d.Backoff, Delay: d.Delay}}})
	}
	for _, d := range deltas {
		if d.Status == "workflow-interrupted" && !st.Completed && !st.Failed {
			st.Failed = true
			st.Failure = "workflow interrupted"
			st.UpdatedAt = d.At
		}
		if d.Index < 0 || d.Skipped {
			continue
		}
		st.UpdatedAt = d.At
		if d.Failed {
			st.Failed = true
			st.Failure = "queue execution failed"
		} else {
			st.NextIndex++
		}
		st.Completed = d.Terminal && !d.Failed
	}
	return st, nil
}

// FindBatch reconstructs aggregate counters without requiring any running poller.
func (c *coordinator) FindBatch(ctx context.Context, id string) (engine.BatchState, error) {
	def, deltas, created, err := c.readState(ctx, id, "batch")
	if err != nil {
		return engine.BatchState{}, err
	}
	st := engine.BatchState{BatchID: id, DispatchID: def.DispatchID, Name: def.Name, Queue: def.Queue, AllowFailed: def.AllowFailures, Total: len(def.Jobs), Pending: len(def.Jobs), CreatedAt: created, UpdatedAt: created}
	for _, d := range deltas {
		if d.Status == "workflow-interrupted" && !st.Completed {
			st.Completed = true
			st.Cancelled = true
			st.UpdatedAt = d.At
		}
		if d.Index < 0 || d.Skipped {
			continue
		}
		st.Pending--
		st.Processed++
		if d.Failed {
			st.Failed++
		}
		st.Cancelled = d.Cancelled
		st.Completed = st.Completed || d.Terminal
		st.UpdatedAt = d.At
	}
	return st, nil
}

// chainBuilder retains immutable member input and validates unsupported closure callbacks.
type chainBuilder struct {
	hooks   []engine.ContinuationDefinition
	c       *coordinator
	jobs    []engine.Job
	queue   string
	closure bool
}

// OnQueue selects the configured resource route.
func (b *chainBuilder) OnQueue(q string) engine.ChainBuilder { b.queue = q; return b }

// Catch records an unsupported process-local callback for dispatch-time rejection.
func (b *chainBuilder) Catch(fn func(context.Context, engine.ChainState, error) error) engine.ChainBuilder {
	b.closure = b.closure || fn != nil
	return b
}

// Finally records an unsupported process-local callback for dispatch-time rejection.
func (b *chainBuilder) Finally(fn func(context.Context, engine.ChainState) error) engine.ChainBuilder {
	b.closure = b.closure || fn != nil
	return b
}

// Dispatch validates the entire definition before remote acceptance.
func (b *chainBuilder) Dispatch(ctx context.Context) (string, error) {
	if b.closure {
		return "", errors.New("Temporal workflows require named durable continuations")
	}
	return b.c.start(ctx, "chain", "", b.queue, false, b.jobs, b.hooks)
}

// batchBuilder retains portable aggregate policy without serializing Go closures.
type batchBuilder struct {
	hooks          []engine.ContinuationDefinition
	c              *coordinator
	jobs           []engine.Job
	queue, name    string
	allow, closure bool
}

// Name sets the persisted aggregate display name.
func (b *batchBuilder) Name(n string) engine.BatchBuilder { b.name = n; return b }

// OnQueue selects the configured resource route.
func (b *batchBuilder) OnQueue(q string) engine.BatchBuilder { b.queue = q; return b }

// AllowFailures keeps independent members running after a terminal member failure.
func (b *batchBuilder) AllowFailures() engine.BatchBuilder { b.allow = true; return b }

// Progress records an unsupported process-local callback for dispatch-time rejection.
func (b *batchBuilder) Progress(fn func(context.Context, engine.BatchState) error) engine.BatchBuilder {
	b.closure = b.closure || fn != nil
	return b
}

// Then records an unsupported process-local callback for dispatch-time rejection.
func (b *batchBuilder) Then(fn func(context.Context, engine.BatchState) error) engine.BatchBuilder {
	b.closure = b.closure || fn != nil
	return b
}

// Catch records an unsupported process-local callback for dispatch-time rejection.
func (b *batchBuilder) Catch(fn func(context.Context, engine.BatchState, error) error) engine.BatchBuilder {
	b.closure = b.closure || fn != nil
	return b
}

// Finally records an unsupported process-local callback for dispatch-time rejection.
func (b *batchBuilder) Finally(fn func(context.Context, engine.BatchState) error) engine.BatchBuilder {
	b.closure = b.closure || fn != nil
	return b
}

// Dispatch validates the entire definition before remote acceptance.
func (b *batchBuilder) Dispatch(ctx context.Context) (string, error) {
	if b.closure {
		return "", errors.New("Temporal workflows require named durable continuations")
	}
	return b.c.start(ctx, "batch", b.name, b.queue, b.allow, b.jobs, b.hooks)
}
