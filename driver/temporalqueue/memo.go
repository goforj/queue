package temporalqueue

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/goforj/queue/busruntime"
)

// journalValue prevents the SDK's default-converter fallback from serializing application data.
// Only guardedConverter unwraps this value for the authoritative bounded converter.
type journalValue struct{ Delta journalDelta }

// MarshalJSON makes fallback conversion fail closed after any authoritative encoding failure.
func (journalValue) MarshalJSON() ([]byte, error) {
	return nil, errors.New("Temporal journal requires its authoritative converter")
}

// largestJournalDelta reserves every hook in one transition with maximum-width safe metadata.
func largestJournalDelta(def workflowDefinition) journalDelta {
	at := time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC)
	memberID := ""
	longest := 0
	for _, member := range def.Jobs {
		encoded, _ := json.Marshal(member.Metadata.JobID)
		if len(encoded) > longest {
			memberID = member.Metadata.JobID
			longest = len(encoded)
		}
	}
	delta := journalDelta{Version: 1, Sequence: 999999, Index: 255, At: at}
	for i, hook := range def.Hooks {
		invocation := busruntime.Continuation{ID: "cnt_" + strings.Repeat("f", 64), WorkflowID: def.ID, DispatchID: def.DispatchID, Phase: hook.Phase, Sequence: 999999, Declaration: i, Trigger: busruntime.ContinuationSummary{Kind: def.Kind, MemberID: memberID, Total: 256, Pending: 256, Processed: 256, Failed: 256, NextIndex: 256}}
		job := hook.Job
		job.Metadata.JobID = invocation.ID
		job.Metadata.Continuation = &invocation
		delta.Intents = append(delta.Intents, continuationInvocation{Job: job, Outcome: busruntime.ContinuationOutcome{Invocation: invocation, Status: "pending", CreatedAt: at}})
	}
	return delta
}
