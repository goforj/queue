//go:build ignore
// +build ignore

// examplegen:generated

package main

import (
	"fmt"
	"github.com/goforj/queue"
)

func main() {
	// PayloadAs unmarshals the delivered payload and returns it as T.

	// Example: typed message payload
	type EmailPayload struct {
		To string `json:"to"`
	}
	message := queue.NewMessage("emails:send", []byte(`{"to":"user@example.com"}`))
	payload, err := message.PayloadAs[EmailPayload]()
	fmt.Println(err == nil, payload.To)
	// true user@example.com
}
