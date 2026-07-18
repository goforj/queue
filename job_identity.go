package queue

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"

	"github.com/goforj/queue/internal/jobidentity"
)

const driverUniqueKeyVersion = "goforj:queue:unique:v1"

// DriverUniqueKey returns a versioned queue-scoped identity for driver deduplication.
// Correlation IDs and delivery policy are excluded when a workflow envelope carries a logical job.
// @group Driver Integration
func DriverUniqueKey(job Job, queueName string) string {
	jobType := job.Type
	payload := job.PayloadBytes()
	if job.options.logicalSet {
		jobType = job.options.logicalType
		payload = job.options.logicalPayload
	} else {
		logical := resolveLogicalJob(job.Type, payload)
		jobType = logical.jobType
		payload = logical.payload
	}
	payload = canonicalIdentityPayload(payload)
	identity := []byte(driverUniqueKeyVersion)
	identity = appendUniqueIdentityPart(identity, []byte(queueName))
	identity = appendUniqueIdentityPart(identity, []byte(jobType))
	identity = appendUniqueIdentityPart(identity, payload)
	digest := sha256.Sum256(identity)
	return "v1:" + hex.EncodeToString(digest[:])
}

// canonicalIdentityPayload keeps payload absence stable while the legacy workflow facade serializes nil as JSON null.
func canonicalIdentityPayload(payload []byte) []byte {
	return jobidentity.CanonicalPayload(payload)
}

// appendUniqueIdentityPart length-frames arbitrary bytes so delimiters inside names or payloads cannot collide.
func appendUniqueIdentityPart(dst, value []byte) []byte {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	dst = append(dst, size[:]...)
	return append(dst, value...)
}
