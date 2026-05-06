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
	"github.com/goforj/queue/internal/testbridge"
)

func NewQueueRuntime(cfg any) (Runtime, error) {
	var (
		high *queue.Queue
		err  error
	)
	switch c := cfg.(type) {
	case queue.Config:
		high, err = queue.New(c)
	case redisqueue.Config:
		high, err = redisqueue.NewWithConfig(c)
	case natsqueue.Config:
		high, err = natsqueue.NewWithConfig(c)
	case sqsqueue.Config:
		high, err = sqsqueue.NewWithConfig(c)
	case rabbitmqqueue.Config:
		high, err = rabbitmqqueue.NewWithConfig(c)
	case mysqlqueue.Config:
		high, err = mysqlqueue.NewWithConfig(c)
	case postgresqueue.Config:
		high, err = postgresqueue.NewWithConfig(c)
	case sqlitequeue.Config:
		high, err = sqlitequeue.NewWithConfig(c)
	default:
		return nil, fmt.Errorf("unsupported integration queue runtime config type %T", cfg)
	}
	if err != nil {
		return nil, err
	}
	raw, err := testbridge.RuntimeFromQueue(high)
	if err != nil {
		return nil, err
	}
	r := wrapRuntime(raw)
	if r == nil {
		return nil, fmt.Errorf("unsupported runtime type %T", raw)
	}
	return r, nil
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
