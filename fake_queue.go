package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sync"
	"testing"

	"github.com/goforj/queue/busruntime"
)

// DispatchRecord captures one dispatch observed by FakeQueue.
// @group Testing
type DispatchRecord struct {
	Job   Job
	Queue string
}

// FakeQueue is an in-memory queue fake for tests.
// @group Testing
type FakeQueue struct {
	state *fakeQueueState
	ctx   context.Context
}

type fakeQueueState struct {
	defaultQueue string
	mu           sync.RWMutex
	records      []DispatchRecord
}

// NewFake creates a queue fake that records dispatches and provides assertions.
// @group Testing
//
// Example: fake queue assertions
//
//	fake := queue.NewFake()
//	_ = fake.Dispatch(
//		queue.NewJob("emails:send").
//			Payload(map[string]any{"id": 1}).
//			OnQueue("critical"),
//	)
//	records := fake.Records()
//	fmt.Println(len(records), records[0].Queue, records[0].Job.Type)
//	// Output: 1 critical emails:send
func NewFake() *FakeQueue {
	return &FakeQueue{
		state: &fakeQueueState{
			defaultQueue: "default",
			records:      make([]DispatchRecord, 0),
		},
	}
}

// Driver returns the active queue driver.
// @group Testing
//
// Example: fake driver
//
//	fake := queue.NewFake()
//	driver := fake.Driver()
//	_ = driver
func (f *FakeQueue) Driver() Driver { return DriverNull }

// WithContext returns a derived fake queue handle bound to ctx.
// @group Testing
func (f *FakeQueue) WithContext(ctx context.Context) queueRuntime {
	if f == nil {
		return nil
	}
	clone := *f
	clone.ctx = ctx
	return &clone
}

// Dispatch records a typed job payload in-memory using the fake default queue.
// @group Testing
//
// Example: dispatch to fake queue
//
//	fake := queue.NewFake()
//	err := fake.Dispatch(queue.NewJob("emails:send").OnQueue("default"))
//	_ = err
func (f *FakeQueue) Dispatch(job any) error {
	ctx := context.Background()
	if f != nil && f.ctx != nil {
		ctx = f.ctx
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	dispatchJob, err := f.jobFromAny(job)
	if err != nil {
		return err
	}
	queueName := dispatchJob.jobOptions().queueName
	if queueName == "" {
		queueName = f.state.defaultQueue
	}
	f.state.mu.Lock()
	f.state.records = append(f.state.records, DispatchRecord{
		Job:   dispatchJob,
		Queue: queueName,
	})
	f.state.mu.Unlock()
	return nil
}

// Register associates a handler with a job type.
// @group Testing
//
// Example: register no-op on fake
//
//	fake := queue.NewFake()
//	fake.Register("emails:send", func(context.Context, queue.Job) error { return nil })
func (f *FakeQueue) Register(string, Handler) {}

// StartWorkers starts worker execution.
// @group Testing
//
// Example: start fake workers
//
//	fake := queue.NewFake()
//	err := fake.StartWorkers(context.Background())
//	_ = err
func (f *FakeQueue) StartWorkers(context.Context) error { return nil }

// Workers sets desired worker concurrency before StartWorkers.
// @group Testing
//
// Example: set worker count
//
//	fake := queue.NewFake()
//	q := fake.Workers(4)
//	fmt.Println(q != nil)
//	// Output: true
func (f *FakeQueue) Workers(int) queueRuntime { return f }

// Shutdown drains running work and releases resources.
// @group Testing
//
// Example: shutdown fake queue
//
//	fake := queue.NewFake()
//	err := fake.Shutdown(context.Background())
//	_ = err
func (f *FakeQueue) Shutdown(context.Context) error { return nil }

// Ready validates fake queue readiness.
// @group Testing
//
// Example: fake ready
//
//	fake := queue.NewFake()
//	fmt.Println(fake.Ready(context.Background()) == nil)
//	// true
func (f *FakeQueue) Ready(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

// BusRegister satisfies the internal orchestration runtime adapter.
// @group Testing
func (f *FakeQueue) BusRegister(string, busruntime.Handler) {}

// BusDispatch satisfies the internal orchestration runtime adapter.
// @group Testing
func (f *FakeQueue) BusDispatch(ctx context.Context, jobType string, payload []byte, opts busruntime.JobOptions) error {
	job := NewJob(jobType).Payload(payload)
	if opts.Queue != "" {
		job = job.OnQueue(opts.Queue)
	}
	if opts.Delay > 0 {
		job = job.Delay(opts.Delay)
	}
	if opts.Timeout > 0 {
		job = job.Timeout(opts.Timeout)
	}
	if opts.Retry > 0 {
		job = job.Retry(opts.Retry)
	}
	if opts.Backoff > 0 {
		job = job.Backoff(opts.Backoff)
	}
	if opts.UniqueFor > 0 {
		job = job.UniqueFor(opts.UniqueFor)
	}
	return f.WithContext(ctx).Dispatch(job)
}

// Reset clears all recorded dispatches.
// @group Testing
//
// Example: reset records
//
//	fake := queue.NewFake()
//	_ = fake.Dispatch(queue.NewJob("emails:send").OnQueue("default"))
//	fmt.Println(len(fake.Records()))
//	fake.Reset()
//	fmt.Println(len(fake.Records()))
//	// Output:
//	// 1
//	// 0
func (f *FakeQueue) Reset() {
	f.state.mu.Lock()
	f.state.records = f.state.records[:0]
	f.state.mu.Unlock()
}

// Records returns a copy of all dispatch records.
// @group Testing
//
// Example: read records
//
//	fake := queue.NewFake()
//	_ = fake.Dispatch(queue.NewJob("emails:send").OnQueue("default"))
//	records := fake.Records()
//	fmt.Println(len(records), records[0].Job.Type)
//	// Output: 1 emails:send
func (f *FakeQueue) Records() []DispatchRecord {
	f.state.mu.RLock()
	defer f.state.mu.RUnlock()
	out := make([]DispatchRecord, len(f.state.records))
	copy(out, f.state.records)
	return out
}

// AssertNothingDispatched fails when any dispatch was recorded.
// @group Testing
//
// Example: assert nothing dispatched
//
//	fake := queue.NewFake()
//	fake.AssertNothingDispatched(t)
func (f *FakeQueue) AssertNothingDispatched(t testing.TB) {
	t.Helper()
	if got := len(f.Records()); got != 0 {
		t.Fatalf("expected no dispatched jobs, got %d", got)
	}
}

// AssertCount fails when dispatch count is not expected.
// @group Testing
//
// Example: assert dispatch count
//
//	fake := queue.NewFake()
//	_ = fake.Dispatch(queue.NewJob("emails:send"))
//	fake.AssertCount(t, 1)
func (f *FakeQueue) AssertCount(t testing.TB, expected int) {
	t.Helper()
	if got := len(f.Records()); got != expected {
		t.Fatalf("expected %d dispatched jobs, got %d", expected, got)
	}
}

// AssertDispatched fails when jobType was not dispatched.
// @group Testing
//
// Example: assert job type dispatched
//
//	fake := queue.NewFake()
//	_ = fake.Dispatch(queue.NewJob("emails:send"))
//	fake.AssertDispatched(t, "emails:send")
func (f *FakeQueue) AssertDispatched(t testing.TB, jobType string) {
	t.Helper()
	for _, record := range f.Records() {
		if record.Job.Type == jobType {
			return
		}
	}
	t.Fatalf("expected job type %q to be dispatched", jobType)
}

// AssertDispatchedOn fails when jobType was not dispatched on queueName.
// @group Testing
//
// Example: assert job type dispatched on queue
//
//	fake := queue.NewFake()
//	_ = fake.Dispatch(
//		queue.NewJob("emails:send").
//			OnQueue("critical"),
//	)
//	fake.AssertDispatchedOn(t, "critical", "emails:send")
func (f *FakeQueue) AssertDispatchedOn(t testing.TB, queueName, jobType string) {
	t.Helper()
	for _, record := range f.Records() {
		if record.Job.Type == jobType && record.Queue == queueName {
			return
		}
	}
	t.Fatalf("expected job type %q dispatched on queue %q", jobType, queueName)
}

// AssertDispatchedTimes fails when jobType dispatch count does not match expected.
// @group Testing
//
// Example: assert job type dispatched times
//
//	fake := queue.NewFake()
//	_ = fake.Dispatch(queue.NewJob("emails:send"))
//	_ = fake.Dispatch(queue.NewJob("emails:send"))
//	fake.AssertDispatchedTimes(t, "emails:send", 2)
func (f *FakeQueue) AssertDispatchedTimes(t testing.TB, jobType string, expected int) {
	t.Helper()
	var count int
	for _, record := range f.Records() {
		if record.Job.Type == jobType {
			count++
		}
	}
	if count != expected {
		t.Fatalf("expected job type %q dispatched %d times, got %d", jobType, expected, count)
	}
}

// AssertNotDispatched fails when jobType was dispatched.
// @group Testing
//
// Example: assert job type not dispatched
//
//	fake := queue.NewFake()
//	_ = fake.Dispatch(queue.NewJob("emails:send"))
//	fake.AssertNotDispatched(t, "emails:cancel")
func (f *FakeQueue) AssertNotDispatched(t testing.TB, jobType string) {
	t.Helper()
	for _, record := range f.Records() {
		if record.Job.Type == jobType {
			t.Fatalf("expected job type %q not to be dispatched", jobType)
		}
	}
}

func (f *FakeQueue) jobFromAny(job any) (Job, error) {
	if job, ok := job.(Job); ok {
		if job.Type == "" {
			return Job{}, fmt.Errorf("dispatch job type is required")
		}
		return job, nil
	}
	if job == nil {
		return Job{}, fmt.Errorf("dispatch job is nil")
	}
	jobType := fakeJobTypeFromValue(job)
	if jobType == "" {
		return Job{}, fmt.Errorf("dispatch job type could not be inferred")
	}
	if typed, ok := job.(interface{ JobType() string }); ok {
		if t := typed.JobType(); t != "" {
			jobType = t
		}
	}
	payload, err := json.Marshal(job)
	if err != nil {
		return Job{}, fmt.Errorf("marshal dispatch job: %w", err)
	}
	return NewJob(jobType).Payload(payload).OnQueue(f.state.defaultQueue), nil
}

func fakeJobTypeFromValue(v any) string {
	t := reflect.TypeOf(v)
	if t == nil {
		return ""
	}
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Name() == "" {
		return ""
	}
	return t.Name()
}
