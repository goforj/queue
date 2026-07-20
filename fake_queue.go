package queue

import (
	"context"
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

// FakeQueue is the concurrency-safe queue and workflow fake for tests.
// @group Testing
type FakeQueue struct {
	state *fakeQueueState
	ctx   context.Context
}

// fakeQueueState owns every mutable projection shared by context-bound and
// compatibility fake handles.
type fakeQueueState struct {
	defaultQueue string
	// workflowOps prevents Reset or Prune from splitting one engine dispatch
	// across state generations or intermediate terminal transitions.
	workflowOps sync.RWMutex
	mu          sync.RWMutex
	records     []DispatchRecord
	workflow    *fakeWorkflowRecorder
}

// NewFake creates the canonical fake for direct and workflow tests.
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
	fake := &FakeQueue{
		state: &fakeQueueState{
			defaultQueue: "default",
			records:      make([]DispatchRecord, 0),
		},
	}
	fake.state.workflow = newFakeWorkflowRecorder(fake)
	return fake
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

// physicalQueueNameOrDefault keeps fake event labels aligned with its recording queue names.
func (f *FakeQueue) physicalQueueNameOrDefault(queueName string) string {
	defaultQueue := "default"
	if f != nil && f.state != nil && f.state.defaultQueue != "" {
		defaultQueue = f.state.defaultQueue
	}
	return PhysicalQueueName(defaultQueue, queueName)
}

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

// setHandlerContextDecorator remains inert because the fake records intent and
// never invokes registered handlers.
func (f *FakeQueue) setHandlerContextDecorator(func(context.Context) context.Context) {}

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
	return f.dispatch(ctx, job)
}

// dispatch validates and freezes one accepted job before publishing it to all
// fake views that share this state.
func (f *FakeQueue) dispatch(ctx context.Context, job any) error {
	dispatchJob, err := normalizeDispatchJob(job, f.state.defaultQueue)
	if err != nil {
		return err
	}
	if err := dispatchJob.validate(); err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	queueName := dispatchJob.jobOptions().queueName
	if queueName == "" {
		queueName = f.state.defaultQueue
	}
	f.state.mu.Lock()
	f.state.records = append(f.state.records, DispatchRecord{
		Job:   cloneFakeJob(dispatchJob),
		Queue: queueName,
	})
	f.state.mu.Unlock()
	return nil
}

// Register is a compatibility no-op because the recording fake never executes handlers.
// @group Testing
//
// Example: register no-op on fake
//
//	fake := queue.NewFake()
//	fake.Register("emails:send", func(context.Context, queue.Job) error { return nil })
func (f *FakeQueue) Register(string, Handler) {}

// StartWorkers is a compatibility no-op because the recording fake owns no workers.
// @group Testing
//
// Example: start fake workers
//
//	fake := queue.NewFake()
//	err := fake.StartWorkers(context.Background())
//	_ = err
func (f *FakeQueue) StartWorkers(context.Context) error { return nil }

// Workers preserves fluent lifecycle compatibility without creating workers.
// @group Testing
//
// Example: set worker count
//
//	fake := queue.NewFake()
//	q := fake.Workers(4)
//	fmt.Println(q != nil)
//	// Output: true
func (f *FakeQueue) Workers(int) queueRuntime { return f }

// Shutdown is a compatibility no-op because the recording fake owns no worker resources.
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
//	// Output: true
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
	job := fakeBusJob(jobType, payload, opts, true)
	if fakeWorkflowDeliverySuppressed(ctx, jobType) {
		if err := ctx.Err(); err != nil {
			return err
		}
		return job.validate()
	}
	return f.dispatch(ctx, job)
}

// BusDispatchDirect records the application job and its correlation metadata
// without introducing the legacy workflow envelope.
// @group Testing
func (f *FakeQueue) BusDispatchDirect(ctx context.Context, jobType string, payload []byte, metadata busruntime.DeliveryMetadata, opts busruntime.JobOptions) error {
	job := DriverWithMetadata(fakeBusJob(jobType, payload, opts, false), metadata)
	return f.dispatch(ctx, job)
}

// fakeBusJob mirrors the production runtime adapter so explicit retry zero and
// workflow identity retain their delivery meaning in tests.
func fakeBusJob(jobType string, payload []byte, opts busruntime.JobOptions, legacyIdentity bool) Job {
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
	job = job.Retry(opts.Retry)
	if opts.Backoff > 0 {
		job = job.Backoff(opts.Backoff)
	}
	if opts.UniqueFor > 0 {
		job = job.UniqueFor(opts.UniqueFor)
		if legacyIdentity {
			logical := resolveLogicalJob(jobType, payload)
			job = job.withLogicalIdentity(logical.jobType, logical.payload)
		}
	}
	return job
}

// Reset clears direct dispatches and all workflow records through every fake view.
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
	f.state.workflowOps.Lock()
	defer f.state.workflowOps.Unlock()
	f.state.mu.Lock()
	f.state.records = nil
	f.state.workflow.resetLocked()
	f.state.mu.Unlock()
}

// Records returns isolated records for accepted direct dispatches.
// Chain and batch creation is available through ChainRecords and BatchRecords.
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
	for i, record := range f.state.records {
		out[i] = DispatchRecord{
			Job:   cloneFakeJob(record.Job),
			Queue: record.Queue,
		}
	}
	return out
}

// AssertNothingDispatched fails when any direct dispatch was recorded.
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

// AssertCount fails when the direct dispatch count is not expected.
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

// cloneFakeJob isolates the mutable payload and option pointers exposed by
// driver helpers so inspection cannot rewrite previously accepted evidence.
func cloneFakeJob(job Job) Job {
	job.payload = cloneWorkflowPayload(job.payload)
	job.options.logicalPayload = cloneWorkflowPayload(job.options.logicalPayload)
	if job.options.timeout != nil {
		value := *job.options.timeout
		job.options.timeout = &value
	}
	if job.options.maxRetry != nil {
		value := *job.options.maxRetry
		job.options.maxRetry = &value
	}
	if job.options.backoff != nil {
		value := *job.options.backoff
		job.options.backoff = &value
	}
	return job
}
