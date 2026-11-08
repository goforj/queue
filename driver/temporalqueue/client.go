package temporalqueue

import (
	"context"
	"errors"
	"reflect"
	"sync"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/log"
	"go.temporal.io/sdk/workflow"
	"google.golang.org/grpc"
)

// ClientBinding owns one immutable client policy shared by queue and native SDK calls.
// Close must follow shutdown of all queues and native callers borrowing Client.
type ClientBinding struct {
	client    client.Client
	options   client.Options
	limits    SerializationLimits
	gate      *admission
	mu        sync.Mutex
	closed    bool
	closeOnce sync.Once
	borrowers int
}

// NewClientBinding creates a lazy SDK client with bounded conversion and redacted diagnostics.
// It does not connect or create a namespace during construction.
func NewClientBinding(options client.Options, limits SerializationLimits) (binding *ClientBinding, err error) {
	defer func() {
		if recover() != nil {
			binding = nil
			err = errors.New("Temporal client adapter configuration rejected")
		}
	}()
	if len(options.ConnectionOptions.DialOptions) > 0 {
		return nil, errors.New("opaque Temporal transport overrides are unsupported")
	}
	if !reflect.ValueOf(options.ExternalStorage).IsZero() {
		return nil, errors.New("experimental Temporal external payload storage is unsupported")
	}
	if options.ConnectionOptions.Authority != "" && options.ConnectionOptions.TLS == nil {
		return nil, errors.New("Temporal server identity requires TLS")
	}
	if options.HeadersProvider != nil && options.ConnectionOptions.TLS == nil {
		return nil, errors.New("Temporal request header providers require verified TLS")
	}
	if len(options.Plugins) > 0 {
		return nil, errors.New("experimental Temporal client plugins are unsupported")
	}
	if limits == (SerializationLimits{}) {
		limits = DefaultSerializationLimits()
	}
	if options.Credentials != nil && (options.ConnectionOptions.TLS == nil || options.ConnectionOptions.TLSDisabled) {
		return nil, errors.New("Temporal credentials require explicit verified TLS")
	}
	if tls := options.ConnectionOptions.TLS; tls != nil {
		if tls.InsecureSkipVerify || options.ConnectionOptions.TLSDisabled {
			return nil, errors.New("Temporal TLS verification must remain enabled")
		}
		options.ConnectionOptions.TLS = tls.Clone()
	}
	if options.Identity == "" {
		options.Identity = newID("queue_")
	}
	if options.Namespace == "" {
		options.Namespace = "default"
	}
	if options.HostPort == "" {
		options.HostPort = client.DefaultHostPort
	}
	if options.ConnectionOptions.MaxPayloadSize > 8<<20 {
		return nil, errors.New("Temporal transport message budget exceeds 8 MiB")
	}
	if options.ConnectionOptions.MaxPayloadSize == 0 {
		options.ConnectionOptions.MaxPayloadSize = 8 << 20
	}
	gate := newAdmission()
	if options.ConnectionOptions.TLS != nil {
		options.ConnectionOptions.TLS, err = guardedTLS(options.ConnectionOptions.TLS, gate)
		if err != nil {
			return nil, err
		}
	}
	dc, err := newGuardedConverter(options.DataConverter, limits, gate)
	if err != nil {
		return nil, err
	}
	options.DataConverter = dc
	options.Interceptors = append([]interceptor.ClientInterceptor(nil), options.Interceptors...)
	for index, item := range options.Interceptors {
		options.Interceptors[index] = &trackedClientInterceptor{inner: item, gate: gate}
	}
	if options.MetricsHandler != nil {
		options.MetricsHandler = &guardedMetrics{inner: options.MetricsHandler, gate: gate}
	}
	if options.HeadersProvider != nil {
		options.HeadersProvider = &guardedHeaders{inner: options.HeadersProvider, gate: gate}
	}
	options.ConnectionOptions.DialOptions = []grpc.DialOption{grpc.WithUnaryInterceptor(admittedRPC(gate))}
	options.Logger = &redactedLogger{inner: options.Logger, gate: gate}
	options.FailureConverter, err = newGuardedFailureConverter(options.FailureConverter, dc, gate)
	if err != nil {
		return nil, err
	}
	options.ContextPropagators = append([]workflow.ContextPropagator(nil), options.ContextPropagators...)
	for i, propagator := range options.ContextPropagators {
		bounded, ok := propagator.(BoundedContextPropagator)
		if !ok || bounded.MaxDecodedBytes() < 1 || bounded.MaxDecodedBytes() > 65536 {
			return nil, errors.New("Temporal context propagator must declare bounded decoding")
		}
		options.ContextPropagators[i] = &guardedPropagator{inner: bounded, gate: gate}
	}
	c, err := client.NewLazyClient(options)
	if err != nil {
		return nil, errors.New("Temporal client configuration rejected")
	}
	return &ClientBinding{client: c, options: options, limits: limits, gate: gate}, nil
}

// Client returns the actual SDK client for signals, updates, queries and typed workflows.
func (b *ClientBinding) Client() client.Client { return b.client }

// Namespace returns the namespace fixed when this binding was constructed.
func (b *ClientBinding) Namespace() string { return b.options.Namespace }

// acquire reserves a borrower without transferring client ownership.
func (b *ClientBinding) acquire() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return errors.New("Temporal client binding is closed")
	}
	b.borrowers++
	return nil
}

// release retires a borrower only after its SDK handle and callbacks are drained.
func (b *ClientBinding) release() { b.mu.Lock(); b.borrowers--; b.mu.Unlock() }

// Close rejects outstanding queue borrowers and joins admitted adapter work.
// Native SDK callers must be joined by the owner before calling Close.
func (b *ClientBinding) Close(ctx context.Context) error {
	b.mu.Lock()
	if b.borrowers != 0 {
		b.mu.Unlock()
		return errors.New("Temporal client binding still has queue borrowers")
	}
	b.closed = true
	b.gate.close()
	b.mu.Unlock()
	if err := b.gate.wait(ctx); err != nil {
		return err
	}
	b.closeOnce.Do(b.client.Close)
	return nil
}

// redactedLogger excludes SDK messages and fields, which may contain payloads or stacks.
type redactedLogger struct {
	inner log.Logger
	gate  *admission
}

// emit protects custom logging lifetime and drops diagnostics after shutdown admission.
func (l *redactedLogger) emit(level string) {
	if l.inner == nil {
		return
	}
	release, err := l.gate.enter()
	if err != nil {
		return
	}
	defer release()
	defer func() { _ = recover() }()
	switch level {
	case "debug":
		l.inner.Debug("Temporal SDK event")
	case "info":
		l.inner.Info("Temporal SDK event")
	case "warn":
		l.inner.Warn("Temporal SDK event")
	default:
		l.inner.Error("Temporal SDK event")
	}
}

// Debug emits only a fixed diagnostic category.
func (l *redactedLogger) Debug(string, ...interface{}) { l.emit("debug") }

// Info emits only a fixed diagnostic category.
func (l *redactedLogger) Info(string, ...interface{}) { l.emit("info") }

// Warn emits only a fixed diagnostic category.
func (l *redactedLogger) Warn(string, ...interface{}) { l.emit("warn") }

// Error emits only a fixed diagnostic category.
func (l *redactedLogger) Error(string, ...interface{}) { l.emit("error") }
