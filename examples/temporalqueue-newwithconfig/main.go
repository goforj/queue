//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"context"
	"github.com/goforj/queue"
	"github.com/goforj/queue/driver/temporalqueue"
	"github.com/goforj/queue/queueconfig"
	"go.temporal.io/sdk/client"
	"time"
)

func main() {
	// NewWithConfig constructs a Temporal-backed queue without starting workers.

	// Example: Temporal config constructor
	q, err := temporalqueue.NewWithConfig(temporalqueue.Config{
		DriverBaseConfig:       queueconfig.DriverBaseConfig{DefaultQueue: "reports"},
		ClientOptions:          client.Options{HostPort: "localhost:7233"},
		DefaultActivityTimeout: 30 * time.Second,
		ShutdownTimeout:        15 * time.Second,
	}, queue.WithWorkers(4))
	if err != nil {
		return
	}
	defer q.Shutdown(context.Background())
	_ = q
}
