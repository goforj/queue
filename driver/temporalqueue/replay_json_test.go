package temporalqueue

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestReplayJSONPreflightCountsBeforeObjectDecoding verifies structural bounds independently of SDK allocation and proto validity.
func TestReplayJSONPreflightCountsBeforeObjectDecoding(t *testing.T) {
	for name, data := range map[string]string{
		"exact boundary":        `{"events":[{"id":1},{"id":2}]}`,
		"unknown nested fields": `{"metadata":{"events":[1,2,3],"value":null},"events":[{}]}`,
		"nested event values":   `{"events":[{"fields":[{"values":[true,false,null]}]}]}`,
		"empty":                 `{"events":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := preflightReplayJSON([]byte(data), 2); err != nil {
				t.Fatal("valid bounded capture rejected", err)
			}
		})
	}
	for name, data := range map[string]string{
		"not object": `[]`, "missing array": `{"events":{}}`, "incomplete key": `{"events"`, "invalid event": `{"events":[{"x":]}`, "incomplete event": `{"events":[{`, "incomplete array": `{"events":[{}`, "incomplete object": `{"events":[]`, "trailing data": `{"events":[]} true`, "unknown invalid value": `{"extra":{`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := preflightReplayJSON([]byte(data), 2); err == nil {
				t.Fatal("malformed capture accepted")
			}
		})
	}
	if err := preflightReplayJSON([]byte(`{"events":[{},{},"unterminated`), 2); err == nil || !strings.Contains(err.Error(), "event limit") {
		t.Fatal("event boundary did not reject before decoding next value", err)
	}
}

// TestReplayJSONPublicLimitAppliesBeforeFullAndPartialSDKParsing prevents an offline capture from bypassing bounds through a replay cutoff.
func TestReplayJSONPublicLimitAppliesBeforeFullAndPartialSDKParsing(t *testing.T) {
	q, err := NewWithConfig(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer q.Shutdown(t.Context())
	replayer, err := NewWorkflowReplayer(q)
	if err != nil {
		t.Fatal(err)
	}
	data := `{"events":[` + strings.Repeat(`{},`, maxReplayHistoryEvents) + `{}]}`
	path := filepath.Join(t.TempDir(), "over-event-limit.json")
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	for _, cutoff := range []int64{0, 1} {
		if err := replayer.ReplayPartialWorkflowHistoryFromJSONFile(nil, path, cutoff); err == nil || !strings.Contains(err.Error(), "event limit") {
			t.Fatal("oversized capture reached SDK parsing", cutoff, err)
		}
	}
	exact := `{"events":[` + strings.Repeat(`{},`, maxReplayHistoryEvents-1) + `{}]}`
	if err := preflightReplayJSON([]byte(exact), maxReplayHistoryEvents); err != nil {
		t.Fatal("exact public event boundary rejected", err)
	}
}
