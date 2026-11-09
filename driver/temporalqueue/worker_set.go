package temporalqueue

import (
	"context"
	"errors"
	"runtime"
	"sync"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/worker"
)

// workerRoleSpec resolves one physical worker without multiplying remote activity capacity.
type workerRoleSpec struct {
	taskQueue, role string
	options         worker.Options
}

// workerSpecs selects a combined worker or one workflow/local role and one remote-activity role.
func (b *backend) workerSpecs() []workerRoleSpec {
	if b.workflowQueue == b.activityQueue {
		return []workerRoleSpec{{taskQueue: b.workflowQueue, role: "combined", options: b.cfg.WorkerOptions}}
	}
	return []workerRoleSpec{{taskQueue: b.workflowQueue, role: "workflow", options: b.cfg.WorkflowWorkerOptions}, {taskQueue: b.activityQueue, role: "activity", options: b.cfg.ActivityWorkerOptions}}
}

// startWorkerSet creates every registry before starting any poller and rolls back the whole set on failure.
func (b *backend) startWorkerSet(c client.Client) (err error) {
	plan, err := b.applicationPlan()
	if err != nil {
		return err
	}
	var workers []worker.Worker
	var barriers []chan struct{}
	for _, spec := range b.workerSpecs() {
		options := spec.options
		if options.SysInfoProvider != nil {
			options.SysInfoProvider = &guardedSystemInfo{inner: options.SysInfoProvider, gate: b.gate}
		}
		options.Interceptors = append([]interceptor.WorkerInterceptor(nil), options.Interceptors...)
		for i, item := range options.Interceptors {
			options.Interceptors[i] = &trackedWorkerInterceptor{inner: item, gate: b.gate}
		}
		if spec.role == "workflow" {
			options.LocalActivityWorkerOnly = true
		} else {
			if spec.role == "activity" {
				options.DisableWorkflowWorker = true
			}
			if b.workers > 0 {
				options.MaxConcurrentActivityExecutionSize = b.workers
			} else if options.MaxConcurrentActivityExecutionSize == 0 {
				options.MaxConcurrentActivityExecutionSize = max(1, runtime.NumCPU())
			}
		}
		if options.WorkerStopTimeout == 0 {
			options.WorkerStopTimeout = b.cfg.ShutdownTimeout
		}
		options.OnFatalError = b.onFatal
		w := worker.New(c, spec.taskQueue, options)
		if err := plan.apply(b, w, spec.role); err != nil {
			return err
		}
		workers = append(workers, w)
		barriers = append(barriers, make(chan struct{}))
	}
	stop := newStopOperation(func() {
		var joined sync.WaitGroup
		for i, w := range workers {
			joined.Go(func() { <-barriers[i]; w.Stop() })
		}
		joined.Wait()
	})
	closed := make([]bool, len(barriers))
	b.mu.Lock()
	if b.stopping {
		b.mu.Unlock()
		return errAdmissionClosed
	}
	b.worker = workers[0]
	b.workerClient = c
	b.stop = stop
	b.mu.Unlock()
	defer func() {
		if recover() != nil {
			err = errors.New("Temporal worker startup adapter failed")
		}
		for i, barrier := range barriers {
			if !closed[i] {
				close(barrier)
			}
		}
		if err != nil {
			_ = stop.wait(context.Background())
		}
	}()
	for i, w := range workers {
		b.mu.Lock()
		stopping := b.stopping
		b.mu.Unlock()
		if stopping {
			return errAdmissionClosed
		}
		startErr := w.Start()
		closed[i] = true
		close(barriers[i])
		if startErr != nil {
			return errors.New("Temporal worker startup failed")
		}
	}
	return nil
}
