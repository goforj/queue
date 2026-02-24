package queue

import (
	"encoding/json"
	"reflect"
	"testing"
)

func FuzzJobBindMatchesJSONUnmarshal(f *testing.F) {
	seeds := [][]byte{
		nil,
		[]byte(""),
		[]byte("null"),
		[]byte("{}"),
		[]byte(`{"id":1,"name":"ok"}`),
		[]byte(`{"nested":{"ids":[1,2,3]}}`),
		[]byte(`[]`),
		[]byte(`{"id":`),
		[]byte(`not-json`),
	}
	for _, seed := range seeds {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, payload []byte) {
		job := NewJob("job:fuzz:bind").Payload(payload)

		var got map[string]any
		err := job.Bind(&got)

		var want map[string]any
		wantErr := json.Unmarshal(payload, &want)

		if (err == nil) != (wantErr == nil) {
			t.Fatalf("Bind/json.Unmarshal success mismatch: bind=%v json=%v payload=%q", err, wantErr, payload)
		}
		if err == nil && !reflect.DeepEqual(got, want) {
			t.Fatalf("Bind/json.Unmarshal result mismatch: got=%v want=%v payload=%q", got, want, payload)
		}
	})
}

func FuzzNormalizeQueueName(f *testing.F) {
	for _, seed := range []string{
		"",
		"default",
		"emails",
		"jobs:critical",
		" with-spaces ",
		"UPPER",
		"日本語",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, name string) {
		got := NormalizeQueueName(name)
		if got == "" {
			t.Fatal("NormalizeQueueName returned empty string")
		}
		if name == "" && got != "default" {
			t.Fatalf("expected empty name to normalize to default, got %q", got)
		}
		if name != "" && got != name {
			t.Fatalf("expected non-empty name to remain unchanged, got %q want %q", got, name)
		}
	})
}
