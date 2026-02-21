//go:build ignore
// +build ignore

package main

import "github.com/goforj/queue"

func main() {
	// PayloadBytes returns a copy of job payload bytes.

	// Example: payload bytes read
	job := queue.NewJob("emails:send").Payload([]byte(`{"id":1}`))
	payload := job.PayloadBytes()
	_ = payload
}
