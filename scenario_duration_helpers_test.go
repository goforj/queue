package queue

import (
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func reportScenarioDuration(t *testing.T, backend string, scenario string, elapsed time.Duration) {
	t.Helper()
	t.Logf("[%s][%s] duration=%s", backend, scenario, elapsed.Round(time.Millisecond))
}

func requireScenarioDurationLTE(t *testing.T, backend string, scenario string, elapsed time.Duration, defaultLimit time.Duration) {
	t.Helper()
	limit := scenarioDurationLimitFromEnv(backend, scenario, defaultLimit)
	if elapsed <= limit {
		return
	}
	t.Fatalf("[%s][%s] duration %s exceeded limit %s", backend, scenario, elapsed.Round(time.Millisecond), limit)
}

func scenarioDurationLimitFromEnv(backend string, scenario string, defaultLimit time.Duration) time.Duration {
	keys := []string{
		"SCENARIO_DURATION_LIMIT_SECONDS_" + durationLimitEnvSuffix(scenario) + "_" + durationLimitEnvSuffix(backend),
		"SCENARIO_DURATION_LIMIT_SECONDS_" + durationLimitEnvSuffix(scenario),
		"SCENARIO_DURATION_LIMIT_SECONDS_" + durationLimitEnvSuffix(backend),
		"SCENARIO_DURATION_LIMIT_SECONDS",
	}
	for _, key := range keys {
		if limit, ok := durationLimitFromEnvKey(key); ok {
			return limit
		}
	}
	return defaultLimit
}

func durationLimitFromEnvKey(key string) (time.Duration, bool) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return 0, false
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds <= 0 {
		return 0, false
	}
	return time.Duration(seconds) * time.Second, true
}

func durationLimitEnvSuffix(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	prevUnderscore := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z':
			b.WriteByte(c - ('a' - 'A'))
			prevUnderscore = false
		case c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
			b.WriteByte(c)
			prevUnderscore = false
		default:
			if !prevUnderscore {
				b.WriteByte('_')
				prevUnderscore = true
			}
		}
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		return "UNKNOWN"
	}
	return out
}

func TestScenarioDurationLimitFromEnv_Priority(t *testing.T) {
	t.Setenv("SCENARIO_DURATION_LIMIT_SECONDS", "90")
	t.Setenv("SCENARIO_DURATION_LIMIT_SECONDS_SQS", "80")
	t.Setenv("SCENARIO_DURATION_LIMIT_SECONDS_SCENARIO_WORKER_RESTART_RECOVERY", "70")
	t.Setenv("SCENARIO_DURATION_LIMIT_SECONDS_SCENARIO_WORKER_RESTART_RECOVERY_SQS", "60")

	got := scenarioDurationLimitFromEnv("sqs", "scenario_worker_restart_recovery", 30*time.Second)
	if got != 60*time.Second {
		t.Fatalf("expected scenario+backend override, got %s", got)
	}

	got = scenarioDurationLimitFromEnv("rabbitmq", "scenario_worker_restart_recovery", 30*time.Second)
	if got != 70*time.Second {
		t.Fatalf("expected scenario override, got %s", got)
	}

	got = scenarioDurationLimitFromEnv("sqs", "scenario_shutdown_during_delay_retry", 30*time.Second)
	if got != 80*time.Second {
		t.Fatalf("expected backend override, got %s", got)
	}

	got = scenarioDurationLimitFromEnv("rabbitmq", "scenario_shutdown_during_delay_retry", 30*time.Second)
	if got != 90*time.Second {
		t.Fatalf("expected global override, got %s", got)
	}
}

func TestScenarioDurationLimitFromEnv_NormalizesAndIgnoresInvalid(t *testing.T) {
	t.Setenv("SCENARIO_DURATION_LIMIT_SECONDS_SCENARIO_SHUTDOWN_DURING_DELAY_RETRY_SQS", "not-a-number")
	t.Setenv("SCENARIO_DURATION_LIMIT_SECONDS_SQS", "0")
	t.Setenv("SCENARIO_DURATION_LIMIT_SECONDS", "15")

	got := scenarioDurationLimitFromEnv("sqs", "scenario.shutdown-during delay/retry", 30*time.Second)
	if got != 15*time.Second {
		t.Fatalf("expected invalid overrides to be ignored and global fallback used, got %s", got)
	}

	if suffix := durationLimitEnvSuffix("scenario.shutdown-during delay/retry"); suffix != "SCENARIO_SHUTDOWN_DURING_DELAY_RETRY" {
		t.Fatalf("unexpected normalized suffix: %q", suffix)
	}
	if suffix := durationLimitEnvSuffix("   "); suffix != "UNKNOWN" {
		t.Fatalf("expected UNKNOWN suffix, got %q", suffix)
	}
}
