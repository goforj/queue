module github.com/goforj/queue/driver/natsqueue

go 1.24.4

require (
	github.com/goforj/queue v0.0.0
	github.com/nats-io/nats.go v1.48.0
)

require (
	github.com/klauspost/compress v1.18.0 // indirect
	github.com/nats-io/nkeys v0.4.11 // indirect
	github.com/nats-io/nuid v1.0.1 // indirect
	golang.org/x/crypto v0.37.0 // indirect
	golang.org/x/sys v0.40.0 // indirect
)

replace github.com/goforj/queue => ../..
