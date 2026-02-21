package bus

import (
	"context"
	"encoding/json"
	"time"
)

type Handler func(ctx context.Context, j Context) error

type Job struct {
	Type    string
	Payload any
	Options JobOptions
}

// NewJob creates a typed bus job payload with optional fluent options.
// @group Constructors
//
// Example: new bus job
//
//	type PollPayload struct {
//		URL string `json:"url"`
//	}
//	job := bus.NewJob("monitor:poll", PollPayload{
//		URL: "https://goforj.dev/health",
//	}).
//		OnQueue("monitor-critical").
//		Delay(2 * time.Second).
//		Timeout(15 * time.Second).
//		Retry(3).
//		Backoff(500 * time.Millisecond).
//		UniqueFor(30 * time.Second)
//	_ = job
func NewJob(jobType string, payload any) Job {
	return Job{Type: jobType, Payload: payload}
}

// OnQueue sets the target queue for this job.
// @group Job
//
// Example: set queue
//
//	job := bus.NewJob("emails:send", nil).OnQueue("critical")
//	_ = job
func (j Job) OnQueue(name string) Job {
	j.Options.Queue = name
	return j
}

// Delay defers job execution.
// @group Job
//
// Example: set delay
//
//	job := bus.NewJob("emails:send", nil).Delay(2 * time.Second)
//	_ = job
func (j Job) Delay(delay time.Duration) Job {
	j.Options.Delay = delay
	return j
}

// Timeout sets execution timeout for this job.
// @group Job
//
// Example: set timeout
//
//	job := bus.NewJob("emails:send", nil).Timeout(15 * time.Second)
//	_ = job
func (j Job) Timeout(timeout time.Duration) Job {
	j.Options.Timeout = timeout
	return j
}

// Retry sets max retry attempts for this job.
// @group Job
//
// Example: set retry count
//
//	job := bus.NewJob("emails:send", nil).Retry(5)
//	_ = job
func (j Job) Retry(max int) Job {
	j.Options.Retry = max
	return j
}

// Backoff sets retry backoff for this job.
// @group Job
//
// Example: set retry backoff
//
//	job := bus.NewJob("emails:send", nil).Backoff(500 * time.Millisecond)
//	_ = job
func (j Job) Backoff(backoff time.Duration) Job {
	j.Options.Backoff = backoff
	return j
}

// UniqueFor sets dedupe TTL for this job.
// @group Job
//
// Example: set unique TTL
//
//	job := bus.NewJob("emails:send", nil).UniqueFor(30 * time.Second)
//	_ = job
func (j Job) UniqueFor(ttl time.Duration) Job {
	j.Options.UniqueFor = ttl
	return j
}

type JobOptions struct {
	Queue     string
	Delay     time.Duration
	Timeout   time.Duration
	Retry     int
	Backoff   time.Duration
	UniqueFor time.Duration
}

type DispatchResult struct {
	DispatchID string
}

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

// PayloadBytes returns a copy of raw job payload bytes.
// @group Job
//
// Example: read raw payload bytes
//
//	raw := jc.PayloadBytes()
//	_ = raw
func (c Context) PayloadBytes() []byte {
	return append([]byte(nil), c.payload...)
}

// Bind unmarshals the job payload into dst.
// @group Job
//
// Example: bind payload
//
//	type PollPayload struct {
//		URL string `json:"url"`
//	}
//	var payload PollPayload
//	_ = jc.Bind(&payload)
func (c Context) Bind(dst any) error {
	return json.Unmarshal(c.payload, dst)
}
