package temporalqueue

import (
	"context"
	"errors"
	"testing"

	"go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/interceptor"
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
	for _, mode := range []string{"valid", "nil", "ordinary", "count", "bytes", "total", "nil item", "metadata", "error", "result and error", "closed"} {
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
			}
			gate := newAdmission()
			if mode == "closed" {
				gate.close()
			}
			guard := (&resourceInterceptor{limits: limits, gate: gate}).InterceptActivity(t.Context(), fixture)
			result, err := guard.ExecuteActivity(t.Context(), &interceptor.ExecuteActivityInput{})
			switch mode {
			case "valid", "nil", "ordinary", "error", "result and error":
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
