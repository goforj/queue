//go:build ignore
// +build ignore

// examplegen:manual

package main

import (
	"github.com/goforj/queue/driver/rabbitmqqueue"
)

func main() {
	q, err := rabbitmqqueue.New("amqp://guest:guest@127.0.0.1:5672/")
	if err != nil {
		return
	}
	_ = q
}

