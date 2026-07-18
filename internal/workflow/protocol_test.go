package workflow

import (
	"reflect"
	"testing"
)

// TestProtocolConstants pins the persisted schema and physical delivery namespace.
func TestProtocolConstants(t *testing.T) {
	if ProtocolSchemaVersion != 1 {
		t.Fatalf("protocol schema version = %d, want 1", ProtocolSchemaVersion)
	}
	types := []struct {
		name string
		got  string
		want string
	}{
		{name: "direct", got: DirectDeliveryType, want: "bus:job"},
		{name: "chain node", got: ChainNodeDeliveryType, want: "bus:chain:node"},
		{name: "batch job", got: BatchJobDeliveryType, want: "bus:batch:job"},
		{name: "callback", got: CallbackDeliveryType, want: "bus:callback"},
	}
	for _, test := range types {
		t.Run(test.name, func(t *testing.T) {
			if test.got != test.want {
				t.Fatalf("delivery type = %q, want %q", test.got, test.want)
			}
			if !IsDeliveryType(test.got) {
				t.Fatalf("IsDeliveryType(%q) = false, want true", test.got)
			}
		})
	}
	if IsDeliveryType("bus:tenant") {
		t.Fatal("IsDeliveryType accepted an unowned bus namespace")
	}
}

// TestResolveDeliveryMetadata decodes every version-one delivery shape without constructing production envelopes.
func TestResolveDeliveryMetadata(t *testing.T) {
	tests := []struct {
		name         string
		deliveryType string
		payload      []byte
		want         DeliveryMetadata
	}{
		{
			name:         "direct base64 payload",
			deliveryType: DirectDeliveryType,
			payload:      []byte(`{"schema_version":1,"dispatch_id":"dsp_direct","job_id":"job_direct","job":{"type":"reports:build","payload":"eyJpZCI6MX0="}}`),
			want: DeliveryMetadata{
				JobType:    "reports:build",
				Payload:    []byte(`{"id":1}`),
				DispatchID: "dsp_direct",
				JobID:      "job_direct",
			},
		},
		{
			name:         "chain null payload",
			deliveryType: ChainNodeDeliveryType,
			payload:      []byte(`{"schema_version":1,"dispatch_id":"dsp_chain","job_id":"job_chain","chain_id":"chn_1","job":{"type":"reports:chain","payload":null}}`),
			want: DeliveryMetadata{
				JobType:    "reports:chain",
				Payload:    nil,
				DispatchID: "dsp_chain",
				JobID:      "job_chain",
				ChainID:    "chn_1",
			},
		},
		{
			name:         "batch empty payload",
			deliveryType: BatchJobDeliveryType,
			payload:      []byte(`{"schema_version":1,"dispatch_id":"dsp_batch","job_id":"job_batch","batch_id":"bat_1","job":{"type":"reports:batch","payload":""}}`),
			want: DeliveryMetadata{
				JobType:    "reports:batch",
				Payload:    []byte{},
				DispatchID: "dsp_batch",
				JobID:      "job_batch",
				BatchID:    "bat_1",
			},
		},
		{
			name:         "callback omitted payload",
			deliveryType: CallbackDeliveryType,
			payload:      []byte(`{"schema_version":1,"dispatch_id":"dsp_callback","job_id":"job_callback","chain_id":"chn_2","job":{"type":"reports:callback"}}`),
			want: DeliveryMetadata{
				JobType:    "reports:callback",
				Payload:    nil,
				DispatchID: "dsp_callback",
				JobID:      "job_callback",
				ChainID:    "chn_2",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ResolveDeliveryMetadata(test.deliveryType, test.payload); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("ResolveDeliveryMetadata() = %#v, want %#v", got, test.want)
			}
		})
	}
}

// TestResolveDeliveryMetadataFallbacks pins physical identity whenever the protocol cannot safely unwrap a delivery.
func TestResolveDeliveryMetadataFallbacks(t *testing.T) {
	unknownSchema := []byte(`{"schema_version":2,"dispatch_id":"dsp_unknown","job":{"type":"reports:unknown","payload":"e30="}}`)
	malformed := []byte(`{"schema_version":1`)
	nonWorkflow := []byte(`{"schema_version":1,"dispatch_id":"dsp_spoofed","job":{"type":"reports:spoofed","payload":"e30="}}`)
	emptyJob := []byte(`{"schema_version":1,"dispatch_id":"dsp_callback","job_id":"job_callback","batch_id":"bat_2","job":{}}`)
	tests := []struct {
		name         string
		deliveryType string
		payload      []byte
		want         DeliveryMetadata
	}{
		{
			name:         "nil payload",
			deliveryType: DirectDeliveryType,
			want:         DeliveryMetadata{JobType: DirectDeliveryType},
		},
		{
			name:         "malformed json",
			deliveryType: DirectDeliveryType,
			payload:      malformed,
			want:         DeliveryMetadata{JobType: DirectDeliveryType, Payload: malformed},
		},
		{
			name:         "unknown schema",
			deliveryType: DirectDeliveryType,
			payload:      unknownSchema,
			want:         DeliveryMetadata{JobType: DirectDeliveryType, Payload: unknownSchema},
		},
		{
			name:         "non-workflow type",
			deliveryType: "application:job",
			payload:      nonWorkflow,
			want:         DeliveryMetadata{JobType: "application:job", Payload: nonWorkflow},
		},
		{
			name:         "valid callback without logical type",
			deliveryType: CallbackDeliveryType,
			payload:      emptyJob,
			want: DeliveryMetadata{
				JobType:    CallbackDeliveryType,
				Payload:    emptyJob,
				DispatchID: "dsp_callback",
				JobID:      "job_callback",
				BatchID:    "bat_2",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ResolveDeliveryMetadata(test.deliveryType, test.payload); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("ResolveDeliveryMetadata() = %#v, want %#v", got, test.want)
			}
		})
	}
}
