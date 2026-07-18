package queue

import "github.com/goforj/queue/internal/jobidentity"

// ObservedJobMetadata contains observability correlation extracted from a legacy internal delivery envelope.
// JobKey groups the same logical type and payload used by UniqueFor without becoming its persisted, queue-scoped key.
// @group Driver Integration
type ObservedJobMetadata struct {
	JobType    string
	JobKey     string
	DispatchID string
	JobID      string
	ChainID    string
	BatchID    string
}

// ResolveObservedJobMetadata bridges legacy versioned workflow envelopes into the event model.
// Malformed, unknown-version, and non-internal payloads remain observable as their raw physical job.
// @group Driver Integration
func ResolveObservedJobMetadata(rawType string, payload []byte) ObservedJobMetadata {
	metadata, _ := resolveObservedJobMetadata(rawType, payload)
	return metadata
}

// resolveObservedJobMetadata returns both correlation fields and the exact logical payload used by delivery policy.
func resolveObservedJobMetadata(rawType string, payload []byte) (ObservedJobMetadata, []byte) {
	logical := resolveLogicalJob(rawType, payload)
	metadata := ObservedJobMetadata{
		JobType:    logical.jobType,
		JobKey:     observedJobKey(logical.jobType, logical.payload),
		DispatchID: logical.dispatchID,
		JobID:      logical.jobID,
		ChainID:    logical.chainID,
		BatchID:    logical.batchID,
	}
	return metadata, logical.payload
}

// ResolveObservedJobType returns the effective application job type that should
// be emitted to observers. External workers may process internal bus wrapper
// jobs (for example, "bus:job") whose payload embeds the real application job
// type. When possible, this helper unwraps that payload so dashboards and
// metrics reflect the user-facing job type instead of the transport wrapper.
func ResolveObservedJobType(rawType string, payload []byte) string {
	return ResolveObservedJobMetadata(rawType, payload).JobType
}

// observedJobKey keeps telemetry correlation stable when volatile workflow IDs surround the application payload.
func observedJobKey(jobType string, payload []byte) string {
	return jobidentity.ObservedKey(jobType, payload)
}
