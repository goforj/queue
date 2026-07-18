package queue

import (
	"crypto/sha1"
	"encoding/json"
	"fmt"
)

const observedBusEnvelopeSchemaVersion = 1

type observedBusEnvelope struct {
	SchemaVersion int    `json:"schema_version"`
	DispatchID    string `json:"dispatch_id"`
	JobID         string `json:"job_id"`
	ChainID       string `json:"chain_id"`
	BatchID       string `json:"batch_id"`
	Job           struct {
		Type    string `json:"type"`
		Payload []byte `json:"payload"`
	} `json:"job"`
}

// ObservedJobMetadata contains observability correlation extracted from a legacy internal delivery envelope.
// JobKey groups telemetry and is not the identity drivers use to enforce UniqueFor.
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
	metadata := ObservedJobMetadata{
		JobType: rawType,
		JobKey:  observedJobKey(rawType, payload),
	}
	if rawType == "" || len(payload) == 0 || !isObservedBusJobType(rawType) {
		return metadata
	}

	var envelope observedBusEnvelope
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return metadata
	}
	if envelope.SchemaVersion != observedBusEnvelopeSchemaVersion {
		return metadata
	}
	metadata.DispatchID = envelope.DispatchID
	metadata.JobID = envelope.JobID
	metadata.ChainID = envelope.ChainID
	metadata.BatchID = envelope.BatchID
	if envelope.Job.Type != "" {
		metadata.JobType = envelope.Job.Type
		metadata.JobKey = observedJobKey(envelope.Job.Type, envelope.Job.Payload)
	}
	return metadata
}

// isObservedBusJobType restricts decoding to the internal namespace owned by the current workflow runtime.
func isObservedBusJobType(jobType string) bool {
	switch jobType {
	case "bus:job", "bus:chain:node", "bus:batch:job", "bus:callback":
		return true
	default:
		return false
	}
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
	hash := sha1.Sum(append([]byte(jobType+":"), payload...))
	return fmt.Sprintf("%x", hash[:])
}
