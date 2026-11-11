package queue

import (
	"context"
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
func (w *startupContextWorker) Shutdown(context.Context) error { return nil }

// SetHandlerContextDecorator models the existing legacy Redis decorator hook.
func (w *startupContextWorker) SetHandlerContextDecorator(fn func(context.Context) context.Context) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.decorator = fn
}

// deliver follows native Redis ordering so duplicate fallback preparation overwrites the consumer identity.
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
	return handler(ctx, job)
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

// TestContextPreparationOverlappingStarts keeps initial and dynamic handlers tied to each worker's negotiated hooks.
func TestContextPreparationOverlappingStarts(t *testing.T) {
	for _, firstPrepared := range []bool{false, true} {
		name := "legacy first"
		if firstPrepared {
			name = "prepared first"
		}
		t.Run(name, func(t *testing.T) {
			first, second := newStartupContextWorker(), newStartupContextWorker()
			workers := []driverWorkerBackend{first, &preparedStartupContextWorker{second}}
			if firstPrepared {
				workers = []driverWorkerBackend{&preparedStartupContextWorker{first}, second}
			}
			propagator := &concurrentContextPropagator{}
			var decorated atomic.Int64
			common := &queueCommon{
				inner: &queueBackendRecorder{}, cfg: Config{Driver: DriverRedis}, contextPropagator: propagator,
				handlerContextDecorator: func(ctx context.Context) context.Context {
					decorated.Add(1)
					if ctx.Value(propagationKey("trace")) != "producer" {
						t.Error("decorator did not receive the transported producer")
					}
					return context.WithValue(ctx, propagationKey("trace"), "consumer")
				},
			}
			var factoryCalls atomic.Int64
			q := &externalQueueRuntime{common: common, newWorker: func(int) (driverWorkerBackend, error) {
				return workers[factoryCalls.Add(1)-1], nil
			}}
			var handled atomic.Int64
			handler := func(ctx context.Context, _ Job) error {
				handled.Add(1)
				if ctx.Value(propagationKey("trace")) != "consumer" {
					t.Error("handler lost the worker's decorated consumer identity")
				}
				return nil
			}
			q.Register("bus:job", handler)
			firstDone, secondDone := make(chan error, 1), make(chan error, 1)
			go func() { firstDone <- q.StartWorkers(context.Background()) }()
			<-first.startEntered
			go func() { secondDone <- q.StartWorkers(context.Background()) }()
			<-second.startEntered
			close(first.startRelease)
			if err := <-firstDone; err != nil {
				t.Fatal(err)
			}
			q.Register("bus:chain:node", handler)
			for _, jobType := range []string{"bus:job", "bus:chain:node"} {
				if err := first.deliver(jobType); err != nil {
					t.Fatal(err)
				}
			}
			if err := q.Shutdown(context.Background()); err != nil {
				t.Fatal(err)
			}
			close(second.startRelease)
			if err := <-secondDone; err != nil {
				t.Fatal(err)
			}
			q.Register("bus:chain:node", handler)
			for _, jobType := range []string{"bus:job", "bus:chain:node"} {
				if err := second.deliver(jobType); err != nil {
					t.Fatal(err)
				}
			}
			if handled.Load() != 4 || decorated.Load() != 4 || propagator.extracts.Load() != 4 {
				t.Fatalf("handlers=%d decorations=%d extractions=%d", handled.Load(), decorated.Load(), propagator.extracts.Load())
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
