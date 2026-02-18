//go:build ignore
// +build ignore

package main

import "github.com/goforj/queue"

func main() {
	// Bind unmarshals task payload JSON into dst.

	// Example: bind payload
	type EmailPayload struct {
		ID int `json:"id"`
	}
	task := queue.NewTask("emails:send").Payload(EmailPayload{ID: 1})
	var payload EmailPayload
	_ = task.Bind(&payload)
}
