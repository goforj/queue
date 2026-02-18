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
	newQueue                 func(t *testing.T) Queue
	requiresRegisteredHandle bool
	requiresQueueName        bool
	assertMissingHandlerErr  bool
	backoffUnsupported       bool
	uniqueTTL                time.Duration
	uniqueExpiryWait         time.Duration
	beforeEach               func(t *testing.T)
}

func runQueueContractSuite(t *testing.T, factory contractFactory) {
	t.Helper()

	t.Run("lifecycle_start_shutdown", func(t *testing.T) {
		d := factory.newQueue(t)
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
		d := factory.newQueue(t)
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
		err := d.Enqueue(context.Background(), NewTask("job:contract:immediate").Payload([]byte("ok")).OnQueue("default"))
		if err != nil {
			t.Fatalf("immediate enqueue failed: %v", err)
		}
	})

	t.Run("enqueue_delayed", func(t *testing.T) {
		d := factory.newQueue(t)
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
		err := d.Enqueue(context.Background(), NewTask("job:contract:delay").Payload([]byte("delayed")).OnQueue("default").Delay(20*time.Millisecond))
		if err != nil {
			t.Fatalf("delayed enqueue failed: %v", err)
		}
	})

	t.Run("enqueue_with_queue", func(t *testing.T) {
		d := factory.newQueue(t)
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
		err := d.Enqueue(context.Background(), NewTask("job:contract:queue").Payload([]byte("queue")).OnQueue("contract"))
		if err != nil {
			t.Fatalf("enqueue with queue failed: %v", err)
		}
	})

	t.Run("enqueue_without_queue_behavior", func(t *testing.T) {
		d := factory.newQueue(t)
		t.Cleanup(func() { _ = d.Shutdown(context.Background()) })
		if factory.requiresRegisteredHandle {
			d.Register("job:contract:noqueue", func(_ context.Context, _ Task) error { return nil })
		}
		if err := d.Start(context.Background()); err != nil {
			t.Fatalf("start failed: %v", err)
		}
		if factory.beforeEach != nil {
			factory.beforeEach(t)
		}
		err := d.Enqueue(context.Background(), NewTask("job:contract:noqueue").Payload([]byte("no-queue")))
		if factory.requiresQueueName {
			if err == nil {
				t.Fatal("expected missing queue error")
			}
			if !strings.Contains(err.Error(), "task queue is required") {
				t.Fatalf("expected missing queue error, got %v", err)
			}
			return
		}
		if err != nil {
			t.Fatalf("unexpected enqueue without queue error: %v", err)
		}
	})

	t.Run("enqueue_with_nil_context", func(t *testing.T) {
		d := factory.newQueue(t)
		t.Cleanup(func() { _ = d.Shutdown(context.Background()) })
		if factory.requiresRegisteredHandle {
			d.Register("job:contract:nilctx", func(_ context.Context, _ Task) error { return nil })
		}
		if err := d.Start(nil); err != nil {
			t.Fatalf("start with nil context failed: %v", err)
		}
		if factory.beforeEach != nil {
			factory.beforeEach(t)
		}
		err := d.Enqueue(nil, NewTask("job:contract:nilctx").Payload([]byte("ok")).OnQueue("default"))
		if err != nil {
			t.Fatalf("enqueue with nil context failed: %v", err)
		}
		if err := d.Shutdown(nil); err != nil {
			t.Fatalf("shutdown with nil context failed: %v", err)
		}
	})

	t.Run("enqueue_with_timeout", func(t *testing.T) {
		d := factory.newQueue(t)
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
		err := d.Enqueue(context.Background(), NewTask("job:contract:timeout").Payload([]byte("timeout")).OnQueue("default").Timeout(80*time.Millisecond))
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

	t.Run("enqueue_with_invalid_payload_fails", func(t *testing.T) {
		d := factory.newQueue(t)
		t.Cleanup(func() { _ = d.Shutdown(context.Background()) })
		if factory.requiresRegisteredHandle {
			d.Register("job:contract:invalid-payload", func(_ context.Context, _ Task) error { return nil })
		}
		if err := d.Start(context.Background()); err != nil {
			t.Fatalf("start failed: %v", err)
		}
		if factory.beforeEach != nil {
			factory.beforeEach(t)
		}
		task := NewTask("job:contract:invalid-payload").Payload(func() {})
		err := d.Enqueue(context.Background(), task.OnQueue("default"))
		if err == nil {
			t.Fatal("expected payload build error")
		}
		if !strings.Contains(err.Error(), "marshal payload json") {
			t.Fatalf("expected marshal payload json error, got %v", err)
		}
	})

	t.Run("invalid_fluent_option_values", func(t *testing.T) {
		d := factory.newQueue(t)
		t.Cleanup(func() { _ = d.Shutdown(context.Background()) })
		if factory.requiresRegisteredHandle {
			d.Register("job:contract:invalid-values", func(_ context.Context, _ Task) error { return nil })
		}
		if err := d.Start(context.Background()); err != nil {
			t.Fatalf("start failed: %v", err)
		}
		if factory.beforeEach != nil {
			factory.beforeEach(t)
		}
		task := NewTask("job:contract:invalid-values").
			OnQueue("default").
			Timeout(-1 * time.Second).
			Retry(-1).
			Backoff(-1 * time.Second).
			Delay(-1 * time.Second).
			UniqueFor(-1 * time.Second)
		err := d.Enqueue(context.Background(), task)
		if err == nil {
			t.Fatal("expected invalid option value error")
		}
		if !strings.Contains(err.Error(), "must be >= 0") {
			t.Fatalf("expected invalid option value error, got %v", err)
		}
	})

	t.Run("handler_bind_payload", func(t *testing.T) {
		if !factory.requiresRegisteredHandle {
			t.Skip("driver does not register handlers on queue runtime")
		}
		d := factory.newQueue(t)
		t.Cleanup(func() { _ = d.Shutdown(context.Background()) })
		type payload struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		}
		seen := make(chan payload, 1)
		d.Register("job:contract:bind", func(_ context.Context, task Task) error {
			var in payload
			if err := task.Bind(&in); err != nil {
				return err
			}
			seen <- in
			return nil
		})
		if err := d.Start(context.Background()); err != nil {
			t.Fatalf("start failed: %v", err)
		}
		if factory.beforeEach != nil {
			factory.beforeEach(t)
		}
		want := payload{ID: 42, Name: "bind"}
		if err := d.Enqueue(context.Background(), NewTask("job:contract:bind").Payload(want).OnQueue("default")); err != nil {
			t.Fatalf("enqueue bind task failed: %v", err)
		}
		select {
		case got := <-seen:
			if got != want {
				t.Fatalf("bind payload mismatch: got %+v want %+v", got, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("bind handler was not invoked")
		}
	})

	t.Run("enqueue_with_max_retry", func(t *testing.T) {
		d := factory.newQueue(t)
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
		err := d.Enqueue(context.Background(), NewTask("job:contract:maxretry").Payload([]byte("retry")).OnQueue("default").Retry(2))
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
		d := factory.newQueue(t)
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
		err := d.Enqueue(context.Background(), NewTask("job:contract:backoff").Payload([]byte("backoff")).OnQueue("default").Retry(1).Backoff(10*time.Millisecond))
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
		d := factory.newQueue(t)
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
		taskType := "job:contract:unique"
		payload := []byte("same")
		err := d.Enqueue(context.Background(), NewTask(taskType).Payload(payload).OnQueue("default").UniqueFor(ttl))
		if err != nil {
			t.Fatalf("first unique enqueue failed: %v", err)
		}
		err = d.Enqueue(context.Background(), NewTask(taskType).Payload(payload).OnQueue("default").UniqueFor(ttl))
		if !errors.Is(err, ErrDuplicate) {
			t.Fatalf("expected ErrDuplicate, got %v", err)
		}
		time.Sleep(expiryWait)
		err = d.Enqueue(context.Background(), NewTask(taskType).Payload(payload).OnQueue("default").UniqueFor(ttl))
		if err != nil {
			t.Fatalf("enqueue after unique ttl failed: %v", err)
		}
	})

	t.Run("unique_is_scoped_by_queue", func(t *testing.T) {
		d := factory.newQueue(t)
		t.Cleanup(func() { _ = d.Shutdown(context.Background()) })
		if factory.requiresRegisteredHandle {
			d.Register("job:contract:unique-scope", func(_ context.Context, _ Task) error { return nil })
		}
		if err := d.Start(context.Background()); err != nil {
			t.Fatalf("start failed: %v", err)
		}
		if factory.beforeEach != nil {
			factory.beforeEach(t)
		}
		taskType := "job:contract:unique-scope"
		payload := []byte("same")
		ttl := factory.uniqueTTL
		if ttl <= 0 {
			ttl = 400 * time.Millisecond
		}
		first := NewTask(taskType).Payload(payload).OnQueue("queue-a").UniqueFor(ttl)
		if err := d.Enqueue(context.Background(), first); err != nil {
			t.Fatalf("first enqueue failed: %v", err)
		}
		second := NewTask(taskType).Payload(payload).OnQueue("queue-b").UniqueFor(ttl)
		if err := d.Enqueue(context.Background(), second); err != nil {
			t.Fatalf("expected unique lock to be queue scoped, got %v", err)
		}
	})

	t.Run("missing_task_type", func(t *testing.T) {
		d := factory.newQueue(t)
		t.Cleanup(func() { _ = d.Shutdown(context.Background()) })
		err := d.Enqueue(context.Background(), NewTask("").OnQueue("default"))
		if err == nil {
			t.Fatal("expected missing task type error")
		}
	})

	t.Run("missing_handler", func(t *testing.T) {
		d := factory.newQueue(t)
		t.Cleanup(func() { _ = d.Shutdown(context.Background()) })
		if err := d.Start(context.Background()); err != nil {
			t.Fatalf("start failed: %v", err)
		}
		if factory.beforeEach != nil {
			factory.beforeEach(t)
		}
		err := d.Enqueue(context.Background(), NewTask("job:contract:missing-handler").OnQueue("default"))
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
		DriverNATS:       "integration nats contract suite",
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

func TestQueueContract_LocalAndSQLite(t *testing.T) {
	factories := []contractFactory{
		{
			name: "sync",
			newQueue: func(_ *testing.T) Queue {
				q, err := New(Config{Driver: DriverSync})
				if err != nil {
					t.Fatalf("new sync q failed: %v", err)
				}
				return q
			},
			requiresRegisteredHandle: true,
			requiresQueueName:        false,
			assertMissingHandlerErr:  true,
		},
		{
			name: "workerpool",
			newQueue: func(_ *testing.T) Queue {
				q, err := New(Config{
					Driver: DriverWorkerpool,
				})
				if err != nil {
					t.Fatalf("new workerpool q failed: %v", err)
				}
				return q
			},
			requiresRegisteredHandle: true,
			requiresQueueName:        false,
			assertMissingHandlerErr:  true,
		},
		{
			name: "database-sqlite",
			newQueue: func(t *testing.T) Queue {
				q, err := New(Config{
					Driver:         DriverDatabase,
					DatabaseDriver: "sqlite",
					DatabaseDSN:    fmt.Sprintf("%s/contract-%d.db", t.TempDir(), time.Now().UnixNano()),
				})
				if err != nil {
					t.Fatalf("new sqlite q failed: %v", err)
				}
				return q
			},
			requiresRegisteredHandle: true,
			requiresQueueName:        true,
			assertMissingHandlerErr:  false,
		},
	}

	for _, factory := range factories {
		factory := factory
		t.Run(factory.name, func(t *testing.T) {
			contractFactory := factory
			contractFactory.newQueue = func(t *testing.T) Queue {
				d := factory.newQueue(t)
				t.Cleanup(func() {
					_ = d.Shutdown(context.Background())
				})
				return d
			}
			runQueueContractSuite(t, contractFactory)
		})
	}
}
