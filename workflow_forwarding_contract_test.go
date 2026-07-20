package queue

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/goforj/queue/internal/workflow"
)

type workflowRateLimiterFunc func(context.Context, string) (bool, time.Duration, error)

// Allow invokes the test rate-limiter function.
func (f workflowRateLimiterFunc) Allow(ctx context.Context, key string) (bool, time.Duration, error) {
	return f(ctx, key)
}

type workflowLockerFunc func(context.Context, string, time.Duration) (Lock, bool, error)

// Acquire invokes the test locker function.
func (f workflowLockerFunc) Acquire(ctx context.Context, key string, ttl time.Duration) (Lock, bool, error) {
	return f(ctx, key, ttl)
}

type workflowTestLock struct {
	release func(context.Context) error
}

// Release invokes the test lock release function.
func (l *workflowTestLock) Release(ctx context.Context) error {
	return l.release(ctx)
}

type workflowCancelStore struct {
	workflow.Store
	receivedContext context.Context
	receivedBatchID string
	err             error
}

// CancelBatch records the forwarded call and preserves its configured error identity.
func (s *workflowCancelStore) CancelBatch(ctx context.Context, batchID string) error {
	s.receivedContext = ctx
	s.receivedBatchID = batchID
	return s.err
}

// TestNewSQLStoreForwardsConstructionError verifies the root constructor does not reinterpret engine errors.
func TestNewSQLStoreForwardsConstructionError(t *testing.T) {
	store, err := NewSQLStore(SQLStoreConfig{})
	if store != nil {
		t.Fatalf("store = %T, want nil on construction failure", store)
	}
	want := "sql store driver name is required"
	if err == nil || err.Error() != want {
		t.Fatalf("construction error = %v, want %q", err, want)
	}
}

// TestWorkflowStoreViewCancelBatchForwardsUnchanged verifies the built-in view is transparent to callers.
func TestWorkflowStoreViewCancelBatchForwardsUnchanged(t *testing.T) {
	type contextKey struct{}

	wantErr := errors.New("cancel failed")
	store := &workflowCancelStore{err: wantErr}
	view := &workflowStoreView{store: store}
	ctx := context.WithValue(context.Background(), contextKey{}, "request")

	if err := view.CancelBatch(ctx, "batch-17"); err != wantErr {
		t.Fatalf("cancel error = %v, want exact error %v", err, wantErr)
	}
	if store.receivedBatchID != "batch-17" {
		t.Fatalf("batch id = %q, want batch-17", store.receivedBatchID)
	}
	if got := store.receivedContext.Value(contextKey{}); got != "request" {
		t.Fatalf("context value = %v, want request", got)
	}
}

// TestSkipWhenBranches pins pass-through and suppression behavior for every predicate outcome.
func TestSkipWhenBranches(t *testing.T) {
	message := NewMessage("reports:build", []byte(`{"id":7}`))
	nextErr := errors.New("next failed")

	tests := []struct {
		name       string
		predicate  func(context.Context, Message) bool
		wantErr    error
		wantCalled bool
	}{
		{name: "nil predicate", wantErr: nextErr, wantCalled: true},
		{name: "false predicate", predicate: func(context.Context, Message) bool { return false }, wantErr: nextErr, wantCalled: true},
		{name: "true predicate", predicate: func(context.Context, Message) bool { return true }, wantCalled: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			called := false
			err := (SkipWhen{Predicate: test.predicate}).Handle(context.Background(), message, func(context.Context, Message) error {
				called = true
				return nextErr
			})
			if err != test.wantErr {
				t.Fatalf("handle error = %v, want exact error %v", err, test.wantErr)
			}
			if called != test.wantCalled {
				t.Fatalf("next called = %t, want %t", called, test.wantCalled)
			}
		})
	}
}

// TestFailOnErrorBranches pins successful, retryable, and permanent error paths.
func TestFailOnErrorBranches(t *testing.T) {
	message := NewMessage("reports:build", nil)
	nextErr := errors.New("next failed")

	t.Run("success bypasses predicate", func(t *testing.T) {
		predicateCalled := false
		middleware := FailOnError{When: func(error) bool {
			predicateCalled = true
			return true
		}}
		if err := middleware.Handle(context.Background(), message, func(context.Context, Message) error { return nil }); err != nil {
			t.Fatalf("handle success: %v", err)
		}
		if predicateCalled {
			t.Fatal("predicate called for successful execution")
		}
	})

	t.Run("unmatched error remains unchanged", func(t *testing.T) {
		middleware := FailOnError{When: func(error) bool { return false }}
		if err := middleware.Handle(context.Background(), message, func(context.Context, Message) error { return nextErr }); err != nextErr {
			t.Fatalf("handle error = %v, want exact error %v", err, nextErr)
		}
	})

	for _, test := range []struct {
		name string
		when func(error) bool
	}{
		{name: "nil predicate"},
		{name: "matched predicate", when: func(error) bool { return true }},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := (FailOnError{When: test.when}).Handle(context.Background(), message, func(context.Context, Message) error { return nextErr })
			if !IsPermanent(err) {
				t.Fatalf("handle error = %v, want permanent classification", err)
			}
			if !errors.Is(err, nextErr) {
				t.Fatalf("handle error = %v, want wrapped error %v", err, nextErr)
			}
		})
	}
}

// TestRateLimitBranches pins key resolution, limiter failures, denial, and pass-through behavior.
func TestRateLimitBranches(t *testing.T) {
	message := NewMessage("reports:build", nil)
	nextErr := errors.New("next failed")
	limiterErr := errors.New("limiter failed")

	t.Run("nil limiter", func(t *testing.T) {
		called := false
		err := (RateLimit{}).Handle(context.Background(), message, func(context.Context, Message) error {
			called = true
			return nextErr
		})
		if err != nextErr || !called {
			t.Fatalf("handle result = (%v, %t), want exact next error and call", err, called)
		}
	})

	for _, test := range []struct {
		name    string
		key     func(context.Context, Message) string
		wantKey string
	}{
		{name: "default key", wantKey: "reports:build"},
		{name: "empty resolved key", key: func(context.Context, Message) string { return "" }, wantKey: "reports:build"},
		{name: "custom key", key: func(context.Context, Message) string { return "tenant:17" }, wantKey: "tenant:17"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var gotKey string
			limiter := workflowRateLimiterFunc(func(_ context.Context, key string) (bool, time.Duration, error) {
				gotKey = key
				return true, time.Second, nil
			})
			err := (RateLimit{Key: test.key, Limiter: limiter}).Handle(context.Background(), message, func(context.Context, Message) error { return nextErr })
			if err != nextErr {
				t.Fatalf("handle error = %v, want exact next error %v", err, nextErr)
			}
			if gotKey != test.wantKey {
				t.Fatalf("limiter key = %q, want %q", gotKey, test.wantKey)
			}
		})
	}

	t.Run("limiter error", func(t *testing.T) {
		called := false
		limiter := workflowRateLimiterFunc(func(context.Context, string) (bool, time.Duration, error) {
			return false, 0, limiterErr
		})
		err := (RateLimit{Limiter: limiter}).Handle(context.Background(), message, func(context.Context, Message) error {
			called = true
			return nil
		})
		if err != limiterErr || called {
			t.Fatalf("handle result = (%v, %t), want exact limiter error without next call", err, called)
		}
	})

	t.Run("denied", func(t *testing.T) {
		called := false
		limiter := workflowRateLimiterFunc(func(context.Context, string) (bool, time.Duration, error) {
			return false, time.Second, nil
		})
		err := (RateLimit{Limiter: limiter}).Handle(context.Background(), message, func(context.Context, Message) error {
			called = true
			return nil
		})
		if err != ErrRateLimited || called {
			t.Fatalf("handle result = (%v, %t), want exact ErrRateLimited without next call", err, called)
		}
	})
}

// TestWithoutOverlappingBranches pins key resolution, acquisition outcomes, and release behavior.
func TestWithoutOverlappingBranches(t *testing.T) {
	message := NewMessage("reports:build", nil)
	nextErr := errors.New("next failed")
	lockerErr := errors.New("locker failed")
	releaseErr := errors.New("release failed")

	t.Run("nil locker", func(t *testing.T) {
		called := false
		err := (WithoutOverlapping{}).Handle(context.Background(), message, func(context.Context, Message) error {
			called = true
			return nextErr
		})
		if err != nextErr || !called {
			t.Fatalf("handle result = (%v, %t), want exact next error and call", err, called)
		}
	})

	t.Run("acquire error uses default key", func(t *testing.T) {
		var gotKey string
		locker := workflowLockerFunc(func(_ context.Context, key string, _ time.Duration) (Lock, bool, error) {
			gotKey = key
			return nil, false, lockerErr
		})
		err := (WithoutOverlapping{Locker: locker}).Handle(context.Background(), message, func(context.Context, Message) error { return nil })
		if err != lockerErr || gotKey != "reports:build" {
			t.Fatalf("handle result = (%v, %q), want exact locker error and default key", err, gotKey)
		}
	})

	t.Run("denied uses default for empty resolved key", func(t *testing.T) {
		called := false
		var gotKey string
		locker := workflowLockerFunc(func(_ context.Context, key string, _ time.Duration) (Lock, bool, error) {
			gotKey = key
			return nil, false, nil
		})
		err := (WithoutOverlapping{
			Key:    func(context.Context, Message) string { return "" },
			Locker: locker,
		}).Handle(context.Background(), message, func(context.Context, Message) error {
			called = true
			return nil
		})
		if err != ErrOverlapping || called || gotKey != "reports:build" {
			t.Fatalf("handle result = (%v, %t, %q), want exact ErrOverlapping, no next call, and default key", err, called, gotKey)
		}
	})

	t.Run("acquired custom key releases after next", func(t *testing.T) {
		const ttl = 3 * time.Second
		var gotKey string
		var gotTTL time.Duration
		released := false
		lock := &workflowTestLock{release: func(context.Context) error {
			released = true
			return releaseErr
		}}
		locker := workflowLockerFunc(func(_ context.Context, key string, requestedTTL time.Duration) (Lock, bool, error) {
			gotKey = key
			gotTTL = requestedTTL
			return lock, true, nil
		})
		err := (WithoutOverlapping{
			Key:    func(context.Context, Message) string { return "tenant:17" },
			TTL:    ttl,
			Locker: locker,
		}).Handle(context.Background(), message, func(context.Context, Message) error { return nextErr })
		if err != nextErr {
			t.Fatalf("handle error = %v, want exact next error %v", err, nextErr)
		}
		if gotKey != "tenant:17" || gotTTL != ttl || !released {
			t.Fatalf("lock lifecycle = (%q, %s, %t), want custom key, ttl, and release", gotKey, gotTTL, released)
		}
	})
}

// TestRetryPolicyPassesThroughUnchanged verifies retry ownership stays with the worker runtime.
func TestRetryPolicyPassesThroughUnchanged(t *testing.T) {
	type contextKey struct{}

	wantErr := errors.New("next failed")
	wantMessage := NewMessage("reports:build", []byte(`{"id":7}`))
	ctx := context.WithValue(context.Background(), contextKey{}, "request")
	called := false
	err := (RetryPolicy{}).Handle(ctx, wantMessage, func(gotContext context.Context, gotMessage Message) error {
		called = true
		if got := gotContext.Value(contextKey{}); got != "request" {
			t.Errorf("context value = %v, want request", got)
		}
		if gotMessage.JobType != wantMessage.JobType || string(gotMessage.PayloadBytes()) != string(wantMessage.PayloadBytes()) {
			t.Errorf("message = %+v, want unchanged message %+v", gotMessage, wantMessage)
		}
		return wantErr
	})
	if err != wantErr || !called {
		t.Fatalf("handle result = (%v, %t), want exact next error and call", err, called)
	}
}

// TestWorkflowAdaptersPreserveNilAndEmptyCollectionsAndPayloads pins nil as distinct from an allocated empty value.
func TestWorkflowAdaptersPreserveNilAndEmptyCollectionsAndPayloads(t *testing.T) {
	if payload := NewMessage("reports:build", nil).PayloadBytes(); payload != nil {
		t.Fatalf("new message payload = %#v, want nil", payload)
	}
	if payload := messageFromWorkflow(workflow.NewContext(1, "", "", "", "", 0, "reports:build", nil)).PayloadBytes(); payload != nil {
		t.Fatalf("message from workflow payload = %#v, want nil", payload)
	}
	if payload := messageToWorkflow(NewMessage("reports:build", nil)).PayloadBytes(); payload != nil {
		t.Fatalf("message to workflow payload = %#v, want nil", payload)
	}
	if payload := storedJobToWorkflow(StoredJob{}).Payload; payload != nil {
		t.Fatalf("stored job to workflow payload = %#v, want nil", payload)
	}
	if payload := storedJobFromWorkflow(workflow.StoredJob{}).Payload; payload != nil {
		t.Fatalf("stored job from workflow payload = %#v, want nil", payload)
	}
	emptyMessage := NewMessage("reports:build", []byte{})
	if payload := messageToWorkflow(emptyMessage).PayloadBytes(); payload == nil || len(payload) != 0 {
		t.Fatalf("empty message to workflow payload = %#v, want non-nil empty slice", payload)
	}
	if payload := messageFromWorkflow(workflow.NewContext(1, "", "", "", "", 0, "reports:build", []byte{})).PayloadBytes(); payload == nil || len(payload) != 0 {
		t.Fatalf("empty message from workflow payload = %#v, want non-nil empty slice", payload)
	}
	if payload := storedJobToWorkflow(StoredJob{Payload: []byte{}}).Payload; payload == nil || len(payload) != 0 {
		t.Fatalf("empty stored job to workflow payload = %#v, want non-nil empty slice", payload)
	}
	if payload := storedJobFromWorkflow(workflow.StoredJob{Payload: []byte{}}).Payload; payload == nil || len(payload) != 0 {
		t.Fatalf("empty stored job from workflow payload = %#v, want non-nil empty slice", payload)
	}

	if nodes := chainNodesToWorkflow(nil); nodes != nil {
		t.Fatalf("chain nodes to workflow = %#v, want nil", nodes)
	}
	if nodes := chainNodesFromWorkflow(nil); nodes != nil {
		t.Fatalf("chain nodes from workflow = %#v, want nil", nodes)
	}
	if jobs := batchJobsToWorkflow(nil); jobs != nil {
		t.Fatalf("batch jobs to workflow = %#v, want nil", jobs)
	}
	if jobs := batchJobsFromWorkflow(nil); jobs != nil {
		t.Fatalf("batch jobs from workflow = %#v, want nil", jobs)
	}
}

// TestMiddlewaresToWorkflowPreservesNilAndFiltersNilEntries verifies optional middleware lists compose safely.
func TestMiddlewaresToWorkflowPreservesNilAndFiltersNilEntries(t *testing.T) {
	if converted := middlewaresToWorkflow(nil); converted != nil {
		t.Fatalf("nil middleware conversion = %#v, want nil", converted)
	}

	called := false
	middleware := MiddlewareFunc(func(ctx context.Context, message Message, next Next) error {
		called = true
		return next(ctx, message)
	})
	converted := middlewaresToWorkflow([]Middleware{nil, middleware, nil})
	if len(converted) != 1 {
		t.Fatalf("converted middleware count = %d, want 1", len(converted))
	}
	if err := converted[0].Handle(context.Background(), workflow.NewContext(1, "", "", "", "", 0, "reports:build", nil), func(context.Context, workflow.Context) error { return nil }); err != nil {
		t.Fatalf("run converted middleware: %v", err)
	}
	if !called {
		t.Fatal("non-nil middleware was not retained")
	}
}

// TestNilWorkflowCallbackAdaptersRemainNil verifies absent callbacks do not become callable wrappers.
func TestNilWorkflowCallbackAdaptersRemainNil(t *testing.T) {
	if chainCatchToWorkflow(nil) != nil {
		t.Fatal("nil chain catch callback became non-nil")
	}
	if chainFinallyToWorkflow(nil) != nil {
		t.Fatal("nil chain finally callback became non-nil")
	}
	if batchStateCallbackToWorkflow(nil) != nil {
		t.Fatal("nil batch state callback became non-nil")
	}
	if batchCatchToWorkflow(nil) != nil {
		t.Fatal("nil batch catch callback became non-nil")
	}
}
