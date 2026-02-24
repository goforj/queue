package mysqlqueue

import (
	"database/sql"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/goforj/queue"
	"github.com/goforj/queue/driver/sqlqueuecore"
	"github.com/goforj/queue/queueconfig"
)

type Config struct {
	queueconfig.DriverBaseConfig
	DB                       *sql.DB
	DSN                      string
	ProcessingRecoveryGrace  time.Duration
	ProcessingLeaseNoTimeout time.Duration
}

func New(dsn string) (*queue.Queue, error) {
	return NewWithConfig(Config{DSN: dsn})
}

func NewWithConfig(cfg Config, opts ...queue.Option) (*queue.Queue, error) {
	return sqlqueuecore.NewQueue("mysql", sqlqueuecore.ModuleConfig{
		DriverBaseConfig:         cfg.DriverBaseConfig,
		DB:                       cfg.DB,
		DSN:                      cfg.DSN,
		ProcessingRecoveryGrace:  cfg.ProcessingRecoveryGrace,
		ProcessingLeaseNoTimeout: cfg.ProcessingLeaseNoTimeout,
	}, opts...)
}

func NewRuntime(cfg Config) (queue.QueueRuntime, error) {
	return sqlqueuecore.NewRuntime("mysql", sqlqueuecore.ModuleConfig{
		DriverBaseConfig:         cfg.DriverBaseConfig,
		DB:                       cfg.DB,
		DSN:                      cfg.DSN,
		ProcessingRecoveryGrace:  cfg.ProcessingRecoveryGrace,
		ProcessingLeaseNoTimeout: cfg.ProcessingLeaseNoTimeout,
	})
}
