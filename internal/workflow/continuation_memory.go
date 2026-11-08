package workflow

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/goforj/queue/busruntime"
)

// PrepareContinuations confirms the built-in memory store supports atomic process-local intents.
func (m *memoryStore) PrepareContinuations(context.Context) error { return nil }

// triggerChainContinuations captures terminal chain counters under the parent's mutex.
func (m *memoryStore) triggerChainContinuations(state ChainState, member string, failed bool) {
	ledger := m.continuations[state.ChainID]
	if ledger == nil {
		return
	}
	ledger.trigger(busruntime.ContinuationSummary{Kind: "chain", MemberID: member, Total: len(state.Nodes), Processed: state.NextIndex, Pending: len(state.Nodes) - state.NextIndex, NextIndex: state.NextIndex, Failed: continuationFailureCount(failed), Completed: state.Completed, Cancelled: failed}, failed, state.Completed || failed, state.UpdatedAt)
}

// continuationFailureCount converts a single terminal failure into the safe summary count.
func continuationFailureCount(value bool) int {
	if value {
		return 1
	}
	return 0
}

// ContinuationOutcomes copies committed invocation views without exposing mutable intent payloads.
func (m *memoryStore) ContinuationOutcomes(_ context.Context, id string) ([]busruntime.ContinuationOutcome, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ledger := m.continuations[id]
	if ledger == nil {
		return nil, ErrNotFound
	}
	result := make([]busruntime.ContinuationOutcome, len(ledger.Invocations))
	for i, intent := range ledger.Invocations {
		result[i] = intent.Outcome
	}
	return result, nil
}

// ClaimContinuation serializes resource-scoped recovery ownership with parent transitions.
func (m *memoryStore) ClaimContinuation(_ context.Context, resource, owner string, now time.Time, lease time.Duration) (continuationClaim, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	keys := make([]string, 0, len(m.continuations))
	for key := range m.continuations {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		ledger := m.continuations[key]
		if ledger.Resource != resource {
			continue
		}
		if claim, ok := ledger.claim(owner, now, lease); ok {
			return claim, true, nil
		}
	}
	return continuationClaim{}, false, nil
}

// ValidateContinuation rejects stale or forged physical deliveries before application code.
func (m *memoryStore) ValidateContinuation(_ context.Context, claim continuationClaim) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ledger := m.continuations[claim.WorkflowID]
	return ledger != nil && ledger.owns(claim), nil
}

// SettleContinuation durably releases only the authoritative eligible head.
func (m *memoryStore) SettleContinuation(_ context.Context, claim continuationClaim, status string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	ledger := m.continuations[claim.WorkflowID]
	if ledger == nil {
		return errors.New("workflow continuation is absent")
	}
	return ledger.settle(claim, status, at)
}

// BeginContinuation claims durable execution independently of publication lease generation.
func (m *memoryStore) BeginContinuation(_ context.Context, publication continuationClaim, owner string, now time.Time) (continuationClaim, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ledger := m.continuations[publication.WorkflowID]
	if ledger == nil {
		return continuationClaim{}, false, nil
	}
	claim, ok := ledger.begin(publication, owner, now)
	return claim, ok, nil
}

// RenewContinuation retains execution authority while the actual application callback remains active.
func (m *memoryStore) RenewContinuation(_ context.Context, claim continuationClaim, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	ledger := m.continuations[claim.WorkflowID]
	if ledger == nil || !ledger.owns(claim) {
		return errors.New("stale continuation execution")
	}
	ledger.ExecutionUntil = now.Add(time.Minute)
	return nil
}
