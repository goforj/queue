package queue

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestNullQueue_StartWorkersAndRegister(t *testing.T) {
	q := newNullQueue().(*nullQueue)

	q.Register("job:null", func(context.Context, Job) error { return nil })

	if err := q.StartWorkers(nil); err != nil {
		t.Fatalf("nil context should be accepted: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := q.StartWorkers(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled, got %v", err)
	}
}

func TestLocalQueue_PauseResumeStatsAndShutdownStats(t *testing.T) {
	q := newLocalQueueWithConfig(DriverWorkerpool, WorkerpoolConfig{
		Workers:       1,
		QueueCapacity: 4,
	})
	q.Register("job:local:stats", func(_ context.Context, _ Job) error {
		time.Sleep(30 * time.Millisecond)
		return nil
	})
	if err := q.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers failed: %v", err)
	}
	t.Cleanup(func() { _ = q.Shutdown(context.Background()) })

	if err := q.Pause(context.Background(), "default"); err != nil {
		t.Fatalf("pause failed: %v", err)
	}
	pausedSnap, err := q.Stats(context.Background())
	if err != nil {
		t.Fatalf("stats while paused failed: %v", err)
	}
	if pausedSnap.Paused("default") != 1 {
		t.Fatalf("expected paused=1, got %d", pausedSnap.Paused("default"))
	}

	err = q.Dispatch(context.Background(), NewJob("job:local:stats").Payload([]byte("x")).OnQueue("default"))
	if !errors.Is(err, ErrQueuePaused) {
		t.Fatalf("expected ErrQueuePaused, got %v", err)
	}

	if err := q.Resume(context.Background(), "default"); err != nil {
		t.Fatalf("resume failed: %v", err)
	}
	if err := q.Dispatch(context.Background(), NewJob("job:local:stats").Payload([]byte("x")).OnQueue("default").Delay(80*time.Millisecond)); err != nil {
		t.Fatalf("dispatch delayed job failed: %v", err)
	}

	pendingObserved := false
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		snap, statsErr := q.Stats(context.Background())
		if statsErr != nil {
			t.Fatalf("stats failed: %v", statsErr)
		}
		if snap.Paused("default") != 0 {
			t.Fatalf("expected paused=0 after resume, got %d", snap.Paused("default"))
		}
		if snap.Pending("default") >= 1 {
			pendingObserved = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !pendingObserved {
		t.Fatal("expected delayed job to appear in pending stats")
	}

	statsText := q.shutdownStats()
	required := []string{"enqueued=", "started=", "finished=", "inflight=", "delayed_pending=", "queue_len=", "queue_cap="}
	for _, needle := range required {
		if !strings.Contains(statsText, needle) {
			t.Fatalf("shutdown stats missing %q: %s", needle, statsText)
		}
	}
}
