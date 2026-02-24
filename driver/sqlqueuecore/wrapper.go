package sqlqueuecore

import (
	"database/sql"
	"time"

	"github.com/goforj/queue"
	"github.com/goforj/queue/queueconfig"
)

// ModuleConfig contains shared configuration fields used by the SQL dialect
// driver wrapper modules (mysqlqueue, postgresqueue, sqlitequeue).
type ModuleConfig struct {
	queueconfig.DriverBaseConfig
	DB                       *sql.DB
	DSN                      string
	ProcessingRecoveryGrace  time.Duration
	ProcessingLeaseNoTimeout time.Duration
}

// NewRuntime wraps the shared SQL queue implementation into a queue.QueueRuntime
// for a specific SQL driver name (for example, "mysql", "pgx", "sqlite").
func NewRuntime(driverName string, cfg ModuleConfig) (queue.QueueRuntime, error) {
	backend, err := New(queue.DatabaseConfig{
		DB:                       cfg.DB,
		DriverName:               driverName,
		DSN:                      cfg.DSN,
		DefaultQueue:             cfg.DefaultQueue,
		ProcessingRecoveryGrace:  cfg.ProcessingRecoveryGrace,
		ProcessingLeaseNoTimeout: cfg.ProcessingLeaseNoTimeout,
		Observer:                 cfg.Observer,
	})
	if err != nil {
		return nil, err
	}
	rootCfg := queue.Config{
		Driver:       queue.DriverDatabase,
		DefaultQueue: cfg.DefaultQueue,
		Observer:     cfg.Observer,
	}
	return queue.NewQueueFromDriver(rootCfg, backend, nil)
}

// NewQueue creates a high-level queue.Queue from the shared SQL implementation
// for a specific SQL driver name.
func NewQueue(driverName string, cfg ModuleConfig, opts ...queue.Option) (*queue.Queue, error) {
	raw, err := NewRuntime(driverName, cfg)
	if err != nil {
		return nil, err
	}
	return queue.NewFromRuntime(raw, opts...)
}
