package temporalqueue

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.temporal.io/sdk/client"
	"google.golang.org/grpc"
)

// metricsFixture observes factory names and callback values independently of the diagnostic guards.
type metricsFixture struct {
	mode               string
	tags               map[string]string
	names              []string
	factories, updates int
	count              int64
	gauge              float64
	duration           time.Duration
}

// factory exposes diagnostic provider panics without depending on a particular metrics backend.
func (m *metricsFixture) factory(name string) {
	m.factories++
	m.names = append(m.names, name)
	if m.mode == "factory panic" {
		panic("private metrics factory diagnostic")
	}
}

// WithTags retains tag semantics while allowing separate factory and update failure cases.
func (m *metricsFixture) WithTags(tags map[string]string) client.MetricsHandler {
	m.factory("tags")
	m.tags = tags
	return m
}

// Counter records requested names before returning an instrument whose updates are separately guarded.
func (m *metricsFixture) Counter(name string) client.MetricsCounter { m.factory(name); return m }

// Gauge records gauge creation independently of updates.
func (m *metricsFixture) Gauge(name string) client.MetricsGauge { m.factory(name); return m }

// Timer records timer creation independently of duration samples.
func (m *metricsFixture) Timer(name string) client.MetricsTimer { m.factory(name); return m }

// update distinguishes provider execution from harmless fallback instruments.
func (m *metricsFixture) update() {
	m.updates++
	if m.mode == "update panic" {
		panic("private metrics update diagnostic")
	}
}

// Inc retains the exact counter delta supplied by the SDK.
func (m *metricsFixture) Inc(value int64) { m.update(); m.count += value }

// Update retains fractional gauge values supplied by the SDK.
func (m *metricsFixture) Update(value float64) { m.update(); m.gauge = value }

// Record retains duration precision supplied by the SDK.
func (m *metricsFixture) Record(value time.Duration) { m.update(); m.duration = value }

// TestMetricsPreserveValuesAndFenceFactoriesAndInstruments verifies both already-created and late-created instruments after closure.
func TestMetricsPreserveValuesAndFenceFactoriesAndInstruments(t *testing.T) {
	for _, mode := range []string{"success", "factory panic", "update panic"} {
		t.Run(mode, func(t *testing.T) {
			fixture := &metricsFixture{mode: mode}
			gate := newAdmission()
			guard := &guardedMetrics{inner: fixture, gate: gate}
			tagged := guard.WithTags(map[string]string{"namespace": "reports"})
			counter, gauge, timer := guard.Counter("attempts"), guard.Gauge("pending"), guard.Timer("duration")
			counter.Inc(7)
			gauge.Update(2.5)
			timer.Record(17 * time.Microsecond)
			if mode == "success" && (fixture.count != 7 || fixture.gauge != 2.5 || fixture.duration != 17*time.Microsecond || fixture.tags["namespace"] != "reports") {
				t.Fatalf("metrics semantics changed: %+v", fixture)
			}
			if mode != "factory panic" && tagged == client.MetricsNopHandler {
				t.Fatal("tagged metrics lost provider")
			}
			if mode == "factory panic" && fixture.updates != 0 {
				t.Fatal("failed metric factory exposed provider")
			}
			factories, updates := fixture.factories, fixture.updates
			gate.close()
			counter.Inc(10)
			gauge.Update(9)
			timer.Record(time.Hour)
			tagged.Counter("retained tagged counter").Inc(1)
			tagged.Gauge("retained tagged gauge").Update(1)
			tagged.Timer("retained tagged timer").Record(time.Second)
			guard.WithTags(nil).Counter("late tagged").Inc(1)
			guard.Counter("late counter").Inc(1)
			guard.Gauge("late gauge").Update(1)
			guard.Timer("late timer").Record(time.Second)
			if fixture.factories != factories || fixture.updates != updates {
				t.Fatal("late metrics touched disposed provider")
			}
			if err := gate.wait(t.Context()); err != nil {
				t.Fatal("metric callback leaked admission", err)
			}
		})
	}
}

// requestHeadersFixture exposes mutable source headers and safe injected provider failures.
type requestHeadersFixture struct {
	headers map[string]string
	mode    string
	calls   int
}

// GetHeaders retains error identity only inside the fixture so the guard must redact it.
func (h *requestHeadersFixture) GetHeaders(context.Context) (map[string]string, error) {
	h.calls++
	if h.mode == "panic" {
		panic("private authorization diagnostic")
	}
	if h.mode == "error" {
		return nil, errors.New("private authorization diagnostic")
	}
	return h.headers, nil
}

// TestRequestHeadersEnforceBoundsAndRedaction verifies outgoing metadata is copied and rejected before service invocation.
func TestRequestHeadersEnforceBoundsAndRedaction(t *testing.T) {
	cases := map[string]map[string]string{"success": {"authorization": "synthetic-test-value"}, "long key": {strings.Repeat("k", 257): "v"}, "long value": {"key": strings.Repeat("v", 8193)}}
	for _, name := range []string{"count", "aggregate", "boundary"} {
		values := make(map[string]string)
		count, size := 33, 0
		if name == "aggregate" {
			count, size = 5, 7000
		}
		if name == "boundary" {
			count, size = 4, 8191
		}
		for i := 0; i < count; i++ {
			values[strconv.Itoa(i)] = strings.Repeat("v", size)
		}
		cases[name] = values
	}
	for _, name := range []string{"error", "panic", "closed"} {
		cases[name] = nil
	}
	for name, headers := range cases {
		t.Run(name, func(t *testing.T) {
			fixture := &requestHeadersFixture{headers: headers, mode: name}
			gate := newAdmission()
			if name == "closed" {
				gate.close()
			}
			guard := &guardedHeaders{inner: fixture, gate: gate}
			result, err := guard.GetHeaders(t.Context())
			if name == "success" || name == "boundary" {
				if err != nil || !reflect.DeepEqual(result, headers) {
					t.Fatalf("valid headers changed: %v", err)
				}
				for key := range result {
					result[key] = "changed"
					if headers[key] == "changed" {
						t.Fatal("forwarded headers alias provider map")
					}
					break
				}
			} else {
				if err == nil || strings.Contains(err.Error(), "private") || result != nil {
					t.Fatalf("unsafe header result: %+v %v", result, err)
				}
			}
			if name == "closed" && fixture.calls != 0 {
				t.Fatal("late header factory invoked")
			}
		})
	}
}

// TestRPCAdmissionPreservesNativeErrorsAndContainsPanics checks transport fencing without requiring a live service.
func TestRPCAdmissionPreservesNativeErrorsAndContainsPanics(t *testing.T) {
	for _, mode := range []string{"success", "native error", "panic", "closed"} {
		t.Run(mode, func(t *testing.T) {
			gate := newAdmission()
			if mode == "closed" {
				gate.close()
			}
			calls := 0
			native := errors.New("native service error")
			request, response := &struct{ Value int }{1}, &struct{ Value int }{}
			invoke := func(ctx context.Context, method string, req, resp any, _ *grpc.ClientConn, options ...grpc.CallOption) error {
				calls++
				if ctx != t.Context() || method != "fixture.method" || req != request || resp != response || len(options) != 1 {
					t.Error("RPC arguments changed")
				}
				if mode == "panic" {
					panic("private transport diagnostic")
				}
				if mode == "native error" {
					return native
				}
				response.Value = request.Value
				return nil
			}
			err := admittedRPC(gate)(t.Context(), "fixture.method", request, response, nil, invoke, grpc.WaitForReady(true))
			switch mode {
			case "success":
				if err != nil || response.Value != 1 {
					t.Fatal("RPC result lost", err)
				}
			case "native error":
				if err != native {
					t.Fatal("native service error identity lost", err)
				}
			case "closed":
				if !errors.Is(err, errAdmissionClosed) || calls != 0 {
					t.Fatal("late RPC invoked transport", err)
				}
			case "panic":
				if err == nil || strings.Contains(err.Error(), "private") {
					t.Fatal("transport panic leaked", err)
				}
			}
			if err := gate.wait(t.Context()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// blockingCounterFixture models a metric backend that ignores cancellation until its actual dependency returns.
type blockingCounterFixture struct{ entered, release chan struct{} }

// Inc exposes deterministic barriers for shutdown joining independently of SDK grace timers.
func (c *blockingCounterFixture) Inc(int64) { close(c.entered); <-c.release }

// TestMetricsDrainJoinsActualCallback verifies no cleanup permission follows from a canceled wait alone.
func TestMetricsDrainJoinsActualCallback(t *testing.T) {
	gate := newAdmission()
	fixture := &blockingCounterFixture{entered: make(chan struct{}), release: make(chan struct{})}
	guard := &guardedCounter{inner: fixture, gate: gate}
	done := make(chan struct{})
	go func() { defer close(done); guard.Inc(1) }()
	<-fixture.entered
	gate.close()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := gate.wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("callback escaped drain", err)
	}
	close(fixture.release)
	<-done
	if err := gate.wait(t.Context()); err != nil {
		t.Fatal(err)
	}
}
