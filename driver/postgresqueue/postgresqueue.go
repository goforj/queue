package postgresqueue

import (
	"database/sql"
	"time"

	"github.com/goforj/queue"
	"github.com/goforj/queue/driver/sqlqueuecore"
	"github.com/goforj/queue/queueconfig"
	_ "github.com/jackc/pgx/v5/stdlib"
)

type Config struct {
	queueconfig.DriverBaseConfig
	DB                       *sql.DB
	DSN                      string
	ProcessingRecoveryGrace  time.Duration
	ProcessingLeaseNoTimeout time.Duration
}

// New creates a high-level Queue using the Postgres SQL backend.
// @group Constructors
//
// Example: postgres shorthand constructor
//
//	q, err := postgresqueue.New(
//		"postgres://user:pass@127.0.0.1:5432/queue?sslmode=disable",
//		queue.WithWorkers(4), // optional; default: 1 worker
//	)
//	if err != nil {
//		return
//	}
//	_ = q
func New(dsn string, opts ...queue.Option) (*queue.Queue, error) {
	return NewWithConfig(Config{DSN: dsn}, opts...)
}

// NewWithConfig creates a high-level Queue using an explicit Postgres SQL driver config.
// @group Constructors
//
// Example: postgres config constructor
//
//	q, err := postgresqueue.NewWithConfig(
//		postgresqueue.Config{
//			DriverBaseConfig: queueconfig.DriverBaseConfig{
//				DefaultQueue: "critical", // default if empty: "default"
//				Observer:     nil,        // default: nil
//			},
//			DB: nil, // optional; provide *sql.DB instead of DSN
//			DSN: "postgres://user:pass@127.0.0.1:5432/queue?sslmode=disable", // optional if DB is set
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
	return sqlqueuecore.NewQueue("pgx", sqlqueuecore.ModuleConfig{
		DriverBaseConfig:         cfg.DriverBaseConfig,
		DB:                       cfg.DB,
		DSN:                      cfg.DSN,
		ProcessingRecoveryGrace:  cfg.ProcessingRecoveryGrace,
		ProcessingLeaseNoTimeout: cfg.ProcessingLeaseNoTimeout,
	}, opts...)
}
