package queue

import (
	"context"
	"crypto/sha1"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// EventKind identifies a queue runtime event.
// @group Driver Integration
type EventKind string

const (
	// EventDispatchStarted indicates workflow dispatch began.
	EventDispatchStarted EventKind = "dispatch_started"
	// EventDispatchSucceeded indicates workflow dispatch completed successfully.
	EventDispatchSucceeded EventKind = "dispatch_succeeded"
	// EventDispatchFailed indicates workflow dispatch failed before handler execution.
	EventDispatchFailed EventKind = "dispatch_failed"
	// EventEnqueueAccepted indicates a job was accepted for enqueue.
	EventEnqueueAccepted EventKind = "enqueue_accepted"
	// EventEnqueueRejected indicates enqueue failed.
	EventEnqueueRejected EventKind = "enqueue_rejected"
	// EventEnqueueDuplicate indicates enqueue was rejected as duplicate.
	EventEnqueueDuplicate EventKind = "enqueue_duplicate"
	// EventEnqueueCanceled indicates enqueue was canceled by context.
	EventEnqueueCanceled EventKind = "enqueue_canceled"
	// EventProcessStarted indicates a handler started processing.
	EventProcessStarted EventKind = "process_started"
	// EventProcessSucceeded indicates a handler completed successfully.
	EventProcessSucceeded EventKind = "process_succeeded"
	// EventProcessFailed indicates a handler returned an error.
	EventProcessFailed EventKind = "process_failed"
	// EventProcessRetried indicates a failed attempt was requeued for retry.
	EventProcessRetried EventKind = "process_retried"
	// EventProcessArchived indicates a failed attempt reached terminal state.
	EventProcessArchived EventKind = "process_archived"
	// EventQueuePaused indicates queue consumption was paused.
	EventQueuePaused EventKind = "queue_paused"
	// EventQueueResumed indicates queue consumption was resumed.
	EventQueueResumed EventKind = "queue_resumed"
	// EventProcessRecovered indicates a stale in-flight job was requeued for recovery.
	EventProcessRecovered EventKind = "process_recovered"
	// EventRepublishFailed indicates an internal delay/retry republish attempt failed.
	EventRepublishFailed EventKind = "republish_failed"
)

// Event is emitted through Observer hooks for queue/worker activity.
// @group Driver Integration
type Event struct {
	Kind      EventKind
	Driver    Driver
	Queue     string
	JobType   string
	JobKey    string
	Attempt   int
	MaxRetry  int
	Scheduled bool
	Duration  time.Duration
	Err       error
	Time      time.Time
}

// Observer receives queue runtime events.
// @group Observability
type Observer interface {
	// Observe handles a queue runtime event.
	// @group Observability
	//
	// Example: observe runtime event
	//
	//	var observer queue.Observer
	//	observer.Observe(context.Background(), queue.Event{
	//		Kind:   queue.EventEnqueueAccepted,
	//		Driver: queue.DriverSync,
	//		Queue:  "default",
	//	})
	Observe(ctx context.Context, event Event)
}

// ObserverFunc adapts a function to an Observer.
// @group Observability
type ObserverFunc func(ctx context.Context, event Event)

// Observe calls the wrapped function.
// @group Observability
//
// Example: observer func logging hook
//
//	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
//	observer := queue.ObserverFunc(func(ctx context.Context, event queue.Event) {
//		logger.Info("queue event",
//			"kind", event.Kind,
//			"driver", event.Driver,
//			"queue", event.Queue,
//			"job_type", event.JobType,
//			"attempt", event.Attempt,
//			"max_retry", event.MaxRetry,
//			"duration", event.Duration,
//			"err", event.Err,
//		)
//	})
//	observer.Observe(context.Background(), queue.Event{
//		Kind:     queue.EventProcessSucceeded,
//		Driver:   queue.DriverSync,
//		Queue:    "default",
//		JobType: "emails:send",
//	})
func (f ObserverFunc) Observe(ctx context.Context, event Event) {
	f(ctx, event)
}

type multiObserver struct {
	observers []Observer
}

// MultiObserver fans out events to multiple observers.
// @group Observability
//
// Example: fan out to multiple observers
//
//	events := make(chan queue.Event, 2)
//	observer := queue.MultiObserver(
//		queue.ChannelObserver{Events: events},
//		queue.ObserverFunc(func(context.Context, queue.Event) {}),
//	)
//	observer.Observe(context.Background(), queue.Event{Kind: queue.EventEnqueueAccepted})
//	fmt.Println(len(events))
//	// Output: 1
func MultiObserver(observers ...Observer) Observer {
	filtered := make([]Observer, 0, len(observers))
	for _, observer := range observers {
		if observer != nil {
			filtered = append(filtered, observer)
		}
	}
	return &multiObserver{observers: filtered}
}

func (m *multiObserver) Observe(ctx context.Context, event Event) {
	for _, observer := range m.observers {
		safeObserve(ctx, observer, event)
	}
}

// ChannelObserver forwards events to a channel.
// @group Observability
type ChannelObserver struct {
	Events     chan<- Event
	DropIfFull bool
}

// Observe forwards an event to the configured channel.
// @group Observability
//
// Example: channel observer
//
//	ch := make(chan queue.Event, 1)
//	observer := queue.ChannelObserver{Events: ch}
//	observer.Observe(context.Background(), queue.Event{Kind: queue.EventProcessStarted, Queue: "default"})
//	event := <-ch
//	_ = event
func (c ChannelObserver) Observe(_ context.Context, event Event) {
	if c.Events == nil {
		return
	}
	if c.DropIfFull {
		select {
		case c.Events <- event:
		default:
		}
		return
	}
	c.Events <- event
}

// StatsProvider exposes driver-native queue snapshots.
// @group Observability
type StatsProvider interface {
	Stats(ctx context.Context) (StatsSnapshot, error)
}

// QueueController exposes queue pause/resume controls.
// @group Observability
type QueueController interface {
	Pause(ctx context.Context, queueName string) error
	Resume(ctx context.Context, queueName string) error
}

// ErrPauseUnsupported indicates queue pause/resume is unsupported by a driver.
var ErrPauseUnsupported = errors.New("pause/resume is not supported by this driver")

// QueueCounters exposes normalized queue counters collected from events.
// @group Observability
type QueueCounters struct {
	Pending   int64
	Active    int64
	Scheduled int64
	Retry     int64
	Archived  int64
	Processed int64
	Failed    int64
	Paused    int64
	AvgWait   time.Duration
	AvgRun    time.Duration
}

// ThroughputWindow captures processed vs failed counts in a fixed window.
// @group Observability
type ThroughputWindow struct {
	Processed int64
	Failed    int64
}

// QueueThroughput contains rolling throughput windows for a queue.
// @group Observability
type QueueThroughput struct {
	Hour ThroughputWindow
	Day  ThroughputWindow
	Week ThroughputWindow
}

// StatsSnapshot is a point-in-time view of counters by queue.
// @group Observability
type StatsSnapshot struct {
	ByQueue           map[string]QueueCounters
	ThroughputByQueue map[string]QueueThroughput
}

// Queue returns queue counters for a queue name.
// @group Observability
//
// Example: queue counters getter
//
//	collector := queue.NewStatsCollector()
//	collector.Observe(context.Background(), queue.Event{
//		Kind:   queue.EventEnqueueAccepted,
//		Driver: queue.DriverSync,
//		Queue:  "default",
//		Time:   time.Now(),
//	})
//	snapshot := collector.Snapshot()
//	counters, ok := snapshot.Queue("default")
//	fmt.Println(ok, counters.Pending)
//	// Output: true 1
func (s StatsSnapshot) Queue(name string) (QueueCounters, bool) {
	if s.ByQueue == nil {
		return QueueCounters{}, false
	}
	counters, ok := s.ByQueue[name]
	return counters, ok
}

// Throughput returns rolling throughput windows for a queue name.
// @group Observability
//
// Example: throughput getter
//
//	collector := queue.NewStatsCollector()
//	collector.Observe(context.Background(), queue.Event{
//		Kind:   queue.EventProcessSucceeded,
//		Driver: queue.DriverSync,
//		Queue:  "default",
//		Time:   time.Now(),
//	})
//	snapshot := collector.Snapshot()
//	throughput, ok := snapshot.Throughput("default")
//	fmt.Printf("ok=%v hour=%+v day=%+v week=%+v\n", ok, throughput.Hour, throughput.Day, throughput.Week)
//	// Output: ok=true hour={Processed:1 Failed:0} day={Processed:1 Failed:0} week={Processed:1 Failed:0}
func (s StatsSnapshot) Throughput(name string) (QueueThroughput, bool) {
	if s.ThroughputByQueue == nil {
		return QueueThroughput{}, false
	}
	throughput, ok := s.ThroughputByQueue[name]
	return throughput, ok
}

// Queues returns sorted queue names present in the snapshot.
// @group Observability
//
// Example: list queues
//
//	collector := queue.NewStatsCollector()
//	collector.Observe(context.Background(), queue.Event{
//		Kind:   queue.EventEnqueueAccepted,
//		Driver: queue.DriverSync,
//		Queue:  "critical",
//		Time:   time.Now(),
//	})
//	snapshot := collector.Snapshot()
//	names := snapshot.Queues()
//	fmt.Println(len(names), names[0])
//	// Output: 1 critical
func (s StatsSnapshot) Queues() []string {
	if len(s.ByQueue) == 0 {
		return nil
	}
	names := make([]string, 0, len(s.ByQueue))
	for name := range s.ByQueue {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Pending returns pending count for a queue.
// @group Observability
//
// Example: pending count getter
//
//	snapshot := queue.StatsSnapshot{
//		ByQueue: map[string]queue.QueueCounters{
//			"default": {Pending: 3},
//		},
//	}
//	fmt.Println(snapshot.Pending("default"))
//	// Output: 3
func (s StatsSnapshot) Pending(name string) int64 {
	counters, ok := s.Queue(name)
	if !ok {
		return 0
	}
	return counters.Pending
}

// Active returns active count for a queue.
// @group Observability
//
// Example: active count getter
//
//	snapshot := queue.StatsSnapshot{
//		ByQueue: map[string]queue.QueueCounters{
//			"default": {Active: 2},
//		},
//	}
//	fmt.Println(snapshot.Active("default"))
//	// Output: 2
func (s StatsSnapshot) Active(name string) int64 {
	counters, ok := s.Queue(name)
	if !ok {
		return 0
	}
	return counters.Active
}

// Scheduled returns scheduled count for a queue.
// @group Observability
//
// Example: scheduled count getter
//
//	snapshot := queue.StatsSnapshot{
//		ByQueue: map[string]queue.QueueCounters{
//			"default": {Scheduled: 4},
//		},
//	}
//	fmt.Println(snapshot.Scheduled("default"))
//	// Output: 4
func (s StatsSnapshot) Scheduled(name string) int64 {
	counters, ok := s.Queue(name)
	if !ok {
		return 0
	}
	return counters.Scheduled
}

// RetryCount returns retry count for a queue.
// @group Observability
//
// Example: retry count getter
//
//	snapshot := queue.StatsSnapshot{
//		ByQueue: map[string]queue.QueueCounters{
//			"default": {Retry: 1},
//		},
//	}
//	fmt.Println(snapshot.RetryCount("default"))
//	// Output: 1
func (s StatsSnapshot) RetryCount(name string) int64 {
	counters, ok := s.Queue(name)
	if !ok {
		return 0
	}
	return counters.Retry
}

// Archived returns archived count for a queue.
// @group Observability
//
// Example: archived count getter
//
//	snapshot := queue.StatsSnapshot{
//		ByQueue: map[string]queue.QueueCounters{
//			"default": {Archived: 7},
//		},
//	}
//	fmt.Println(snapshot.Archived("default"))
//	// Output: 7
func (s StatsSnapshot) Archived(name string) int64 {
	counters, ok := s.Queue(name)
	if !ok {
		return 0
	}
	return counters.Archived
}

// Processed returns processed count for a queue.
// @group Observability
//
// Example: processed count getter
//
//	snapshot := queue.StatsSnapshot{
//		ByQueue: map[string]queue.QueueCounters{
//			"default": {Processed: 11},
//		},
//	}
//	fmt.Println(snapshot.Processed("default"))
//	// Output: 11
func (s StatsSnapshot) Processed(name string) int64 {
	counters, ok := s.Queue(name)
	if !ok {
		return 0
	}
	return counters.Processed
}

// Failed returns failed count for a queue.
// @group Observability
//
// Example: failed count getter
//
//	snapshot := queue.StatsSnapshot{
//		ByQueue: map[string]queue.QueueCounters{
//			"default": {Failed: 2},
//		},
//	}
//	fmt.Println(snapshot.Failed("default"))
//	// Output: 2
func (s StatsSnapshot) Failed(name string) int64 {
	counters, ok := s.Queue(name)
	if !ok {
		return 0
	}
	return counters.Failed
}

// Paused returns paused count for a queue.
// @group Observability
//
// Example: paused count getter
//
//	collector := queue.NewStatsCollector()
//	collector.Observe(context.Background(), queue.Event{
//		Kind:   queue.EventQueuePaused,
//		Driver: queue.DriverSync,
//		Queue:  "default",
//		Time:   time.Now(),
//	})
//	snapshot := collector.Snapshot()
//	fmt.Println(snapshot.Paused("default"))
//	// Output: 1
func (s StatsSnapshot) Paused(name string) int64 {
	counters, ok := s.Queue(name)
	if !ok {
		return 0
	}
	return counters.Paused
}

// StatsCollector aggregates normalized counters from Observer events.
// @group Observability
type StatsCollector struct {
	mu      sync.RWMutex
	byQueue map[string]*collectorQueueState
}

type collectorQueueState struct {
	counters     QueueCounters
	processedAt  []time.Time
	failedAt     []time.Time
	pendingByKey map[string][]time.Time
	waitSum      time.Duration
	waitCount    int64
	runSum       time.Duration
	runCount     int64
}

// NewStatsCollector creates an event collector for queue counters.
// @group Constructors
//
// Example: new stats collector
//
//	collector := queue.NewStatsCollector()
//	_ = collector
func NewStatsCollector() *StatsCollector {
	return &StatsCollector{byQueue: make(map[string]*collectorQueueState)}
}

// Observe records an event and updates normalized counters.
// @group Observability
//
// Example: observe event
//
//	collector := queue.NewStatsCollector()
//	collector.Observe(context.Background(), queue.Event{
//		Kind:   queue.EventEnqueueAccepted,
//		Driver: queue.DriverSync,
//		Queue:  "default",
//		Time:   time.Now(),
//	})
func (c *StatsCollector) Observe(_ context.Context, event Event) {
	queue := event.Queue
	if queue == "" {
		queue = "default"
	}
	now := event.Time
	if now.IsZero() {
		now = time.Now()
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	state := c.byQueue[queue]
	if state == nil {
		state = &collectorQueueState{pendingByKey: make(map[string][]time.Time)}
		c.byQueue[queue] = state
	}

	switch event.Kind {
	case EventEnqueueAccepted:
		state.counters.Pending++
		if event.Scheduled {
			state.counters.Scheduled++
		}
		if event.JobKey != "" {
			state.pendingByKey[event.JobKey] = append(state.pendingByKey[event.JobKey], now)
		}
	case EventEnqueueRejected, EventEnqueueCanceled:
		// no queue-depth mutation
	case EventEnqueueDuplicate:
		// no queue-depth mutation
	case EventProcessStarted:
		if state.counters.Pending > 0 {
			state.counters.Pending--
		}
		if state.counters.Scheduled > 0 && event.Scheduled {
			state.counters.Scheduled--
		}
		state.counters.Active++
		if event.JobKey != "" {
			if entries, ok := state.pendingByKey[event.JobKey]; ok && len(entries) > 0 {
				enqueuedAt := entries[0]
				rest := entries[1:]
				if len(rest) == 0 {
					delete(state.pendingByKey, event.JobKey)
				} else {
					state.pendingByKey[event.JobKey] = rest
				}
				if now.After(enqueuedAt) {
					state.waitSum += now.Sub(enqueuedAt)
					state.waitCount++
					state.counters.AvgWait = state.waitSum / time.Duration(state.waitCount)
				}
			}
		}
	case EventProcessRetried:
		state.counters.Retry++
	case EventProcessArchived:
		state.counters.Archived++
	case EventQueuePaused:
		state.counters.Paused++
	case EventQueueResumed:
		if state.counters.Paused > 0 {
			state.counters.Paused--
		}
	case EventProcessSucceeded:
		if state.counters.Active > 0 {
			state.counters.Active--
		}
		state.counters.Processed++
		state.processedAt = append(state.processedAt, now)
		state.runSum += event.Duration
		state.runCount++
		state.counters.AvgRun = state.runSum / time.Duration(state.runCount)
	case EventProcessFailed:
		if state.counters.Active > 0 {
			state.counters.Active--
		}
		state.counters.Failed++
		state.failedAt = append(state.failedAt, now)
		state.runSum += event.Duration
		state.runCount++
		state.counters.AvgRun = state.runSum / time.Duration(state.runCount)
	}

	c.pruneThroughputLocked(state, now)
}

// Snapshot returns a copy of collected counters.
// @group Observability
//
// Example: snapshot print
//
//	collector := queue.NewStatsCollector()
//	collector.Observe(context.Background(), queue.Event{
//		Kind:   queue.EventEnqueueAccepted,
//		Driver: queue.DriverSync,
//		Queue:  "default",
//		Time:   time.Now(),
//	})
//	collector.Observe(context.Background(), queue.Event{
//		Kind:   queue.EventProcessStarted,
//		Driver: queue.DriverSync,
//		Queue:  "default",
//		JobKey: "job-1",
//		Time:   time.Now(),
//	})
//	collector.Observe(context.Background(), queue.Event{
//		Kind:     queue.EventProcessSucceeded,
//		Driver:   queue.DriverSync,
//		Queue:    "default",
//		JobKey:  "job-1",
//		Duration: 12 * time.Millisecond,
//		Time:     time.Now(),
//	})
//	snapshot := collector.Snapshot()
//	counters, _ := snapshot.Queue("default")
//	throughput, _ := snapshot.Throughput("default")
//	fmt.Printf("queues=%v\n", snapshot.Queues())
//	fmt.Printf("counters=%+v\n", counters)
//	fmt.Printf("hour=%+v\n", throughput.Hour)
//	// Output:
//	// queues=[default]
//	// counters={Pending:0 Active:0 Scheduled:0 Retry:0 Archived:0 Processed:1 Failed:0 Paused:0 AvgWait:0s AvgRun:12ms}
//	// hour={Processed:1 Failed:0}
func (c *StatsCollector) Snapshot() StatsSnapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()

	now := time.Now()
	out := make(map[string]QueueCounters, len(c.byQueue))
	throughput := make(map[string]QueueThroughput, len(c.byQueue))
	for queueName, state := range c.byQueue {
		out[queueName] = state.counters
		throughput[queueName] = QueueThroughput{
			Hour: ThroughputWindow{
				Processed: countSince(state.processedAt, now.Add(-1*time.Hour)),
				Failed:    countSince(state.failedAt, now.Add(-1*time.Hour)),
			},
			Day: ThroughputWindow{
				Processed: countSince(state.processedAt, now.Add(-24*time.Hour)),
				Failed:    countSince(state.failedAt, now.Add(-24*time.Hour)),
			},
			Week: ThroughputWindow{
				Processed: countSince(state.processedAt, now.Add(-7*24*time.Hour)),
				Failed:    countSince(state.failedAt, now.Add(-7*24*time.Hour)),
			},
		}
	}
	return StatsSnapshot{ByQueue: out, ThroughputByQueue: throughput}
}

func (c *StatsCollector) pruneThroughputLocked(state *collectorQueueState, now time.Time) {
	cutoff := now.Add(-7 * 24 * time.Hour)
	state.processedAt = pruneBefore(state.processedAt, cutoff)
	state.failedAt = pruneBefore(state.failedAt, cutoff)
}

func pruneBefore(in []time.Time, cutoff time.Time) []time.Time {
	idx := 0
	for idx < len(in) && in[idx].Before(cutoff) {
		idx++
	}
	if idx == 0 {
		return in
	}
	out := make([]time.Time, len(in)-idx)
	copy(out, in[idx:])
	return out
}

func countSince(in []time.Time, cutoff time.Time) int64 {
	var total int64
	for _, ts := range in {
		if !ts.Before(cutoff) {
			total++
		}
	}
	return total
}

type observedQueue struct {
	inner    queueBackend
	driver   Driver
	observer Observer
}

func newObservedQueue(inner queueBackend, driver Driver, observer Observer) queueBackend {
	if observer == nil {
		return inner
	}
	return &observedQueue{
		inner:    inner,
		driver:   driver,
		observer: observer,
	}
}

func (q *observedQueue) StartWorkers(ctx context.Context) error {
	runtime, ok := q.inner.(interface {
		StartWorkers(context.Context) error
	})
	if !ok {
		return nil
	}
	return runtime.StartWorkers(ctx)
}

func (q *observedQueue) Shutdown(ctx context.Context) error {
	return q.inner.Shutdown(ctx)
}

func (q *observedQueue) Stats(ctx context.Context) (StatsSnapshot, error) {
	provider, ok := q.inner.(StatsProvider)
	if !ok {
		return StatsSnapshot{}, fmt.Errorf("stats provider is not available for driver %q", q.driver)
	}
	return provider.Stats(ctx)
}

func (q *observedQueue) Ready(ctx context.Context) error {
	checker, ok := q.inner.(interface{ Ready(context.Context) error })
	if !ok {
		legacy, ok := q.inner.(interface{ Preflight(context.Context) error })
		if !ok {
			return nil
		}
		return legacy.Preflight(ctx)
	}
	return checker.Ready(ctx)
}

func (q *observedQueue) Pause(ctx context.Context, queueName string) error {
	controller, ok := q.inner.(QueueController)
	if !ok {
		return ErrPauseUnsupported
	}
	if err := controller.Pause(ctx, queueName); err != nil {
		return err
	}
	safeObserve(ctx, q.observer, Event{
		Kind:   EventQueuePaused,
		Driver: q.driver,
		Queue:  normalizeQueueName(queueName),
		Time:   time.Now(),
	})
	return nil
}

func (q *observedQueue) Resume(ctx context.Context, queueName string) error {
	controller, ok := q.inner.(QueueController)
	if !ok {
		return ErrPauseUnsupported
	}
	if err := controller.Resume(ctx, queueName); err != nil {
		return err
	}
	safeObserve(ctx, q.observer, Event{
		Kind:   EventQueueResumed,
		Driver: q.driver,
		Queue:  normalizeQueueName(queueName),
		Time:   time.Now(),
	})
	return nil
}

func (q *observedQueue) ListJobs(ctx context.Context, opts ListJobsOptions) (ListJobsResult, error) {
	admin, ok := q.inner.(QueueAdmin)
	if !ok {
		return ListJobsResult{}, ErrQueueAdminUnsupported
	}
	return admin.ListJobs(ctx, opts)
}

func (q *observedQueue) RetryJob(ctx context.Context, queueName, jobID string) error {
	admin, ok := q.inner.(QueueAdmin)
	if !ok {
		return ErrQueueAdminUnsupported
	}
	return admin.RetryJob(ctx, queueName, jobID)
}

func (q *observedQueue) CancelJob(ctx context.Context, jobID string) error {
	admin, ok := q.inner.(QueueAdmin)
	if !ok {
		return ErrQueueAdminUnsupported
	}
	return admin.CancelJob(ctx, jobID)
}

func (q *observedQueue) DeleteJob(ctx context.Context, queueName, jobID string) error {
	admin, ok := q.inner.(QueueAdmin)
	if !ok {
		return ErrQueueAdminUnsupported
	}
	return admin.DeleteJob(ctx, queueName, jobID)
}

func (q *observedQueue) ClearQueue(ctx context.Context, queueName string) error {
	admin, ok := q.inner.(QueueAdmin)
	if !ok {
		return ErrQueueAdminUnsupported
	}
	return admin.ClearQueue(ctx, queueName)
}

func (q *observedQueue) History(ctx context.Context, queueName string, window QueueHistoryWindow) ([]QueueHistoryPoint, error) {
	history, ok := q.inner.(QueueHistoryProvider)
	if !ok {
		return nil, ErrQueueAdminUnsupported
	}
	return history.History(ctx, queueName, window)
}

func (q *observedQueue) Dispatch(ctx context.Context, job Job) error {
	err := q.inner.Dispatch(ctx, job)
	opts := job.jobOptions()
	base := Event{
		Driver:    q.driver,
		Queue:     jobQueueName(job),
		JobType:   job.Type,
		JobKey:    jobEventKey(job),
		MaxRetry:  optionInt(opts.maxRetry),
		Scheduled: opts.delay > 0,
		Time:      time.Now(),
	}
	switch {
	case err == nil:
		base.Kind = EventEnqueueAccepted
		safeObserve(ctx, q.observer, base)
	case errors.Is(err, ErrDuplicate):
		base.Kind = EventEnqueueDuplicate
		base.Err = err
		safeObserve(ctx, q.observer, base)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		base.Kind = EventEnqueueCanceled
		base.Err = err
		safeObserve(ctx, q.observer, base)
	default:
		base.Kind = EventEnqueueRejected
		base.Err = err
		safeObserve(ctx, q.observer, base)
	}
	return err
}

func (q *observedQueue) Register(jobType string, handler Handler) {
	runtime, ok := q.inner.(interface {
		Register(string, Handler)
	})
	if !ok {
		return
	}
	if handler == nil {
		runtime.Register(jobType, handler)
		return
	}
	runtime.Register(jobType, wrapObservedHandler(q.observer, q.driver, "", jobType, nil, handler))
}

func (q *observedQueue) Driver() Driver {
	return q.driver
}

func wrapObservedHandler(observer Observer, driver Driver, queueName string, jobType string, ctxDecorator func(context.Context) context.Context, handler Handler) Handler {
	return func(ctx context.Context, job Job) error {
		if ctxDecorator != nil {
			if decorated := ctxDecorator(ctx); decorated != nil {
				ctx = decorated
			}
		}
		opts := job.jobOptions()
		effectiveQueue := queueName
		if effectiveQueue == "" {
			effectiveQueue = jobQueueName(job)
		}
		start := time.Now()
		base := Event{
			Driver:    driver,
			Queue:     effectiveQueue,
			JobType:   jobType,
			JobKey:    jobEventKey(job),
			Attempt:   opts.attempt,
			MaxRetry:  optionInt(opts.maxRetry),
			Scheduled: opts.delay > 0,
			Time:      start,
		}
		base.Kind = EventProcessStarted
		safeObserve(ctx, observer, base)

		err := handler(ctx, job)
		finish := base
		finish.Duration = time.Since(start)
		finish.Time = time.Now()
		finish.Err = err
		if err == nil {
			finish.Kind = EventProcessSucceeded
			safeObserve(ctx, observer, finish)
			return nil
		}

		finish.Kind = EventProcessFailed
		safeObserve(ctx, observer, finish)
		if finish.Attempt < finish.MaxRetry {
			retry := finish
			retry.Kind = EventProcessRetried
			retry.Err = nil
			safeObserve(ctx, observer, retry)
		} else {
			archive := finish
			archive.Kind = EventProcessArchived
			archive.Err = nil
			safeObserve(ctx, observer, archive)
		}
		return err
	}
}

func safeObserve(ctx context.Context, observer Observer, event Event) {
	SafeObserve(ctx, observer, event)
}

// SafeObserve delivers an event to an observer and recovers observer panics.
//
// This is an advanced helper intended for driver-module implementations.
// @group Observability
func SafeObserve(ctx context.Context, observer Observer, event Event) {
	if observer == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	defer func() {
		_ = recover()
	}()
	observer.Observe(ctx, event)
}

func jobQueueName(job Job) string {
	queueName := job.jobOptions().queueName
	if queueName == "" {
		return "default"
	}
	return queueName
}

func optionInt(v *int) int {
	if v == nil {
		return 0
	}
	return *v
}

func jobEventKey(job Job) string {
	hash := sha1.Sum(append([]byte(job.Type+":"), job.PayloadBytes()...))
	return fmt.Sprintf("%x", hash[:])
}

func normalizeQueueName(name string) string {
	if name == "" {
		return "default"
	}
	return name
}

func resolveQueueRuntime(v any) queueRuntime {
	switch q := v.(type) {
	case nil:
		return nil
	case queueRuntime:
		return q
	case *Queue:
		if q == nil {
			return nil
		}
		return q.q
	default:
		return nil
	}
}

// Pause pauses queue consumption for drivers that support it.
// @group Observability
//
// Example: pause queue
//
//	q, _ := queue.NewSync()
//	_ = queue.Pause(context.Background(), q, "default")
//	snapshot, _ := queue.Snapshot(context.Background(), q, nil)
//	fmt.Println(snapshot.Paused("default"))
//	// Output: 1
func Pause(ctx context.Context, q any, queueName string) error {
	raw := resolveQueueRuntime(q)
	controller, ok := raw.(QueueController)
	if !ok {
		return ErrPauseUnsupported
	}
	return controller.Pause(ctx, queueName)
}

// SupportsPause reports whether a queue runtime supports Pause/Resume.
// @group Observability
//
// Example: check pause support
//
//	q, _ := queue.NewSync()
//	fmt.Println(queue.SupportsPause(q))
//	// Output: true
func SupportsPause(q any) bool {
	raw := resolveQueueRuntime(q)
	if raw == nil {
		return false
	}
	_, ok := raw.(QueueController)
	return ok
}

// SupportsNativeStats reports whether a queue runtime exposes native stats snapshots.
// @group Observability
//
// Example: check native stats support
//
//	q, _ := queue.NewSync()
//	fmt.Println(queue.SupportsNativeStats(q))
//	// Output: true
func SupportsNativeStats(q any) bool {
	raw := resolveQueueRuntime(q)
	if raw == nil {
		return false
	}
	_, ok := raw.(StatsProvider)
	return ok
}

// Resume resumes queue consumption for drivers that support it.
// @group Observability
//
// Example: resume queue
//
//	q, _ := queue.NewSync()
//	_ = queue.Pause(context.Background(), q, "default")
//	_ = queue.Resume(context.Background(), q, "default")
//	snapshot, _ := queue.Snapshot(context.Background(), q, nil)
//	fmt.Println(snapshot.Paused("default"))
//	// Output: 0
func Resume(ctx context.Context, q any, queueName string) error {
	raw := resolveQueueRuntime(q)
	controller, ok := raw.(QueueController)
	if !ok {
		return ErrPauseUnsupported
	}
	return controller.Resume(ctx, queueName)
}

// Snapshot returns driver-native stats, falling back to collector data.
// @group Observability
//
// Example: snapshot from queue runtime
//
//	q, _ := queue.NewSync()
//	snapshot, _ := q.Stats(context.Background())
//	_, ok := snapshot.Queue("default")
//	fmt.Println(ok)
//	// Output: true
func Snapshot(ctx context.Context, q any, collector *StatsCollector) (StatsSnapshot, error) {
	raw := resolveQueueRuntime(q)
	if provider, ok := raw.(StatsProvider); ok {
		snapshot, err := provider.Stats(ctx)
		if err == nil {
			return snapshot, nil
		}
	}
	if collector != nil {
		return collector.Snapshot(), nil
	}
	return StatsSnapshot{}, fmt.Errorf("snapshot is unavailable: no driver stats provider and no collector fallback")
}
