package testenv

import (
	"context"
	"fmt"
	"reflect"

	"github.com/goforj/queue"
	"github.com/goforj/queue/busruntime"
)

type Runtime interface {
	Driver() queue.Driver
	WithContext(ctx context.Context) Runtime
	Dispatch(job any) error
	Register(jobType string, handler queue.Handler)
	StartWorkers(ctx context.Context) error
	Shutdown(ctx context.Context) error
}

func WithWorkers(r Runtime, count int) Runtime {
	if r == nil {
		return nil
	}
	if wrapped, ok := r.(*runtimeAdapter); ok {
		out := reflect.ValueOf(wrapped.raw).MethodByName("Workers").Call([]reflect.Value{reflect.ValueOf(count)})
		if len(out) != 1 {
			return r
		}
		return wrapRuntime(out[0].Interface())
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

type runtimeCore interface {
	Driver() queue.Driver
	Dispatch(job any) error
	Register(jobType string, handler queue.Handler)
	StartWorkers(ctx context.Context) error
	Shutdown(ctx context.Context) error
}

type runtimeBusCore interface {
	BusRegister(jobType string, handler busruntime.Handler)
	BusDispatch(ctx context.Context, jobType string, payload []byte, opts busruntime.JobOptions) error
}

type runtimeAdapter struct {
	raw runtimeCore
}

func wrapRuntime(raw any) Runtime {
	core, ok := raw.(runtimeCore)
	if !ok {
		return nil
	}
	return &runtimeAdapter{raw: core}
}

func (r *runtimeAdapter) Driver() queue.Driver {
	return r.raw.Driver()
}

func (r *runtimeAdapter) WithContext(ctx context.Context) Runtime {
	m := reflect.ValueOf(r.raw).MethodByName("WithContext")
	if !m.IsValid() {
		return r
	}
	arg := reflect.Zero(m.Type().In(0))
	if ctx != nil {
		arg = reflect.ValueOf(ctx)
	}
	out := m.Call([]reflect.Value{arg})
	if len(out) != 1 {
		return r
	}
	wrapped := wrapRuntime(out[0].Interface())
	if wrapped == nil {
		return r
	}
	return wrapped
}

func (r *runtimeAdapter) Dispatch(job any) error {
	return r.raw.Dispatch(job)
}

func (r *runtimeAdapter) Register(jobType string, handler queue.Handler) {
	r.raw.Register(jobType, handler)
}

func (r *runtimeAdapter) StartWorkers(ctx context.Context) error {
	return r.raw.StartWorkers(ctx)
}

func (r *runtimeAdapter) Shutdown(ctx context.Context) error {
	return r.raw.Shutdown(ctx)
}

func (r *runtimeAdapter) BusRegister(jobType string, handler busruntime.Handler) {
	if rt, ok := r.raw.(runtimeBusCore); ok {
		rt.BusRegister(jobType, handler)
	}
}

func (r *runtimeAdapter) BusDispatch(ctx context.Context, jobType string, payload []byte, opts busruntime.JobOptions) error {
	rt, ok := r.raw.(runtimeBusCore)
	if !ok {
		return fmt.Errorf("queue does not support bus runtime adapter")
	}
	return rt.BusDispatch(ctx, jobType, payload, opts)
}

func (r *runtimeAdapter) Pause(ctx context.Context, queueName string) error {
	if rt, ok := r.raw.(interface {
		Pause(context.Context, string) error
	}); ok {
		return rt.Pause(ctx, queueName)
	}
	return queue.ErrPauseUnsupported
}

func (r *runtimeAdapter) Resume(ctx context.Context, queueName string) error {
	if rt, ok := r.raw.(interface {
		Resume(context.Context, string) error
	}); ok {
		return rt.Resume(ctx, queueName)
	}
	return queue.ErrPauseUnsupported
}

func (r *runtimeAdapter) Stats(ctx context.Context) (queue.StatsSnapshot, error) {
	if rt, ok := r.raw.(interface {
		Stats(context.Context) (queue.StatsSnapshot, error)
	}); ok {
		return rt.Stats(ctx)
	}
	return queue.StatsSnapshot{}, fmt.Errorf("stats provider is not available for driver %q", r.raw.Driver())
}

type runtimeController interface {
	Pause(context.Context, string) error
	Resume(context.Context, string) error
}

type runtimeStats interface {
	Stats(context.Context) (queue.StatsSnapshot, error)
}

type runtimeReady interface {
	Ready(context.Context) error
}

func rawRuntime(v any) any {
	if wrapped, ok := v.(*runtimeAdapter); ok {
		return wrapped.raw
	}
	return v
}

func SupportsPause(v any) bool {
	_, ok := rawRuntime(v).(runtimeController)
	return ok
}

func SupportsNativeStats(v any) bool {
	_, ok := rawRuntime(v).(runtimeStats)
	return ok
}

func Ready(ctx context.Context, v any) error {
	if rt, ok := rawRuntime(v).(runtimeReady); ok {
		return rt.Ready(ctx)
	}
	return nil
}

func Pause(ctx context.Context, v any, queueName string) error {
	if rt, ok := rawRuntime(v).(runtimeController); ok {
		return rt.Pause(ctx, queueName)
	}
	return queue.ErrPauseUnsupported
}

func Resume(ctx context.Context, v any, queueName string) error {
	if rt, ok := rawRuntime(v).(runtimeController); ok {
		return rt.Resume(ctx, queueName)
	}
	return queue.ErrPauseUnsupported
}

func Snapshot(ctx context.Context, v any, collector *queue.StatsCollector) (queue.StatsSnapshot, error) {
	if rt, ok := rawRuntime(v).(runtimeStats); ok {
		snapshot, err := rt.Stats(ctx)
		if err == nil || collector == nil {
			return snapshot, err
		}
		return collector.Snapshot(), nil
	}
	if collector != nil {
		return collector.Snapshot(), nil
	}
	driver := queue.Driver("")
	if rt, ok := rawRuntime(v).(interface{ Driver() queue.Driver }); ok {
		driver = rt.Driver()
	}
	return queue.StatsSnapshot{}, fmt.Errorf("stats provider is not available for driver %q", driver)
}
