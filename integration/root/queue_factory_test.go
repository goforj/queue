//go:build integration

package root_test

import (
	"github.com/goforj/queue"
	"github.com/goforj/queue/integration/testenv"
)

func newQueueRuntime(cfg any) (queue.QueueRuntime, error) {
	return testenv.NewQueueRuntime(cfg)
}
