module github.com/goforj/queue/driver/redisqueue

go 1.24.4

require (
	github.com/goforj/queue v0.0.0
	github.com/hibiken/asynq v0.26.0
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/dgryski/go-rendezvous v0.0.0-20200823014737-9f7001d12a5f // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/redis/go-redis/v9 v9.14.1 // indirect
	github.com/robfig/cron/v3 v3.0.1 // indirect
	github.com/spf13/cast v1.10.0 // indirect
	golang.org/x/sys v0.40.0 // indirect
	golang.org/x/time v0.14.0 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
)

replace github.com/goforj/queue => ../..
