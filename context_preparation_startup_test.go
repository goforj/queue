package queue

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// startupContextWorker models legacy Redis hooks while startup is controlled independently for each worker.
type startupContextWorker struct {
	mu           sync.Mutex
	handlers     map[string]Handler
	decorator    func(context.Context) context.Context
	preparer     func(context.Context, Job) context.Context
	startEntered chan struct{}
	startRelease chan struct{}
	shutdowns    atomic.Int64
	shutdownErr  error
	observer     Observer
}

// newStartupContextWorker gives each startup its own hook and registration state.
func newStartupContextWorker() *startupContextWorker {
	return &startupContextWorker{
		handlers:     make(map[string]Handler),
		startEntered: make(chan struct{}),
		startRelease: make(chan struct{}),
	}
}

// Register allows dynamic registrations to overlap another worker's pending startup.
func (w *startupContextWorker) Register(jobType string, handler Handler) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.handlers[jobType] = handler
}

// StartWorkers lets tests publish workers in a different order from hook negotiation.
func (w *startupContextWorker) StartWorkers(context.Context) error {
	close(w.startEntered)
	<-w.startRelease
	return nil
}

// Shutdown leaves hook state inspectable so another pending startup can still be verified.
func (w *startupContextWorker) Shutdown(context.Context) error {
	w.shutdowns.Add(1)
	return w.shutdownErr
}

// SetHandlerContextDecorator models the existing legacy Redis decorator hook.
func (w *startupContextWorker) SetHandlerContextDecorator(fn func(context.Context) context.Context) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.decorator = fn
}

// deliver follows native Redis ordering so observations and handlers share the decorated context.
func (w *startupContextWorker) deliver(jobType string) error {
	w.mu.Lock()
	handler, preparer, decorator := w.handlers[jobType], w.preparer, w.decorator
	w.mu.Unlock()
	job := NewJob(jobType).Payload([]byte(`{"context":{"traceparent":"producer"}}`))
	ctx := context.Background()
	if preparer != nil {
		ctx = preparer(ctx, job)
	}
	if decorator != nil {
		ctx = decorator(ctx)
	}
	if w.observer != nil {
		w.observer.Observe(ctx, Event{Kind: EventProcessStarted})
	}
	err := handler(ctx, job)
	if w.observer != nil {
		w.observer.Observe(ctx, Event{Kind: EventProcessSucceeded})
	}
	return err
}

// preparedStartupContextWorker models modern Redis preparation without giving legacy workers the capability.
type preparedStartupContextWorker struct{ *startupContextWorker }

// SetHandlerContextPreparer records the hook owned exclusively by this worker instance.
func (w *preparedStartupContextWorker) SetHandlerContextPreparer(fn func(context.Context, Job) context.Context) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.preparer = fn
	return true
}

// decliningStartupContextWorker models an adapter that exposes the hook but cannot honor it.
type decliningStartupContextWorker struct{ *startupContextWorker }

// SetHandlerContextPreparer declines preparation so startup must reject this worker before registration.
func (*decliningStartupContextWorker) SetHandlerContextPreparer(func(context.Context, Job) context.Context) bool {
	return false
}

// TestContextPreparationRejectsUnsupportedRedisWorkers preserves startup state and releases only the rejected worker.
func TestContextPreparationRejectsUnsupportedRedisWorkers(t *testing.T) {
	cleanupFailure := errors.New("cleanup failed")
	for _, test := range []struct {
		name       string
		declines   bool
		cleanupErr error
	}{
		{name: "missing hook"},
		{name: "declining hook", declines: true},
		{name: "cleanup failure", cleanupErr: cleanupFailure},
	} {
		t.Run(test.name, func(t *testing.T) {
			rejected, supported := newStartupContextWorker(), newStartupContextWorker()
			rejected.shutdownErr = test.cleanupErr
			close(rejected.startRelease)
			close(supported.startRelease)
			var rejectedBackend driverWorkerBackend = rejected
			if test.declines {
				rejectedBackend = &decliningStartupContextWorker{rejected}
			}
			inner := &queueBackendRecorder{}
			var calls atomic.Int64
			q := &externalQueueRuntime{
				common: &queueCommon{inner: inner, cfg: Config{Driver: DriverRedis}, contextPropagator: &concurrentContextPropagator{}},
				newWorker: func(int) (driverWorkerBackend, error) {
					if calls.Add(1) == 1 {
						return rejectedBackend, nil
					}
					return &preparedStartupContextWorker{supported}, nil
				},
			}
			q.Register("bus:job", func(context.Context, Job) error { return nil })
			err := q.StartWorkers(context.Background())
			if err == nil || !strings.Contains(err.Error(), "redis worker must support handler context preparation") || !strings.Contains(err.Error(), "WithContextPropagator") {
				t.Fatalf("startup error=%v", err)
			}
			if test.cleanupErr != nil && !errors.Is(err, test.cleanupErr) {
				t.Fatalf("startup lost cleanup failure: %v", err)
			}
			if q.started || q.worker != nil || inner.shutdowns != 0 || rejected.shutdowns.Load() != 1 || len(rejected.handlers) != 0 || rejected.decorator != nil {
				t.Fatal("rejection changed runtime/shared backend state or registered/decorated the rejected worker")
			}
			select {
			case <-rejected.startEntered:
				t.Fatal("unsupported worker was started")
			default:
			}
			if err := q.StartWorkers(context.Background()); err != nil {
				t.Fatalf("retry with supported worker: %v", err)
			}
			if !q.started || q.worker == nil || supported.handlers["bus:job"] == nil {
				t.Fatal("retry did not publish supported worker with retained registrations")
			}
			if err := q.Shutdown(context.Background()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestContextPreparationPublisherOnlyRedisDoesNotRequireWorker keeps construction and publishing independent of worker capability.
func TestContextPreparationPublisherOnlyRedisDoesNotRequireWorker(t *testing.T) {
	inner := &queueBackendRecorder{}
	var factoryCalls atomic.Int64
	physical := &externalQueueRuntime{
		common: &queueCommon{inner: inner, cfg: Config{Driver: DriverRedis}},
		newWorker: func(int) (driverWorkerBackend, error) {
			factoryCalls.Add(1)
			return newStartupContextWorker(), nil
		},
	}
	q, err := newQueueFromRuntime(physical, WithContextPropagator(&concurrentContextPropagator{}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.Dispatch(NewJob("publish.only")); err != nil {
		t.Fatal(err)
	}
	if factoryCalls.Load() != 0 || len(inner.dispatched) != 1 {
		t.Fatal("publisher-only use attempted to construct a worker")
	}
	if err := q.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// TestContextPreparationLegacyRedisWithoutPropagator retains decoration on every native observation and handler.
func TestContextPreparationLegacyRedisWithoutPropagator(t *testing.T) {
	worker := newStartupContextWorker()
	close(worker.startRelease)
	var observed, decorated, handled atomic.Int64
	observer := ObserverFunc(func(ctx context.Context, _ Event) {
		observed.Add(1)
		if ctx.Value(propagationKey("trace")) != "consumer" {
			t.Error("native observation lost the existing decorator")
		}
	})
	worker.observer = observer
	q := &externalQueueRuntime{
		common: &queueCommon{
			inner: &queueBackendRecorder{}, cfg: Config{Driver: DriverRedis, Observer: observer},
			handlerContextDecorator: func(ctx context.Context) context.Context {
				decorated.Add(1)
				return context.WithValue(ctx, propagationKey("trace"), "consumer")
			},
		},
		newWorker: func(int) (driverWorkerBackend, error) { return worker, nil },
	}
	handler := func(ctx context.Context, _ Job) error {
		handled.Add(1)
		if ctx.Value(propagationKey("trace")) != "consumer" {
			t.Error("legacy handler lost the existing decorator")
		}
		return nil
	}
	q.Register("bus:job", handler)
	if err := q.StartWorkers(context.Background()); err != nil {
		t.Fatal(err)
	}
	q.Register("bus:chain:node", handler)
	for _, jobType := range []string{"bus:job", "bus:chain:node"} {
		if err := worker.deliver(jobType); err != nil {
			t.Fatal(err)
		}
	}
	if observed.Load() != 4 || decorated.Load() != 2 || handled.Load() != 2 {
		t.Fatalf("observations=%d decorations=%d handlers=%d", observed.Load(), decorated.Load(), handled.Load())
	}
	if err := q.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// TestContextPreparationOverlappingRejectedAndSupportedStarts never lets rejection overwrite another worker's published state.
func TestContextPreparationOverlappingRejectedAndSupportedStarts(t *testing.T) {
	for _, rejectFirst := range []bool{true, false} {
		name := "reject after publication"
		if rejectFirst {
			name = "reject before publication"
		}
		t.Run(name, func(t *testing.T) {
			supported, rejected := newStartupContextWorker(), newStartupContextWorker()
			close(rejected.startRelease)
			rejectEntered, rejectRelease := make(chan struct{}), make(chan struct{})
			var factoryCalls atomic.Int64
			propagator := &concurrentContextPropagator{}
			var decorated, handled, observed atomic.Int64
			observer := ObserverFunc(func(ctx context.Context, _ Event) {
				observed.Add(1)
				if ctx.Value(propagationKey("trace")) != "consumer" {
					t.Error("native observation lost consumer identity")
				}
			})
			supported.observer = observer
			q := &externalQueueRuntime{
				common: &queueCommon{
					inner: &queueBackendRecorder{}, cfg: Config{Driver: DriverRedis, Observer: observer}, contextPropagator: propagator,
					handlerContextDecorator: func(ctx context.Context) context.Context {
						decorated.Add(1)
						if ctx.Value(propagationKey("trace")) != "producer" {
							t.Error("decorator lost transported producer")
						}
						return context.WithValue(ctx, propagationKey("trace"), "consumer")
					},
				},
				newWorker: func(int) (driverWorkerBackend, error) {
					if factoryCalls.Add(1) == 1 {
						return &preparedStartupContextWorker{supported}, nil
					}
					close(rejectEntered)
					<-rejectRelease
					return rejected, nil
				},
			}
			handler := func(ctx context.Context, _ Job) error {
				handled.Add(1)
				if ctx.Value(propagationKey("trace")) != "consumer" {
					t.Error("handler lost consumer identity")
				}
				return nil
			}
			q.Register("bus:job", handler)
			supportedDone, rejectedDone := make(chan error, 1), make(chan error, 1)
			go func() { supportedDone <- q.StartWorkers(context.Background()) }()
			<-supported.startEntered
			go func() { rejectedDone <- q.StartWorkers(context.Background()) }()
			<-rejectEntered
			if rejectFirst {
				close(rejectRelease)
				if err := <-rejectedDone; err == nil {
					t.Fatal("unsupported concurrent worker was accepted")
				}
			}
			close(supported.startRelease)
			if err := <-supportedDone; err != nil {
				t.Fatal(err)
			}
			published := q.worker
			q.Register("bus:chain:node", handler)
			if !rejectFirst {
				close(rejectRelease)
				if err := <-rejectedDone; err == nil {
					t.Fatal("unsupported concurrent worker was accepted")
				}
			}
			if !q.started || q.worker != published || rejected.shutdowns.Load() != 1 || supported.shutdowns.Load() != 0 || len(rejected.handlers) != 0 {
				t.Fatal("rejected startup changed the supported worker or leaked its own worker")
			}
			for _, jobType := range []string{"bus:job", "bus:chain:node"} {
				if err := supported.deliver(jobType); err != nil {
					t.Fatal(err)
				}
			}
			if handled.Load() != 2 || decorated.Load() != 2 || propagator.extracts.Load() != 2 || observed.Load() != 4 {
				t.Fatalf("handlers=%d decorations=%d extractions=%d observations=%d", handled.Load(), decorated.Load(), propagator.extracts.Load(), observed.Load())
			}
			if err := q.Shutdown(context.Background()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestContextPreparationConcurrentStarts exercises simultaneous hook negotiation under the race detector.
func TestContextPreparationConcurrentStarts(t *testing.T) {
	workers := []*startupContextWorker{newStartupContextWorker(), newStartupContextWorker()}
	var ready sync.WaitGroup
	ready.Add(len(workers))
	release := make(chan struct{})
	var factoryCalls atomic.Int64
	q := &externalQueueRuntime{
		common: &queueCommon{cfg: Config{Driver: DriverRedis}, contextPropagator: &concurrentContextPropagator{}},
		newWorker: func(int) (driverWorkerBackend, error) {
			worker := workers[factoryCalls.Add(1)-1]
			ready.Done()
			<-release
			return &preparedStartupContextWorker{worker}, nil
		},
	}
	finished := make(chan error, len(workers))
	for range workers {
		go func() { finished <- q.StartWorkers(context.Background()) }()
	}
	ready.Wait()
	close(release)
	for _, worker := range workers {
		<-worker.startEntered
		close(worker.startRelease)
	}
	for range workers {
		if err := <-finished; err != nil {
			t.Fatal(err)
		}
	}
}
