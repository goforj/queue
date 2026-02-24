// Package runtimehook provides a tiny internal hook registry used to avoid
// import cycles between the public queue package and internal bridge/test
// helpers introduced during API flattening.
package runtimehook

type WorkerFactory func(workers int) (any, error)

// BuildQueueFromDriver builds a high-level *queue.Queue from a private driver
// backend representation provided by internal/driverbridge.
type BuildQueueFromDriverFunc func(cfg any, backend any, workerFactory WorkerFactory, opts []any) (any, error)

// ExtractRuntimeFromQueue exposes the internal runtime from *queue.Queue for
// test-only bridges (not public API use).
type ExtractRuntimeFromQueueFunc func(q any) (any, error)

var BuildQueueFromDriver BuildQueueFromDriverFunc
var ExtractRuntimeFromQueue ExtractRuntimeFromQueueFunc
