package sqlitequeue

import (
	"database/sql"
	"time"

	"github.com/goforj/queue"
	"github.com/goforj/queue/driver/sqlqueuecore"
	_ "modernc.org/sqlite"
)

type Config struct {
	DB                       *sql.DB
	DSN                      string
	DefaultQueue             string
	Observer                 queue.Observer
	ProcessingRecoveryGrace  time.Duration
	ProcessingLeaseNoTimeout time.Duration
}

func New(dsn string) (*queue.Queue, error) {
	return NewWithConfig(Config{DSN: dsn})
}

func NewWithConfig(cfg Config, opts ...queue.Option) (*queue.Queue, error) {
	raw, err := NewQueue(cfg)
	if err != nil {
		return nil, err
	}
	return queue.NewFromRuntime(raw, opts...)
}

func NewQueue(cfg Config) (queue.QueueRuntime, error) {
	rootCfg := queue.Config{
		Driver:                           queue.DriverDatabase,
		Database:                         cfg.DB,
		DatabaseDriver:                   "sqlite",
		DatabaseDSN:                      cfg.DSN,
		DefaultQueue:                     cfg.DefaultQueue,
		Observer:                         cfg.Observer,
		DatabaseProcessingRecoveryGrace:  cfg.ProcessingRecoveryGrace,
		DatabaseProcessingLeaseNoTimeout: cfg.ProcessingLeaseNoTimeout,
	}
	backend, err := sqlqueuecore.New(queue.DatabaseConfig{
		DB:                       rootCfg.Database,
		DriverName:               rootCfg.DatabaseDriver,
		DSN:                      rootCfg.DatabaseDSN,
		DefaultQueue:             rootCfg.DefaultQueue,
		ProcessingRecoveryGrace:  rootCfg.DatabaseProcessingRecoveryGrace,
		ProcessingLeaseNoTimeout: rootCfg.DatabaseProcessingLeaseNoTimeout,
		Observer:                 rootCfg.Observer,
	})
	if err != nil {
		return nil, err
	}
	return queue.NewQueueFromDriver(rootCfg, backend, nil)
}
