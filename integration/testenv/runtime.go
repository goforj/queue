package testenv

import (
	"context"
	"reflect"

	"github.com/goforj/queue"
)

type Runtime interface {
	Driver() queue.Driver
	Dispatch(job any) error
	DispatchCtx(ctx context.Context, job any) error
	Register(jobType string, handler queue.Handler)
	StartWorkers(ctx context.Context) error
	Shutdown(ctx context.Context) error
}

func WithWorkers(r Runtime, count int) Runtime {
	if r == nil {
		return nil
	}
	m := reflect.ValueOf(r).MethodByName("Workers")
	if !m.IsValid() {
		return r
	}
	out := m.Call([]reflect.Value{reflect.ValueOf(count)})
	if len(out) != 1 {
		return r
	}
	if next, ok := out[0].Interface().(Runtime); ok {
		return next
	}
	return r
}
