//go:build integration

package temporal

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goforj/queue"
	"github.com/goforj/queue/busruntime"
	"github.com/goforj/queue/driver/temporalqueue"
	"github.com/goforj/queue/queueconfig"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
)

// TestTemporalDriverTranslatesOutcomesBeforeObservers exercises handler, middleware, panic and missing-registration paths.
func TestTemporalDriverTranslatesOutcomesBeforeObservers(t *testing.T) {
	requireTemporalServer(t)
	for _, kind := range []string{"handler-uncommitted", "middleware-uncommitted", "handler-panic", "middleware-panic", "missing"} {
		t.Run(kind, func(t *testing.T) {
			name := fmt.Sprintf("driver-outcome-%s-%d", kind, time.Now().UnixNano())
			var calls atomic.Int32
			var mu sync.Mutex
			var events []queue.Event
			options := []queue.Option{}
			if strings.HasPrefix(kind, "middleware") {
				options = append(options, queue.WithMiddleware(queue.MiddlewareFunc(func(context.Context, queue.Message, queue.Next) error {
					calls.Add(1)
					if kind == "middleware-panic" {
						panic("private middleware panic")
					}
					return busruntime.Uncommitted(errors.New("private middleware error"))
				})))
			}
			observer := queue.ObserverFunc(func(_ context.Context, event queue.Event) {
				mu.Lock()
				defer mu.Unlock()
				events = append(events, event)
			})
			q, err := temporalqueue.NewWithConfig(temporalqueue.Config{DriverBaseConfig: queueconfig.DriverBaseConfig{DefaultQueue: name, Observer: observer}, ClientOptions: client.Options{HostPort: temporalAddress}, DefaultActivityTimeout: time.Second, ShutdownTimeout: 3 * time.Second}, options...)
			if err != nil {
				t.Fatal(err)
			}
			defer q.Shutdown(context.Background())
			if kind != "missing" {
				q.Register("business", func(context.Context, queue.Message) error {
					calls.Add(1)
					if kind == "handler-panic" {
						panic("private handler panic")
					}
					return busruntime.Uncommitted(errors.New("private handler error"))
				})
			}
			if err := q.StartWorkers(t.Context()); err != nil {
				t.Fatal(err)
			}
			result, err := q.WithContext(t.Context()).Dispatch(queue.NewJob("business").Retry(4))
			if err != nil {
				t.Fatal(err)
			}
			c, err := temporalqueue.Client(q)
			if err != nil {
				t.Fatal(err)
			}
			err = c.GetWorkflow(t.Context(), result.DispatchID, "").Get(t.Context(), nil)
			var application *temporal.ApplicationError
			if !errors.As(err, &application) || !application.NonRetryable() {
				t.Fatalf("unsupported outcome was retryable: %v", err)
			}
			want := int32(1)
			if kind == "missing" {
				want = 0
			}
			if calls.Load() != want {
				t.Fatalf("application calls=%d, want %d", calls.Load(), want)
			}
			mu.Lock()
			defer mu.Unlock()
			failed := 0
			for _, event := range events {
				if event.Kind == queue.EventJobFailed {
					failed++
					if event.Err == nil || strings.Contains(event.Err.Error(), "private") {
						t.Fatalf("unsafe shared outcome: %+v", event)
					}
				}
				if event.Kind == queue.EventJobSucceeded || strings.Contains(strings.ToLower(string(event.Kind)), "retry") {
					t.Fatalf("conflicting shared attempt fact: %+v", event)
				}
			}
			if failed != 1 {
				t.Fatalf("terminal shared facts=%d, want 1", failed)
			}
		})
	}
}
