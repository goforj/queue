package temporalqueue

import (
	"context"
	"fmt"
	"sync"
)

var errAdmissionClosed = fmt.Errorf("%w: resource is stopping", ErrAdapterUnavailable)

// admission fences all application adapter entries against dependency cleanup.
type admission struct {
	mu     sync.Mutex
	closed bool
	active int
	idle   chan struct{}
}

// newAdmission initializes an already-idle resource without a polling loop.
func newAdmission() *admission {
	idle := make(chan struct{})
	close(idle)
	return &admission{idle: idle}
}

// enter admits one callback under the same lock used to close admission.
func (a *admission) enter() (func(), error) {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return nil, errAdmissionClosed
	}
	if a.active == 0 {
		a.idle = make(chan struct{})
	}
	a.active++
	a.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			a.mu.Lock()
			a.active--
			if a.active == 0 {
				close(a.idle)
			}
			a.mu.Unlock()
		})
	}, nil
}

// close stops admission without pretending existing callbacks have returned.
func (a *admission) close() { a.mu.Lock(); a.closed = true; a.mu.Unlock() }

// wait joins actual admitted work while retaining caller-specific deadlines.
func (a *admission) wait(ctx context.Context) error {
	a.mu.Lock()
	idle := a.idle
	a.mu.Unlock()
	select {
	case <-idle:
		return nil
	default:
	}
	select {
	case <-idle:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// stopOperation arbitrates the SDK's non-idempotent Stop across fatal and explicit paths.
type stopOperation struct {
	once sync.Once
	done chan struct{}
	stop func()
}

// newStopOperation preserves its result beyond the first caller's timeout.
func newStopOperation(stop func()) *stopOperation {
	return &stopOperation{done: make(chan struct{}), stop: stop}
}

// wait initiates or joins one SDK stop, independently of application drain.
func (s *stopOperation) wait(ctx context.Context) error {
	s.once.Do(func() { go func() { s.stop(); close(s.done) }() })
	select {
	case <-s.done:
		return nil
	default:
	}
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
