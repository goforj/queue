package queue

import "encoding/json"

const logicalJobEnvelopeSchemaVersion = 1

type logicalJob struct {
	jobType    string
	payload    []byte
	dispatchID string
	jobID      string
	chainID    string
	batchID    string
}

type logicalJobEnvelope struct {
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

// resolveLogicalJob decodes only the owned workflow schema so identity and telemetry cannot drift onto separate interpretations.
func resolveLogicalJob(rawType string, payload []byte) logicalJob {
	resolved := logicalJob{jobType: rawType, payload: payload}
	if rawType == "" || len(payload) == 0 || !isLogicalWorkflowDeliveryType(rawType) {
		return resolved
	}

	var envelope logicalJobEnvelope
	if err := json.Unmarshal(payload, &envelope); err != nil || envelope.SchemaVersion != logicalJobEnvelopeSchemaVersion {
		return resolved
	}
	resolved.dispatchID = envelope.DispatchID
	resolved.jobID = envelope.JobID
	resolved.chainID = envelope.ChainID
	resolved.batchID = envelope.BatchID
	if envelope.Job.Type != "" {
		resolved.jobType = envelope.Job.Type
		resolved.payload = envelope.Job.Payload
	}
	return resolved
}

// isLogicalWorkflowDeliveryType restricts envelope decoding to the private delivery namespace owned by this runtime.
func isLogicalWorkflowDeliveryType(jobType string) bool {
	switch jobType {
	case "bus:job", "bus:chain:node", "bus:batch:job", "bus:callback":
		return true
	default:
		return false
	}
}
