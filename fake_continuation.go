package queue

import (
	"context"
	"time"

	"github.com/goforj/queue/busruntime"
	"github.com/goforj/queue/internal/workflow"
)

// ContinuationDefinition describes a named hook without representing a triggered invocation.
type ContinuationDefinition struct {
	Phase string
	Job   StoredJob
}

// ContinuationRecord contains the immutable hook catalog of an accepted fake workflow.
type ContinuationRecord struct {
	WorkflowID  string
	Definitions []ContinuationDefinition
}

// recordContinuationDefinitions retains definitions separately from physical dispatch and outcomes.
func (r *fakeWorkflowRecorder) recordContinuationDefinitions(id string, definitions []workflow.ContinuationDefinition) {
	if len(definitions) == 0 {
		return
	}
	copied := make([]ContinuationDefinition, len(definitions))
	for i, definition := range definitions {
		copied[i] = ContinuationDefinition{Phase: definition.Phase, Job: storedJobFromWorkflow(definition.Job)}
	}
	r.continuations[id] = copied
}

// ContinuationRecords returns accepted fake workflow definitions without phantom pending invocations.
func (f *FakeQueue) ContinuationRecords() []ContinuationRecord {
	recorder := f.state.workflow
	f.state.mu.RLock()
	defer f.state.mu.RUnlock()
	var records []ContinuationRecord
	ids := append(append([]string(nil), recorder.acceptedChainIDs...), recorder.acceptedBatchIDs...)
	for _, id := range ids {
		definitions, ok := recorder.continuations[id]
		if !ok {
			continue
		}
		copy := make([]ContinuationDefinition, len(definitions))
		for i, definition := range definitions {
			copy[i] = definition
			copy[i].Job.Payload = cloneWorkflowPayload(definition.Job.Payload)
		}
		records = append(records, ContinuationRecord{WorkflowID: id, Definitions: copy})
	}
	return records
}

// FindContinuations returns committed triggered fake outcomes independently of definitions.
func (f *FakeQueue) FindContinuations(ctx context.Context, id string) ([]ContinuationOutcome, error) {
	return f.state.workflow.ContinuationOutcomes(ctx, id)
}

// PrepareContinuations confirms the fake's atomic process-local store capability.
func (r *fakeWorkflowRecorder) PrepareContinuations(ctx context.Context) error {
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	return r.store.PrepareContinuations(ctx)
}

// ContinuationOutcomes returns isolated invocation views from the fake store.
func (r *fakeWorkflowRecorder) ContinuationOutcomes(ctx context.Context, id string) ([]busruntime.ContinuationOutcome, error) {
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	return r.store.ContinuationOutcomes(ctx, id)
}

// ClaimContinuation forwards fenced publication without fabricating fake executions.
func (r *fakeWorkflowRecorder) ClaimContinuation(ctx context.Context, resource, owner string, now time.Time, lease time.Duration) (workflow.ContinuationClaim, bool, error) {
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	return r.store.ClaimContinuation(ctx, resource, owner, now, lease)
}

// ValidateContinuation preserves authoritative fake invocation association.
func (r *fakeWorkflowRecorder) ValidateContinuation(ctx context.Context, claim workflow.ContinuationClaim) (bool, error) {
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	return r.store.ValidateContinuation(ctx, claim)
}

// SettleContinuation preserves durable fake head ordering and attempt outcomes.
func (r *fakeWorkflowRecorder) SettleContinuation(ctx context.Context, claim workflow.ContinuationClaim, status string, at time.Time) error {
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	return r.store.SettleContinuation(ctx, claim, status, at)
}

// BeginContinuation preserves execution fencing when tests exercise fake store transitions.
func (r *fakeWorkflowRecorder) BeginContinuation(ctx context.Context, claim workflow.ContinuationClaim, owner string, now time.Time) (workflow.ContinuationClaim, bool, error) {
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	return r.store.BeginContinuation(ctx, claim, owner, now)
}

// RenewContinuation preserves actual callback lease authority during fake transitions.
func (r *fakeWorkflowRecorder) RenewContinuation(ctx context.Context, claim workflow.ContinuationClaim, now time.Time) error {
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	return r.store.RenewContinuation(ctx, claim, now)
}
