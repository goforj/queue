package workflow

import "encoding/json"

const (
	// ProtocolSchemaVersion identifies the workflow delivery envelope understood by this version of the library.
	ProtocolSchemaVersion = 1
	// DirectDeliveryType identifies an ordinary job carried through the workflow protocol.
	DirectDeliveryType = "bus:job"
	// ChainNodeDeliveryType identifies one sequential workflow node delivery.
	ChainNodeDeliveryType = "bus:chain:node"
	// BatchJobDeliveryType identifies one aggregate workflow member delivery.
	BatchJobDeliveryType = "bus:batch:job"
	// CallbackDeliveryType identifies one ephemeral workflow callback delivery.
	CallbackDeliveryType = "bus:callback"
)

// DeliveryMetadata contains the logical application identity and workflow correlation carried by a physical delivery.
type DeliveryMetadata struct {
	// JobType is the logical application type when a supported envelope provides one, otherwise the physical type.
	JobType string
	// Payload is the logical application payload when a supported envelope provides one, otherwise the physical payload.
	Payload []byte
	// DispatchID correlates deliveries created by the same application dispatch.
	DispatchID string
	// JobID identifies the logical workflow job represented by this delivery.
	JobID string
	// ChainID identifies the owning chain when the delivery belongs to one.
	ChainID string
	// BatchID identifies the owning batch when the delivery belongs to one.
	BatchID string
}

// ResolveDeliveryMetadata decodes the owned protocol while preserving physical identity for unsupported or malformed input.
func ResolveDeliveryMetadata(deliveryType string, payload []byte) DeliveryMetadata {
	metadata := DeliveryMetadata{JobType: deliveryType, Payload: payload}
	if deliveryType == "" || len(payload) == 0 || !IsDeliveryType(deliveryType) {
		return metadata
	}

	var envelope struct {
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
	if err := json.Unmarshal(payload, &envelope); err != nil || envelope.SchemaVersion != ProtocolSchemaVersion {
		return metadata
	}

	metadata.DispatchID = envelope.DispatchID
	metadata.JobID = envelope.JobID
	metadata.ChainID = envelope.ChainID
	metadata.BatchID = envelope.BatchID
	if envelope.Job.Type != "" {
		metadata.JobType = envelope.Job.Type
		metadata.Payload = envelope.Job.Payload
	}
	return metadata
}

// IsDeliveryType reports whether a physical job type belongs to the workflow protocol.
func IsDeliveryType(deliveryType string) bool {
	switch deliveryType {
	case DirectDeliveryType, ChainNodeDeliveryType, BatchJobDeliveryType, CallbackDeliveryType:
		return true
	default:
		return false
	}
}
