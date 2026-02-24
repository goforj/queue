module github.com/goforj/queue/examples

go 1.24.4

replace github.com/goforj/queue => ..

replace github.com/goforj/queue/driver/redisqueue => ../driver/redisqueue

replace github.com/goforj/queue/driver/sqlqueuecore => ../driver/sqlqueuecore

replace github.com/goforj/queue/driver/mysqlqueue => ../driver/mysqlqueue

replace github.com/goforj/queue/driver/postgresqueue => ../driver/postgresqueue

replace github.com/goforj/queue/driver/sqlitequeue => ../driver/sqlitequeue

replace github.com/goforj/queue/driver/natsqueue => ../driver/natsqueue

replace github.com/goforj/queue/driver/sqsqueue => ../driver/sqsqueue

replace github.com/goforj/queue/driver/rabbitmqqueue => ../driver/rabbitmqqueue
