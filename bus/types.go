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
//	job := bus.NewJob("monitor:poll", map[string]string{
//		"url": "https://goforj.dev/health",
//	}).
//		OnQueue("monitor-critical").
//		Retry(3).
//		Backoff(500 * time.Millisecond)
//	_ = job
func NewJob(jobType string, payload any) Job {
	return Job{Type: jobType, Payload: payload}
}

func (j Job) OnQueue(name string) Job {
	j.Options.Queue = name
	return j
}

func (j Job) Delay(delay time.Duration) Job {
	j.Options.Delay = delay
	return j
}

func (j Job) Timeout(timeout time.Duration) Job {
	j.Options.Timeout = timeout
	return j
}

func (j Job) Retry(max int) Job {
	j.Options.Retry = max
	return j
}

func (j Job) Backoff(backoff time.Duration) Job {
	j.Options.Backoff = backoff
	return j
}

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

func (c Context) PayloadBytes() []byte {
	return append([]byte(nil), c.payload...)
}

func (c Context) Bind(dst any) error {
	return json.Unmarshal(c.payload, dst)
}
