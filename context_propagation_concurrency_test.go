package queue

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
)

// concurrentContextPropagator requires each transported identity to come from that delivery's sender context.
type concurrentContextPropagator struct{ extracts atomic.Int64 }

// Inject snapshots the current sender identity so fixed headers cannot hide delivery cross-contamination.
func (*concurrentContextPropagator) Inject(ctx context.Context) map[string]string {
	identity, _ := ctx.Value(propagationKey("trace")).(string)
	return map[string]string{"traceparent": identity}
}

// Extract restores identity on the independently owned receiver context and counts each physical attempt.
func (p *concurrentContextPropagator) Extract(ctx context.Context, headers map[string]string) context.Context {
	p.extracts.Add(1)
	return context.WithValue(ctx, propagationKey("trace"), headers["traceparent"])
}

// concurrentContextBackend gives every physical attempt independent receiver state and applies serialized retry options.
type concurrentContextBackend struct {
	runtimeBackendStub
	driver    Driver
	receivers atomic.Uint64
}

// Driver selects the native or external wrapper while deliveries share the same broker simulation.
func (b *concurrentContextBackend) Driver() Driver { return b.driver }

// Dispatch discards the publisher context and gives retries fresh receiver contexts around the unchanged envelope.
func (b *concurrentContextBackend) Dispatch(_ context.Context, job Job) error {
	attempts := 1
	if job.options.maxRetry != nil {
		attempts += *job.options.maxRetry
	}
	var err error
	for attempt := 0; attempt < attempts; attempt++ {
		job.options.attempt = attempt
		ctx := context.WithValue(context.Background(), propagationKey("receiver"), b.receivers.Add(1))
		err = b.registered[job.Type](ctx, job)
		if err == nil {
			return nil
		}
	}
	return err
}

// newConcurrentContextQueue builds either physical wrapper without retaining publisher contexts in the broker fixture.
func newConcurrentContextQueue(t *testing.T, seam string, backend *concurrentContextBackend, observer Observer, options ...Option) *Queue {
	t.Helper()
	backend.driver = DriverSync
	if seam == "external" {
		backend.driver = DriverNATS
	}
	common := &queueCommon{inner: backend, driver: backend.driver, cfg: Config{Driver: backend.driver, Observer: observer}}
	var physical queueRuntime = &nativeQueueRuntime{common: common, runtime: backend}
	if seam == "external" {
		physical = &externalQueueRuntime{common: common, newWorker: func(int) (driverWorkerBackend, error) { return backend, nil }}
	}
	q, err := newQueueFromRuntime(physical, options...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := q.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return q
}

// concurrentDeliveryScope identifies the receiver and consumer state that must remain owned by one delivery.
type concurrentDeliveryScope struct {
	producer string
	receiver uint64
	consumer string
}

// TestContextPreparationConcurrentDeliveries keeps distinct carriers, receiver state, and observer identities isolated under overlap.
func TestContextPreparationConcurrentDeliveries(t *testing.T) {
	const deliveries = 32
	for _, seam := range []string{"native", "external"} {
		t.Run(seam, func(t *testing.T) {
			propagator := &concurrentContextPropagator{}
			backend := &concurrentContextBackend{}
			var ready sync.WaitGroup
			ready.Add(deliveries)
			release := make(chan struct{})
			var mu sync.Mutex
			events := make(map[string][]EventKind)
			scopes := make(map[string]*concurrentDeliveryScope)
			observer := ObserverFunc(func(ctx context.Context, event Event) {
				if event.Kind != EventProcessStarted && event.Kind != EventProcessSucceeded {
					return
				}
				scope, ok := ctx.Value(propagationKey("scope")).(*concurrentDeliveryScope)
				if !ok || ctx.Value(propagationKey("trace")) != scope.consumer || ctx.Value(propagationKey("receiver")) != scope.receiver {
					t.Error("process observer lost delivery-owned consumer state")
					return
				}
				mu.Lock()
				defer mu.Unlock()
				if previous := scopes[scope.producer]; previous != nil && previous != scope {
					t.Error("process events changed consumer scope within one delivery")
				}
				scopes[scope.producer] = scope
				events[scope.producer] = append(events[scope.producer], event.Kind)
			})
			q := newConcurrentContextQueue(t, seam, backend, observer, WithContextPropagator(propagator), WithHandlerContextDecorator(func(ctx context.Context) context.Context {
				producer, _ := ctx.Value(propagationKey("trace")).(string)
				receiver, _ := ctx.Value(propagationKey("receiver")).(uint64)
				scope := &concurrentDeliveryScope{producer: producer, receiver: receiver, consumer: "consumer:" + producer}
				ctx = context.WithValue(ctx, propagationKey("trace"), scope.consumer)
				ctx = context.WithValue(ctx, propagationKey("scope"), scope)
				ready.Done()
				<-release
				return ctx
			}))
			var handled atomic.Int64
			q.Register("orders.process", func(ctx context.Context, message Message) error {
				var payload struct{ ID string }
				if err := message.Bind(&payload); err != nil {
					return err
				}
				scope, ok := ctx.Value(propagationKey("scope")).(*concurrentDeliveryScope)
				if !ok || scope.producer != payload.ID || scope.receiver == 0 || ctx.Value(propagationKey("trace")) != "consumer:"+payload.ID || ctx.Value(propagationKey("receiver")) != scope.receiver {
					t.Error("handler received another delivery's carrier, receiver, or decorator state")
				}
				handled.Add(1)
				return nil
			})
			if err := q.StartWorkers(context.Background()); err != nil {
				t.Fatal(err)
			}
			var finished sync.WaitGroup
			for index := range deliveries {
				finished.Add(1)
				go func() {
					defer finished.Done()
					identity := fmt.Sprintf("producer-%d", index)
					ctx := context.WithValue(context.Background(), propagationKey("trace"), identity)
					if _, err := q.WithContext(ctx).Dispatch(NewJob("orders.process").Payload(map[string]string{"ID": identity})); err != nil {
						t.Error(err)
					}
				}()
			}
			ready.Wait()
			close(release)
			finished.Wait()
			if handled.Load() != deliveries || propagator.extracts.Load() != deliveries || len(events) != deliveries {
				t.Fatalf("handlers=%d extractions=%d observed deliveries=%d", handled.Load(), propagator.extracts.Load(), len(events))
			}
			receivers := make(map[uint64]bool)
			for identity, kinds := range events {
				if !reflect.DeepEqual(kinds, []EventKind{EventProcessStarted, EventProcessSucceeded}) {
					t.Errorf("delivery %s process events=%v", identity, kinds)
				}
				receiver := scopes[identity].receiver
				if receivers[receiver] {
					t.Error("independent deliveries shared receiver state")
				}
				receivers[receiver] = true
			}
		})
	}
}

// TestContextPreparationFailureRetryAndArchiveObservers preserves each attempt's derived identity through terminal observations.
func TestContextPreparationFailureRetryAndArchiveObservers(t *testing.T) {
	for _, seam := range []string{"native", "external"} {
		t.Run(seam, func(t *testing.T) {
			propagator := &concurrentContextPropagator{}
			backend := &concurrentContextBackend{}
			failure := errors.New("permanent handler failure")
			var events []Event
			consumerByAttempt := make(map[int]string)
			observer := ObserverFunc(func(ctx context.Context, event Event) {
				if event.Kind != EventProcessStarted && event.Kind != EventProcessFailed && event.Kind != EventProcessRetried && event.Kind != EventProcessArchived {
					return
				}
				consumer, _ := ctx.Value(propagationKey("trace")).(string)
				receiver, _ := ctx.Value(propagationKey("receiver")).(uint64)
				if consumer != fmt.Sprintf("consumer:producer:%d", receiver) {
					t.Error("failure lifecycle lost the attempt's derived context")
				}
				if previous := consumerByAttempt[event.Attempt]; previous != "" && previous != consumer {
					t.Error("failure lifecycle changed consumer context within an attempt")
				}
				consumerByAttempt[event.Attempt] = consumer
				events = append(events, event)
			})
			q := newConcurrentContextQueue(t, seam, backend, observer, WithContextPropagator(propagator), WithHandlerContextDecorator(func(ctx context.Context) context.Context {
				if ctx.Value(propagationKey("trace")) != "producer" {
					t.Error("retry decorator did not receive the original transported parent")
				}
				receiver := ctx.Value(propagationKey("receiver")).(uint64)
				return context.WithValue(ctx, propagationKey("trace"), fmt.Sprintf("consumer:producer:%d", receiver))
			}))
			handled := 0
			q.Register("orders.fail", func(ctx context.Context, _ Message) error {
				handled++
				receiver := ctx.Value(propagationKey("receiver")).(uint64)
				if ctx.Value(propagationKey("trace")) != fmt.Sprintf("consumer:producer:%d", receiver) {
					t.Error("handler lost the attempt's consumer identity")
				}
				return failure
			})
			if err := q.StartWorkers(context.Background()); err != nil {
				t.Fatal(err)
			}
			ctx := context.WithValue(context.Background(), propagationKey("trace"), "producer")
			if _, err := q.WithContext(ctx).Dispatch(NewJob("orders.fail").Retry(1)); !errors.Is(err, failure) {
				t.Fatalf("dispatch error=%v want=%v", err, failure)
			}
			wantKinds := []EventKind{EventProcessStarted, EventProcessFailed, EventProcessRetried, EventProcessStarted, EventProcessFailed, EventProcessArchived}
			if len(events) != len(wantKinds) || handled != 2 || propagator.extracts.Load() != 2 {
				t.Fatalf("events=%v handlers=%d extractions=%d", events, handled, propagator.extracts.Load())
			}
			for index, event := range events {
				if event.Kind != wantKinds[index] || event.Attempt != index/3 {
					t.Errorf("event %d=%s attempt=%d", index, event.Kind, event.Attempt)
				}
				if event.Kind == EventProcessFailed && !errors.Is(event.Err, failure) {
					t.Errorf("failure event lost handler error: %v", event.Err)
				}
			}
			if len(consumerByAttempt) != 2 || consumerByAttempt[0] == consumerByAttempt[1] {
				t.Fatal("retries reused the prior attempt's derived context")
			}
		})
	}
}
