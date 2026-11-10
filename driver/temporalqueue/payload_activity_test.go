package temporalqueue

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/worker"
)

// payloadActivityFixture controls native payload-level execution without involving typed converters.
type payloadActivityFixture struct {
	call func(context.Context, *common.Payloads) (*common.Payloads, error)
}

// Execute records actual user callback admission through the supplied fixture.
func (f *payloadActivityFixture) Execute(ctx context.Context, input *common.Payloads) (*common.Payloads, error) {
	return f.call(ctx, input)
}

// ActivityType supplies the full SDK interface rather than merely a struct method named Execute.
func (*payloadActivityFixture) ActivityType() activity.Type { return activity.Type{Name: "payload"} }

// GetFunction supplies SDK metadata for the payload-level execution function.
func (f *payloadActivityFixture) GetFunction() interface{} { return f.Execute }

// TestPayloadActivityAdmissionAndBounds covers the SDK path that bypasses typed inbound interception.
func TestPayloadActivityAdmissionAndBounds(t *testing.T) {
	limits := SerializationLimits{MaxPayloads: 1, MaxPayloadBytes: 1024, MaxTotalBytes: 2048}
	for _, mode := range []string{"valid", "nil", "input count", "input bytes", "output count", "output bytes", "error", "closed"} {
		t.Run(mode, func(t *testing.T) {
			gate := newAdmission()
			policy, err := newGuardedConverter(nil, limits, newAdmission())
			if err != nil {
				t.Fatal(err)
			}
			input := &common.Payloads{Payloads: []*common.Payload{{Data: []byte("input")}}}
			output := &common.Payloads{Payloads: []*common.Payload{{Data: []byte("output")}}}
			if mode == "nil" {
				input, output = nil, nil
			}
			if mode == "input count" {
				input.Payloads = append(input.Payloads, &common.Payload{})
			}
			if mode == "input bytes" {
				input.Payloads[0].Data = make([]byte, 1025)
			}
			if mode == "output count" {
				output.Payloads = append(output.Payloads, &common.Payload{})
			}
			if mode == "output bytes" {
				output.Payloads[0].Data = make([]byte, 1025)
			}
			cause := errors.New("native application error")
			calls := 0
			fixture := &payloadActivityFixture{call: func(_ context.Context, received *common.Payloads) (*common.Payloads, error) {
				calls++
				if received != input {
					t.Fatal("raw native input changed")
				}
				if mode == "error" {
					return nil, cause
				}
				return output, nil
			}}
			guard := &admittedPayloadActivity{inner: fixture, kind: fixture.ActivityType(), function: fixture.GetFunction(), gate: gate, policy: policy}
			if mode == "closed" {
				gate.close()
			}
			result, err := guard.Execute(t.Context(), input)
			if mode == "valid" || mode == "nil" {
				if err != nil || result != output || guard.ActivityType().Name != "payload" || guard.GetFunction() == nil {
					t.Fatal("native payload result or metadata changed", result, err)
				}
			} else if err == nil {
				t.Fatal("rejected native payload execution succeeded")
			} else if mode == "error" && !errors.Is(err, cause) {
				t.Fatal("native application error taxonomy changed", err)
			}
			wantCalls := 1
			if mode == "input count" || mode == "input bytes" || mode == "closed" {
				wantCalls = 0
			}
			if calls != wantCalls {
				t.Fatal("callback crossed input or admission rejection", calls)
			}
		})
	}
}

// TestPayloadActivityDrainRetainsResistantCallback verifies successful cleanup cannot precede actual raw callback return.
func TestPayloadActivityDrainRetainsResistantCallback(t *testing.T) {
	gate := newAdmission()
	policy, err := newGuardedConverter(nil, DefaultSerializationLimits(), newAdmission())
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	fixture := &payloadActivityFixture{call: func(context.Context, *common.Payloads) (*common.Payloads, error) {
		close(entered)
		<-release
		return nil, nil
	}}
	guard := &admittedPayloadActivity{inner: fixture, gate: gate, policy: policy}
	done := make(chan error, 1)
	go func() { _, err := guard.Execute(t.Context(), nil); done <- err }()
	ctx, stop := context.WithTimeout(t.Context(), 5*time.Second)
	defer stop()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("raw callback never entered", ctx.Err())
	}
	gate.close()
	short, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	if err := gate.wait(short); !errors.Is(err, context.DeadlineExceeded) {
		close(release)
		t.Fatal("cleanup escaped active raw callback", err)
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("released raw callback did not return", ctx.Err())
	}
	if err := gate.wait(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// executeOnlyFixture must follow normal SDK struct-method registration rather than payload-level dispatch.
type executeOnlyFixture struct{}

// Execute supplies a valid struct activity method with an ordinary prefix-derived SDK name.
func (*executeOnlyFixture) Execute(_ context.Context, input *common.Payloads) (*common.Payloads, error) {
	return input, nil
}

// TestExecuteOnlyStructUsesActualSDKCatalog rejects duplicates even when SDK duplicate checks are disabled.
func TestExecuteOnlyStructUsesActualSDKCatalog(t *testing.T) {
	for _, duplicate := range []bool{false, true} {
		b := &backend{cfg: Config{Register: func(registry worker.Registry) error {
			if duplicate {
				registry.RegisterActivityWithOptions(registrationActivityFixture, activity.RegisterOptions{Name: "prefixExecute"})
			}
			registry.RegisterActivityWithOptions(&executeOnlyFixture{}, activity.RegisterOptions{Name: "prefix", DisableAlreadyRegisteredCheck: true})
			return nil
		}}}
		plan, err := b.applicationPlan()
		if duplicate {
			if err == nil || plan != nil {
				t.Fatal("SDK struct activity silently overwrote a catalog entry")
			}
		} else if err != nil || len(b.nativeNames) != 1 || b.nativeNames[0] != "activity:prefixExecute" {
			t.Fatal("Execute-only struct was misclassified", b.nativeNames, err)
		}
	}
}
