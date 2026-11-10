package temporalqueue

import (
	"errors"
	"github.com/goforj/queue"
	"github.com/goforj/queue/busruntime"
	engine "github.com/goforj/queue/internal/workflow"
)

// errUnsupportedOutcome excludes SQL-style budget-free redelivery from Temporal's bounded activity policy.
var errUnsupportedOutcome = errors.New("Temporal queue does not support uncommitted handler outcomes")

// errQueuePanic is the only generic-queue panic detail retained by shared observers.
var errQueuePanic = errors.New("Temporal queue handler panicked")

// translateQueueError runs before the shared executor classifies or publishes the attempt outcome.
func translateQueueError(err error) error {
	if busruntime.IsUncommitted(err) {
		return queue.Permanent(errUnsupportedOutcome)
	}
	if errors.Is(err, engine.ErrHandlerNotRegistered) {
		return queue.Permanent(err)
	}
	return err
}

// translateQueuePanic omits application panic values while retaining one terminal generic-job outcome.
func translateQueuePanic(any) error { return queue.Permanent(errQueuePanic) }
