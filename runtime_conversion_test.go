package queue

import (
	"encoding/json"
	"testing"
	"time"
)

func TestToBusJob_MapsPayloadAndOptions(t *testing.T) {
	type payload struct {
		ID int `json:"id"`
	}

	in := NewJob("emails:send").
		Payload(payload{ID: 7}).
		OnQueue("critical").
		Delay(2 * time.Second).
		Timeout(3 * time.Second).
		Retry(4).
		Backoff(500 * time.Millisecond).
		UniqueFor(30 * time.Second)

	got, err := toBusJob(in)
	if err != nil {
		t.Fatalf("toBusJob error: %v", err)
	}

	if got.Type != "emails:send" {
		t.Fatalf("type=%q expected=%q", got.Type, "emails:send")
	}
	if got.Options.Queue != "critical" {
		t.Fatalf("queue=%q expected=%q", got.Options.Queue, "critical")
	}
	if got.Options.Delay != 2*time.Second {
		t.Fatalf("delay=%s expected=%s", got.Options.Delay, 2*time.Second)
	}
	if got.Options.Timeout != 3*time.Second {
		t.Fatalf("timeout=%s expected=%s", got.Options.Timeout, 3*time.Second)
	}
	if got.Options.Retry != 4 {
		t.Fatalf("retry=%d expected=4", got.Options.Retry)
	}
	if got.Options.Backoff != 500*time.Millisecond {
		t.Fatalf("backoff=%s expected=%s", got.Options.Backoff, 500*time.Millisecond)
	}
	if got.Options.UniqueFor != 30*time.Second {
		t.Fatalf("unique=%s expected=%s", got.Options.UniqueFor, 30*time.Second)
	}

	raw, ok := got.Payload.(json.RawMessage)
	if !ok {
		t.Fatalf("payload type=%T expected json.RawMessage", got.Payload)
	}
	var decoded payload
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if decoded.ID != 7 {
		t.Fatalf("payload id=%d expected=7", decoded.ID)
	}
}

func TestToBusJob_PreservesNilPayload(t *testing.T) {
	got, err := toBusJob(NewJob("job:nil"))
	if err != nil {
		t.Fatalf("toBusJob error: %v", err)
	}
	if got.Payload != nil {
		t.Fatalf("payload=%T expected nil", got.Payload)
	}
}

func TestToBusJob_ValidationError(t *testing.T) {
	if _, err := toBusJob(NewJob("bad").Retry(-1)); err == nil {
		t.Fatal("expected validation error")
	}
}
