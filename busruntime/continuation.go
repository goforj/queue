package busruntime

import (
	"context"
	"time"
)

// ContinuationSummary contains safe immutable trigger counters without application payloads.
type ContinuationSummary struct {
	Kind      string
	MemberID  string
	Total     int
	Pending   int
	Processed int
	Failed    int
	NextIndex int
	Completed bool
	Cancelled bool
}

// Continuation identifies one durable hook invocation across physical retries.
type Continuation struct {
	ID          string
	WorkflowID  string
	DispatchID  string
	Phase       string
	Sequence    int
	Declaration int
	Trigger     ContinuationSummary
}

// ContinuationOutcome is the durable scheduling and settlement view of one triggered hook.
type ContinuationOutcome struct {
	Invocation      Continuation
	Status          string
	FailureCategory string
	CreatedAt       time.Time
	ScheduledAt     time.Time
	SettledAt       time.Time
}

// CloneDeliveryMetadata isolates invocation pointers when correlation crosses value and context boundaries.
func CloneDeliveryMetadata(metadata DeliveryMetadata) DeliveryMetadata {
	if metadata.Continuation != nil {
		copy := *metadata.Continuation
		metadata.Continuation = &copy
	}
	return metadata
}

// independentExecutionKey marks publication whose accepted delivery outlives the publisher loop.
type independentExecutionKey struct{}

// WithIndependentExecution retains cancellation for admission while marking accepted local execution as independent.
func WithIndependentExecution(ctx context.Context) context.Context {
	return context.WithValue(ctx, independentExecutionKey{}, true)
}

// DeliveryExecutionContext detaches only marked accepted deliveries, preserving ordinary caller cancellation semantics.
func DeliveryExecutionContext(ctx context.Context) context.Context {
	if independent, _ := ctx.Value(independentExecutionKey{}).(bool); independent {
		return context.WithValue(context.WithoutCancel(ctx), independentExecutionKey{}, false)
	}
	return ctx
}
