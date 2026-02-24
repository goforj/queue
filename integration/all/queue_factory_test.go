//go:build integration

package all_test

import (
	. "github.com/goforj/queue"
	"github.com/goforj/queue/integration/testenv"
)

type QueueRuntime = testenv.Runtime

func newQueueRuntime(cfg any) (QueueRuntime, error) {
	return testenv.NewQueueRuntime(cfg)
}

func withWorkers(q QueueRuntime, count int) QueueRuntime {
	return testenv.WithWorkers(q, count)
}

func newQueue(cfg any, opts ...Option) (*Queue, error) {
	return testenv.NewQueue(cfg, opts...)
}
