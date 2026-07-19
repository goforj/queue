package readmecheck

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/goforj/queue"
)

// TestReadmeManualSnippetsCompile prevents curated README examples from drifting beyond public API signatures.
func TestReadmeManualSnippetsCompile(t *testing.T) {
	// The helpers mirror curated manual README snippets that have drifted before,
	// including Dispatch/WithContext(ctx).Dispatch and handler signatures.
	_ = []any{
		compileQuickStartQueueSnippet,
		compileQuickStartWorkflowSnippet,
		compileRunAsWorkerServiceSnippet,
		compileJobBuilderOptionsSnippet,
		compileMiddlewareSnippet,
		compileObservabilitySnippet,
		compileComposeObserversSnippet,
		compileFakeQueueSnippet,
	}
	assertReadmeObserverSignatures(t)
}

// assertReadmeObserverSignatures keeps the manual observer snippets tied to the compiled helpers.
func assertReadmeObserverSignatures(t *testing.T) {
	t.Helper()
	readmePath := filepath.Join("..", "..", "README.md")
	contents, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatalf("read %s: %v", readmePath, err)
	}
	manual, _, found := strings.Cut(string(contents), "<!-- api:embed:start -->")
	if !found {
		t.Fatal("README is missing the generated API start marker")
	}

	for _, signature := range []string{
		"queue.ObserverFunc(func(_ context.Context, event queue.Event)",
		"queue.ObserverFunc(func(_ context.Context, e queue.Event)",
	} {
		if !strings.Contains(manual, signature) {
			t.Fatalf("README manual observer snippet is missing compiled signature %q", signature)
		}
	}
}

// compileQuickStartQueueSnippet pins the queue quick start to the supported dispatch and handler signatures.
func compileQuickStartQueueSnippet(q *queue.Queue) {
	if q == nil {
		return
	}
	type EmailPayload struct {
		To string `json:"to"`
	}

	q.Register("emails:send", func(ctx context.Context, m queue.Message) error {
		var payload EmailPayload
		_ = m.Bind(&payload)
		return nil
	})

	_ = q.StartWorkers(context.Background())
	defer q.Shutdown(context.Background())

	_, _ = q.Dispatch(
		queue.NewJob("emails:send").
			Payload(EmailPayload{To: "user@example.com"}),
	)
}

// compileQuickStartWorkflowSnippet pins the workflow quick start to the supported builder signatures.
func compileQuickStartWorkflowSnippet(q *queue.Queue) {
	q, _ = queue.NewWorkerpool(queue.WithWorkers(2))

	type EmailPayload struct {
		ID int `json:"id"`
	}

	q.Register("reports:generate", func(context.Context, queue.Message) error { return nil })
	q.Register("reports:upload", func(_ context.Context, m queue.Message) error {
		var payload EmailPayload
		return m.Bind(&payload)
	})
	q.Register("users:notify_report_ready", func(context.Context, queue.Message) error { return nil })

	_ = q.StartWorkers(context.Background())
	defer q.Shutdown(context.Background())

	chainID, _ := q.Chain(
		queue.NewJob("reports:generate").Payload(map[string]any{"report_id": "rpt_123"}),
		queue.NewJob("reports:upload").Payload(EmailPayload{ID: 123}),
		queue.NewJob("users:notify_report_ready").Payload(map[string]any{"user_id": 123}),
	).OnQueue("critical").Dispatch(context.Background())
	_ = chainID
}

// compileRunAsWorkerServiceSnippet pins the worker-service example to the supported lifecycle signatures.
func compileRunAsWorkerServiceSnippet(q *queue.Queue) {
	if q == nil {
		return
	}

	q.Register("emails:send", func(ctx context.Context, m queue.Message) error { return nil })

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := q.Run(ctx); err != nil {
		log.Print(err)
	}
}

// compileJobBuilderOptionsSnippet pins the job-options example to the supported fluent builder API.
func compileJobBuilderOptionsSnippet(q *queue.Queue) {
	if q == nil {
		return
	}

	type EmailPayload struct {
		ID int    `json:"id"`
		To string `json:"to"`
	}

	job := queue.NewJob("emails:send").
		Payload(EmailPayload{ID: 123, To: "user@example.com"}).
		OnQueue("default").
		Timeout(20 * time.Second).
		Retry(3).
		Backoff(500 * time.Millisecond).
		Delay(2 * time.Second).
		UniqueFor(45 * time.Second)

	_, _ = q.Dispatch(job)

	q.Register("emails:send", func(ctx context.Context, m queue.Message) error {
		var payload EmailPayload
		return m.Bind(&payload)
	})
}

// compileMiddlewareSnippet pins the middleware example to the supported middleware contracts.
func compileMiddlewareSnippet() {
	var errValidation = errors.New("validation failed")
	maintenanceMode := false

	audit := queue.MiddlewareFunc(func(ctx context.Context, m queue.Message, next queue.Next) error {
		log.Printf("start job=%s", m.JobType)
		err := next(ctx, m)
		log.Printf("done job=%s err=%v", m.JobType, err)
		return err
	})

	skipMaintenance := queue.SkipWhen{
		Predicate: func(context.Context, queue.Message) bool {
			return maintenanceMode
		},
	}

	fatalValidation := queue.FailOnError{
		When: func(err error) bool {
			return errors.Is(err, errValidation)
		},
	}

	q, _ := queue.New(
		queue.Config{Driver: queue.DriverWorkerpool},
		queue.WithMiddleware(audit, skipMaintenance, fatalValidation),
	)
	_ = q
}

// compileObservabilitySnippet mirrors the README's basic observer composition example.
func compileObservabilitySnippet() {
	collector := queue.NewStatsCollector()
	observer := queue.MultiObserver(
		collector,
		queue.ObserverFunc(func(_ context.Context, event queue.Event) {
			_ = event.Kind
		}),
	)

	q, _ := queue.New(queue.Config{
		Driver:   queue.DriverWorkerpool,
		Observer: observer,
	})
	_ = q
}

// compileComposeObserversSnippet mirrors the README's multi-observer example.
func compileComposeObserversSnippet() {
	events := make(chan queue.Event, 100)
	collector := queue.NewStatsCollector()
	observer := queue.MultiObserver(
		collector,
		queue.ChannelObserver{
			Events:     events,
			DropIfFull: true,
		},
		queue.ObserverFunc(func(_ context.Context, e queue.Event) {
			_ = e
		}),
	)

	q, _ := queue.New(queue.Config{
		Driver:   queue.DriverWorkerpool,
		Observer: observer,
	})
	_ = q
}

// compileFakeQueueSnippet pins the fake-queue example to the supported testing API.
func compileFakeQueueSnippet() {
	fake := queue.NewFake()
	fake.Register("emails:send", func(context.Context, queue.Job) error { return nil })
}
