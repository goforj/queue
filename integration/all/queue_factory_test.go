//go:build integration

package all_test

import (
	. "github.com/goforj/queue"
	"github.com/goforj/queue/integration/testenv"
)

func newQueueRuntime(cfg any) (QueueRuntime, error) {
	return testenv.NewQueueRuntime(cfg)
}

func newQueue(cfg any, opts ...Option) (*Queue, error) {
	return testenv.NewQueue(cfg, opts...)
}
