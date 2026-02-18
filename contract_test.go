package queue

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type contractFactory struct {
	name                     string
	newDispatcher            func(t *testing.T) Dispatcher
	requiresRegisteredHandle bool
	assertMissingHandlerErr  bool
	backoffUnsupported       bool
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

	t.Run("enqueue_with_queue", func(t *testing.T) {
		d := factory.newDispatcher(t)
		t.Cleanup(func() { _ = d.Shutdown(context.Background()) })
		if factory.requiresRegisteredHandle {
			d.Register("job:contract:queue", func(_ context.Context, _ Task) error { return nil })
		}
		if err := d.Start(context.Background()); err != nil {
			t.Fatalf("start failed: %v", err)
		}
		if factory.beforeEach != nil {
			factory.beforeEach(t)
		}
		err := d.Enqueue(
			context.Background(),
			Task{Type: "job:contract:queue", Payload: []byte("queue")},
			WithQueue("contract"),
		)
		if err != nil {
			t.Fatalf("enqueue with queue failed: %v", err)
		}
	})

	t.Run("enqueue_with_timeout", func(t *testing.T) {
		d := factory.newDispatcher(t)
		t.Cleanup(func() { _ = d.Shutdown(context.Background()) })
		timeoutChecked := make(chan bool, 1)
		if factory.requiresRegisteredHandle {
			d.Register("job:contract:timeout", func(ctx context.Context, _ Task) error {
				_, ok := ctx.Deadline()
				timeoutChecked <- ok
				return nil
			})
		}
		if err := d.Start(context.Background()); err != nil {
			t.Fatalf("start failed: %v", err)
		}
		if factory.beforeEach != nil {
			factory.beforeEach(t)
		}
		err := d.Enqueue(
			context.Background(),
			Task{Type: "job:contract:timeout", Payload: []byte("timeout")},
			WithTimeout(80*time.Millisecond),
		)
		if err != nil {
			t.Fatalf("enqueue with timeout failed: %v", err)
		}
		if factory.requiresRegisteredHandle {
			select {
			case ok := <-timeoutChecked:
				if !ok {
					t.Fatal("expected handler context to include a deadline")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("timeout handler was not invoked")
			}
		}
	})

	t.Run("enqueue_with_max_retry", func(t *testing.T) {
		d := factory.newDispatcher(t)
		t.Cleanup(func() { _ = d.Shutdown(context.Background()) })
		var calls atomic.Int32
		done := make(chan struct{}, 1)
		if factory.requiresRegisteredHandle {
			d.Register("job:contract:maxretry", func(_ context.Context, _ Task) error {
				if calls.Add(1) < 3 {
					return errors.New("transient")
				}
				done <- struct{}{}
				return nil
			})
		}
		if err := d.Start(context.Background()); err != nil {
			t.Fatalf("start failed: %v", err)
		}
		if factory.beforeEach != nil {
			factory.beforeEach(t)
		}
		err := d.Enqueue(
			context.Background(),
			Task{Type: "job:contract:maxretry", Payload: []byte("retry")},
			WithMaxRetry(2),
		)
		if err != nil {
			t.Fatalf("enqueue with max retry failed: %v", err)
		}
		if factory.requiresRegisteredHandle {
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("max-retry handler was not invoked")
			}
			if got := calls.Load(); got != 3 {
				t.Fatalf("expected 3 attempts, got %d", got)
			}
		}
	})

	t.Run("enqueue_with_backoff_behavior", func(t *testing.T) {
		d := factory.newDispatcher(t)
		t.Cleanup(func() { _ = d.Shutdown(context.Background()) })
		start := time.Now()
		done := make(chan time.Duration, 1)
		var calls atomic.Int32
		if factory.requiresRegisteredHandle {
			d.Register("job:contract:backoff", func(_ context.Context, _ Task) error {
				if calls.Add(1) < 2 {
					return errors.New("retry-me")
				}
				done <- time.Since(start)
				return nil
			})
		}
		if err := d.Start(context.Background()); err != nil {
			t.Fatalf("start failed: %v", err)
		}
		if factory.beforeEach != nil {
			factory.beforeEach(t)
		}
		err := d.Enqueue(
			context.Background(),
			Task{Type: "job:contract:backoff", Payload: []byte("backoff")},
			WithMaxRetry(1),
			WithBackoff(10*time.Millisecond),
		)
		if factory.backoffUnsupported {
			if !errors.Is(err, ErrBackoffUnsupported) {
				t.Fatalf("expected ErrBackoffUnsupported, got %v", err)
			}
			return
		}
		if err != nil {
			t.Fatalf("enqueue with backoff failed: %v", err)
		}
		if factory.requiresRegisteredHandle {
			select {
			case elapsed := <-done:
				if elapsed < 8*time.Millisecond {
					t.Fatalf("expected retry backoff delay, got %s", elapsed)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("backoff retry handler was not invoked")
			}
			if got := calls.Load(); got != 2 {
				t.Fatalf("expected 2 attempts, got %d", got)
			}
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

func TestOptionContractCoverage_AllDriversAccountedFor(t *testing.T) {
	declared := declaredDriversFromSource(t)
	accounted := map[Driver]string{
		DriverSync:       "local contract suite",
		DriverWorkerpool: "local contract suite",
		DriverDatabase:   "local/integration database contract suites",
		DriverRedis:      "integration redis contract suite",
	}
	for _, d := range declared {
		if _, ok := accounted[d]; !ok {
			t.Fatalf("driver %q is not mapped to an option contract suite; update option coverage tests", d)
		}
	}
}

func declaredDriversFromSource(t *testing.T) []Driver {
	t.Helper()
	file := filepath.Join(".", "driver.go")
	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, file, nil, 0)
	if err != nil {
		t.Fatalf("parse driver.go failed: %v", err)
	}
	var out []Driver
	for _, decl := range node.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range vs.Names {
				if !strings.HasPrefix(name.Name, "Driver") || name.Name == "Driver" {
					continue
				}
				if i >= len(vs.Values) {
					continue
				}
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok {
					continue
				}
				val := strings.Trim(lit.Value, `"`)
				if val == "" {
					continue
				}
				out = append(out, Driver(val))
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func TestDispatcherContract_LocalAndSQLite(t *testing.T) {
	factories := []contractFactory{
		{
			name: "sync",
			newDispatcher: func(_ *testing.T) Dispatcher {
				dispatcher, err := NewDispatcher(DispatcherConfig{Driver: DriverSync})
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
				dispatcher, err := NewDispatcher(DispatcherConfig{
					Driver: DriverWorkerpool,
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
				dispatcher, err := NewDispatcher(DispatcherConfig{
					Driver:         DriverDatabase,
					DatabaseDriver: "sqlite",
					DatabaseDSN:    fmt.Sprintf("%s/contract-%d.db", t.TempDir(), time.Now().UnixNano()),
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
