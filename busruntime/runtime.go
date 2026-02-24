package busruntime

import (
	"context"
	"time"
)

// InboundJob is the minimal job view the orchestration runtime needs from the queue layer.
type InboundJob interface {
	Bind(dst any) error
	PayloadBytes() []byte
}

type Handler func(ctx context.Context, job InboundJob) error

type JobOptions struct {
	Queue     string
	Delay     time.Duration
	Timeout   time.Duration
	Retry     int
	Backoff   time.Duration
	UniqueFor time.Duration
}

// Runtime is the queue runtime surface required by the orchestration engine.
type Runtime interface {
	BusRegister(jobType string, handler Handler)
	BusDispatch(ctx context.Context, jobType string, payload []byte, opts JobOptions) error
	StartWorkers(ctx context.Context) error
	Shutdown(ctx context.Context) error
}
