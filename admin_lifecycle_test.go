package queue

import (
	"context"
	"errors"
	"testing"
)

// lifecycleAdminBackend records whether lifecycle-gated calls reach driver resources.
type lifecycleAdminBackend struct {
	activeLeases  *int
	callErr       error
	calls         int
	leaseViolated bool
	queues        []string
}

// recordCall verifies driver resources are used only while the runtime lease is active.
func (b *lifecycleAdminBackend) recordCall() error {
	b.calls++
	if b.activeLeases != nil && *b.activeLeases != 1 {
		b.leaseViolated = true
	}
	return b.callErr
}

// recordQueueCall retains the physical queue while applying the common lease assertion.
func (b *lifecycleAdminBackend) recordQueueCall(queueName string) error {
	b.queues = append(b.queues, queueName)
	return b.recordCall()
}

// Driver identifies the backend used by the admin wrapper tests.
func (b *lifecycleAdminBackend) Driver() Driver { return DriverRedis }

// Dispatch satisfies queueBackend without affecting admin call accounting.
func (b *lifecycleAdminBackend) Dispatch(context.Context, Job) error { return nil }

// Shutdown satisfies queueBackend without affecting admin call accounting.
func (b *lifecycleAdminBackend) Shutdown(context.Context) error { return nil }

// ListJobs records the physical queue passed through the namespace wrapper.
func (b *lifecycleAdminBackend) ListJobs(_ context.Context, opts ListJobsOptions) (ListJobsResult, error) {
	return ListJobsResult{}, b.recordQueueCall(opts.Queue)
}

// RetryJob records the physical queue passed through the namespace wrapper.
func (b *lifecycleAdminBackend) RetryJob(_ context.Context, queueName, _ string) error {
	return b.recordQueueCall(queueName)
}

// CancelJob records that the queue-independent operation reached the backend.
func (b *lifecycleAdminBackend) CancelJob(context.Context, string) error {
	return b.recordCall()
}

// DeleteJob records the physical queue passed through the namespace wrapper.
func (b *lifecycleAdminBackend) DeleteJob(_ context.Context, queueName, _ string) error {
	return b.recordQueueCall(queueName)
}

// ClearQueue records the physical queue passed through the namespace wrapper.
func (b *lifecycleAdminBackend) ClearQueue(_ context.Context, queueName string) error {
	return b.recordQueueCall(queueName)
}

// History records the physical queue passed through the namespace wrapper.
func (b *lifecycleAdminBackend) History(_ context.Context, queueName string, _ QueueHistoryWindow) ([]QueueHistoryPoint, error) {
	return []QueueHistoryPoint{{Processed: 1}}, b.recordQueueCall(queueName)
}

// TestQueueAdminNamespaceLeaseCoversEveryOperation verifies backend access remains leased through all admin calls.
func TestQueueAdminNamespaceLeaseCoversEveryOperation(t *testing.T) {
	activeLeases := 0
	backend := &lifecycleAdminBackend{activeLeases: &activeLeases}
	leases := 0
	releases := 0
	admin := queueAdminWithNamespace{
		admin:  backend,
		common: &queueCommon{cfg: Config{DefaultQueue: "billing_default"}},
		lease: func(context.Context) (func(), error) {
			leases++
			activeLeases++
			return func() {
				activeLeases--
				releases++
			}, nil
		},
	}

	if _, err := admin.ListJobs(context.Background(), ListJobsOptions{Queue: "reports"}); err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if err := admin.RetryJob(context.Background(), "reports", "job-1"); err != nil {
		t.Fatalf("RetryJob: %v", err)
	}
	if err := admin.CancelJob(context.Background(), "job-1"); err != nil {
		t.Fatalf("CancelJob: %v", err)
	}
	if err := admin.DeleteJob(context.Background(), "reports", "job-1"); err != nil {
		t.Fatalf("DeleteJob: %v", err)
	}
	if err := admin.ClearQueue(context.Background(), "reports"); err != nil {
		t.Fatalf("ClearQueue: %v", err)
	}
	if _, err := admin.History(context.Background(), "reports", QueueHistoryHour); err != nil {
		t.Fatalf("History: %v", err)
	}

	if leases != 6 || releases != 6 || activeLeases != 0 || backend.calls != 6 {
		t.Fatalf("leases/releases/active/backend calls = %d/%d/%d/%d, want 6/6/0/6", leases, releases, activeLeases, backend.calls)
	}
	if backend.leaseViolated {
		t.Fatal("backend operation ran without exactly one active runtime lease")
	}
	if len(backend.queues) != 5 {
		t.Fatalf("physical queue calls = %v, want five queue-scoped calls", backend.queues)
	}
	for _, queueName := range backend.queues {
		if queueName != "billing_reports" {
			t.Fatalf("physical queue = %q, want billing_reports", queueName)
		}
	}
}

// TestQueueAdminNamespaceReleasesLeaseAfterBackendFailure verifies deferred
// release is not conditional on a successful driver operation.
func TestQueueAdminNamespaceReleasesLeaseAfterBackendFailure(t *testing.T) {
	activeLeases := 0
	callErr := errors.New("admin backend unavailable")
	backend := &lifecycleAdminBackend{activeLeases: &activeLeases, callErr: callErr}
	releases := 0
	admin := queueAdminWithNamespace{
		admin:  backend,
		common: &queueCommon{cfg: Config{DefaultQueue: "default"}},
		lease: func(context.Context) (func(), error) {
			activeLeases++
			return func() {
				activeLeases--
				releases++
			}, nil
		},
	}

	if err := admin.CancelJob(context.Background(), "job-1"); !errors.Is(err, callErr) {
		t.Fatalf("CancelJob error = %v, want %v", err, callErr)
	}
	if backend.leaseViolated || activeLeases != 0 || releases != 1 || backend.calls != 1 {
		t.Fatalf("failure lease state = violated:%t active:%d releases:%d calls:%d", backend.leaseViolated, activeLeases, releases, backend.calls)
	}
}

// TestQueueAdminNamespaceLeaseFailureRejectsEveryOperation verifies shutdown rejection cannot reach driver resources.
func TestQueueAdminNamespaceLeaseFailureRejectsEveryOperation(t *testing.T) {
	backend := &lifecycleAdminBackend{}
	leaseErr := errors.New("runtime is draining")
	admin := queueAdminWithNamespace{
		admin:  backend,
		common: &queueCommon{cfg: Config{DefaultQueue: "default"}},
		lease:  func(context.Context) (func(), error) { return nil, leaseErr },
	}

	tests := []struct {
		name string
		call func() error
	}{
		{
			name: "list jobs",
			call: func() error {
				_, err := admin.ListJobs(context.Background(), ListJobsOptions{})
				return err
			},
		},
		{name: "retry job", call: func() error { return admin.RetryJob(context.Background(), "default", "job-1") }},
		{name: "cancel job", call: func() error { return admin.CancelJob(context.Background(), "job-1") }},
		{name: "delete job", call: func() error { return admin.DeleteJob(context.Background(), "default", "job-1") }},
		{name: "clear queue", call: func() error { return admin.ClearQueue(context.Background(), "default") }},
		{
			name: "history",
			call: func() error {
				_, err := admin.History(context.Background(), "default", QueueHistoryHour)
				return err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.call(); !errors.Is(err, leaseErr) {
				t.Fatalf("error = %v, want %v", err, leaseErr)
			}
		})
	}
	if backend.calls != 0 {
		t.Fatalf("backend calls = %d, want none after lease rejection", backend.calls)
	}

	history := queueHistoryWithNamespace{
		history: backend,
		common:  &queueCommon{cfg: Config{DefaultQueue: "default"}},
		lease:   func(context.Context) (func(), error) { return nil, leaseErr },
	}
	if _, err := history.History(context.Background(), "default", QueueHistoryHour); !errors.Is(err, leaseErr) {
		t.Fatalf("history-only error = %v, want %v", err, leaseErr)
	}
	if backend.calls != 0 {
		t.Fatalf("backend calls = %d, want none after history lease rejection", backend.calls)
	}
}

// TestQueueAdminNamespaceOptionalLeasePreservesCompatibility verifies wrappers constructed without lifecycle wiring still delegate safely.
func TestQueueAdminNamespaceOptionalLeasePreservesCompatibility(t *testing.T) {
	backend := &lifecycleAdminBackend{}
	common := &queueCommon{cfg: Config{DefaultQueue: "billing_default"}}
	admin := queueAdminWithNamespace{admin: backend, common: common}
	if err := admin.CancelJob(context.Background(), "job-1"); err != nil {
		t.Fatalf("CancelJob without lease: %v", err)
	}

	history := queueHistoryWithNamespace{history: backend, common: common}
	if _, err := history.History(context.Background(), "reports", QueueHistoryHour); err != nil {
		t.Fatalf("History without lease: %v", err)
	}
	if backend.calls != 2 {
		t.Fatalf("backend calls = %d, want 2", backend.calls)
	}
}

// TestResolvedNativeQueueAdminRejectsOperationsAfterShutdown verifies resolver-installed leases honor the runtime lifecycle gate.
func TestResolvedNativeQueueAdminRejectsOperationsAfterShutdown(t *testing.T) {
	backend := &lifecycleAdminBackend{}
	runtime := &nativeQueueRuntime{
		common: &queueCommon{
			inner: backend,
			cfg:   Config{DefaultQueue: "default"},
		},
		nativeQueueRuntimeState: &nativeQueueRuntimeState{
			registered: make(map[string]Handler),
			closed:     true,
		},
	}
	admin := resolveQueueAdmin(runtime)
	if admin == nil {
		t.Fatal("resolved native admin is nil")
	}
	if err := admin.CancelJob(context.Background(), "job-1"); !errors.Is(err, ErrQueuerShuttingDown) {
		t.Fatalf("CancelJob error = %v, want %v", err, ErrQueuerShuttingDown)
	}
	if backend.calls != 0 {
		t.Fatalf("backend calls = %d, want none after shutdown", backend.calls)
	}
}
