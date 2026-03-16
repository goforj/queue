package queue

import (
	"database/sql"
	"time"
)

const (
	defaultProcessingRecoveryGrace  = 2 * time.Second
	defaultProcessingLeaseNoTimeout = 5 * time.Minute
)

// DatabaseConfig configures the SQL-backed database q.
// @group Config
type DatabaseConfig struct {
	DB                       *sql.DB
	DriverName               string
	DSN                      string
	Workers                  int
	PollInterval             time.Duration
	DefaultQueue             string
	AutoMigrate              bool
	ProcessingRecoveryGrace  time.Duration
	ProcessingLeaseNoTimeout time.Duration
	Observer                 Observer
	Logger                   Logger
}

func (c DatabaseConfig) normalize() DatabaseConfig {
	c.Workers = defaultWorkerCount(c.Workers)
	if c.PollInterval <= 0 {
		c.PollInterval = 50 * time.Millisecond
	}
	if c.DefaultQueue == "" {
		c.DefaultQueue = "default"
	}
	if !c.AutoMigrate {
		c.AutoMigrate = true
	}
	if c.ProcessingRecoveryGrace <= 0 {
		c.ProcessingRecoveryGrace = defaultProcessingRecoveryGrace
	}
	if c.ProcessingLeaseNoTimeout <= 0 {
		c.ProcessingLeaseNoTimeout = defaultProcessingLeaseNoTimeout
	}
	return c
}
