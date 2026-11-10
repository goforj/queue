//go:build integration

package temporal

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goforj/queue/driver/temporalqueue"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/worker"
)

// cacheLifetimeTracer retains application-owned tracing dependencies until SDK cache cleanup completes.
type cacheLifetimeTracer struct {
	interceptor.BaseTracer
	queueStopped atomic.Bool
	closed       atomic.Bool
	finished     chan bool
}

// Options supplies stable identities for the SDK's native tracing extension.
func (*cacheLifetimeTracer) Options() interceptor.TracerOptions {
	return interceptor.TracerOptions{SpanContextKey: struct{}{}, HeaderKey: "cache-lifetime-trace"}
}

// UnmarshalSpan accepts absent ancestry for this local lifecycle fixture.
func (*cacheLifetimeTracer) UnmarshalSpan(map[string]string) (interceptor.TracerSpanRef, error) {
	return nil, nil
}

// MarshalSpan keeps the fixture independent of external tracing transport.
func (*cacheLifetimeTracer) MarshalSpan(interceptor.TracerSpan) (map[string]string, error) {
	return nil, nil
}

// SpanFromContext declines ancestry without accessing application dependencies.
func (*cacheLifetimeTracer) SpanFromContext(context.Context) interceptor.TracerSpan { return nil }

// ContextWithSpan preserves SDK request cancellation.
func (*cacheLifetimeTracer) ContextWithSpan(ctx context.Context, _ interceptor.TracerSpan) context.Context {
	return ctx
}

// StartSpan leaves workflow span lifetime under actual SDK control.
func (t *cacheLifetimeTracer) StartSpan(options *interceptor.TracerStartSpanOptions) (interceptor.TracerSpan, error) {
	return &cacheLifetimeSpan{owner: t, operation: options.Operation}, nil
}

// cacheLifetimeSpan observes the native workflow's deferred cleanup rather than short client/query spans.
type cacheLifetimeSpan struct {
	owner     *cacheLifetimeTracer
	operation string
}

// Finish verifies application tracing remains valid when SDK-owned workflow cleanup runs after queue shutdown.
func (s *cacheLifetimeSpan) Finish(*interceptor.TracerFinishSpanOptions) {
	if s.operation == "RunWorkflow" && s.owner.queueStopped.Load() {
		s.owner.finished <- s.owner.closed.Load()
	}
}

// TestTemporalDriverNativeCacheOwnership isolates SDK-global cleanup from every other test's workers.
func TestTemporalDriverNativeCacheOwnership(t *testing.T) {
	requireTemporalServer(t)
	if os.Getenv("QUEUE_NATIVE_CACHE_LIFETIME_CHILD") != "1" {
		binary, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 40*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, binary, "-test.run=^TestTemporalDriverNativeCacheOwnership$", "-test.timeout=30s")
		command.Env = append(os.Environ(), "QUEUE_DRIVER_WORKER_ADDRESS="+temporalAddress, "QUEUE_NATIVE_CACHE_LIFETIME_CHILD=1")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("isolated SDK cache lifecycle failed: %v\n%s", err, output)
		}
		return
	}
	tracer := &cacheLifetimeTracer{finished: make(chan bool, 8)}
	name := fmt.Sprintf("native-cache-lifetime-%d", time.Now().UnixNano())
	q := newDriverResource(t, name, func(r worker.Registry) error { r.RegisterWorkflow(nativeMessageWorkflow); return nil }, worker.Options{Interceptors: []interceptor.WorkerInterceptor{interceptor.NewTracingInterceptor(tracer)}})
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	if err := q.StartWorkers(ctx); err != nil {
		t.Fatal(err)
	}
	sdk, err := temporalqueue.Client(q)
	if err != nil {
		t.Fatal(err)
	}
	run, err := sdk.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: name, TaskQueue: name}, nativeMessageWorkflow)
	if err != nil {
		t.Fatal(err)
	}
	controller, err := client.Dial(client.Options{HostPort: temporalAddress})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_ = controller.TerminateWorkflow(cleanup, name, run.GetRunID(), "synthetic cache lifecycle cleanup")
		controller.Close()
	})
	if _, err := sdk.QueryWorkflow(ctx, name, run.GetRunID(), "total"); err != nil {
		t.Fatal(err)
	}
	if err := q.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	tracer.queueStopped.Store(true)
	// This child owns every SDK worker in its process, so the public global purge is legal here.
	worker.PurgeStickyWorkflowCache()
	select {
	case closed := <-tracer.finished:
		if closed {
			t.Fatal("application tracer closed before SDK-owned workflow cleanup")
		}
	case <-ctx.Done():
		t.Fatal("SDK cache did not finalize the retained workflow span", ctx.Err())
	}
	tracer.closed.Store(true)
}
