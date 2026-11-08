package queue

import (
	"context"
	"errors"

	"github.com/goforj/queue/internal/workflow"
)

// ContinuationBuilder binds named durable hooks to a snapshot of a configured workflow.
// Configure the ChainBuilder or BatchBuilder before passing it to Continue.
type ContinuationBuilder struct {
	definitions []workflow.ContinuationDefinition
	dispatch    func(context.Context, []workflow.ContinuationDefinition) (string, error)
	batch       bool
	err         error
}

// Continue snapshots a queue-owned chain or batch without changing existing builder interfaces.
// Unsupported custom builders and mixed closure callbacks fail when Dispatch is called.
// @group Workflows
//
// Example: named durable completion
//
//	q, err := queue.NewSync(queue.WithResourceID("reports"))
//	if err != nil {
//		return
//	}
//	q.Register("reports:build", func(context.Context, queue.Message) error { return nil })
//	q.Register("reports:complete", func(_ context.Context, m queue.Message) error {
//		invocation, _ := m.Continuation()
//		_ = invocation.ID // Stable across retry, suitable for application idempotency.
//		return nil
//	})
//	if err := q.StartWorkers(context.Background()); err != nil {
//		return
//	}
//	defer q.Shutdown(context.Background())
//	id, err := queue.Continue(q.Chain(queue.NewJob("reports:build"))).
//		OnCompletion(queue.NewJob("reports:complete")).Dispatch(context.Background())
//	_ = id
//	_ = err
func Continue(builder any) *ContinuationBuilder {
	result := &ContinuationBuilder{}
	switch builder := builder.(type) {
	case *chainBuilderAdapter:
		if builder == nil || builder.err != nil {
			result.err = errors.New("invalid chain continuation binding")
			return result
		}
		binder, ok := builder.inner.(workflow.ContinuationBinder)
		if !ok {
			result.err = errors.New("chain builder does not support durable continuations")
			return result
		}
		snapshot, err := binder.BindContinuations(nil)
		if err != nil {
			result.err = err
			return result
		}
		copy := *builder
		inner, ok := snapshot.(workflow.ChainBuilder)
		if !ok {
			result.err = errors.New("invalid chain continuation capability")
			return result
		}
		copy.inner = inner
		result.dispatch = func(ctx context.Context, definitions []workflow.ContinuationDefinition) (string, error) {
			configured, err := copy.inner.(workflow.ContinuationBinder).BindContinuations(definitions)
			if err != nil {
				return "", err
			}
			bound := copy
			inner, ok := configured.(workflow.ChainBuilder)
			if !ok {
				return "", errors.New("invalid chain continuation capability")
			}
			bound.inner = inner
			return bound.Dispatch(ctx)
		}
	case *batchBuilderAdapter:
		result.batch = true
		if builder == nil || builder.err != nil {
			result.err = errors.New("invalid batch continuation binding")
			return result
		}
		binder, ok := builder.inner.(workflow.ContinuationBinder)
		if !ok {
			result.err = errors.New("batch builder does not support durable continuations")
			return result
		}
		snapshot, err := binder.BindContinuations(nil)
		if err != nil {
			result.err = err
			return result
		}
		copy := *builder
		inner, ok := snapshot.(workflow.BatchBuilder)
		if !ok {
			result.err = errors.New("invalid batch continuation capability")
			return result
		}
		copy.inner = inner
		result.dispatch = func(ctx context.Context, definitions []workflow.ContinuationDefinition) (string, error) {
			configured, err := copy.inner.(workflow.ContinuationBinder).BindContinuations(definitions)
			if err != nil {
				return "", err
			}
			bound := copy
			inner, ok := configured.(workflow.BatchBuilder)
			if !ok {
				return "", errors.New("invalid batch continuation capability")
			}
			bound.inner = inner
			return bound.Dispatch(ctx)
		}
	default:
		result.err = errors.New("builder is not a queue-owned durable workflow")
	}
	return result
}

// append retains exact job bytes and defers validation errors until Dispatch.
func (b *ContinuationBuilder) append(phase string, job Job) *ContinuationBuilder {
	if b.err != nil {
		return b
	}
	stored, err := toDirectWorkflowJob(job)
	if err != nil {
		b.err = err
		return b
	}
	b.definitions = append(b.definitions, workflow.ContinuationDefinition{Phase: phase, Job: stored})
	return b
}

// OnSuccess appends a named hook to the workflow's successful aggregate transition.
func (b *ContinuationBuilder) OnSuccess(job Job) *ContinuationBuilder {
	return b.append("success", job)
}

// OnFailure appends a named hook to the first terminal business failure.
func (b *ContinuationBuilder) OnFailure(job Job) *ContinuationBuilder {
	return b.append("failure", job)
}

// OnCompletion appends a named hook to the first aggregate terminal transition.
func (b *ContinuationBuilder) OnCompletion(job Job) *ContinuationBuilder {
	return b.append("completion", job)
}

// OnProgress appends a named hook for each first committed batch member outcome.
func (b *ContinuationBuilder) OnProgress(job Job) *ContinuationBuilder {
	if !b.batch {
		b.err = errors.New("progress continuations require a batch")
	}
	return b.append("progress", job)
}

// Dispatch validates and accepts one immutable workflow with its durable hook definitions.
func (b *ContinuationBuilder) Dispatch(ctx context.Context) (string, error) {
	if b.err != nil {
		return "", b.err
	}
	if b.dispatch == nil {
		return "", errors.New("invalid workflow continuation binding")
	}
	if len(b.definitions) == 0 {
		return "", errors.New("durable continuation binding requires at least one named hook")
	}
	return b.dispatch(ctx, b.definitions)
}

// FindContinuations returns triggered invocation outcomes, including scheduled but unsettled work.
func (r *Queue) FindContinuations(ctx context.Context, id string) ([]ContinuationOutcome, error) {
	if r == nil {
		return nil, errors.New("queue runtime is nil")
	}
	reader, ok := r.b.(workflow.ContinuationReader)
	if !ok {
		return nil, ErrWorkflowUnsupported
	}
	return reader.FindContinuations(ctx, id)
}
