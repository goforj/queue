package testenv

import (
	"context"
	"testing"

	"github.com/goforj/queue"
)

type runtimeNoWorkersStub struct{}

func (runtimeNoWorkersStub) Driver() queue.Driver                { return queue.DriverSync }
func (runtimeNoWorkersStub) Dispatch(any) error                  { return nil }
func (runtimeNoWorkersStub) WithContext(context.Context) Runtime { return runtimeNoWorkersStub{} }
func (runtimeNoWorkersStub) Register(string, queue.Handler)      {}
func (runtimeNoWorkersStub) StartWorkers(context.Context) error  { return nil }
func (runtimeNoWorkersStub) Shutdown(context.Context) error      { return nil }

type runtimeWorkersStub struct {
	next  Runtime
	calls []int
}

func (r *runtimeWorkersStub) Driver() queue.Driver                { return queue.DriverSync }
func (r *runtimeWorkersStub) Dispatch(any) error                  { return nil }
func (r *runtimeWorkersStub) WithContext(context.Context) Runtime { return r }
func (r *runtimeWorkersStub) Register(string, queue.Handler)      {}
func (r *runtimeWorkersStub) StartWorkers(context.Context) error  { return nil }
func (r *runtimeWorkersStub) Shutdown(context.Context) error      { return nil }
func (r *runtimeWorkersStub) Workers(n int) Runtime {
	r.calls = append(r.calls, n)
	if r.next != nil {
		return r.next
	}
	return r
}

type runtimeBadWorkersStub struct{ runtimeNoWorkersStub }

func (runtimeBadWorkersStub) Workers(_ int) string { return "not-runtime" }

func TestWithWorkers(t *testing.T) {
	t.Run("nil", func(t *testing.T) {
		if got := WithWorkers(nil, 2); got != nil {
			t.Fatalf("expected nil, got %T", got)
		}
	})

	t.Run("no workers method", func(t *testing.T) {
		r := runtimeNoWorkersStub{}
		got := WithWorkers(r, 3)
		if got != r {
			t.Fatalf("expected original runtime, got %#v", got)
		}
	})

	t.Run("workers method returns runtime", func(t *testing.T) {
		next := runtimeNoWorkersStub{}
		r := &runtimeWorkersStub{next: next}
		got := WithWorkers(r, 4)
		if len(r.calls) != 1 || r.calls[0] != 4 {
			t.Fatalf("expected Workers(4) call, got %v", r.calls)
		}
		if got != next {
			t.Fatalf("expected next runtime, got %#v", got)
		}
	})

	t.Run("workers method returns wrong type", func(t *testing.T) {
		r := runtimeBadWorkersStub{}
		got := WithWorkers(r, 5)
		if got != r {
			t.Fatalf("expected original runtime fallback, got %#v", got)
		}
	})
}
