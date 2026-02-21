package queue

import (
	"context"
	"errors"
	"testing"
	"time"
)

type queueBackendStub struct {
	dispatchErr error
	pauseErr    error
	resumeErr   error
	stats       StatsSnapshot
	statsErr    error
	startErr    error
	registered  string
	handler     Handler
}

func (s *queueBackendStub) Driver() Driver { return DriverNull }
func (s *queueBackendStub) Dispatch(context.Context, Task) error {
	return s.dispatchErr
}
func (s *queueBackendStub) Shutdown(context.Context) error { return nil }
func (s *queueBackendStub) StartWorkers(context.Context) error {
	return s.startErr
}
func (s *queueBackendStub) Register(taskType string, h Handler) {
	s.registered = taskType
	s.handler = h
}
func (s *queueBackendStub) Pause(context.Context, string) error {
	return s.pauseErr
}
func (s *queueBackendStub) Resume(context.Context, string) error {
	return s.resumeErr
}
func (s *queueBackendStub) Stats(context.Context) (StatsSnapshot, error) {
	return s.stats, s.statsErr
}

type observerRecorder struct {
	events []Event
}

func (r *observerRecorder) Observe(event Event) {
	r.events = append(r.events, event)
}

func TestChannelObserver_DropIfFullAndBlockingSend(t *testing.T) {
	ch := make(chan Event, 1)
	ch <- Event{Kind: EventEnqueueAccepted}

	ChannelObserver{Events: ch, DropIfFull: true}.Observe(Event{Kind: EventEnqueueRejected})
	if len(ch) != 1 {
		t.Fatalf("expected full channel to remain unchanged, got len=%d", len(ch))
	}

	blocking := ChannelObserver{Events: ch, DropIfFull: false}
	<-ch
	blocking.Observe(Event{Kind: EventProcessStarted})
	if len(ch) != 1 {
		t.Fatalf("expected event forwarded to channel, got len=%d", len(ch))
	}

	ChannelObserver{}.Observe(Event{Kind: EventProcessStarted})
}

func TestObservedQueue_DispatchClassifiesErrors(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		expected EventKind
	}{
		{name: "accepted", err: nil, expected: EventEnqueueAccepted},
		{name: "duplicate", err: ErrDuplicate, expected: EventEnqueueDuplicate},
		{name: "canceled", err: context.Canceled, expected: EventEnqueueCanceled},
		{name: "rejected", err: errors.New("boom"), expected: EventEnqueueRejected},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := &observerRecorder{}
			oq := &observedQueue{
				inner:    &queueBackendStub{dispatchErr: tc.err},
				driver:   DriverSync,
				observer: recorder,
			}
			err := oq.Dispatch(context.Background(), NewTask("job:x").OnQueue("default").Retry(1))
			if !errors.Is(err, tc.err) {
				t.Fatalf("expected dispatch err %v, got %v", tc.err, err)
			}
			if len(recorder.events) != 1 {
				t.Fatalf("expected one observed event, got %d", len(recorder.events))
			}
			if recorder.events[0].Kind != tc.expected {
				t.Fatalf("expected event kind %q, got %q", tc.expected, recorder.events[0].Kind)
			}
		})
	}
}

func TestWrapObservedHandler_EmitsRetriedAndArchived(t *testing.T) {
	t.Run("retry path", func(t *testing.T) {
		recorder := &observerRecorder{}
		h := wrapObservedHandler(recorder, DriverSync, "", "job:retry", func(context.Context, Task) error {
			return errors.New("boom")
		})

		err := h(context.Background(), NewTask("job:retry").OnQueue("default").Retry(1).withAttempt(0))
		if err == nil {
			t.Fatal("expected handler error")
		}
		if len(recorder.events) != 3 {
			t.Fatalf("expected 3 events (started/failed/retried), got %d", len(recorder.events))
		}
		if recorder.events[2].Kind != EventProcessRetried {
			t.Fatalf("expected retried event, got %q", recorder.events[2].Kind)
		}
	})

	t.Run("archive path", func(t *testing.T) {
		recorder := &observerRecorder{}
		h := wrapObservedHandler(recorder, DriverSync, "", "job:archive", func(context.Context, Task) error {
			return errors.New("boom")
		})

		err := h(context.Background(), NewTask("job:archive").OnQueue("default").Retry(1).withAttempt(1))
		if err == nil {
			t.Fatal("expected handler error")
		}
		if len(recorder.events) != 3 {
			t.Fatalf("expected 3 events (started/failed/archived), got %d", len(recorder.events))
		}
		if recorder.events[2].Kind != EventProcessArchived {
			t.Fatalf("expected archived event, got %q", recorder.events[2].Kind)
		}
	})
}

func TestObservedQueue_WrapperMethods(t *testing.T) {
	recorder := &observerRecorder{}
	inner := &queueBackendStub{
		stats: StatsSnapshot{
			ByQueue: map[string]QueueCounters{
				"default": {Pending: 2},
			},
		},
	}
	oq := &observedQueue{
		inner:    inner,
		driver:   DriverWorkerpool,
		observer: recorder,
	}

	if err := oq.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers: %v", err)
	}
	snap, err := oq.Stats(context.Background())
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if got := snap.Pending("default"); got != 2 {
		t.Fatalf("expected pending=2, got %d", got)
	}
	if err := oq.Pause(context.Background(), "critical"); err != nil {
		t.Fatalf("pause: %v", err)
	}
	if err := oq.Resume(context.Background(), "critical"); err != nil {
		t.Fatalf("resume: %v", err)
	}
	oq.Register("job:wrapped", func(context.Context, Task) error { return nil })
	if inner.registered != "job:wrapped" {
		t.Fatalf("expected wrapped register call, got %q", inner.registered)
	}
	if oq.Driver() != DriverWorkerpool {
		t.Fatalf("expected driver %q, got %q", DriverWorkerpool, oq.Driver())
	}
	if len(recorder.events) < 2 {
		t.Fatalf("expected pause/resume events, got %d", len(recorder.events))
	}
}

func TestObservedQueue_RegisterBranches(t *testing.T) {
	inner := &queueBackendStub{}
	recorder := &observerRecorder{}
	oq := &observedQueue{
		inner:    inner,
		driver:   DriverSync,
		observer: recorder,
	}

	oq.Register("job:nil", nil)
	// nil handler should pass through without wrapping and without panic.
	if inner.registered != "job:nil" {
		t.Fatalf("expected register call for nil handler, got %q", inner.registered)
	}

	oq.Register("job:wrapped", func(context.Context, Task) error { return nil })
	if inner.handler == nil {
		t.Fatal("expected wrapped handler to be registered")
	}
	if err := inner.handler(context.Background(), NewTask("job:wrapped").OnQueue("default").Retry(0)); err != nil {
		t.Fatalf("wrapped handler returned error: %v", err)
	}
	if len(recorder.events) < 2 {
		t.Fatalf("expected observed start/success events, got %d", len(recorder.events))
	}
}

func TestPruneBefore_Branches(t *testing.T) {
	now := time.Now()
	input := []time.Time{now.Add(-2 * time.Hour), now.Add(-time.Hour), now}
	cutoff := now.Add(-90 * time.Minute)

	pruned := pruneBefore(input, cutoff)
	if len(pruned) != 2 {
		t.Fatalf("expected 2 entries after pruning, got %d", len(pruned))
	}

	unchanged := pruneBefore(pruned, now.Add(-24*time.Hour))
	if len(unchanged) != len(pruned) {
		t.Fatalf("expected unchanged slice length %d, got %d", len(pruned), len(unchanged))
	}
}

func TestStatsSnapshot_AllGetterHelpers(t *testing.T) {
	snapshot := StatsSnapshot{
		ByQueue: map[string]QueueCounters{
			"default": {
				Pending:   1,
				Active:    2,
				Scheduled: 3,
				Retry:     4,
				Archived:  5,
				Processed: 6,
				Failed:    7,
				Paused:    8,
			},
		},
	}
	if got := snapshot.Pending("default"); got != 1 {
		t.Fatalf("expected pending=1, got %d", got)
	}
	if got := snapshot.Active("default"); got != 2 {
		t.Fatalf("expected active=2, got %d", got)
	}
	if got := snapshot.Scheduled("default"); got != 3 {
		t.Fatalf("expected scheduled=3, got %d", got)
	}
	if got := snapshot.RetryCount("default"); got != 4 {
		t.Fatalf("expected retry=4, got %d", got)
	}
	if got := snapshot.Archived("default"); got != 5 {
		t.Fatalf("expected archived=5, got %d", got)
	}
	if got := snapshot.Processed("default"); got != 6 {
		t.Fatalf("expected processed=6, got %d", got)
	}
	if got := snapshot.Failed("default"); got != 7 {
		t.Fatalf("expected failed=7, got %d", got)
	}
	if got := snapshot.Paused("default"); got != 8 {
		t.Fatalf("expected paused=8, got %d", got)
	}
	if snapshot.Pending("missing") != 0 || snapshot.Active("missing") != 0 || snapshot.Scheduled("missing") != 0 {
		t.Fatal("expected missing queue getters to return zero values")
	}
}
