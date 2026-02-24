//go:build ignore
// +build ignore

// examplegen:manual

package main

import "github.com/goforj/queue/driver/sqsqueue"

func main() {
	q, err := sqsqueue.New("us-east-1")
	if err != nil {
		return
	}
	_ = q
}

