package queue

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

type localDispatcher struct {
	driver       Driver
	cfg          WorkerpoolConfig
	mu           sync.RWMutex
	queueMu      sync.RWMutex
	handlers     map[string]Handler
	unique       map[string]time.Time
	workQueue    chan queuedTask
	shutdownCh   chan struct{}
	shutdownOnce sync.Once
	workerWG     sync.WaitGroup
	delayedWG    sync.WaitGroup
	shuttingDown atomic.Bool
	enqueued     atomic.Int64
	started      atomic.Int64
	finished     atomic.Int64
	delayed      atomic.Int64
}

type workerContextKey string

const workerEnqueueKey workerContextKey = "queue.worker.enqueue.allowed"

type queuedTask struct {
	ctx  context.Context
	task Task
	opts enqueueOptions
}

func newLocalDispatcher(driver Driver) *localDispatcher {
	return newLocalDispatcherWithConfig(driver, WorkerpoolConfig{})
}

func newLocalDispatcherWithConfig(driver Driver, cfg WorkerpoolConfig) *localDispatcher {
	dispatcher := &localDispatcher{
		driver:     driver,
		cfg:        cfg.normalize(),
		handlers:   make(map[string]Handler),
		unique:     make(map[string]time.Time),
		shutdownCh: make(chan struct{}),
	}
	return dispatcher
}

// Driver returns the local dispatcher's driver mode.
// @group Dispatcher
//
// Example: local driver
//
//	dispatcher, err := queue.NewDispatcher(queue.Config{Driver: queue.DriverSync})
//	if err != nil {
//		return
//	}
//	fmt.Println(dispatcher.Driver())
//	// Output: sync
func (d *localDispatcher) Driver() Driver {
	return d.driver
}

// Register adds a task handler to the local dispatcher.
// @group Dispatcher
//
// Example: local register
//
//	dispatcher, err := queue.NewDispatcher(queue.Config{Driver: queue.DriverSync})
//	if err != nil {
//		return
//	}
//	dispatcher.Register("emails:send", func(ctx context.Context, task queue.Task) error { return nil })
func (d *localDispatcher) Register(taskType string, handler Handler) {
	if taskType == "" || handler == nil {
		return
	}
	d.mu.Lock()
	d.handlers[taskType] = handler
	d.mu.Unlock()
}

// Start initializes worker goroutines for workerpool mode.
// @group Dispatcher
//
// Example: local start
//
//	dispatcher, err := queue.NewDispatcher(queue.Config{
//		Driver:     queue.DriverWorkerpool,
//		Workerpool: queue.WorkerpoolConfig{Workers: 1, Buffer: 4},
//	})
//	if err != nil {
//		return
//	}
//	_ = dispatcher.Start(context.Background())
func (d *localDispatcher) Start(_ context.Context) error {
	if d.driver != DriverWorkerpool {
		return nil
	}
	d.startMemoryWorkers()
	return nil
}

// Shutdown drains delayed and active local workerpool tasks.
// @group Dispatcher
//
// Example: local shutdown
//
//	dispatcher, err := queue.NewDispatcher(queue.Config{
//		Driver:     queue.DriverWorkerpool,
//		Workerpool: queue.WorkerpoolConfig{Workers: 1, Buffer: 4},
//	})
//	if err != nil {
//		return
//	}
//	_ = dispatcher.Start(context.Background())
//	_ = dispatcher.Shutdown(context.Background())
func (d *localDispatcher) Shutdown(ctx context.Context) error {
	if d.driver != DriverWorkerpool {
		return nil
	}

	d.shutdownOnce.Do(func() {
		d.shuttingDown.Store(true)
		close(d.shutdownCh)
	})

	if err := waitGroupWithContext(ctx, &d.delayedWG); err != nil {
		return fmt.Errorf("workerpool delayed tasks drain failed: %w (%s)", err, d.shutdownStats())
	}

	d.queueMu.Lock()
	if d.workQueue != nil {
		close(d.workQueue)
		d.workQueue = nil
	}
	d.queueMu.Unlock()

	if err := waitGroupWithContext(ctx, &d.workerWG); err != nil {
		return fmt.Errorf("workerpool active tasks drain failed: %w (%s)", err, d.shutdownStats())
	}
	return nil
}

// Enqueue schedules or executes a task using the local driver.
// @group Dispatcher
//
// Example: local enqueue
//
//	dispatcher, err := queue.NewDispatcher(queue.Config{Driver: queue.DriverSync})
//	if err != nil {
//		return
//	}
//	dispatcher.Register("emails:send", func(ctx context.Context, task queue.Task) error { return nil })
//	_ = dispatcher.Enqueue(context.Background(), queue.Task{Type: "emails:send"}, queue.WithDelay(10*time.Millisecond))
func (d *localDispatcher) Enqueue(ctx context.Context, task Task, opts ...Option) error {
	if d.shuttingDown.Load() && !allowEnqueueDuringShutdown(ctx) {
		return ErrDispatcherShuttingDown
	}
	parsed := resolveOptions(opts...)
	if task.Type == "" {
		return fmt.Errorf("task type is required")
	}
	if parsed.uniqueTTL > 0 {
		if !d.claimUnique(task, parsed.uniqueTTL) {
			return ErrDuplicate
		}
	}
	if parsed.delay <= 0 {
		return d.enqueueNow(ctx, task, parsed)
	}
	d.delayedWG.Add(1)
	d.delayed.Add(1)
	go func() {
		defer d.delayedWG.Done()
		defer d.delayed.Add(-1)
		timer := time.NewTimer(parsed.delay)
		defer timer.Stop()
		select {
		case <-timer.C:
			_ = d.enqueueNow(context.Background(), task, parsed)
		case <-d.shutdownCh:
			return
		}
	}()
	return nil
}

func (d *localDispatcher) enqueueNow(ctx context.Context, task Task, parsed enqueueOptions) error {
	if _, ok := d.lookup(task.Type); !ok {
		return fmt.Errorf("no handler registered for task type %q", task.Type)
	}
	if d.driver == DriverWorkerpool {
		return d.enqueueAsync(ctx, task, parsed)
	}
	return d.runWithRetry(ctx, task, parsed)
}

func (d *localDispatcher) enqueueAsync(ctx context.Context, task Task, parsed enqueueOptions) error {
	if d.shuttingDown.Load() && !allowEnqueueDuringShutdown(ctx) {
		return ErrDispatcherShuttingDown
	}
	if ctx == nil {
		ctx = context.Background()
	}
	workQueue, err := d.workerQueueForEnqueue()
	if err != nil {
		return err
	}
	select {
	case workQueue <- queuedTask{ctx: ctx, task: task, opts: parsed}:
		d.enqueued.Add(1)
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (d *localDispatcher) workerQueueForEnqueue() (chan queuedTask, error) {
	d.queueMu.RLock()
	workQueue := d.workQueue
	d.queueMu.RUnlock()
	if workQueue != nil {
		return workQueue, nil
	}

	// Self-heal: if the in-memory worker queue is unexpectedly nil while the
	// dispatcher is active, rebuild workers so enqueue can continue.
	d.queueMu.Lock()
	defer d.queueMu.Unlock()
	if d.workQueue != nil {
		return d.workQueue, nil
	}
	if d.shuttingDown.Load() {
		return nil, ErrWorkerpoolQueueNotInitialized
	}
	d.startMemoryWorkersLocked()
	if d.workQueue == nil {
		return nil, ErrWorkerpoolQueueNotInitialized
	}
	return d.workQueue, nil
}

func (d *localDispatcher) startMemoryWorkers() {
	d.queueMu.Lock()
	defer d.queueMu.Unlock()
	d.startMemoryWorkersLocked()
}

func (d *localDispatcher) startMemoryWorkersLocked() {
	if d.workQueue != nil {
		return
	}
	workers := d.cfg.Workers
	bufferSize := d.cfg.Buffer
	d.workQueue = make(chan queuedTask, bufferSize)
	workQueue := d.workQueue
	for i := 0; i < workers; i++ {
		d.workerWG.Add(1)
		go d.worker(workQueue)
	}
}

func (d *localDispatcher) worker(workQueue <-chan queuedTask) {
	defer d.workerWG.Done()
	taskTimeout := d.cfg.TaskTimeout
	for job := range workQueue {
		func() {
			defer func() {
				_ = recover()
			}()
			d.started.Add(1)
			defer d.finished.Add(1)
			workerCtx := context.WithValue(job.ctx, workerEnqueueKey, true)
			if taskTimeout > 0 {
				var cancel context.CancelFunc
				workerCtx, cancel = context.WithTimeout(workerCtx, taskTimeout)
				defer cancel()
			}
			_ = d.runWithRetry(workerCtx, job.task, job.opts)
		}()
	}
}

func (d *localDispatcher) run(ctx context.Context, task Task) error {
	handler, ok := d.lookup(task.Type)
	if !ok {
		return fmt.Errorf("no handler registered for task type %q", task.Type)
	}
	return handler(ctx, task)
}

func (d *localDispatcher) runWithRetry(ctx context.Context, task Task, parsed enqueueOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	attempts := 1
	if parsed.maxRetry != nil && *parsed.maxRetry > 0 {
		attempts += *parsed.maxRetry
	}
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		lastErr = d.run(ctx, task)
		if lastErr == nil {
			return nil
		}
		if attempt == attempts {
			break
		}
		if parsed.backoff == nil || *parsed.backoff <= 0 {
			continue
		}
		timer := time.NewTimer(*parsed.backoff)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		}
	}
	return lastErr
}

func (d *localDispatcher) lookup(taskType string) (Handler, bool) {
	d.mu.RLock()
	handler, ok := d.handlers[taskType]
	d.mu.RUnlock()
	return handler, ok
}

func (d *localDispatcher) claimUnique(task Task, ttl time.Duration) bool {
	now := time.Now()
	key := task.Type + ":" + string(task.Payload)

	d.mu.Lock()
	defer d.mu.Unlock()

	for candidate, expiresAt := range d.unique {
		if expiresAt.Before(now) {
			delete(d.unique, candidate)
		}
	}
	if expiresAt, ok := d.unique[key]; ok && expiresAt.After(now) {
		return false
	}
	d.unique[key] = now.Add(ttl)
	return true
}

func waitGroupWithContext(ctx context.Context, waitGroup *sync.WaitGroup) error {
	done := make(chan struct{})
	go func() {
		waitGroup.Wait()
		close(done)
	}()
	if ctx == nil {
		<-done
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func allowEnqueueDuringShutdown(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	value := ctx.Value(workerEnqueueKey)
	allowed, _ := value.(bool)
	return allowed
}

func (d *localDispatcher) shutdownStats() string {
	d.queueMu.RLock()
	queued := 0
	capacity := 0
	if d.workQueue != nil {
		queued = len(d.workQueue)
		capacity = cap(d.workQueue)
	}
	d.queueMu.RUnlock()
	started := d.started.Load()
	finished := d.finished.Load()
	inFlight := started - finished
	return fmt.Sprintf(
		"enqueued=%d started=%d finished=%d inflight=%d delayed_pending=%d queue_len=%d queue_cap=%d",
		d.enqueued.Load(),
		started,
		finished,
		inFlight,
		d.delayed.Load(),
		queued,
		capacity,
	)
}
