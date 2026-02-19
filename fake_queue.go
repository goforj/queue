package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sync"
	"testing"
)

// DispatchRecord captures one dispatch observed by FakeQueue.
// @group Testing
type DispatchRecord struct {
	Task  Task
	Queue string
}

// FakeQueue is an in-memory queue fake for tests.
// @group Testing
type FakeQueue struct {
	defaultQueue string

	mu      sync.RWMutex
	records []DispatchRecord
}

// NewFake creates a queue fake that records dispatches and provides assertions.
// @group Testing
//
// Example: fake queue assertions
//
//	fake := queue.NewFake()
//	_ = fake.Dispatch(
//		queue.NewTask("emails:send").
//			Payload(map[string]any{"id": 1}).
//			OnQueue("critical"),
//	)
//	records := fake.Records()
//	fmt.Println(len(records), records[0].Queue, records[0].Task.Type)
//	// Output: 1 critical emails:send
func NewFake() *FakeQueue {
	return &FakeQueue{
		defaultQueue: "default",
		records:      make([]DispatchRecord, 0),
	}
}

// Driver returns the active queue driver.
func (f *FakeQueue) Driver() Driver { return DriverNull }

// Dispatch submits a typed job payload using the default queue.
func (f *FakeQueue) Dispatch(job any) error {
	return f.DispatchCtx(context.Background(), job)
}

// DispatchCtx submits a typed job payload using the provided context.
// @group Testing
//
// Example: dispatch with context
//
//	fake := queue.NewFake()
//	ctx := context.Background()
//	err := fake.DispatchCtx(ctx, queue.NewTask("emails:send").OnQueue("default"))
//	fmt.Println(err == nil)
//	// Output: true
func (f *FakeQueue) DispatchCtx(ctx context.Context, job any) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	task, err := f.taskFromJob(job)
	if err != nil {
		return err
	}
	queueName := task.enqueueOptions().queueName
	if queueName == "" {
		queueName = f.defaultQueue
	}
	f.mu.Lock()
	f.records = append(f.records, DispatchRecord{
		Task:  task,
		Queue: queueName,
	})
	f.mu.Unlock()
	return nil
}

// Register associates a handler with a task type.
func (f *FakeQueue) Register(string, Handler) {}

// StartWorkers starts worker execution.
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
func (f *FakeQueue) Workers(int) Queue { return f }

// Shutdown drains running work and releases resources.
func (f *FakeQueue) Shutdown(context.Context) error { return nil }

// Reset clears all recorded dispatches.
// @group Testing
//
// Example: reset records
//
//	fake := queue.NewFake()
//	_ = fake.Dispatch(queue.NewTask("emails:send").OnQueue("default"))
//	fmt.Println(len(fake.Records()))
//	fake.Reset()
//	fmt.Println(len(fake.Records()))
//	// Output:
//	// 1
//	// 0
func (f *FakeQueue) Reset() {
	f.mu.Lock()
	f.records = f.records[:0]
	f.mu.Unlock()
}

// Records returns a copy of all dispatch records.
// @group Testing
//
// Example: read records
//
//	fake := queue.NewFake()
//	_ = fake.Dispatch(queue.NewTask("emails:send").OnQueue("default"))
//	records := fake.Records()
//	fmt.Println(len(records), records[0].Task.Type)
//	// Output: 1 emails:send
func (f *FakeQueue) Records() []DispatchRecord {
	f.mu.RLock()
	defer f.mu.RUnlock()
	out := make([]DispatchRecord, len(f.records))
	copy(out, f.records)
	return out
}

// AssertNothingDispatched fails when any dispatch was recorded.
// @group Testing
//
// Example: assert nothing dispatched
//
//	fake := queue.NewFake()
//	fake.AssertNothingDispatched(nil)
func (f *FakeQueue) AssertNothingDispatched(t testing.TB) {
	t.Helper()
	if got := len(f.Records()); got != 0 {
		t.Fatalf("expected no dispatched tasks, got %d", got)
	}
}

// AssertCount fails when dispatch count is not expected.
// @group Testing
//
// Example: assert dispatch count
//
//	fake := queue.NewFake()
//	_ = fake.Dispatch(queue.NewTask("emails:send"))
//	fake.AssertCount(nil, 1)
func (f *FakeQueue) AssertCount(t testing.TB, expected int) {
	t.Helper()
	if got := len(f.Records()); got != expected {
		t.Fatalf("expected %d dispatched tasks, got %d", expected, got)
	}
}

// AssertDispatched fails when taskType was not dispatched.
// @group Testing
//
// Example: assert task type dispatched
//
//	fake := queue.NewFake()
//	_ = fake.Dispatch(queue.NewTask("emails:send"))
//	fake.AssertDispatched(nil, "emails:send")
func (f *FakeQueue) AssertDispatched(t testing.TB, taskType string) {
	t.Helper()
	for _, record := range f.Records() {
		if record.Task.Type == taskType {
			return
		}
	}
	t.Fatalf("expected task type %q to be dispatched", taskType)
}

// AssertDispatchedOn fails when taskType was not dispatched on queueName.
// @group Testing
//
// Example: assert task type dispatched on queue
//
//	fake := queue.NewFake()
//	_ = fake.Dispatch(
//		queue.NewTask("emails:send").
//			OnQueue("critical"),
//	)
//	fake.AssertDispatchedOn(nil, "critical", "emails:send")
func (f *FakeQueue) AssertDispatchedOn(t testing.TB, queueName, taskType string) {
	t.Helper()
	for _, record := range f.Records() {
		if record.Task.Type == taskType && record.Queue == queueName {
			return
		}
	}
	t.Fatalf("expected task type %q dispatched on queue %q", taskType, queueName)
}

// AssertDispatchedTimes fails when taskType dispatch count does not match expected.
// @group Testing
//
// Example: assert task type dispatched times
//
//	fake := queue.NewFake()
//	_ = fake.Dispatch(queue.NewTask("emails:send"))
//	_ = fake.Dispatch(queue.NewTask("emails:send"))
//	fake.AssertDispatchedTimes(nil, "emails:send", 2)
func (f *FakeQueue) AssertDispatchedTimes(t testing.TB, taskType string, expected int) {
	t.Helper()
	var count int
	for _, record := range f.Records() {
		if record.Task.Type == taskType {
			count++
		}
	}
	if count != expected {
		t.Fatalf("expected task type %q dispatched %d times, got %d", taskType, expected, count)
	}
}

// AssertNotDispatched fails when taskType was dispatched.
// @group Testing
//
// Example: assert task type not dispatched
//
//	fake := queue.NewFake()
//	_ = fake.Dispatch(queue.NewTask("emails:send"))
//	fake.AssertNotDispatched(nil, "emails:cancel")
func (f *FakeQueue) AssertNotDispatched(t testing.TB, taskType string) {
	t.Helper()
	for _, record := range f.Records() {
		if record.Task.Type == taskType {
			t.Fatalf("expected task type %q not to be dispatched", taskType)
		}
	}
}

func (f *FakeQueue) taskFromJob(job any) (Task, error) {
	if task, ok := job.(Task); ok {
		if task.Type == "" {
			return Task{}, fmt.Errorf("dispatch task type is required")
		}
		return task, nil
	}
	if job == nil {
		return Task{}, fmt.Errorf("dispatch job is nil")
	}
	taskType := fakeTaskTypeFromValue(job)
	if taskType == "" {
		return Task{}, fmt.Errorf("dispatch job type could not be inferred")
	}
	if typed, ok := job.(interface{ TaskType() string }); ok {
		if t := typed.TaskType(); t != "" {
			taskType = t
		}
	}
	payload, err := json.Marshal(job)
	if err != nil {
		return Task{}, fmt.Errorf("marshal dispatch job: %w", err)
	}
	return NewTask(taskType).Payload(payload).OnQueue(f.defaultQueue), nil
}

func fakeTaskTypeFromValue(v any) string {
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
