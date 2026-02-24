package bus_test

import (
	"fmt"

	"github.com/goforj/queue"
	"github.com/goforj/queue/busruntime"
	"github.com/goforj/queue/internal/testbridge"
)

type busTestRuntime interface {
	busruntime.Runtime
	Dispatch(job any) error
}

func newBusTestRuntime(cfg queue.Config) (busTestRuntime, error) {
	q, err := queue.New(cfg)
	if err != nil {
		return nil, err
	}
	raw, err := testbridge.RuntimeFromQueue(q)
	if err != nil {
		return nil, err
	}
	rt, ok := raw.(busTestRuntime)
	if !ok {
		return nil, fmt.Errorf("unexpected runtime type %T", raw)
	}
	return rt, nil
}
