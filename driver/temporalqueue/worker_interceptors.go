package temporalqueue

import (
	"context"

	"github.com/nexus-rpc/sdk-go/nexus"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

// trackedWorkerInterceptor fences user factories before SDK activity/workflow initialization.
type trackedWorkerInterceptor struct {
	interceptor.WorkerInterceptorBase
	inner interceptor.WorkerInterceptor
	gate  *admission
}

// InterceptActivity retains actual user factory lifetime and fences subsequent initialization.
func (i *trackedWorkerInterceptor) InterceptActivity(ctx context.Context, next interceptor.ActivityInboundInterceptor) (result interceptor.ActivityInboundInterceptor) {
	result = &trackedActivityInitialization{gate: i.gate, rejected: true}
	defer func() {
		if recover() != nil {
			result = &trackedActivityInitialization{gate: i.gate, rejected: true}
		}
	}()
	release, err := i.gate.enter()
	if err != nil {
		return result
	}
	defer release()
	return &trackedActivityInitialization{ActivityInboundInterceptorBase: interceptor.ActivityInboundInterceptorBase{Next: i.inner.InterceptActivity(ctx, next)}, gate: i.gate}
}

// InterceptWorkflow fences factories while leaving replay-safe execution under native SDK policy.
func (i *trackedWorkerInterceptor) InterceptWorkflow(ctx workflow.Context, next interceptor.WorkflowInboundInterceptor) (result interceptor.WorkflowInboundInterceptor) {
	result = &trackedWorkflowInitialization{gate: i.gate, rejected: true}
	defer func() {
		if recover() != nil {
			result = &trackedWorkflowInitialization{gate: i.gate, rejected: true}
		}
	}()
	release, err := i.gate.enter()
	if err != nil {
		return result
	}
	defer release()
	return &trackedWorkflowInitialization{WorkflowInboundInterceptorBase: interceptor.WorkflowInboundInterceptorBase{Next: i.inner.InterceptWorkflow(ctx, next)}, gate: i.gate}
}

// InterceptNexusOperation retains user factory lifetime and joins service callbacks through drain.
func (i *trackedWorkerInterceptor) InterceptNexusOperation(ctx context.Context, next interceptor.NexusOperationInboundInterceptor) (result interceptor.NexusOperationInboundInterceptor) {
	result = &trackedNexus{gate: i.gate, rejected: true}
	defer func() {
		if recover() != nil {
			result = &trackedNexus{gate: i.gate, rejected: true}
		}
	}()
	release, err := i.gate.enter()
	if err != nil {
		return result
	}
	defer release()
	return &trackedNexus{NexusOperationInboundInterceptorBase: interceptor.NexusOperationInboundInterceptorBase{Next: i.inner.InterceptNexusOperation(ctx, next)}, gate: i.gate}
}

// trackedActivityInitialization prevents initialization dependencies from escaping execution admission.
type trackedActivityInitialization struct {
	interceptor.ActivityInboundInterceptorBase
	gate     *admission
	rejected bool
}

// Init catches only adapter initialization failures without entering the application activity.
func (i *trackedActivityInitialization) Init(out interceptor.ActivityOutboundInterceptor) (err error) {
	defer func() {
		if recover() != nil {
			err = temporal.NewNonRetryableApplicationError("activity interceptor initialization rejected", "goforj.invalid.v1", nil)
		}
	}()
	if i.rejected {
		return temporal.NewNonRetryableApplicationError("activity interceptor initialization rejected", "goforj.invalid.v1", nil)
	}
	release, err := i.gate.enter()
	if err != nil {
		return err
	}
	defer release()
	return i.Next.Init(out)
}

// trackedWorkflowInitialization turns deterministic adapter initialization failures into entry rejection.
type trackedWorkflowInitialization struct {
	interceptor.WorkflowInboundInterceptorBase
	gate     *admission
	rejected bool
}

// Init records adapter failure without producing an infinite BlockWorkflow task retry.
func (i *trackedWorkflowInitialization) Init(out interceptor.WorkflowOutboundInterceptor) (err error) {
	defer func() {
		if recover() != nil || err != nil {
			i.rejected = true
			err = nil
		}
	}()
	if i.rejected {
		return nil
	}
	release, err := i.gate.enter()
	if err != nil {
		i.rejected = true
		return nil
	}
	defer release()
	return i.Next.Init(out)
}

// ExecuteWorkflow leaves genuine workflow panics untouched after successful adapter initialization.
func (i *trackedWorkflowInitialization) ExecuteWorkflow(ctx workflow.Context, input *interceptor.ExecuteWorkflowInput) (any, error) {
	if i.rejected {
		return nil, temporal.NewNonRetryableApplicationError("workflow interceptor initialization rejected", "goforj.invalid.v1", nil)
	}
	return i.Next.ExecuteWorkflow(ctx, input)
}

// trackedNexus joins actual Nexus initialization, start and cancellation callback return.
type trackedNexus struct {
	interceptor.NexusOperationInboundInterceptorBase
	gate     *admission
	rejected bool
}

// Init fences initialization before service code can acquire application dependencies.
func (i *trackedNexus) Init(ctx context.Context, out interceptor.NexusOperationOutboundInterceptor) error {
	if i.rejected {
		return errAdmissionClosed
	}
	release, err := i.gate.enter()
	if err != nil {
		return err
	}
	defer release()
	return i.Next.Init(ctx, out)
}

// StartOperation preserves native Nexus result types while retaining callback lifetime.
func (i *trackedNexus) StartOperation(ctx context.Context, input interceptor.NexusStartOperationInput) (nexus.HandlerStartOperationResult[any], error) {
	release, err := i.gate.enter()
	if err != nil {
		return nil, err
	}
	defer release()
	return i.Next.StartOperation(ctx, input)
}

// CancelOperation joins actual cancellation callback return independently of the SDK stop timeout.
func (i *trackedNexus) CancelOperation(ctx context.Context, input interceptor.NexusCancelOperationInput) error {
	release, err := i.gate.enter()
	if err != nil {
		return err
	}
	defer release()
	return i.Next.CancelOperation(ctx, input)
}

// InterceptNexusOperation places resource admission outside all inherited worker service interceptors.
func (i *resourceInterceptor) InterceptNexusOperation(ctx context.Context, next interceptor.NexusOperationInboundInterceptor) interceptor.NexusOperationInboundInterceptor {
	return &trackedNexus{NexusOperationInboundInterceptorBase: interceptor.NexusOperationInboundInterceptorBase{Next: next}, gate: i.gate}
}
