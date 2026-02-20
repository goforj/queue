package bus_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/goforj/queue"
	"github.com/goforj/queue/bus"
)

func TestMiddlewareOrder(t *testing.T) {
	q, err := queue.NewSync()
	if err != nil {
		t.Fatalf("new sync queue: %v", err)
	}
	order := make([]string, 0, 4)
	m1 := bus.MiddlewareFunc(func(ctx context.Context, jc bus.Context, next bus.Next) error {
		order = append(order, "m1-before")
		err := next(ctx, jc)
		order = append(order, "m1-after")
		return err
	})
	m2 := bus.MiddlewareFunc(func(ctx context.Context, jc bus.Context, next bus.Next) error {
		order = append(order, "m2-before")
		err := next(ctx, jc)
		order = append(order, "m2-after")
		return err
	})
	b, err := bus.New(q, bus.WithMiddleware(m1, m2))
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}
	if err := b.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers: %v", err)
	}
	b.Register("monitor:poll", func(context.Context, bus.Context) error {
		order = append(order, "handler")
		return nil
	})
	if _, err := b.Dispatch(context.Background(), bus.NewJob("monitor:poll", nil)); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	expected := []string{"m1-before", "m2-before", "handler", "m2-after", "m1-after"}
	if len(order) != len(expected) {
		t.Fatalf("unexpected order length: got %v want %v", order, expected)
	}
	for i := range expected {
		if order[i] != expected[i] {
			t.Fatalf("unexpected order[%d]=%q want %q full=%v", i, order[i], expected[i], order)
		}
	}
}

func TestSkipWhenMiddlewareSkipsHandler(t *testing.T) {
	q, err := queue.NewSync()
	if err != nil {
		t.Fatalf("new sync queue: %v", err)
	}
	var called bool
	b, err := bus.New(q, bus.WithMiddleware(bus.SkipWhen{
		Predicate: func(context.Context, bus.Context) bool { return true },
	}))
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}
	if err := b.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers: %v", err)
	}
	b.Register("monitor:poll", func(context.Context, bus.Context) error {
		called = true
		return nil
	})
	if _, err := b.Dispatch(context.Background(), bus.NewJob("monitor:poll", nil)); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if called {
		t.Fatal("expected handler not called")
	}
}

func TestFailOnErrorWrapsFatal(t *testing.T) {
	q, err := queue.NewSync()
	if err != nil {
		t.Fatalf("new sync queue: %v", err)
	}
	b, err := bus.New(q, bus.WithMiddleware(bus.FailOnError{}))
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}
	if err := b.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers: %v", err)
	}
	b.Register("monitor:poll", func(context.Context, bus.Context) error {
		return errors.New("boom")
	})
	_, err = b.Dispatch(context.Background(), bus.NewJob("monitor:poll", nil))
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "fatal bus error") {
		t.Fatalf("expected fatal wrapping, got %v", err)
	}
}

type stubRateLimiter struct {
	allowed bool
	err     error
	keySeen string
}

func (s *stubRateLimiter) Allow(_ context.Context, key string) (bool, time.Duration, error) {
	s.keySeen = key
	if s.err != nil {
		return false, 0, s.err
	}
	return s.allowed, 0, nil
}

func TestRateLimitDeniedPreventsHandler(t *testing.T) {
	q, err := queue.NewSync()
	if err != nil {
		t.Fatalf("new sync queue: %v", err)
	}
	limiter := &stubRateLimiter{allowed: false}
	var called bool
	b, err := bus.New(q, bus.WithMiddleware(bus.RateLimit{
		Limiter: limiter,
		Key: func(context.Context, bus.Context) string {
			return "monitor:bucket"
		},
	}))
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}
	if err := b.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers: %v", err)
	}
	b.Register("monitor:poll", func(context.Context, bus.Context) error {
		called = true
		return nil
	})
	if _, err := b.Dispatch(context.Background(), bus.NewJob("monitor:poll", nil)); !errors.Is(err, bus.ErrRateLimited) {
		t.Fatalf("expected ErrRateLimited, got %v", err)
	}
	if called {
		t.Fatal("expected handler not called when rate-limited")
	}
	if limiter.keySeen != "monitor:bucket" {
		t.Fatalf("expected limiter key monitor:bucket, got %q", limiter.keySeen)
	}
}

func TestRateLimitLimiterErrorPropagates(t *testing.T) {
	q, err := queue.NewSync()
	if err != nil {
		t.Fatalf("new sync queue: %v", err)
	}
	limiter := &stubRateLimiter{err: errors.New("limiter unavailable")}
	b, err := bus.New(q, bus.WithMiddleware(bus.RateLimit{
		Limiter: limiter,
	}))
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}
	if err := b.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers: %v", err)
	}
	b.Register("monitor:poll", func(context.Context, bus.Context) error { return nil })
	if _, err := b.Dispatch(context.Background(), bus.NewJob("monitor:poll", nil)); err == nil {
		t.Fatal("expected limiter error")
	}
}

func TestRetryPolicyPassesThrough(t *testing.T) {
	q, err := queue.NewSync()
	if err != nil {
		t.Fatalf("new sync queue: %v", err)
	}
	var called bool
	b, err := bus.New(q, bus.WithMiddleware(bus.RetryPolicy{}))
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}
	if err := b.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers: %v", err)
	}
	b.Register("monitor:poll", func(context.Context, bus.Context) error {
		called = true
		return nil
	})
	if _, err := b.Dispatch(context.Background(), bus.NewJob("monitor:poll", nil)); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if !called {
		t.Fatal("expected handler called")
	}
}

type stubLock struct {
	released bool
}

func (l *stubLock) Release(context.Context) error {
	l.released = true
	return nil
}

type stubLocker struct {
	ok      bool
	err     error
	lock    *stubLock
	keySeen string
	ttlSeen time.Duration
}

func (s *stubLocker) Acquire(_ context.Context, key string, ttl time.Duration) (bus.Lock, bool, error) {
	s.keySeen = key
	s.ttlSeen = ttl
	if s.err != nil {
		return nil, false, s.err
	}
	if s.lock == nil {
		s.lock = &stubLock{}
	}
	return s.lock, s.ok, nil
}

func TestWithoutOverlappingDeniedPreventsHandler(t *testing.T) {
	q, err := queue.NewSync()
	if err != nil {
		t.Fatalf("new sync queue: %v", err)
	}
	locker := &stubLocker{ok: false}
	var called bool
	b, err := bus.New(q, bus.WithMiddleware(bus.WithoutOverlapping{
		Locker: locker,
		TTL:    30 * time.Second,
		Key: func(context.Context, bus.Context) string {
			return "monitor:poll:key"
		},
	}))
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}
	if err := b.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers: %v", err)
	}
	b.Register("monitor:poll", func(context.Context, bus.Context) error {
		called = true
		return nil
	})
	if _, err := b.Dispatch(context.Background(), bus.NewJob("monitor:poll", nil)); !errors.Is(err, bus.ErrOverlapping) {
		t.Fatalf("expected ErrOverlapping, got %v", err)
	}
	if called {
		t.Fatal("expected handler not called when overlap denied")
	}
	if locker.keySeen != "monitor:poll:key" {
		t.Fatalf("expected locker key monitor:poll:key, got %q", locker.keySeen)
	}
	if locker.ttlSeen != 30*time.Second {
		t.Fatalf("expected ttl 30s, got %v", locker.ttlSeen)
	}
}

func TestWithoutOverlappingAcquireErrorPropagates(t *testing.T) {
	q, err := queue.NewSync()
	if err != nil {
		t.Fatalf("new sync queue: %v", err)
	}
	locker := &stubLocker{err: errors.New("locker unavailable")}
	b, err := bus.New(q, bus.WithMiddleware(bus.WithoutOverlapping{
		Locker: locker,
	}))
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}
	if err := b.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers: %v", err)
	}
	b.Register("monitor:poll", func(context.Context, bus.Context) error { return nil })
	if _, err := b.Dispatch(context.Background(), bus.NewJob("monitor:poll", nil)); err == nil {
		t.Fatal("expected locker acquire error")
	}
}

func TestWithoutOverlappingReleasesLockAfterHandler(t *testing.T) {
	q, err := queue.NewSync()
	if err != nil {
		t.Fatalf("new sync queue: %v", err)
	}
	lock := &stubLock{}
	locker := &stubLocker{ok: true, lock: lock}
	b, err := bus.New(q, bus.WithMiddleware(bus.WithoutOverlapping{
		Locker: locker,
		TTL:    5 * time.Second,
	}))
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}
	if err := b.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers: %v", err)
	}
	b.Register("monitor:poll", func(context.Context, bus.Context) error {
		return nil
	})
	if _, err := b.Dispatch(context.Background(), bus.NewJob("monitor:poll", nil)); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if !lock.released {
		t.Fatal("expected lock released after handler")
	}
}

func TestWithoutOverlappingReleasesLockAfterHandlerError(t *testing.T) {
	q, err := queue.NewSync()
	if err != nil {
		t.Fatalf("new sync queue: %v", err)
	}
	lock := &stubLock{}
	locker := &stubLocker{ok: true, lock: lock}
	b, err := bus.New(q, bus.WithMiddleware(bus.WithoutOverlapping{
		Locker: locker,
		TTL:    5 * time.Second,
	}))
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}
	if err := b.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers: %v", err)
	}
	b.Register("monitor:poll", func(context.Context, bus.Context) error {
		return errors.New("handler failed")
	})
	if _, err := b.Dispatch(context.Background(), bus.NewJob("monitor:poll", nil)); err == nil {
		t.Fatal("expected dispatch error")
	}
	if !lock.released {
		t.Fatal("expected lock released after handler error")
	}
}
