package queue

import "testing"

func TestTaskBind_Success(t *testing.T) {
	type Meta struct {
		Active bool `json:"active"`
	}
	type Payload struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
		Meta Meta   `json:"meta"`
	}

	task := NewTask("job:bind").Payload(Payload{
		ID:   7,
		Name: "alice",
		Meta: Meta{Active: true},
	})

	var out Payload
	if err := task.Bind(&out); err != nil {
		t.Fatalf("bind failed: %v", err)
	}
	if out.ID != 7 || out.Name != "alice" || !out.Meta.Active {
		t.Fatalf("unexpected bind output: %+v", out)
	}
}

func TestTaskBind_RequiresPointerDestination(t *testing.T) {
	task := NewTask("job:bind").Payload(map[string]any{"id": 1})

	if err := task.Bind(nil); err == nil {
		t.Fatal("expected nil destination error")
	}

	var out map[string]any
	if err := task.Bind(out); err == nil {
		t.Fatal("expected non-pointer destination error")
	}

	var ptr *map[string]any
	if err := task.Bind(ptr); err == nil {
		t.Fatal("expected nil pointer destination error")
	}
}

func TestTaskBind_InvalidJSON(t *testing.T) {
	task := NewTask("job:bind").Payload("not-json")
	var out map[string]any
	if err := task.Bind(&out); err == nil {
		t.Fatal("expected bind error for invalid json payload")
	}
}
