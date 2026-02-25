//go:build ignore
// +build ignore

// examplegen:generated

package main

import "github.com/goforj/queue"

func main() {
	// Payload sets job payload from common value types.

	// Example: payload bytes
	func() {
		jobBytes := queue.NewJob("emails:send").Payload([]byte(`{"id":1}`))
		_ = jobBytes

	}()

	// Example: payload struct
	func() {
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

	}()

	// Example: payload map
	func() {
		jobMap := queue.NewJob("emails:send").Payload(map[string]any{
			"id":  1,
			"to":  "user@example.com",
			"meta": map[string]any{"nested": true},
		})
		_ = jobMap
	}()
}
