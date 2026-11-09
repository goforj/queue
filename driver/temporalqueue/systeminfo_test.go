package temporalqueue

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// systemInfoFixture exposes deterministic provider faults and cancellation-resistant sampling.
type systemInfoFixture struct {
	mode    string
	calls   atomic.Int64
	entered chan struct{}
	release chan struct{}
}

// sample supplies both SDK callbacks with identical fault and lifetime behavior.
func (p *systemInfoFixture) sample() (float64, error) {
	p.calls.Add(1)
	if p.mode == "panic" {
		panic("private system information diagnostic")
	}
	if p.mode == "error" {
		return 0.75, errors.New("private system information diagnostic")
	}
	if p.mode == "block" && p.calls.Load() == 1 {
		close(p.entered)
		<-p.release
	}
	return 0.25, nil
}

// CpuUsage permits exercising the actual SDK shutdown heartbeat rather than a synthetic Stop implementation.
func (p *systemInfoFixture) CpuUsage(*worker.SysInfoContext) (float64, error) { return p.sample() }

// MemoryUsage exercises the same protection on the second heartbeat callback.
func (p *systemInfoFixture) MemoryUsage(*worker.SysInfoContext) (float64, error) { return p.sample() }

// TestWorkerSystemInformationGuards prevents diagnostic providers from leaking panics, errors or late dependency access.
func TestWorkerSystemInformationGuards(t *testing.T) {
	for _, method := range []string{"cpu", "memory"} {
		for _, mode := range []string{"success", "error", "panic", "closed"} {
			t.Run(method+"/"+mode, func(t *testing.T) {
				fixture := &systemInfoFixture{mode: mode}
				gate := newAdmission()
				guard := &guardedSystemInfo{inner: fixture, gate: gate}
				if mode == "closed" {
					gate.close()
				}
				call := guard.CpuUsage
				if method == "memory" {
					call = guard.MemoryUsage
				}
				value, err := call(&worker.SysInfoContext{})
				if mode == "success" {
					if value != 0.25 || err != nil {
						t.Fatal("successful provider changed", value, err)
					}
				} else if value != 0 || err == nil || strings.Contains(err.Error(), "private") {
					t.Fatal("provider rejection was not sanitized", value, err)
				}
				wantCalls := int64(1)
				if mode == "closed" {
					wantCalls = 0
				}
				if fixture.calls.Load() != wantCalls {
					t.Fatal("late provider entry", fixture.calls.Load())
				}
				if err := gate.wait(t.Context()); err != nil {
					t.Fatal("provider leaked admission", err)
				}
			})
		}
	}
}

// systemInfoStartupService forces real SDK rollback after creating its shutdown heartbeat callback.
type systemInfoStartupService struct {
	workflowservice.UnimplementedWorkflowServiceServer
}

// GetSystemInfo allows lazy client initialization to complete before worker startup rejects the namespace.
func (*systemInfoStartupService) GetSystemInfo(context.Context, *workflowservice.GetSystemInfoRequest) (*workflowservice.GetSystemInfoResponse, error) {
	return &workflowservice.GetSystemInfoResponse{Capabilities: &workflowservice.GetSystemInfoResponse_Capabilities{}}, nil
}

// DescribeNamespace selects the SDK startup rollback path without requiring a Temporal service.
func (*systemInfoStartupService) DescribeNamespace(context.Context, *workflowservice.DescribeNamespaceRequest) (*workflowservice.DescribeNamespaceResponse, error) {
	return nil, status.Error(codes.NotFound, "startup fixture namespace is absent")
}

// TestWorkerRollbackContainsAndJoinsSystemInformation verifies the production worker set protects SDK Stop callbacks.
func TestWorkerRollbackContainsAndJoinsSystemInformation(t *testing.T) {
	for _, mode := range []string{"panic", "block"} {
		t.Run(mode, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			server := grpc.NewServer()
			workflowservice.RegisterWorkflowServiceServer(server, &systemInfoStartupService{})
			go func() { _ = server.Serve(listener) }()
			t.Cleanup(server.Stop)
			fixture := &systemInfoFixture{mode: mode, entered: make(chan struct{}), release: make(chan struct{})}
			q, err := NewWithConfig(Config{ClientOptions: client.Options{HostPort: listener.Addr().String()}, DefaultActivityTimeout: time.Second, ShutdownTimeout: time.Second, WorkerOptions: worker.Options{SysInfoProvider: fixture}})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			started := make(chan error, 1)
			go func() { started <- q.StartWorkers(ctx) }()
			if mode == "block" {
				defer func() {
					select {
					case <-fixture.release:
					default:
						close(fixture.release)
					}
				}()
				select {
				case <-fixture.entered:
				case <-ctx.Done():
					t.Fatal("SDK rollback never sampled provider", ctx.Err())
				}
				short, stop := context.WithTimeout(ctx, 10*time.Millisecond)
				if err := q.Shutdown(short); !errors.Is(err, context.DeadlineExceeded) {
					t.Fatal("shutdown released blocked provider", err)
				}
				stop()
				close(fixture.release)
			}
			select {
			case err := <-started:
				if err == nil || strings.Contains(err.Error(), "private") {
					t.Fatal("startup failure changed or leaked provider text", err)
				}
			case <-ctx.Done():
				t.Fatal("startup rollback did not join provider", ctx.Err())
			}
			if err := q.Shutdown(ctx); err != nil {
				t.Fatal("failed to finish resource cleanup", err)
			}
			if fixture.calls.Load() == 0 {
				t.Fatal("SDK rollback did not invoke system information provider")
			}
		})
	}
}
