module github.com/goforj/queue/driver/mysqlqueue

go 1.27.0

require (
	github.com/go-sql-driver/mysql v1.9.3
	github.com/goforj/queue v0.0.0
	github.com/goforj/queue/driver/sqlqueuecore v0.0.0
)

require filippo.io/edwards25519 v1.1.1 // indirect

replace github.com/goforj/queue => ../..

replace github.com/goforj/queue/driver/sqlqueuecore => ../sqlqueuecore
