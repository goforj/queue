package natsqueue

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/goforj/queue"
	"github.com/goforj/queue/busruntime"
	"github.com/goforj/queue/queuecore"
	"github.com/nats-io/nats.go"
)

type natsWorkerSubscriptionStub struct {
	drained  chan struct{}
	once     sync.Once
	drainErr error
	release  chan struct{}
}

// Drain records that intake stopped before worker settlement resources closed.
func (s *natsWorkerSubscriptionStub) Drain() error {
	s.once.Do(func() { close(s.drained) })
	if s.release != nil {
		<-s.release
	}
	return s.drainErr
}

type natsWorkerConnectionLifecycleStub struct {
	mu        sync.Mutex
	published chan struct{}
	drained   chan struct{}
	pubOnce   sync.Once
	drainOnce sync.Once
	closed    bool
	flushErr  error
	drainErr  error
}

// Publish records replacement work and rejects publication after Close.
func (s *natsWorkerConnectionLifecycleStub) Publish(string, []byte) error {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return nats.ErrConnectionClosed
	}
	s.pubOnce.Do(func() { close(s.published) })
	return nil
}

// FlushWithContext completes the fake server roundtrip unless the connection already closed.
func (s *natsWorkerConnectionLifecycleStub) FlushWithContext(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nats.ErrConnectionClosed
	}
	return s.flushErr
}

// Drain records graceful connection drain after every expected replacement publish.
func (s *natsWorkerConnectionLifecycleStub) Drain() error {
	s.drainOnce.Do(func() { close(s.drained) })
	return s.drainErr
}

// Close marks the fake connection unavailable for later publication.
func (s *natsWorkerConnectionLifecycleStub) Close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
}

// newNATSWorkerLifecycleStubs creates observable subscription and connection boundaries for shutdown tests.
func newNATSWorkerLifecycleStubs() (*natsWorkerConnectionLifecycleStub, *natsWorkerSubscriptionStub) {
	return &natsWorkerConnectionLifecycleStub{
		published: make(chan struct{}),
		drained:   make(chan struct{}),
	}, &natsWorkerSubscriptionStub{drained: make(chan struct{})}
}

func TestNATSWorker_NewRegisterAndShutdown(t *testing.T) {
	w := newNATSWorker("nats://example:4222")
	if w.url != "nats://example:4222" {
		t.Fatalf("expected url to be preserved, got %q", w.url)
	}
	if w.workers <= 0 {
		t.Fatalf("expected positive default workers, got %d", w.workers)
	}

	w.Register("", func(context.Context, queue.Job) error { return nil })
	w.Register("job:nil", nil)
	if len(w.handlers) != 0 {
		t.Fatalf("expected ignored registrations, got %d handlers", len(w.handlers))
	}

	w.Register("job:ok", func(context.Context, queue.Job) error { return nil })
	if len(w.handlers) != 1 {
		t.Fatalf("expected one handler, got %d", len(w.handlers))
	}

	if err := w.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown should be no-op with nil conn/sub: %v", err)
	}
}

func TestNATSWorker_StartWorkersCanceledContext(t *testing.T) {
	w := newNATSWorker("nats://example:4222")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := w.StartWorkers(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled, got %v", err)
	}
}

// TestNATSWorkerNilContextAndIdempotentStart verifies a nil startup context is
// normalized and an already-started worker does not reconnect.
func TestNATSWorkerNilContextAndIdempotentStart(t *testing.T) {
	w := newNATSWorker("nats://example:4222")
	connection, subscription := newNATSWorkerLifecycleStubs()
	var calls int
	w.connect = func(string, string, nats.MsgHandler) (natsConnection, natsWorkerSubscription, error) {
		calls++
		return connection, subscription, nil
	}
	if err := w.StartWorkers(nil); err != nil {
		t.Fatalf("start workers with nil context: %v", err)
	}
	if err := w.StartWorkers(context.Background()); err != nil {
		t.Fatalf("idempotent start workers: %v", err)
	}
	if calls != 1 {
		t.Fatalf("connector calls = %d, want 1", calls)
	}
	if err := w.Shutdown(nil); err != nil {
		t.Fatalf("shutdown with nil context: %v", err)
	}
}

// TestNATSWorkerAcceptedCallbackRunsThroughWorkerLifecycle verifies successful
// readiness admits subscribed messages and shutdown waits for their handlers.
func TestNATSWorkerAcceptedCallbackRunsThroughWorkerLifecycle(t *testing.T) {
	w := newNATSWorkerWithConfig(natsWorkerConfig{
		URL:          "nats://example:4222",
		DefaultQueue: "critical",
		Workers:      1,
	})
	connection, subscription := newNATSWorkerLifecycleStubs()
	var callback nats.MsgHandler
	var connectedSubject string
	w.connect = func(_ string, subject string, handler nats.MsgHandler) (natsConnection, natsWorkerSubscription, error) {
		connectedSubject = subject
		callback = handler
		return connection, subscription, nil
	}
	handled := make(chan queue.Job, 1)
	handlerStarted := make(chan struct{})
	releaseHandler := make(chan struct{})
	handlerReleased := false
	defer func() {
		if !handlerReleased {
			close(releaseHandler)
		}
	}()
	w.Register("reports:accepted", func(_ context.Context, job queue.Job) error {
		handled <- job
		close(handlerStarted)
		<-releaseHandler
		return nil
	})
	if err := w.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers: %v", err)
	}
	if connectedSubject != "queue.critical" || callback == nil {
		t.Fatalf("subscription = subject:%q callback:%t", connectedSubject, callback != nil)
	}
	payload, err := json.Marshal(natsMessage{Type: "reports:accepted", Queue: "critical", Payload: []byte("report")})
	if err != nil {
		t.Fatalf("marshal accepted message: %v", err)
	}
	callback(&nats.Msg{Data: payload})
	select {
	case <-handlerStarted:
	case <-time.After(time.Second):
		t.Fatal("accepted callback did not reach its handler")
	}
	job := <-handled
	if job.Type != "reports:accepted" || string(job.PayloadBytes()) != "report" {
		t.Fatalf("handled job = type:%q payload:%q", job.Type, job.PayloadBytes())
	}

	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- w.Shutdown(context.Background()) }()
	select {
	case <-subscription.drained:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not drain the subscription")
	}
	select {
	case err := <-shutdownDone:
		t.Fatalf("shutdown returned before the handler completed: %v", err)
	default:
	}
	select {
	case <-connection.drained:
		t.Fatal("connection drained before the handler completed")
	default:
	}

	handlerReleased = true
	close(releaseHandler)
	select {
	case err := <-shutdownDone:
		if err != nil {
			t.Fatalf("shutdown: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown did not finish after the handler completed")
	}
}

// TestNATSWorkerRejectsStartDuringShutdown verifies callers can retry a timed
// out drain while worker restart remains blocked.
func TestNATSWorkerRejectsStartDuringShutdown(t *testing.T) {
	w := newNATSWorker("nats://example:4222")
	connection, subscription := newNATSWorkerLifecycleStubs()
	subscription.release = make(chan struct{})
	w.conn = connection
	w.sub = subscription
	w.started = true

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := w.Shutdown(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("shutdown error = %v, want context.Canceled", err)
	}
	if err := w.StartWorkers(context.Background()); !errors.Is(err, queue.ErrQueuerShuttingDown) {
		t.Fatalf("start during shutdown error = %v, want ErrQueuerShuttingDown", err)
	}
	close(subscription.release)
	if err := w.Shutdown(context.Background()); err != nil {
		t.Fatalf("retry shutdown: %v", err)
	}
}

// TestNATSWorkerStartRetriesAfterConnectionFailure verifies one transient connect error cannot poison worker startup.
func TestNATSWorkerStartRetriesAfterConnectionFailure(t *testing.T) {
	w := newNATSWorker("nats://example:4222")
	connection, subscription := newNATSWorkerLifecycleStubs()
	connectErr := errors.New("nats unavailable")
	var calls int
	w.connect = func(string, string, nats.MsgHandler) (natsConnection, natsWorkerSubscription, error) {
		calls++
		if calls == 1 {
			return nil, nil, connectErr
		}
		return connection, subscription, nil
	}
	if err := w.StartWorkers(context.Background()); !errors.Is(err, connectErr) {
		t.Fatalf("first start error = %v, want %v", err, connectErr)
	}
	if err := w.StartWorkers(context.Background()); err != nil {
		t.Fatalf("retry start: %v", err)
	}
	if calls != 2 || !w.started || w.conn == nil || w.sub == nil {
		t.Fatalf("retry state = calls:%d started:%t conn:%T sub:%T", calls, w.started, w.conn, w.sub)
	}
	if err := w.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}

// TestNATSWorkerStartRetriesAfterSubscriptionFlushFailure verifies startup is not accepted until the server observes the subscription.
func TestNATSWorkerStartRetriesAfterSubscriptionFlushFailure(t *testing.T) {
	w := newNATSWorker("nats://example:4222")
	firstConnection, firstSubscription := newNATSWorkerLifecycleStubs()
	firstConnection.flushErr = errors.New("subscription flush failed")
	secondConnection, secondSubscription := newNATSWorkerLifecycleStubs()
	connections := []*natsWorkerConnectionLifecycleStub{firstConnection, secondConnection}
	subscriptions := []*natsWorkerSubscriptionStub{firstSubscription, secondSubscription}
	var calls int
	w.connect = func(string, string, nats.MsgHandler) (natsConnection, natsWorkerSubscription, error) {
		index := calls
		calls++
		return connections[index], subscriptions[index], nil
	}
	if err := w.StartWorkers(context.Background()); !errors.Is(err, firstConnection.flushErr) {
		t.Fatalf("first start error = %v, want %v", err, firstConnection.flushErr)
	}
	if !firstConnection.closed || w.started || w.conn != nil || w.sub != nil {
		t.Fatalf("failed flush cleanup = closed:%t started:%t conn:%T sub:%T", firstConnection.closed, w.started, w.conn, w.sub)
	}
	if err := w.StartWorkers(context.Background()); err != nil {
		t.Fatalf("retry start after flush failure: %v", err)
	}
	if calls != 2 || !w.started {
		t.Fatalf("retry state = calls:%d started:%t", calls, w.started)
	}
	if err := w.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}

// TestNATSWorkerFlushFailureRejectsPendingCallbacks verifies a callback that
// arrives during startup cannot escape after readiness fails.
func TestNATSWorkerFlushFailureRejectsPendingCallbacks(t *testing.T) {
	w := newNATSWorker("nats://example:4222")
	connection, subscription := newNATSWorkerLifecycleStubs()
	connection.flushErr = errors.New("subscription flush failed")
	var handled int
	w.Register("job:pending", func(context.Context, queue.Job) error {
		handled++
		return nil
	})
	callbackStarted := make(chan struct{})
	callbackDone := make(chan struct{})
	w.connect = func(_ string, _ string, callback nats.MsgHandler) (natsConnection, natsWorkerSubscription, error) {
		go func() {
			close(callbackStarted)
			callback(&nats.Msg{Data: []byte(`{"type":"job:pending","queue":"default"}`)})
			close(callbackDone)
		}()
		<-callbackStarted
		return connection, subscription, nil
	}

	if err := w.StartWorkers(context.Background()); !errors.Is(err, connection.flushErr) {
		t.Fatalf("start error = %v, want %v", err, connection.flushErr)
	}
	<-callbackDone
	w.running.Wait()
	if handled != 0 {
		t.Fatalf("failed startup accepted %d callbacks", handled)
	}
}

// TestConnectNATSWorkerRejectsInvalidURL verifies the production connector
// returns parse failures before attempting subscription setup.
func TestConnectNATSWorkerRejectsInvalidURL(t *testing.T) {
	nc, sub, err := connectNATSWorker("://bad-url", "queue.default", func(*nats.Msg) {})
	if err == nil || nc != nil || sub != nil {
		t.Fatalf("invalid connector result = conn:%T sub:%T err:%v", nc, sub, err)
	}
}

// TestNATSWorkerDefaultConnectorRejectsInvalidURL verifies StartWorkers uses
// the production connector when no custom lifecycle connector is configured.
func TestNATSWorkerDefaultConnectorRejectsInvalidURL(t *testing.T) {
	w := newNATSWorker("://bad-url")
	if err := w.StartWorkers(context.Background()); err == nil {
		t.Fatal("expected default connector to reject invalid URL")
	}
	if w.started || w.conn != nil || w.sub != nil {
		t.Fatalf("failed default start retained state: started:%t conn:%T sub:%T", w.started, w.conn, w.sub)
	}
}

// TestNATSWorkerRepublishRejected verifies a publish rejection is returned
// directly and never followed by a flush.
func TestNATSWorkerRepublishRejected(t *testing.T) {
	publishErr := errors.New("retry publish rejected")
	connection := &natsConnectionStub{publishErr: publishErr}
	w := &natsWorker{conn: connection}
	if err := w.republish(natsMessage{Type: "job:retry", Queue: "default"}); !errors.Is(err, publishErr) {
		t.Fatalf("republish error = %v, want %v", err, publishErr)
	}
	if connection.publishN != 1 || connection.flushN != 0 {
		t.Fatalf("publish/flush calls = %d/%d, want 1/0", connection.publishN, connection.flushN)
	}
}

// TestNATSWorkerRepublishFlushFailure verifies a lost flush response remains
// an ambiguous error after the retry publish reaches the connection.
func TestNATSWorkerRepublishFlushFailure(t *testing.T) {
	flushErr := errors.New("retry flush response lost")
	connection := &natsConnectionStub{flushErr: flushErr}
	w := &natsWorker{conn: connection}
	if err := w.republish(natsMessage{Type: "job:retry", Queue: "default"}); !errors.Is(err, flushErr) {
		t.Fatalf("republish error = %v, want %v", err, flushErr)
	}
	if connection.publishN != 1 || connection.flushN != 1 {
		t.Fatalf("publish/flush calls = %d/%d, want 1/1", connection.publishN, connection.flushN)
	}
}

// TestNATSWorkerShutdownDrainDiagnosticConverges verifies completed cleanup does not poison every later root shutdown attempt.
func TestNATSWorkerShutdownDrainDiagnosticConverges(t *testing.T) {
	w := newNATSWorker("nats://example:4222")
	connection, subscription := newNATSWorkerLifecycleStubs()
	drainErr := errors.New("subscription drain diagnostic")
	subscription.drainErr = drainErr
	w.conn = connection
	w.sub = subscription
	w.started = true
	if err := w.Shutdown(context.Background()); !errors.Is(err, drainErr) {
		t.Fatalf("first shutdown error = %v, want %v", err, drainErr)
	}
	if err := w.Shutdown(context.Background()); err != nil {
		t.Fatalf("completed cleanup remained poisoned: %v", err)
	}
}

// TestNATSWorkerShutdownConnectionDrainDiagnosticConverges verifies a completed
// connection drain is reported once without poisoning later root cleanup.
func TestNATSWorkerShutdownConnectionDrainDiagnosticConverges(t *testing.T) {
	w := newNATSWorker("nats://example:4222")
	connection, subscription := newNATSWorkerLifecycleStubs()
	drainErr := errors.New("connection drain diagnostic")
	connection.drainErr = drainErr
	w.conn = connection
	w.sub = subscription
	w.started = true

	if err := w.Shutdown(context.Background()); !errors.Is(err, drainErr) {
		t.Fatalf("first shutdown error = %v, want %v", err, drainErr)
	}
	if !connection.closed {
		t.Fatal("connection remained open after its drain diagnostic")
	}
	if err := w.Shutdown(context.Background()); err != nil {
		t.Fatalf("completed connection cleanup remained poisoned: %v", err)
	}
}

// TestNATSWorkerShutdownWaitsForInFlightRepublish verifies the connection remains open through a handler's best-effort Core NATS retry publication.
func TestNATSWorkerShutdownWaitsForInFlightRepublish(t *testing.T) {
	w := newNATSWorker("nats://example:4222")
	connection, subscription := newNATSWorkerLifecycleStubs()
	w.conn = connection
	w.sub = subscription
	w.started = true
	handlerStarted := make(chan struct{})
	releaseHandler := make(chan struct{})
	w.Register("job:retry-on-shutdown", func(context.Context, queue.Job) error {
		close(handlerStarted)
		<-releaseHandler
		return errors.New("retry me")
	})
	payload, err := json.Marshal(natsMessage{Type: "job:retry-on-shutdown", Queue: "default", MaxRetry: 1})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	w.running.Add(1)
	go func() {
		defer w.running.Done()
		w.processMessage(&nats.Msg{Data: payload})
	}()
	<-handlerStarted
	shutdownResult := make(chan error, 1)
	go func() { shutdownResult <- w.Shutdown(context.Background()) }()
	<-subscription.drained
	select {
	case <-connection.drained:
		t.Fatal("connection drained before the in-flight handler finished")
	default:
	}
	close(releaseHandler)
	<-connection.published
	if err := <-shutdownResult; err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	select {
	case <-connection.drained:
	default:
		t.Fatal("connection did not drain after replacement publication")
	}
}

// TestNATSWorkerShutdownFlushesDelayedRepublish verifies maintenance can return long-delay work to Core NATS without waiting for its due time.
func TestNATSWorkerShutdownFlushesDelayedRepublish(t *testing.T) {
	w := newNATSWorker("nats://example:4222")
	connection, subscription := newNATSWorkerLifecycleStubs()
	w.conn = connection
	w.sub = subscription
	w.started = true
	payload, err := json.Marshal(natsMessage{
		Type:          "job:delayed-shutdown",
		Queue:         "default",
		AvailableAtMS: time.Now().Add(time.Hour).UnixMilli(),
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	w.processMessage(&nats.Msg{Data: payload})
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := w.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	select {
	case <-connection.published:
	default:
		t.Fatal("shutdown returned before delayed replacement publication")
	}
}

func TestNATSWorker_ProcessMessageBranches(t *testing.T) {
	t.Run("invalid json ignored", func(t *testing.T) {
		w := newNATSWorker("nats://example:4222")
		w.processMessage(&nats.Msg{Data: []byte("{")})
	})

	t.Run("missing handler ignored", func(t *testing.T) {
		w := newNATSWorker("nats://example:4222")
		body, err := json.Marshal(natsMessage{Type: "job:none", Queue: "default"})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		w.processMessage(&nats.Msg{Data: body})
	})

	t.Run("expired availability processes immediately", func(t *testing.T) {
		w := newNATSWorker("nats://example:4222")
		var calls int
		w.Register("job:ready", func(context.Context, queue.Job) error {
			calls++
			return nil
		})
		body, err := json.Marshal(natsMessage{
			Type:          "job:ready",
			Queue:         "default",
			AvailableAtMS: time.Now().Add(-time.Second).UnixMilli(),
		})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}

		w.processMessage(&nats.Msg{Data: body})

		if calls != 1 {
			t.Fatalf("immediate handler calls = %d, want 1", calls)
		}
	})

	t.Run("success uses timeout and job options", func(t *testing.T) {
		called := 0
		w := newNATSWorker("nats://example:4222")
		w.Register("job:ok", func(ctx context.Context, job queue.Job) error {
			called++
			if _, ok := ctx.Deadline(); !ok {
				t.Fatal("expected timeout context")
			}
			opts := queuecore.DriverOptions(job)
			if job.Type != "job:ok" || opts.QueueName != "critical" || opts.Attempt != 2 {
				t.Fatalf("unexpected job fields: type=%q queue=%q attempt=%d", job.Type, opts.QueueName, opts.Attempt)
			}
			if opts.MaxRetry == nil || *opts.MaxRetry != 3 {
				t.Fatalf("expected retry=3, got %+v", opts.MaxRetry)
			}
			return nil
		})
		body, err := json.Marshal(natsMessage{Type: "job:ok", Queue: "critical", Attempt: 2, MaxRetry: 3, TimeoutMillis: 20})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		w.processMessage(&nats.Msg{Data: body})
		if called != 1 {
			t.Fatalf("expected handler once, got %d", called)
		}
	})

	t.Run("future message schedules republish without panic", func(t *testing.T) {
		w := newNATSWorker("nats://example:4222")
		body, err := json.Marshal(natsMessage{Type: "job:future", Queue: "default", AvailableAtMS: time.Now().Add(10 * time.Millisecond).UnixMilli()})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		w.processMessage(&nats.Msg{Data: body})
		time.Sleep(20 * time.Millisecond)
	})

	t.Run("failed handler with retries calls republish path", func(t *testing.T) {
		var events []queue.Event
		w := newNATSWorkerWithConfig(natsWorkerConfig{
			URL:      "nats://example:4222",
			Workers:  1,
			Observer: queue.ObserverFunc(func(_ context.Context, e queue.Event) { events = append(events, e) }),
		})
		w.Register("job:fail", func(context.Context, queue.Job) error { return errors.New("boom") })
		body, err := json.Marshal(natsMessage{Type: "job:fail", Queue: "default", Attempt: 0, MaxRetry: 2, BackoffMillis: 5})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		w.processMessage(&nats.Msg{Data: body})
		if len(events) == 0 || events[0].Kind != queue.EventRepublishFailed || events[0].Driver != queue.DriverNATS {
			t.Fatalf("expected republish_failed nats event, got %+v", events)
		}
		if events[0].Layer != queue.EventLayerWorker {
			t.Fatalf("republish_failed layer = %q, want worker", events[0].Layer)
		}
	})

	t.Run("republish failure unwraps bus envelope job type", func(t *testing.T) {
		var events []queue.Event
		w := newNATSWorkerWithConfig(natsWorkerConfig{
			URL:      "nats://example:4222",
			Workers:  1,
			Observer: queue.ObserverFunc(func(_ context.Context, e queue.Event) { events = append(events, e) }),
		})
		w.Register("bus:job", func(context.Context, queue.Job) error { return errors.New("boom") })
		body, err := json.Marshal(natsMessage{
			Type:          "bus:job",
			Queue:         "default",
			Attempt:       0,
			MaxRetry:      2,
			BackoffMillis: 5,
			Payload:       []byte(`{"schema_version":1,"dispatch_id":"dsp_nats","job_id":"job_nats","chain_id":"chn_nats","job":{"type":"monitoring:check"}}`),
		})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		w.processMessage(&nats.Msg{Data: body})
		if len(events) == 0 {
			t.Fatal("expected republish failure event")
		}
		if events[0].JobType != "monitoring:check" {
			t.Fatalf("expected unwrapped observed job type, got %q", events[0].JobType)
		}
		if events[0].DispatchID != "dsp_nats" || events[0].JobID != "job_nats" || events[0].ChainID != "chn_nats" {
			t.Fatalf("expected correlated nats event, got %+v", events[0])
		}
	})

	t.Run("failed handler at max retries stops", func(t *testing.T) {
		w := newNATSWorker("nats://example:4222")
		w.Register("job:terminal", func(context.Context, queue.Job) error { return errors.New("boom") })
		body, err := json.Marshal(natsMessage{Type: "job:terminal", Queue: "default", Attempt: 2, MaxRetry: 2})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		w.processMessage(&nats.Msg{Data: body})
	})
}

// TestNATSWorker_AttemptDecisionSettlement verifies terminal and uncommitted outcomes choose distinct Core NATS settlement paths.
func TestNATSWorker_AttemptDecisionSettlement(t *testing.T) {
	t.Run("permanent failure does not republish", func(t *testing.T) {
		var events []queue.Event
		w := newNATSWorkerWithConfig(natsWorkerConfig{
			URL:      "nats://example:4222",
			Observer: queue.ObserverFunc(func(_ context.Context, event queue.Event) { events = append(events, event) }),
		})
		w.Register("job:permanent", func(ctx context.Context, _ queue.Job) error {
			attempt, ok := busruntime.DeliveryAttemptFromContext(ctx)
			if !ok || attempt.Number != 0 || attempt.MaxRetry != 3 {
				t.Fatalf("unexpected delivery attempt: %+v, present=%t", attempt, ok)
			}
			return busruntime.Permanent(errors.New("invalid job"))
		})
		body, err := json.Marshal(natsMessage{Type: "job:permanent", Queue: "default", MaxRetry: 3})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}

		w.processMessage(&nats.Msg{Data: body})

		if len(events) != 0 {
			t.Fatalf("permanent failure must not reach the republish path, got %+v", events)
		}
	})

	t.Run("uncommitted failure republishes the same attempt", func(t *testing.T) {
		var events []queue.Event
		w := newNATSWorkerWithConfig(natsWorkerConfig{
			URL:      "nats://example:4222",
			Observer: queue.ObserverFunc(func(_ context.Context, event queue.Event) { events = append(events, event) }),
		})
		w.Register("job:uncommitted", func(ctx context.Context, _ queue.Job) error {
			attempt, ok := busruntime.DeliveryAttemptFromContext(ctx)
			if !ok || attempt.Number != 1 || attempt.MaxRetry != 4 {
				t.Fatalf("unexpected delivery attempt: %+v, present=%t", attempt, ok)
			}
			return busruntime.Uncommitted(errors.New("store unavailable"))
		})
		body, err := json.Marshal(natsMessage{
			Type:          "job:uncommitted",
			Queue:         "default",
			Attempt:       1,
			MaxRetry:      4,
			BackoffMillis: 1_000,
		})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}

		w.processMessage(&nats.Msg{Data: body})

		if len(events) != 1 || events[0].Kind != queue.EventRepublishFailed {
			t.Fatalf("core NATS must attempt to republish uncommitted work, got %+v", events)
		}
		if events[0].Attempt != 1 || events[0].MaxRetry != 4 {
			t.Fatalf("uncommitted republish consumed an application attempt: %+v", events[0])
		}
	})

	t.Run("successful uncommitted republish preserves attempt and clears delay", func(t *testing.T) {
		connection := &natsConnectionStub{}
		w := newNATSWorker("nats://example:4222")
		w.conn = connection
		w.Register("job:uncommitted", func(context.Context, queue.Job) error {
			return busruntime.Uncommitted(errors.New("store unavailable"))
		})
		body, err := json.Marshal(natsMessage{
			Type:          "job:uncommitted",
			Queue:         "default",
			Attempt:       1,
			MaxRetry:      4,
			AvailableAtMS: time.Now().Add(-time.Minute).UnixMilli(),
		})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}

		w.processMessage(&nats.Msg{Data: body})

		if connection.publishN != 1 || connection.flushN != 1 {
			t.Fatalf("republish calls = publish:%d flush:%d, want 1/1", connection.publishN, connection.flushN)
		}
		var republished natsMessage
		if err := json.Unmarshal(connection.published[0], &republished); err != nil {
			t.Fatalf("decode republished message: %v", err)
		}
		if republished.Attempt != 1 || republished.AvailableAtMS != 0 {
			t.Fatalf("republished attempt/delay = %d/%d, want 1/0", republished.Attempt, republished.AvailableAtMS)
		}
	})
}
