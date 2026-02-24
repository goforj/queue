//go:build ignore
// +build ignore

// examplegen:generated

package main

import "github.com/goforj/queue"

func main() {
	// Bind unmarshals job payload JSON into dst.

	// Example: bind payload
	type EmailPayload struct {
		ID int    `json:"id"`
		To string `json:"to"`
	}
	job := queue.NewJob("emails:send").Payload(EmailPayload{
		ID: 1,
		To: "user@example.com",
	})
	var payload EmailPayload
	if err := job.Bind(&payload); err != nil {
		return
	}
	_ = payload.To
}
