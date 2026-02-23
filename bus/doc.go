// Package bus provides the workflow orchestration engine used by queue.
//
// Most applications should use the top-level queue package (`queue.New(...)`,
// `Queue.Dispatch`, `Queue.Chain`, `Queue.Batch`) rather than importing bus
// directly.
//
// This package remains available for advanced/internal orchestration plumbing,
// custom workflow integration, and lower-level testing.
package bus
