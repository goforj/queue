//go:build integration

package root_test

import (
	"database/sql"
	"time"

	"github.com/goforj/queue"
	"github.com/goforj/queue/driver/mysqlqueue"
	"github.com/goforj/queue/driver/natsqueue"
	"github.com/goforj/queue/driver/postgresqueue"
	"github.com/goforj/queue/driver/rabbitmqqueue"
	"github.com/goforj/queue/driver/redisqueue"
	"github.com/goforj/queue/driver/sqlitequeue"
	"github.com/goforj/queue/driver/sqsqueue"
	"github.com/goforj/queue/queueconfig"
	"github.com/goforj/queue/integration/testenv"
)

func nullCfg() queue.Config       { return queue.Config{Driver: queue.DriverNull} }
func syncCfg() queue.Config       { return queue.Config{Driver: queue.DriverSync} }
func workerpoolCfg() queue.Config { return queue.Config{Driver: queue.DriverWorkerpool} }

func redisCfg(addr string) redisqueue.Config {
	return redisqueue.Config{DriverBaseConfig: queueconfig.DriverBaseConfig{}, Addr: addr}
}
func natsCfg(url string) natsqueue.Config {
	return natsqueue.Config{DriverBaseConfig: queueconfig.DriverBaseConfig{}, URL: url}
}
func rabbitmqCfg(url string) rabbitmqqueue.Config {
	return rabbitmqqueue.Config{DriverBaseConfig: queueconfig.DriverBaseConfig{}, URL: url}
}
func sqsCfg(region, endpoint, accessKey, secretKey string) sqsqueue.Config {
	return sqsqueue.Config{
		DriverBaseConfig: queueconfig.DriverBaseConfig{},
		Region:           region,
		Endpoint:         endpoint,
		AccessKey:        accessKey,
		SecretKey:        secretKey,
	}
}

func mysqlCfg(dsn string) mysqlqueue.Config {
	return mysqlqueue.Config{DriverBaseConfig: queueconfig.DriverBaseConfig{}, DSN: dsn}
}
func postgresCfg(dsn string) postgresqueue.Config {
	return postgresqueue.Config{DriverBaseConfig: queueconfig.DriverBaseConfig{}, DSN: dsn}
}
func sqliteCfg(dsn string) sqlitequeue.Config {
	return sqlitequeue.Config{DriverBaseConfig: queueconfig.DriverBaseConfig{}, DSN: dsn}
}

func withDefaultQueue[T any](cfg T, name string) T {
	switch c := any(&cfg).(type) {
	case *queue.Config:
		c.DefaultQueue = name
	case *redisqueue.Config:
		c.DefaultQueue = name
	case *natsqueue.Config:
		c.DefaultQueue = name
	case *sqsqueue.Config:
		c.DefaultQueue = name
	case *rabbitmqqueue.Config:
		c.DefaultQueue = name
	case *mysqlqueue.Config:
		c.DefaultQueue = name
	case *postgresqueue.Config:
		c.DefaultQueue = name
	case *sqlitequeue.Config:
		c.DefaultQueue = name
	}
	return cfg
}

func withObserver[T any](cfg T, observer queue.Observer) T {
	switch c := any(&cfg).(type) {
	case *queue.Config:
		c.Observer = observer
	case *redisqueue.Config:
		c.Observer = observer
	case *natsqueue.Config:
		c.Observer = observer
	case *sqsqueue.Config:
		c.Observer = observer
	case *rabbitmqqueue.Config:
		c.Observer = observer
	case *mysqlqueue.Config:
		c.Observer = observer
	case *postgresqueue.Config:
		c.Observer = observer
	case *sqlitequeue.Config:
		c.Observer = observer
	}
	return cfg
}

func withDBHandle[T any](cfg T, db *sql.DB) T {
	switch c := any(&cfg).(type) {
	case *mysqlqueue.Config:
		c.DB = db
	case *postgresqueue.Config:
		c.DB = db
	case *sqlitequeue.Config:
		c.DB = db
	}
	return cfg
}

func withDBRecoveryPolicy[T any](cfg T, grace, leaseNoTimeout time.Duration) T {
	switch c := any(&cfg).(type) {
	case *mysqlqueue.Config:
		c.ProcessingRecoveryGrace = grace
		c.ProcessingLeaseNoTimeout = leaseNoTimeout
	case *postgresqueue.Config:
		c.ProcessingRecoveryGrace = grace
		c.ProcessingLeaseNoTimeout = leaseNoTimeout
	case *sqlitequeue.Config:
		c.ProcessingRecoveryGrace = grace
		c.ProcessingLeaseNoTimeout = leaseNoTimeout
	}
	return cfg
}

func mysqlDSN(addr string) string    { return testenv.MySQLDSN(addr) }
func postgresDSN(addr string) string { return testenv.PostgresDSN(addr) }
