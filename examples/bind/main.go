//go:build ignore
// +build ignore

package main

import "github.com/goforj/queue"

func main() {
	// Bind unmarshals job payload JSON into dst.

	// Example: bind payload
	type EmailPayload struct {
		ID int `json:"id"`
	}
	job := queue.NewJob("emails:send").Payload(EmailPayload{ID: 1})
	var payload EmailPayload
	_ = job.Bind(&payload)
}
