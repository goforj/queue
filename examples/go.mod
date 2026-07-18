module github.com/goforj/queue/examples

go 1.24.4

require (
	github.com/goforj/queue v0.0.0
	github.com/goforj/queue/driver/mysqlqueue v0.0.0
	github.com/goforj/queue/driver/natsqueue v0.0.0
	github.com/goforj/queue/driver/postgresqueue v0.0.0
	github.com/goforj/queue/driver/rabbitmqqueue v0.0.0
	github.com/goforj/queue/driver/redisqueue v0.0.0
	github.com/goforj/queue/driver/sqlitequeue v0.0.0
	github.com/goforj/queue/driver/sqlqueuecore v0.0.0
	github.com/goforj/queue/driver/sqsqueue v0.0.0
)

require (
	github.com/aws/aws-sdk-go-v2 v1.41.1 // indirect
	github.com/aws/aws-sdk-go-v2/config v1.32.8 // indirect
	github.com/aws/aws-sdk-go-v2/credentials v1.19.8 // indirect
	github.com/aws/aws-sdk-go-v2/feature/ec2/imds v1.18.17 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.4.17 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.7.17 // indirect
	github.com/aws/aws-sdk-go-v2/internal/ini v1.8.4 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.13.4 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.13.17 // indirect
	github.com/aws/aws-sdk-go-v2/service/signin v1.0.5 // indirect
	github.com/aws/aws-sdk-go-v2/service/sqs v1.42.21 // indirect
	github.com/aws/aws-sdk-go-v2/service/sso v1.30.9 // indirect
	github.com/aws/aws-sdk-go-v2/service/ssooidc v1.35.14 // indirect
	github.com/aws/aws-sdk-go-v2/service/sts v1.41.6 // indirect
	github.com/aws/smithy-go v1.24.0 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/dgryski/go-rendezvous v0.0.0-20200823014737-9f7001d12a5f // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/hibiken/asynq v0.26.0 // indirect
	github.com/klauspost/compress v1.18.0 // indirect
	github.com/nats-io/nats.go v1.48.0 // indirect
	github.com/nats-io/nkeys v0.4.11 // indirect
	github.com/nats-io/nuid v1.0.1 // indirect
	github.com/rabbitmq/amqp091-go v1.10.0 // indirect
	github.com/redis/go-redis/v9 v9.14.1 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/robfig/cron/v3 v3.0.1 // indirect
	github.com/spf13/cast v1.10.0 // indirect
	golang.org/x/crypto v0.37.0 // indirect
	golang.org/x/exp v0.0.0-20251023183803-a4bb9ffd2546 // indirect
	golang.org/x/sys v0.40.0 // indirect
	golang.org/x/time v0.14.0 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
	modernc.org/libc v1.67.6 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.11.0 // indirect
	modernc.org/sqlite v1.46.0 // indirect
)

replace github.com/goforj/queue => ..

replace github.com/goforj/queue/driver/redisqueue => ../driver/redisqueue

replace github.com/goforj/queue/driver/sqlqueuecore => ../driver/sqlqueuecore

replace github.com/goforj/queue/driver/mysqlqueue => ../driver/mysqlqueue

replace github.com/goforj/queue/driver/postgresqueue => ../driver/postgresqueue

replace github.com/goforj/queue/driver/sqlitequeue => ../driver/sqlitequeue

replace github.com/goforj/queue/driver/natsqueue => ../driver/natsqueue

replace github.com/goforj/queue/driver/sqsqueue => ../driver/sqsqueue

replace github.com/goforj/queue/driver/rabbitmqqueue => ../driver/rabbitmqqueue
