package queue

// Driver identifies the queue backend.
// @group Driver
//
// Example: driver values
//
//	fmt.Println(queue.DriverNull, queue.DriverSync, queue.DriverWorkerpool, queue.DriverDatabase, queue.DriverRedis, queue.DriverNATS, queue.DriverSQS, queue.DriverRabbitMQ)
type Driver string

const (
	// DriverNull drops dispatched jobs and performs no execution.
	DriverNull Driver = "null"
	// DriverSync runs handlers inline in the caller goroutine.
	DriverSync Driver = "sync"
	// DriverWorkerpool runs handlers on an in-memory workerpool.
	DriverWorkerpool Driver = "workerpool"
	// DriverDatabase selects the SQL-backed queue backend.
	DriverDatabase Driver = "database"
	// DriverRedis selects the Redis (asynq) backend.
	DriverRedis Driver = "redis"
	// DriverNATS selects the NATS backend.
	DriverNATS Driver = "nats"
	// DriverSQS selects the AWS SQS backend.
	DriverSQS Driver = "sqs"
	// DriverRabbitMQ selects the RabbitMQ backend.
	DriverRabbitMQ Driver = "rabbitmq"
)
