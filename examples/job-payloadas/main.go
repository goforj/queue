//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"fmt"
	"github.com/goforj/queue"
)

func main() {
	// PayloadAs unmarshals the job payload JSON and returns it as T.

	// Example: typed job payload
	type EmailPayload struct {
		To string `json:"to"`
	}
	job := queue.NewJob("emails:send").Payload(EmailPayload{To: "user@example.com"})
	payload, err := job.PayloadAs[EmailPayload]()
	fmt.Println(err == nil, payload.To)
	// true user@example.com
}
