package queue

import "testing"

// TestDriverUniqueKeyGoldenVector pins persisted identity bytes across rolling producer versions.
func TestDriverUniqueKeyGoldenVector(t *testing.T) {
	const want = "v1:b260f44c0b067a6a4b13214544d81291c3d58e43350c2e196bc4d7af39c11f5b"
	direct := NewJob("reports:build").Payload([]byte(`{"id":1}`))
	if got := DriverUniqueKey(direct, "critical"); got != want {
		t.Fatalf("direct unique key = %q, want golden %q", got, want)
	}
	envelope := NewJob("bus:job").Payload([]byte(`{"schema_version":1,"dispatch_id":"volatile","job_id":"volatile","job":{"type":"reports:build","payload":"eyJpZCI6MX0="}}`))
	if got := DriverUniqueKey(envelope, "critical"); got != want {
		t.Fatalf("workflow unique key = %q, want golden %q", got, want)
	}
}

// TestDriverUniqueKeyEmptyPayloadGoldenVector pins the compatibility normalization required when direct jobs replace workflow envelopes.
func TestDriverUniqueKeyEmptyPayloadGoldenVector(t *testing.T) {
	const want = "v1:c61e4fa70176e7ae023e4aae041317dbc8c8503b1ab07f9062cbfbcae1c328c7"
	jobs := []Job{
		NewJob("reports:empty"),
		NewJob("reports:empty").Payload([]byte{}),
		NewJob("reports:empty").PayloadJSON(nil),
		NewJob("reports:empty").Payload([]byte("null")),
		NewJob("bus:job").Payload([]byte(`{"schema_version":1,"job":{"type":"reports:empty","payload":"bnVsbA=="}}`)),
		NewJob("bus:job").Payload([]byte(`{"schema_version":1,"job":{"type":"reports:empty","payload":""}}`)),
	}
	for i, job := range jobs {
		if got := DriverUniqueKey(job, "critical"); got != want {
			t.Fatalf("empty payload variant %d unique key = %q, want golden %q", i, got, want)
		}
	}
}

// TestDriverUniqueKeyExcludesWorkflowCorrelationAndPolicy verifies only logical job bytes and queue define identity.
func TestDriverUniqueKeyExcludesWorkflowCorrelationAndPolicy(t *testing.T) {
	first := NewJob("bus:job").Payload([]byte(`{"schema_version":1,"dispatch_id":"dsp_1","job_id":"job_1","attempt":0,"job":{"type":"reports:build","payload":"eyJpZCI6MX0=","options":{"retry":1}}}`))
	second := NewJob("bus:job").Payload([]byte(`{"schema_version":1,"dispatch_id":"dsp_2","job_id":"job_2","attempt":7,"job":{"type":"reports:build","payload":"eyJpZCI6MX0=","options":{"retry":9}}}`))
	firstKey := DriverUniqueKey(first, "critical")
	secondKey := DriverUniqueKey(second, "critical")
	if firstKey == "" || firstKey != secondKey {
		t.Fatalf("volatile workflow fields changed identity: %q != %q", firstKey, secondKey)
	}
	if firstKey == DriverUniqueKey(second, "default") {
		t.Fatal("queue scope did not change identity")
	}
}

// TestDriverUniqueKeyFramesArbitraryBytes verifies delimiter-like values cannot alias each other.
func TestDriverUniqueKeyFramesArbitraryBytes(t *testing.T) {
	first := DriverUniqueKey(NewJob("a").Payload([]byte("b:c")), "default")
	second := DriverUniqueKey(NewJob("a:b").Payload([]byte("c")), "default")
	if first == second {
		t.Fatalf("length framing collided: %q", first)
	}
	if first != DriverUniqueKey(NewJob("a").Payload([]byte("b:c")), "default") {
		t.Fatal("equal logical jobs produced unstable identities")
	}
}

// TestDriverUniqueKeyPrivateIdentityMatchesEnvelope verifies root dispatch can carry identity without exporting mutable driver options.
func TestDriverUniqueKeyPrivateIdentityMatchesEnvelope(t *testing.T) {
	envelope := NewJob("bus:job").Payload([]byte(`{"schema_version":1,"dispatch_id":"dsp_1","job":{"type":"reports:build","payload":"eyJpZCI6MX0="}}`))
	physical := NewJob("bus:job").Payload([]byte("opaque")).withLogicalIdentity("reports:build", []byte(`{"id":1}`))
	if DriverUniqueKey(envelope, "critical") != DriverUniqueKey(physical, "critical") {
		t.Fatal("private identity and decoded envelope disagreed")
	}
}
