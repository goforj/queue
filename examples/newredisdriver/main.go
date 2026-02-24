//go:build ignore
// +build ignore

// examplegen:manual

package main

import "github.com/goforj/queue/driver/redisqueue"

func main() {
	q, err := redisqueue.New("127.0.0.1:6379")
	if err != nil {
		return
	}
	_ = q
}

