package queue

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// localQueue is an in-memory queue implementation supporting sync and workerpool drivers.
type localQueue struct {
	driver       Driver
	cfg          WorkerpoolConfig
	mu           sync.RWMutex
	metricsMu    sync.RWMutex
	queueMu      sync.RWMutex
	handlers     map[string]Handler
	unique       map[string]time.Time
	metrics      map[string]*localQueueMetrics
	pausedQueues map[string]bool
	workQueue    chan queuedJob
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

type queuedJob struct {
	ctx  context.Context
	job  Job
	opts jobOptions
}

type localQueueMetrics struct {
	Pending   int64
	Active    int64
	Processed int64
	Failed    int64
	Delayed   int64
}

func newLocalQueue(driver Driver) *localQueue {
	return newLocalQueueWithConfig(driver, WorkerpoolConfig{})
}

func newLocalQueueWithConfig(driver Driver, cfg WorkerpoolConfig) *localQueue {
	q := &localQueue{
		driver:       driver,
		cfg:          cfg.normalize(),
		handlers:     make(map[string]Handler),
		unique:       make(map[string]time.Time),
		metrics:      make(map[string]*localQueueMetrics),
		pausedQueues: make(map[string]bool),
		shutdownCh:   make(chan struct{}),
	}
	return q
}

// Driver returns the active queue driver.
// @group Queue
//
// Example: local driver
//
//	q, err := queue.NewSync()
//	if err != nil {
//		return
//	}
//	fmt.Println(q.Driver())
//	// Output: sync
func (d *localQueue) Driver() Driver {
	return d.driver
}

// Register associates a handler with a job type.
// @group Queue
//
// Example: local register
//
//	q, err := queue.NewSync()
//	if err != nil {
//		return
//	}
//	type EmailPayload struct {
//		ID int `json:"id"`
//	}
//	q.Register("emails:send", func(ctx context.Context, m queue.Message) error {
//		var payload EmailPayload
//		if err := m.Bind(&payload); err != nil {
//			return err
//		}
//		_ = payload
//		return nil
//	})
func (d *localQueue) Register(jobType string, handler Handler) {
	if jobType == "" || handler == nil {
		return
	}
	d.mu.Lock()
	d.handlers[jobType] = handler
	d.mu.Unlock()
}

// StartWorkers starts worker execution.
// @group Queue
//
// Example: local start workers
//
//	q, err := queue.NewWorkerpool()
//	if err != nil {
//		return
//	}
//	_ = q.StartWorkers(context.Background())
func (d *localQueue) StartWorkers(_ context.Context) error {
	if d.driver != DriverWorkerpool {
		return nil
	}
	d.startMemoryWorkers()
	return nil
}

// Shutdown drains running work and releases resources.
// @group Queue
//
// Example: local shutdown
//
//	q, err := queue.NewWorkerpool()
//	if err != nil {
//		return
//	}
//	_ = q.StartWorkers(context.Background())
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
		return fmt.Errorf("workerpool delayed jobs drain failed: %w (%s)", err, d.shutdownStats())
	}

	d.queueMu.Lock()
	if d.workQueue != nil {
		close(d.workQueue)
		d.workQueue = nil
	}
	d.queueMu.Unlock()

	if err := waitGroupWithContext(ctx, &d.workerWG); err != nil {
		return fmt.Errorf("workerpool active jobs drain failed: %w (%s)", err, d.shutdownStats())
	}
	return nil
}

func (d *localQueue) Ready(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

// Dispatch submits a typed job payload using the default queue.
// @group Queue
//
// Example: local dispatch
//
//	q, err := queue.NewSync()
//	if err != nil {
//		return
//	}
//	type EmailPayload struct {
//		ID int `json:"id"`
//	}
//	q.Register("emails:send", func(ctx context.Context, m queue.Message) error {
//		var payload EmailPayload
//		if err := m.Bind(&payload); err != nil {
//			return err
//		}
//		_ = payload
//		return nil
//	})
//	job := queue.NewJob("emails:send").
//		Payload(EmailPayload{ID: 1}).
//		OnQueue("default").
//		Delay(10 * time.Millisecond)
//	_, _ = q.Dispatch(job)
func (d *localQueue) Dispatch(ctx context.Context, job Job) error {
	if d.shuttingDown.Load() && !allowEnqueueDuringShutdown(ctx) {
		return ErrQueuerShuttingDown
	}
	if err := job.validate(); err != nil {
		return err
	}
	parsed := job.jobOptions()
	queueName := normalizeQueueName(parsed.queueName)
	if parsed.uniqueTTL > 0 {
		if !d.claimUnique(job, queueName, parsed.uniqueTTL) {
			return ErrDuplicate
		}
	}
	if parsed.delay <= 0 {
		return d.enqueueNow(ctx, job, parsed)
	}
	d.delayedWG.Add(1)
	d.delayed.Add(1)
	d.updateQueueMetrics(queueName, func(metrics *localQueueMetrics) {
		metrics.Delayed++
	})
	go func() {
		defer d.delayedWG.Done()
		defer d.delayed.Add(-1)
		defer d.updateQueueMetrics(queueName, func(metrics *localQueueMetrics) {
			if metrics.Delayed > 0 {
				metrics.Delayed--
			}
		})
		timer := time.NewTimer(parsed.delay)
		defer timer.Stop()
		select {
		case <-timer.C:
			_ = d.enqueueNow(context.Background(), job, parsed)
		case <-d.shutdownCh:
			return
		}
	}()
	return nil
}

func (d *localQueue) enqueueNow(ctx context.Context, job Job, parsed jobOptions) error {
	queueName := normalizeQueueName(parsed.queueName)
	if d.isPaused(queueName) {
		return ErrQueuePaused
	}
	if _, ok := d.lookup(job.Type); !ok {
		return fmt.Errorf("no handler registered for job type %q", job.Type)
	}
	if d.driver == DriverWorkerpool {
		return d.enqueueAsync(ctx, job, parsed)
	}
	d.updateQueueMetrics(queueName, func(metrics *localQueueMetrics) {
		metrics.Active++
	})
	err := d.runWithRetry(ctx, job, parsed)
	d.updateQueueMetrics(queueName, func(metrics *localQueueMetrics) {
		if metrics.Active > 0 {
			metrics.Active--
		}
		if err == nil {
			metrics.Processed++
			return
		}
		metrics.Failed++
	})
	return err
}

func (d *localQueue) enqueueAsync(ctx context.Context, job Job, parsed jobOptions) error {
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
	case workQueue <- queuedJob{ctx: ctx, job: job, opts: parsed}:
		d.enqueued.Add(1)
		d.updateQueueMetrics(normalizeQueueName(parsed.queueName), func(metrics *localQueueMetrics) {
			metrics.Pending++
		})
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (d *localQueue) workerQueueForEnqueue() (chan queuedJob, error) {
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
	d.workQueue = make(chan queuedJob, bufferSize)
	workQueue := d.workQueue
	for i := 0; i < workers; i++ {
		d.workerWG.Add(1)
		go d.worker(workQueue)
	}
}

func (d *localQueue) worker(workQueue <-chan queuedJob) {
	defer d.workerWG.Done()
	jobTimeout := d.cfg.DefaultJobTimeout
	for job := range workQueue {
		func() {
			d.started.Add(1)
			defer d.finished.Add(1)
			queueName := normalizeQueueName(job.opts.queueName)
			d.updateQueueMetrics(queueName, func(metrics *localQueueMetrics) {
				if metrics.Pending > 0 {
					metrics.Pending--
				}
				metrics.Active++
			})
			var runErr error
			defer d.updateQueueMetrics(queueName, func(metrics *localQueueMetrics) {
				if metrics.Active > 0 {
					metrics.Active--
				}
				if runErr == nil {
					metrics.Processed++
					return
				}
				metrics.Failed++
			})
			workerCtx := context.WithValue(job.ctx, workerEnqueueKey, true)
			if jobTimeout > 0 {
				var cancel context.CancelFunc
				workerCtx, cancel = context.WithTimeout(workerCtx, jobTimeout)
				defer cancel()
			}
			func() {
				defer func() {
					if recovered := recover(); recovered != nil {
						runErr = fmt.Errorf("panic: %v", recovered)
					}
				}()
				runErr = d.runWithRetry(workerCtx, job.job, job.opts)
			}()
		}()
	}
}

func (d *localQueue) run(ctx context.Context, job Job) error {
	handler, ok := d.lookup(job.Type)
	if !ok {
		return fmt.Errorf("no handler registered for job type %q", job.Type)
	}
	return handler(ctx, job)
}

func (d *localQueue) runWithRetry(ctx context.Context, job Job, parsed jobOptions) error {
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
	jobForRun := job
	if parsed.maxRetry != nil {
		jobForRun = jobForRun.Retry(*parsed.maxRetry)
	}
	if parsed.queueName != "" {
		jobForRun = jobForRun.OnQueue(parsed.queueName)
	}
	for attempt := 1; attempt <= attempts; attempt++ {
		lastErr = d.run(ctx, jobForRun.withAttempt(attempt-1))
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

func (d *localQueue) lookup(jobType string) (Handler, bool) {
	d.mu.RLock()
	handler, ok := d.handlers[jobType]
	d.mu.RUnlock()
	return handler, ok
}

func (d *localQueue) Pause(_ context.Context, queueName string) error {
	d.mu.Lock()
	d.pausedQueues[normalizeQueueName(queueName)] = true
	d.mu.Unlock()
	return nil
}

func (d *localQueue) Resume(_ context.Context, queueName string) error {
	d.mu.Lock()
	delete(d.pausedQueues, normalizeQueueName(queueName))
	d.mu.Unlock()
	return nil
}

func (d *localQueue) isPaused(queueName string) bool {
	d.mu.RLock()
	paused := d.pausedQueues[normalizeQueueName(queueName)]
	d.mu.RUnlock()
	return paused
}

func (d *localQueue) Stats(_ context.Context) (StatsSnapshot, error) {
	metricsByQueue := d.snapshotQueueMetrics()
	d.mu.RLock()
	pausedQueues := make(map[string]bool, len(d.pausedQueues))
	for queueName, paused := range d.pausedQueues {
		pausedQueues[queueName] = paused
	}
	d.mu.RUnlock()

	byQueue := make(map[string]QueueCounters, len(metricsByQueue))
	throughputByQueue := make(map[string]QueueThroughput, len(metricsByQueue))
	for queueName, metrics := range metricsByQueue {
		counters := QueueCounters{
			Pending:   metrics.Pending + metrics.Delayed,
			Active:    metrics.Active,
			Processed: metrics.Processed,
			Failed:    metrics.Failed,
			Paused:    boolToInt64(pausedQueues[queueName]),
		}
		byQueue[queueName] = counters
		throughputByQueue[queueName] = QueueThroughput{}
		delete(pausedQueues, queueName)
	}
	for queueName, paused := range pausedQueues {
		byQueue[queueName] = QueueCounters{Paused: boolToInt64(paused)}
		throughputByQueue[queueName] = QueueThroughput{}
	}
	if len(byQueue) == 0 {
		byQueue["default"] = QueueCounters{}
		throughputByQueue["default"] = QueueThroughput{}
	}

	return StatsSnapshot{
		ByQueue:           byQueue,
		ThroughputByQueue: throughputByQueue,
	}, nil
}

func (d *localQueue) History(ctx context.Context, queueName string, window QueueHistoryWindow) ([]QueueHistoryPoint, error) {
	snapshot, err := d.Stats(ctx)
	if err != nil {
		return nil, err
	}
	points := TimelineHistoryFromSnapshot(snapshot, queueName, window)
	if len(points) > 0 {
		return points, nil
	}
	return SinglePointHistory(snapshot, queueName), nil
}

func (d *localQueue) claimUnique(job Job, queueName string, ttl time.Duration) bool {
	now := time.Now()
	key := queueName + ":" + job.Type + ":" + string(job.PayloadBytes())

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

func (d *localQueue) updateQueueMetrics(queueName string, update func(metrics *localQueueMetrics)) {
	if update == nil {
		return
	}
	name := normalizeQueueName(queueName)
	d.metricsMu.Lock()
	metrics, ok := d.metrics[name]
	if !ok {
		metrics = &localQueueMetrics{}
		d.metrics[name] = metrics
	}
	update(metrics)
	d.metricsMu.Unlock()
}

func (d *localQueue) snapshotQueueMetrics() map[string]localQueueMetrics {
	d.metricsMu.RLock()
	defer d.metricsMu.RUnlock()
	out := make(map[string]localQueueMetrics, len(d.metrics))
	for queueName, metrics := range d.metrics {
		if metrics == nil {
			continue
		}
		out[queueName] = *metrics
	}
	return out
}

func boolToInt64(v bool) int64 {
	if v {
		return 1
	}
	return 0
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
