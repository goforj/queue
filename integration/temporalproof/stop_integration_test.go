//go:build integration

package temporalproof

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestTemporalProofSDKFatalStop exercises the candidate SDK's actual fatal
// callback and SDK-owned fallback Stop, including a stop from its own poller.
// The SDK deliberately retries non-retriable poll errors for two minutes.
func TestTemporalProofSDKFatalStop(t *testing.T) {
	requireProofServer(t)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	armed := make(chan struct{})
	polling := make(chan struct{})
	var pollOnce sync.Once
	inject := func(ctx context.Context, method string, request, response any, connection *grpc.ClientConn, invoke grpc.UnaryInvoker, options ...grpc.CallOption) error {
		if strings.HasSuffix(method, "/PollWorkflowTaskQueue") {
			pollOnce.Do(func() { close(polling) })
			select {
			case <-armed:
				return status.Error(codes.InvalidArgument, "queue-proof-forced-fatal")
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return invoke(ctx, method, request, response, connection, options...)
	}
	c := newProofClient(t, client.Options{ConnectionOptions: client.ConnectionOptions{DialOptions: []grpc.DialOption{grpc.WithChainUnaryInterceptor(inject)}}})
	fatal := make(chan error, 1)
	callbackReturned := make(chan struct{})
	var calls atomic.Int32
	var w worker.Worker
	stop := newProofStop(func() { calls.Add(1); w.Stop() })
	w = worker.New(c, fmt.Sprintf("queue-fatal-proof-%d", proofSequence.Add(1)), worker.Options{
		WorkerStopTimeout: 50 * time.Millisecond,
		OnFatalError: func(err error) {
			fatal <- err
			// Joining synchronously closes the SDK stop channel before its fatal
			// callback returns and reaches the SDK-owned fallback Stop branch.
			_ = stop.wait(context.Background())
			close(callbackReturned)
		},
	})
	w.RegisterWorkflow(func(workflow.Context) error { return nil })
	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := stop.wait(cleanup); err != nil {
			t.Error(err)
		}
	})
	select {
	case <-polling:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	close(armed)
	select {
	case err := <-fatal:
		if !strings.Contains(err.Error(), "queue-proof-forced-fatal") {
			t.Fatalf("unexpected fatal category: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("SDK fatal callback not reached: ", ctx.Err())
	}
	results := make(chan error, 16)
	for i := 0; i < cap(results); i++ {
		go func() { results <- stop.wait(ctx) }()
	}
	select {
	case <-callbackReturned:
	case <-ctx.Done():
		t.Fatal("self-poller stop did not return: ", ctx.Err())
	}
	for i := 0; i < cap(results); i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("module-owned Stop called %d times", calls.Load())
	}
	t.Log("actual SDK fatal callback joined one Stop, returned through SDK fallback without a second-stop panic; this is forced SDK stop, not application drain proof")
}
