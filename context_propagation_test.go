package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// propagationKey separates transport metadata from receiver-owned values in these contract tests.
type propagationKey string

// testContextPropagator supplies deterministic carrier values without introducing a tracing SDK dependency.
type testContextPropagator struct{ headers map[string]string }

// Inject returns the caller-controlled metadata used to check snapshot and input limits.
func (p testContextPropagator) Inject(context.Context) map[string]string { return p.headers }

// Extract restores only the selected correlation key onto the receiver context.
func (testContextPropagator) Extract(ctx context.Context, headers map[string]string) context.Context {
	return context.WithValue(ctx, propagationKey("trace"), headers["traceparent"])
}

// TestContextPropagationOptionReachesLogicalHandlers covers the actual queue execution seam through its existing delivery envelope.
func TestContextPropagationOptionReachesLogicalHandlers(t *testing.T) {
	options := []Option{WithContextPropagator(testContextPropagator{headers: map[string]string{"traceparent": "producer"}})}
	q, err := NewSync(options...)
	if err != nil {
		t.Fatal(err)
	}
	if err := q.StartWorkers(context.Background()); err != nil {
		t.Fatal(err)
	}
	calls := 0
	q.Register("orders.process", func(ctx context.Context, message Message) error {
		calls++
		if ctx.Value(propagationKey("trace")) != "producer" || string(message.PayloadBytes()) != `{"value":"exact"}` {
			t.Error("handler lost propagated context or exact payload")
		}
		return nil
	})
	if _, err := q.Dispatch(NewJob("orders.process").Payload([]byte(`{"value":"exact"}`))); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("handler calls=%d", calls)
	}
	if err := q.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// readingContextPropagator makes delivery identity depend on the sender context and counts physical extraction.
type readingContextPropagator struct{ extracts int }

// Inject snapshots the current identity so missing propagation cannot be hidden by a constant test carrier.
func (*readingContextPropagator) Inject(ctx context.Context) map[string]string {
	identity, _ := ctx.Value(propagationKey("trace")).(string)
	if identity == "" {
		return nil
	}
	return map[string]string{"traceparent": identity}
}

// Extract counts real restorations and models a propagator that would overwrite a decorator's consumer identity.
func (p *readingContextPropagator) Extract(ctx context.Context, headers map[string]string) context.Context {
	p.extracts++
	return context.WithValue(ctx, propagationKey("trace"), headers["traceparent"])
}

// isolatedContextBackend delivers physical jobs on worker-owned contexts for both native and external runtime tests.
type isolatedContextBackend struct {
	runtimeBackendStub
	driver    Driver
	receiver  context.Context
	transform func(Job) Job
}

// Driver preserves the selected seam while the same isolated delivery fixture exercises its wrappers.
func (b *isolatedContextBackend) Driver() Driver { return b.driver }

// Dispatch discards the publisher context so only serialized metadata can reach the handler decorator.
func (b *isolatedContextBackend) Dispatch(_ context.Context, job Job) error {
	if b.transform != nil {
		job = b.transform(job)
	}
	return b.registered[job.Type](b.receiver, job)
}

// TestContextPreparationPrecedesDecoratorAndObservers covers independent native and external receiver boundaries.
func TestContextPreparationPrecedesDecoratorAndObservers(t *testing.T) {
	for _, seam := range []string{"native", "external"} {
		for _, observed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/observer=%v", seam, observed), func(t *testing.T) {
				propagator := &readingContextPropagator{}
				receiver, cancel := context.WithDeadline(context.WithValue(context.Background(), propagationKey("receiver"), "worker"), time.Now().Add(time.Hour))
				defer cancel()
				backend := &isolatedContextBackend{driver: DriverSync, receiver: receiver}
				if seam == "external" {
					backend.driver = DriverNATS
				}
				var processEvents []EventKind
				cfg := Config{Driver: backend.driver}
				if observed {
					cfg.Observer = ObserverFunc(func(ctx context.Context, event Event) {
						if event.Kind != EventProcessStarted && event.Kind != EventProcessSucceeded {
							return
						}
						processEvents = append(processEvents, event.Kind)
						if ctx.Value(propagationKey("trace")) != "consumer" {
							t.Error("process observer lost decorated correlation")
						}
					})
				}
				var transport queueBackend = backend
				var worker driverWorkerBackend = backend
				common := &queueCommon{inner: transport, driver: backend.driver, cfg: cfg}
				var physical queueRuntime = &nativeQueueRuntime{common: common, runtime: backend}
				if seam != "native" {
					physical = &externalQueueRuntime{common: common, newWorker: func(int) (driverWorkerBackend, error) { return worker, nil }}
				}
				decorated := 0
				q, err := newQueueFromRuntime(physical, WithContextPropagator(propagator), WithHandlerContextDecorator(func(ctx context.Context) context.Context {
					decorated++
					if ctx.Value(propagationKey("trace")) != "producer" || ctx.Value(propagationKey("receiver")) != "worker" {
						t.Error("decorator did not receive transported parent and worker values")
					}
					deadline, ok := ctx.Deadline()
					want, _ := receiver.Deadline()
					if !ok || !deadline.Equal(want) {
						t.Error("preparation changed receiver deadline")
					}
					return context.WithValue(ctx, propagationKey("trace"), "consumer")
				}))
				if err != nil {
					t.Fatal(err)
				}
				handled := 0
				q.Register("orders.process", func(ctx context.Context, message Message) error {
					handled++
					if ctx.Value(propagationKey("trace")) != "consumer" || string(message.PayloadBytes()) != `{"value":"exact"}` {
						t.Error("logical extraction overwrote decorator context or changed payload")
					}
					return nil
				})
				if err := q.StartWorkers(receiver); err != nil {
					t.Fatal(err)
				}
				defer q.Shutdown(context.Background())
				ctx := context.WithValue(context.Background(), propagationKey("trace"), "producer")
				if _, err := q.WithContext(ctx).Dispatch(NewJob("orders.process").Payload([]byte(`{"value":"exact"}`))); err != nil {
					t.Fatal(err)
				}
				if handled != 1 || decorated != 1 || propagator.extracts != 1 {
					t.Fatalf("handlers=%d decorators=%d extractions=%d", handled, decorated, propagator.extracts)
				}
				if observed && len(processEvents) != 2 {
					t.Fatalf("process events=%v", processEvents)
				}
			})
		}
	}
}

// TestContextDecoratorWithoutObserverOrPropagator retains the existing decorator contract when optional instrumentation is absent.
func TestContextDecoratorWithoutObserverOrPropagator(t *testing.T) {
	q, err := NewSync(WithHandlerContextDecorator(func(ctx context.Context) context.Context {
		return context.WithValue(ctx, propagationKey("decorated"), true)
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer q.Shutdown(context.Background())
	q.Register("decorated", func(ctx context.Context, _ Message) error {
		if ctx.Value(propagationKey("decorated")) != true {
			t.Error("decorator skipped without observer")
		}
		return nil
	})
	if err := q.StartWorkers(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Dispatch(NewJob("decorated")); err != nil {
		t.Fatal(err)
	}
}

// TestContextPreparationIgnoresMalformedMetadata keeps optional correlation failures out of business delivery.
func TestContextPreparationIgnoresMalformedMetadata(t *testing.T) {
	for _, encoded := range []string{`null`, `42`, `{"traceparent":42}`, `{"traceparent":"` + strings.Repeat("x", 8193) + `"}`} {
		t.Run(encoded[:min(len(encoded), 20)], func(t *testing.T) {
			propagator := &readingContextPropagator{}
			backend := &isolatedContextBackend{driver: DriverSync, receiver: context.Background(), transform: func(job Job) Job {
				var envelope map[string]json.RawMessage
				if err := json.Unmarshal(job.PayloadBytes(), &envelope); err != nil {
					t.Fatal(err)
				}
				envelope["context"] = json.RawMessage(encoded)
				payload, err := json.Marshal(envelope)
				if err != nil {
					t.Fatal(err)
				}
				return job.Payload(payload)
			}}
			common := &queueCommon{inner: backend, driver: DriverSync, cfg: Config{Driver: DriverSync}}
			q, err := newQueueFromRuntime(&nativeQueueRuntime{common: common, runtime: backend}, WithContextPropagator(propagator))
			if err != nil {
				t.Fatal(err)
			}
			defer q.Shutdown(context.Background())
			handled := false
			q.Register("orders.process", func(ctx context.Context, message Message) error {
				handled = true
				if ctx.Value(propagationKey("trace")) != nil || string(message.PayloadBytes()) != `{"value":"exact"}` {
					t.Error("malformed metadata changed business delivery")
				}
				return nil
			})
			if err := q.StartWorkers(context.Background()); err != nil {
				t.Fatal(err)
			}
			if _, err := q.Dispatch(NewJob("orders.process").Payload([]byte(`{"value":"exact"}`))); err != nil {
				t.Fatal(err)
			}
			if !handled || propagator.extracts != 0 {
				t.Fatalf("handled=%v extractions=%d", handled, propagator.extracts)
			}
		})
	}
}

// TestContextPreparationRecognizesOnlyWorkflowJobs prevents application payloads from being mistaken for workflow metadata.
func TestContextPreparationRecognizesOnlyWorkflowJobs(t *testing.T) {
	propagator := &readingContextPropagator{}
	common := queueCommon{contextPropagator: propagator}
	ctx := context.Background()
	if got := common.prepareHandlerContext(ctx, NewJob("application.job").Payload([]byte(`{"context":{"traceparent":"ignored"}}`))); got != ctx || propagator.extracts != 0 {
		t.Fatal("application payload treated as transport envelope")
	}
	if got := common.prepareHandlerContext(ctx, NewJob("bus:job").Payload([]byte(`{`))); got != ctx {
		t.Fatal("invalid envelope changed receiver context")
	}
	for _, payload := range []string{`{"job":{"type":"orders.process"}}`, `{"job":{"context":{"traceparent":"ignored"}}}`, `{"context":`, `{"context":{}}`} {
		if got := common.prepareHandlerContext(ctx, NewJob("bus:job").Payload([]byte(payload))); got != ctx || propagator.extracts != 0 {
			t.Fatal("absent, nested, empty, or invalid carrier changed receiver context")
		}
	}
	many := map[string]string{}
	for index := range 33 {
		many[fmt.Sprint(index)] = "value"
	}
	encoded, err := json.Marshal(map[string]any{"context": many})
	if err != nil {
		t.Fatal(err)
	}
	if got := common.prepareHandlerContext(ctx, NewJob("bus:job").Payload(encoded)); got != ctx || propagator.extracts != 0 {
		t.Fatal("oversized carrier changed receiver context")
	}
}
