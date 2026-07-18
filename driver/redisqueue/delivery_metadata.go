package redisqueue

import (
	"encoding/json"

	"github.com/goforj/queue"
)

const redisDriverJobMetadataHeader = "goforj-queue-driver-job-metadata"

// redisJobWithDriverMetadata restores supported correlation without making malformed or future metadata a delivery failure.
func redisJobWithDriverMetadata(job queue.Job, headers map[string]string) queue.Job {
	raw, ok := headers[redisDriverJobMetadataHeader]
	if !ok {
		return job
	}
	var metadata queue.DriverJobMetadata
	if err := json.Unmarshal([]byte(raw), &metadata); err != nil {
		return job
	}
	return queue.DriverWithMetadata(job, metadata)
}
