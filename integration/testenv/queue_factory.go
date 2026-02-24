package testenv

import (
	"fmt"

	"github.com/goforj/queue"
	"github.com/goforj/queue/driver/mysqlqueue"
	"github.com/goforj/queue/driver/natsqueue"
	"github.com/goforj/queue/driver/postgresqueue"
	"github.com/goforj/queue/driver/rabbitmqqueue"
	"github.com/goforj/queue/driver/redisqueue"
	"github.com/goforj/queue/driver/sqlitequeue"
	"github.com/goforj/queue/driver/sqsqueue"
)

func NewQueueRuntime(cfg any) (queue.QueueRuntime, error) {
	switch c := cfg.(type) {
	case queue.Config:
		return queue.NewQueue(c)
	case redisqueue.Config:
		return redisqueue.NewRuntime(c)
	case natsqueue.Config:
		return natsqueue.NewRuntime(c)
	case sqsqueue.Config:
		return sqsqueue.NewRuntime(c)
	case rabbitmqqueue.Config:
		return rabbitmqqueue.NewRuntime(c)
	case mysqlqueue.Config:
		return mysqlqueue.NewRuntime(c)
	case postgresqueue.Config:
		return postgresqueue.NewRuntime(c)
	case sqlitequeue.Config:
		return sqlitequeue.NewRuntime(c)
	default:
		return nil, fmt.Errorf("unsupported integration queue runtime config type %T", cfg)
	}
}

func NewQueue(cfg any, opts ...queue.Option) (*queue.Queue, error) {
	switch c := cfg.(type) {
	case queue.Config:
		return queue.New(c, opts...)
	case redisqueue.Config:
		return redisqueue.NewWithConfig(c, opts...)
	case natsqueue.Config:
		return natsqueue.NewWithConfig(c, opts...)
	case sqsqueue.Config:
		return sqsqueue.NewWithConfig(c, opts...)
	case rabbitmqqueue.Config:
		return rabbitmqqueue.NewWithConfig(c, opts...)
	case mysqlqueue.Config:
		return mysqlqueue.NewWithConfig(c, opts...)
	case postgresqueue.Config:
		return postgresqueue.NewWithConfig(c, opts...)
	case sqlitequeue.Config:
		return sqlitequeue.NewWithConfig(c, opts...)
	default:
		return nil, fmt.Errorf("unsupported integration queue config type %T", cfg)
	}
}
