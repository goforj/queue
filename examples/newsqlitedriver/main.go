//go:build ignore
// +build ignore

// examplegen:manual

package main

import "github.com/goforj/queue/driver/sqlitequeue"

func main() {
	q, err := sqlitequeue.New("file:queue.db?_busy_timeout=5000")
	if err != nil {
		return
	}
	_ = q
}
