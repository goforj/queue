package queue

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/goforj/queue/busruntime"
	"github.com/goforj/queue/internal/uniqueness"
)

// localQueue is an in-memory queue implementation supporting sync and workerpool drivers.
type localQueue struct {
	driver        Driver
	cfg           WorkerpoolConfig
	mu            sync.RWMutex
	metricsMu     sync.RWMutex
	queueMu       sync.RWMutex
	handlers      map[string]Handler
	unique        uniqueness.MemoryStore
	metrics       map[string]*localQueueMetrics
	pausedQueues  map[string]bool
	workQueue     chan queuedJob
	workPending   int
	workIdle      chan struct{}
	continuation  *busruntime.ContinuationScope
	resizeBuffer  bool
	shutdownOnce  sync.Once
	workerWG      sync.WaitGroup
	workerStateMu sync.Mutex
	workerPaused  bool
	workerActive  int
	workerResume  chan struct{}
	workerDrained chan struct{}

	syncWorkMu      sync.Mutex
	syncWorkPending int
	syncWorkIdle    chan struct{}

	shuttingDown atomic.Bool
	enqueued     atomic.Int64
	started      atomic.Int64
	finished     atomic.Int64
	delayed      atomic.Int64
}

const localRedeliveryBackoff = time.Millisecond

type queuedJob struct {
	ctx   context.Context
	job   Job
	opts  jobOptions
	ready <-chan struct{}
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
	resizeBuffer := cfg.QueueCapacity <= 0
	q := &localQueue{
		driver:       driver,
		cfg:          cfg.normalize(),
		handlers:     make(map[string]Handler),
		metrics:      make(map[string]*localQueueMetrics),
		pausedQueues: make(map[string]bool),
		continuation: busruntime.NewContinuationScope(),
		resizeBuffer: resizeBuffer,
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

// PauseWorkers prevents the in-memory pool from starting queued jobs and waits for active handlers.
func (d *localQueue) PauseWorkers(ctx context.Context) error {
	if d.driver != DriverWorkerpool {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	d.workerStateMu.Lock()
	if !d.workerPaused {
		d.workerPaused = true
		d.workerResume = make(chan struct{})
	}
	if d.workerActive == 0 {
		d.workerStateMu.Unlock()
		return nil
	}
	if d.workerDrained == nil {
		d.workerDrained = make(chan struct{})
	}
	drained := d.workerDrained
	d.workerStateMu.Unlock()
	select {
	case <-drained:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ResumeWorkers allows the in-memory pool to start queued jobs again.
func (d *localQueue) ResumeWorkers(_ context.Context) error {
	if d.driver != DriverWorkerpool {
		return nil
	}
	d.workerStateMu.Lock()
	d.resumeWorkersLocked()
	d.workerStateMu.Unlock()
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
	return d.DrainWorkers(ctx)
}

// DrainWorkers stops admission from unrelated callers and waits for the
// accepted local work tree to finish while handler continuations remain valid.
func (d *localQueue) DrainWorkers(ctx context.Context) error {
	if d.driver != DriverWorkerpool && d.driver != DriverSync {
		return nil
	}

	d.shutdownOnce.Do(func() {
		d.shuttingDown.Store(true)
		d.workerStateMu.Lock()
		d.resumeWorkersLocked()
		d.workerStateMu.Unlock()
	})

	if d.driver == DriverSync {
		if err := d.waitForSyncWork(ctx); err != nil {
			return fmt.Errorf("sync jobs drain failed: %w (%s)", err, d.shutdownStats())
		}
		return nil
	}

	if err := d.closeWorkerQueueWhenIdle(ctx); err != nil {
		return fmt.Errorf("workerpool queued jobs drain failed: %w (%s)", err, d.shutdownStats())
	}
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
	ctx, acceptance := ensureDispatchAcceptance(ctx)
	if d.shuttingDown.Load() && !d.continuation.Owns(ctx) {
		return ErrQueuerShuttingDown
	}
	if err := job.validate(); err != nil {
		return err
	}
	parsed := job.jobOptions()
	queueName := normalizeQueueName(parsed.queueName)
	if err := d.validateEnqueue(job, queueName); err != nil {
		return err
	}
	var (
		uniqueKey   string
		uniqueToken uint64
	)
	if parsed.uniqueTTL > 0 {
		var acquired bool
		uniqueKey, uniqueToken, acquired = d.claimUnique(job, queueName, parsed.uniqueTTL)
		if !acquired {
			return ErrDuplicate
		}
	}
	if parsed.delay <= 0 {
		if d.driver == DriverSync {
			if err := d.reserveSyncWork(ctx); err != nil {
				d.unique.Release(uniqueKey, uniqueToken)
				return err
			}
			defer d.finishSyncWork()
		}
		err := d.enqueueNow(ctx, job, parsed)
		if err != nil && !acceptance.isAccepted() {
			d.unique.Release(uniqueKey, uniqueToken)
		}
		return err
	}
	var reservedQueue chan queuedJob
	switch d.driver {
	case DriverWorkerpool:
		var reserveErr error
		reservedQueue, reserveErr = d.reserveWorkerQueue(ctx)
		if reserveErr != nil {
			d.unique.Release(uniqueKey, uniqueToken)
			return reserveErr
		}
	case DriverSync:
		if err := d.reserveSyncWork(ctx); err != nil {
			d.unique.Release(uniqueKey, uniqueToken)
			return err
		}
	}
	d.delayed.Add(1)
	d.updateQueueMetrics(queueName, func(metrics *localQueueMetrics) {
		metrics.Delayed++
	})
	if acceptance := dispatchAcceptanceFromContext(ctx); acceptance != nil {
		acceptance.markAccepted()
	}
	delayedCtx := context.WithoutCancel(ctx)
	go func() {
		if d.driver == DriverSync {
			defer d.finishSyncWork()
		}
		defer d.delayed.Add(-1)
		defer d.updateQueueMetrics(queueName, func(metrics *localQueueMetrics) {
			if metrics.Delayed > 0 {
				metrics.Delayed--
			}
		})
		timer := time.NewTimer(parsed.delay)
		defer timer.Stop()
		<-timer.C
		if d.driver == DriverWorkerpool {
			if err := d.enqueueReservedAsync(delayedCtx, job, parsed, reservedQueue, false); err != nil {
				d.finishQueuedWork()
			}
			return
		}
		_ = d.enqueueNow(delayedCtx, job, parsed)
	}()
	return nil
}

func (d *localQueue) enqueueNow(ctx context.Context, job Job, parsed jobOptions) error {
	queueName := normalizeQueueName(parsed.queueName)
	if err := d.validateEnqueue(job, queueName); err != nil {
		return err
	}
	if d.driver == DriverWorkerpool {
		return d.enqueueAsync(ctx, job, parsed)
	}
	if acceptance := dispatchAcceptanceFromContext(ctx); acceptance != nil {
		acceptance.markAccepted()
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
	if ctx == nil {
		ctx = context.Background()
	}
	workQueue, err := d.reserveWorkerQueue(ctx)
	if err != nil {
		return err
	}
	return d.enqueueReservedAsync(ctx, job, parsed, workQueue, true)
}

// enqueueReservedAsync accepts one already-reserved workerpool slot and preserves handler progress when bounded capacity is full.
func (d *localQueue) enqueueReservedAsync(ctx context.Context, job Job, parsed jobOptions, workQueue chan queuedJob, markAcceptance bool) error {
	var ready chan struct{}
	acceptance := dispatchAcceptanceFromContext(ctx)
	if markAcceptance && acceptance != nil {
		ready = make(chan struct{})
	}
	queued := queuedJob{ctx: ctx, job: job, opts: parsed, ready: ready}
	if d.continuation.Owns(ctx) {
		select {
		case workQueue <- queued:
			d.recordQueuedJob(parsed, acceptance, ready)
			return nil
		default:
			if ready != nil {
				acceptance.markAccepted()
				close(ready)
				queued.ready = nil
			}
			d.recordQueuedJob(parsed, nil, nil)
			go func() { workQueue <- queued }()
			return nil
		}
	}
	select {
	case workQueue <- queued:
		d.recordQueuedJob(parsed, acceptance, ready)
		return nil
	case <-ctx.Done():
		d.finishQueuedWork()
		return ctx.Err()
	}
}

// recordQueuedJob updates acceptance and metrics only after this in-memory backend owns the reserved work.
func (d *localQueue) recordQueuedJob(parsed jobOptions, acceptance *dispatchAcceptance, ready chan struct{}) {
	d.enqueued.Add(1)
	d.updateQueueMetrics(normalizeQueueName(parsed.queueName), func(metrics *localQueueMetrics) {
		metrics.Pending++
	})
	if ready != nil {
		defer close(ready)
		acceptance.markAccepted()
	}
}

// reserveWorkerQueue keeps the channel open until this queued or active job and all descendants finish.
func (d *localQueue) reserveWorkerQueue(ctx context.Context) (chan queuedJob, error) {
	d.queueMu.Lock()
	defer d.queueMu.Unlock()
	if d.shuttingDown.Load() && !d.continuation.Owns(ctx) {
		return nil, ErrQueuerShuttingDown
	}
	if d.workQueue == nil {
		if d.shuttingDown.Load() {
			return nil, ErrWorkerpoolQueueNotInitialized
		}
		d.startMemoryWorkersLocked()
	}
	if d.workQueue == nil {
		return nil, ErrWorkerpoolQueueNotInitialized
	}
	if d.workPending == 0 {
		d.workIdle = make(chan struct{})
	}
	d.workPending++
	return d.workQueue, nil
}

// reserveSyncWork keeps shutdown attached to the current Sync work generation, including descendants admitted by a live handler.
func (d *localQueue) reserveSyncWork(ctx context.Context) error {
	d.syncWorkMu.Lock()
	defer d.syncWorkMu.Unlock()
	if d.shuttingDown.Load() && !d.continuation.Owns(ctx) {
		return ErrQueuerShuttingDown
	}
	if d.syncWorkPending == 0 {
		d.syncWorkIdle = make(chan struct{})
	}
	d.syncWorkPending++
	return nil
}

// finishSyncWork releases one Sync job only after its handler can no longer admit descendants.
func (d *localQueue) finishSyncWork() {
	d.syncWorkMu.Lock()
	d.syncWorkPending--
	if d.syncWorkPending == 0 && d.syncWorkIdle != nil {
		close(d.syncWorkIdle)
	}
	d.syncWorkMu.Unlock()
}

// waitForSyncWork waits on the stable channel for the active Sync work generation without creating shutdown waiter goroutines.
func (d *localQueue) waitForSyncWork(ctx context.Context) error {
	d.syncWorkMu.Lock()
	if d.syncWorkPending == 0 {
		d.syncWorkMu.Unlock()
		return nil
	}
	idle := d.syncWorkIdle
	d.syncWorkMu.Unlock()
	if ctx == nil {
		<-idle
		return nil
	}
	select {
	case <-idle:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// finishQueuedWork releases one accepted workerpool job after its handler can no longer enqueue descendants.
func (d *localQueue) finishQueuedWork() {
	d.queueMu.Lock()
	d.workPending--
	if d.workPending == 0 && d.workIdle != nil {
		close(d.workIdle)
		d.workIdle = nil
	}
	d.queueMu.Unlock()
}

// closeWorkerQueueWhenIdle waits for the accepted work tree to quiesce before closing the worker channel.
func (d *localQueue) closeWorkerQueueWhenIdle(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		d.queueMu.Lock()
		if d.workPending == 0 {
			if d.workQueue != nil {
				close(d.workQueue)
				d.workQueue = nil
			}
			d.queueMu.Unlock()
			return nil
		}
		idle := d.workIdle
		d.queueMu.Unlock()
		select {
		case <-idle:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (d *localQueue) startMemoryWorkers() {
	d.queueMu.Lock()
	defer d.queueMu.Unlock()
	d.startMemoryWorkersLocked()
}

func (d *localQueue) startMemoryWorkersLocked() {
	if d.workQueue != nil || d.shuttingDown.Load() {
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

// setWorkers applies high-level worker configuration before the in-memory runtime starts.
func (d *localQueue) setWorkers(count int) {
	if count <= 0 {
		return
	}
	d.queueMu.Lock()
	defer d.queueMu.Unlock()
	if d.workQueue != nil || d.shuttingDown.Load() {
		return
	}
	d.cfg.Workers = count
	if d.resizeBuffer {
		d.cfg.QueueCapacity = count
	}
}

func (d *localQueue) worker(workQueue <-chan queuedJob) {
	defer d.workerWG.Done()
	jobTimeout := d.cfg.DefaultJobTimeout
	for job := range workQueue {
		d.beginWorkerExecution()
		func() {
			defer d.finishQueuedWork()
			defer d.endWorkerExecution()
			if job.ready != nil {
				<-job.ready
			}
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
			workerCtx := job.ctx
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

// beginWorkerExecution keeps accepted in-memory work behind the lifecycle gate until intake resumes or graceful shutdown drains it.
func (d *localQueue) beginWorkerExecution() {
	for {
		d.workerStateMu.Lock()
		if !d.workerPaused {
			d.workerActive++
			d.workerStateMu.Unlock()
			return
		}
		resume := d.workerResume
		d.workerStateMu.Unlock()
		<-resume
	}
}

// endWorkerExecution releases pause waiters after an admitted handler completes.
func (d *localQueue) endWorkerExecution() {
	d.workerStateMu.Lock()
	d.workerActive--
	if d.workerActive == 0 && d.workerDrained != nil {
		close(d.workerDrained)
		d.workerDrained = nil
	}
	d.workerStateMu.Unlock()
}

// resumeWorkersLocked releases workers reserved behind the lifecycle gate.
func (d *localQueue) resumeWorkersLocked() {
	if !d.workerPaused {
		return
	}
	d.workerPaused = false
	close(d.workerResume)
	d.workerResume = nil
}

// validateEnqueue rejects work before uniqueness is claimed or an acceptance fact is committed.
func (d *localQueue) validateEnqueue(job Job, queueName string) error {
	if d.isPaused(queueName) {
		return ErrQueuePaused
	}
	if _, ok := d.lookup(job.Type); !ok {
		return fmt.Errorf("no handler registered for job type %q", job.Type)
	}
	return nil
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
	maxRetry := 0
	if parsed.maxRetry != nil && *parsed.maxRetry > 0 {
		maxRetry = *parsed.maxRetry
	}
	jobForRun := job
	if parsed.maxRetry != nil {
		jobForRun = jobForRun.Retry(*parsed.maxRetry)
	}
	if parsed.queueName != "" {
		jobForRun = jobForRun.OnQueue(parsed.queueName)
	}
	for attempt := 0; ; {
		delivery := busruntime.DeliveryAttempt{Number: attempt, MaxRetry: maxRetry}
		attemptCtx := busruntime.WithDeliveryAttempt(ctx, delivery)
		attemptCtx, release := d.continuation.Permit(attemptCtx)
		err := func() error {
			defer release()
			return d.run(attemptCtx, jobForRun.withAttempt(attempt))
		}()
		switch busruntime.ClassifyAttempt(delivery, err) {
		case busruntime.AttemptSucceeded:
			return nil
		case busruntime.AttemptFailed:
			return err
		case busruntime.AttemptRetry:
			attempt++
			delay := time.Duration(0)
			if parsed.backoff != nil {
				delay = *parsed.backoff
			}
			if waitErr := waitForLocalRetry(ctx, delay); waitErr != nil {
				return waitErr
			}
		case busruntime.AttemptRedeliver:
			if waitErr := waitForLocalRetry(ctx, localRedeliveryBackoff); waitErr != nil {
				return waitErr
			}
		}
	}
}

// waitForLocalRetry keeps retry and redelivery waits cancellable while avoiding timers for immediate application retries.
func waitForLocalRetry(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
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

// claimUnique returns the ownership token needed to compensate a pre-acceptance failure.
func (d *localQueue) claimUnique(job Job, queueName string, ttl time.Duration) (string, uint64, bool) {
	key := DriverUniqueKey(job, queueName)
	token, ok := d.unique.Acquire(key, ttl)
	return key, token, ok
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
