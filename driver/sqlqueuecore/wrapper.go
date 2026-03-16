package sqlqueuecore

import (
	"database/sql"
	"time"

	"github.com/goforj/queue"
	"github.com/goforj/queue/internal/driverbridge"
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

// NewQueue creates a high-level queue.Queue from the shared SQL implementation
// for a specific SQL driver name.
func NewQueue(driverName string, cfg ModuleConfig, opts ...queue.Option) (*queue.Queue, error) {
	backend, err := New(queue.DatabaseConfig{
		DB:                       cfg.DB,
		DriverName:               driverName,
		DSN:                      cfg.DSN,
		DefaultQueue:             cfg.DefaultQueue,
		ProcessingRecoveryGrace:  cfg.ProcessingRecoveryGrace,
		ProcessingLeaseNoTimeout: cfg.ProcessingLeaseNoTimeout,
		Observer:                 cfg.Observer,
		Logger:                   cfg.Logger,
	})
	if err != nil {
		return nil, err
	}
	rootCfg := queue.Config{
		Driver:       queue.DriverDatabase,
		DefaultQueue: cfg.DefaultQueue,
		Observer:     cfg.Observer,
		Logger:       cfg.Logger,
	}
	return driverbridge.NewQueueFromDriver(rootCfg, backend, nil, opts...)
}
