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

replace github.com/goforj/queue/driver/temporalqueue => ../driver/temporalqueue

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
	github.com/goforj/queue/driver/temporalqueue v0.0.0
)

require (
	filippo.io/edwards25519 v1.2.0 // indirect
	github.com/aws/aws-sdk-go-v2 v1.47.4 // indirect
	github.com/aws/aws-sdk-go-v2/config v1.33.10 // indirect
	github.com/aws/aws-sdk-go-v2/credentials v1.20.10 // indirect
	github.com/aws/aws-sdk-go-v2/feature/ec2/imds v1.20.4 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.5.7 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.8.7 // indirect
	github.com/aws/aws-sdk-go-v2/internal/v4a v1.5.7 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.13.22 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.14.7 // indirect
	github.com/aws/aws-sdk-go-v2/service/signin v1.10.5 // indirect
	github.com/aws/aws-sdk-go-v2/service/sqs v1.52.6 // indirect
	github.com/aws/aws-sdk-go-v2/service/sso v1.38.5 // indirect
	github.com/aws/aws-sdk-go-v2/service/ssooidc v1.43.5 // indirect
	github.com/aws/aws-sdk-go-v2/service/sts v1.51.5 // indirect
	github.com/aws/smithy-go v1.28.6 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/dustin/go-humanize v1.1.0 // indirect
	github.com/facebookgo/clock v0.0.0-20150410010913-600d898af40a // indirect
	github.com/go-sql-driver/mysql v1.10.1 // indirect
	github.com/gogo/protobuf v1.3.2 // indirect
	github.com/golang/mock v1.6.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/grpc-ecosystem/go-grpc-middleware/v2 v2.3.2 // indirect
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.22.0 // indirect
	github.com/hibiken/asynq v0.26.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/pgx/v5 v5.11.0 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/klauspost/compress v1.20.2 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/nats-io/nats.go v1.55.0 // indirect
	github.com/nats-io/nkeys v0.4.16 // indirect
	github.com/nats-io/nuid v1.0.1 // indirect
	github.com/ncruces/go-strftime v1.1.0 // indirect
	github.com/nexus-rpc/sdk-go v0.6.0 // indirect
	github.com/rabbitmq/amqp091-go v1.16.0 // indirect
	github.com/redis/go-redis/v9 v9.23.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/robfig/cron v1.2.0 // indirect
	github.com/robfig/cron/v3 v3.0.1 // indirect
	github.com/spf13/cast v1.10.0 // indirect
	github.com/stretchr/objx v0.5.3 // indirect
	github.com/stretchr/testify v1.12.1 // indirect
	go.temporal.io/api v1.62.12 // indirect
	go.temporal.io/sdk v1.45.0 // indirect
	go.uber.org/atomic v1.12.0 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/crypto v0.58.0 // indirect
	golang.org/x/net v0.60.0 // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/text v0.43.0 // indirect
	golang.org/x/time v0.14.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20260526163538-3dc84a4a5aaa // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260526163538-3dc84a4a5aaa // indirect
	google.golang.org/grpc v1.83.2 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
	modernc.org/libc v1.79.0 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.1 // indirect
	modernc.org/sqlite v1.61.0 // indirect
)
