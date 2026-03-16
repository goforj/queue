package queueconfig

import "github.com/goforj/queue"

// DriverBaseConfig contains common constructor options shared by driver modules.
// It is embedded by driver module config types (for example, redisqueue.Config).
type DriverBaseConfig struct {
	DefaultQueue string
	Observer     queue.Observer
	Logger       queue.Logger
}
