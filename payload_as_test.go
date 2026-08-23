package queue

import (
	"testing"
)

// payloadAsValue is the shared payload shape for result-method tests and benchmarks.
type payloadAsValue struct {
	ID int `json:"id"`
}

// payloadAsSink prevents decoded benchmark values from being optimized away.
var payloadAsSink payloadAsValue

// TestPayloadAsMethods verifies jobs and delivered messages return equivalent typed payloads.
func TestPayloadAsMethods(t *testing.T) {
	job := NewJob("reports:build").Payload(payloadAsValue{ID: 27})
	jobAs := (Job).PayloadAs[payloadAsValue]
	fromJob, err := jobAs(job)
	if err != nil || fromJob.ID != 27 {
		t.Fatalf("Job.PayloadAs returned %+v, %v", fromJob, err)
	}

	message := NewMessage("reports:build", job.PayloadBytes())
	messageAs := message.PayloadAs[payloadAsValue]
	fromMessage, err := messageAs()
	if err != nil || fromMessage.ID != 27 {
		t.Fatalf("Message.PayloadAs returned %+v, %v", fromMessage, err)
	}
}

// TestPayloadAsErrors verifies result methods preserve each receiver's existing Bind errors.
func TestPayloadAsErrors(t *testing.T) {
	job := NewJob("reports:build").Payload([]byte("not-json"))
	var jobDestination payloadAsValue
	bindErr := job.Bind(&jobDestination)
	_, resultErr := job.PayloadAs[payloadAsValue]()
	if bindErr == nil || resultErr == nil || bindErr.Error() != resultErr.Error() {
		t.Fatalf("Job errors differ: Bind=%v PayloadAs=%v", bindErr, resultErr)
	}

	message := NewMessage("reports:build", []byte("not-json"))
	var messageDestination payloadAsValue
	bindErr = message.Bind(&messageDestination)
	_, resultErr = message.PayloadAs[payloadAsValue]()
	if bindErr == nil || resultErr == nil || bindErr.Error() != resultErr.Error() {
		t.Fatalf("Message errors differ: Bind=%v PayloadAs=%v", bindErr, resultErr)
	}
}

// BenchmarkPayloadDecode compares caller-owned binding and generic result methods.
func BenchmarkPayloadDecode(b *testing.B) {
	job := NewJob("reports:build").Payload(payloadAsValue{ID: 27})
	message := NewMessage("reports:build", job.PayloadBytes())

	b.Run("JobBind", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			var out payloadAsValue
			if err := job.Bind(&out); err != nil {
				b.Fatalf("Bind: %v", err)
			}
			payloadAsSink = out
		}
	})
	b.Run("JobPayloadAs", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			out, err := job.PayloadAs[payloadAsValue]()
			if err != nil {
				b.Fatalf("PayloadAs: %v", err)
			}
			payloadAsSink = out
		}
	})
	b.Run("MessageBind", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			var out payloadAsValue
			if err := message.Bind(&out); err != nil {
				b.Fatalf("Bind: %v", err)
			}
			payloadAsSink = out
		}
	})
	b.Run("MessagePayloadAs", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			out, err := message.PayloadAs[payloadAsValue]()
			if err != nil {
				b.Fatalf("PayloadAs: %v", err)
			}
			payloadAsSink = out
		}
	})
}
