package queue

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

type localQueue struct {
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
	opts taskOptions
}

func newLocalQueue(driver Driver) *localQueue {
	return newLocalQueueWithConfig(driver, WorkerpoolConfig{})
}

func newLocalQueueWithConfig(driver Driver, cfg WorkerpoolConfig) *localQueue {
	q := &localQueue{
		driver:     driver,
		cfg:        cfg.normalize(),
		handlers:   make(map[string]Handler),
		unique:     make(map[string]time.Time),
		shutdownCh: make(chan struct{}),
	}
	return q
}

// Driver returns the local queue runtime's driver mode.
// @group Queue
//
// Example: local driver
//
//	q, err := queue.New(queue.Config{Driver: queue.DriverSync})
//	if err != nil {
//		return
//	}
//	driverAware, ok := q.(interface{ Driver() queue.Driver })
//	if !ok {
//		return
//	}
//	fmt.Println(driverAware.Driver())
//	// Output: sync
func (d *localQueue) Driver() Driver {
	return d.driver
}

// Register adds a task handler to the local queue runtime.
// @group Queue
//
// Example: local register
//
//	q, err := queue.New(queue.Config{Driver: queue.DriverSync})
//	if err != nil {
//		return
//	}
//	type EmailPayload struct {
//		ID int `json:"id"`
//	}
//	q.Register("emails:send", func(ctx context.Context, task queue.Task) error {
//		var payload EmailPayload
//		if err := task.Bind(&payload); err != nil {
//			return err
//		}
//		_ = payload
//		return nil
//	})
func (d *localQueue) Register(taskType string, handler Handler) {
	if taskType == "" || handler == nil {
		return
	}
	d.mu.Lock()
	d.handlers[taskType] = handler
	d.mu.Unlock()
}

// Start initializes worker goroutines for workerpool mode.
// @group Queue
//
// Example: local start
//
//	q, err := queue.New(queue.Config{
//		Driver: queue.DriverWorkerpool,
//	})
//	if err != nil {
//		return
//	}
//	_ = q.Start(context.Background())
func (d *localQueue) Start(_ context.Context) error {
	if d.driver != DriverWorkerpool {
		return nil
	}
	d.startMemoryWorkers()
	return nil
}

// Shutdown drains delayed and active local workerpool tasks.
// @group Queue
//
// Example: local shutdown
//
//	q, err := queue.New(queue.Config{
//		Driver: queue.DriverWorkerpool,
//	})
//	if err != nil {
//		return
//	}
//	_ = q.Start(context.Background())
//	_ = q.Shutdown(context.Background())
func (d *localQueue) Shutdown(ctx context.Context) error {
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
// @group Queue
//
// Example: local enqueue
//
//	q, err := queue.New(queue.Config{Driver: queue.DriverSync})
//	if err != nil {
//		return
//	}
//	type EmailPayload struct {
//		ID int `json:"id"`
//	}
//	q.Register("emails:send", func(ctx context.Context, task queue.Task) error {
//		var payload EmailPayload
//		if err := task.Bind(&payload); err != nil {
//			return err
//		}
//		_ = payload
//		return nil
//	})
//	task := queue.NewTask("emails:send").
//		Payload(EmailPayload{ID: 1}).
//		OnQueue("default").
//		Delay(10 * time.Millisecond)
//	_ = q.Enqueue(context.Background(), task)
func (d *localQueue) Enqueue(ctx context.Context, task Task) error {
	if d.shuttingDown.Load() && !allowEnqueueDuringShutdown(ctx) {
		return ErrQueuerShuttingDown
	}
	if err := task.validate(); err != nil {
		return err
	}
	parsed := task.enqueueOptions()
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

func (d *localQueue) enqueueNow(ctx context.Context, task Task, parsed taskOptions) error {
	if _, ok := d.lookup(task.Type); !ok {
		return fmt.Errorf("no handler registered for task type %q", task.Type)
	}
	if d.driver == DriverWorkerpool {
		return d.enqueueAsync(ctx, task, parsed)
	}
	return d.runWithRetry(ctx, task, parsed)
}

func (d *localQueue) enqueueAsync(ctx context.Context, task Task, parsed taskOptions) error {
	if d.shuttingDown.Load() && !allowEnqueueDuringShutdown(ctx) {
		return ErrQueuerShuttingDown
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

func (d *localQueue) workerQueueForEnqueue() (chan queuedTask, error) {
	d.queueMu.RLock()
	workQueue := d.workQueue
	d.queueMu.RUnlock()
	if workQueue != nil {
		return workQueue, nil
	}

	// Self-heal: if the in-memory worker queue is unexpectedly nil while the
	// the queue runtime is active, rebuild workers so dispatch can continue.
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

func (d *localQueue) startMemoryWorkers() {
	d.queueMu.Lock()
	defer d.queueMu.Unlock()
	d.startMemoryWorkersLocked()
}

func (d *localQueue) startMemoryWorkersLocked() {
	if d.workQueue != nil {
		return
	}
	workers := d.cfg.Workers
	bufferSize := d.cfg.QueueCapacity
	d.workQueue = make(chan queuedTask, bufferSize)
	workQueue := d.workQueue
	for i := 0; i < workers; i++ {
		d.workerWG.Add(1)
		go d.worker(workQueue)
	}
}

func (d *localQueue) worker(workQueue <-chan queuedTask) {
	defer d.workerWG.Done()
	taskTimeout := d.cfg.DefaultTaskTimeout
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

func (d *localQueue) run(ctx context.Context, task Task) error {
	handler, ok := d.lookup(task.Type)
	if !ok {
		return fmt.Errorf("no handler registered for task type %q", task.Type)
	}
	return handler(ctx, task)
}

func (d *localQueue) runWithRetry(ctx context.Context, task Task, parsed taskOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if parsed.timeout != nil && *parsed.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *parsed.timeout)
		defer cancel()
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

func (d *localQueue) lookup(taskType string) (Handler, bool) {
	d.mu.RLock()
	handler, ok := d.handlers[taskType]
	d.mu.RUnlock()
	return handler, ok
}

func (d *localQueue) claimUnique(task Task, ttl time.Duration) bool {
	now := time.Now()
	key := task.Type + ":" + string(task.PayloadBytes())

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

func (d *localQueue) shutdownStats() string {
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
