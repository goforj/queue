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

// New creates a high-level Queue using the MySQL SQL backend.
// @group Constructors
//
// Example: mysql shorthand constructor
//
//	q, err := mysqlqueue.New(
//		"user:pass@tcp(127.0.0.1:3306)/queue?parseTime=true",
//		queue.WithWorkers(4), // optional; default: 1 worker
//	)
//	if err != nil {
//		return
//	}
//	_ = q
func New(dsn string, opts ...queue.Option) (*queue.Queue, error) {
	return NewWithConfig(Config{DSN: dsn}, opts...)
}

// NewWithConfig creates a high-level Queue using an explicit MySQL SQL driver config.
// @group Constructors
//
// Example: mysql config constructor
//
//	q, err := mysqlqueue.NewWithConfig(
//		mysqlqueue.Config{
//			DriverBaseConfig: queueconfig.DriverBaseConfig{
//				DefaultQueue: "critical", // default if empty: "default"
//				Observer:     nil,        // default: nil
//			},
//			DB: nil, // optional; provide *sql.DB instead of DSN
//			DSN: "user:pass@tcp(127.0.0.1:3306)/queue?parseTime=true", // optional if DB is set
//			ProcessingRecoveryGrace:  2 * time.Second, // default if <=0: 2s
//			ProcessingLeaseNoTimeout: 5 * time.Minute, // default if <=0: 5m
//		},
//		queue.WithWorkers(4), // optional; default: 1 worker
//	)
//	if err != nil {
//		return
//	}
//	_ = q
func NewWithConfig(cfg Config, opts ...queue.Option) (*queue.Queue, error) {
	return sqlqueuecore.NewQueue("mysql", sqlqueuecore.ModuleConfig{
		DriverBaseConfig:         cfg.DriverBaseConfig,
		DB:                       cfg.DB,
		DSN:                      cfg.DSN,
		ProcessingRecoveryGrace:  cfg.ProcessingRecoveryGrace,
		ProcessingLeaseNoTimeout: cfg.ProcessingLeaseNoTimeout,
	}, opts...)
}
