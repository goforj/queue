package temporalqueue

import (
	"context"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// admittedActivity tracks actual callback return independently of the SDK Stop timeout.
type admittedActivity struct {
	interceptor.ActivityInboundInterceptorBase
	gate *admission
}

// ExecuteActivity retains the SDK context and native result/error semantics.
func (i *admittedActivity) ExecuteActivity(ctx context.Context, input *interceptor.ExecuteActivityInput) (any, error) {
	release, err := i.gate.enter()
	if err != nil {
		return nil, err
	}
	defer release()
	return i.Next.ExecuteActivity(ctx, input)
}

// resourceInterceptor places admission outside worker interceptors inherited from client options.
type resourceInterceptor struct {
	limits   SerializationLimits
	overhead int
	interceptor.InterceptorBase
	gate *admission
}

// InterceptActivity fences the complete inherited worker interception chain.
func (i *resourceInterceptor) InterceptActivity(ctx context.Context, next interceptor.ActivityInboundInterceptor) interceptor.ActivityInboundInterceptor {
	return &admittedActivity{ActivityInboundInterceptorBase: interceptor.ActivityInboundInterceptorBase{Next: next}, gate: i.gate}
}

// InterceptWorkflow rejects malformed propagated input before the application workflow runs.
func (i *resourceInterceptor) InterceptWorkflow(ctx workflow.Context, next interceptor.WorkflowInboundInterceptor) interceptor.WorkflowInboundInterceptor {
	return &guardedInboundWorkflow{limits: i.limits, overhead: i.overhead, gate: i.gate, WorkflowInboundInterceptorBase: interceptor.WorkflowInboundInterceptorBase{Next: next}}
}

// guardedInboundWorkflow leaves genuine native workflow panic policy to the SDK.
type guardedInboundWorkflow struct {
	limits   SerializationLimits
	overhead int
	gate     *admission
	interceptor.WorkflowInboundInterceptorBase
}

// ExecuteWorkflow turns only adapter preflight rejection into a terminal input failure.
func (i *guardedInboundWorkflow) ExecuteWorkflow(ctx workflow.Context, input *interceptor.ExecuteWorkflowInput) (any, error) {
	if err := propagatedInputError(ctx); err != nil {
		return nil, err
	}
	for _, argument := range input.Args {
		if definition, ok := argument.(workflowDefinition); ok {
			if definition.JournalBudget != i.limits.MaxPayloadBytes || definition.EncodingOverhead != i.overhead {
				return nil, invalidAdapterError()
			}
		}
	}
	return i.Next.ExecuteWorkflow(ctx, input)
}

// propagatedInputError preserves availability while rejecting malformed context before any native handler.
func propagatedInputError(ctx workflow.Context) error {
	category, _ := ctx.Value(headerRejectionKey{}).(string)
	if category == "unavailable" {
		return unavailableAdapterError()
	}
	if category != "" {
		return temporal.NewNonRetryableApplicationError("Temporal context adapter rejected input", "goforj.invalid.v1", nil)
	}
	return nil
}

// HandleSignal drops malformed messages like SDK corrupt signal arguments and retries transient propagation outages.
func (i *guardedInboundWorkflow) HandleSignal(ctx workflow.Context, input *interceptor.HandleSignalInput) error {
	if err := propagatedInputError(ctx); err != nil {
		if category, _ := ctx.Value(headerRejectionKey{}).(string); category == "unavailable" {
			return err
		}
		workflow.GetLogger(ctx).Warn("Temporal signal context rejected")
		return nil
	}
	release, err := i.gate.enter()
	if err != nil {
		return unavailableAdapterError()
	}
	defer release()
	return i.Next.HandleSignal(ctx, input)
}

// HandleQuery prevents rejected context from reaching read handlers or their application dependencies.
func (i *guardedInboundWorkflow) HandleQuery(ctx workflow.Context, input *interceptor.HandleQueryInput) (any, error) {
	if err := propagatedInputError(ctx); err != nil {
		return nil, err
	}
	release, err := i.gate.enter()
	if err != nil {
		return nil, unavailableAdapterError()
	}
	defer release()
	return i.Next.HandleQuery(ctx, input)
}

// ValidateUpdate rejects untrusted context before the SDK accepts an update.
func (i *guardedInboundWorkflow) ValidateUpdate(ctx workflow.Context, input *interceptor.UpdateInput) error {
	if err := propagatedInputError(ctx); err != nil {
		return err
	}
	release, err := i.gate.enter()
	if err != nil {
		return unavailableAdapterError()
	}
	defer release()
	return i.Next.ValidateUpdate(ctx, input)
}

// ExecuteUpdate also checks replay and handlers whose update validation is omitted by the SDK.
func (i *guardedInboundWorkflow) ExecuteUpdate(ctx workflow.Context, input *interceptor.UpdateInput) (any, error) {
	if err := propagatedInputError(ctx); err != nil {
		return nil, err
	}
	return i.Next.ExecuteUpdate(ctx, input)
}
