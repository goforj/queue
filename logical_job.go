package queue

import "github.com/goforj/queue/internal/workflow"

// logicalJob is the root-facing view of identity and correlation resolved from one physical delivery.
type logicalJob struct {
	jobType    string
	payload    []byte
	dispatchID string
	jobID      string
	chainID    string
	batchID    string
}

// resolveLogicalJob decodes only the owned workflow schema so identity and telemetry cannot drift onto separate interpretations.
func resolveLogicalJob(rawType string, payload []byte) logicalJob {
	metadata := workflow.ResolveDeliveryMetadata(rawType, payload)
	return logicalJob{
		jobType:    metadata.JobType,
		payload:    metadata.Payload,
		dispatchID: metadata.DispatchID,
		jobID:      metadata.JobID,
		chainID:    metadata.ChainID,
		batchID:    metadata.BatchID,
	}
}
