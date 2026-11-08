package temporalqueue

import (
	"errors"
	"reflect"

	"github.com/goforj/queue/busruntime"
)

// validateJournal independently reduces business counters and verifies every immutable continuation association.
func validateJournal(def workflowDefinition, deltas []journalDelta, originalRun string, activities map[string]int64) error {
	seen := make(map[int]bool)
	processed, failed, invocations := 0, 0, 0
	terminal, cancelled := false, false
	var ledger []continuationInvocation
	head := 0
	active := ""
	for _, delta := range deltas {
		if delta.Status == "workflow-interrupted" {
			continue
		}
		if delta.Index >= 0 {
			if seen[delta.Index] || delta.InvocationID != "" || delta.Status != "" {
				return errors.New("duplicate or mixed business journal outcome")
			}
			seen[delta.Index] = true
			if def.Kind == "chain" && (delta.Index != len(seen)-1 || terminal) {
				return errors.New("chain journal node order rejected")
			}
			if delta.Skipped {
				if delta.Failed || len(delta.Intents) > 0 || delta.Terminal {
					return errors.New("canceled member journal outcome rejected")
				}
				continue
			}
			processed++
			if delta.Failed {
				failed++
			}
			becameTerminal := !terminal && ((def.Kind == "chain" && (delta.Failed || processed == len(def.Jobs))) || (def.Kind == "batch" && ((delta.Failed && !def.AllowFailures) || processed == len(def.Jobs))))
			if becameTerminal {
				terminal = true
				if delta.Failed && !def.AllowFailures {
					cancelled = true
				}
			}
			if delta.Terminal != becameTerminal || delta.Cancelled != cancelled {
				return errors.New("journal aggregate transition rejected")
			}
			summary := busruntime.ContinuationSummary{Kind: def.Kind, MemberID: def.Jobs[delta.Index].Metadata.JobID, Total: len(def.Jobs), Processed: processed, Pending: len(def.Jobs) - processed, Failed: failed, Completed: terminal && (def.Kind == "batch" || failed == 0), Cancelled: cancelled}
			if def.Kind == "chain" {
				summary.NextIndex = processed - failed
			}
			var phases []string
			if def.Kind == "batch" {
				phases = append(phases, "progress")
			}
			if delta.Failed && failed == 1 {
				phases = append(phases, "failure")
			}
			if becameTerminal && !cancelled && (def.Kind == "batch" || failed == 0) {
				phases = append(phases, "success")
			}
			if becameTerminal {
				phases = append(phases, "completion")
			}
			position := 0
			for _, phase := range phases {
				for declaration, definition := range def.Hooks {
					if definition.Phase != phase {
						continue
					}
					if position >= len(delta.Intents) {
						return errors.New("journal continuation intent is missing")
					}
					actual := delta.Intents[position]
					position++
					invocations++
					expected := busruntime.Continuation{ID: continuationID(def.Resource, def.ID, originalRun, def.DispatchID, phase, summary.MemberID, invocations, declaration), WorkflowID: def.ID, DispatchID: def.DispatchID, Phase: phase, Sequence: invocations, Declaration: declaration, Trigger: summary}
					job := definition.Job
					job.Metadata.JobID = expected.ID
					job.Metadata.Continuation = &expected
					if actual.Outcome.Invocation != expected || actual.Outcome.Status != "pending" || actual.Outcome.CreatedAt != delta.At || !reflect.DeepEqual(actual.Job, job) {
						return errors.New("immutable continuation journal snapshot rejected")
					}
					ledger = append(ledger, actual)
				}
			}
			if position != len(delta.Intents) {
				return errors.New("unexpected continuation journal intent")
			}
			continue
		}
		if delta.Index != -1 || delta.InvocationID == "" || len(delta.Intents) > 0 || delta.Failed || delta.Terminal || delta.Skipped {
			return errors.New("invalid continuation journal delta")
		}
		if head >= len(ledger) || ledger[head].Outcome.Invocation.ID != delta.InvocationID {
			return errors.New("continuation journal head order rejected")
		}
		outcome := &ledger[head].Outcome
		switch delta.Status {
		case "scheduled":
			if active != "" || outcome.Status != "pending" || delta.taskCommit == 0 || activities[delta.InvocationID] != delta.taskCommit {
				return errors.New("continuation scheduling commit rejected")
			}
			active = delta.InvocationID
			outcome.Status = "scheduled"
		case "succeeded", "exhausted":
			if active != delta.InvocationID || outcome.Status != "scheduled" {
				return errors.New("continuation settled before scheduling")
			}
			outcome.Status = delta.Status
			active = ""
			head++
		case "interrupted":
			if outcome.Status != "pending" && outcome.Status != "scheduled" {
				return errors.New("continuation interruption order rejected")
			}
			outcome.Status = delta.Status
			active = ""
			head++
		default:
			return errors.New("unsupported continuation journal delta")
		}
	}
	return nil
}
