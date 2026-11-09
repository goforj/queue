package workflow

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/goforj/queue/busruntime"
)

// ErrContinuationProtocol identifies permanent persisted-protocol or configuration failures.
var ErrContinuationProtocol = errors.New("workflow continuation protocol rejected")

// ContinuationDefinition binds one named application job to a durable trigger phase.
type ContinuationDefinition struct {
	Phase string
	Job   StoredJob
}

// ContinuationBinder snapshots a configured builder without extending public builder interfaces.
type ContinuationBinder interface {
	BindContinuations([]ContinuationDefinition) (any, error)
}

// ContinuationReader returns triggered durable invocations rather than untriggered definitions.
type ContinuationReader interface {
	FindContinuations(context.Context, string) ([]busruntime.ContinuationOutcome, error)
}

// continuationLedger owns ordered intents and the publisher's fenced lease.
type continuationLedger struct {
	Version             int
	Resource            string
	WorkflowID          string
	DispatchID          string
	Kind                string
	Definitions         []ContinuationDefinition
	Invocations         []continuationIntent
	Generation          int64
	LeaseUntil          time.Time
	LeaseOwner          string
	Terminal            bool
	Attempts            int
	ExecutionGeneration int64
	ExecutionOwner      string
	ExecutionUntil      time.Time
	NextAttemptAt       time.Time
}

// continuationIntent retains the exact job and immutable snapshot captured at transition commit.
type continuationIntent struct {
	Outcome busruntime.ContinuationOutcome
	Job     StoredJob
	Receipt continuationSettlement
}

// continuationSettlement preserves the exact authority of one idempotently committed settlement.
type continuationSettlement struct {
	Owner      string
	Generation int64
	Execution  bool
	Status     string
}

// ContinuationStore adds atomic intent persistence without changing Store.
type ContinuationStore interface {
	PrepareContinuations(context.Context) error
	ContinuationOutcomes(context.Context, string) ([]busruntime.ContinuationOutcome, error)
	ClaimContinuation(context.Context, string, string, time.Time, time.Duration) (continuationClaim, bool, error)
	ValidateContinuation(context.Context, continuationClaim) (bool, error)
	SettleContinuation(context.Context, continuationClaim, string, time.Time) error
	BeginContinuation(context.Context, continuationClaim, string, time.Time) (continuationClaim, bool, error)
	RenewContinuation(context.Context, continuationClaim, time.Time) error
}

// continuationClaim fences publication and settlement against a later lease generation.
type continuationClaim struct {
	Version      int
	Resource     string
	WorkflowID   string
	InvocationID string
	Owner        string
	Generation   int64
	Execution    bool
	Attempt      int
	Intent       continuationIntent
}

// continuationPlan attaches definitions to the parent's initial atomic store write.
type continuationPlan struct {
	Resource    string
	Definitions []ContinuationDefinition
}

// cloneDefinitions isolates every payload from later builder mutation.
func cloneDefinitions(definitions []ContinuationDefinition) []ContinuationDefinition {
	cloned := make([]ContinuationDefinition, len(definitions))
	for i, definition := range definitions {
		cloned[i] = definition
		if cloned[i].Job.Options.Timeout == 0 {
			cloned[i].Job.Options.Timeout = 30 * time.Second
		}
		cloned[i].Job.Payload = append([]byte(nil), definition.Job.Payload...)
	}
	return cloned
}

// validateDefinitions rejects unsupported phases and unbounded hook catalogs before acceptance.
func validateDefinitions(kind string, definitions []ContinuationDefinition) error {
	if len(definitions) > 32 {
		return errors.New("too many durable workflow continuations")
	}
	for _, definition := range definitions {
		if err := validateContinuationText(definition.Job.Type, definition.Job.Options.Queue); err != nil {
			return err
		}
		if definition.Phase != "success" && definition.Phase != "failure" && definition.Phase != "completion" && (kind != "batch" || definition.Phase != "progress") {
			return errors.New("unsupported workflow continuation phase")
		}
		if definition.Job.Type == "" || definition.Job.Options.Retry < 0 || definition.Job.Options.Timeout < 0 || definition.Job.Options.UniqueFor != 0 || definition.Job.Options.Timeout > time.Hour || definition.Job.Options.Retry > 100 || definition.Job.Options.Delay < 0 || definition.Job.Options.Delay > 7*24*time.Hour || definition.Job.Options.Backoff < 0 || definition.Job.Options.Backoff > time.Hour {
			return errors.New("invalid workflow continuation job")
		}
	}
	encoded, err := json.Marshal(definitions)
	if err != nil || len(encoded) > 128<<10 {
		return errors.New("workflow continuation definitions exceed byte budget")
	}
	return nil
}

// newLedger captures the configured hook catalog alongside the parent's accepted identity.
func newLedger(kind, id, dispatchID string, plan *continuationPlan) *continuationLedger {
	if plan == nil || len(plan.Definitions) == 0 {
		return nil
	}
	return &continuationLedger{Version: 1, Resource: plan.Resource, WorkflowID: id, DispatchID: dispatchID, Kind: kind, Definitions: cloneDefinitions(plan.Definitions)}
}

// trigger appends immutable intents in phase and declaration order under parent synchronization.
func (l *continuationLedger) trigger(summary busruntime.ContinuationSummary, firstFailure, terminal bool, at time.Time) {
	terminal = terminal && !l.Terminal
	phases := make([]string, 0, 4)
	if l.Kind == "batch" && summary.MemberID != "" {
		phases = append(phases, "progress")
	}
	if firstFailure {
		phases = append(phases, "failure")
	}
	if terminal && !summary.Cancelled && summary.Failed == 0 && l.Kind == "chain" {
		phases = append(phases, "success")
	}
	if terminal && !summary.Cancelled && l.Kind == "batch" {
		phases = append(phases, "success")
	}
	if terminal {
		phases = append(phases, "completion")
		l.Terminal = true
	}
	for _, phase := range phases {
		for index, definition := range l.Definitions {
			if definition.Phase != phase {
				continue
			}
			sequence := len(l.Invocations) + 1
			digest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s\x00%d\x00%d", l.Resource, l.WorkflowID, l.DispatchID, phase, summary.MemberID, sequence, index)))
			invocation := busruntime.Continuation{ID: "cnt_" + hex.EncodeToString(digest[:]), WorkflowID: l.WorkflowID, DispatchID: l.DispatchID, Phase: phase, Sequence: sequence, Declaration: index, Trigger: summary}
			l.Invocations = append(l.Invocations, continuationIntent{Job: definition.Job, Outcome: busruntime.ContinuationOutcome{Invocation: invocation, Status: "pending", CreatedAt: at}})
		}
	}
}

// head returns only the first unsettled invocation so later work cannot overtake it.
func (l *continuationLedger) head() *continuationIntent {
	for i := range l.Invocations {
		intent := &l.Invocations[i]
		if intent.Outcome.Status == "pending" || intent.Outcome.Status == "scheduled" {
			return intent
		}
	}
	return nil
}

// cloneLedger preserves caller ownership when a lease claim crosses synchronization boundaries.
func cloneLedger(ledger *continuationLedger) *continuationLedger {
	copy := *ledger
	copy.Definitions = make([]ContinuationDefinition, len(ledger.Definitions))
	for i, definition := range ledger.Definitions {
		copy.Definitions[i] = definition
		copy.Definitions[i].Job.Payload = append([]byte(nil), definition.Job.Payload...)
	}
	copy.Invocations = append([]continuationIntent(nil), ledger.Invocations...)
	for i := range copy.Invocations {
		copy.Invocations[i].Job.Payload = append([]byte(nil), ledger.Invocations[i].Job.Payload...)
	}
	return &copy
}

// claim leases the next eligible head while retaining its identity across ambiguous publication.
func (l *continuationLedger) claim(owner string, now time.Time, lease time.Duration) (continuationClaim, bool) {
	head := l.head()
	if head == nil || l.availableAt().After(now) {
		return continuationClaim{}, false
	}
	l.Generation++
	l.LeaseOwner = owner
	l.LeaseUntil = now.Add(lease)
	head.Outcome.Status = "scheduled"
	if head.Outcome.ScheduledAt.IsZero() {
		head.Outcome.ScheduledAt = now
	}
	copy := cloneLedger(l).head()
	return continuationClaim{Version: 1, Resource: l.Resource, WorkflowID: l.WorkflowID, InvocationID: head.Outcome.Invocation.ID, Owner: owner, Generation: l.Generation, Intent: *copy}, true
}

// availableAt keeps delay, publication and execution leases independent of physical queue waiting.
func (l *continuationLedger) availableAt() time.Time {
	head := l.head()
	if head == nil {
		return time.Time{}
	}
	available := head.Outcome.CreatedAt.Add(head.Job.Options.Delay)
	for _, candidate := range []time.Time{l.LeaseUntil, l.ExecutionUntil, l.NextAttemptAt} {
		if candidate.After(available) {
			available = candidate
		}
	}
	return available
}

// associated checks immutable invocation data independently of a publisher's expired lease.
func (l *continuationLedger) associated(claim continuationClaim) bool {
	head := l.head()
	return head != nil && claim.Version == 1 && l.Resource == claim.Resource && head.Outcome.Invocation.ID == claim.InvocationID && head.Outcome.Invocation == claim.Intent.Outcome.Invocation && head.Job.Type == claim.Intent.Job.Type && head.Job.Options == claim.Intent.Job.Options && bytes.Equal(head.Job.Payload, claim.Intent.Job.Payload)
}

// begin allocates one durable logical attempt even when an old publication arrives late.
func (l *continuationLedger) begin(publication continuationClaim, owner string, now time.Time) (continuationClaim, bool) {
	if l.associated(publication) && l.ExecutionOwner == owner && owner != "" && l.Attempts > 0 {
		claim := publication
		claim.Execution = true
		claim.Generation = l.ExecutionGeneration
		claim.Owner = owner
		claim.Attempt = l.Attempts - 1
		claim.Intent = *cloneLedger(l).head()
		return claim, true
	}
	if !l.associated(publication) || l.ExecutionUntil.After(now) || l.NextAttemptAt.After(now) {
		return continuationClaim{}, false
	}
	head := l.head()
	if head.Outcome.CreatedAt.Add(head.Job.Options.Delay).After(now) {
		return continuationClaim{}, false
	}
	if l.Attempts > head.Job.Options.Retry {
		head.Outcome.Status = "exhausted"
		head.Outcome.FailureCategory = "execution_interrupted"
		head.Outcome.SettledAt = now
		l.clearExecution()
		return continuationClaim{}, false
	}
	l.Attempts++
	l.ExecutionGeneration++
	l.ExecutionOwner = owner
	l.ExecutionUntil = now.Add(time.Minute)
	claim := publication
	claim.Execution = true
	claim.Generation = l.ExecutionGeneration
	claim.Owner = owner
	claim.Attempt = l.Attempts - 1
	claim.Intent = *cloneLedger(l).head()
	return claim, true
}

// clearExecution releases the settled head without reusing its durable attempt budget.
func (l *continuationLedger) clearExecution() {
	l.Attempts = 0
	l.ExecutionOwner = ""
	l.ExecutionUntil = time.Time{}
	l.NextAttemptAt = time.Time{}
	l.LeaseOwner = ""
	l.LeaseUntil = time.Time{}
}

// owns rejects stale publishers and mismatched association before application execution.
func (l *continuationLedger) owns(claim continuationClaim) bool {
	if !l.associated(claim) {
		return false
	}
	if claim.Execution {
		return l.ExecutionGeneration == claim.Generation && l.ExecutionOwner == claim.Owner
	}
	return l.Generation == claim.Generation && l.LeaseOwner == claim.Owner
}

// settle releases only the authoritative head and preserves the next intent's eligibility.
func (l *continuationLedger) settle(claim continuationClaim, status string, at time.Time) error {
	if claim.Version == 1 && claim.Resource == l.Resource && claim.WorkflowID == l.WorkflowID {
		for _, intent := range l.Invocations {
			receipt := intent.Receipt
			if intent.Outcome.Invocation.ID == claim.InvocationID && receipt.Owner == claim.Owner && receipt.Generation == claim.Generation && receipt.Execution == claim.Execution && receipt.Status == status && intent.Outcome.Invocation == claim.Intent.Outcome.Invocation && intent.Job.Type == claim.Intent.Job.Type && intent.Job.Options == claim.Intent.Job.Options && bytes.Equal(intent.Job.Payload, claim.Intent.Job.Payload) {
				return nil
			}
		}
	}

	if !l.owns(claim) {
		return errors.New("stale workflow continuation claim")
	}
	if status == "redeliver" && !claim.Execution {
		return fmt.Errorf("%w: redelivery requires execution authority", ErrContinuationProtocol)
	}
	head := l.head()
	head.Receipt = continuationSettlement{Owner: claim.Owner, Generation: claim.Generation, Execution: claim.Execution, Status: status}
	if status == "retry" || status == "redeliver" {
		if status == "redeliver" {
			l.Attempts--
		}
		l.ExecutionOwner = ""
		l.ExecutionUntil = time.Time{}
		l.LeaseOwner = ""
		l.LeaseUntil = time.Time{}
		l.NextAttemptAt = at.Add(head.Job.Options.Backoff)
		return nil
	}
	head.Outcome.Status = status
	head.Outcome.SettledAt = at
	if status != "succeeded" {
		head.Outcome.FailureCategory = "execution_failed"
	}
	l.clearExecution()
	return nil
}

// validateContinuationFootprint reserves the complete worst-case ledger before accepting business work.
func validateContinuationFootprint(kind string, members int, definitions []ContinuationDefinition) error {
	if len(definitions) == 0 {
		return nil
	}
	if members < 1 || members > 256 {
		return errors.New("named workflows require between 1 and 256 members")
	}
	bytes, count := 0, 0
	for _, definition := range definitions {
		copies := 1
		if definition.Phase == "progress" {
			copies = members
		}
		raw, err := json.Marshal(definition)
		if err != nil {
			return err
		}
		bytes += copies * (len(raw) + 2048)
		count += copies
	}
	if bytes > 4<<20 || count > 4096 {
		return errors.New("workflow continuation ledger exceeds reserved capacity")
	}
	return nil
}

// ContinuationClaim exposes the internal recovery capability to root-owned adapters and fakes.
type ContinuationClaim = continuationClaim

// Continuations returns an isolated named hook catalog from accepted chain input.
func (r ChainRecord) Continuations() []ContinuationDefinition {
	if r.continuation == nil {
		return nil
	}
	return cloneDefinitions(r.continuation.Definitions)
}

// Continuations returns an isolated named hook catalog from accepted batch input.
func (r BatchRecord) Continuations() []ContinuationDefinition {
	if r.continuation == nil {
		return nil
	}
	return cloneDefinitions(r.continuation.Definitions)
}

// validateContinuationText rejects identifiers that JSON would normalize and invalidate during durable delivery association.
func validateContinuationText(values ...string) error {
	for _, value := range values {
		if !utf8.ValidString(value) {
			return errors.New("named workflow identifiers must be valid UTF-8")
		}
	}
	return nil
}

// chainContinuationSummary counts a settled failed member without advancing the successful-node cursor.
func chainContinuationSummary(state ChainState, member string, failed bool) busruntime.ContinuationSummary {
	processed := state.NextIndex
	if failed && member != "" {
		processed++
	}
	return busruntime.ContinuationSummary{Kind: "chain", MemberID: member, Total: len(state.Nodes), Processed: processed, Pending: len(state.Nodes) - processed, NextIndex: state.NextIndex, Failed: continuationFailureCount(failed), Completed: state.Completed, Cancelled: failed}
}

// continuationFailureCount converts a single terminal failure into the safe summary count.
func continuationFailureCount(value bool) int {
	if value {
		return 1
	}
	return 0
}
