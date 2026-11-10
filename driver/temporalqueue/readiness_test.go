package temporalqueue

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/goforj/queue"
	"github.com/goforj/queue/internal/runtimehook"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
)

// healthClientFixture separates a lazy client from a successful or failed readiness RPC.
type healthClientFixture struct {
	client.Client
	err   error
	calls int
}

// CheckHealth captures connectivity probes without contacting a Temporal service.
func (c *healthClientFixture) CheckHealth(context.Context, *client.CheckHealthRequest) (*client.CheckHealthResponse, error) {
	c.calls++
	return &client.CheckHealthResponse{}, c.err
}

// TestReadinessDistinguishesStartupServiceAndClosedAdmission preserves actionable readiness outcomes before any polling worker exists.
func TestReadinessDistinguishesStartupServiceAndClosedAdmission(t *testing.T) {
	for _, mode := range []string{"healthy", "service failure", "starting", "startup failure", "closed"} {
		t.Run(mode, func(t *testing.T) {
			sdk := &healthClientFixture{}
			gate := newAdmission()
			b := &backend{entry: gate, clientBinding: &ClientBinding{client: sdk}}
			native := errors.New("startup result")
			switch mode {
			case "service failure":
				sdk.err = errors.New("private service diagnostic")
			case "starting":
				b.starting = true
			case "startup failure":
				b.startErr = native
			case "closed":
				gate.close()
			}
			err := b.Ready(t.Context())
			if mode == "healthy" {
				if err != nil || sdk.calls != 1 {
					t.Fatal("healthy lazy client rejected", err)
				}
			} else if err == nil || strings.Contains(err.Error(), "private") {
				t.Fatal("invalid readiness result", err)
			}
			if mode == "startup failure" && err != native {
				t.Fatal("startup error identity lost")
			}
			if mode != "healthy" && mode != "service failure" && sdk.calls != 0 {
				t.Fatal("readiness contacted service before local state was ready")
			}
			if err := gate.wait(t.Context()); err != nil {
				t.Fatal("readiness leaked admission", err)
			}
		})
	}
}

// TestWorkerStartupAdmissionAndJoining honors canceled callers without inventing a second startup operation.
func TestWorkerStartupAdmissionAndJoining(t *testing.T) {
	native := errors.New("deferred configuration")
	for _, mode := range []string{"stopping", "configuration", "joined", "canceled join"} {
		t.Run(mode, func(t *testing.T) {
			b := &backend{cfg: testConfig(), entry: newAdmission(), gate: newAdmission(), startDone: make(chan struct{})}
			ctx := t.Context()
			switch mode {
			case "stopping":
				b.stopping = true
			case "configuration":
				b.configErr = native
			case "joined":
				b.startErr = native
				close(b.startDone)
			case "canceled join":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			err := b.StartWorkers(ctx)
			if mode == "canceled join" {
				if !errors.Is(err, context.Canceled) {
					t.Fatal("startup join ignored cancellation", err)
				}
			} else if mode == "stopping" {
				if !errors.Is(err, errAdmissionClosed) {
					t.Fatal("stopping resource restarted", err)
				}
			} else if err != native {
				t.Fatal("startup/configuration error changed", err)
			}
		})
	}
	b := &backend{cfg: testConfig(), entry: newAdmission(), gate: newAdmission(), startDone: make(chan struct{})}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := b.DrainWorkers(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("drain ignored outstanding startup", err)
	}
	close(b.startDone)
	if err := b.DrainWorkers(nil); err != nil {
		t.Fatal("later drain failed to join", err)
	}
}

// unstartedWorkerFixture supplies only a catalog-ready marker without creating SDK pollers.
type unstartedWorkerFixture struct{ worker.Worker }

// TestPublicNativeCapabilitiesRejectWrongResourcesAndUnfinalizedCatalogs prevents lazy or failed startup from advertising finalized registrations.
func TestPublicNativeCapabilitiesRejectWrongResourcesAndUnfinalizedCatalogs(t *testing.T) {
	ordinary, err := queue.NewSync()
	if err != nil {
		t.Fatal(err)
	}
	defer ordinary.Shutdown(t.Context())
	if _, err := Client(ordinary); err == nil {
		t.Fatal("non-Temporal queue exposed a native client")
	}
	if _, err := RegistrationFingerprint(ordinary); err == nil {
		t.Fatal("non-Temporal queue exposed a native catalog")
	}
	q, err := NewWithConfig(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer q.Shutdown(t.Context())
	if _, err := RegistrationFingerprint(q); err == nil {
		t.Fatal("unstarted native catalog advertised readiness")
	}
	b := runtimehook.DriverResource(q).(*backend)
	b.worker = &unstartedWorkerFixture{}
	fingerprint, err := RegistrationFingerprint(q)
	if err != nil || fingerprint == "" {
		t.Fatal("finalized catalog unavailable", err)
	}
	for _, mode := range []string{"zero capacity", "after startup"} {
		t.Run(mode, func(t *testing.T) {
			count := 2
			if mode == "zero capacity" {
				count = 0
			}
			b.SetWorkers(count)
			if b.ConfigurationError() == nil {
				t.Fatal("late or invalid capacity accepted")
			}
		})
	}
	if err := b.Shutdown(nil); err != nil {
		t.Fatal("nil-context shutdown failed", err)
	}
	cfg := testConfig()
	cfg.DefaultActivityTimeout = time.Duration(math.MaxInt64)
	if _, err := NewWithConfig(cfg); err == nil {
		t.Fatal("overflowing default timeout accepted")
	}
	binding, err := NewClientBinding(client.Options{}, SerializationLimits{})
	if err != nil {
		t.Fatal(err)
	}
	defer binding.Close(t.Context())
	cfg = testConfig()
	cfg.Binding = binding
	cfg.ClientOptions.Namespace = "different"
	if _, err := NewWithConfig(cfg); err == nil {
		t.Fatal("borrowed binding accepted divergent client policy")
	}
}
