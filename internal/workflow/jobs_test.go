package workflow

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

type jobsTestMiddleware func(context.Context, Context, Next) error

// Handle adapts test middleware without involving the public queue package.
func (m jobsTestMiddleware) Handle(ctx context.Context, message Context, next Next) error {
	return m(ctx, message, next)
}

// TestJobsRunBeforeCoordinatorExists proves shared execution needs no workflow
// store or chain/batch/callback registration, including the legacy delivery path.
func TestJobsRunBeforeCoordinatorExists(t *testing.T) {
	for _, direct := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "direct"}[direct], func(t *testing.T) {
			transport := newDirectTestRuntime()
			fixed := time.Unix(42, 0)
			var events []Event
			var middlewareCalls int
			middlewares := []Middleware{nil, jobsTestMiddleware(func(ctx context.Context, message Context, next Next) error {
				middlewareCalls++
				return next(ctx, message)
			})}
			jobs, err := NewJobs(transport, JobConfig{
				Clock: func() time.Time { return fixed },
				Observer: ObserverFunc(func(_ context.Context, event Event) {
					events = append(events, event)
				}),
				Middlewares: middlewares,
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(transport.handlers) != 1 || transport.handlers[internalJob] == nil {
				t.Fatalf("shared execution installed workflow routes: %v", reflect.ValueOf(transport.handlers).MapKeys())
			}
			// Configuration owns its slice even if the caller reuses the input.
			middlewares[1] = nil
			var message Context
			jobs.Register("reports:build", func(_ context.Context, received Context) error {
				message = received
				return nil
			})
			if err := jobs.StartWorkers(t.Context()); err != nil {
				t.Fatal(err)
			}
			var result DispatchResult
			if direct {
				result, err = jobs.DispatchDirect(t.Context(), StoredJob{Type: "reports:build", Payload: []byte(`{"id":42}`)})
			} else {
				result, err = jobs.Dispatch(t.Context(), NewJob("reports:build", map[string]int{"id": 42}))
			}
			if err != nil {
				t.Fatal(err)
			}
			if middlewareCalls != 1 || result.DispatchID == "" || message.DispatchID != result.DispatchID || message.JobID == "" || string(message.PayloadBytes()) != `{"id":42}` {
				t.Fatalf("shared execution lost middleware, payload or identity: calls=%d result=%+v message=%+v", middlewareCalls, result, message)
			}
			want := []EventKind{EventDispatchStarted, EventJobStarted, EventJobSucceeded, EventDispatchSucceeded}
			var kinds []EventKind
			for _, event := range events {
				kinds = append(kinds, event.Kind)
				if !event.Time.Equal(fixed) {
					t.Fatalf("event clock = %s, want %s", event.Time, fixed)
				}
			}
			if !reflect.DeepEqual(kinds, want) {
				t.Fatalf("events = %v, want %v", kinds, want)
			}
			if err := jobs.Shutdown(t.Context()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestJobsRejectInvalidTransport preserves constructor failure without allocating a coordinator.
func TestJobsRejectInvalidTransport(t *testing.T) {
	for _, transport := range []any{nil, struct{}{}} {
		if _, err := NewJobs(transport, JobConfig{}); err == nil {
			t.Fatalf("NewJobs(%T) succeeded", transport)
		}
	}
}

// TestCoordinatorSharesHandlerReplacement proves workflow nodes and ordinary
// jobs resolve one live registry rather than copies captured by construction.
func TestCoordinatorSharesHandlerReplacement(t *testing.T) {
	transport := newDirectTestRuntime()
	jobs, err := NewJobs(transport, JobConfig{})
	if err != nil {
		t.Fatal(err)
	}
	coordinator := NewCoordinator(jobs)
	jobs.Register("shared:job", func(context.Context, Context) error { return errors.New("stale handler") })
	var calls int
	jobs.Register("shared:job", func(context.Context, Context) error { calls++; return nil })
	if _, err := jobs.DispatchDirect(t.Context(), StoredJob{Type: "shared:job"}); err != nil {
		t.Fatal(err)
	}
	chainID, err := coordinator.Chain(NewJob("shared:job", nil)).Dispatch(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	batchID, err := coordinator.Batch(NewJob("shared:job", nil)).Dispatch(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	chain, err := coordinator.FindChain(t.Context(), chainID)
	if err != nil || !chain.Completed {
		t.Fatalf("chain = %+v, err = %v", chain, err)
	}
	batch, err := coordinator.FindBatch(t.Context(), batchID)
	if err != nil || !batch.Completed || batch.Processed != 1 {
		t.Fatalf("batch = %+v, err = %v", batch, err)
	}
	if calls != 3 {
		t.Fatalf("shared handler calls = %d, want 3", calls)
	}
}
