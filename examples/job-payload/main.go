//go:build ignore
// +build ignore

// examplegen:generated

package main

import "github.com/goforj/queue"

func main() {
	example1()
	example2()
	example3()
}

func example1() {
	// Payload sets job payload from common value types.

	// Example: payload bytes
	jobBytes := queue.NewJob("emails:send").Payload([]byte(`{"id":1}`))
	_ = jobBytes

}

func example2() {
	// Example: payload struct
	type Meta struct {
		Nested bool `json:"nested"`
	}
	type EmailPayload struct {
		ID   int    `json:"id"`
		To   string `json:"to"`
		Meta Meta   `json:"meta"`
	}
	jobStruct := queue.NewJob("emails:send").Payload(EmailPayload{
		ID:   1,
		To:   "user@example.com",
		Meta: Meta{Nested: true},
	})
	_ = jobStruct

}

func example3() {
	// Example: payload map
	jobMap := queue.NewJob("emails:send").Payload(map[string]any{
		"id":  1,
		"to":  "user@example.com",
		"meta": map[string]any{"nested": true},
	})
	_ = jobMap
}

