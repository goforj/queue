// Package queuefake provides a queue-first test harness for queue and workflow assertions.
//
// It wraps queue.NewFake() for dispatch assertions and bus.NewFake() for workflow
// orchestration assertions so tests can stay aligned with the flattened queue API
// surface.
package queuefake

