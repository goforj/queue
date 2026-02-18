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
// @group Observability
type EventKind string

const (
	// EventEnqueueAccepted indicates a task was accepted for enqueue.
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
)

// Event is emitted through Observer hooks for queue/worker activity.
// @group Observability
type Event struct {
	Kind      EventKind
	Driver    Driver
	Queue     string
	TaskType  string
	TaskKey   string
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
	Observe(event Event)
}

// ObserverFunc adapts a function to an Observer.
// @group Observability
type ObserverFunc func(event Event)

// Observe calls the wrapped function.
func (f ObserverFunc) Observe(event Event) {
	f(event)
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
//		queue.ObserverFunc(func(queue.Event) {}),
//	)
//	observer.Observe(queue.Event{Kind: queue.EventEnqueueAccepted})
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

func (m *multiObserver) Observe(event Event) {
	for _, observer := range m.observers {
		observer.Observe(event)
	}
}

// ChannelObserver forwards events to a channel.
// @group Observability
type ChannelObserver struct {
	Events     chan<- Event
	DropIfFull bool
}

// Observe forwards an event to the configured channel.
func (c ChannelObserver) Observe(event Event) {
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
//	collector.Observe(queue.Event{
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
//	collector.Observe(queue.Event{
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
//	collector.Observe(queue.Event{
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

// Retry returns retry count for a queue.
// @group Observability
//
// Example: retry count getter
//
//	snapshot := queue.StatsSnapshot{
//		ByQueue: map[string]queue.QueueCounters{
//			"default": {Retry: 1},
//		},
//	}
//	fmt.Println(snapshot.Retry("default"))
//	// Output: 1
func (s StatsSnapshot) Retry(name string) int64 {
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
//	collector.Observe(queue.Event{
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
//	collector.Observe(queue.Event{
//		Kind:   queue.EventEnqueueAccepted,
//		Driver: queue.DriverSync,
//		Queue:  "default",
//		Time:   time.Now(),
//	})
func (c *StatsCollector) Observe(event Event) {
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
		if event.TaskKey != "" {
			state.pendingByKey[event.TaskKey] = append(state.pendingByKey[event.TaskKey], now)
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
		if event.TaskKey != "" {
			if entries, ok := state.pendingByKey[event.TaskKey]; ok && len(entries) > 0 {
				enqueuedAt := entries[0]
				rest := entries[1:]
				if len(rest) == 0 {
					delete(state.pendingByKey, event.TaskKey)
				} else {
					state.pendingByKey[event.TaskKey] = rest
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
//	collector.Observe(queue.Event{
//		Kind:   queue.EventEnqueueAccepted,
//		Driver: queue.DriverSync,
//		Queue:  "default",
//		Time:   time.Now(),
//	})
//	collector.Observe(queue.Event{
//		Kind:   queue.EventProcessStarted,
//		Driver: queue.DriverSync,
//		Queue:  "default",
//		TaskKey: "task-1",
//		Time:   time.Now(),
//	})
//	collector.Observe(queue.Event{
//		Kind:     queue.EventProcessSucceeded,
//		Driver:   queue.DriverSync,
//		Queue:    "default",
//		TaskKey:  "task-1",
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
	inner    Queue
	driver   Driver
	observer Observer
}

func newObservedQueue(inner Queue, observer Observer) Queue {
	if observer == nil {
		return inner
	}
	return &observedQueue{
		inner:    inner,
		driver:   detectQueueDriver(inner),
		observer: observer,
	}
}

func (q *observedQueue) Start(ctx context.Context) error {
	return q.inner.Start(ctx)
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

func (q *observedQueue) Pause(ctx context.Context, queueName string) error {
	controller, ok := q.inner.(QueueController)
	if !ok {
		return ErrPauseUnsupported
	}
	if err := controller.Pause(ctx, queueName); err != nil {
		return err
	}
	q.observer.Observe(Event{
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
	q.observer.Observe(Event{
		Kind:   EventQueueResumed,
		Driver: q.driver,
		Queue:  normalizeQueueName(queueName),
		Time:   time.Now(),
	})
	return nil
}

func (q *observedQueue) Enqueue(ctx context.Context, task Task) error {
	err := q.inner.Enqueue(ctx, task)
	opts := task.enqueueOptions()
	base := Event{
		Driver:    q.driver,
		Queue:     taskQueueName(task),
		TaskType:  task.Type,
		TaskKey:   taskEventKey(task),
		MaxRetry:  optionInt(opts.maxRetry),
		Scheduled: opts.delay > 0,
		Time:      time.Now(),
	}
	switch {
	case err == nil:
		base.Kind = EventEnqueueAccepted
		q.observer.Observe(base)
	case errors.Is(err, ErrDuplicate):
		base.Kind = EventEnqueueDuplicate
		base.Err = err
		q.observer.Observe(base)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		base.Kind = EventEnqueueCanceled
		base.Err = err
		q.observer.Observe(base)
	default:
		base.Kind = EventEnqueueRejected
		base.Err = err
		q.observer.Observe(base)
	}
	return err
}

func (q *observedQueue) Register(taskType string, handler Handler) {
	if handler == nil {
		q.inner.Register(taskType, handler)
		return
	}
	q.inner.Register(taskType, wrapObservedHandler(q.observer, q.driver, "", taskType, handler))
}

func (q *observedQueue) Driver() Driver {
	return q.driver
}

type observedWorker struct {
	inner    Worker
	driver   Driver
	observer Observer
}

func newObservedWorker(inner Worker, observer Observer) Worker {
	if observer == nil {
		return inner
	}
	return &observedWorker{
		inner:    inner,
		driver:   inner.Driver(),
		observer: observer,
	}
}

func (w *observedWorker) Driver() Driver {
	return w.driver
}

func (w *observedWorker) Register(taskType string, handler Handler) {
	if handler == nil {
		w.inner.Register(taskType, handler)
		return
	}
	w.inner.Register(taskType, wrapObservedHandler(w.observer, w.driver, "", taskType, handler))
}

func (w *observedWorker) Start() error {
	return w.inner.Start()
}

func (w *observedWorker) Shutdown() error {
	return w.inner.Shutdown()
}

func wrapObservedHandler(observer Observer, driver Driver, queueName string, taskType string, handler Handler) Handler {
	return func(ctx context.Context, task Task) error {
		opts := task.enqueueOptions()
		effectiveQueue := queueName
		if effectiveQueue == "" {
			effectiveQueue = taskQueueName(task)
		}
		start := time.Now()
		base := Event{
			Driver:    driver,
			Queue:     effectiveQueue,
			TaskType:  taskType,
			TaskKey:   taskEventKey(task),
			Attempt:   opts.attempt,
			MaxRetry:  optionInt(opts.maxRetry),
			Scheduled: opts.delay > 0,
			Time:      start,
		}
		base.Kind = EventProcessStarted
		observer.Observe(base)

		err := handler(ctx, task)
		finish := base
		finish.Duration = time.Since(start)
		finish.Time = time.Now()
		finish.Err = err
		if err == nil {
			finish.Kind = EventProcessSucceeded
			observer.Observe(finish)
			return nil
		}

		finish.Kind = EventProcessFailed
		observer.Observe(finish)
		if finish.Attempt < finish.MaxRetry {
			retry := finish
			retry.Kind = EventProcessRetried
			retry.Err = nil
			observer.Observe(retry)
		} else {
			archive := finish
			archive.Kind = EventProcessArchived
			archive.Err = nil
			observer.Observe(archive)
		}
		return err
	}
}

func detectQueueDriver(q Queue) Driver {
	if driverAware, ok := q.(interface{ Driver() Driver }); ok {
		return driverAware.Driver()
	}
	return Driver("")
}

func taskQueueName(task Task) string {
	queueName := task.enqueueOptions().queueName
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

func taskEventKey(task Task) string {
	hash := sha1.Sum(append([]byte(task.Type+":"), task.PayloadBytes()...))
	return fmt.Sprintf("%x", hash[:])
}

func normalizeQueueName(name string) string {
	if name == "" {
		return "default"
	}
	return name
}

// PauseQueue pauses queue consumption for drivers that support it.
// @group Observability
//
// Example: pause queue
//
//	q, _ := queue.New(queue.Config{Driver: queue.DriverSync})
//	_ = queue.PauseQueue(context.Background(), q, "default")
//	snapshot, _ := queue.SnapshotQueue(context.Background(), q, nil)
//	fmt.Println(snapshot.Paused("default"))
//	// Output: 1
func PauseQueue(ctx context.Context, q Queue, queueName string) error {
	controller, ok := q.(QueueController)
	if !ok {
		return ErrPauseUnsupported
	}
	return controller.Pause(ctx, queueName)
}

// SupportsPause reports whether a queue runtime supports PauseQueue/ResumeQueue.
// @group Observability
//
// Example: check pause support
//
//	q, _ := queue.New(queue.Config{Driver: queue.DriverSync})
//	fmt.Println(queue.SupportsPause(q))
//	// Output: true
func SupportsPause(q Queue) bool {
	if q == nil {
		return false
	}
	_, ok := q.(QueueController)
	return ok
}

// SupportsNativeStats reports whether a queue runtime exposes native stats snapshots.
// @group Observability
//
// Example: check native stats support
//
//	q, _ := queue.New(queue.Config{Driver: queue.DriverSync})
//	fmt.Println(queue.SupportsNativeStats(q))
//	// Output: true
func SupportsNativeStats(q Queue) bool {
	if q == nil {
		return false
	}
	_, ok := q.(StatsProvider)
	return ok
}

// ResumeQueue resumes queue consumption for drivers that support it.
// @group Observability
//
// Example: resume queue
//
//	q, _ := queue.New(queue.Config{Driver: queue.DriverSync})
//	_ = queue.PauseQueue(context.Background(), q, "default")
//	_ = queue.ResumeQueue(context.Background(), q, "default")
//	snapshot, _ := queue.SnapshotQueue(context.Background(), q, nil)
//	fmt.Println(snapshot.Paused("default"))
//	// Output: 0
func ResumeQueue(ctx context.Context, q Queue, queueName string) error {
	controller, ok := q.(QueueController)
	if !ok {
		return ErrPauseUnsupported
	}
	return controller.Resume(ctx, queueName)
}

// SnapshotQueue returns driver-native stats, falling back to collector data.
// @group Observability
//
// Example: snapshot from queue runtime
//
//	q, _ := queue.New(queue.Config{Driver: queue.DriverSync})
//	snapshot, _ := queue.SnapshotQueue(context.Background(), q, nil)
//	_, ok := snapshot.Queue("default")
//	fmt.Println(ok)
//	// Output: true
func SnapshotQueue(ctx context.Context, q Queue, collector *StatsCollector) (StatsSnapshot, error) {
	if provider, ok := q.(StatsProvider); ok {
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
