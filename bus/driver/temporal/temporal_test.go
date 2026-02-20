package temporal

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/goforj/queue/bus"
)

type stubEngine struct {
	req   StartWorkflowRequest
	res   StartWorkflowResult
	err   error
	calls int
}

func (s *stubEngine) StartWorkflow(_ context.Context, req StartWorkflowRequest) (StartWorkflowResult, error) {
	s.calls++
	s.req = req
	if s.err != nil {
		return StartWorkflowResult{}, s.err
	}
	return s.res, nil
}

func TestDispatch_BasicJobSuccess(t *testing.T) {
	eng := &stubEngine{res: StartWorkflowResult{WorkflowID: "wf-1", RunID: "run-1"}}
	a, err := New(Config{
		Namespace: "default",
		TaskQueue: "monitor",
		Engine:    eng,
	})
	if err != nil {
		t.Fatalf("new adapter: %v", err)
	}

	res, err := a.Dispatch(context.Background(), bus.NewJob("monitor:poll", map[string]string{"url": "https://goforj.dev/health"}))
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if eng.calls != 1 {
		t.Fatalf("expected 1 call, got %d", eng.calls)
	}
	if res.DispatchID == "" {
		t.Fatal("expected non-empty dispatch id")
	}
	if eng.req.JobType != "monitor:poll" {
		t.Fatalf("expected job type monitor:poll, got %q", eng.req.JobType)
	}
	if eng.req.TaskQueue != "monitor" {
		t.Fatalf("expected task queue monitor, got %q", eng.req.TaskQueue)
	}
}

func TestDispatch_FailsWithoutEngine(t *testing.T) {
	a, err := New(Config{})
	if err != nil {
		t.Fatalf("new adapter: %v", err)
	}
	_, err = a.Dispatch(context.Background(), bus.NewJob("monitor:poll", nil))
	if !errors.Is(err, ErrNotImplemented) {
		t.Fatalf("expected ErrNotImplemented, got %v", err)
	}
}

func TestDispatch_EngineFailure(t *testing.T) {
	eng := &stubEngine{err: errors.New("engine down")}
	a, err := New(Config{Engine: eng})
	if err != nil {
		t.Fatalf("new adapter: %v", err)
	}
	res, err := a.Dispatch(context.Background(), bus.NewJob("monitor:poll", nil))
	if err == nil {
		t.Fatal("expected error")
	}
	if res.DispatchID == "" {
		t.Fatal("expected non-empty dispatch id on engine failure")
	}
	if err.Error() != "engine down" {
		t.Fatalf("expected engine error, got %v", err)
	}
}

func TestDispatch_PassesJobOptionsToEngine(t *testing.T) {
	eng := &stubEngine{res: StartWorkflowResult{WorkflowID: "wf-1", RunID: "run-1"}}
	a, err := New(Config{
		Namespace: "default",
		TaskQueue: "monitor",
		Engine:    eng,
	})
	if err != nil {
		t.Fatalf("new adapter: %v", err)
	}

	job := bus.NewJob("monitor:poll", map[string]string{"url": "https://goforj.dev/health"}).
		OnQueue("monitor-critical").
		Delay(2 * time.Second).
		Timeout(15 * time.Second).
		Retry(4).
		Backoff(500 * time.Millisecond).
		UniqueFor(30 * time.Second)
	if _, err := a.Dispatch(context.Background(), job); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	if eng.req.TaskQueue != "monitor-critical" {
		t.Fatalf("expected task queue override monitor-critical, got %q", eng.req.TaskQueue)
	}
	if eng.req.Options.Queue != "monitor-critical" {
		t.Fatalf("expected options queue monitor-critical, got %q", eng.req.Options.Queue)
	}
	if eng.req.Options.Delay != 2*time.Second {
		t.Fatalf("expected delay 2s, got %v", eng.req.Options.Delay)
	}
	if eng.req.Options.Timeout != 15*time.Second {
		t.Fatalf("expected timeout 15s, got %v", eng.req.Options.Timeout)
	}
	if eng.req.Options.Retry != 4 {
		t.Fatalf("expected retry 4, got %d", eng.req.Options.Retry)
	}
	if eng.req.Options.Backoff != 500*time.Millisecond {
		t.Fatalf("expected backoff 500ms, got %v", eng.req.Options.Backoff)
	}
	if eng.req.Options.UniqueFor != 30*time.Second {
		t.Fatalf("expected unique_for 30s, got %v", eng.req.Options.UniqueFor)
	}
}

func TestChain_BasicSuccess(t *testing.T) {
	eng := &stubEngine{res: StartWorkflowResult{WorkflowID: "wf-chain-1", RunID: "run-1"}}
	a, err := New(Config{
		Namespace: "default",
		TaskQueue: "monitor",
		Engine:    eng,
	})
	if err != nil {
		t.Fatalf("new adapter: %v", err)
	}

	id, err := a.Chain(
		bus.NewJob("monitor:poll", map[string]string{"url": "https://a"}),
		bus.NewJob("monitor:downsample", map[string]any{"window": "5m"}),
		bus.NewJob("monitor:alert", map[string]any{"level": "critical"}),
	).OnQueue("critical-monitor").Dispatch(context.Background())
	if err != nil {
		t.Fatalf("chain dispatch: %v", err)
	}
	if id == "" {
		t.Fatal("expected non-empty chain id")
	}
	if eng.calls != 1 {
		t.Fatalf("expected 1 engine call, got %d", eng.calls)
	}
	if eng.req.JobType != "bus:chain" {
		t.Fatalf("expected job type bus:chain, got %q", eng.req.JobType)
	}
	if eng.req.TaskQueue != "critical-monitor" {
		t.Fatalf("expected queue override critical-monitor, got %q", eng.req.TaskQueue)
	}
	st, err := a.FindChain(context.Background(), id)
	if err != nil {
		t.Fatalf("find chain: %v", err)
	}
	if st.ChainID != id {
		t.Fatalf("expected chain id %q, got %q", id, st.ChainID)
	}
	var payload struct {
		Steps []wireJob `json:"steps"`
	}
	if err := json.Unmarshal(eng.req.Payload, &payload); err != nil {
		t.Fatalf("unmarshal chain payload: %v", err)
	}
	if len(payload.Steps) != 3 {
		t.Fatalf("expected 3 steps, got %d", len(payload.Steps))
	}
}

func TestChain_RequiresJobs(t *testing.T) {
	eng := &stubEngine{}
	a, err := New(Config{Engine: eng})
	if err != nil {
		t.Fatalf("new adapter: %v", err)
	}
	_, err = a.Chain().Dispatch(context.Background())
	if err == nil {
		t.Fatal("expected error for empty chain")
	}
}

func TestBatch_BasicSuccess(t *testing.T) {
	eng := &stubEngine{res: StartWorkflowResult{WorkflowID: "wf-batch-1", RunID: "run-1"}}
	a, err := New(Config{
		Namespace: "default",
		TaskQueue: "monitor",
		Engine:    eng,
	})
	if err != nil {
		t.Fatalf("new adapter: %v", err)
	}

	id, err := a.Batch(
		bus.NewJob("monitor:poll", map[string]string{"url": "https://a"}),
		bus.NewJob("monitor:downsample", map[string]any{"window": "5m"}),
		bus.NewJob("monitor:alert", map[string]any{"level": "critical"}),
	).Name("Monitor Sweep").OnQueue("batch-monitor").AllowFailures().Dispatch(context.Background())
	if err != nil {
		t.Fatalf("batch dispatch: %v", err)
	}
	if id == "" {
		t.Fatal("expected non-empty batch id")
	}
	if eng.calls != 1 {
		t.Fatalf("expected 1 engine call, got %d", eng.calls)
	}
	if eng.req.JobType != "bus:batch" {
		t.Fatalf("expected job type bus:batch, got %q", eng.req.JobType)
	}
	if eng.req.TaskQueue != "batch-monitor" {
		t.Fatalf("expected queue override batch-monitor, got %q", eng.req.TaskQueue)
	}
	st, err := a.FindBatch(context.Background(), id)
	if err != nil {
		t.Fatalf("find batch: %v", err)
	}
	if st.BatchID != id {
		t.Fatalf("expected batch id %q, got %q", id, st.BatchID)
	}
	var payload struct {
		Name          string    `json:"name"`
		AllowFailures bool      `json:"allow_failures"`
		Steps         []wireJob `json:"steps"`
	}
	if err := json.Unmarshal(eng.req.Payload, &payload); err != nil {
		t.Fatalf("unmarshal batch payload: %v", err)
	}
	if payload.Name != "Monitor Sweep" {
		t.Fatalf("expected payload name Monitor Sweep, got %q", payload.Name)
	}
	if !payload.AllowFailures {
		t.Fatal("expected allow_failures=true")
	}
	if len(payload.Steps) != 3 {
		t.Fatalf("expected 3 steps, got %d", len(payload.Steps))
	}
}

func TestBatch_RequiresJobs(t *testing.T) {
	eng := &stubEngine{}
	a, err := New(Config{Engine: eng})
	if err != nil {
		t.Fatalf("new adapter: %v", err)
	}
	_, err = a.Batch().Dispatch(context.Background())
	if err == nil {
		t.Fatal("expected error for empty batch")
	}
}

func TestFindNotFoundAndPruneNoop(t *testing.T) {
	a, err := New(Config{})
	if err != nil {
		t.Fatalf("new adapter: %v", err)
	}
	if _, err := a.FindChain(context.Background(), "missing"); !errors.Is(err, bus.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for chain, got %v", err)
	}
	if _, err := a.FindBatch(context.Background(), "missing"); !errors.Is(err, bus.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for batch, got %v", err)
	}
	if err := a.Prune(context.Background(), time.Now()); err != nil {
		t.Fatalf("expected prune noop, got %v", err)
	}
}
