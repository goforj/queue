package queue

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

type contractFactory struct {
	name                     string
	newDispatcher            func(t *testing.T) Dispatcher
	requiresRegisteredHandle bool
	assertMissingHandlerErr  bool
	uniqueTTL                time.Duration
	uniqueExpiryWait         time.Duration
	beforeEach               func(t *testing.T)
}

func runDispatcherContractSuite(t *testing.T, factory contractFactory) {
	t.Helper()

	t.Run("lifecycle_start_shutdown", func(t *testing.T) {
		d := factory.newDispatcher(t)
		t.Cleanup(func() { _ = d.Shutdown(context.Background()) })
		if err := d.Start(context.Background()); err != nil {
			t.Fatalf("start failed: %v", err)
		}
		if factory.beforeEach != nil {
			factory.beforeEach(t)
		}
		if err := d.Shutdown(context.Background()); err != nil {
			t.Fatalf("shutdown failed: %v", err)
		}
	})

	t.Run("enqueue_immediate", func(t *testing.T) {
		d := factory.newDispatcher(t)
		t.Cleanup(func() { _ = d.Shutdown(context.Background()) })
		if factory.requiresRegisteredHandle {
			d.Register("job:contract:immediate", func(_ context.Context, _ Task) error { return nil })
		}
		if err := d.Start(context.Background()); err != nil {
			t.Fatalf("start failed: %v", err)
		}
		if factory.beforeEach != nil {
			factory.beforeEach(t)
		}
		err := d.Enqueue(context.Background(), Task{Type: "job:contract:immediate", Payload: []byte("ok")})
		if err != nil {
			t.Fatalf("immediate enqueue failed: %v", err)
		}
	})

	t.Run("enqueue_delayed", func(t *testing.T) {
		d := factory.newDispatcher(t)
		t.Cleanup(func() { _ = d.Shutdown(context.Background()) })
		if factory.requiresRegisteredHandle {
			d.Register("job:contract:delay", func(_ context.Context, _ Task) error { return nil })
		}
		if err := d.Start(context.Background()); err != nil {
			t.Fatalf("start failed: %v", err)
		}
		if factory.beforeEach != nil {
			factory.beforeEach(t)
		}
		err := d.Enqueue(
			context.Background(),
			Task{Type: "job:contract:delay", Payload: []byte("delayed")},
			WithDelay(20*time.Millisecond),
		)
		if err != nil {
			t.Fatalf("delayed enqueue failed: %v", err)
		}
	})

	t.Run("unique_duplicate_and_ttl_expiry", func(t *testing.T) {
		d := factory.newDispatcher(t)
		t.Cleanup(func() { _ = d.Shutdown(context.Background()) })
		if factory.requiresRegisteredHandle {
			d.Register("job:contract:unique", func(_ context.Context, _ Task) error { return nil })
		}
		if err := d.Start(context.Background()); err != nil {
			t.Fatalf("start failed: %v", err)
		}
		if factory.beforeEach != nil {
			factory.beforeEach(t)
		}
		ttl := factory.uniqueTTL
		if ttl <= 0 {
			ttl = 120 * time.Millisecond
		}
		expiryWait := factory.uniqueExpiryWait
		if expiryWait <= 0 {
			expiryWait = ttl + 30*time.Millisecond
		}
		task := Task{Type: "job:contract:unique", Payload: []byte("same")}
		err := d.Enqueue(context.Background(), task, WithUnique(ttl))
		if err != nil {
			t.Fatalf("first unique enqueue failed: %v", err)
		}
		err = d.Enqueue(context.Background(), task, WithUnique(ttl))
		if !errors.Is(err, ErrDuplicate) {
			t.Fatalf("expected ErrDuplicate, got %v", err)
		}
		time.Sleep(expiryWait)
		err = d.Enqueue(context.Background(), task, WithUnique(ttl))
		if err != nil {
			t.Fatalf("enqueue after unique ttl failed: %v", err)
		}
	})

	t.Run("missing_task_type", func(t *testing.T) {
		d := factory.newDispatcher(t)
		t.Cleanup(func() { _ = d.Shutdown(context.Background()) })
		err := d.Enqueue(context.Background(), Task{})
		if err == nil {
			t.Fatal("expected missing task type error")
		}
	})

	t.Run("missing_handler", func(t *testing.T) {
		d := factory.newDispatcher(t)
		t.Cleanup(func() { _ = d.Shutdown(context.Background()) })
		if err := d.Start(context.Background()); err != nil {
			t.Fatalf("start failed: %v", err)
		}
		if factory.beforeEach != nil {
			factory.beforeEach(t)
		}
		err := d.Enqueue(context.Background(), Task{Type: "job:contract:missing-handler"})
		if factory.assertMissingHandlerErr && err == nil {
			t.Fatal("expected missing handler error")
		}
		if !factory.assertMissingHandlerErr && err != nil {
			t.Fatalf("unexpected missing handler error: %v", err)
		}
	})
}

func TestDispatcherContract_LocalAndSQLite(t *testing.T) {
	factories := []contractFactory{
		{
			name: "sync",
			newDispatcher: func(_ *testing.T) Dispatcher {
				dispatcher, err := NewDispatcher(Config{Driver: DriverSync})
				if err != nil {
					t.Fatalf("new sync dispatcher failed: %v", err)
				}
				return dispatcher
			},
			requiresRegisteredHandle: true,
			assertMissingHandlerErr:  true,
		},
		{
			name: "workerpool",
			newDispatcher: func(_ *testing.T) Dispatcher {
				dispatcher, err := NewDispatcher(Config{
					Driver:     DriverWorkerpool,
					Workerpool: WorkerpoolConfig{Workers: 1, Buffer: 4},
				})
				if err != nil {
					t.Fatalf("new workerpool dispatcher failed: %v", err)
				}
				return dispatcher
			},
			requiresRegisteredHandle: true,
			assertMissingHandlerErr:  true,
		},
		{
			name: "database-sqlite",
			newDispatcher: func(t *testing.T) Dispatcher {
				dispatcher, err := NewDispatcher(Config{
					Driver: DriverDatabase,
					Database: DatabaseConfig{
						DriverName:   "sqlite",
						DSN:          fmt.Sprintf("%s/contract-%d.db", t.TempDir(), time.Now().UnixNano()),
						Workers:      1,
						PollInterval: 10 * time.Millisecond,
					},
				})
				if err != nil {
					t.Fatalf("new sqlite dispatcher failed: %v", err)
				}
				return dispatcher
			},
			requiresRegisteredHandle: true,
			assertMissingHandlerErr:  true,
		},
	}

	for _, factory := range factories {
		factory := factory
		t.Run(factory.name, func(t *testing.T) {
			contractFactory := factory
			contractFactory.newDispatcher = func(t *testing.T) Dispatcher {
				d := factory.newDispatcher(t)
				t.Cleanup(func() {
					_ = d.Shutdown(context.Background())
				})
				return d
			}
			runDispatcherContractSuite(t, contractFactory)
		})
	}
}
