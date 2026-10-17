module github.com/goforj/queue/examples

go 1.27.0

replace github.com/goforj/queue => ./..

replace github.com/goforj/queue/driver/redisqueue => ../driver/redisqueue

replace github.com/goforj/queue/driver/sqlqueuecore => ../driver/sqlqueuecore

replace github.com/goforj/queue/driver/mysqlqueue => ../driver/mysqlqueue

replace github.com/goforj/queue/driver/postgresqueue => ../driver/postgresqueue

replace github.com/goforj/queue/driver/sqlitequeue => ../driver/sqlitequeue

replace github.com/goforj/queue/driver/natsqueue => ../driver/natsqueue

replace github.com/goforj/queue/driver/sqsqueue => ../driver/sqsqueue

replace github.com/goforj/queue/driver/rabbitmqqueue => ../driver/rabbitmqqueue

require (
	filippo.io/edwards25519 v1.2.0 // indirect
	github.com/aws/aws-sdk-go-v2 v1.47.3 // indirect
	github.com/aws/aws-sdk-go-v2/config v1.33.9 // indirect
	github.com/aws/aws-sdk-go-v2/credentials v1.20.9 // indirect
	github.com/aws/aws-sdk-go-v2/feature/ec2/imds v1.20.3 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.5.6 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.8.6 // indirect
	github.com/aws/aws-sdk-go-v2/internal/v4a v1.5.6 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.13.21 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.14.6 // indirect
	github.com/aws/aws-sdk-go-v2/service/signin v1.10.4 // indirect
	github.com/aws/aws-sdk-go-v2/service/sqs v1.52.4 // indirect
	github.com/aws/aws-sdk-go-v2/service/sso v1.38.4 // indirect
	github.com/aws/aws-sdk-go-v2/service/ssooidc v1.43.4 // indirect
	github.com/aws/aws-sdk-go-v2/service/sts v1.51.4 // indirect
	github.com/aws/smithy-go v1.28.5 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/go-sql-driver/mysql v1.10.1 // indirect
	github.com/goforj/queue v0.0.0 // indirect
	github.com/goforj/queue/driver/mysqlqueue v0.0.0 // indirect
	github.com/goforj/queue/driver/natsqueue v0.0.0 // indirect
	github.com/goforj/queue/driver/postgresqueue v0.0.0 // indirect
	github.com/goforj/queue/driver/rabbitmqqueue v0.0.0 // indirect
	github.com/goforj/queue/driver/redisqueue v0.0.0 // indirect
	github.com/goforj/queue/driver/sqlitequeue v0.0.0 // indirect
	github.com/goforj/queue/driver/sqlqueuecore v0.0.0 // indirect
	github.com/goforj/queue/driver/sqsqueue v0.0.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/hibiken/asynq v0.26.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/pgx/v5 v5.10.0 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/klauspost/compress v1.20.2 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/nats-io/nats.go v1.54.0 // indirect
	github.com/nats-io/nkeys v0.4.16 // indirect
	github.com/nats-io/nuid v1.0.1 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/rabbitmq/amqp091-go v1.15.0 // indirect
	github.com/redis/go-redis/v9 v9.23.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/robfig/cron/v3 v3.0.1 // indirect
	github.com/spf13/cast v1.10.0 // indirect
	go.uber.org/atomic v1.12.0 // indirect
	golang.org/x/crypto v0.57.0 // indirect
	golang.org/x/sync v0.23.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/text v0.42.0 // indirect
	golang.org/x/time v0.14.0 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
	modernc.org/libc v1.75.6 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.1 // indirect
	modernc.org/sqlite v1.58.0 // indirect
)
