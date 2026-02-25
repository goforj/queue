package queuecore

import "github.com/goforj/queue"

var (
	// ErrDuplicate is returned when a driver rejects a unique job dispatch because
	// an unexpired unique lock already exists.
	ErrDuplicate = queue.ErrDuplicate
	// ErrBackoffUnsupported is returned when a driver/runtime does not support
	// custom backoff behavior.
	ErrBackoffUnsupported = queue.ErrBackoffUnsupported
)

// ValidateDriverJob validates a low-level queue.Job before driver dispatch.
func ValidateDriverJob(job queue.Job) error { return queue.ValidateDriverJob(job) }

// DriverOptions returns normalized driver-facing job options for a queue.Job.
func DriverOptions(job queue.Job) queue.DriverJobOptions { return queue.DriverOptions(job) }

// DriverWithAttempt annotates a queue.Job with an attempt count for worker
// handler execution paths.
func DriverWithAttempt(job queue.Job, attempt int) queue.Job {
	return queue.DriverWithAttempt(job, attempt)
}

// SafeObserve emits an event to an observer and recovers panics from the
// observer callback.
func SafeObserve(observer queue.Observer, event queue.Event) { queue.SafeObserve(observer, event) }

// NormalizeQueueName returns "default" when the provided queue name is empty.
func NormalizeQueueName(name string) string {
	if name == "" {
		return "default"
	}
	return name
}
