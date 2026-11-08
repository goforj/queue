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
	return &guardedInboundWorkflow{limits: i.limits, overhead: i.overhead, WorkflowInboundInterceptorBase: interceptor.WorkflowInboundInterceptorBase{Next: next}}
}

// guardedInboundWorkflow leaves genuine native workflow panic policy to the SDK.
type guardedInboundWorkflow struct {
	limits   SerializationLimits
	overhead int
	interceptor.WorkflowInboundInterceptorBase
}

// ExecuteWorkflow turns only adapter preflight rejection into a terminal input failure.
func (i *guardedInboundWorkflow) ExecuteWorkflow(ctx workflow.Context, input *interceptor.ExecuteWorkflowInput) (any, error) {
	if category, _ := ctx.Value(headerRejectionKey{}).(string); category != "" {
		if category == "unavailable" {
			return nil, unavailableAdapterError()
		}
		return nil, temporal.NewNonRetryableApplicationError("Temporal context adapter rejected input", "goforj.invalid.v1", nil)
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
