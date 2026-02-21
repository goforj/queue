package bus

import (
	"context"
	"errors"

	"github.com/goforj/queue"
)

type ChainBuilder interface {
	// OnQueue applies a default queue to chain jobs that do not set one.
	// @group Chaining
	//
	// Example: set chain queue
	//
	//	chainID, _ := b.Chain(
	//		bus.NewJob("a", nil),
	//		bus.NewJob("b", nil),
	//	).OnQueue("critical").Dispatch(context.Background())
	//	_ = chainID
	OnQueue(queue string) ChainBuilder
	// Catch registers a callback invoked when chain execution fails.
	// @group Chaining
	//
	// Example: chain catch callback
	//
	//	chainID, _ := b.Chain(bus.NewJob("a", nil)).
	//		Catch(func(context.Context, bus.ChainState, error) error { return nil }).
	//		Dispatch(context.Background())
	//	_ = chainID
	Catch(fn func(ctx context.Context, st ChainState, err error) error) ChainBuilder
	// Finally registers a callback invoked once when chain execution finishes.
	// @group Chaining
	//
	// Example: chain finally callback
	//
	//	chainID, _ := b.Chain(bus.NewJob("a", nil)).
	//		Finally(func(context.Context, bus.ChainState) error { return nil }).
	//		Dispatch(context.Background())
	//	_ = chainID
	Finally(fn func(ctx context.Context, st ChainState) error) ChainBuilder
	// Dispatch creates and starts the chain workflow.
	// @group Chaining
	//
	// Example: dispatch chain
	//
	//	chainID, _ := b.Chain(bus.NewJob("a", nil), bus.NewJob("b", nil)).Dispatch(context.Background())
	//	_ = chainID
	Dispatch(ctx context.Context) (string, error)
}

type chainBuilder struct {
	r     *runtime
	jobs  []Job
	queue string
	catch func(ctx context.Context, st ChainState, err error) error
	done  func(ctx context.Context, st ChainState) error
}

func (b *chainBuilder) OnQueue(queue string) ChainBuilder {
	b.queue = queue
	return b
}

func (b *chainBuilder) Catch(fn func(ctx context.Context, st ChainState, err error) error) ChainBuilder {
	b.catch = fn
	return b
}

func (b *chainBuilder) Finally(fn func(ctx context.Context, st ChainState) error) ChainBuilder {
	b.done = fn
	return b
}

func (b *chainBuilder) Dispatch(ctx context.Context) (string, error) {
	if len(b.jobs) == 0 {
		return "", errors.New("chain requires at least one job")
	}
	chainID := newID("chn")
	dispatchID := newID("dsp")
	nodes := make([]ChainNode, 0, len(b.jobs))
	for i, job := range b.jobs {
		wj, err := toWireJob(job)
		if err != nil {
			return "", err
		}
		if b.queue != "" && wj.Options.Queue == "" {
			wj.Options.Queue = b.queue
		}
		nodes = append(nodes, ChainNode{
			NodeID: nodeID(chainID, i),
			Job:    wj,
		})
	}
	if err := b.r.store.CreateChain(ctx, ChainRecord{
		ChainID:    chainID,
		DispatchID: dispatchID,
		Queue:      b.queue,
		Nodes:      nodes,
		CreatedAt:  b.r.now(),
	}); err != nil {
		return "", err
	}
	b.r.mu.Lock()
	b.r.chainCallbacks[chainID] = chainCallbacks{
		catch:   b.catch,
		finally: b.done,
	}
	b.r.mu.Unlock()

	b.r.emit(Event{SchemaVersion: schemaVersion, EventID: newID("evt"), Kind: EventChainStarted, DispatchID: dispatchID, ChainID: chainID, Queue: b.queue, Time: b.r.now()})
	first := nodes[0]
	if err := b.r.dispatchEnvelope(ctx, internalJobChainNode, envelope{
		SchemaVersion: schemaVersion,
		DispatchID:    dispatchID,
		Kind:          "chain_node",
		ChainID:       chainID,
		NodeID:        first.NodeID,
		JobID:         newID("job"),
		Job:           first.Job,
	}); err != nil {
		if st, stErr := b.r.store.GetChain(ctx, chainID); stErr == nil && (st.Failed || st.Completed || st.NextIndex > 0) {
			return chainID, err
		}
		_ = b.r.store.FailChain(ctx, chainID, err)
		st, stErr := b.r.store.GetChain(ctx, chainID)
		if stErr == nil {
			_ = b.r.invokeChainCatch(ctx, st, err)
			_ = b.r.invokeChainFinally(ctx, st)
		}
		b.r.emit(Event{SchemaVersion: schemaVersion, EventID: newID("evt"), Kind: EventChainFailed, DispatchID: dispatchID, ChainID: chainID, Time: b.r.now(), Err: err})
		return chainID, err
	}
	return chainID, nil
}

type chainCallbacks struct {
	catch   func(ctx context.Context, st ChainState, err error) error
	finally func(ctx context.Context, st ChainState) error
}

func nodeID(chainID string, idx int) string {
	return chainID + "_" + newID("n")
}

func (r *runtime) handleInternalChainNode(ctx context.Context, job queue.Job) error {
	var env envelope
	if err := job.Bind(&env); err != nil {
		return err
	}
	err := r.executeWireJob(ctx, env)
	if err != nil {
		_ = r.store.FailChain(ctx, env.ChainID, err)
		r.emit(Event{SchemaVersion: schemaVersion, EventID: newID("evt"), Kind: EventChainFailed, DispatchID: env.DispatchID, ChainID: env.ChainID, JobID: env.JobID, JobType: env.Job.Type, Queue: env.Job.Options.Queue, Time: r.now(), Err: err})
		_ = r.dispatchCallback(ctx, env, "chain_catch", err)
		_ = r.dispatchCallback(ctx, env, "chain_finally", nil)
		return err
	}
	next, done, advErr := r.store.AdvanceChain(ctx, env.ChainID, env.NodeID)
	if advErr != nil {
		return advErr
	}
	if done {
		r.emit(Event{SchemaVersion: schemaVersion, EventID: newID("evt"), Kind: EventChainCompleted, DispatchID: env.DispatchID, ChainID: env.ChainID, Time: r.now()})
		_ = r.dispatchCallback(ctx, env, "chain_finally", nil)
		return nil
	}
	r.emit(Event{SchemaVersion: schemaVersion, EventID: newID("evt"), Kind: EventChainAdvanced, DispatchID: env.DispatchID, ChainID: env.ChainID, Time: r.now()})
	return r.dispatchEnvelope(ctx, internalJobChainNode, envelope{
		SchemaVersion: schemaVersion,
		DispatchID:    env.DispatchID,
		Kind:          "chain_node",
		ChainID:       env.ChainID,
		NodeID:        next.NodeID,
		JobID:         newID("job"),
		Job:           next.Job,
	})
}

func (r *runtime) invokeChainCatch(ctx context.Context, st ChainState, err error) error {
	key := "chain_catch:" + st.ChainID
	ok, onceErr := r.callbackOnce(ctx, key)
	if onceErr != nil {
		return onceErr
	}
	if !ok {
		return nil
	}
	r.mu.RLock()
	cb := r.chainCallbacks[st.ChainID]
	r.mu.RUnlock()
	if cb.catch != nil {
		_ = cb.catch(ctx, st, err)
	}
	return nil
}

func (r *runtime) invokeChainFinally(ctx context.Context, st ChainState) error {
	key := "chain_finally:" + st.ChainID
	ok, onceErr := r.callbackOnce(ctx, key)
	if onceErr != nil {
		return onceErr
	}
	if !ok {
		return nil
	}
	r.mu.RLock()
	cb := r.chainCallbacks[st.ChainID]
	r.mu.RUnlock()
	if cb.finally != nil {
		_ = cb.finally(ctx, st)
	}
	r.mu.Lock()
	delete(r.chainCallbacks, st.ChainID)
	r.mu.Unlock()
	return nil
}
