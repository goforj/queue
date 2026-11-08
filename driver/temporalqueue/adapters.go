package temporalqueue

import (
	"context"
	"errors"
	"time"

	"go.temporal.io/sdk/client"
	"google.golang.org/grpc"
)

// adapterCall joins actual callback return and suppresses adapter panics in diagnostic paths.
func adapterCall[T any](gate *admission, fallback T, call func() T) (result T) {
	result = fallback
	release, err := gate.enter()
	if err != nil {
		return result
	}
	defer release()
	defer func() {
		if recover() != nil {
			result = fallback
		}
	}()
	return call()
}

// guardedMetrics protects every factory and metric callback through resource cleanup.
type guardedMetrics struct {
	inner client.MetricsHandler
	gate  *admission
}

// WithTags retains SDK tags while fencing factory callbacks.
func (m *guardedMetrics) WithTags(tags map[string]string) client.MetricsHandler {
	return adapterCall[client.MetricsHandler](m.gate, client.MetricsNopHandler, func() client.MetricsHandler { return &guardedMetrics{inner: m.inner.WithTags(tags), gate: m.gate} })
}

// Counter tracks both metric creation and later updates.
func (m *guardedMetrics) Counter(name string) client.MetricsCounter {
	return adapterCall[client.MetricsCounter](m.gate, client.MetricsNopHandler.Counter(name), func() client.MetricsCounter { return &guardedCounter{inner: m.inner.Counter(name), gate: m.gate} })
}

// Gauge tracks both metric creation and later updates.
func (m *guardedMetrics) Gauge(name string) client.MetricsGauge {
	return adapterCall[client.MetricsGauge](m.gate, client.MetricsNopHandler.Gauge(name), func() client.MetricsGauge { return &guardedGauge{inner: m.inner.Gauge(name), gate: m.gate} })
}

// Timer tracks both metric creation and later updates.
func (m *guardedMetrics) Timer(name string) client.MetricsTimer {
	return adapterCall[client.MetricsTimer](m.gate, client.MetricsNopHandler.Timer(name), func() client.MetricsTimer { return &guardedTimer{inner: m.inner.Timer(name), gate: m.gate} })
}

// guardedCounter retains diagnostic callback admission for the actual update.
type guardedCounter struct {
	inner client.MetricsCounter
	gate  *admission
}

// Inc drops late metrics without touching application dependencies.
func (m *guardedCounter) Inc(value int64) {
	adapterCall(m.gate, false, func() bool { m.inner.Inc(value); return true })
}

// guardedGauge retains diagnostic callback admission for the actual update.
type guardedGauge struct {
	inner client.MetricsGauge
	gate  *admission
}

// Update drops late metrics without touching application dependencies.
func (m *guardedGauge) Update(value float64) {
	adapterCall(m.gate, false, func() bool { m.inner.Update(value); return true })
}

// guardedTimer retains diagnostic callback admission for the actual update.
type guardedTimer struct {
	inner client.MetricsTimer
	gate  *admission
}

// Record drops late metrics without touching application dependencies.
func (m *guardedTimer) Record(value time.Duration) {
	adapterCall(m.gate, false, func() bool { m.inner.Record(value); return true })
}

// guardedHeaders protects custom authentication/header providers through actual return.
type guardedHeaders struct {
	inner interface {
		GetHeaders(context.Context) (map[string]string, error)
	}
	gate *admission
}

// GetHeaders bounds metadata before it enters a service request and redacts adapter errors.
func (h *guardedHeaders) GetHeaders(ctx context.Context) (result map[string]string, err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("Temporal request header provider failed")
			result = nil
		}
	}()
	release, err := h.gate.enter()
	if err != nil {
		return nil, err
	}
	defer release()
	headers, err := h.inner.GetHeaders(ctx)
	if err != nil {
		return nil, errors.New("Temporal request header provider failed")
	}
	if len(headers) > 32 {
		return nil, errors.New("Temporal request header count exceeds limit")
	}
	total := 0
	result = make(map[string]string, len(headers))
	for key, value := range headers {
		total += len(key) + len(value)
		if len(key) > 256 || len(value) > 8192 || total > 32768 {
			return nil, errors.New("Temporal request header bytes exceed limit")
		}
		result[key] = value
	}
	return result, nil
}

// admittedRPC retains binding-owned credential and transport dependencies through actual RPC return.
func admittedRPC(gate *admission) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, request, response any, connection *grpc.ClientConn, invoke grpc.UnaryInvoker, options ...grpc.CallOption) (result error) {
		release, err := gate.enter()
		if err != nil {
			return err
		}
		defer release()
		defer func() {
			if recover() != nil {
				result = errors.New("Temporal transport adapter failed")
			}
		}()
		return invoke(ctx, method, request, response, connection, options...)
	}
}
