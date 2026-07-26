package driverbridge

import (
	"context"
	"errors"
	"testing"

	"github.com/goforj/queue"
)

type externalBackendStub struct {
	driver queue.Driver
	stats  queue.StatsSnapshot

	listJobsResult queue.ListJobsResult
	historyResult  []queue.QueueHistoryPoint
	listCalled     bool
	retryCalled    bool
	cancelCalled   bool
	deleteCalled   bool
	clearCalled    bool
	historyCalled  bool
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
func (b *externalBackendStub) ListJobs(context.Context, queue.ListJobsOptions) (queue.ListJobsResult, error) {
	b.listCalled = true
	return b.listJobsResult, nil
}
func (b *externalBackendStub) RetryJob(context.Context, string, string) error {
	b.retryCalled = true
	return nil
}
func (b *externalBackendStub) CancelJob(context.Context, string) error {
	b.cancelCalled = true
	return nil
}
func (b *externalBackendStub) DeleteJob(context.Context, string, string) error {
	b.deleteCalled = true
	return nil
}
func (b *externalBackendStub) ClearQueue(context.Context, string) error {
	b.clearCalled = true
	return nil
}
func (b *externalBackendStub) History(context.Context, string, queue.QueueHistoryWindow) ([]queue.QueueHistoryPoint, error) {
	b.historyCalled = true
	return b.historyResult, nil
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

// DrainWorkers completes the native bridge stub's worker-drain phase.
func (b *nativeBackendStub) DrainWorkers(context.Context) error {
	return nil
}
func (b *nativeBackendStub) Shutdown(context.Context) error {
	b.stopped = true
	return nil
}

type preflightBackendStub struct {
	externalBackendStub
	err   error
	calls int
}

// Preflight records one legacy backend readiness check.
func (b *preflightBackendStub) Preflight(context.Context) error {
	b.calls++
	return b.err
}

type readyBackendStub struct {
	preflightBackendStub
	readyErr   error
	readyCalls int
}

// Ready records one canonical backend readiness check.
func (b *readyBackendStub) Ready(context.Context) error {
	b.readyCalls++
	return b.readyErr
}

// TestNewQueueFromDriverPreservesReadiness verifies optional health checks
// survive adaptation and canonical Ready takes precedence over legacy Preflight.
func TestNewQueueFromDriverPreservesReadiness(t *testing.T) {
	preflightErr := errors.New("managed schema unavailable")
	legacy := &preflightBackendStub{
		externalBackendStub: externalBackendStub{driver: queue.DriverDatabase},
		err:                 preflightErr,
	}
	legacyQueue, err := NewQueueFromDriver(
		queue.Config{Driver: queue.DriverDatabase},
		nil,
		legacy,
		nil,
	)
	if err != nil {
		t.Fatalf("new queue from legacy-ready driver: %v", err)
	}
	if err := legacyQueue.Ready(context.Background()); !errors.Is(err, preflightErr) {
		t.Fatalf("legacy readiness = %v, want %v", err, preflightErr)
	}
	if legacy.calls != 1 {
		t.Fatalf("legacy readiness calls = %d, want 1", legacy.calls)
	}

	readyErr := errors.New("backend not ready")
	canonical := &readyBackendStub{
		preflightBackendStub: preflightBackendStub{
			externalBackendStub: externalBackendStub{driver: queue.DriverDatabase},
			err:                 errors.New("legacy readiness must not run"),
		},
		readyErr: readyErr,
	}
	canonicalQueue, err := NewQueueFromDriver(
		queue.Config{Driver: queue.DriverDatabase},
		nil,
		canonical,
		nil,
	)
	if err != nil {
		t.Fatalf("new queue from canonical-ready driver: %v", err)
	}
	if err := canonicalQueue.Ready(context.Background()); !errors.Is(err, readyErr) {
		t.Fatalf("canonical readiness = %v, want %v", err, readyErr)
	}
	if canonical.readyCalls != 1 || canonical.calls != 0 {
		t.Fatalf("readiness calls = canonical:%d legacy:%d, want 1/0", canonical.readyCalls, canonical.calls)
	}
}

func TestNewQueueFromDriver_ExternalWorkerFactoryAndOptionalCapabilities(t *testing.T) {
	backend := &externalBackendStub{
		driver: queue.DriverNATS,
		stats: queue.StatsSnapshot{
			ByQueue: map[string]queue.QueueCounters{
				"default": {Pending: 2},
			},
		},
		listJobsResult: queue.ListJobsResult{
			Jobs:  []queue.JobSnapshot{{ID: "job-1", Queue: "default"}},
			Total: 1,
		},
		historyResult: []queue.QueueHistoryPoint{
			{Processed: 10, Failed: 1},
		},
	}
	w := &workerStub{}

	q, err := NewQueueFromDriver(
		queue.Config{Driver: queue.DriverNATS, DefaultQueue: "default"},
		nil,
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
	if !queue.SupportsQueueAdmin(q) {
		t.Fatal("expected queue admin support to be preserved through driver bridge")
	}
	if _, err := q.Stats(context.Background()); err != nil {
		t.Fatalf("expected stats to succeed, got %v", err)
	}
	list, err := q.ListJobs(context.Background(), queue.ListJobsOptions{Queue: "default", State: queue.JobStatePending, Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("expected list jobs to succeed, got %v", err)
	}
	if list.Total != 1 || len(list.Jobs) != 1 {
		t.Fatalf("unexpected list jobs result: %+v", list)
	}
	if !backend.listCalled {
		t.Fatal("expected list jobs to be forwarded")
	}
	if err := q.RetryJob(context.Background(), "default", "job-1"); err != nil {
		t.Fatalf("expected retry job to succeed, got %v", err)
	}
	if err := q.CancelJob(context.Background(), "job-1"); err != nil {
		t.Fatalf("expected cancel job to succeed, got %v", err)
	}
	if err := q.DeleteJob(context.Background(), "default", "job-1"); err != nil {
		t.Fatalf("expected delete job to succeed, got %v", err)
	}
	if err := q.ClearQueue(context.Background(), "default"); err != nil {
		t.Fatalf("expected clear queue to succeed, got %v", err)
	}
	history, err := q.History(context.Background(), "default", queue.QueueHistoryHour)
	if err != nil {
		t.Fatalf("expected history to succeed, got %v", err)
	}
	if len(history) != 1 || history[0].Processed != 10 || history[0].Failed != 1 {
		t.Fatalf("unexpected history result: %+v", history)
	}
	if !backend.retryCalled || !backend.cancelCalled || !backend.deleteCalled || !backend.clearCalled || !backend.historyCalled {
		t.Fatal("expected all queue admin methods to be forwarded")
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
		nil,
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

// TestNewQueueFromDriverSharesObserverSink verifies late root options reach the same sink retained by driver workers.
func TestNewQueueFromDriverSharesObserverSink(t *testing.T) {
	var configEvents []queue.Event
	var optionEvents []queue.Event
	sink := NewObserverSink(queue.ObserverFunc(func(_ context.Context, event queue.Event) {
		configEvents = append(configEvents, event)
	}))

	q, err := NewQueueFromDriver(
		queue.Config{Driver: queue.DriverNATS, DefaultQueue: "default"},
		sink,
		&externalBackendStub{driver: queue.DriverNATS},
		nil,
		queue.WithObserver(queue.ObserverFunc(func(_ context.Context, event queue.Event) {
			optionEvents = append(optionEvents, event)
		})),
	)
	if err != nil {
		t.Fatalf("new queue from driver failed: %v", err)
	}
	if q.Driver() != queue.DriverNATS {
		t.Fatalf("driver = %q, want nats", q.Driver())
	}

	queue.SafeObserve(context.Background(), sink, queue.Event{Kind: queue.EventRepublishFailed})
	if len(configEvents) != 1 || len(optionEvents) != 1 {
		t.Fatalf("captured sink counts = config:%d option:%d, want 1/1", len(configEvents), len(optionEvents))
	}
	if configEvents[0].EventID == "" || configEvents[0].EventID != optionEvents[0].EventID {
		t.Fatalf("captured sink event identity differs: config=%+v option=%+v", configEvents[0], optionEvents[0])
	}
	if configEvents[0].Layer != queue.EventLayerWorker {
		t.Fatalf("republish_failed layer = %q, want worker", configEvents[0].Layer)
	}
}
