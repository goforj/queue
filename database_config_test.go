package queue

import (
	"testing"
	"time"
)

// TestDatabaseConfigNormalizeDefaultsAndAutoMigrationOptOut verifies the compatibility default and its explicit override.
func TestDatabaseConfigNormalizeDefaultsAndAutoMigrationOptOut(t *testing.T) {
	defaults := (DatabaseConfig{}).normalize()
	if defaults.Workers <= 0 {
		t.Fatalf("default workers = %d, want a positive count", defaults.Workers)
	}
	if defaults.PollInterval != 50*time.Millisecond {
		t.Fatalf("default poll interval = %v, want 50ms", defaults.PollInterval)
	}
	if defaults.DefaultQueue != "default" {
		t.Fatalf("default queue = %q, want default", defaults.DefaultQueue)
	}
	if !defaults.AutoMigrate {
		t.Fatal("AutoMigrate defaulted false, want compatibility default true")
	}
	if defaults.ProcessingRecoveryGrace != defaultProcessingRecoveryGrace {
		t.Fatalf("default recovery grace = %v, want %v", defaults.ProcessingRecoveryGrace, defaultProcessingRecoveryGrace)
	}
	if defaults.ProcessingLeaseNoTimeout != defaultProcessingLeaseNoTimeout {
		t.Fatalf("default no-timeout lease = %v, want %v", defaults.ProcessingLeaseNoTimeout, defaultProcessingLeaseNoTimeout)
	}

	configured := (DatabaseConfig{
		Workers:                  3,
		PollInterval:             time.Second,
		DefaultQueue:             "critical",
		AutoMigrate:              true,
		DisableAutoMigrate:       true,
		ProcessingRecoveryGrace:  3 * time.Second,
		ProcessingLeaseNoTimeout: 7 * time.Minute,
	}).normalize()
	if configured.Workers != 3 || configured.PollInterval != time.Second || configured.DefaultQueue != "critical" {
		t.Fatalf("configured runtime values changed during normalization: %+v", configured)
	}
	if configured.AutoMigrate {
		t.Fatal("DisableAutoMigrate did not override the compatibility default")
	}
	if configured.ProcessingRecoveryGrace != 3*time.Second || configured.ProcessingLeaseNoTimeout != 7*time.Minute {
		t.Fatalf("configured recovery values changed during normalization: %+v", configured)
	}

	enabled := (DatabaseConfig{AutoMigrate: true}).normalize()
	if !enabled.AutoMigrate {
		t.Fatal("explicit AutoMigrate true was not preserved")
	}
}
