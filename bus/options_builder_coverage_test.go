package bus_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/goforj/queue"
	"github.com/goforj/queue/bus"
)

type storeStub struct {
	createBatchErr error
}

func (s *storeStub) CreateChain(context.Context, bus.ChainRecord) error { return nil }
func (s *storeStub) AdvanceChain(context.Context, string, string) (*bus.ChainNode, bool, error) {
	return nil, false, nil
}
func (s *storeStub) FailChain(context.Context, string, error) error { return nil }
func (s *storeStub) GetChain(context.Context, string) (bus.ChainState, error) {
	return bus.ChainState{}, bus.ErrNotFound
}
func (s *storeStub) CreateBatch(context.Context, bus.BatchRecord) error { return s.createBatchErr }
func (s *storeStub) MarkBatchJobStarted(context.Context, string, string) error {
	return nil
}
func (s *storeStub) MarkBatchJobSucceeded(context.Context, string, string) (bus.BatchState, bool, error) {
	return bus.BatchState{}, false, nil
}
func (s *storeStub) MarkBatchJobFailed(context.Context, string, string, error) (bus.BatchState, bool, error) {
	return bus.BatchState{}, false, nil
}
func (s *storeStub) CancelBatch(context.Context, string) error { return nil }
func (s *storeStub) GetBatch(context.Context, string) (bus.BatchState, error) {
	return bus.BatchState{}, bus.ErrNotFound
}
func (s *storeStub) MarkCallbackInvoked(context.Context, string) (bool, error) { return true, nil }
func (s *storeStub) Prune(context.Context, time.Time) error                    { return nil }

func TestWithStore_OverrideAndNilGuard(t *testing.T) {
	storeErr := errors.New("store create batch failure")
	overrideStore := &storeStub{createBatchErr: storeErr}

	q := queue.NewFake()
	b, err := bus.NewWithStore(q, bus.NewMemoryStore(), bus.WithStore(overrideStore))
	if err != nil {
		t.Fatalf("new bus with store override: %v", err)
	}
	_, err = b.Batch(bus.NewJob("job:one", nil)).Dispatch(context.Background())
	if !errors.Is(err, storeErr) {
		t.Fatalf("expected override store error, got %v", err)
	}

	bNil, err := bus.NewWithStore(q, overrideStore, bus.WithStore(nil))
	if err != nil {
		t.Fatalf("new bus with nil store option: %v", err)
	}
	_, err = bNil.Batch(bus.NewJob("job:one", nil)).Dispatch(context.Background())
	if !errors.Is(err, storeErr) {
		t.Fatalf("expected base store to remain when WithStore(nil), got %v", err)
	}
}

func TestWithClockAndBatchBuilderMetadata(t *testing.T) {
	q := queue.NewFake()
	fixedNow := time.Date(2024, time.January, 15, 12, 30, 0, 0, time.UTC)

	var observed []bus.Event
	b, err := bus.New(
		q,
		bus.WithClock(func() time.Time { return fixedNow }),
		bus.WithObserver(bus.ObserverFunc(func(_ context.Context, e bus.Event) { observed = append(observed, e) })),
	)
	if err != nil {
		t.Fatalf("new bus: %v", err)
	}

	batchID, err := b.Batch(
		bus.NewJob("job:a", map[string]any{"id": 1}),
	).Name("nightly sweep").
		OnQueue("critical").
		AllowFailures().
		Progress(func(context.Context, bus.BatchState) error { return nil }).
		Then(func(context.Context, bus.BatchState) error { return nil }).
		Catch(func(context.Context, bus.BatchState, error) error { return nil }).
		Finally(func(context.Context, bus.BatchState) error { return nil }).
		Dispatch(context.Background())
	if err != nil {
		t.Fatalf("dispatch batch: %v", err)
	}

	state, err := b.FindBatch(context.Background(), batchID)
	if err != nil {
		t.Fatalf("find batch: %v", err)
	}
	if state.Name != "nightly sweep" {
		t.Fatalf("expected batch name nightly sweep, got %q", state.Name)
	}
	if state.Queue != "critical" {
		t.Fatalf("expected batch queue critical, got %q", state.Queue)
	}
	if !state.AllowFailed {
		t.Fatal("expected allow_failed=true")
	}
	if !state.CreatedAt.Equal(fixedNow) {
		t.Fatalf("expected created_at %v, got %v", fixedNow, state.CreatedAt)
	}

	if len(observed) == 0 {
		t.Fatal("expected emitted events")
	}
	if observed[0].Kind != bus.EventBatchStarted {
		t.Fatalf("expected first event batch started, got %q", observed[0].Kind)
	}
	if !observed[0].Time.Equal(fixedNow) {
		t.Fatalf("expected first event time %v, got %v", fixedNow, observed[0].Time)
	}
}
