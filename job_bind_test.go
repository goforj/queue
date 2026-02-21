package queue

import (
	"errors"
	"testing"
)

type goodMarshaler struct{}
type badMarshaler struct{}

func (goodMarshaler) MarshalJSON() ([]byte, error) { return []byte(`{"ok":true}`), nil }
func (badMarshaler) MarshalJSON() ([]byte, error)  { return nil, errors.New("boom") }

func TestJobBind_Success(t *testing.T) {
	type Meta struct {
		Active bool `json:"active"`
	}
	type Payload struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
		Meta Meta   `json:"meta"`
	}

	job := NewJob("job:bind").Payload(Payload{
		ID:   7,
		Name: "alice",
		Meta: Meta{Active: true},
	})

	var out Payload
	if err := job.Bind(&out); err != nil {
		t.Fatalf("bind failed: %v", err)
	}
	if out.ID != 7 || out.Name != "alice" || !out.Meta.Active {
		t.Fatalf("unexpected bind output: %+v", out)
	}
}

func TestJobBind_RequiresPointerDestination(t *testing.T) {
	job := NewJob("job:bind").Payload(map[string]any{"id": 1})

	if err := job.Bind(nil); err == nil {
		t.Fatal("expected nil destination error")
	}

	var out map[string]any
	if err := job.Bind(out); err == nil {
		t.Fatal("expected non-pointer destination error")
	}

	var ptr *map[string]any
	if err := job.Bind(ptr); err == nil {
		t.Fatal("expected nil pointer destination error")
	}
}

func TestJobBind_InvalidJSON(t *testing.T) {
	job := NewJob("job:bind").Payload("not-json")
	var out map[string]any
	if err := job.Bind(&out); err == nil {
		t.Fatal("expected bind error for invalid json payload")
	}
}

func TestJobPayloadJSON_UsesMarshalerAndCapturesError(t *testing.T) {
	job := NewJob("job:json").PayloadJSON(goodMarshaler{})
	var out map[string]bool
	if err := job.Bind(&out); err != nil {
		t.Fatalf("bind marshaled payload failed: %v", err)
	}
	if !out["ok"] {
		t.Fatalf("expected marshaled payload to include ok=true, got %#v", out)
	}

	errJob := NewJob("job:json").PayloadJSON(badMarshaler{})
	if err := errJob.validate(); err == nil {
		t.Fatal("expected payload marshaling error")
	}
}
