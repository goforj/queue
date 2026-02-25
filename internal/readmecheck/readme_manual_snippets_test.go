package readmecheck

import (
	"context"
	"errors"
	"log"
	"os/signal"
	"syscall"
	"testing"
	"time"

	"github.com/goforj/queue"
)

func TestReadmeManualSnippetsCompile(t *testing.T) {
	// Compile-check only. The helpers mirror curated manual README snippets that
	// have drifted before (Dispatch/DispatchCtx and handler signatures).
	_ = []any{
		compileQuickStartQueueSnippet,
		compileQuickStartWorkflowSnippet,
		compileRunAsWorkerServiceSnippet,
		compileJobBuilderOptionsSnippet,
		compileMiddlewareSnippet,
		compileFakeQueueSnippet,
	}
}

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

func compileFakeQueueSnippet() {
	fake := queue.NewFake()
	fake.Register("emails:send", func(context.Context, queue.Job) error { return nil })
}
