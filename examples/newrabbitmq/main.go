//go:build ignore
// +build ignore

package main

import "github.com/goforj/queue"

func main() {
	// NewRabbitMQ creates a RabbitMQ-backed queue runtime.

	// Example: new rabbitmq queue
	q, err := queue.NewRabbitMQ("amqp://guest:guest@127.0.0.1:5672/")
	if err != nil {
		return
	}
	_ = q
}
