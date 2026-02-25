<p align="center">
  <img src="./docs/images/logo.png?v=1" width="420" alt="queue logo">
</p>

<p align="center">
    queue is a queue and workflow library with pluggable backends and runtime extensions.
</p>

<p align="center">
    <a href="https://pkg.go.dev/github.com/goforj/queue"><img src="https://pkg.go.dev/badge/github.com/goforj/queue.svg" alt="Go Reference"></a>
    <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-blue.svg" alt="License: MIT"></a>
    <a href="https://github.com/goforj/queue/actions"><img src="https://github.com/goforj/queue/actions/workflows/test.yml/badge.svg" alt="Go Test"></a>
    <a href="https://golang.org"><img src="https://img.shields.io/badge/go-1.23+-blue?logo=go" alt="Go version"></a>
    <img src="https://img.shields.io/github/v/tag/goforj/queue?label=version&sort=semver" alt="Latest tag">
    <a href="https://goreportcard.com/report/github.com/goforj/queue"><img src="https://goreportcard.com/badge/github.com/goforj/queue" alt="Go Report Card"></a>
    <a href="https://codecov.io/gh/goforj/queue"><img src="https://codecov.io/gh/goforj/queue/graph/badge.svg?token=40Z5UQATME"/></a>
<!-- test-count:embed:start -->
    <img src="https://img.shields.io/badge/unit_tests-249-brightgreen" alt="Unit tests (executed count)">
    <img src="https://img.shields.io/badge/integration_tests-517-blue" alt="Integration tests (executed count)">
<!-- test-count:embed:end -->
</p>

## Installation

```bash
go get github.com/goforj/queue
```

## Quick Start

```go
import (
	"context"
	"fmt"

	"github.com/goforj/queue"
)

func main() {
	q, _ := queue.NewWorkerpool(
		queue.WithWorkers(2), // optional; default: runtime.NumCPU() (min 1)
	)
	type EmailPayload struct {
		To string `json:"to"`
	}

	q.Register("emails:send", func(ctx context.Context, m queue.Message) error {
		var payload EmailPayload
		_ = m.Bind(&payload)
		fmt.Println("send to", payload.To)
		return nil
	})

	_ = q.StartWorkers(context.Background())
	defer q.Shutdown(context.Background())

	_, _ = q.Dispatch(
		queue.NewJob("emails:send").
			Payload(EmailPayload{To: "user@example.com"}),
	)
}
```

## Quick Start (Advanced: Workflows)

```go
import (
	"context"

	"github.com/goforj/queue"
)

type EmailPayload struct {
	ID int `json:"id"`
}

func main() {
	q, _ := queue.NewWorkerpool()

	q.Register("reports:generate", func(ctx context.Context, m queue.Message) error {
		return nil
	})
	q.Register("reports:upload", func(ctx context.Context, m queue.Message) error {
		var payload EmailPayload
		if err := m.Bind(&payload); err != nil {
			return err
		}
		return nil
	})
	q.Register("users:notify_report_ready", func(ctx context.Context, m queue.Message) error {
		return nil
	})

	_ = q.StartWorkers(context.Background())
	defer q.Shutdown(context.Background())

	chainID, _ := q.Chain(
		// 1) generate report data
		queue.NewJob("reports:generate").Payload(map[string]any{"report_id": "rpt_123"}),
		// 2) upload report artifact after generate succeeds
		queue.NewJob("reports:upload").Payload(EmailPayload{ID: 123}),
		// 3) notify user only after upload succeeds
		queue.NewJob("users:notify_report_ready").Payload(map[string]any{"user_id": 123}),
	).OnQueue("critical").Dispatch(context.Background())
	_ = chainID
}
```

## Run as a Worker Service

Use `Run(ctx)` for long-lived workers: it starts processing, waits for shutdown signals, and performs graceful termination.

```go
import (
	"context"
	"log"
	"os/signal"
	"syscall"

	"github.com/goforj/queue"
)

func main() {
	q, _ := queue.NewWorkerpool()

	// Register handlers before starting workers.
	q.Register("emails:send", func(ctx context.Context, m queue.Message) error {
		return nil
	})

	// Create a context that is canceled on SIGINT/SIGTERM (Ctrl+C, container stop).
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Run starts workers, blocks until ctx is canceled, then gracefully shuts down.
	if err := q.Run(ctx); err != nil {
		log.Fatal(err)
	}
}
```

## Job builder options

```go
// Define a struct for your job payload.
type EmailPayload struct {
	ID int `json:"id"`
	To string `json:"to"`
}

// Fluent builder pattern for job options.
job := queue.NewJob("emails:send").
	// Payload can be bytes, structs, maps, or JSON-marshalable values.
	// Default payload is empty.
	Payload(EmailPayload{ID: 123, To: "user@example.com"}).
	// OnQueue sets the queue name.
	// Default is empty; broker-style drivers expect an explicit queue.
	OnQueue("default").
	// Timeout sets per-job execution timeout.
	// Default is unset; some drivers may apply driver/runtime defaults.
	Timeout(20 * time.Second).
	// Retry sets max retries.
	// Default is 0, which means one total attempt.
	Retry(3).
	// Backoff sets retry delay.
	// Default is unset; Redis dispatch returns ErrBackoffUnsupported.
	Backoff(500 * time.Millisecond).
	// Delay schedules first execution in the future.
	// Default is 0 (run immediately).
	Delay(2 * time.Second).
	// UniqueFor deduplicates Type+Payload for a TTL window.
	// Default is 0 (no dedupe).
	UniqueFor(45 * time.Second)

// Dispatch the job to the queue.
_, _ = q.Dispatch(job)

// In handlers, use Bind to decode payload into a struct.
q.Register("emails:send", func(ctx context.Context, m queue.Message) error {
	var payload EmailPayload
	if err := m.Bind(&payload); err != nil {
		return err
	}
	return nil
})
```

## Drivers

| Driver / Backend | Mode | Notes | Durable | Async | Delay | Unique | Backoff | Timeout | Native Stats |
| ---: | :--- | :--- | :---: | :---: | :---: | :---: | :---: | :---: | :---: |
| <img src="https://img.shields.io/badge/null-%23666?style=flat" alt="Null"> | Drop-only | Discards dispatched jobs; useful for disabled queue modes and smoke tests. | - | - | - | - | - | - | - |
| <img src="https://img.shields.io/badge/sync-%23999999?logo=gnometerminal&logoColor=white" alt="Sync"> | Inline (caller) | Deterministic local execution with no external infra. | - | - | - | ✓ | - | ✓ | - |
| <img src="https://img.shields.io/badge/workerpool-%23696969?logo=clockify&logoColor=white" alt="Workerpool"> | In-process pool | Local async behavior without external broker/database. | - | ✓ | ✓ | ✓ | ✓ | ✓ | - |
| <img src="https://img.shields.io/badge/mysql-%234479A1?logo=mysql&logoColor=white" alt="MySQL"> | SQL durable queue | MySQL driver module (`driver/mysqlqueue`) built on shared SQL queue core. | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| <img src="https://img.shields.io/badge/postgres-%23336791?logo=postgresql&logoColor=white" alt="Postgres"> | SQL durable queue | Postgres driver module (`driver/postgresqueue`) built on shared SQL queue core. | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| <img src="https://img.shields.io/badge/sqlite-%23003B57?logo=sqlite&logoColor=white" alt="SQLite"> | SQL durable queue | SQLite driver module (`driver/sqlitequeue`) built on shared SQL queue core. | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| <img src="https://img.shields.io/badge/redis-%23DC382D?logo=redis&logoColor=white" alt="Redis"> | Redis/Asynq | Production Redis backend (Asynq semantics). | ✓ | ✓ | ✓ | ✓ | - | ✓ | ✓ |
| <img src="https://img.shields.io/badge/NATS-007ACC?style=flat" alt="NATS"> | Broker target | NATS transport with queue-subject routing. | - | ✓ | ✓ | ✓ | ✓ | ✓ | - |
| <img src="https://img.shields.io/badge/SQS-FF9900?style=flat" alt="SQS"> | Broker target | AWS SQS transport with endpoint overrides for localstack/testing. | - | ✓ | ✓ | ✓ | ✓ | ✓ | - |
| <img src="https://img.shields.io/badge/rabbitmq-%23FF6600?logo=rabbitmq&logoColor=white" alt="RabbitMQ"> | Broker target | RabbitMQ transport and worker consumption. | - | ✓ | ✓ | ✓ | ✓ | ✓ | - |

> SQL-backed queues (`sqlite`, `mysql`, `postgres`) are durable and convenient, but they trade throughput for operational simplicity. They default to `1` worker, and increasing concurrency may require DB tuning (indexes, connection pool, lock contention). Prefer broker-backed drivers for higher-throughput workloads.

### Driver constructor quick examples

Use root constructors for in-process backends, and driver-module constructors for external backends. See the `Driver Constructors` API section below for full constructor shapes (`New(...)` and `NewWithConfig(...)`).
Driver backends live in separate packages so applications only import/link the optional backend dependencies they actually use (smaller builds, less dependency overhead, cleaner deploys).

```go
package main

import (
	"github.com/goforj/queue"
	"github.com/goforj/queue/driver/mysqlqueue"
	"github.com/goforj/queue/driver/natsqueue"
	"github.com/goforj/queue/driver/postgresqueue"
	"github.com/goforj/queue/driver/rabbitmqqueue"
	"github.com/goforj/queue/driver/redisqueue"
	"github.com/goforj/queue/driver/sqlitequeue"
	"github.com/goforj/queue/driver/sqsqueue"
)

func main() {
	queue.NewSync()       // in-process sync
	queue.NewWorkerpool() // in-process worker pool
	queue.NewNull()       // drop-only / disabled mode

	sqlitequeue.New("file:queue.db?_busy_timeout=5000") // SQL durable queue (SQLite)
	mysqlqueue.New("user:pass@tcp(127.0.0.1:3306)/app") // SQL durable queue (MySQL)
	postgresqueue.New("postgres://user:pass@127.0.0.1:5432/app?sslmode=disable") // SQL durable queue (Postgres)

	redisqueue.New("127.0.0.1:6379")                           // Redis/Asynq
	natsqueue.New("nats://127.0.0.1:4222")                     // NATS
	sqsqueue.New("us-east-1")                                  // SQS
    rabbitmqqueue.New("amqp://guest:guest@127.0.0.1:5672/")    // RabbitMQ
}
```

## Benchmarks

Run local + integration-backed benchmarks (requires Docker/testcontainers):

```bash
INTEGRATION_BACKEND=all GOCACHE=/tmp/queue-gocache go test -tags=benchrender ./docs/bench -run '^TestRenderBenchmarks$'
```

<!-- bench:embed:start -->

### Latency (ns/op)

![Queue benchmark latency chart](docs/bench/benchmarks_ns.svg)

### Throughput (ops/s)

![Queue benchmark throughput chart](docs/bench/benchmarks_ops.svg)

### Allocated Bytes (B/op)

![Queue benchmark bytes chart](docs/bench/benchmarks_bytes.svg)

### Allocations (allocs/op)

![Queue benchmark allocations chart](docs/bench/benchmarks_allocs.svg)

### Tables

| Class | Driver | ns/op | ops/s | B/op | allocs/op |
|:------|:------|-----:|-----:|-----:|---------:|
| External | nats | 774 | 1291823 | 1258 | 13 |
| External | redis | 95295 | 10494 | 2113 | 33 |
| External | rabbitmq | 165780 | 6032 | 1882 | 57 |
| External | sqlite | 202380 | 4941 | 1931 | 47 |
| External | postgres | 1056731 | 946 | 3809 | 78 |
| External | sqs | 1873911 | 534 | 94784 | 1082 |
| External | mysql | 2286406 | 437 | 3303 | 62 |
| Local | null | 37 | 26673780 | 128 | 1 |
| Local | sync | 282 | 3539823 | 408 | 6 |
| Local | workerpool | 650 | 1538462 | 456 | 7 |

<!-- bench:embed:end -->

## Middleware

Use `queue.WithMiddleware(...)` to apply cross-cutting workflow behavior to workflow job execution (logging, filtering, and error policy).

Common patterns:
- wrap handler execution (before/after logging, timing, tracing)
- skip jobs conditionally (maintenance mode, feature flags)
- convert matched errors into terminal failures (no retry)

```go
var errValidation = errors.New("validation failed")
maintenanceMode := false

audit := queue.MiddlewareFunc(func(ctx context.Context, m queue.Message, next queue.Next) error {
	log.Printf("start job=%s", m.JobType)
	err := next(ctx, m)
	log.Printf("done job=%s err=%v", m.JobType, err)
	return err
})

skipMaintenance := queue.SkipWhen{
	Predicate: func(context.Context, queue.Message) bool {
		return maintenanceMode
	},
}

fatalValidation := queue.FailOnError{
	When: func(err error) bool {
		return errors.Is(err, errValidation)
	},
}

q, _ := queue.New(
	queue.Config{Driver: queue.DriverWorkerpool},
	queue.WithMiddleware(audit, skipMaintenance, fatalValidation),
)
_ = q
```

## Core Concepts

| Concept | Purpose | Primary API |
| --- | --- | --- |
| Job | Typed work unit for app handlers | `queue.NewJob`, `Dispatch` |
| Chain | Ordered workflow (A then B then C) | `Chain(...).Dispatch(...)` |
| Batch | Parallel workflow with callbacks | `Batch(...).Then/Catch/Finally` |
| Middleware | Cross-cutting execution policy | `Queue` middleware (`queue.WithMiddleware`) |
| Events | Lifecycle hooks and observability | queue runtime events (`queue.Observer`) + workflow events (advanced plumbing) |
| Backends | Driver/runtime transport | `queue.New(...)` and driver module `New(...)` constructors |

## Observability

Use `queue.Observer` implementations to capture normalized runtime events across drivers.

```go
collector := queue.NewStatsCollector()
observer := queue.MultiObserver(
    collector,
    queue.ObserverFunc(func(event queue.Event) {
        _ = event.Kind
    }),
)

q, _ := queue.New(queue.Config{
    Driver:   queue.DriverWorkerpool,
    Observer: observer,
})
_ = q
```

### Distributed counters and source of truth

- `StatsCollector` counters are process-local and event-driven.
- In multi-process deployments, aggregate metrics externally (OTel/Prometheus/etc.).
- Prefer backend-native stats when available.
- `queue.SupportsNativeStats(q)` indicates native driver snapshot support.
- `queue.Snapshot(ctx, q, collector)` merges native + collector where possible.

### Compose observers

```go
events := make(chan queue.Event, 100)
collector := queue.NewStatsCollector()
observer := queue.MultiObserver(
    collector,
    queue.ChannelObserver{
        Events:     events,
        DropIfFull: true,
    },
    queue.ObserverFunc(func(e queue.Event) {
        _ = e
    }),
)

q, _ := queue.New(queue.Config{
    Driver:   queue.DriverWorkerpool,
    Observer: observer,
})
_ = q
```

### Kitchen sink event logging (runtime + workflow)

Runnable example: `examples/observeall/main.go`

```go
logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
runtimeObserver := queue.ObserverFunc(func(event queue.Event) {
	attemptInfo := fmt.Sprintf("attempt=%d/%d", event.Attempt, event.MaxRetry+1)
	jobInfo := fmt.Sprintf("job=%s key=%s queue=%s driver=%s", event.JobType, event.JobKey, event.Queue, event.Driver)

	switch event.Kind {
	case queue.EventEnqueueAccepted:
		logger.Info("Accepted dispatch", "msg", fmt.Sprintf("Accepted %s", jobInfo), "scheduled", event.Scheduled, "at", event.Time.Format(time.RFC3339Nano))
	case queue.EventEnqueueRejected:
		logger.Error("Dispatch failed", "msg", fmt.Sprintf("Rejected %s", jobInfo), "error", event.Err)
	case queue.EventEnqueueDuplicate:
		logger.Warn("Skipped duplicate job", "msg", fmt.Sprintf("Duplicate %s", jobInfo))
	case queue.EventEnqueueCanceled:
		logger.Warn("Canceled dispatch", "msg", fmt.Sprintf("Canceled %s", jobInfo), "error", event.Err)
	case queue.EventProcessStarted:
		logger.Info("Started processing job", "msg", fmt.Sprintf("Started %s (%s)", jobInfo, attemptInfo), "at", event.Time.Format(time.RFC3339Nano))
	case queue.EventProcessSucceeded:
		logger.Info("Processed job", "msg", fmt.Sprintf("Processed %s in %s (%s)", jobInfo, event.Duration, attemptInfo))
	case queue.EventProcessFailed:
		logger.Error("Processing failed", "msg", fmt.Sprintf("Failed %s after %s (%s)", jobInfo, event.Duration, attemptInfo), "error", event.Err)
	case queue.EventProcessRetried:
		logger.Warn("Retrying job", "msg", fmt.Sprintf("Retry scheduled for %s (%s)", jobInfo, attemptInfo), "error", event.Err)
	case queue.EventProcessArchived:
		logger.Error("Archived failed job", "msg", fmt.Sprintf("Archived %s after final failure (%s)", jobInfo, attemptInfo), "error", event.Err)
	case queue.EventQueuePaused:
		logger.Info("Paused queue", "msg", fmt.Sprintf("Paused queue=%s driver=%s", event.Queue, event.Driver))
	case queue.EventQueueResumed:
		logger.Info("Resumed queue", "msg", fmt.Sprintf("Resumed queue=%s driver=%s", event.Queue, event.Driver))
	default:
		logger.Info("Queue event", "msg", fmt.Sprintf("kind=%s %s", event.Kind, jobInfo))
	}
})
workflowObserver := queue.WorkflowObserverFunc(func(event queue.WorkflowEvent) {
	logger.Info("workflow event",
		"kind", event.Kind,
		"dispatch_id", event.DispatchID,
		"job_id", event.JobID,
		"chain_id", event.ChainID,
		"batch_id", event.BatchID,
		"job_type", event.JobType,
		"queue", event.Queue,
		"attempt", event.Attempt,
		"duration", event.Duration,
		"err", event.Err,
	)
})

q, _ := queue.New(
	queue.Config{
		Driver:   queue.DriverSync,
		Observer: runtimeObserver,
	},
	queue.WithObserver(workflowObserver),
)
_ = q
```

### Events reference

| Type | EventKind | Meaning |
| ---: | --- | --- |
| **queue** | enqueue_accepted | Job accepted by driver for enqueue. |
| **queue** | enqueue_rejected | Job enqueue failed. |
| **queue** | enqueue_duplicate | Duplicate job rejected due to uniqueness key. |
| **queue** | enqueue_canceled | Context cancellation prevented enqueue. |
| **queue** | process_started | Worker began processing job. |
| **queue** | process_succeeded | Handler returned success. |
| **queue** | process_failed | Handler returned error. |
| **queue** | process_retried | Driver scheduled retry attempt. |
| **queue** | process_archived | Job moved to terminal failure state. |
| **queue** | queue_paused | Queue was paused (driver supports pause). |
| **queue** | queue_resumed | Queue was resumed. |
| **workflow** | dispatch_started | Workflow runtime accepted a dispatch request and created a dispatch record. |
| **workflow** | dispatch_succeeded | Dispatch was successfully enqueued to the underlying queue runtime. |
| **workflow** | dispatch_failed | Dispatch failed before job execution could start. |
| **workflow** | job_started | A workflow job handler started execution. |
| **workflow** | job_succeeded | A workflow job handler completed successfully. |
| **workflow** | job_failed | A workflow job handler returned an error. |
| **workflow** | chain_started | A chain workflow was created and started. |
| **workflow** | chain_advanced | Chain progressed from one node to the next node. |
| **workflow** | chain_completed | Chain reached terminal success. |
| **workflow** | chain_failed | Chain reached terminal failure. |
| **workflow** | batch_started | A batch workflow was created and started. |
| **workflow** | batch_progressed | Batch state changed as jobs completed/failed. |
| **workflow** | batch_completed | Batch reached terminal success (or allowed-failure completion). |
| **workflow** | batch_failed | Batch reached terminal failure. |
| **workflow** | batch_cancelled | Batch was cancelled before normal completion. |
| **workflow** | callback_started | Chain/batch callback execution started. |
| **workflow** | callback_succeeded | Chain/batch callback completed successfully. |
| **workflow** | callback_failed | Chain/batch callback returned an error. |

## Testing By Audience

### Running tests

Unit tests (root module):

```bash
go test ./...
```

Integration tests (separate `integration` module):

```bash
go test -tags=integration ./integration/...
```

Select specific backends with `INTEGRATION_BACKEND` (comma-separated), for example:

```bash
INTEGRATION_BACKEND=sqlite go test -tags=integration ./integration/...
INTEGRATION_BACKEND=redis,rabbitmq go test -tags=integration ./integration/... -count=1
INTEGRATION_BACKEND=all go test -tags=integration ./integration/... -count=1
```

### Application tests

Use `queue.NewFake()` to assert dispatch behavior in application tests.

```go
fake := queue.NewFake()
_ = fake.Dispatch(queue.NewJob("emails:send").OnQueue("default"))
fake.AssertDispatched(nil, "emails:send")
```

### Runtime/driver tests

Use the same `queue.NewFake()` helper when testing queue/job-level dispatch semantics.

```go
fake := queue.NewFake()
_ = fake.Dispatch(queue.NewJob("emails:send").OnQueue("default"))
fake.AssertDispatched(nil, "emails:send")
```

Matrix status and backend integration notes are tracked in `docs/integration-scenarios.md`.
## API reference

The API section below is autogenerated; do not edit between the markers.

<!-- api:embed:start -->

## API Index

| Group | Functions |
|------:|:-----------|
| **Constructors** | [New](#queue-new) [NewNull](#queue-newnull) [NewStatsCollector](#queue-newstatscollector) [NewSync](#queue-newsync) [NewWorkerpool](#queue-newworkerpool) |
| **Job** | [Backoff](#queue-job-backoff) [Bind](#queue-job-bind) [Delay](#queue-job-delay) [NewJob](#queue-newjob) [OnQueue](#queue-job-onqueue) [Payload](#queue-job-payload) [PayloadBytes](#queue-job-payloadbytes) [PayloadJSON](#queue-job-payloadjson) [Retry](#queue-job-retry) [Timeout](#queue-job-timeout) [UniqueFor](#queue-job-uniquefor) |
| **Observability** | [Active](#queue-statssnapshot-active) [Archived](#queue-statssnapshot-archived) [Failed](#queue-statssnapshot-failed) [MultiObserver](#queue-multiobserver) [ChannelObserver.Observe](#queue-channelobserver-observe) [Observer.Observe](#queue-observer-observe) [ObserverFunc.Observe](#queue-observerfunc-observe) [StatsCollector.Observe](#queue-statscollector-observe) [Pause](#queue-pause) [Paused](#queue-statssnapshot-paused) [Pending](#queue-statssnapshot-pending) [Processed](#queue-statssnapshot-processed) [Queue](#queue-statssnapshot-queue) [Queues](#queue-statssnapshot-queues) [Resume](#queue-resume) [RetryCount](#queue-statssnapshot-retrycount) [SafeObserve](#queue-safeobserve) [Scheduled](#queue-statssnapshot-scheduled) [Snapshot](#queue-snapshot) [StatsCollector.Snapshot](#queue-statscollector-snapshot) [SupportsNativeStats](#queue-supportsnativestats) [SupportsPause](#queue-supportspause) [Throughput](#queue-statssnapshot-throughput) |
| **Queue** | [Batch](#queue-queue-batch) [Chain](#queue-queue-chain) [Dispatch](#queue-queue-dispatch) [DispatchCtx](#queue-queue-dispatchctx) [Driver](#queue-queue-driver) [FindBatch](#queue-queue-findbatch) [FindChain](#queue-queue-findchain) [Pause](#queue-queue-pause) [Prune](#queue-queue-prune) [Register](#queue-queue-register) [Resume](#queue-queue-resume) [Run](#queue-queue-run) [Shutdown](#queue-queue-shutdown) [StartWorkers](#queue-queue-startworkers) [Stats](#queue-queue-stats) [WithClock](#queue-withclock) [WithMiddleware](#queue-withmiddleware) [WithObserver](#queue-withobserver) [WithStore](#queue-withstore) [WithWorkers](#queue-withworkers) [Queue.WithWorkers](#queue-queue-withworkers) |
| **Driver Constructors** | [mysqlqueue.New](#mysqlqueue-new) [mysqlqueue.NewWithConfig](#mysqlqueue-newwithconfig) [natsqueue.New](#natsqueue-new) [natsqueue.NewWithConfig](#natsqueue-newwithconfig) [postgresqueue.New](#postgresqueue-new) [postgresqueue.NewWithConfig](#postgresqueue-newwithconfig) [rabbitmqqueue.New](#rabbitmqqueue-new) [rabbitmqqueue.NewWithConfig](#rabbitmqqueue-newwithconfig) [redisqueue.New](#redisqueue-new) [redisqueue.NewWithConfig](#redisqueue-newwithconfig) [sqlitequeue.New](#sqlitequeue-new) [sqlitequeue.NewWithConfig](#sqlitequeue-newwithconfig) [sqsqueue.New](#sqsqueue-new) [sqsqueue.NewWithConfig](#sqsqueue-newwithconfig) |
| **Testing** | [AssertBatchCount](#queuefake-fake-assertbatchcount) [AssertBatched](#queuefake-fake-assertbatched) [AssertChained](#queuefake-fake-assertchained) [AssertCount](#queuefake-fake-assertcount) [AssertDispatched](#queuefake-fake-assertdispatched) [AssertDispatchedOn](#queuefake-fake-assertdispatchedon) [AssertDispatchedTimes](#queuefake-fake-assertdispatchedtimes) [AssertNotDispatched](#queuefake-fake-assertnotdispatched) [AssertNothingBatched](#queuefake-fake-assertnothingbatched) [AssertNothingDispatched](#queuefake-fake-assertnothingdispatched) [AssertNothingWorkflowDispatched](#queuefake-fake-assertnothingworkflowdispatched) [AssertWorkflowDispatched](#queuefake-fake-assertworkflowdispatched) [AssertWorkflowDispatchedOn](#queuefake-fake-assertworkflowdispatchedon) [AssertWorkflowDispatchedTimes](#queuefake-fake-assertworkflowdispatchedtimes) [AssertWorkflowNotDispatched](#queuefake-fake-assertworkflownotdispatched) [Count](#queuefake-fake-count) [CountJob](#queuefake-fake-countjob) [CountOn](#queuefake-fake-counton) [New](#queuefake-new) [Queue](#queuefake-fake-queue) [Records](#queuefake-fake-records) [Reset](#queuefake-fake-reset) [Workflow](#queuefake-fake-workflow) |



## API

#### Constructors

#### <a id="queue-new"></a>queue.New

New creates the high-level Queue API based on Config.Driver.

```go
q, err := queue.New(queue.Config{Driver: queue.DriverWorkerpool})
if err != nil {
	return
}
type EmailPayload struct {
	ID int `json:"id"`
}
q.Register("emails:send", func(ctx context.Context, m queue.Message) error {
	var payload EmailPayload
	if err := m.Bind(&payload); err != nil {
		return err
	}
	return nil
})
defer q.Shutdown(context.Background())
_, _ = q.Dispatch(
	queue.NewJob("emails:send").
		Payload(EmailPayload{ID: 1}).
		OnQueue("default"),
)
```

#### <a id="queue-newnull"></a>NewNull

NewNull creates a Queue on the null backend.

```go
q, err := queue.NewNull()
if err != nil {
	return
}
```

#### <a id="queue-newstatscollector"></a>NewStatsCollector

NewStatsCollector creates an event collector for queue counters.

```go
collector := queue.NewStatsCollector()
```

#### <a id="queue-newsync"></a>NewSync

NewSync creates a Queue on the synchronous in-process backend.

```go
q, err := queue.NewSync()
if err != nil {
	return
}
```

#### <a id="queue-newworkerpool"></a>NewWorkerpool

NewWorkerpool creates a Queue on the in-process workerpool backend.

```go
q, err := queue.NewWorkerpool()
if err != nil {
	return
}
```

#### Job

#### <a id="queue-job-backoff"></a>Backoff

Backoff sets delay between retries.

```go
job := queue.NewJob("emails:send").Backoff(500 * time.Millisecond)
```

#### <a id="queue-job-bind"></a>Bind

Bind unmarshals job payload JSON into dst.

```go
type EmailPayload struct {
	ID int    `json:"id"`
	To string `json:"to"`
}
job := queue.NewJob("emails:send").Payload(EmailPayload{
	ID: 1,
	To: "user@example.com",
})
var payload EmailPayload
if err := job.Bind(&payload); err != nil {
	return
}
```

#### <a id="queue-job-delay"></a>Delay

Delay defers execution by duration.

```go
job := queue.NewJob("emails:send").Delay(300 * time.Millisecond)
```

#### <a id="queue-newjob"></a>NewJob

NewJob creates a job value with a required job type.

```go
job := queue.NewJob("emails:send")
```

#### <a id="queue-job-onqueue"></a>OnQueue

OnQueue sets the target queue name.

```go
job := queue.NewJob("emails:send").OnQueue("critical")
```

#### <a id="queue-job-payload"></a>Payload

Payload sets job payload from common value types.

_Example: payload bytes_

```go
jobBytes := queue.NewJob("emails:send").Payload([]byte(`{"id":1}`))
```

_Example: payload struct_

```go
type Meta struct {
	Nested bool `json:"nested"`
}
type EmailPayload struct {
	ID   int    `json:"id"`
	To   string `json:"to"`
	Meta Meta   `json:"meta"`
}
jobStruct := queue.NewJob("emails:send").Payload(EmailPayload{
	ID:   1,
	To:   "user@example.com",
	Meta: Meta{Nested: true},
})
```

_Example: payload map_

```go
jobMap := queue.NewJob("emails:send").Payload(map[string]any{
	"id":  1,
	"to":  "user@example.com",
	"meta": map[string]any{"nested": true},
})
```

#### <a id="queue-job-payloadbytes"></a>PayloadBytes

PayloadBytes returns a copy of job payload bytes.

```go
job := queue.NewJob("emails:send").Payload([]byte(`{"id":1}`))
payload := job.PayloadBytes()
```

#### <a id="queue-job-payloadjson"></a>PayloadJSON

PayloadJSON marshals payload as JSON.

```go
job := queue.NewJob("emails:send").PayloadJSON(map[string]int{"id": 1})
```

#### <a id="queue-job-retry"></a>Retry

Retry sets max retry attempts.

```go
job := queue.NewJob("emails:send").Retry(4)
```

#### <a id="queue-job-timeout"></a>Timeout

Timeout sets per-job execution timeout.

```go
job := queue.NewJob("emails:send").Timeout(10 * time.Second)
```

#### <a id="queue-job-uniquefor"></a>UniqueFor

UniqueFor enables uniqueness dedupe within the given TTL.

```go
job := queue.NewJob("emails:send").UniqueFor(45 * time.Second)
```

#### Observability

#### <a id="queue-statssnapshot-active"></a>Active

Active returns active count for a queue.

```go
snapshot := queue.StatsSnapshot{
	ByQueue: map[string]queue.QueueCounters{
		"default": {Active: 2},
	},
}
fmt.Println(snapshot.Active("default"))
// Output: 2
```

#### <a id="queue-statssnapshot-archived"></a>Archived

Archived returns archived count for a queue.

```go
snapshot := queue.StatsSnapshot{
	ByQueue: map[string]queue.QueueCounters{
		"default": {Archived: 7},
	},
}
fmt.Println(snapshot.Archived("default"))
// Output: 7
```

#### <a id="queue-statssnapshot-failed"></a>Failed

Failed returns failed count for a queue.

```go
snapshot := queue.StatsSnapshot{
	ByQueue: map[string]queue.QueueCounters{
		"default": {Failed: 2},
	},
}
fmt.Println(snapshot.Failed("default"))
// Output: 2
```

#### <a id="queue-multiobserver"></a>MultiObserver

MultiObserver fans out events to multiple observers.

```go
events := make(chan queue.Event, 2)
observer := queue.MultiObserver(
	queue.ChannelObserver{Events: events},
	queue.ObserverFunc(func(queue.Event) {}),
)
observer.Observe(queue.Event{Kind: queue.EventEnqueueAccepted})
fmt.Println(len(events))
// Output: 1
```

#### <a id="queue-channelobserver-observe"></a>ChannelObserver.Observe

Observe forwards an event to the configured channel.

```go
ch := make(chan queue.Event, 1)
observer := queue.ChannelObserver{Events: ch}
observer.Observe(queue.Event{Kind: queue.EventProcessStarted, Queue: "default"})
event := <-ch
```

#### <a id="queue-observer-observe"></a>Observer.Observe

Observe handles a queue runtime event.

```go
var observer queue.Observer
observer.Observe(queue.Event{
	Kind:   queue.EventEnqueueAccepted,
	Driver: queue.DriverSync,
	Queue:  "default",
})
```

#### <a id="queue-observerfunc-observe"></a>ObserverFunc.Observe

Observe calls the wrapped function.

```go
logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
observer := queue.ObserverFunc(func(event queue.Event) {
	logger.Info("queue event",
		"kind", event.Kind,
		"driver", event.Driver,
		"queue", event.Queue,
		"job_type", event.JobType,
		"attempt", event.Attempt,
		"max_retry", event.MaxRetry,
		"duration", event.Duration,
		"err", event.Err,
	)
})
observer.Observe(queue.Event{
	Kind:     queue.EventProcessSucceeded,
	Driver:   queue.DriverSync,
	Queue:    "default",
	JobType: "emails:send",
})
```

#### <a id="queue-statscollector-observe"></a>StatsCollector.Observe

Observe records an event and updates normalized counters.

```go
collector := queue.NewStatsCollector()
collector.Observe(queue.Event{
	Kind:   queue.EventEnqueueAccepted,
	Driver: queue.DriverSync,
	Queue:  "default",
	Time:   time.Now(),
})
```

#### <a id="queue-pause"></a>Pause

Pause pauses queue consumption for drivers that support it.

```go
q, _ := queue.NewSync()
snapshot, _ := queue.Snapshot(context.Background(), q, nil)
fmt.Println(snapshot.Paused("default"))
// Output: 1
```

#### <a id="queue-statssnapshot-paused"></a>Paused

Paused returns paused count for a queue.

```go
collector := queue.NewStatsCollector()
collector.Observe(queue.Event{
	Kind:   queue.EventQueuePaused,
	Driver: queue.DriverSync,
	Queue:  "default",
	Time:   time.Now(),
})
snapshot := collector.Snapshot()
fmt.Println(snapshot.Paused("default"))
// Output: 1
```

#### <a id="queue-statssnapshot-pending"></a>Pending

Pending returns pending count for a queue.

```go
snapshot := queue.StatsSnapshot{
	ByQueue: map[string]queue.QueueCounters{
		"default": {Pending: 3},
	},
}
fmt.Println(snapshot.Pending("default"))
// Output: 3
```

#### <a id="queue-statssnapshot-processed"></a>Processed

Processed returns processed count for a queue.

```go
snapshot := queue.StatsSnapshot{
	ByQueue: map[string]queue.QueueCounters{
		"default": {Processed: 11},
	},
}
fmt.Println(snapshot.Processed("default"))
// Output: 11
```

#### <a id="queue-statssnapshot-queue"></a>StatsSnapshot.Queue

Queue returns queue counters for a queue name.

```go
collector := queue.NewStatsCollector()
collector.Observe(queue.Event{
	Kind:   queue.EventEnqueueAccepted,
	Driver: queue.DriverSync,
	Queue:  "default",
	Time:   time.Now(),
})
snapshot := collector.Snapshot()
counters, ok := snapshot.Queue("default")
fmt.Println(ok, counters.Pending)
// Output: true 1
```

#### <a id="queue-statssnapshot-queues"></a>Queues

Queues returns sorted queue names present in the snapshot.

```go
collector := queue.NewStatsCollector()
collector.Observe(queue.Event{
	Kind:   queue.EventEnqueueAccepted,
	Driver: queue.DriverSync,
	Queue:  "critical",
	Time:   time.Now(),
})
snapshot := collector.Snapshot()
names := snapshot.Queues()
fmt.Println(len(names), names[0])
// Output: 1 critical
```

#### <a id="queue-resume"></a>Resume

Resume resumes queue consumption for drivers that support it.

```go
q, _ := queue.NewSync()
snapshot, _ := queue.Snapshot(context.Background(), q, nil)
fmt.Println(snapshot.Paused("default"))
// Output: 0
```

#### <a id="queue-statssnapshot-retrycount"></a>RetryCount

RetryCount returns retry count for a queue.

```go
snapshot := queue.StatsSnapshot{
	ByQueue: map[string]queue.QueueCounters{
		"default": {Retry: 1},
	},
}
fmt.Println(snapshot.RetryCount("default"))
// Output: 1
```

#### <a id="queue-safeobserve"></a>SafeObserve

SafeObserve delivers an event to an observer and recovers observer panics.

This is an advanced helper intended for driver-module implementations.

#### <a id="queue-statssnapshot-scheduled"></a>Scheduled

Scheduled returns scheduled count for a queue.

```go
snapshot := queue.StatsSnapshot{
	ByQueue: map[string]queue.QueueCounters{
		"default": {Scheduled: 4},
	},
}
fmt.Println(snapshot.Scheduled("default"))
// Output: 4
```

#### <a id="queue-snapshot"></a>Snapshot

Snapshot returns driver-native stats, falling back to collector data.

```go
q, _ := queue.NewSync()
snapshot, _ := q.Stats(context.Background())
_, ok := snapshot.Queue("default")
fmt.Println(ok)
// Output: true
```

#### <a id="queue-statscollector-snapshot"></a>StatsCollector.Snapshot

Snapshot returns a copy of collected counters.

```go
collector := queue.NewStatsCollector()
collector.Observe(queue.Event{
	Kind:   queue.EventEnqueueAccepted,
	Driver: queue.DriverSync,
	Queue:  "default",
	Time:   time.Now(),
})
collector.Observe(queue.Event{
	Kind:   queue.EventProcessStarted,
	Driver: queue.DriverSync,
	Queue:  "default",
	JobKey: "job-1",
	Time:   time.Now(),
})
collector.Observe(queue.Event{
	Kind:     queue.EventProcessSucceeded,
	Driver:   queue.DriverSync,
	Queue:    "default",
	JobKey:  "job-1",
	Duration: 12 * time.Millisecond,
	Time:     time.Now(),
})
snapshot := collector.Snapshot()
counters, _ := snapshot.Queue("default")
throughput, _ := snapshot.Throughput("default")
fmt.Printf("queues=%v\n", snapshot.Queues())
fmt.Printf("counters=%+v\n", counters)
fmt.Printf("hour=%+v\n", throughput.Hour)
// Output:
// queues=[default]
// counters={Pending:0 Active:0 Scheduled:0 Retry:0 Archived:0 Processed:1 Failed:0 Paused:0 AvgWait:0s AvgRun:12ms}
// hour={Processed:1 Failed:0}
```

#### <a id="queue-supportsnativestats"></a>SupportsNativeStats

SupportsNativeStats reports whether a queue runtime exposes native stats snapshots.

```go
q, _ := queue.NewSync()
fmt.Println(queue.SupportsNativeStats(q))
// Output: true
```

#### <a id="queue-supportspause"></a>SupportsPause

SupportsPause reports whether a queue runtime supports Pause/Resume.

```go
q, _ := queue.NewSync()
fmt.Println(queue.SupportsPause(q))
// Output: true
```

#### <a id="queue-statssnapshot-throughput"></a>Throughput

Throughput returns rolling throughput windows for a queue name.

```go
collector := queue.NewStatsCollector()
collector.Observe(queue.Event{
	Kind:   queue.EventProcessSucceeded,
	Driver: queue.DriverSync,
	Queue:  "default",
	Time:   time.Now(),
})
snapshot := collector.Snapshot()
throughput, ok := snapshot.Throughput("default")
fmt.Printf("ok=%v hour=%+v day=%+v week=%+v\n", ok, throughput.Hour, throughput.Day, throughput.Week)
// Output: ok=true hour={Processed:1 Failed:0} day={Processed:1 Failed:0} week={Processed:1 Failed:0}
```

#### Queue

#### <a id="queue-queue-batch"></a>Batch

Batch creates a batch builder for fan-out workflow execution.

```go
q, err := queue.NewSync()
if err != nil {
	return
}
q.Register("emails:send", func(ctx context.Context, m queue.Message) error { return nil })
_, _ = q.Batch(
	queue.NewJob("emails:send").Payload(map[string]any{"id": 1}),
	queue.NewJob("emails:send").Payload(map[string]any{"id": 2}),
).Name("send-emails").OnQueue("default").Dispatch(context.Background())
```

#### <a id="queue-queue-chain"></a>Chain

Chain creates a chain builder for sequential workflow execution.

```go
q, err := queue.NewSync()
if err != nil {
	return
}
q.Register("first", func(ctx context.Context, m queue.Message) error { return nil })
q.Register("second", func(ctx context.Context, m queue.Message) error { return nil })
_, _ = q.Chain(
	queue.NewJob("first"),
	queue.NewJob("second"),
).OnQueue("default").Dispatch(context.Background())
```

#### <a id="queue-queue-dispatch"></a>Queue.Dispatch

Dispatch enqueues a high-level job using context.Background.

```go
q, err := queue.NewSync()
if err != nil {
	return
}
q.Register("emails:send", func(ctx context.Context, m queue.Message) error { return nil })
job := queue.NewJob("emails:send").Payload(map[string]any{"id": 1}).OnQueue("default")
_, _ = q.Dispatch(job)
```

#### <a id="queue-queue-dispatchctx"></a>Queue.DispatchCtx

DispatchCtx enqueues a high-level job using the provided context.

#### <a id="queue-queue-driver"></a>Queue.Driver

Driver reports the configured backend driver for the underlying queue runtime.

```go
q, err := queue.NewSync()
if err != nil {
	return
}
fmt.Println(q.Driver())
// Output: sync
```

#### <a id="queue-queue-findbatch"></a>FindBatch

FindBatch returns current batch state by ID.

```go
q, err := queue.NewSync()
if err != nil {
	return
}
q.Register("emails:send", func(ctx context.Context, m queue.Message) error { return nil })
batchID, err := q.Batch(queue.NewJob("emails:send")).Dispatch(context.Background())
if err != nil {
	return
}
_, _ = q.FindBatch(context.Background(), batchID)
```

#### <a id="queue-queue-findchain"></a>FindChain

FindChain returns current chain state by ID.

```go
q, err := queue.NewSync()
if err != nil {
	return
}
q.Register("first", func(ctx context.Context, m queue.Message) error { return nil })
chainID, err := q.Chain(queue.NewJob("first")).Dispatch(context.Background())
if err != nil {
	return
}
_, _ = q.FindChain(context.Background(), chainID)
```

#### <a id="queue-queue-pause"></a>Queue.Pause

Pause pauses consumption for a queue when supported by the underlying driver.
See the README "Queue Backends" table for Pause/Resume support and
docs/backend-guarantees.md (Capability Matrix) for broader backend differences.

```go
q, err := queue.NewSync()
if err != nil {
	return
}
if queue.SupportsPause(q) {
}
```

#### <a id="queue-queue-prune"></a>Prune

Prune deletes old workflow state records.

```go
q, err := queue.NewSync()
if err != nil {
	return
}
```

#### <a id="queue-queue-register"></a>Queue.Register

Register binds a handler for a high-level job type.

```go
q, err := queue.NewSync()
if err != nil {
	return
}
type EmailPayload struct {
	ID int `json:"id"`
}
q.Register("emails:send", func(ctx context.Context, m queue.Message) error {
	var payload EmailPayload
	if err := m.Bind(&payload); err != nil {
		return err
	}
	return nil
})
```

#### <a id="queue-queue-resume"></a>Queue.Resume

Resume resumes consumption for a queue when supported by the underlying driver.

```go
q, err := queue.NewSync()
if err != nil {
	return
}
if queue.SupportsPause(q) {
}
```

#### <a id="queue-queue-run"></a>Run

Run starts worker processing, blocks until ctx is canceled, then gracefully shuts down.

```go
ctx, cancel := context.WithCancel(context.Background())
defer cancel()
q, err := queue.NewWorkerpool()
if err != nil {
	return
}
q.Register("emails:send", func(ctx context.Context, m queue.Message) error { return nil })
go func() {
	time.Sleep(100 * time.Millisecond)
	cancel()
}()
```

#### <a id="queue-queue-shutdown"></a>Queue.Shutdown

Shutdown drains workers and closes underlying resources.

```go
q, err := queue.NewWorkerpool()
if err != nil {
	return
}
```

#### <a id="queue-queue-startworkers"></a>Queue.StartWorkers

StartWorkers starts worker processing.

```go
q, err := queue.NewWorkerpool()
if err != nil {
	return
}
```

#### <a id="queue-queue-stats"></a>Stats

Stats returns a normalized snapshot when supported by the underlying driver.

```go
q, err := queue.NewSync()
if err != nil {
	return
}
if queue.SupportsNativeStats(q) {
	_, _ = q.Stats(context.Background())
}
```

#### <a id="queue-withclock"></a>WithClock

WithClock overrides the workflow runtime clock.

```go
q, err := queue.New(
	queue.Config{Driver: queue.DriverSync},
	queue.WithClock(func() time.Time { return time.Unix(0, 0) }),
)
if err != nil {
	return
}
```

#### <a id="queue-withmiddleware"></a>WithMiddleware

WithMiddleware appends queue workflow middleware.

```go
mw := queue.MiddlewareFunc(func(ctx context.Context, m queue.Message, next queue.Next) error {
	return next(ctx, m)
})
q, err := queue.New(queue.Config{Driver: queue.DriverSync}, queue.WithMiddleware(mw))
if err != nil {
	return
}
```

#### <a id="queue-withobserver"></a>WithObserver

WithObserver installs a workflow lifecycle observer.

```go
observer := queue.WorkflowObserverFunc(func(event queue.WorkflowEvent) {
})
q, err := queue.New(queue.Config{Driver: queue.DriverSync}, queue.WithObserver(observer))
if err != nil {
	return
}
```

#### <a id="queue-withstore"></a>WithStore

WithStore overrides the workflow orchestration store.

```go
var store queue.WorkflowStore
q, err := queue.New(queue.Config{Driver: queue.DriverSync}, queue.WithStore(store))
if err != nil {
	return
}
```

#### <a id="queue-withworkers"></a>WithWorkers

WithWorkers sets desired worker concurrency before StartWorkers.
It applies to high-level queue constructors (for example NewWorkerpool/New/NewSync).

```go
q, err := queue.NewWorkerpool(
	queue.WithWorkers(4), // optional; default: runtime.NumCPU() (min 1)
)
if err != nil {
	return
}
```

#### <a id="queue-queue-withworkers"></a>Queue.WithWorkers

WithWorkers sets desired worker concurrency before StartWorkers.

```go
q, err := queue.NewWorkerpool()
if err != nil {
	return
}
q.WithWorkers(4) // optional; default: runtime.NumCPU() (min 1)
```

#### Testing

#### <a id="queue-fakequeue-assertcount"></a>FakeQueue.AssertCount

AssertCount fails when dispatch count is not expected.

```go
fake := queue.NewFake()
fake.AssertCount(nil, 1)
```

#### <a id="queue-fakequeue-assertdispatched"></a>FakeQueue.AssertDispatched

AssertDispatched fails when jobType was not dispatched.

```go
fake := queue.NewFake()
fake.AssertDispatched(nil, "emails:send")
```

#### <a id="queue-fakequeue-assertdispatchedon"></a>FakeQueue.AssertDispatchedOn

AssertDispatchedOn fails when jobType was not dispatched on queueName.

```go
fake := queue.NewFake()
	queue.NewJob("emails:send").
		OnQueue("critical"),
)
fake.AssertDispatchedOn(nil, "critical", "emails:send")
```

#### <a id="queue-fakequeue-assertdispatchedtimes"></a>FakeQueue.AssertDispatchedTimes

AssertDispatchedTimes fails when jobType dispatch count does not match expected.

```go
fake := queue.NewFake()
fake.AssertDispatchedTimes(nil, "emails:send", 2)
```

#### <a id="queue-fakequeue-assertnotdispatched"></a>FakeQueue.AssertNotDispatched

AssertNotDispatched fails when jobType was dispatched.

```go
fake := queue.NewFake()
fake.AssertNotDispatched(nil, "emails:cancel")
```

#### <a id="queue-fakequeue-assertnothingdispatched"></a>FakeQueue.AssertNothingDispatched

AssertNothingDispatched fails when any dispatch was recorded.

```go
fake := queue.NewFake()
fake.AssertNothingDispatched(nil)
```

#### <a id="queue-fakequeue-dispatch"></a>FakeQueue.Dispatch

Dispatch records a typed job payload in-memory using the fake default queue.

```go
fake := queue.NewFake()
err := fake.Dispatch(queue.NewJob("emails:send").OnQueue("default"))
```

#### <a id="queue-fakequeue-dispatchctx"></a>FakeQueue.DispatchCtx

DispatchCtx submits a typed job payload using the provided context.

```go
fake := queue.NewFake()
ctx := context.Background()
err := fake.DispatchCtx(ctx, queue.NewJob("emails:send").OnQueue("default"))
fmt.Println(err == nil)
// Output: true
```

#### <a id="queue-fakequeue-driver"></a>FakeQueue.Driver

Driver returns the active queue driver.

```go
fake := queue.NewFake()
driver := fake.Driver()
```

#### <a id="queue-newfake"></a>NewFake

NewFake creates a queue fake that records dispatches and provides assertions.

```go
fake := queue.NewFake()
	queue.NewJob("emails:send").
		Payload(map[string]any{"id": 1}).
		OnQueue("critical"),
)
records := fake.Records()
fmt.Println(len(records), records[0].Queue, records[0].Job.Type)
// Output: 1 critical emails:send
```

#### <a id="queue-fakequeue-records"></a>FakeQueue.Records

Records returns a copy of all dispatch records.

```go
fake := queue.NewFake()
records := fake.Records()
fmt.Println(len(records), records[0].Job.Type)
// Output: 1 emails:send
```

#### <a id="queue-fakequeue-register"></a>FakeQueue.Register

Register associates a handler with a job type.

```go
fake := queue.NewFake()
fake.Register("emails:send", func(context.Context, queue.Job) error { return nil })
```

#### <a id="queue-fakequeue-reset"></a>FakeQueue.Reset

Reset clears all recorded dispatches.

```go
fake := queue.NewFake()
fmt.Println(len(fake.Records()))
fake.Reset()
fmt.Println(len(fake.Records()))
// Output:
// 1
// 0
```

#### <a id="queue-fakequeue-shutdown"></a>FakeQueue.Shutdown

Shutdown drains running work and releases resources.

```go
fake := queue.NewFake()
err := fake.Shutdown(context.Background())
```

#### <a id="queue-fakequeue-startworkers"></a>FakeQueue.StartWorkers

StartWorkers starts worker execution.

```go
fake := queue.NewFake()
err := fake.StartWorkers(context.Background())
```

#### <a id="queue-fakequeue-workers"></a>FakeQueue.Workers

Workers sets desired worker concurrency before StartWorkers.

```go
fake := queue.NewFake()
q := fake.Workers(4)
fmt.Println(q != nil)
// Output: true
```


## Driver Constructors

### mysqlqueue

#### <a id="mysqlqueue-new"></a>mysqlqueue.New

New creates a high-level Queue using the MySQL SQL backend.

```go
q, err := mysqlqueue.New(
	"user:pass@tcp(127.0.0.1:3306)/queue?parseTime=true",
	queue.WithWorkers(4), // optional; default: 1 worker
)
if err != nil {
	return
}
```

#### <a id="mysqlqueue-newwithconfig"></a>mysqlqueue.NewWithConfig

NewWithConfig creates a high-level Queue using an explicit MySQL SQL driver config.

```go
q, err := mysqlqueue.NewWithConfig(
	mysqlqueue.Config{
		DriverBaseConfig: queueconfig.DriverBaseConfig{
			DefaultQueue: "critical", // default if empty: "default"
			Observer:     nil,        // default: nil
		},
		DB: nil, // optional; provide *sql.DB instead of DSN
		DSN: "user:pass@tcp(127.0.0.1:3306)/queue?parseTime=true", // optional if DB is set
		ProcessingRecoveryGrace:  2 * time.Second, // default if <=0: 2s
		ProcessingLeaseNoTimeout: 5 * time.Minute, // default if <=0: 5m
	},
	queue.WithWorkers(4), // optional; default: 1 worker
)
if err != nil {
	return
}
```


### natsqueue

#### <a id="natsqueue-new"></a>natsqueue.New

New creates a high-level Queue using the NATS backend.

```go
q, err := natsqueue.New(
	"nats://127.0.0.1:4222",
	queue.WithWorkers(4), // optional; default: runtime.NumCPU() (min 1)
)
if err != nil {
	return
}
```

#### <a id="natsqueue-newwithconfig"></a>natsqueue.NewWithConfig

NewWithConfig creates a high-level Queue using an explicit NATS driver config.

```go
q, err := natsqueue.NewWithConfig(
	natsqueue.Config{
		DriverBaseConfig: queueconfig.DriverBaseConfig{
			DefaultQueue: "critical", // default if empty: "default"
			Observer:     nil,        // default: nil
		},
		URL: "nats://127.0.0.1:4222", // required
	},
	queue.WithWorkers(4), // optional; default: runtime.NumCPU() (min 1)
)
if err != nil {
	return
}
```


### postgresqueue

#### <a id="postgresqueue-new"></a>postgresqueue.New

New creates a high-level Queue using the Postgres SQL backend.

```go
q, err := postgresqueue.New(
	"postgres://user:pass@127.0.0.1:5432/queue?sslmode=disable",
	queue.WithWorkers(4), // optional; default: 1 worker
)
if err != nil {
	return
}
```

#### <a id="postgresqueue-newwithconfig"></a>postgresqueue.NewWithConfig

NewWithConfig creates a high-level Queue using an explicit Postgres SQL driver config.

```go
q, err := postgresqueue.NewWithConfig(
	postgresqueue.Config{
		DriverBaseConfig: queueconfig.DriverBaseConfig{
			DefaultQueue: "critical", // default if empty: "default"
			Observer:     nil,        // default: nil
		},
		DB: nil, // optional; provide *sql.DB instead of DSN
		DSN: "postgres://user:pass@127.0.0.1:5432/queue?sslmode=disable", // optional if DB is set
		ProcessingRecoveryGrace:  2 * time.Second, // default if <=0: 2s
		ProcessingLeaseNoTimeout: 5 * time.Minute, // default if <=0: 5m
	},
	queue.WithWorkers(4), // optional; default: 1 worker
)
if err != nil {
	return
}
```


### rabbitmqqueue

#### <a id="rabbitmqqueue-new"></a>rabbitmqqueue.New

New creates a high-level Queue using the RabbitMQ backend.

```go
q, err := rabbitmqqueue.New(
	"amqp://guest:guest@127.0.0.1:5672/",
	queue.WithWorkers(4), // optional; default: runtime.NumCPU() (min 1)
)
if err != nil {
	return
}
```

#### <a id="rabbitmqqueue-newwithconfig"></a>rabbitmqqueue.NewWithConfig

NewWithConfig creates a high-level Queue using an explicit RabbitMQ driver config.

```go
q, err := rabbitmqqueue.NewWithConfig(
	rabbitmqqueue.Config{
		DriverBaseConfig: queueconfig.DriverBaseConfig{
			DefaultQueue: "critical", // default if empty: "default"
			Observer:     nil,        // default: nil
		},
		URL: "amqp://guest:guest@127.0.0.1:5672/", // required
	},
	queue.WithWorkers(4), // optional; default: runtime.NumCPU() (min 1)
)
if err != nil {
	return
}
```


### redisqueue

#### <a id="redisqueue-new"></a>redisqueue.New

New creates a high-level Queue using the Redis backend.

```go
q, err := redisqueue.New(
	"127.0.0.1:6379",
	queue.WithWorkers(4), // optional; default: runtime.NumCPU() (min 1)
)
if err != nil {
	return
}
```

#### <a id="redisqueue-newwithconfig"></a>redisqueue.NewWithConfig

NewWithConfig creates a high-level Queue using an explicit Redis driver config.

```go
q, err := redisqueue.NewWithConfig(
	redisqueue.Config{
		DriverBaseConfig: queueconfig.DriverBaseConfig{
			DefaultQueue: "critical", // default if empty: "default"
			Observer:     nil,        // default: nil
		},
		Addr: "127.0.0.1:6379", // required
		Password: "",           // optional; default empty
		DB: 0,                  // optional; default 0
	},
	queue.WithWorkers(4), // optional; default: runtime.NumCPU() (min 1)
)
if err != nil {
	return
}
```


### sqlitequeue

#### <a id="sqlitequeue-new"></a>sqlitequeue.New

New creates a high-level Queue using the SQLite SQL backend.

```go
q, err := sqlitequeue.New(
	"file:queue.db?_busy_timeout=5000",
	queue.WithWorkers(4), // optional; default: 1 worker
)
if err != nil {
	return
}
```

#### <a id="sqlitequeue-newwithconfig"></a>sqlitequeue.NewWithConfig

NewWithConfig creates a high-level Queue using an explicit SQLite SQL driver config.

```go
q, err := sqlitequeue.NewWithConfig(
	sqlitequeue.Config{
		DriverBaseConfig: queueconfig.DriverBaseConfig{
			DefaultQueue: "critical", // default if empty: "default"
			Observer:     nil,        // default: nil
		},
		DB: nil, // optional; provide *sql.DB instead of DSN
		DSN: "file:queue.db?_busy_timeout=5000", // optional if DB is set
		ProcessingRecoveryGrace:  2 * time.Second, // default if <=0: 2s
		ProcessingLeaseNoTimeout: 5 * time.Minute, // default if <=0: 5m
	},
	queue.WithWorkers(4), // optional; default: 1 worker
)
if err != nil {
	return
}
```


### sqsqueue

#### <a id="sqsqueue-new"></a>sqsqueue.New

New creates a high-level Queue using the SQS backend.

```go
q, err := sqsqueue.New(
	"us-east-1",
	queue.WithWorkers(4), // optional; default: runtime.NumCPU() (min 1)
)
if err != nil {
	return
}
```

#### <a id="sqsqueue-newwithconfig"></a>sqsqueue.NewWithConfig

NewWithConfig creates a high-level Queue using an explicit SQS driver config.

```go
q, err := sqsqueue.NewWithConfig(
	sqsqueue.Config{
		DriverBaseConfig: queueconfig.DriverBaseConfig{
			DefaultQueue: "critical", // default if empty: "default"
			Observer:     nil,        // default: nil
		},
		Region: "us-east-1", // default if empty: "us-east-1"
		Endpoint: "",        // optional; set for LocalStack/custom endpoint
		AccessKey: "",       // optional; static credentials
		SecretKey: "",       // optional; static credentials
	},
	queue.WithWorkers(4), // optional; default: runtime.NumCPU() (min 1)
)
if err != nil {
	return
}
```


## Testing API

#### <a id="queuefake-fake-assertbatchcount"></a>Fake.AssertBatchCount

AssertBatchCount fails if total recorded workflow batch count does not match n.

```go
f := queuefake.New()
_, _ = f.Workflow().Batch(bus.NewJob("a", nil)).Dispatch(nil)
f.AssertBatchCount(nil, 1)
```

#### <a id="queuefake-fake-assertbatched"></a>Fake.AssertBatched

AssertBatched fails unless at least one recorded workflow batch matches predicate.

```go
f := queuefake.New()
_, _ = f.Workflow().Batch(bus.NewJob("a", nil), bus.NewJob("b", nil)).Dispatch(nil)
f.AssertBatched(nil, func(spec bus.BatchSpec) bool { return len(spec.Jobs) == 2 })
```

#### <a id="queuefake-fake-assertchained"></a>Fake.AssertChained

AssertChained fails if no recorded workflow chain matches expected job type order.

```go
f := queuefake.New()
_, _ = f.Workflow().Chain(bus.NewJob("a", nil), bus.NewJob("b", nil)).Dispatch(nil)
f.AssertChained(nil, []string{"a", "b"})
```

#### <a id="queuefake-fake-assertcount"></a>Fake.AssertCount

AssertCount fails when total dispatch count is not expected.

```go
f := queuefake.New()
q := f.Queue()
f.AssertCount(nil, 2)
```

#### <a id="queuefake-fake-assertdispatched"></a>Fake.AssertDispatched

AssertDispatched fails when jobType was not dispatched.

```go
f := queuefake.New()
f.AssertDispatched(nil, "emails:send")
```

#### <a id="queuefake-fake-assertdispatchedon"></a>Fake.AssertDispatchedOn

AssertDispatchedOn fails when jobType was not dispatched on queueName.

```go
f := queuefake.New()
f.AssertDispatchedOn(nil, "critical", "emails:send")
```

#### <a id="queuefake-fake-assertdispatchedtimes"></a>Fake.AssertDispatchedTimes

AssertDispatchedTimes fails when jobType dispatch count does not match expected.

```go
f := queuefake.New()
q := f.Queue()
f.AssertDispatchedTimes(nil, "emails:send", 2)
```

#### <a id="queuefake-fake-assertnotdispatched"></a>Fake.AssertNotDispatched

AssertNotDispatched fails when jobType was dispatched.

```go
f := queuefake.New()
f.AssertNotDispatched(nil, "emails:send")
```

#### <a id="queuefake-fake-assertnothingbatched"></a>Fake.AssertNothingBatched

AssertNothingBatched fails if any workflow batch was recorded.

```go
f := queuefake.New()
f.AssertNothingBatched(nil)
```

#### <a id="queuefake-fake-assertnothingdispatched"></a>Fake.AssertNothingDispatched

AssertNothingDispatched fails when any dispatch was recorded.

```go
f := queuefake.New()
f.AssertNothingDispatched(nil)
```

#### <a id="queuefake-fake-assertnothingworkflowdispatched"></a>Fake.AssertNothingWorkflowDispatched

AssertNothingWorkflowDispatched fails when any workflow dispatch was recorded.

```go
f := queuefake.New()
f.AssertNothingWorkflowDispatched(nil)
```

#### <a id="queuefake-fake-assertworkflowdispatched"></a>Fake.AssertWorkflowDispatched

AssertWorkflowDispatched fails when jobType was not workflow-dispatched.

```go
f := queuefake.New()
_, _ = f.Workflow().Chain(bus.NewJob("a", nil)).Dispatch(nil)
f.AssertWorkflowDispatched(nil, "a")
```

#### <a id="queuefake-fake-assertworkflowdispatchedon"></a>Fake.AssertWorkflowDispatchedOn

AssertWorkflowDispatchedOn fails when jobType was not workflow-dispatched on queueName.

```go
f := queuefake.New()
_, _ = f.Workflow().Chain(bus.NewJob("a", nil)).OnQueue("critical").Dispatch(nil)
f.AssertWorkflowDispatchedOn(nil, "critical", "a")
```

#### <a id="queuefake-fake-assertworkflowdispatchedtimes"></a>Fake.AssertWorkflowDispatchedTimes

AssertWorkflowDispatchedTimes fails when workflow dispatch count for jobType does not match expected.

```go
f := queuefake.New()
wf := f.Workflow()
_, _ = wf.Chain(bus.NewJob("a", nil)).Dispatch(nil)
_, _ = wf.Chain(bus.NewJob("a", nil)).Dispatch(nil)
f.AssertWorkflowDispatchedTimes(nil, "a", 2)
```

#### <a id="queuefake-fake-assertworkflownotdispatched"></a>Fake.AssertWorkflowNotDispatched

AssertWorkflowNotDispatched fails when jobType was workflow-dispatched.

```go
f := queuefake.New()
f.AssertWorkflowNotDispatched(nil, "emails:send")
```

#### <a id="queuefake-fake-count"></a>Fake.Count

Count returns the total number of recorded dispatches.

```go
f := queuefake.New()
q := f.Queue()
```

#### <a id="queuefake-fake-countjob"></a>Fake.CountJob

CountJob returns how many times a job type was dispatched.

```go
f := queuefake.New()
q := f.Queue()
```

#### <a id="queuefake-fake-counton"></a>Fake.CountOn

CountOn returns how many times a job type was dispatched on a queue.

```go
f := queuefake.New()
q := f.Queue()
```

#### <a id="queuefake-new"></a>queuefake.New

New creates a fake queue harness backed by queue.NewFake().

```go
f := queuefake.New()
q := f.Queue()
f.AssertDispatched(nil, "emails:send")
f.AssertCount(nil, 1)
```

#### <a id="queuefake-fake-queue"></a>Fake.Queue

Queue returns the queue fake to inject into code under test.

```go
f := queuefake.New()
q := f.Queue()
```

#### <a id="queuefake-fake-records"></a>Fake.Records

Records returns a copy of recorded dispatches.

```go
f := queuefake.New()
records := f.Records()
```

#### <a id="queuefake-fake-reset"></a>Fake.Reset

Reset clears recorded dispatches.

```go
f := queuefake.New()
q := f.Queue()
f.Reset()
f.AssertNothingDispatched(nil)
```

#### <a id="queuefake-fake-workflow"></a>Fake.Workflow

Workflow returns the workflow/orchestration fake for chain/batch assertions.

```go
f := queuefake.New()
wf := f.Workflow()
_, _ = wf.Chain(
	bus.NewJob("a", nil),
	bus.NewJob("b", nil),
).Dispatch(context.Background())
f.AssertChained(nil, []string{"a", "b"})
```
<!-- api:embed:end -->
