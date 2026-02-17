package queue

// Driver identifies the queue backend.
type Driver string

const (
	DriverDatabase   Driver = "database"
	DriverRedis      Driver = "redis"
	DriverSync       Driver = "sync"
	DriverWorkerpool Driver = "workerpool"
)
