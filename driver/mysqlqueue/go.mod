module github.com/goforj/queue/driver/mysqlqueue

go 1.27.0

require (
	github.com/go-sql-driver/mysql v1.10.1
	github.com/goforj/queue v0.0.0
	github.com/goforj/queue/driver/sqlqueuecore v0.0.0
)

require filippo.io/edwards25519 v1.2.0 // indirect

replace github.com/goforj/queue => ../..

replace github.com/goforj/queue/driver/sqlqueuecore => ../sqlqueuecore
