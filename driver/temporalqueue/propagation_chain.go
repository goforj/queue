package temporalqueue

import (
	"context"

	"go.temporal.io/sdk/workflow"
)

// combinedPropagators preserves SDK adapter order inside one aggregate outgoing-header guard.
type combinedPropagators struct {
	items        []workflow.ContextPropagator
	decodedBound int
}

// MaxDecodedBytes retains the sum of approved adapter decoding declarations.
func (p *combinedPropagators) MaxDecodedBytes() int { return p.decodedBound }

// Inject accumulates every adapter's headers before the enclosing guard forwards any SDK request data.
func (p *combinedPropagators) Inject(ctx context.Context, writer workflow.HeaderWriter) error {
	for _, item := range p.items {
		if err := item.Inject(ctx, writer); err != nil {
			return err
		}
	}
	return nil
}

// Extract preserves the configured order and context returned by each approved adapter.
func (p *combinedPropagators) Extract(ctx context.Context, reader workflow.HeaderReader) (context.Context, error) {
	for _, item := range p.items {
		var err error
		ctx, err = item.Extract(ctx, reader)
		if err != nil {
			return nil, err
		}
	}
	return ctx, nil
}

// InjectFromWorkflow keeps the aggregate scheduling preflight replay-safe and atomic before command creation.
func (p *combinedPropagators) InjectFromWorkflow(ctx workflow.Context, writer workflow.HeaderWriter) error {
	for _, item := range p.items {
		if err := item.InjectFromWorkflow(ctx, writer); err != nil {
			return err
		}
	}
	return nil
}

// ExtractToWorkflow retains rejection markers and adapter context values across the complete configured chain.
func (p *combinedPropagators) ExtractToWorkflow(ctx workflow.Context, reader workflow.HeaderReader) (workflow.Context, error) {
	for _, item := range p.items {
		var err error
		ctx, err = item.ExtractToWorkflow(ctx, reader)
		if err != nil {
			return nil, err
		}
		if category, _ := ctx.Value(headerRejectionKey{}).(string); category != "" {
			return ctx, nil
		}
	}
	return ctx, nil
}
