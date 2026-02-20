package temporal

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/goforj/queue/bus"
)

var ErrEngineRequired = errors.New("temporal driver requires an engine")

// ErrNotImplemented is kept for backward compatibility.
var ErrNotImplemented = ErrEngineRequired

type Config struct {
	Namespace string
	TaskQueue string
	Engine    Engine
}

type Engine interface {
	StartWorkflow(ctx context.Context, req StartWorkflowRequest) (StartWorkflowResult, error)
}

type StartWorkflowRequest struct {
	DispatchID string
	JobID      string
	Namespace  string
	TaskQueue  string
	JobType    string
	Payload    []byte
	Options    bus.JobOptions
}

type StartWorkflowResult struct {
	WorkflowID string
	RunID      string
}

type Adapter struct {
	cfg Config

	mu          sync.RWMutex
	handlers    map[string]bus.Handler
	chainStates map[string]bus.ChainState
	batchStates map[string]bus.BatchState
}

var _ bus.Bus = (*Adapter)(nil)

func New(cfg Config) (*Adapter, error) {
	if cfg.TaskQueue == "" {
		cfg.TaskQueue = "default"
	}
	return &Adapter{
		cfg:         cfg,
		handlers:    make(map[string]bus.Handler),
		chainStates: make(map[string]bus.ChainState),
		batchStates: make(map[string]bus.BatchState),
	}, nil
}

func (a *Adapter) Register(jobType string, handler bus.Handler) {
	if jobType == "" || handler == nil {
		return
	}
	a.mu.Lock()
	a.handlers[jobType] = handler
	a.mu.Unlock()
}

func (a *Adapter) DispatchCtx(ctx context.Context, job bus.Job) (bus.DispatchResult, error) {
	if a.cfg.Engine == nil {
		return bus.DispatchResult{}, ErrEngineRequired
	}
	wj, err := toWireJob(job)
	if err != nil {
		return bus.DispatchResult{}, err
	}
	dispatchID := newID("dsp")
	jobID := newID("job")
	result, err := a.cfg.Engine.StartWorkflow(ctx, StartWorkflowRequest{
		DispatchID: dispatchID,
		JobID:      jobID,
		Namespace:  a.cfg.Namespace,
		TaskQueue:  queueForJob(a.cfg.TaskQueue, wj.Options.Queue),
		JobType:    wj.Type,
		Payload:    wj.Payload,
		Options:    wj.Options,
	})
	if err != nil {
		return bus.DispatchResult{DispatchID: dispatchID}, err
	}
	_ = result
	return bus.DispatchResult{DispatchID: dispatchID}, nil
}

func (a *Adapter) Chain(jobs ...bus.Job) bus.ChainBuilder {
	return &temporalChainBuilder{adapter: a, jobs: append([]bus.Job(nil), jobs...)}
}

func (a *Adapter) Batch(jobs ...bus.Job) bus.BatchBuilder {
	return &temporalBatchBuilder{adapter: a, jobs: append([]bus.Job(nil), jobs...)}
}

func (a *Adapter) Shutdown(context.Context) error         { return nil }
func (a *Adapter) Prune(context.Context, time.Time) error { return nil }

type temporalChainBuilder struct {
	adapter *Adapter
	jobs    []bus.Job
	queue   string
}

func (b *temporalChainBuilder) OnQueue(queue string) bus.ChainBuilder {
	b.queue = queue
	return b
}
func (b *temporalChainBuilder) Catch(fn func(context.Context, bus.ChainState, error) error) bus.ChainBuilder {
	return b
}
func (b *temporalChainBuilder) Finally(fn func(context.Context, bus.ChainState) error) bus.ChainBuilder {
	return b
}
func (b *temporalChainBuilder) Dispatch(ctx context.Context) (string, error) {
	if len(b.jobs) == 0 {
		return "", errors.New("chain requires at least one job")
	}
	if b.adapter == nil || b.adapter.cfg.Engine == nil {
		return "", ErrEngineRequired
	}
	now := time.Now()
	chainID := newID("chn")
	steps := make([]wireJob, 0, len(b.jobs))
	for _, job := range b.jobs {
		wj, err := toWireJob(job)
		if err != nil {
			return "", err
		}
		steps = append(steps, wj)
	}
	chainPayload, err := json.Marshal(struct {
		Steps []wireJob `json:"steps"`
	}{Steps: steps})
	if err != nil {
		return "", err
	}
	_, err = b.adapter.cfg.Engine.StartWorkflow(ctx, StartWorkflowRequest{
		DispatchID: chainID,
		JobID:      newID("job"),
		Namespace:  b.adapter.cfg.Namespace,
		TaskQueue:  queueForJob(b.adapter.cfg.TaskQueue, b.queue),
		JobType:    "bus:chain",
		Payload:    chainPayload,
		Options:    bus.JobOptions{Queue: b.queue},
	})
	if err != nil {
		return "", err
	}
	b.adapter.mu.Lock()
	b.adapter.chainStates[chainID] = bus.ChainState{
		ChainID:    chainID,
		DispatchID: chainID,
		Queue:      queueForJob(b.adapter.cfg.TaskQueue, b.queue),
		Nodes:      nil,
		NextIndex:  0,
		Completed:  false,
		Failed:     false,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	b.adapter.mu.Unlock()
	return chainID, nil
}

type temporalBatchBuilder struct {
	adapter       *Adapter
	jobs          []bus.Job
	name          string
	queue         string
	allowFailures bool
}

func (b *temporalBatchBuilder) Name(name string) bus.BatchBuilder {
	b.name = name
	return b
}
func (b *temporalBatchBuilder) OnQueue(queue string) bus.BatchBuilder {
	b.queue = queue
	return b
}
func (b *temporalBatchBuilder) AllowFailures() bus.BatchBuilder {
	b.allowFailures = true
	return b
}
func (b *temporalBatchBuilder) Dispatch(ctx context.Context) (string, error) {
	if len(b.jobs) == 0 {
		return "", errors.New("batch requires at least one job")
	}
	if b.adapter == nil || b.adapter.cfg.Engine == nil {
		return "", ErrEngineRequired
	}
	now := time.Now()
	batchID := newID("bat")
	steps := make([]wireJob, 0, len(b.jobs))
	for _, job := range b.jobs {
		wj, err := toWireJob(job)
		if err != nil {
			return "", err
		}
		steps = append(steps, wj)
	}
	batchPayload, err := json.Marshal(struct {
		Name          string    `json:"name"`
		AllowFailures bool      `json:"allow_failures"`
		Steps         []wireJob `json:"steps"`
	}{
		Name:          b.name,
		AllowFailures: b.allowFailures,
		Steps:         steps,
	})
	if err != nil {
		return "", err
	}
	_, err = b.adapter.cfg.Engine.StartWorkflow(ctx, StartWorkflowRequest{
		DispatchID: batchID,
		JobID:      newID("job"),
		Namespace:  b.adapter.cfg.Namespace,
		TaskQueue:  queueForJob(b.adapter.cfg.TaskQueue, b.queue),
		JobType:    "bus:batch",
		Payload:    batchPayload,
		Options:    bus.JobOptions{Queue: b.queue},
	})
	if err != nil {
		return "", err
	}
	b.adapter.mu.Lock()
	b.adapter.batchStates[batchID] = bus.BatchState{
		BatchID:     batchID,
		DispatchID:  batchID,
		Name:        b.name,
		Queue:       queueForJob(b.adapter.cfg.TaskQueue, b.queue),
		AllowFailed: b.allowFailures,
		Total:       len(b.jobs),
		Pending:     len(b.jobs),
		Processed:   0,
		Failed:      0,
		Cancelled:   false,
		Completed:   false,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	b.adapter.mu.Unlock()
	return batchID, nil
}
func (b *temporalBatchBuilder) Progress(fn func(context.Context, bus.BatchState) error) bus.BatchBuilder {
	return b
}
func (b *temporalBatchBuilder) Then(fn func(context.Context, bus.BatchState) error) bus.BatchBuilder {
	return b
}
func (b *temporalBatchBuilder) Catch(fn func(context.Context, bus.BatchState, error) error) bus.BatchBuilder {
	return b
}
func (b *temporalBatchBuilder) Finally(fn func(context.Context, bus.BatchState) error) bus.BatchBuilder {
	return b
}

func (a *Adapter) Dispatch(ctx context.Context, job bus.Job) (bus.DispatchResult, error) {
	return a.DispatchCtx(ctx, job)
}

func (a *Adapter) StartWorkers(context.Context) error { return nil }

func (a *Adapter) FindChain(_ context.Context, chainID string) (bus.ChainState, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	st, ok := a.chainStates[chainID]
	if !ok {
		return bus.ChainState{}, bus.ErrNotFound
	}
	return st, nil
}

func (a *Adapter) FindBatch(_ context.Context, batchID string) (bus.BatchState, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	st, ok := a.batchStates[batchID]
	if !ok {
		return bus.BatchState{}, bus.ErrNotFound
	}
	return st, nil
}

func toWireJob(job bus.Job) (wireJob, error) {
	if job.Type == "" {
		return wireJob{}, errors.New("bus job type is required")
	}
	payload, err := json.Marshal(job.Payload)
	if err != nil {
		return wireJob{}, err
	}
	return wireJob{
		Type:    job.Type,
		Payload: payload,
		Options: job.Options,
	}, nil
}

func queueForJob(defaultQueue, override string) string {
	if override != "" {
		return override
	}
	return defaultQueue
}

type wireJob struct {
	Type    string         `json:"type"`
	Payload []byte         `json:"payload"`
	Options bus.JobOptions `json:"options"`
}

func newID(prefix string) string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return prefix + "_" + hex.EncodeToString(b[:])
}
