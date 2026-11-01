module github.com/goforj/queue/driver/natsqueue

go 1.27.0

require (
	github.com/goforj/queue v0.0.0
	github.com/nats-io/nats.go v1.55.0
)

require (
	github.com/klauspost/compress v1.20.2 // indirect
	github.com/nats-io/nkeys v0.4.16 // indirect
	github.com/nats-io/nuid v1.0.1 // indirect
	golang.org/x/crypto v0.58.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
)

replace github.com/goforj/queue => ../..
