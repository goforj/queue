package bus

import (
	"context"
	"errors"

	"github.com/goforj/queue"
)

type BatchBuilder interface {
	Name(name string) BatchBuilder
	OnQueue(queue string) BatchBuilder
	AllowFailures() BatchBuilder
	Progress(fn func(ctx context.Context, st BatchState) error) BatchBuilder
	Then(fn func(ctx context.Context, st BatchState) error) BatchBuilder
	Catch(fn func(ctx context.Context, st BatchState, err error) error) BatchBuilder
	Finally(fn func(ctx context.Context, st BatchState) error) BatchBuilder
	Dispatch(ctx context.Context) (string, error)
}

type batchBuilder struct {
	r           *runtime
	jobs        []Job
	name        string
	queue       string
	allowFailed bool
	progress    func(ctx context.Context, st BatchState) error
	then        func(ctx context.Context, st BatchState) error
	catch       func(ctx context.Context, st BatchState, err error) error
	finally     func(ctx context.Context, st BatchState) error
}

func (b *batchBuilder) Name(name string) BatchBuilder { b.name = name; return b }
func (b *batchBuilder) OnQueue(queue string) BatchBuilder {
	b.queue = queue
	return b
}
func (b *batchBuilder) AllowFailures() BatchBuilder {
	b.allowFailed = true
	return b
}
func (b *batchBuilder) Progress(fn func(ctx context.Context, st BatchState) error) BatchBuilder {
	b.progress = fn
	return b
}
func (b *batchBuilder) Then(fn func(ctx context.Context, st BatchState) error) BatchBuilder {
	b.then = fn
	return b
}
func (b *batchBuilder) Catch(fn func(ctx context.Context, st BatchState, err error) error) BatchBuilder {
	b.catch = fn
	return b
}
func (b *batchBuilder) Finally(fn func(ctx context.Context, st BatchState) error) BatchBuilder {
	b.finally = fn
	return b
}

func (b *batchBuilder) Dispatch(ctx context.Context) (string, error) {
	if len(b.jobs) == 0 {
		return "", errors.New("batch requires at least one job")
	}
	batchID := newID("bat")
	dispatchID := newID("dsp")
	jobs := make([]BatchJob, 0, len(b.jobs))
	for _, job := range b.jobs {
		wj, err := toWireJob(job)
		if err != nil {
			return "", err
		}
		if b.queue != "" && wj.Options.Queue == "" {
			wj.Options.Queue = b.queue
		}
		jobs = append(jobs, BatchJob{
			JobID: newID("job"),
			Job:   wj,
		})
	}
	if err := b.r.store.CreateBatch(ctx, BatchRecord{
		BatchID:     batchID,
		DispatchID:  dispatchID,
		Name:        b.name,
		Queue:       b.queue,
		AllowFailed: b.allowFailed,
		Jobs:        jobs,
		CreatedAt:   b.r.now(),
	}); err != nil {
		return "", err
	}

	b.r.mu.Lock()
	b.r.batchCallbacks[batchID] = batchCallbacks{
		progress: b.progress,
		then:     b.then,
		catch:    b.catch,
		finally:  b.finally,
	}
	b.r.mu.Unlock()

	b.r.emit(Event{SchemaVersion: schemaVersion, EventID: newID("evt"), Kind: EventBatchStarted, DispatchID: dispatchID, BatchID: batchID, Queue: b.queue, Time: b.r.now()})
	for _, job := range jobs {
		if err := b.r.dispatchEnvelope(ctx, internalTaskBatchJob, envelope{
			SchemaVersion: schemaVersion,
			DispatchID:    dispatchID,
			Kind:          "batch_job",
			BatchID:       batchID,
			JobID:         job.JobID,
			Job:           job.Job,
		}); err != nil {
			if st, stErr := b.r.store.GetBatch(ctx, batchID); stErr == nil && (st.Completed || st.Processed > 0 || st.Failed > 0) {
				return batchID, err
			}
			_ = b.r.store.CancelBatch(ctx, batchID)
			st, stErr := b.r.store.GetBatch(ctx, batchID)
			if stErr == nil {
				_ = b.r.invokeBatchCatch(ctx, st, err)
				_ = b.r.invokeBatchFinally(ctx, st)
			}
			b.r.emit(Event{SchemaVersion: schemaVersion, EventID: newID("evt"), Kind: EventBatchFailed, DispatchID: dispatchID, BatchID: batchID, Time: b.r.now(), Err: err})
			b.r.emit(Event{SchemaVersion: schemaVersion, EventID: newID("evt"), Kind: EventBatchCancelled, DispatchID: dispatchID, BatchID: batchID, Time: b.r.now()})
			return batchID, err
		}
	}
	return batchID, nil
}

type batchCallbacks struct {
	progress func(ctx context.Context, st BatchState) error
	then     func(ctx context.Context, st BatchState) error
	catch    func(ctx context.Context, st BatchState, err error) error
	finally  func(ctx context.Context, st BatchState) error
}

func (r *runtime) handleInternalBatchJob(ctx context.Context, task queue.Task) error {
	var env envelope
	if err := task.Bind(&env); err != nil {
		return err
	}
	_ = r.store.MarkBatchJobStarted(ctx, env.BatchID, env.JobID)

	err := r.executeWireJob(ctx, env)
	if err != nil {
		st, done, markErr := r.store.MarkBatchJobFailed(ctx, env.BatchID, env.JobID, err)
		if markErr != nil {
			return markErr
		}
		r.emit(Event{SchemaVersion: schemaVersion, EventID: newID("evt"), Kind: EventBatchFailed, DispatchID: env.DispatchID, BatchID: env.BatchID, JobID: env.JobID, JobType: env.Job.Type, Queue: env.Job.Options.Queue, Time: r.now(), Err: err})
		if st.Cancelled {
			r.emit(Event{SchemaVersion: schemaVersion, EventID: newID("evt"), Kind: EventBatchCancelled, DispatchID: env.DispatchID, BatchID: env.BatchID, Time: r.now()})
		}
		_ = r.dispatchCallback(ctx, env, "batch_catch", err)
		r.invokeBatchProgress(ctx, st)
		if done {
			_ = r.dispatchCallback(ctx, env, "batch_finally", nil)
		}
		return err
	}
	st, done, markErr := r.store.MarkBatchJobSucceeded(ctx, env.BatchID, env.JobID)
	if markErr != nil {
		return markErr
	}
	r.emit(Event{SchemaVersion: schemaVersion, EventID: newID("evt"), Kind: EventBatchProgressed, DispatchID: env.DispatchID, BatchID: env.BatchID, JobID: env.JobID, JobType: env.Job.Type, Queue: env.Job.Options.Queue, Time: r.now()})
	r.invokeBatchProgress(ctx, st)
	if done {
		r.emit(Event{SchemaVersion: schemaVersion, EventID: newID("evt"), Kind: EventBatchCompleted, DispatchID: env.DispatchID, BatchID: env.BatchID, Time: r.now()})
		_ = r.dispatchCallback(ctx, env, "batch_then", nil)
		_ = r.dispatchCallback(ctx, env, "batch_finally", nil)
	}
	return nil
}

func (r *runtime) invokeBatchProgress(ctx context.Context, st BatchState) {
	r.mu.RLock()
	cb := r.batchCallbacks[st.BatchID]
	r.mu.RUnlock()
	if cb.progress != nil {
		_ = cb.progress(ctx, st)
	}
}

func (r *runtime) invokeBatchThen(ctx context.Context, st BatchState) error {
	key := "batch_then:" + st.BatchID
	ok, onceErr := r.callbackOnce(ctx, key)
	if onceErr != nil {
		return onceErr
	}
	if !ok {
		return nil
	}
	r.mu.RLock()
	cb := r.batchCallbacks[st.BatchID]
	r.mu.RUnlock()
	if cb.then != nil {
		_ = cb.then(ctx, st)
	}
	return nil
}

func (r *runtime) invokeBatchCatch(ctx context.Context, st BatchState, err error) error {
	key := "batch_catch:" + st.BatchID
	ok, onceErr := r.callbackOnce(ctx, key)
	if onceErr != nil {
		return onceErr
	}
	if !ok {
		return nil
	}
	r.mu.RLock()
	cb := r.batchCallbacks[st.BatchID]
	r.mu.RUnlock()
	if cb.catch != nil {
		_ = cb.catch(ctx, st, err)
	}
	return nil
}

func (r *runtime) invokeBatchFinally(ctx context.Context, st BatchState) error {
	key := "batch_finally:" + st.BatchID
	ok, onceErr := r.callbackOnce(ctx, key)
	if onceErr != nil {
		return onceErr
	}
	if !ok {
		return nil
	}
	r.mu.RLock()
	cb := r.batchCallbacks[st.BatchID]
	r.mu.RUnlock()
	if cb.finally != nil {
		_ = cb.finally(ctx, st)
	}
	r.mu.Lock()
	delete(r.batchCallbacks, st.BatchID)
	r.mu.Unlock()
	return nil
}

func (r *runtime) callbackOnce(ctx context.Context, key string) (bool, error) {
	return r.store.MarkCallbackInvoked(ctx, key)
}

func (r *runtime) handleInternalCallback(ctx context.Context, task queue.Task) error {
	var env envelope
	if err := task.Bind(&env); err != nil {
		return err
	}
	cbErr := error(nil)
	if env.Error != "" {
		cbErr = errors.New(env.Error)
	}
	start := r.now()
	r.emit(Event{
		SchemaVersion: schemaVersion,
		EventID:       newID("evt"),
		Kind:          EventCallbackStarted,
		DispatchID:    env.DispatchID,
		JobID:         env.JobID,
		ChainID:       env.ChainID,
		BatchID:       env.BatchID,
		Queue:         env.Job.Options.Queue,
		Time:          r.now(),
	})
	var err error
	switch env.CallbackKind {
	case "chain_catch":
		if env.ChainID == "" {
			err = errors.New("chain callback requires chain_id")
			break
		}
		st, stErr := r.store.GetChain(ctx, env.ChainID)
		if stErr != nil {
			err = stErr
			break
		}
		err = r.invokeChainCatch(ctx, st, cbErr)
	case "chain_finally":
		if env.ChainID == "" {
			err = errors.New("chain callback requires chain_id")
			break
		}
		st, stErr := r.store.GetChain(ctx, env.ChainID)
		if stErr != nil {
			err = stErr
			break
		}
		err = r.invokeChainFinally(ctx, st)
	case "batch_catch":
		if env.BatchID == "" {
			err = errors.New("batch callback requires batch_id")
			break
		}
		st, stErr := r.store.GetBatch(ctx, env.BatchID)
		if stErr != nil {
			err = stErr
			break
		}
		err = r.invokeBatchCatch(ctx, st, cbErr)
	case "batch_then":
		if env.BatchID == "" {
			err = errors.New("batch callback requires batch_id")
			break
		}
		st, stErr := r.store.GetBatch(ctx, env.BatchID)
		if stErr != nil {
			err = stErr
			break
		}
		err = r.invokeBatchThen(ctx, st)
	case "batch_finally":
		if env.BatchID == "" {
			err = errors.New("batch callback requires batch_id")
			break
		}
		st, stErr := r.store.GetBatch(ctx, env.BatchID)
		if stErr != nil {
			err = stErr
			break
		}
		err = r.invokeBatchFinally(ctx, st)
	default:
		err = errors.New("unknown callback kind")
	}
	if err != nil {
		r.emit(Event{
			SchemaVersion: schemaVersion,
			EventID:       newID("evt"),
			Kind:          EventCallbackFailed,
			DispatchID:    env.DispatchID,
			JobID:         env.JobID,
			ChainID:       env.ChainID,
			BatchID:       env.BatchID,
			Queue:         env.Job.Options.Queue,
			Duration:      r.now().Sub(start),
			Time:          r.now(),
			Err:           err,
		})
		return err
	}
	r.emit(Event{
		SchemaVersion: schemaVersion,
		EventID:       newID("evt"),
		Kind:          EventCallbackSucceeded,
		DispatchID:    env.DispatchID,
		JobID:         env.JobID,
		ChainID:       env.ChainID,
		BatchID:       env.BatchID,
		Queue:         env.Job.Options.Queue,
		Duration:      r.now().Sub(start),
		Time:          r.now(),
	})
	return nil
}
