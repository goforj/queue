package temporalqueue

import (
	"github.com/goforj/queue/busruntime"
	"math"
	"testing"
	"time"
)

// TestResolvePolicyPreservesExplicitBoundedDefaults exercises rounding and SDK overflow boundaries.
func TestResolvePolicyPreservesExplicitBoundedDefaults(t *testing.T) {
	cases := []struct {
		name                    string
		timeout, delay, backoff time.Duration
		retry                   int
		want                    activityPolicy
	}{
		{name: "unset", want: activityPolicy{Timeout: 3 * time.Second, Backoff: time.Second, MaximumAttempts: 1}},
		{name: "subsecond", timeout: time.Nanosecond, delay: time.Nanosecond, backoff: time.Nanosecond, retry: 2, want: activityPolicy{Timeout: time.Second, Delay: time.Second, Backoff: time.Second, MaximumAttempts: 3}},
		{name: "round up", timeout: 1500 * time.Millisecond, delay: 2500 * time.Millisecond, backoff: 3500 * time.Millisecond, retry: 1, want: activityPolicy{Timeout: 2 * time.Second, Delay: 3 * time.Second, Backoff: 4 * time.Second, MaximumAttempts: 2}},
		{name: "maximum attempt field", retry: math.MaxInt32 - 1, want: activityPolicy{Timeout: 3 * time.Second, Backoff: time.Second, MaximumAttempts: math.MaxInt32}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			actual, err := resolvePolicy(tc.timeout, tc.delay, tc.backoff, tc.retry, 2500*time.Millisecond)
			if err != nil || actual != tc.want {
				t.Fatalf("resolved %+v %v, want %+v", actual, err, tc.want)
			}
		})
	}
	for name, tc := range map[string]struct {
		timeout, delay, backoff, defaultTimeout time.Duration
		retry                                   int
	}{
		"missing default": {}, "short default": {defaultTimeout: time.Millisecond}, "negative timeout": {timeout: -1, defaultTimeout: time.Second}, "negative delay": {delay: -1, defaultTimeout: time.Second}, "negative backoff": {backoff: -1, defaultTimeout: time.Second}, "negative retry": {retry: -1, defaultTimeout: time.Second}, "retry overflow": {retry: math.MaxInt32, defaultTimeout: time.Second}, "rounding overflow": {timeout: math.MaxInt64, defaultTimeout: time.Second},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := resolvePolicy(tc.timeout, tc.delay, tc.backoff, tc.retry, tc.defaultTimeout); err == nil {
				t.Fatal("invalid policy accepted")
			}
		})
	}
}

// TestDeliveryRejectsLossyWireIdentifiers prevents serialization from changing a validated logical identity.
func TestDeliveryRejectsLossyWireIdentifiers(t *testing.T) {
	for _, scenario := range []string{"type", "workflow queue", "activity queue", "dispatch", "job", "chain", "batch", "queue"} {
		t.Run(scenario, func(t *testing.T) {
			d := delivery{Version: 1, Type: "job", WorkflowQueue: "route", ActivityQueue: "route", Metadata: busruntime.DeliveryMetadata{SchemaVersion: 1, DispatchID: "dispatch", JobID: "job", Queue: "route"}}
			if err := d.resolve(time.Second); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "type":
				d.Type = "job\xff"
			case "workflow queue":
				d.WorkflowQueue = "route\xff"
			case "activity queue":
				d.ActivityQueue = "route\xff"
			case "dispatch":
				d.Metadata.DispatchID = "dispatch\xff"
			case "job":
				d.Metadata.JobID = "job\xff"
			case "chain":
				d.Metadata.ChainID = "chain\xff"
			case "batch":
				d.Metadata.BatchID = "batch\xff"
			case "queue":
				d.Metadata.Queue = "queue\xff"
			}
			if err := d.validate(); err == nil {
				t.Fatal("lossy identifier accepted")
			}
		})
	}
}
