//go:build ignore
// +build ignore

package main

import "github.com/goforj/queue"

func main() {
	// NewStatsCollector creates an event collector for queue counters.

	// Example: new stats collector
	collector := queue.NewStatsCollector()
	_ = collector
}
