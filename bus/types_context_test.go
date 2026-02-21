package bus

import "testing"

func TestContextPayloadBytesReturnsCopy(t *testing.T) {
	c := Context{payload: []byte(`{"id":1}`)}

	got := c.PayloadBytes()
	if string(got) != `{"id":1}` {
		t.Fatalf("unexpected payload bytes: %q", string(got))
	}

	// Mutating returned bytes must not mutate internal payload.
	got[0] = 'X'
	if string(c.payload) != `{"id":1}` {
		t.Fatalf("expected original payload to remain unchanged, got %q", string(c.payload))
	}
}

func TestContextBind(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		c := Context{payload: []byte(`{"id":123}`)}
		var out struct {
			ID int `json:"id"`
		}

		if err := c.Bind(&out); err != nil {
			t.Fatalf("bind failed: %v", err)
		}
		if out.ID != 123 {
			t.Fatalf("expected id=123, got %d", out.ID)
		}
	})

	t.Run("invalid json", func(t *testing.T) {
		c := Context{payload: []byte(`{`)}
		var out map[string]any
		if err := c.Bind(&out); err == nil {
			t.Fatal("expected bind error for invalid json")
		}
	})
}

