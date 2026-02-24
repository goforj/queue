//go:build ignore
// +build ignore

// examplegen:manual

package main

import "github.com/goforj/queue/driver/natsqueue"

func main() {
	q, err := natsqueue.New("nats://127.0.0.1:4222")
	if err != nil {
		return
	}
	_ = q
}

