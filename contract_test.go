package queue

import (
	"context"
	"errors"
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
	expectedDriver           Driver
	newQueue                 func(t *testing.T) QueueRuntime
	requiresRegisteredHandle bool
	requiresQueueName        bool
	assertMissingHandlerErr  bool
	backoffUnsupported       bool
	supportsPause            bool
	supportsNativeStats      bool
	uniqueTTL                time.Duration
	uniqueExpiryWait         time.Duration
	beforeEach               func(t *testing.T)
}

func runQueueContractSuite(t *testing.T, factory contractFactory) {
	t.Helper()

	startWorker := func(t *testing.T, d QueueRuntime, register func(QueueRuntime)) {
		t.Helper()
		if register != nil {
			register(d)
		}
		if err := d.Workers(1).StartWorkers(context.Background()); err != nil {
			t.Fatalf("worker start failed: %v", err)
		}
		t.Cleanup(func() { _ = d.Shutdown(context.Background()) })
	}

	t.Run("lifecycle_start_shutdown", func(t *testing.T) {
		d := factory.newQueue(t)
		t.Cleanup(func() { _ = d.Shutdown(context.Background()) })
		if factory.expectedDriver != "" && d.Driver() != factory.expectedDriver {
			t.Fatalf("expected driver %q, got %q", factory.expectedDriver, d.Driver())
		}
		if err := d.Workers(1).StartWorkers(context.Background()); err != nil {
			t.Fatalf("worker start failed: %v", err)
		}
		if factory.beforeEach != nil {
			factory.beforeEach(t)
		}
		if err := d.Shutdown(context.Background()); err != nil {
			t.Fatalf("worker shutdown failed: %v", err)
		}
	})

	t.Run("runtime_capabilities", func(t *testing.T) {
		d := factory.newQueue(t)
		t.Cleanup(func() { _ = d.Shutdown(context.Background()) })

		if !SupportsPause(d) {
			t.Fatal("queue runtime should expose QueueController surface")
		}
		if !SupportsNativeStats(d) {
			t.Fatal("queue runtime should expose StatsProvider surface")
		}

		pauseErr := Pause(context.Background(), d, "default")
		resumeErr := Resume(context.Background(), d, "default")
		if factory.supportsPause {
			if pauseErr != nil {
				t.Fatalf("expected pause to succeed, got %v", pauseErr)
			}
			if resumeErr != nil {
				t.Fatalf("expected resume to succeed, got %v", resumeErr)
			}
		} else {
			if !errors.Is(pauseErr, ErrPauseUnsupported) {
				t.Fatalf("expected ErrPauseUnsupported for pause, got %v", pauseErr)
			}
			if !errors.Is(resumeErr, ErrPauseUnsupported) {
				t.Fatalf("expected ErrPauseUnsupported for resume, got %v", resumeErr)
			}
		}

		_, err := Snapshot(context.Background(), d, nil)
		if factory.supportsNativeStats && err != nil {
			if startErr := d.Workers(1).StartWorkers(context.Background()); startErr != nil {
				t.Fatalf("expected native stats support; bootstrap start workers failed: %v", startErr)
			}
			_, err = Snapshot(context.Background(), d, nil)
			if err != nil {
				t.Fatalf("expected native stats support after bootstrap, got error: %v", err)
			}
		}
		if !factory.supportsNativeStats && err == nil {
			t.Fatal("expected native stats to be unsupported")
		}
	})

	t.Run("dispatch_immediate", func(t *testing.T) {
		d := factory.newQueue(t)
		t.Cleanup(func() { _ = d.Shutdown(context.Background()) })
		if factory.requiresRegisteredHandle {
			startWorker(t, d, func(q QueueRuntime) {
				q.Register("job:contract:immediate", func(_ context.Context, _ Job) error { return nil })
			})
		}
		if factory.beforeEach != nil {
			factory.beforeEach(t)
		}
		err := d.DispatchCtx(context.Background(), NewJob("job:contract:immediate").Payload([]byte("ok")).OnQueue("default"))
		if err != nil {
			t.Fatalf("immediate dispatch failed: %v", err)
		}
	})

	t.Run("dispatch_delayed", func(t *testing.T) {
		d := factory.newQueue(t)
		t.Cleanup(func() { _ = d.Shutdown(context.Background()) })
		if factory.requiresRegisteredHandle {
			startWorker(t, d, func(q QueueRuntime) {
				q.Register("job:contract:delay", func(_ context.Context, _ Job) error { return nil })
			})
		}
		if factory.beforeEach != nil {
			factory.beforeEach(t)
		}
		err := d.DispatchCtx(context.Background(), NewJob("job:contract:delay").Payload([]byte("delayed")).OnQueue("default").Delay(20*time.Millisecond))
		if err != nil {
			t.Fatalf("delayed dispatch failed: %v", err)
		}
	})

	t.Run("dispatch_with_queue", func(t *testing.T) {
		d := factory.newQueue(t)
		t.Cleanup(func() { _ = d.Shutdown(context.Background()) })
		if factory.requiresRegisteredHandle {
			startWorker(t, d, func(q QueueRuntime) {
				q.Register("job:contract:queue", func(_ context.Context, _ Job) error { return nil })
			})
		}
		if factory.beforeEach != nil {
			factory.beforeEach(t)
		}
		err := d.DispatchCtx(context.Background(), NewJob("job:contract:queue").Payload([]byte("queue")).OnQueue("contract"))
		if err != nil {
			t.Fatalf("dispatch with queue failed: %v", err)
		}
	})

	t.Run("dispatch_without_queue_behavior", func(t *testing.T) {
		d := factory.newQueue(t)
		t.Cleanup(func() { _ = d.Shutdown(context.Background()) })
		if factory.requiresRegisteredHandle {
			startWorker(t, d, func(q QueueRuntime) {
				q.Register("job:contract:noqueue", func(_ context.Context, _ Job) error { return nil })
			})
		}
		if factory.beforeEach != nil {
			factory.beforeEach(t)
		}
		err := d.DispatchCtx(context.Background(), NewJob("job:contract:noqueue").Payload([]byte("no-queue")))
		if factory.requiresQueueName {
			if err == nil {
				t.Fatal("expected missing queue error")
			}
			if !strings.Contains(err.Error(), "job queue is required") {
				t.Fatalf("expected missing queue error, got %v", err)
			}
			return
		}
		if err != nil {
			t.Fatalf("unexpected dispatch without queue error: %v", err)
		}
	})

	t.Run("dispatch_with_nil_context", func(t *testing.T) {
		d := factory.newQueue(t)
		t.Cleanup(func() { _ = d.Shutdown(context.Background()) })
		if factory.requiresRegisteredHandle {
			startWorker(t, d, func(q QueueRuntime) {
				q.Register("job:contract:nilctx", func(_ context.Context, _ Job) error { return nil })
			})
		}
		if factory.beforeEach != nil {
			factory.beforeEach(t)
		}
		err := d.DispatchCtx(nil, NewJob("job:contract:nilctx").Payload([]byte("ok")).OnQueue("default"))
		if err != nil {
			t.Fatalf("dispatch with nil context failed: %v", err)
		}
		if err := d.Shutdown(nil); err != nil {
			t.Fatalf("shutdown with nil context failed: %v", err)
		}
	})

	t.Run("dispatch_with_timeout", func(t *testing.T) {
		d := factory.newQueue(t)
		t.Cleanup(func() { _ = d.Shutdown(context.Background()) })
		timeoutChecked := make(chan bool, 1)
		if factory.requiresRegisteredHandle {
			startWorker(t, d, func(q QueueRuntime) {
				q.Register("job:contract:timeout", func(ctx context.Context, _ Job) error {
					_, ok := ctx.Deadline()
					timeoutChecked <- ok
					return nil
				})
			})
		}
		if factory.beforeEach != nil {
			factory.beforeEach(t)
		}
		err := d.DispatchCtx(context.Background(), NewJob("job:contract:timeout").Payload([]byte("timeout")).OnQueue("default").Timeout(80*time.Millisecond))
		if err != nil {
			t.Fatalf("dispatch with timeout failed: %v", err)
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

	t.Run("dispatch_with_invalid_payload_fails", func(t *testing.T) {
		d := factory.newQueue(t)
		t.Cleanup(func() { _ = d.Shutdown(context.Background()) })
		if factory.requiresRegisteredHandle {
			startWorker(t, d, func(q QueueRuntime) {
				q.Register("job:contract:invalid-payload", func(_ context.Context, _ Job) error { return nil })
			})
		}
		if factory.beforeEach != nil {
			factory.beforeEach(t)
		}
		job := NewJob("job:contract:invalid-payload").Payload(func() {})
		err := d.DispatchCtx(context.Background(), job.OnQueue("default"))
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
			startWorker(t, d, func(q QueueRuntime) {
				q.Register("job:contract:invalid-values", func(_ context.Context, _ Job) error { return nil })
			})
		}
		if factory.beforeEach != nil {
			factory.beforeEach(t)
		}
		job := NewJob("job:contract:invalid-values").
			OnQueue("default").
			Timeout(-1 * time.Second).
			Retry(-1).
			Backoff(-1 * time.Second).
			Delay(-1 * time.Second).
			UniqueFor(-1 * time.Second)
		err := d.DispatchCtx(context.Background(), job)
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
		startWorker(t, d, func(q QueueRuntime) {
			q.Register("job:contract:bind", func(_ context.Context, job Job) error {
				var in payload
				if err := job.Bind(&in); err != nil {
					return err
				}
				seen <- in
				return nil
			})
		})
		if factory.beforeEach != nil {
			factory.beforeEach(t)
		}
		want := payload{ID: 42, Name: "bind"}
		if err := d.DispatchCtx(context.Background(), NewJob("job:contract:bind").Payload(want).OnQueue("default")); err != nil {
			t.Fatalf("dispatch bind job failed: %v", err)
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

	t.Run("dispatch_with_max_retry", func(t *testing.T) {
		d := factory.newQueue(t)
		t.Cleanup(func() { _ = d.Shutdown(context.Background()) })
		var calls atomic.Int32
		done := make(chan struct{}, 1)
		if factory.requiresRegisteredHandle {
			startWorker(t, d, func(q QueueRuntime) {
				q.Register("job:contract:maxretry", func(_ context.Context, _ Job) error {
					if calls.Add(1) < 3 {
						return errors.New("transient")
					}
					done <- struct{}{}
					return nil
				})
			})
		}
		if factory.beforeEach != nil {
			factory.beforeEach(t)
		}
		err := d.DispatchCtx(context.Background(), NewJob("job:contract:maxretry").Payload([]byte("retry")).OnQueue("default").Retry(2))
		if err != nil {
			t.Fatalf("dispatch with max retry failed: %v", err)
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

	t.Run("dispatch_with_backoff_behavior", func(t *testing.T) {
		d := factory.newQueue(t)
		t.Cleanup(func() { _ = d.Shutdown(context.Background()) })
		start := time.Now()
		done := make(chan time.Duration, 1)
		var calls atomic.Int32
		if factory.requiresRegisteredHandle {
			startWorker(t, d, func(q QueueRuntime) {
				q.Register("job:contract:backoff", func(_ context.Context, _ Job) error {
					if calls.Add(1) < 2 {
						return errors.New("retry-me")
					}
					done <- time.Since(start)
					return nil
				})
			})
		}
		if factory.beforeEach != nil {
			factory.beforeEach(t)
		}
		err := d.DispatchCtx(context.Background(), NewJob("job:contract:backoff").Payload([]byte("backoff")).OnQueue("default").Retry(1).Backoff(10*time.Millisecond))
		if factory.backoffUnsupported {
			if !errors.Is(err, ErrBackoffUnsupported) {
				t.Fatalf("expected ErrBackoffUnsupported, got %v", err)
			}
			return
		}
		if err != nil {
			t.Fatalf("dispatch with backoff failed: %v", err)
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
			startWorker(t, d, func(q QueueRuntime) {
				q.Register("job:contract:unique", func(_ context.Context, _ Job) error { return nil })
			})
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
		jobType := "job:contract:unique"
		payload := []byte("same")
		err := d.DispatchCtx(context.Background(), NewJob(jobType).Payload(payload).OnQueue("default").UniqueFor(ttl))
		if err != nil {
			t.Fatalf("first unique dispatch failed: %v", err)
		}
		err = d.DispatchCtx(context.Background(), NewJob(jobType).Payload(payload).OnQueue("default").UniqueFor(ttl))
		if !errors.Is(err, ErrDuplicate) {
			t.Fatalf("expected ErrDuplicate, got %v", err)
		}
		time.Sleep(expiryWait)
		err = d.DispatchCtx(context.Background(), NewJob(jobType).Payload(payload).OnQueue("default").UniqueFor(ttl))
		if err != nil {
			t.Fatalf("dispatch after unique ttl failed: %v", err)
		}
	})

	t.Run("unique_is_scoped_by_queue", func(t *testing.T) {
		d := factory.newQueue(t)
		t.Cleanup(func() { _ = d.Shutdown(context.Background()) })
		if factory.requiresRegisteredHandle {
			startWorker(t, d, func(q QueueRuntime) {
				q.Register("job:contract:unique-scope", func(_ context.Context, _ Job) error { return nil })
			})
		}
		if factory.beforeEach != nil {
			factory.beforeEach(t)
		}
		jobType := "job:contract:unique-scope"
		payload := []byte("same")
		ttl := factory.uniqueTTL
		if ttl <= 0 {
			ttl = 400 * time.Millisecond
		}
		first := NewJob(jobType).Payload(payload).OnQueue("queue-a").UniqueFor(ttl)
		if err := d.DispatchCtx(context.Background(), first); err != nil {
			t.Fatalf("first dispatch failed: %v", err)
		}
		second := NewJob(jobType).Payload(payload).OnQueue("queue-b").UniqueFor(ttl)
		if err := d.DispatchCtx(context.Background(), second); err != nil {
			t.Fatalf("expected unique lock to be queue scoped, got %v", err)
		}
	})

	t.Run("missing_job_type", func(t *testing.T) {
		d := factory.newQueue(t)
		t.Cleanup(func() { _ = d.Shutdown(context.Background()) })
		err := d.DispatchCtx(context.Background(), NewJob("").OnQueue("default"))
		if err == nil {
			t.Fatal("expected missing job type error")
		}
	})

	t.Run("missing_handler", func(t *testing.T) {
		d := factory.newQueue(t)
		t.Cleanup(func() { _ = d.Shutdown(context.Background()) })
		if factory.requiresRegisteredHandle {
			startWorker(t, d, nil)
		}
		if factory.beforeEach != nil {
			factory.beforeEach(t)
		}
		err := d.DispatchCtx(context.Background(), NewJob("job:contract:missing-handler").OnQueue("default"))
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
		DriverNull:       "local contract suite",
		DriverSync:       "local contract suite",
		DriverWorkerpool: "local contract suite",
		DriverDatabase:   "local/integration database contract suites",
		DriverRedis:      "integration redis contract suite",
		DriverNATS:       "integration nats contract suite",
		DriverSQS:        "integration sqs contract suite",
		DriverRabbitMQ:   "integration rabbitmq contract suite",
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

func TestQueueContract_LocalRuntimes(t *testing.T) {
	factories := []contractFactory{
		{
			name:           "null",
			expectedDriver: DriverNull,
			newQueue: func(_ *testing.T) QueueRuntime {
				q, err := NewQueue(Config{Driver: DriverNull})
				if err != nil {
					t.Fatalf("new null q failed: %v", err)
				}
				return q
			},
			requiresRegisteredHandle: false,
			requiresQueueName:        true,
			assertMissingHandlerErr:  false,
			supportsPause:            false,
			supportsNativeStats:      false,
		},
		{
			name:           "sync",
			expectedDriver: DriverSync,
			newQueue: func(_ *testing.T) QueueRuntime {
				q, err := NewQueue(Config{Driver: DriverSync})
				if err != nil {
					t.Fatalf("new sync q failed: %v", err)
				}
				return q
			},
			requiresRegisteredHandle: true,
			requiresQueueName:        false,
			assertMissingHandlerErr:  true,
			supportsPause:            true,
			supportsNativeStats:      true,
		},
		{
			name:           "workerpool",
			expectedDriver: DriverWorkerpool,
			newQueue: func(_ *testing.T) QueueRuntime {
				q, err := NewQueue(Config{
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
			supportsPause:            true,
			supportsNativeStats:      true,
		},
	}

	for _, factory := range factories {
		factory := factory
		t.Run(factory.name, func(t *testing.T) {
			contractFactory := factory
			contractFactory.newQueue = func(t *testing.T) QueueRuntime {
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
