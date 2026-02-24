//go:build integration

package root_test

import (
	"github.com/goforj/queue/integration/testenv"
)

type QueueRuntime = testenv.Runtime

func newQueueRuntime(cfg any) (QueueRuntime, error) {
	return testenv.NewQueueRuntime(cfg)
}

func withWorkers(q QueueRuntime, count int) QueueRuntime {
	return testenv.WithWorkers(q, count)
}
