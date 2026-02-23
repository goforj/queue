//go:build ignore
// +build ignore

// examplegen:generated

package main

import "github.com/goforj/queue"

func main() {
	// NewRabbitMQ creates a Queue on the RabbitMQ backend.

	// Example: rabbitmq backend
	q, err := queue.NewRabbitMQ("amqp://guest:guest@127.0.0.1:5672/")
	if err != nil {
		return
	}
	_ = q
}
