package temporalqueue

import (
	"context"
	"crypto/tls"
	"errors"
	"testing"
	"time"

	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/interceptor"
)

// blockingFactory models interceptor setup that ignores the SDK activity cancellation context.
type blockingFactory struct {
	interceptor.WorkerInterceptorBase
	entered, release chan struct{}
}

// InterceptActivity retains its dependency until an explicit test barrier releases it.
func (i *blockingFactory) InterceptActivity(_ context.Context, next interceptor.ActivityInboundInterceptor) interceptor.ActivityInboundInterceptor {
	close(i.entered)
	<-i.release
	return next
}

// TestWorkerInterceptorFactoryJoinsDrain proves SDK setup callbacks cannot escape actual-work accounting.
func TestWorkerInterceptorFactoryJoinsDrain(t *testing.T) {
	gate := newAdmission()
	factory := &blockingFactory{entered: make(chan struct{}), release: make(chan struct{})}
	done := make(chan struct{})
	wrapper := &trackedWorkerInterceptor{inner: factory, gate: gate}
	go func() {
		defer close(done)
		_ = wrapper.InterceptActivity(t.Context(), &interceptor.ActivityInboundInterceptorBase{})
	}()
	<-factory.entered
	gate.close()
	ctx, cancel := context.WithTimeout(t.Context(), time.Millisecond)
	defer cancel()
	if err := gate.wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("factory was not joined", err)
	}
	close(factory.release)
	<-done
	if err := gate.wait(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// TestTLSVerificationCallbackJoinsDrain covers callbacks that gRPC runs in background handshake goroutines.
func TestTLSVerificationCallbackJoinsDrain(t *testing.T) {
	gate := newAdmission()
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	config, err := guardedTLS(&tls.Config{VerifyConnection: func(tls.ConnectionState) error { close(entered); <-release; return nil }}, gate)
	if err != nil {
		t.Fatal(err)
	}
	go func() { defer close(done); _ = config.VerifyConnection(tls.ConnectionState{}) }()
	<-entered
	gate.close()
	ctx, cancel := context.WithTimeout(t.Context(), time.Millisecond)
	defer cancel()
	if err := gate.wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("TLS callback was not joined", err)
	}
	close(release)
	<-done
	if err := gate.wait(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// TestDynamicCredentialCallbackIsOutsideSDKRetryChain verifies the module gate wraps required SDK credentials.
func TestDynamicCredentialCallbackIsOutsideSDKRetryChain(t *testing.T) {
	entered, release, returned := make(chan struct{}), make(chan struct{}), make(chan struct{})
	credentials := client.NewAPIKeyDynamicCredentials(func(context.Context) (string, error) { close(entered); <-release; return "synthetic-test-key", nil })
	binding, err := NewClientBinding(client.Options{HostPort: "127.0.0.1:1", Credentials: credentials, ConnectionOptions: client.ConnectionOptions{TLS: &tls.Config{}}}, SerializationLimits{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		defer close(returned)
		_, _ = binding.Client().WorkflowService().GetSystemInfo(ctx, &workflowservice.GetSystemInfoRequest{})
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("SDK credential callback did not enter")
	}
	cancel()
	closeCtx, closeCancel := context.WithTimeout(t.Context(), time.Millisecond)
	defer closeCancel()
	if err := binding.Close(closeCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("binding closed across credential callback", err)
	}
	close(release)
	<-returned
	if err := binding.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}
