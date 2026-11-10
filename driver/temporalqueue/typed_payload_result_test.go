package temporalqueue

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"
)

// typedPayloadInboundFixture models either a typed handler result or an inner interceptor's replacement result.
type typedPayloadInboundFixture struct {
	interceptor.ActivityInboundInterceptorBase
	result any
	err    error
	calls  int
}

// ExecuteActivity exposes the exact result that the SDK will otherwise accept without conversion.
func (f *typedPayloadInboundFixture) ExecuteActivity(context.Context, *interceptor.ExecuteActivityInput) (any, error) {
	f.calls++
	return f.result, f.err
}

// TestTypedPayloadResultBounds checks every shared byte/count branch at the SDK's converter-bypass boundary.
func TestTypedPayloadResultBounds(t *testing.T) {
	limits := SerializationLimits{MaxPayloads: 2, MaxPayloadBytes: 1024, MaxTotalBytes: 1536}
	for _, mode := range []string{"valid", "nil", "ordinary", "count", "bytes", "total", "nil item", "metadata", "error", "result and error", "oversized and error", "pending", "closed"} {
		t.Run(mode, func(t *testing.T) {
			output := &common.Payloads{Payloads: []*common.Payload{{Data: make([]byte, 1021)}}}
			cause := errors.New("native application error")
			fixture := &typedPayloadInboundFixture{result: output}
			switch mode {
			case "nil":
				fixture.result = (*common.Payloads)(nil)
			case "ordinary":
				fixture.result = "native result"
			case "count":
				output.Payloads = append(output.Payloads, &common.Payload{}, &common.Payload{})
			case "bytes":
				output.Payloads[0].Data = make([]byte, 1022)
			case "total":
				output.Payloads = append(output.Payloads, &common.Payload{Data: make([]byte, 510)})
			case "nil item":
				output.Payloads = []*common.Payload{nil}
			case "metadata":
				output.Payloads[0] = &common.Payload{Metadata: map[string][]byte{"private": make([]byte, 1024)}}
			case "error":
				fixture.result = nil
				fixture.err = cause
			case "result and error":
				fixture.err = cause
			case "oversized and error", "pending":
				output.Payloads[0].Data = make([]byte, 1022)
				fixture.err = cause
				if mode == "pending" {
					fixture.err = activity.ErrResultPending
				}
			}
			gate := newAdmission()
			if mode == "closed" {
				gate.close()
			}
			guard := (&resourceInterceptor{limits: limits, gate: gate}).InterceptActivity(t.Context(), fixture)
			result, err := guard.ExecuteActivity(t.Context(), &interceptor.ExecuteActivityInput{})
			switch mode {
			case "valid", "nil", "ordinary", "error", "result and error", "oversized and error", "pending":
				if result != fixture.result || !errors.Is(err, fixture.err) {
					t.Fatal("native result/error semantics changed", result, err)
				}
			case "closed":
				if err == nil || fixture.calls != 0 {
					t.Fatal("closed callback admitted", fixture.calls, err)
				}
			default:
				requirePropagationCategory(t, err, false)
				if result != nil {
					t.Fatal("invalid raw result forwarded")
				}
			}
		})
	}
}

// TestTypedPayloadResultPreservesSDKFailureDisposition verifies discarded results cannot rewrite native retry or asynchronous completion policy.
func TestTypedPayloadResultPreservesSDKFailureDisposition(t *testing.T) {
	for _, pending := range []bool{false, true} {
		t.Run(fmt.Sprint(pending), func(t *testing.T) {
			var suite testsuite.WorkflowTestSuite
			env := suite.NewTestActivityEnvironment()
			dc, err := newGuardedConverter(nil, DefaultSerializationLimits(), newAdmission())
			if err != nil {
				t.Fatal(err)
			}
			fc, err := newGuardedFailureConverter(nil, dc, newAdmission())
			if err != nil {
				t.Fatal(err)
			}
			env.SetDataConverter(dc).SetFailureConverter(fc)
			env.SetWorkerOptions(worker.Options{Interceptors: []interceptor.WorkerInterceptor{&resourceInterceptor{limits: DefaultSerializationLimits(), gate: newAdmission()}}})
			handler := func(context.Context) (*common.Payloads, error) {
				result := &common.Payloads{Payloads: []*common.Payload{{Data: make([]byte, 300<<10)}}}
				if pending {
					return result, activity.ErrResultPending
				}
				return result, temporal.NewApplicationError("synthetic transient operation", "native-transient")
			}
			env.RegisterActivity(handler)
			_, err = env.ExecuteActivity(handler)
			if pending {
				if !errors.Is(err, activity.ErrResultPending) {
					t.Fatal("asynchronous activity disposition changed", err)
				}
			} else {
				var application *temporal.ApplicationError
				if !errors.As(err, &application) || application.Type() != "native-transient" || application.NonRetryable() {
					t.Fatal("native retry policy changed", err)
				}
			}
		})
	}
}
