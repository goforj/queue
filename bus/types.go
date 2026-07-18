package bus

import (
	"context"
	"time"

	"github.com/goforj/queue"
)

// Handler processes one legacy workflow message.
//
// Deprecated: register handlers on queue.Queue.
type Handler func(ctx context.Context, message Context) error

// Job is the legacy workflow dispatch DTO.
//
// Deprecated: use queue.Job. This type remains distinct because its public
// fields and deferred JSON encoding are part of the compatibility contract.
type Job struct {
	Type    string
	Payload any
	Options JobOptions
}

// NewJob creates a typed legacy workflow job with optional fluent options.
//
// Deprecated: use queue.NewJob and its Payload method.
// @group Constructors
func NewJob(jobType string, payload any) Job {
	return Job{Type: jobType, Payload: payload}
}

// OnQueue sets the target queue for this job.
//
// Deprecated: use queue.Job.OnQueue.
// @group Job
func (j Job) OnQueue(name string) Job {
	j.Options.Queue = name
	return j
}

// Delay defers job execution.
//
// Deprecated: use queue.Job.Delay.
// @group Job
func (j Job) Delay(delay time.Duration) Job {
	j.Options.Delay = delay
	return j
}

// Timeout sets the execution timeout for this job.
//
// Deprecated: use queue.Job.Timeout.
// @group Job
func (j Job) Timeout(timeout time.Duration) Job {
	j.Options.Timeout = timeout
	return j
}

// Retry sets the maximum retry count for this job.
//
// Deprecated: use queue.Job.Retry.
// @group Job
func (j Job) Retry(max int) Job {
	j.Options.Retry = max
	return j
}

// Backoff sets retry backoff for this job.
//
// Deprecated: use queue.Job.Backoff.
// @group Job
func (j Job) Backoff(backoff time.Duration) Job {
	j.Options.Backoff = backoff
	return j
}

// UniqueFor sets the deduplication TTL for this job.
//
// Deprecated: use queue.Job.UniqueFor.
// @group Job
func (j Job) UniqueFor(ttl time.Duration) Job {
	j.Options.UniqueFor = ttl
	return j
}

// JobOptions contains the legacy workflow delivery options.
//
// Deprecated: configure a queue.Job through its fluent methods.
type JobOptions = queue.StoredJobOptions

// DispatchResult describes an accepted workflow dispatch.
//
// Deprecated: use queue.DispatchResult.
type DispatchResult = queue.DispatchResult

// Context contains a delivered workflow message and its correlation metadata.
//
// Deprecated: use queue.Message.
type Context = queue.Message
