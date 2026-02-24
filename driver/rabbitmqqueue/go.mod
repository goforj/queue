module github.com/goforj/queue/driver/rabbitmqqueue

go 1.24.4

require (
	github.com/goforj/queue v0.0.0
	github.com/rabbitmq/amqp091-go v1.10.0
)

replace github.com/goforj/queue => ../..
