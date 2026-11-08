package temporalqueue

import (
	"context"
	"errors"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/workflow"
)

// trackedClientInterceptor preserves dual client/worker interceptors under their binding owner.
type trackedClientInterceptor struct {
	interceptor.InterceptorBase
	inner interceptor.ClientInterceptor
	gate  *admission
}

// InterceptClient retains actual factory return before exposing an admitted outbound chain.
func (i *trackedClientInterceptor) InterceptClient(next interceptor.ClientOutboundInterceptor) (result interceptor.ClientOutboundInterceptor) {
	result = &trackedClientOutbound{gate: i.gate, rejected: true}
	defer func() {
		if recover() != nil {
			result = &trackedClientOutbound{gate: i.gate, rejected: true}
		}
	}()
	release, err := i.gate.enter()
	if err != nil {
		return result
	}
	defer release()
	return &trackedClientOutbound{ClientOutboundInterceptorBase: interceptor.ClientOutboundInterceptorBase{Next: i.inner.InterceptClient(next)}, gate: i.gate}
}

// workerInterception preserves worker behavior only when the original client interceptor supplied it.
func (i *trackedClientInterceptor) workerInterception() *trackedWorkerInterceptor {
	inner, ok := i.inner.(interceptor.WorkerInterceptor)
	if !ok {
		inner = &interceptor.WorkerInterceptorBase{}
	}
	return &trackedWorkerInterceptor{inner: inner, gate: i.gate}
}

// InterceptActivity fences inherited worker factories through the binding owner.
func (i *trackedClientInterceptor) InterceptActivity(ctx context.Context, next interceptor.ActivityInboundInterceptor) interceptor.ActivityInboundInterceptor {
	return i.workerInterception().InterceptActivity(ctx, next)
}

// InterceptWorkflow preserves inherited replay-safe workflow interception.
func (i *trackedClientInterceptor) InterceptWorkflow(ctx workflow.Context, next interceptor.WorkflowInboundInterceptor) interceptor.WorkflowInboundInterceptor {
	return i.workerInterception().InterceptWorkflow(ctx, next)
}

// InterceptNexusOperation fences inherited service factory and callback lifetime.
func (i *trackedClientInterceptor) InterceptNexusOperation(ctx context.Context, next interceptor.NexusOperationInboundInterceptor) interceptor.NexusOperationInboundInterceptor {
	return i.workerInterception().InterceptNexusOperation(ctx, next)
}

// trackedClientOutbound joins custom outbound callback return before binding cleanup.
type trackedClientOutbound struct {
	interceptor.ClientOutboundInterceptorBase
	gate     *admission
	rejected bool
}

// clientAdapterCall preserves native results/errors while sanitizing only adapter panics.
func clientAdapterCall[T any](c *trackedClientOutbound, call func() (T, error)) (result T, err error) {
	if c.rejected {
		return result, errors.New("Temporal client interceptor initialization rejected")
	}
	release, err := c.gate.enter()
	if err != nil {
		return result, err
	}
	defer release()
	defer func() {
		if recover() != nil {
			var zero T
			result = zero
			err = errors.New("Temporal client interceptor failed")
		}
	}()
	return call()
}

// UpdateWorkflow preserves native SDK behavior while joining the custom outbound callback.
func (c *trackedClientOutbound) UpdateWorkflow(ctx context.Context,
	in *interceptor.ClientUpdateWorkflowInput) (client.WorkflowUpdateHandle, error) {
	return clientAdapterCall(c, func() (client.WorkflowUpdateHandle, error) { return c.Next.UpdateWorkflow(ctx, in) })
}

// UpdateWithStartWorkflow preserves native SDK behavior while joining the custom outbound callback.
func (c *trackedClientOutbound) UpdateWithStartWorkflow(ctx context.Context,
	in *interceptor.ClientUpdateWithStartWorkflowInput) (client.WorkflowUpdateHandle, error) {
	return clientAdapterCall(c, func() (client.WorkflowUpdateHandle, error) { return c.Next.UpdateWithStartWorkflow(ctx, in) })
}

// ExecuteWorkflow preserves native SDK behavior while joining the custom outbound callback.
func (c *trackedClientOutbound) ExecuteWorkflow(ctx context.Context,
	in *interceptor.ClientExecuteWorkflowInput) (client.WorkflowRun, error) {
	return clientAdapterCall(c, func() (client.WorkflowRun, error) { return c.Next.ExecuteWorkflow(ctx, in) })
}

// SignalWorkflow preserves native SDK behavior while joining the custom outbound callback.
func (c *trackedClientOutbound) SignalWorkflow(ctx context.Context, in *interceptor.ClientSignalWorkflowInput) error {
	_, err := clientAdapterCall(c, func() (struct{}, error) { return struct{}{}, c.Next.SignalWorkflow(ctx, in) })
	return err
}

// SignalWithStartWorkflow preserves native SDK behavior while joining the custom outbound callback.
func (c *trackedClientOutbound) SignalWithStartWorkflow(ctx context.Context,
	in *interceptor.ClientSignalWithStartWorkflowInput) (client.WorkflowRun, error) {
	return clientAdapterCall(c, func() (client.WorkflowRun, error) { return c.Next.SignalWithStartWorkflow(ctx, in) })
}

// CancelWorkflow preserves native SDK behavior while joining the custom outbound callback.
func (c *trackedClientOutbound) CancelWorkflow(ctx context.Context, in *interceptor.ClientCancelWorkflowInput) error {
	_, err := clientAdapterCall(c, func() (struct{}, error) { return struct{}{}, c.Next.CancelWorkflow(ctx, in) })
	return err
}

// TerminateWorkflow preserves native SDK behavior while joining the custom outbound callback.
func (c *trackedClientOutbound) TerminateWorkflow(ctx context.Context, in *interceptor.ClientTerminateWorkflowInput) error {
	_, err := clientAdapterCall(c, func() (struct{}, error) { return struct{}{}, c.Next.TerminateWorkflow(ctx, in) })
	return err
}

// QueryWorkflow preserves native SDK behavior while joining the custom outbound callback.
func (c *trackedClientOutbound) QueryWorkflow(ctx context.Context,
	in *interceptor.ClientQueryWorkflowInput) (converter.EncodedValue, error) {
	return clientAdapterCall(c, func() (converter.EncodedValue, error) { return c.Next.QueryWorkflow(ctx, in) })
}

// DescribeWorkflow preserves native SDK behavior while joining the custom outbound callback.
func (c *trackedClientOutbound) DescribeWorkflow(ctx context.Context,
	in *interceptor.ClientDescribeWorkflowInput) (*interceptor.ClientDescribeWorkflowOutput, error) {
	return clientAdapterCall(c, func() (*interceptor.ClientDescribeWorkflowOutput, error) { return c.Next.DescribeWorkflow(ctx, in) })
}

// CreateSchedule preserves native SDK behavior while joining the custom outbound callback.
func (c *trackedClientOutbound) CreateSchedule(ctx context.Context, in *interceptor.ScheduleClientCreateInput) (client.ScheduleHandle, error) {
	return clientAdapterCall(c, func() (client.ScheduleHandle, error) { return c.Next.CreateSchedule(ctx, in) })
}

// ExecuteActivity preserves native SDK behavior while joining the custom outbound callback.
func (c *trackedClientOutbound) ExecuteActivity(ctx context.Context,
	in *interceptor.ClientExecuteActivityInput) (client.ActivityHandle, error) {
	return clientAdapterCall(c, func() (client.ActivityHandle, error) { return c.Next.ExecuteActivity(ctx, in) })
}

// GetActivityHandle preserves native SDK behavior while joining the custom outbound callback.
func (c *trackedClientOutbound) GetActivityHandle(in *interceptor.ClientGetActivityHandleInput) client.ActivityHandle {
	value, _ := clientAdapterCall(c, func() (client.ActivityHandle, error) { return c.Next.GetActivityHandle(in), nil })
	return value
}

// CancelActivity preserves native SDK behavior while joining the custom outbound callback.
func (c *trackedClientOutbound) CancelActivity(ctx context.Context,
	in *interceptor.ClientCancelActivityInput) error {
	_, err := clientAdapterCall(c, func() (struct{}, error) { return struct{}{}, c.Next.CancelActivity(ctx, in) })
	return err
}

// TerminateActivity preserves native SDK behavior while joining the custom outbound callback.
func (c *trackedClientOutbound) TerminateActivity(ctx context.Context,
	in *interceptor.ClientTerminateActivityInput) error {
	_, err := clientAdapterCall(c, func() (struct{}, error) { return struct{}{}, c.Next.TerminateActivity(ctx, in) })
	return err
}

// DescribeActivity preserves native SDK behavior while joining the custom outbound callback.
func (c *trackedClientOutbound) DescribeActivity(ctx context.Context,
	in *interceptor.ClientDescribeActivityInput) (*interceptor.ClientDescribeActivityOutput, error) {
	return clientAdapterCall(c, func() (*interceptor.ClientDescribeActivityOutput, error) { return c.Next.DescribeActivity(ctx, in) })
}

// PollActivityResult preserves native SDK behavior while joining the custom outbound callback.
func (c *trackedClientOutbound) PollActivityResult(ctx context.Context,
	in *interceptor.ClientPollActivityResultInput) (*interceptor.ClientPollActivityResultOutput, error) {
	return clientAdapterCall(c, func() (*interceptor.ClientPollActivityResultOutput, error) { return c.Next.PollActivityResult(ctx, in) })
}

// ExecuteNexusOperation preserves native SDK behavior while joining the custom outbound callback.
func (c *trackedClientOutbound) ExecuteNexusOperation(ctx context.Context,
	in *interceptor.ClientExecuteNexusOperationInput) (client.NexusOperationHandle, error) {
	return clientAdapterCall(c, func() (client.NexusOperationHandle, error) { return c.Next.ExecuteNexusOperation(ctx, in) })
}

// GetNexusOperationHandle preserves native SDK behavior while joining the custom outbound callback.
func (c *trackedClientOutbound) GetNexusOperationHandle(in *interceptor.ClientGetNexusOperationHandleInput) client.NexusOperationHandle {
	value, _ := clientAdapterCall(c, func() (client.NexusOperationHandle, error) { return c.Next.GetNexusOperationHandle(in), nil })
	return value
}

// CancelNexusOperation preserves native SDK behavior while joining the custom outbound callback.
func (c *trackedClientOutbound) CancelNexusOperation(ctx context.Context,
	in *interceptor.ClientCancelNexusOperationInput) error {
	_, err := clientAdapterCall(c, func() (struct{}, error) { return struct{}{}, c.Next.CancelNexusOperation(ctx, in) })
	return err
}

// TerminateNexusOperation preserves native SDK behavior while joining the custom outbound callback.
func (c *trackedClientOutbound) TerminateNexusOperation(ctx context.Context,
	in *interceptor.ClientTerminateNexusOperationInput) error {
	_, err := clientAdapterCall(c, func() (struct{}, error) { return struct{}{}, c.Next.TerminateNexusOperation(ctx, in) })
	return err
}

// DescribeNexusOperation preserves native SDK behavior while joining the custom outbound callback.
func (c *trackedClientOutbound) DescribeNexusOperation(ctx context.Context,
	in *interceptor.ClientDescribeNexusOperationInput) (*interceptor.ClientDescribeNexusOperationOutput, error) {
	return clientAdapterCall(c, func() (*interceptor.ClientDescribeNexusOperationOutput, error) {
		return c.Next.DescribeNexusOperation(ctx, in)
	})
}

// PollNexusOperationResult preserves native SDK behavior while joining the custom outbound callback.
func (c *trackedClientOutbound) PollNexusOperationResult(ctx context.Context,
	in *interceptor.ClientPollNexusOperationResultInput) (*interceptor.ClientPollNexusOperationResultOutput, error) {
	return clientAdapterCall(c, func() (*interceptor.ClientPollNexusOperationResultOutput, error) {
		return c.Next.PollNexusOperationResult(ctx, in)
	})
}
