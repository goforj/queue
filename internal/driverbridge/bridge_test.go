package driverbridge

import (
	"context"
	"testing"

	"github.com/goforj/queue"
)

type externalBackendStub struct {
	driver queue.Driver
	stats  queue.StatsSnapshot
}

func (b *externalBackendStub) Driver() queue.Driver { return b.driver }
func (b *externalBackendStub) Dispatch(context.Context, queue.Job) error {
	return nil
}
func (b *externalBackendStub) Shutdown(context.Context) error { return nil }
func (b *externalBackendStub) Pause(context.Context, string) error {
	return nil
}
func (b *externalBackendStub) Resume(context.Context, string) error {
	return nil
}
func (b *externalBackendStub) Stats(context.Context) (queue.StatsSnapshot, error) {
	return b.stats, nil
}

type workerStub struct {
	started bool
	stopped bool
}

func (w *workerStub) Register(string, queue.Handler) {}
func (w *workerStub) StartWorkers(context.Context) error {
	w.started = true
	return nil
}
func (w *workerStub) Shutdown(context.Context) error {
	w.stopped = true
	return nil
}

type nativeBackendStub struct {
	externalBackendStub
	started bool
	stopped bool
}

func (b *nativeBackendStub) Register(string, queue.Handler) {}
func (b *nativeBackendStub) StartWorkers(context.Context) error {
	b.started = true
	return nil
}
func (b *nativeBackendStub) Shutdown(context.Context) error {
	b.stopped = true
	return nil
}

func TestNewQueueFromDriver_ExternalWorkerFactoryAndOptionalCapabilities(t *testing.T) {
	backend := &externalBackendStub{
		driver: queue.DriverNATS,
		stats: queue.StatsSnapshot{
			ByQueue: map[string]queue.QueueCounters{
				"default": {Pending: 2},
			},
		},
	}
	w := &workerStub{}

	q, err := NewQueueFromDriver(
		queue.Config{Driver: queue.DriverNATS, DefaultQueue: "default"},
		backend,
		func(int) (any, error) { return w, nil },
	)
	if err != nil {
		t.Fatalf("new queue from driver failed: %v", err)
	}

	if !queue.SupportsPause(q) {
		t.Fatal("expected pause support to be preserved through driver bridge")
	}
	if !queue.SupportsNativeStats(q) {
		t.Fatal("expected native stats support to be preserved through driver bridge")
	}
	if _, err := q.Stats(context.Background()); err != nil {
		t.Fatalf("expected stats to succeed, got %v", err)
	}
	if err := q.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers failed: %v", err)
	}
	if !w.started {
		t.Fatal("expected worker factory result to be started")
	}
	if err := q.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown failed: %v", err)
	}
	if !w.stopped {
		t.Fatal("expected worker to be shut down")
	}
}

func TestNewQueueFromDriver_NativeBackendOptionalCapabilities(t *testing.T) {
	backend := &nativeBackendStub{
		externalBackendStub: externalBackendStub{
			driver: queue.DriverDatabase,
			stats: queue.StatsSnapshot{
				ByQueue: map[string]queue.QueueCounters{
					"default": {Pending: 1},
				},
			},
		},
	}

	q, err := NewQueueFromDriver(
		queue.Config{Driver: queue.DriverDatabase, DefaultQueue: "default"},
		backend,
		nil,
	)
	if err != nil {
		t.Fatalf("new queue from native backend failed: %v", err)
	}

	if !queue.SupportsPause(q) {
		t.Fatal("expected pause support on native backend")
	}
	if !queue.SupportsNativeStats(q) {
		t.Fatal("expected native stats support on native backend")
	}
	if err := q.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers failed: %v", err)
	}
	if !backend.started {
		t.Fatal("expected native backend start path to be used")
	}
	if err := q.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown failed: %v", err)
	}
	if !backend.stopped {
		t.Fatal("expected native backend shutdown path to be used")
	}
}
