//go:build integration

package all_test

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
