package temporalqueue

import (
	"errors"
	"math"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/goforj/queue"
	"github.com/goforj/queue/internal/runtimehook"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

// Routing describes the resource's resolved native worker roles.
// Native starters persist ActivityTaskQueue in application workflow input when using split roles.
type Routing struct {
	WorkflowTaskQueue string
	ActivityTaskQueue string
}

// WorkerRouting returns the actual SDK task queues without silently rewriting native activity options.
func WorkerRouting(q *queue.Queue) (Routing, error) {
	b, ok := runtimehook.DriverResource(q).(*backend)
	if !ok {
		return Routing{}, errors.New("queue is not a Temporal resource")
	}
	return Routing{WorkflowTaskQueue: b.workflowQueue, ActivityTaskQueue: b.activityQueue}, nil
}

// validateWorkerOptions rejects conflicts before connection construction or remote acceptance.
func validateWorkerOptions(options worker.Options, budget time.Duration) error {
	if len(options.Plugins) > 0 {
		return errors.New("experimental Temporal worker plugins are unsupported")
	}
	if options.EnableSessionWorker || options.MaxConcurrentSessionExecutionSize != 0 || options.Tuner != nil {
		return errors.New("Temporal session workers and tuners are unsupported")
	}
	if options.OnFatalError != nil || options.DisableWorkflowWorker || options.LocalActivityWorkerOnly {
		return errors.New("Temporal worker roles and fatal callbacks belong to the resource")
	}
	if options.WorkerStopTimeout < 0 || options.WorkerStopTimeout > budget {
		return errors.New("Temporal worker stop timeout exceeds shutdown budget")
	}
	for _, count := range []int{options.MaxConcurrentActivityExecutionSize, options.MaxConcurrentLocalActivityExecutionSize, options.MaxConcurrentWorkflowTaskExecutionSize, options.MaxConcurrentActivityTaskPollers, options.MaxConcurrentWorkflowTaskPollers, options.MaxConcurrentNexusTaskPollers, options.MaxConcurrentNexusTaskExecutionSize} {
		if count < 0 || count > 65536 {
			return errors.New("Temporal worker concurrency is out of bounds")
		}
	}
	if options.MaxConcurrentWorkflowTaskExecutionSize == 1 || options.MaxConcurrentWorkflowTaskPollers == 1 {
		return errors.New("Temporal workflow execution and poller counts cannot be one")
	}
	if options.MaxConcurrentActivityTaskPollers > 128 || options.MaxConcurrentWorkflowTaskPollers > 128 || options.MaxConcurrentNexusTaskPollers > 128 {
		return errors.New("Temporal poller count exceeds 128")
	}
	if options.WorkflowTaskPollerBehavior != nil || options.ActivityTaskPollerBehavior != nil || options.NexusTaskPollerBehavior != nil {
		return errors.New("experimental Temporal poller behaviors are unsupported")
	}
	for _, rate := range []float64{options.WorkerActivitiesPerSecond, options.WorkerLocalActivitiesPerSecond, options.TaskQueueActivitiesPerSecond} {
		if rate < 0 || math.IsNaN(rate) || math.IsInf(rate, 0) {
			return errors.New("Temporal activity rate must be finite and nonnegative")
		}
	}
	deployment := options.DeploymentOptions
	if deployment.UseVersioning && (deployment.Version.DeploymentName == "" || deployment.Version.BuildID == "" || deployment.DefaultVersioningBehavior == workflow.VersioningBehaviorUnspecified) {
		return errors.New("Temporal versioned workers require a deployment, build and default workflow behavior")
	}
	if !deployment.UseVersioning && deployment.DefaultVersioningBehavior != workflow.VersioningBehaviorUnspecified {
		return errors.New("Temporal default versioning behavior requires deployment versioning")
	}
	if deployment.Version.BuildID != "" && options.BuildID != "" {
		return errors.New("Temporal deployment and legacy build identities conflict")
	}
	return nil
}

// resolveTopology snapshots the one supported remote-activity queue and optional split workflow role.
func resolveTopology(cfg Config) (string, string, error) {
	logical := queue.PhysicalQueueName(cfg.DefaultQueue, "")
	if logical == "" {
		logical = "default"
	}
	workflowQueue, activityQueue := cfg.WorkflowTaskQueue, cfg.ActivityTaskQueue
	if (workflowQueue == "") != (activityQueue == "") {
		return "", "", errors.New("Temporal split routing requires both task queue names")
	}
	if workflowQueue == "" {
		workflowQueue = logical
		activityQueue = logical
	}
	for _, name := range []string{workflowQueue, activityQueue} {
		if !utf8.ValidString(name) || strings.TrimSpace(name) != name || len(name) > 255 {
			return "", "", errors.New("Temporal task queue name is invalid")
		}
	}
	split := workflowQueue != activityQueue
	if split {
		if cfg.WorkflowWorkerOptions.MaxConcurrentActivityExecutionSize != 0 || cfg.WorkflowWorkerOptions.MaxConcurrentActivityTaskPollers != 0 {
			return "", "", errors.New("workflow role cannot configure remote activity capacity")
		}
		if cfg.ActivityWorkerOptions.MaxConcurrentWorkflowTaskExecutionSize != 0 || cfg.ActivityWorkerOptions.MaxConcurrentWorkflowTaskPollers != 0 {
			return "", "", errors.New("activity role cannot configure workflow capacity")
		}

		if !reflect.ValueOf(cfg.WorkerOptions).IsZero() {
			return "", "", errors.New("split Temporal roles require role-specific worker options")
		}
		if err := validateWorkerOptions(cfg.WorkflowWorkerOptions, cfg.ShutdownTimeout); err != nil {
			return "", "", err
		}
		if err := validateWorkerOptions(cfg.ActivityWorkerOptions, cfg.ShutdownTimeout); err != nil {
			return "", "", err
		}
		first, second := cfg.WorkflowWorkerOptions.DeploymentOptions, cfg.ActivityWorkerOptions.DeploymentOptions
		if first.UseVersioning != second.UseVersioning || first.Version != second.Version {
			return "", "", errors.New("Temporal split roles require the same deployment version identity")
		}
	} else {
		if !reflect.ValueOf(cfg.WorkflowWorkerOptions).IsZero() || !reflect.ValueOf(cfg.ActivityWorkerOptions).IsZero() {
			return "", "", errors.New("combined Temporal workers require WorkerOptions")
		}
		if err := validateWorkerOptions(cfg.WorkerOptions, cfg.ShutdownTimeout); err != nil {
			return "", "", err
		}
	}
	return workflowQueue, activityQueue, nil
}

// remoteOptions selects the sole remote-activity capacity authority for this resource.
func (b *backend) remoteOptions() worker.Options {
	if b.workflowQueue != b.activityQueue {
		return b.cfg.ActivityWorkerOptions
	}
	return b.cfg.WorkerOptions
}
