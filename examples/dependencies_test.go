package examples_test

// Generated example mains use the ignore build tag and compile through overlays.
// Keep their module graph visible to tidy and dependency update tools.
import (
	_ "github.com/goforj/queue"
	_ "github.com/goforj/queue/driver/mysqlqueue"
	_ "github.com/goforj/queue/driver/natsqueue"
	_ "github.com/goforj/queue/driver/postgresqueue"
	_ "github.com/goforj/queue/driver/rabbitmqqueue"
	_ "github.com/goforj/queue/driver/redisqueue"
	_ "github.com/goforj/queue/driver/sqlitequeue"
	_ "github.com/goforj/queue/driver/sqlqueuecore"
	_ "github.com/goforj/queue/driver/sqsqueue"
)
