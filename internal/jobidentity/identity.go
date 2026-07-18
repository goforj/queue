// Package jobidentity centralizes logical payload normalization and telemetry correlation shared by queue and workflow layers.
package jobidentity

import (
	"bytes"
	"crypto/sha1"
	"fmt"
)

// CanonicalPayload normalizes legacy representations of an absent workflow payload.
func CanonicalPayload(payload []byte) []byte {
	if len(payload) == 0 || bytes.Equal(payload, []byte("null")) {
		return nil
	}
	return payload
}

// ObservedKey returns the stable logical type-and-payload correlation used by telemetry.
func ObservedKey(jobType string, payload []byte) string {
	payload = CanonicalPayload(payload)
	hash := sha1.Sum(append([]byte(jobType+":"), payload...))
	return fmt.Sprintf("%x", hash[:])
}
