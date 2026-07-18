// Package queuefake preserves the historical queue-first testing harness.
//
// Its queue and bus compatibility views now share one concurrency-safe
// queue.FakeQueue. New code should use queue.NewFake directly.
package queuefake
