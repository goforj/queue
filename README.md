<p align="center">
  <img src="./docs/images/logo.png?v=1" width="420" alt="queue logo">
</p>

<p align="center">
    queue gives your services one queue API with Redis, SQL, NATS, SQS, RabbitMQ, and in-process drivers.
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
    <img src="https://img.shields.io/badge/tests-337-brightgreen" alt="Tests">
<!-- test-count:embed:end -->
</p>

<p align="center">
  <a href="https://github.com/goforj/queue/actions/workflows/test.yml"><img src="https://img.shields.io/badge/integration%20matrix-7%2F7%20backends-brightgreen" alt="Integration matrix backends"></a>
  <a href="https://github.com/goforj/queue/actions/workflows/soak.yml"><img src="https://img.shields.io/badge/soak%2Fchaos-7%2F7%20backends-brightgreen" alt="Soak & chaos backends"></a>
  <img src="https://img.shields.io/badge/tests-unit%20%E2%9C%85%20|%20race%20%E2%9C%85%20|%20integration%20%E2%9C%85%20|%20scenarios%20%E2%9C%85-brightgreen" alt="Test suites">
  <img src="https://img.shields.io/badge/options-delay%20|%20backoff%20|%20timeout%20|%20retry%20|%20unique%20|%20queue-brightgreen" alt="Options covered">
</p>

## What queue is

queue is a backend-agnostic job queue runtime. Your application code depends on `queue.Queue` and fluent `queue.Task` values. The driver decides whether work runs via Redis/Asynq, a SQL table, NATS, SQS, RabbitMQ, an in-process worker pool, or synchronously in the caller.

Current matrix trust status and known integration gaps are tracked in `docs/integration-scenarios.md`.

## Drivers

| Driver / Backend | Mode | Notes | Durable | Async | Delay | Unique | Backoff | Timeout |
| ---: | :--- | :--- | :---: | :---: | :---: | :---: | :---: | :---: |
| <img src="https://img.shields.io/badge/null-%23666?style=flat" alt="Null"> | Drop-only | Discards all dispatched tasks; useful for testing/disabled queue modes. | - | - | - | - | - | - |
| <img src="https://img.shields.io/badge/sync-%23999999?logo=gnometerminal&logoColor=white" alt="Sync"> | Inline (caller) | Simplest deterministic test mode; effectively the "no external queue" option. | - | - | - | ✓ | - | ✓ |
| <img src="https://img.shields.io/badge/workerpool-%23696969?logo=clockify&logoColor=white" alt="Workerpool"> | In-process pool | Fast local async testing without external infra. | - | ✓ | ✓ | ✓ | ✓ | ✓ |
| <img src="https://img.shields.io/badge/database-%23336791?logo=postgresql&logoColor=white" alt="Database"> | SQL (pg/mysql/sqlite) | Works with `sqlite`, `mysql`, `pgx`; good local/prod parity option. | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| <img src="https://img.shields.io/badge/redis-%23DC382D?logo=redis&logoColor=white" alt="Redis"> | Redis/Asynq | Production Redis backend; backoff maps to Asynq limitations (`ErrBackoffUnsupported`). | ✓ | ✓ | ✓ | ✓ | - | ✓ |
| <img src="https://img.shields.io/badge/NATS-007ACC?style=flat" alt="NATS"> | Broker target | NATS transport with queue-subject routing. | - | ✓ | ✓ | ✓ | ✓ | ✓ |
| <img src="https://img.shields.io/badge/SQS-FF9900?style=flat" alt="SQS"> | Broker target | AWS SQS transport; supports endpoint override for localstack/testcontainers. | - | ✓ | ✓ | ✓ | ✓ | ✓ |
| <img src="https://img.shields.io/badge/rabbitmq-%23FF6600?logo=rabbitmq&logoColor=white" alt="RabbitMQ"> | Broker target | Uses RabbitMQ for transport and worker consumption. | - | ✓ | ✓ | ✓ | ✓ | ✓ |

## Installation

```bash
go get github.com/goforj/queue
```

## Quick Start

```go
import (
    "context"
    "time"

    "github.com/goforj/queue"
)

type EmailPayload struct {
    ID int `json:"id"`
    To string `json:"to"`
}

func emailHandler(ctx context.Context, task queue.Task) error {
    _ = ctx
    var payload EmailPayload
    if err := task.Bind(&payload); err != nil {
        return err
    }
    _ = payload
    return nil
}

func main() {
    // Create queue runtime.
    q, _ := queue.NewWorkerpool()

    // Register handler on queue runtime.
    q.Register("emails:send", emailHandler)

    // Start workers (2 concurrent workers).
    _ = q.Workers(2).StartWorkers(context.Background())
    defer q.Shutdown(context.Background())

    // Build a task with payload and dispatch behavior.
    task := queue.NewTask("emails:send").
        Payload(EmailPayload{
            ID: 123,
            To: "user@example.com",
        }).
        OnQueue("critical").
        Delay(5 * time.Second).
        Timeout(20 * time.Second).
        Retry(3)

    // Dispatch the task.
    _ = q.Dispatch(task)
}
```

Switch to Redis without changing job code:

```go
q, _ := queue.NewRedis("127.0.0.1:6379")
```

Use SQL for a durable local queue runtime:

```go
q, _ := queue.NewDatabase("sqlite", "file:queue.db?_busy_timeout=5000")
```

## Testing modes

- `null`: drop-only dispatch path when you want queue calls to no-op in tests/dev.
- `sync`: deterministic unit tests with inline execution and no external broker.
- `workerpool`: async local behavior tests without external infrastructure.
- `integration` tag + backend matrix: full broker/database realism (Redis, SQL, NATS, SQS, RabbitMQ).

Use `null` when you only need to exercise dispatch call paths without execution.
Use `sync` when you need handler logic to run in the same test/process deterministically.

### Fake queue assertions

```go
fake := queue.NewFake()
fake.AssertNothingDispatched(t)

// exercise code that dispatches jobs against fake
_ = fake.Dispatch(
	queue.NewTask("orders:ship").
		Payload(map[string]any{"id": 42}).
		OnQueue("orders"),
)

fake.AssertDispatched(t, "orders:ship")
fake.AssertDispatchedOn(t, "orders", "orders:ship")
fake.AssertDispatchedTimes(t, "orders:ship", 1)
fake.AssertNotDispatched(t, "orders:cancel")
fake.AssertCount(t, 1)
```

## Backend end-to-end snippets

```go
// Null: dispatch is accepted and dropped.
q, _ := queue.NewNull()
_ = q.Dispatch(
	queue.NewTask("emails:send").
		Payload(map[string]any{"id": 1}).
		OnQueue("default"),
)
```

```go
// Sync: register handler and execute inline on dispatch.
q, _ := queue.NewSync()
q.Register("emails:send", emailHandler)
_ = q.Workers(1).StartWorkers(context.Background())
defer q.Shutdown(context.Background())
_ = q.Dispatch(
	queue.NewTask("emails:send").
		Payload(EmailPayload{ID: 1, To: "user@example.com"}).
		OnQueue("default"),
)
```

```go
// Workerpool: async local workers.
q, _ := queue.NewWorkerpool()
q.Register("emails:send", emailHandler)
_ = q.Workers(2).StartWorkers(context.Background())
defer q.Shutdown(context.Background())
_ = q.Dispatch(
	queue.NewTask("emails:send").
		Payload(EmailPayload{ID: 1, To: "user@example.com"}).
		OnQueue("default"),
)
```

```go
// Database: durable SQL-backed queue.
q, _ := queue.NewDatabase("sqlite", "file:queue.db?_busy_timeout=5000")
q.Register("emails:send", emailHandler)
_ = q.Workers(2).StartWorkers(context.Background())
defer q.Shutdown(context.Background())
_ = q.Dispatch(
	queue.NewTask("emails:send").
		Payload(EmailPayload{ID: 1, To: "user@example.com"}).
		OnQueue("default"),
)
```

```go
// Redis: broker-backed async queue.
q, _ := queue.NewRedis("127.0.0.1:6379")
q.Register("emails:send", emailHandler)
_ = q.Workers(2).StartWorkers(context.Background())
defer q.Shutdown(context.Background())
_ = q.Dispatch(
	queue.NewTask("emails:send").
		Payload(EmailPayload{ID: 1, To: "user@example.com"}).
		OnQueue("default"),
)
```

```go
// NATS: broker-backed async queue.
q, _ := queue.NewNATS("nats://127.0.0.1:4222")
q.Register("emails:send", emailHandler)
_ = q.Workers(2).StartWorkers(context.Background())
defer q.Shutdown(context.Background())
_ = q.Dispatch(
	queue.NewTask("emails:send").
		Payload(EmailPayload{ID: 1, To: "user@example.com"}).
		OnQueue("default"),
)
```

```go
// SQS: broker-backed async queue.
q, _ := queue.NewSQS("us-east-1")
q.Register("emails:send", emailHandler)
_ = q.Workers(2).StartWorkers(context.Background())
defer q.Shutdown(context.Background())
_ = q.Dispatch(
	queue.NewTask("emails:send").
		Payload(EmailPayload{ID: 1, To: "user@example.com"}).
		OnQueue("default"),
)
```

```go
// RabbitMQ: broker-backed async queue.
q, _ := queue.NewRabbitMQ("amqp://guest:guest@127.0.0.1:5672/")
q.Register("emails:send", emailHandler)
_ = q.Workers(2).StartWorkers(context.Background())
defer q.Shutdown(context.Background())
_ = q.Dispatch(
	queue.NewTask("emails:send").
		Payload(EmailPayload{ID: 1, To: "user@example.com"}).
		OnQueue("default"),
)
```

## Task builder options

```go
task := queue.NewTask("emails:send").
	// Payload can be bytes, structs, maps, or JSON-marshalable values.
	// Default payload is empty.
	Payload(map[string]any{"id": 123, "to": "user@example.com"}).
	// OnQueue sets the queue name.
	// Default is empty; broker-style drivers expect an explicit queue.
	OnQueue("default").
	// Timeout sets per-task execution timeout.
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

_ = q.Dispatch(task)
```

## Benchmarks

```go
go test ./docs/bench -tags benchrender
```

<!-- bench:embed:start -->

### Integration

| Driver | N | ns/op | B/op | allocs/op |
|:------|---:|-----:|-----:|---------:|
| mysql | 579 | 1959891 | 9295 | 151 |
| nats | 1631804 | 727.8 | 1293 | 14 |
| postgres | 1227 | 1107497 | 16162 | 239 |
| rabbitmq | 8144 | 143554 | 4575 | 112 |
| redis | 13513 | 87141 | 2112 | 33 |
| sqs | 1143 | 1019487 | 65594 | 758 |

### Local

| Driver | N | ns/op | B/op | allocs/op |
|:------|---:|-----:|-----:|---------:|
| database-sqlite | 6452 | 395034 | 3345 | 87 |
| null | 26300509 | 47.63 | 128 | 1 |
| sync | 3980317 | 299.3 | 408 | 6 |
| workerpool | 1829486 | 635.2 | 456 | 7 |

<!-- bench:embed:end -->

## Observability quick guide

Attach an observer when creating a queue. Use `StatsCollector` for in-memory counters and throughput windows.

```go
collector := queue.NewStatsCollector()
q, _ := queue.New(queue.Config{
	Driver:    queue.DriverRedis,
	RedisAddr: "127.0.0.1:6379",
	Observer:  collector,
})

snapshot, _ := queue.SnapshotQueue(context.Background(), q, collector)
counters, _ := snapshot.Queue("default")
throughput, _ := snapshot.Throughput("default")

fmt.Printf("%+v\n", counters)
fmt.Printf("hour=%+v\n", throughput.Hour)
```

`SnapshotQueue` prefers native driver stats when available and falls back to the collector snapshot when a driver does not expose native stats.
Use `queue.SupportsNativeStats(q)` and `queue.SupportsPause(q)` to branch runtime behavior safely.

### Distributed counters and source of truth

In distributed systems, `Observer` + `StatsCollector` counters are process-local and in-memory.
They are useful for local insight and per-worker telemetry, but they are not a globally consistent source of truth by themselves.

Use this rule:

- Drivers with native stats (`Sync`, `Workerpool`, `Database`, `Redis`): use `SnapshotQueue(...)` and treat driver-native stats as authoritative.
- Drivers without native stats (`NATS`, `SQS`, `RabbitMQ`, `Null`): treat observer events as telemetry signals and aggregate them centrally (metrics/log pipeline) for cluster-wide numbers.

Practical guidance:

- Always keep an observer attached for eventing/alerting.
- For cluster dashboards, prefer backend-native stats when available.
- For non-native backends, publish observer events to centralized metrics/logging and compute totals there.

### Compose observers

Use `queue.MultiObserver(...)` when you want multiple observer behaviors at once, such as logging plus stats collection.

```go
collector := queue.NewStatsCollector()

loggerObserver := queue.ObserverFunc(func(event queue.Event) {
	// send to your logger here
	_ = event
})

q, _ := queue.New(queue.Config{
	Driver:    queue.DriverRedis,
	RedisAddr: "127.0.0.1:6379",
	Observer:  queue.MultiObserver(loggerObserver, collector),
})

// SnapshotQueue can still use the same collector instance.
snapshot, _ := queue.SnapshotQueue(context.Background(), q, collector)
_, _ = snapshot.Queue("default")
```

### Logging middleware hook

Use `Observer` as your middleware hook for structured logging.
Observers receive events for the entire runtime (all queues and task types).
To observe only specific tasks, filter by `event.TaskType` (and/or `event.Queue`) inside your observer.
This example logs every event kind with human-readable messages.

```go
logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
observer := queue.ObserverFunc(func(event queue.Event) {
	attemptInfo := fmt.Sprintf("attempt=%d/%d", event.Attempt, event.MaxRetry+1)
	taskInfo := fmt.Sprintf("task=%s key=%s queue=%s driver=%s", event.TaskType, event.TaskKey, event.Queue, event.Driver)

	switch event.Kind {
	case queue.EventEnqueueAccepted:
		logger.Info("Accepted dispatch", "msg", fmt.Sprintf("Accepted %s", taskInfo), "scheduled", event.Scheduled, "at", event.Time.Format(time.RFC3339Nano))
	case queue.EventEnqueueRejected:
		logger.Error("Dispatch failed", "msg", fmt.Sprintf("Rejected %s", taskInfo), "error", event.Err)
	case queue.EventEnqueueDuplicate:
		logger.Warn("Skipped duplicate job", "msg", fmt.Sprintf("Duplicate %s", taskInfo))
	case queue.EventEnqueueCanceled:
		logger.Warn("Canceled dispatch", "msg", fmt.Sprintf("Canceled %s", taskInfo), "error", event.Err)
	case queue.EventProcessStarted:
		logger.Info("Started processing job", "msg", fmt.Sprintf("Started %s (%s)", taskInfo, attemptInfo), "at", event.Time.Format(time.RFC3339Nano))
	case queue.EventProcessSucceeded:
		logger.Info("Processed job", "msg", fmt.Sprintf("Processed %s in %s (%s)", taskInfo, event.Duration, attemptInfo))
	case queue.EventProcessFailed:
		logger.Error("Processing failed", "msg", fmt.Sprintf("Failed %s after %s (%s)", taskInfo, event.Duration, attemptInfo), "error", event.Err)
	case queue.EventProcessRetried:
		logger.Warn("Retrying job", "msg", fmt.Sprintf("Retry scheduled for %s (%s)", taskInfo, attemptInfo), "error", event.Err)
	case queue.EventProcessArchived:
		logger.Error("Archived failed job", "msg", fmt.Sprintf("Archived %s after final failure (%s)", taskInfo, attemptInfo), "error", event.Err)
	case queue.EventQueuePaused:
		logger.Info("Paused queue", "msg", fmt.Sprintf("Paused queue=%s driver=%s", event.Queue, event.Driver))
	case queue.EventQueueResumed:
		logger.Info("Resumed queue", "msg", fmt.Sprintf("Resumed queue=%s driver=%s", event.Queue, event.Driver))
	default:
		logger.Info("Queue event", "msg", fmt.Sprintf("kind=%s %s", event.Kind, taskInfo))
	}
})

q, _ := queue.New(queue.Config{
	Driver:    queue.DriverRedis,
	RedisAddr: "127.0.0.1:6379",
	Observer:  observer,
})
```

Example: only log one task type.

```go
observer := queue.ObserverFunc(func(event queue.Event) {
	if event.TaskType != "emails:send" {
		return
	}
	// log event...
})
```

Example: hook only specific event kinds.

```go
observer := queue.ObserverFunc(func(event queue.Event) {
	switch event.Kind {
	case queue.EventProcessFailed, queue.EventProcessRetried, queue.EventProcessArchived:
		// alerting / error logs
	case queue.EventProcessSucceeded:
		// success metrics
	default:
		return
	}
})
```

If you prefer zerolog, implement the same `Observer` interface in a small adapter and set it on `Config.Observer`.

### Observability capabilities by driver

Legend: `✓` supported, `-` unsupported/fallback.

| Driver | Native `Stats` | `PauseQueue`/`ResumeQueue` | Collector counters | Throughput windows |
|:--|:--:|:--:|:--:|:--:|
| Sync | ✓ | ✓ | ✓ | ✓ |
| Workerpool | ✓ | ✓ | ✓ | ✓ |
| Database (sqlite/mysql/postgres) | ✓ | - | ✓ | ✓ |
| Redis | ✓ | ✓ | ✓ | ✓ |
| NATS | - | - | ✓ | ✓ |
| SQS | - | - | ✓ | ✓ |
| RabbitMQ | - | - | ✓ | ✓ |

### Observability events reference

| Event | Meaning |
|:--|:--|
| EventEnqueueAccepted | Task was accepted by dispatch. |
| EventEnqueueRejected | Task dispatch failed with an error. |
| EventEnqueueDuplicate | Task dispatch was rejected as duplicate (`UniqueFor`). |
| EventEnqueueCanceled | Dispatch was canceled by context timeout/cancelation. |
| EventProcessStarted | Worker started handling a task. |
| EventProcessSucceeded | Worker completed a task successfully. |
| EventProcessFailed | Worker attempt failed. |
| EventProcessRetried | Failed attempt was requeued for another attempt. |
| EventProcessArchived | Failed task reached terminal failure (no retries left). |
| EventQueuePaused | Queue consumption was paused. |
| EventQueueResumed | Queue consumption was resumed. |

For full field-level semantics and guarantees, see [`docs/events.md`](docs/events.md).

## Runtime lifecycle

```go
// Register handlers before starting workers.
q.Register("emails:send", emailHandler)

// Start workers with explicit concurrency.
ctx := context.Background()
_ = q.Workers(2).StartWorkers(ctx)

// Dispatch jobs using either Dispatch(...) or DispatchCtx(...).
_ = q.Dispatch(
    queue.NewTask("emails:send").
        Payload(EmailPayload{ID: 123, To: "user@example.com"}).
        OnQueue("default"),
)

// Shutdown gracefully drains in-flight work (where supported).
shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
defer cancel()
_ = q.Shutdown(shutdownCtx)
```

## Driver selection via config

Use `queue.Config` with `New` for advanced/custom setups where you need multiple fields together.
Use `q.Workers(n).StartWorkers(ctx)` to configure worker count before start.

### Config support matrix

Common:

| Field | Notes |
|--:|:--|
| **Driver** | Selects backend. |
| **DefaultQueue** | Default queue name used by helpers (task-level `OnQueue(...)` still controls dispatch target). |

Null:

| Field | Required | Notes |
|--:|:--:|:--|
| **Driver** | ✓ | `DriverNull` |

Sync:

| Field | Required | Notes |
|--:|:--:|:--|
| **Driver** | ✓ | `DriverSync` |

Workerpool:

| Field | Required | Notes |
|--:|:--:|:--|
| **Driver** | ✓ | `DriverWorkerpool` |

Database:

| Field | Required | Notes |
|--:|:--:|:--|
| **Driver** | ✓ | `DriverDatabase` |
| **Database** | o | Existing `*sql.DB` handle; if set, driver/DSN can be omitted. |
| **DatabaseDriver** | ✓* | `sqlite`, `mysql`, or `pgx` (required when `Database` is nil). |
| **DatabaseDSN** | ✓* | Connection string (required when `Database` is nil). |
| **DefaultQueue** | o | Queue default for DB-backed runtime. |

Redis:

| Field | Required | Notes |
|--:|:--:|:--|
| **Driver** | ✓ | `DriverRedis` |
| **RedisAddr** | ✓ | Required for Redis queue dispatching. |
| **RedisPassword** | o | Redis auth password. |
| **RedisDB** | o | Redis logical DB index. |

NATS:

| Field | Required | Notes |
|--:|:--:|:--|
| **Driver** | ✓ | `DriverNATS` |
| **NATSURL** | ✓ | Required for NATS queue dispatching. |

SQS:

| Field | Required | Notes |
|--:|:--:|:--|
| **Driver** | ✓ | `DriverSQS` |
| **SQSRegion** | o | AWS region (defaults to `us-east-1`). |
| **SQSEndpoint** | o | Override endpoint (localstack/testing). |
| **SQSAccessKey** | o | Static access key. |
| **SQSSecretKey** | o | Static secret key. |

RabbitMQ:

| Field | Required | Notes |
|--:|:--:|:--|
| **Driver** | ✓ | `DriverRabbitMQ` |
| **RabbitMQURL** | ✓ | Required for RabbitMQ queue dispatching. |

## API reference

The API section below is autogenerated; do not edit between the markers.

<!-- api:embed:start -->

## API Index

### bus

| Group | Functions |
|------:|:-----------|
| **Batching** | [BatchBuilder.AllowFailures](#bus-batchbuilder-allowfailures) [BatchBuilder.Catch](#bus-batchbuilder-catch) [BatchBuilder.Dispatch](#bus-batchbuilder-dispatch) [BatchBuilder.Finally](#bus-batchbuilder-finally) [BatchBuilder.Name](#bus-batchbuilder-name) [BatchBuilder.OnQueue](#bus-batchbuilder-onqueue) [BatchBuilder.Progress](#bus-batchbuilder-progress) [BatchBuilder.Then](#bus-batchbuilder-then) |
| **Chaining** | [ChainBuilder.Catch](#bus-chainbuilder-catch) [ChainBuilder.Dispatch](#bus-chainbuilder-dispatch) [ChainBuilder.Finally](#bus-chainbuilder-finally) [ChainBuilder.OnQueue](#bus-chainbuilder-onqueue) |
| **Constructors** | [New](#bus-new) [NewFake](#bus-newfake) [NewJob](#bus-newjob) [NewMemoryStore](#bus-newmemorystore) [NewSQLStore](#bus-newsqlstore) [NewWithStore](#bus-newwithstore) |
| **Events** | [MultiObserver](#bus-multiobserver) [ObserverFunc.Observe](#bus-observerfunc-observe) |
| **Job** | [Job.Backoff](#bus-job-backoff) [Context.Bind](#bus-context-bind) [Job.Delay](#bus-job-delay) [Job.OnQueue](#bus-job-onqueue) [Context.PayloadBytes](#bus-context-payloadbytes) [Job.Retry](#bus-job-retry) [Job.Timeout](#bus-job-timeout) [Job.UniqueFor](#bus-job-uniquefor) |
| **Middleware** | [FailOnError.Handle](#bus-failonerror-handle) [MiddlewareFunc.Handle](#bus-middlewarefunc-handle) [RateLimit.Handle](#bus-ratelimit-handle) [RetryPolicy.Handle](#bus-retrypolicy-handle) [SkipWhen.Handle](#bus-skipwhen-handle) [WithoutOverlapping.Handle](#bus-withoutoverlapping-handle) |
| **Options** | [WithClock](#bus-withclock) [WithMiddleware](#bus-withmiddleware) [WithObserver](#bus-withobserver) [WithStore](#bus-withstore) |
| **Testing** | [Fake.AssertBatchCount](#bus-fake-assertbatchcount) [Fake.AssertBatched](#bus-fake-assertbatched) [Fake.AssertChained](#bus-fake-assertchained) [Fake.AssertCount](#bus-fake-assertcount) [Fake.AssertDispatched](#bus-fake-assertdispatched) [Fake.AssertDispatchedOn](#bus-fake-assertdispatchedon) [Fake.AssertDispatchedTimes](#bus-fake-assertdispatchedtimes) [Fake.AssertNotDispatched](#bus-fake-assertnotdispatched) [Fake.AssertNothingBatched](#bus-fake-assertnothingbatched) [Fake.AssertNothingDispatched](#bus-fake-assertnothingdispatched) [Fake.Batch](#bus-fake-batch) [Fake.Chain](#bus-fake-chain) [Fake.Dispatch](#bus-fake-dispatch) |

### queue

| Group | Functions |
|------:|:-----------|
| **Constructors** | [New](#queue-new) [NewDatabase](#queue-newdatabase) [NewNATS](#queue-newnats) [NewNull](#queue-newnull) [NewQueueWithDefaults](#queue-newqueuewithdefaults) [NewRabbitMQ](#queue-newrabbitmq) [NewRedis](#queue-newredis) [NewSQS](#queue-newsqs) [NewStatsCollector](#queue-newstatscollector) [NewSync](#queue-newsync) [NewWorkerpool](#queue-newworkerpool) |
| **Observability** | [StatsSnapshot.Active](#queue-statssnapshot-active) [StatsSnapshot.Archived](#queue-statssnapshot-archived) [StatsSnapshot.Failed](#queue-statssnapshot-failed) [MultiObserver](#queue-multiobserver) [Observer.Observe](#queue-observer-observe) [ObserverFunc.Observe](#queue-observerfunc-observe) [StatsCollector.Observe](#queue-statscollector-observe) [PauseQueue](#queue-pausequeue) [StatsSnapshot.Paused](#queue-statssnapshot-paused) [StatsSnapshot.Pending](#queue-statssnapshot-pending) [StatsSnapshot.Processed](#queue-statssnapshot-processed) [StatsSnapshot.Queue](#queue-statssnapshot-queue) [StatsSnapshot.Queues](#queue-statssnapshot-queues) [ResumeQueue](#queue-resumequeue) [StatsSnapshot.RetryCount](#queue-statssnapshot-retrycount) [StatsSnapshot.Scheduled](#queue-statssnapshot-scheduled) [StatsCollector.Snapshot](#queue-statscollector-snapshot) [SnapshotQueue](#queue-snapshotqueue) [SupportsNativeStats](#queue-supportsnativestats) [SupportsPause](#queue-supportspause) [StatsSnapshot.Throughput](#queue-statssnapshot-throughput) |
| **Other** | [FakeQueue.Dispatch](#queue-fakequeue-dispatch) [Queue.Dispatch](#queue-queue-dispatch) [Queue.DispatchCtx](#queue-queue-dispatchctx) [FakeQueue.Driver](#queue-fakequeue-driver) [Queue.Driver](#queue-queue-driver) [ChannelObserver.Observe](#queue-channelobserver-observe) [FakeQueue.Register](#queue-fakequeue-register) [Queue.Register](#queue-queue-register) [FakeQueue.Shutdown](#queue-fakequeue-shutdown) [Queue.Shutdown](#queue-queue-shutdown) [FakeQueue.StartWorkers](#queue-fakequeue-startworkers) [Queue.StartWorkers](#queue-queue-startworkers) [Queue.Workers](#queue-queue-workers) |
| **Task** | [Task.Backoff](#queue-task-backoff) [Task.Bind](#queue-task-bind) [Task.Delay](#queue-task-delay) [NewTask](#queue-newtask) [Task.OnQueue](#queue-task-onqueue) [Task.Payload](#queue-task-payload) [Task.PayloadBytes](#queue-task-payloadbytes) [Task.PayloadJSON](#queue-task-payloadjson) [Task.Retry](#queue-task-retry) [Task.Timeout](#queue-task-timeout) [Task.UniqueFor](#queue-task-uniquefor) |
| **Testing** | [FakeQueue.AssertCount](#queue-fakequeue-assertcount) [FakeQueue.AssertDispatched](#queue-fakequeue-assertdispatched) [FakeQueue.AssertDispatchedOn](#queue-fakequeue-assertdispatchedon) [FakeQueue.AssertDispatchedTimes](#queue-fakequeue-assertdispatchedtimes) [FakeQueue.AssertNotDispatched](#queue-fakequeue-assertnotdispatched) [FakeQueue.AssertNothingDispatched](#queue-fakequeue-assertnothingdispatched) [FakeQueue.DispatchCtx](#queue-fakequeue-dispatchctx) [NewFake](#queue-newfake) [FakeQueue.Records](#queue-fakequeue-records) [FakeQueue.Reset](#queue-fakequeue-reset) [FakeQueue.Workers](#queue-fakequeue-workers) |



## bus API

### Batching

#### <a id="bus-batchbuilder-allowfailures"></a>bus.BatchBuilder.AllowFailures

AllowFailures keeps the batch running when individual jobs fail.

```go
batchID, _ := b.Batch(bus.NewJob("a", nil)).AllowFailures().Dispatch(context.Background())
```

#### <a id="bus-batchbuilder-catch"></a>bus.BatchBuilder.Catch

Catch registers a callback invoked when batch encounters a failure.

```go
batchID, _ := b.Batch(bus.NewJob("a", nil)).
	Catch(func(context.Context, bus.BatchState, error) error { return nil }).
	Dispatch(context.Background())
```

#### <a id="bus-batchbuilder-dispatch"></a>bus.BatchBuilder.Dispatch

Dispatch creates and starts the batch workflow.

```go
batchID, _ := b.Batch(bus.NewJob("a", nil), bus.NewJob("b", nil)).Dispatch(context.Background())
```

#### <a id="bus-batchbuilder-finally"></a>bus.BatchBuilder.Finally

Finally registers a callback invoked once when batch reaches terminal state.

```go
batchID, _ := b.Batch(bus.NewJob("a", nil)).
	Finally(func(context.Context, bus.BatchState) error { return nil }).
	Dispatch(context.Background())
```

#### <a id="bus-batchbuilder-name"></a>bus.BatchBuilder.Name

Name sets a display name for the batch.

```go
batchID, _ := b.Batch(bus.NewJob("a", nil)).Name("nightly").Dispatch(context.Background())
```

#### <a id="bus-batchbuilder-onqueue"></a>bus.BatchBuilder.OnQueue

OnQueue applies a default queue to batch jobs that do not set one.

```go
batchID, _ := b.Batch(bus.NewJob("a", nil)).OnQueue("critical").Dispatch(context.Background())
```

#### <a id="bus-batchbuilder-progress"></a>bus.BatchBuilder.Progress

Progress registers a callback invoked as jobs complete.

```go
batchID, _ := b.Batch(bus.NewJob("a", nil)).
	Progress(func(context.Context, bus.BatchState) error { return nil }).
	Dispatch(context.Background())
```

#### <a id="bus-batchbuilder-then"></a>bus.BatchBuilder.Then

Then registers a callback invoked once when batch succeeds.

```go
batchID, _ := b.Batch(bus.NewJob("a", nil)).
	Then(func(context.Context, bus.BatchState) error { return nil }).
	Dispatch(context.Background())
```

### Chaining

#### <a id="bus-chainbuilder-catch"></a>bus.ChainBuilder.Catch

Catch registers a callback invoked when chain execution fails.

```go
chainID, _ := b.Chain(bus.NewJob("a", nil)).
	Catch(func(context.Context, bus.ChainState, error) error { return nil }).
	Dispatch(context.Background())
```

#### <a id="bus-chainbuilder-dispatch"></a>bus.ChainBuilder.Dispatch

Dispatch creates and starts the chain workflow.

```go
chainID, _ := b.Chain(bus.NewJob("a", nil), bus.NewJob("b", nil)).Dispatch(context.Background())
```

#### <a id="bus-chainbuilder-finally"></a>bus.ChainBuilder.Finally

Finally registers a callback invoked once when chain execution finishes.

```go
chainID, _ := b.Chain(bus.NewJob("a", nil)).
	Finally(func(context.Context, bus.ChainState) error { return nil }).
	Dispatch(context.Background())
```

#### <a id="bus-chainbuilder-onqueue"></a>bus.ChainBuilder.OnQueue

OnQueue applies a default queue to chain jobs that do not set one.

```go
chainID, _ := b.Chain(
	bus.NewJob("a", nil),
	bus.NewJob("b", nil),
).OnQueue("critical").Dispatch(context.Background())
```

### Constructors

#### <a id="bus-new"></a>bus.New

New creates a bus runtime using an in-memory orchestration store.

```go
q, _ := queue.NewSync()
b, _ := bus.New(q)
b.Register("monitor:poll", func(context.Context, bus.Context) error { return nil })
defer b.Shutdown(context.Background())
type PollPayload struct {
	URL string `json:"url"`
}
_, _ = b.Dispatch(context.Background(), bus.NewJob("monitor:poll", PollPayload{
	URL: "https://goforj.dev/health",
}))
```

#### <a id="bus-newfake"></a>bus.NewFake

NewFake creates a bus fake that records dispatch, chain, and batch calls.

```go
fake := bus.NewFake()
_, _ = fake.Dispatch(context.Background(), bus.NewJob("monitor:poll", nil))
```

#### <a id="bus-newjob"></a>bus.NewJob

NewJob creates a typed bus job payload with optional fluent options.

```go
type PollPayload struct {
	URL string `json:"url"`
}
job := bus.NewJob("monitor:poll", PollPayload{
	URL: "https://goforj.dev/health",
}).
	OnQueue("monitor-critical").
	Delay(2 * time.Second).
	Timeout(15 * time.Second).
	Retry(3).
	Backoff(500 * time.Millisecond).
	UniqueFor(30 * time.Second)
```

#### <a id="bus-newmemorystore"></a>bus.NewMemoryStore

NewMemoryStore creates an in-memory orchestration store implementation.

```go
store := bus.NewMemoryStore()
```

#### <a id="bus-newsqlstore"></a>bus.NewSQLStore

NewSQLStore creates a SQL-backed orchestration store.

```go
store, _ := bus.NewSQLStore(bus.SQLStoreConfig{
	DriverName: "sqlite",
	DSN:        "file:bus.db?_busy_timeout=5000",
})
```

#### <a id="bus-newwithstore"></a>bus.NewWithStore

NewWithStore creates a bus runtime with a custom orchestration store.

```go
q, _ := queue.NewSync()
store := bus.NewMemoryStore()
b, _ := bus.NewWithStore(q, store)
```

### Events

#### <a id="bus-multiobserver"></a>bus.MultiObserver

MultiObserver fans out one event to multiple observers.

```go
observer := bus.MultiObserver(
	bus.ObserverFunc(func(event bus.Event) {}),
	bus.ObserverFunc(func(event bus.Event) {}),
)
observer.Observe(bus.Event{Kind: bus.EventDispatchStarted})
```

#### <a id="bus-observerfunc-observe"></a>bus.ObserverFunc.Observe

Observe calls the wrapped observer function.

```go
observer := bus.ObserverFunc(func(event bus.Event) {
})
observer.Observe(bus.Event{Kind: bus.EventDispatchStarted})
```

### Job

#### <a id="bus-job-backoff"></a>bus.Job.Backoff

Backoff sets retry backoff for this job.

```go
job := bus.NewJob("emails:send", nil).Backoff(500 * time.Millisecond)
```

#### <a id="bus-context-bind"></a>bus.Context.Bind

Bind unmarshals the job payload into dst.

```go
type PollPayload struct {
	URL string `json:"url"`
}
var payload PollPayload
```

#### <a id="bus-job-delay"></a>bus.Job.Delay

Delay defers job execution.

```go
job := bus.NewJob("emails:send", nil).Delay(2 * time.Second)
```

#### <a id="bus-job-onqueue"></a>bus.Job.OnQueue

OnQueue sets the target queue for this job.

```go
job := bus.NewJob("emails:send", nil).OnQueue("critical")
```

#### <a id="bus-context-payloadbytes"></a>bus.Context.PayloadBytes

PayloadBytes returns a copy of raw job payload bytes.

```go
raw := jc.PayloadBytes()
```

#### <a id="bus-job-retry"></a>bus.Job.Retry

Retry sets max retry attempts for this job.

```go
job := bus.NewJob("emails:send", nil).Retry(5)
```

#### <a id="bus-job-timeout"></a>bus.Job.Timeout

Timeout sets execution timeout for this job.

```go
job := bus.NewJob("emails:send", nil).Timeout(15 * time.Second)
```

#### <a id="bus-job-uniquefor"></a>bus.Job.UniqueFor

UniqueFor sets dedupe TTL for this job.

```go
job := bus.NewJob("emails:send", nil).UniqueFor(30 * time.Second)
```

### Middleware

#### <a id="bus-failonerror-handle"></a>bus.FailOnError.Handle

Handle wraps matched errors as fatal errors to stop retries.

```go
mw := bus.FailOnError{
	When: func(err error) bool { return err != nil },
}
```

#### <a id="bus-middlewarefunc-handle"></a>bus.MiddlewareFunc.Handle

Handle calls the wrapped middleware function.

```go
mw := bus.MiddlewareFunc(func(ctx context.Context, jc bus.Context, next bus.Next) error {
	return next(ctx, jc)
})
```

#### <a id="bus-ratelimit-handle"></a>bus.RateLimit.Handle

Handle applies limiter checks before executing the next handler.

```go
mw := bus.RateLimit{
	Key: func(context.Context, bus.Context) string { return "emails" },
}
```

#### <a id="bus-retrypolicy-handle"></a>bus.RetryPolicy.Handle

Handle passes execution through without modification.

```go
policy := bus.RetryPolicy{}
```

#### <a id="bus-skipwhen-handle"></a>bus.SkipWhen.Handle

Handle skips job execution when Predicate returns true.

```go
mw := bus.SkipWhen{
	Predicate: func(context.Context, bus.Context) bool { return true },
}
```

#### <a id="bus-withoutoverlapping-handle"></a>bus.WithoutOverlapping.Handle

Handle acquires a lock and prevents concurrent overlap for the same key.

```go
mw := bus.WithoutOverlapping{
	Key: func(context.Context, bus.Context) string { return "job-key" },
	TTL: 30 * time.Second,
}
```

### Options

#### <a id="bus-withclock"></a>bus.WithClock

WithClock overrides the runtime clock used for event/state timestamps.

```go
fixed := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
b, _ := bus.New(q, bus.WithClock(func() time.Time { return fixed }))
```

#### <a id="bus-withmiddleware"></a>bus.WithMiddleware

WithMiddleware appends middleware to the runtime execution chain.

```go
mw := bus.MiddlewareFunc(func(ctx context.Context, jc bus.Context, next bus.Next) error {
	return next(ctx, jc)
})
b, _ := bus.New(q, bus.WithMiddleware(mw))
```

#### <a id="bus-withobserver"></a>bus.WithObserver

WithObserver installs an event observer for dispatch/job/chain/batch lifecycle hooks.

```go
observer := bus.ObserverFunc(func(event bus.Event) {
})
b, _ := bus.New(q, bus.WithObserver(observer))
```

#### <a id="bus-withstore"></a>bus.WithStore

WithStore overrides the orchestration store used for chain/batch/callback state.

```go
store := bus.NewMemoryStore()
b, _ := bus.New(q, bus.WithStore(store))
```

### Testing

#### <a id="bus-fake-assertbatchcount"></a>bus.Fake.AssertBatchCount

AssertBatchCount fails if total recorded batch count does not match n.

```go
fake := bus.NewFake()
_, _ = fake.Batch(bus.NewJob("a", nil)).Dispatch(context.Background())
fake.AssertBatchCount(nil, 1)
```

#### <a id="bus-fake-assertbatched"></a>bus.Fake.AssertBatched

AssertBatched fails unless at least one recorded batch matches predicate.

```go
fake := bus.NewFake()
_, _ = fake.Batch(bus.NewJob("a", nil), bus.NewJob("b", nil)).Dispatch(context.Background())
fake.AssertBatched(nil, func(spec bus.BatchSpec) bool { return len(spec.JobTypes) == 2 })
```

#### <a id="bus-fake-assertchained"></a>bus.Fake.AssertChained

AssertChained fails if no recorded chain matches expected job type order.

```go
fake := bus.NewFake()
_, _ = fake.Chain(bus.NewJob("a", nil), bus.NewJob("b", nil)).Dispatch(context.Background())
fake.AssertChained(nil, []string{"a", "b"})
```

#### <a id="bus-fake-assertcount"></a>bus.Fake.AssertCount

AssertCount fails if total dispatched count does not match n.

```go
fake := bus.NewFake()
_, _ = fake.Dispatch(context.Background(), bus.NewJob("emails:send", nil))
fake.AssertCount(nil, 1)
```

#### <a id="bus-fake-assertdispatched"></a>bus.Fake.AssertDispatched

AssertDispatched fails if the given job type was never dispatched.

```go
fake := bus.NewFake()
_, _ = fake.Dispatch(context.Background(), bus.NewJob("emails:send", nil))
fake.AssertDispatched(nil, "emails:send")
```

#### <a id="bus-fake-assertdispatchedon"></a>bus.Fake.AssertDispatchedOn

AssertDispatchedOn fails if a job type was not dispatched on queueName.

```go
fake := bus.NewFake()
_, _ = fake.Dispatch(context.Background(), bus.NewJob("emails:send", nil).OnQueue("critical"))
fake.AssertDispatchedOn(nil, "critical", "emails:send")
```

#### <a id="bus-fake-assertdispatchedtimes"></a>bus.Fake.AssertDispatchedTimes

AssertDispatchedTimes fails if dispatched count for job type does not match n.

```go
fake := bus.NewFake()
_, _ = fake.Dispatch(context.Background(), bus.NewJob("emails:send", nil))
_, _ = fake.Dispatch(context.Background(), bus.NewJob("emails:send", nil))
fake.AssertDispatchedTimes(nil, "emails:send", 2)
```

#### <a id="bus-fake-assertnotdispatched"></a>bus.Fake.AssertNotDispatched

AssertNotDispatched fails if the given job type was dispatched.

```go
fake := bus.NewFake()
fake.AssertNotDispatched(nil, "emails:send")
```

#### <a id="bus-fake-assertnothingbatched"></a>bus.Fake.AssertNothingBatched

AssertNothingBatched fails if any batch was recorded.

```go
fake := bus.NewFake()
fake.AssertNothingBatched(nil)
```

#### <a id="bus-fake-assertnothingdispatched"></a>bus.Fake.AssertNothingDispatched

AssertNothingDispatched fails if any job was dispatched.

```go
fake := bus.NewFake()
fake.AssertNothingDispatched(nil)
```

#### <a id="bus-fake-batch"></a>bus.Fake.Batch

Batch records a batch specification.

```go
fake := bus.NewFake()
_, _ = fake.Batch(
	bus.NewJob("a", nil),
	bus.NewJob("b", nil),
).Dispatch(context.Background())
```

#### <a id="bus-fake-chain"></a>bus.Fake.Chain

Chain records a chain specification.

```go
fake := bus.NewFake()
_, _ = fake.Chain(
	bus.NewJob("a", nil),
	bus.NewJob("b", nil),
).Dispatch(context.Background())
```

#### <a id="bus-fake-dispatch"></a>bus.Fake.Dispatch

Dispatch records a dispatched job.

```go
fake := bus.NewFake()
_, _ = fake.Dispatch(context.Background(), bus.NewJob("emails:send", nil))
```


## queue API

### Constructors

#### <a id="queue-new"></a>queue.New

New creates a queue based on Config.Driver.

```go
q, err := queue.NewSync()
if err != nil {
	return
}
type EmailPayload struct {
	ID int `json:"id"`
}
q.Register("emails:send", func(ctx context.Context, task queue.Task) error {
	var payload EmailPayload
	if err := task.Bind(&payload); err != nil {
		return err
	}
	return nil
})
defer q.Shutdown(context.Background())
	context.Background(),
	queue.NewTask("emails:send").
		Payload(EmailPayload{ID: 1}).
		OnQueue("default"),
)
```

#### <a id="queue-newdatabase"></a>queue.NewDatabase

NewDatabase creates a SQL-backed queue runtime.

```go
q, err := queue.NewDatabase("sqlite", "file:queue.db?_busy_timeout=5000")
if err != nil {
	return
}
```

#### <a id="queue-newnats"></a>queue.NewNATS

NewNATS creates a NATS-backed queue runtime.

```go
q, err := queue.NewNATS("nats://127.0.0.1:4222")
if err != nil {
	return
}
```

#### <a id="queue-newnull"></a>queue.NewNull

NewNull creates a drop-only queue runtime.

```go
q, err := queue.NewNull()
if err != nil {
	return
}
```

#### <a id="queue-newqueuewithdefaults"></a>queue.NewQueueWithDefaults

NewQueueWithDefaults creates a queue runtime and sets the default queue name.

```go
q, err := queue.NewQueueWithDefaults("critical", queue.Config{
	Driver: queue.DriverSync,
})
if err != nil {
	return
}
type EmailPayload struct {
	ID int `json:"id"`
}
q.Register("emails:send", func(ctx context.Context, task queue.Task) error {
	var payload EmailPayload
	if err := task.Bind(&payload); err != nil {
		return err
	}
	return nil
})
defer q.Shutdown(context.Background())
```

#### <a id="queue-newrabbitmq"></a>queue.NewRabbitMQ

NewRabbitMQ creates a RabbitMQ-backed queue runtime.

```go
q, err := queue.NewRabbitMQ("amqp://guest:guest@127.0.0.1:5672/")
if err != nil {
	return
}
```

#### <a id="queue-newredis"></a>queue.NewRedis

NewRedis creates a Redis-backed queue runtime.

```go
q, err := queue.NewRedis("127.0.0.1:6379")
if err != nil {
	return
}
```

#### <a id="queue-newsqs"></a>queue.NewSQS

NewSQS creates an SQS-backed queue runtime.

```go
q, err := queue.NewSQS("us-east-1")
if err != nil {
	return
}
```

#### <a id="queue-newstatscollector"></a>queue.NewStatsCollector

NewStatsCollector creates an event collector for queue counters.

```go
collector := queue.NewStatsCollector()
```

#### <a id="queue-newsync"></a>queue.NewSync

NewSync creates a synchronous in-process queue runtime.

```go
q, err := queue.NewSync()
if err != nil {
	return
}
```

#### <a id="queue-newworkerpool"></a>queue.NewWorkerpool

NewWorkerpool creates an in-process workerpool queue runtime.

```go
q, err := queue.NewWorkerpool()
if err != nil {
	return
}
```

### Observability

#### <a id="queue-statssnapshot-active"></a>queue.StatsSnapshot.Active

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

#### <a id="queue-statssnapshot-archived"></a>queue.StatsSnapshot.Archived

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

#### <a id="queue-statssnapshot-failed"></a>queue.StatsSnapshot.Failed

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

#### <a id="queue-multiobserver"></a>queue.MultiObserver

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

#### <a id="queue-observer-observe"></a>queue.Observer.Observe

Observe handles a queue runtime event.

#### <a id="queue-observerfunc-observe"></a>queue.ObserverFunc.Observe

Observe calls the wrapped function.

```go
logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
observer := queue.ObserverFunc(func(event queue.Event) {
	logger.Info("queue event",
		"kind", event.Kind,
		"driver", event.Driver,
		"queue", event.Queue,
		"task_type", event.TaskType,
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
	TaskType: "emails:send",
})
```

#### <a id="queue-statscollector-observe"></a>queue.StatsCollector.Observe

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

#### <a id="queue-pausequeue"></a>queue.PauseQueue

PauseQueue pauses queue consumption for drivers that support it.

```go
q, _ := queue.NewSync()
snapshot, _ := queue.SnapshotQueue(context.Background(), q, nil)
fmt.Println(snapshot.Paused("default"))
// Output: 1
```

#### <a id="queue-statssnapshot-paused"></a>queue.StatsSnapshot.Paused

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

#### <a id="queue-statssnapshot-pending"></a>queue.StatsSnapshot.Pending

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

#### <a id="queue-statssnapshot-processed"></a>queue.StatsSnapshot.Processed

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

#### <a id="queue-statssnapshot-queue"></a>queue.StatsSnapshot.Queue

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

#### <a id="queue-statssnapshot-queues"></a>queue.StatsSnapshot.Queues

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

#### <a id="queue-resumequeue"></a>queue.ResumeQueue

ResumeQueue resumes queue consumption for drivers that support it.

```go
q, _ := queue.NewSync()
snapshot, _ := queue.SnapshotQueue(context.Background(), q, nil)
fmt.Println(snapshot.Paused("default"))
// Output: 0
```

#### <a id="queue-statssnapshot-retrycount"></a>queue.StatsSnapshot.RetryCount

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

#### <a id="queue-statssnapshot-scheduled"></a>queue.StatsSnapshot.Scheduled

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

#### <a id="queue-statscollector-snapshot"></a>queue.StatsCollector.Snapshot

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
	TaskKey: "task-1",
	Time:   time.Now(),
})
collector.Observe(queue.Event{
	Kind:     queue.EventProcessSucceeded,
	Driver:   queue.DriverSync,
	Queue:    "default",
	TaskKey:  "task-1",
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

#### <a id="queue-snapshotqueue"></a>queue.SnapshotQueue

SnapshotQueue returns driver-native stats, falling back to collector data.

```go
q, _ := queue.NewSync()
snapshot, _ := queue.SnapshotQueue(context.Background(), q, nil)
_, ok := snapshot.Queue("default")
fmt.Println(ok)
// Output: true
```

#### <a id="queue-supportsnativestats"></a>queue.SupportsNativeStats

SupportsNativeStats reports whether a queue runtime exposes native stats snapshots.

```go
q, _ := queue.NewSync()
fmt.Println(queue.SupportsNativeStats(q))
// Output: true
```

#### <a id="queue-supportspause"></a>queue.SupportsPause

SupportsPause reports whether a queue runtime supports PauseQueue/ResumeQueue.

```go
q, _ := queue.NewSync()
fmt.Println(queue.SupportsPause(q))
// Output: true
```

#### <a id="queue-statssnapshot-throughput"></a>queue.StatsSnapshot.Throughput

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

### Other

#### <a id="queue-fakequeue-dispatch"></a>queue.FakeQueue.Dispatch

Dispatch submits a typed job payload using the default queue.

#### <a id="queue-queue-dispatch"></a>queue.Queue.Dispatch

Dispatch submits a typed job payload using the default queue.

#### <a id="queue-queue-dispatchctx"></a>queue.Queue.DispatchCtx

DispatchCtx submits a typed job payload using the provided context.

#### <a id="queue-fakequeue-driver"></a>queue.FakeQueue.Driver

Driver returns the active queue driver.

#### <a id="queue-queue-driver"></a>queue.Queue.Driver

Driver returns the active queue driver.

#### <a id="queue-channelobserver-observe"></a>queue.ChannelObserver.Observe

Observe forwards an event to the configured channel.

#### <a id="queue-fakequeue-register"></a>queue.FakeQueue.Register

Register associates a handler with a task type.

#### <a id="queue-queue-register"></a>queue.Queue.Register

Register associates a handler with a task type.

#### <a id="queue-fakequeue-shutdown"></a>queue.FakeQueue.Shutdown

Shutdown drains running work and releases resources.

#### <a id="queue-queue-shutdown"></a>queue.Queue.Shutdown

Shutdown drains running work and releases resources.

#### <a id="queue-fakequeue-startworkers"></a>queue.FakeQueue.StartWorkers

StartWorkers starts worker execution.

#### <a id="queue-queue-startworkers"></a>queue.Queue.StartWorkers

StartWorkers starts worker execution.

#### <a id="queue-queue-workers"></a>queue.Queue.Workers

Workers sets desired worker concurrency before StartWorkers.

### Task

#### <a id="queue-task-backoff"></a>queue.Task.Backoff

Backoff sets delay between retries.

```go
task := queue.NewTask("emails:send").Backoff(500 * time.Millisecond)
```

#### <a id="queue-task-bind"></a>queue.Task.Bind

Bind unmarshals task payload JSON into dst.

```go
type EmailPayload struct {
	ID int `json:"id"`
}
task := queue.NewTask("emails:send").Payload(EmailPayload{ID: 1})
var payload EmailPayload
```

#### <a id="queue-task-delay"></a>queue.Task.Delay

Delay defers execution by duration.

```go
task := queue.NewTask("emails:send").Delay(300 * time.Millisecond)
```

#### <a id="queue-newtask"></a>queue.NewTask

NewTask creates a task value with a required task type.

```go
task := queue.NewTask("emails:send")
```

#### <a id="queue-task-onqueue"></a>queue.Task.OnQueue

OnQueue sets the target queue name.

```go
task := queue.NewTask("emails:send").OnQueue("critical")
```

#### <a id="queue-task-payload"></a>queue.Task.Payload

Payload sets task payload from common value types.

_Example: payload bytes_

```go
taskBytes := queue.NewTask("emails:send").Payload([]byte(`{"id":1}`))
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
taskStruct := queue.NewTask("emails:send").Payload(EmailPayload{
	ID:   1,
	To:   "user@example.com",
	Meta: Meta{Nested: true},
})
```

_Example: payload map_

```go
taskMap := queue.NewTask("emails:send").Payload(map[string]any{
	"id":  1,
	"to":  "user@example.com",
	"meta": map[string]any{"nested": true},
})
```

#### <a id="queue-task-payloadbytes"></a>queue.Task.PayloadBytes

PayloadBytes returns a copy of task payload bytes.

```go
task := queue.NewTask("emails:send").Payload([]byte(`{"id":1}`))
payload := task.PayloadBytes()
```

#### <a id="queue-task-payloadjson"></a>queue.Task.PayloadJSON

PayloadJSON marshals payload as JSON.

```go
task := queue.NewTask("emails:send").PayloadJSON(map[string]int{"id": 1})
```

#### <a id="queue-task-retry"></a>queue.Task.Retry

Retry sets max retry attempts.

```go
task := queue.NewTask("emails:send").Retry(4)
```

#### <a id="queue-task-timeout"></a>queue.Task.Timeout

Timeout sets per-task execution timeout.

```go
task := queue.NewTask("emails:send").Timeout(10 * time.Second)
```

#### <a id="queue-task-uniquefor"></a>queue.Task.UniqueFor

UniqueFor enables uniqueness dedupe within the given TTL.

```go
task := queue.NewTask("emails:send").UniqueFor(45 * time.Second)
```

### Testing

#### <a id="queue-fakequeue-assertcount"></a>queue.FakeQueue.AssertCount

AssertCount fails when dispatch count is not expected.

```go
fake := queue.NewFake()
fake.AssertCount(nil, 1)
```

#### <a id="queue-fakequeue-assertdispatched"></a>queue.FakeQueue.AssertDispatched

AssertDispatched fails when taskType was not dispatched.

```go
fake := queue.NewFake()
fake.AssertDispatched(nil, "emails:send")
```

#### <a id="queue-fakequeue-assertdispatchedon"></a>queue.FakeQueue.AssertDispatchedOn

AssertDispatchedOn fails when taskType was not dispatched on queueName.

```go
fake := queue.NewFake()
	queue.NewTask("emails:send").
		OnQueue("critical"),
)
fake.AssertDispatchedOn(nil, "critical", "emails:send")
```

#### <a id="queue-fakequeue-assertdispatchedtimes"></a>queue.FakeQueue.AssertDispatchedTimes

AssertDispatchedTimes fails when taskType dispatch count does not match expected.

```go
fake := queue.NewFake()
fake.AssertDispatchedTimes(nil, "emails:send", 2)
```

#### <a id="queue-fakequeue-assertnotdispatched"></a>queue.FakeQueue.AssertNotDispatched

AssertNotDispatched fails when taskType was dispatched.

```go
fake := queue.NewFake()
fake.AssertNotDispatched(nil, "emails:cancel")
```

#### <a id="queue-fakequeue-assertnothingdispatched"></a>queue.FakeQueue.AssertNothingDispatched

AssertNothingDispatched fails when any dispatch was recorded.

```go
fake := queue.NewFake()
fake.AssertNothingDispatched(nil)
```

#### <a id="queue-fakequeue-dispatchctx"></a>queue.FakeQueue.DispatchCtx

DispatchCtx submits a typed job payload using the provided context.

```go
fake := queue.NewFake()
ctx := context.Background()
err := fake.DispatchCtx(ctx, queue.NewTask("emails:send").OnQueue("default"))
fmt.Println(err == nil)
// Output: true
```

#### <a id="queue-newfake"></a>queue.NewFake

NewFake creates a queue fake that records dispatches and provides assertions.

```go
fake := queue.NewFake()
	queue.NewTask("emails:send").
		Payload(map[string]any{"id": 1}).
		OnQueue("critical"),
)
records := fake.Records()
fmt.Println(len(records), records[0].Queue, records[0].Task.Type)
// Output: 1 critical emails:send
```

#### <a id="queue-fakequeue-records"></a>queue.FakeQueue.Records

Records returns a copy of all dispatch records.

```go
fake := queue.NewFake()
records := fake.Records()
fmt.Println(len(records), records[0].Task.Type)
// Output: 1 emails:send
```

#### <a id="queue-fakequeue-reset"></a>queue.FakeQueue.Reset

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

#### <a id="queue-fakequeue-workers"></a>queue.FakeQueue.Workers

Workers sets desired worker concurrency before StartWorkers.

```go
fake := queue.NewFake()
q := fake.Workers(4)
fmt.Println(q != nil)
// Output: true
```
<!-- api:embed:end -->
