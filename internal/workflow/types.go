package workflow

import (
	"context"
	"encoding/json"
	"time"
)

// Handler processes one logical workflow delivery.
type Handler func(ctx context.Context, j Context) error

// Job describes one logical application job and its workflow delivery policy.
type Job struct {
	Type    string
	Payload any
	Options JobOptions
}

// NewJob creates a typed workflow job payload with optional fluent policy.
func NewJob(jobType string, payload any) Job {
	return Job{Type: jobType, Payload: payload}
}

// OnQueue sets the target queue for this job.
func (j Job) OnQueue(name string) Job {
	j.Options.Queue = name
	return j
}

// Delay defers job execution.
func (j Job) Delay(delay time.Duration) Job {
	j.Options.Delay = delay
	return j
}

// Timeout sets execution timeout for this job.
func (j Job) Timeout(timeout time.Duration) Job {
	j.Options.Timeout = timeout
	return j
}

// Retry sets max retry attempts for this job.
func (j Job) Retry(max int) Job {
	j.Options.Retry = max
	return j
}

// Backoff sets retry backoff for this job.
func (j Job) Backoff(backoff time.Duration) Job {
	j.Options.Backoff = backoff
	return j
}

// UniqueFor sets dedupe TTL for this job.
func (j Job) UniqueFor(ttl time.Duration) Job {
	j.Options.UniqueFor = ttl
	return j
}

// JobOptions carries queue delivery policy through the versioned workflow envelope.
type JobOptions struct {
	Queue     string
	Delay     time.Duration
	Timeout   time.Duration
	Retry     int
	Backoff   time.Duration
	UniqueFor time.Duration
}

// DispatchResult identifies an accepted logical dispatch.
type DispatchResult struct {
	DispatchID string
}

// Context carries workflow correlation and isolated payload data into handlers and middleware.
type Context struct {
	SchemaVersion int
	DispatchID    string
	JobID         string
	ChainID       string
	BatchID       string
	Attempt       int
	JobType       string
	payload       []byte
}

// NewContext reconstructs an engine message from correlation metadata and raw payload bytes.
// The payload is copied because compatibility adapters may reuse their input buffers.
func NewContext(schemaVersion int, dispatchID, jobID, chainID, batchID string, attempt int, jobType string, payload []byte) Context {
	var isolatedPayload []byte
	if payload != nil {
		isolatedPayload = make([]byte, len(payload))
		copy(isolatedPayload, payload)
	}
	return Context{
		SchemaVersion: schemaVersion,
		DispatchID:    dispatchID,
		JobID:         jobID,
		ChainID:       chainID,
		BatchID:       batchID,
		Attempt:       attempt,
		JobType:       jobType,
		payload:       isolatedPayload,
	}
}

// PayloadBytes returns a copy of raw job payload bytes.
func (c Context) PayloadBytes() []byte {
	if c.payload == nil {
		return nil
	}
	payload := make([]byte, len(c.payload))
	copy(payload, c.payload)
	return payload
}

// Bind unmarshals the job payload into dst.
func (c Context) Bind(dst any) error {
	return json.Unmarshal(c.payload, dst)
}
