package queue

import (
	"bytes"
	"testing"
)

// TestResolveObservedJobType verifies the compatibility helper keeps raw and internal job names meaningful.
func TestResolveObservedJobType(t *testing.T) {
	t.Run("plain job type passes through", func(t *testing.T) {
		got := ResolveObservedJobType("monitoring:check", []byte(`{"anything":"ok"}`))
		if got != "monitoring:check" {
			t.Fatalf("expected original job type, got %q", got)
		}
	})

	t.Run("bus wrapper unwraps nested job type", func(t *testing.T) {
		got := ResolveObservedJobType("bus:job", []byte(`{"schema_version":1,"job":{"type":"monitoring:check"}}`))
		if got != "monitoring:check" {
			t.Fatalf("expected unwrapped job type, got %q", got)
		}
	})

	t.Run("other bus wrappers also unwrap", func(t *testing.T) {
		got := ResolveObservedJobType("bus:batch:job", []byte(`{"schema_version":1,"job":{"type":"reports:build"}}`))
		if got != "reports:build" {
			t.Fatalf("expected unwrapped batch job type, got %q", got)
		}
	})

	t.Run("invalid payload falls back to raw type", func(t *testing.T) {
		got := ResolveObservedJobType("bus:job", []byte(`{`))
		if got != "bus:job" {
			t.Fatalf("expected fallback raw job type, got %q", got)
		}
	})

	t.Run("missing envelope version falls back to raw type", func(t *testing.T) {
		got := ResolveObservedJobType("bus:job", []byte(`{"job":{"type":"monitoring:check"}}`))
		if got != "bus:job" {
			t.Fatalf("expected raw type for missing schema, got %q", got)
		}
	})

	t.Run("missing nested job type falls back to raw type", func(t *testing.T) {
		got := ResolveObservedJobType("bus:job", []byte(`{"schema_version":1,"job":{}}`))
		if got != "bus:job" {
			t.Fatalf("expected fallback raw job type, got %q", got)
		}
	})

	t.Run("unknown internal-looking job type cannot supply metadata", func(t *testing.T) {
		got := ResolveObservedJobType("bus:tenant", []byte(`{"schema_version":1,"job":{"type":"monitoring:check"}}`))
		if got != "bus:tenant" {
			t.Fatalf("expected raw unknown type, got %q", got)
		}
	})

	t.Run("unknown envelope version cannot supply metadata", func(t *testing.T) {
		got := ResolveObservedJobType("bus:job", []byte(`{"schema_version":2,"job":{"type":"monitoring:check"}}`))
		if got != "bus:job" {
			t.Fatalf("expected raw type for unknown schema, got %q", got)
		}
	})

	t.Run("application payload resembling an envelope is not decoded", func(t *testing.T) {
		got := ResolveObservedJobType("monitoring:check", []byte(`{"schema_version":1,"job":{"type":"spoofed"}}`))
		if got != "monitoring:check" {
			t.Fatalf("expected application type, got %q", got)
		}
	})
}

// TestResolveObservedJobMetadata verifies every observable layer can join one internal delivery to its logical job.
func TestResolveObservedJobMetadata(t *testing.T) {
	payload := []byte(`{"schema_version":1,"dispatch_id":"dsp_1","job_id":"job_1","chain_id":"chn_1","batch_id":"bat_1","job":{"type":"monitoring:check","payload":"eyJpZCI6MX0="}}`)
	metadata := ResolveObservedJobMetadata("bus:job", payload)
	if metadata.JobType != "monitoring:check" {
		t.Fatalf("job type = %q, want monitoring:check", metadata.JobType)
	}
	if metadata.DispatchID != "dsp_1" || metadata.JobID != "job_1" || metadata.ChainID != "chn_1" || metadata.BatchID != "bat_1" {
		t.Fatalf("correlation metadata is incomplete: %+v", metadata)
	}
	wantKey := ResolveObservedJobMetadata("monitoring:check", []byte(`{"id":1}`)).JobKey
	if metadata.JobKey != wantKey {
		t.Fatalf("logical job key = %q, want %q", metadata.JobKey, wantKey)
	}

	fallback := ResolveObservedJobMetadata("bus:job", []byte(`{`))
	if fallback.JobType != "bus:job" || fallback.DispatchID != "" || fallback.JobKey == "" {
		t.Fatalf("invalid envelope fallback = %+v", fallback)
	}

	unknownType := ResolveObservedJobMetadata("bus:tenant", payload)
	if unknownType.JobType != "bus:tenant" || unknownType.DispatchID != "" || unknownType.JobID != "" {
		t.Fatalf("unknown internal-looking type decoded metadata: %+v", unknownType)
	}

	unknownVersion := ResolveObservedJobMetadata("bus:job", []byte(`{"schema_version":2,"dispatch_id":"spoofed","job":{"type":"monitoring:check"}}`))
	if unknownVersion.JobType != "bus:job" || unknownVersion.DispatchID != "" || unknownVersion.JobID != "" {
		t.Fatalf("unknown schema decoded metadata: %+v", unknownVersion)
	}
}

// TestResolveObservedJobMetadataInternalTypes locks decoding to the four version-one workflow envelopes already on the wire.
func TestResolveObservedJobMetadataInternalTypes(t *testing.T) {
	tests := []struct {
		name      string
		jobType   string
		payload   string
		wantType  string
		wantChain string
		wantBatch string
	}{
		{
			name:     "direct job",
			jobType:  "bus:job",
			payload:  `{"schema_version":1,"dispatch_id":"dsp_direct","job_id":"job_direct","job":{"type":"reports:build","payload":"e30="}}`,
			wantType: "reports:build",
		},
		{
			name:      "chain node",
			jobType:   "bus:chain:node",
			payload:   `{"schema_version":1,"dispatch_id":"dsp_chain","job_id":"job_chain","chain_id":"chn_1","job":{"type":"reports:build","payload":"e30="}}`,
			wantType:  "reports:build",
			wantChain: "chn_1",
		},
		{
			name:      "batch job",
			jobType:   "bus:batch:job",
			payload:   `{"schema_version":1,"dispatch_id":"dsp_batch","job_id":"job_batch","batch_id":"bat_1","job":{"type":"reports:build","payload":"e30="}}`,
			wantType:  "reports:build",
			wantBatch: "bat_1",
		},
		{
			name:      "callback without application job",
			jobType:   "bus:callback",
			payload:   `{"schema_version":1,"dispatch_id":"dsp_callback","job_id":"job_callback","batch_id":"bat_1","job":{}}`,
			wantType:  "bus:callback",
			wantBatch: "bat_1",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			metadata := ResolveObservedJobMetadata(test.jobType, []byte(test.payload))
			if metadata.JobType != test.wantType || metadata.DispatchID == "" || metadata.JobID == "" {
				t.Fatalf("decoded metadata = %+v, want type %q and correlation IDs", metadata, test.wantType)
			}
			if metadata.ChainID != test.wantChain || metadata.BatchID != test.wantBatch {
				t.Fatalf("workflow correlation = chain:%q batch:%q, want chain:%q batch:%q", metadata.ChainID, metadata.BatchID, test.wantChain, test.wantBatch)
			}
		})
	}
}

// TestResolveObservedJobMetadataLogicalKey verifies random envelope IDs cannot fragment telemetry identity or mutate delivered bytes.
func TestResolveObservedJobMetadataLogicalKey(t *testing.T) {
	first := []byte(`{"schema_version":1,"dispatch_id":"dsp_1","job_id":"job_1","job":{"type":"reports:build","payload":"eyJpZCI6MX0="}}`)
	second := []byte(`{"schema_version":1,"dispatch_id":"dsp_2","job_id":"job_2","job":{"type":"reports:build","payload":"eyJpZCI6MX0="}}`)
	original := append([]byte(nil), first...)

	firstMetadata := ResolveObservedJobMetadata("bus:job", first)
	secondMetadata := ResolveObservedJobMetadata("bus:job", second)
	if firstMetadata.JobKey == "" || firstMetadata.JobKey != secondMetadata.JobKey {
		t.Fatalf("logical keys differ across correlation IDs: %q != %q", firstMetadata.JobKey, secondMetadata.JobKey)
	}
	if !bytes.Equal(first, original) {
		t.Fatalf("decoder mutated payload: got %q, want %q", first, original)
	}
}
