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
<!-- test-count:embed:start -->
    <img src="https://img.shields.io/badge/tests-183-brightgreen" alt="Tests">
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

| Group | Functions |
|------:|:-----------|
| **Constructors** | [New](#new) [NewDatabase](#newdatabase) [NewFake](#newfake) [NewNATS](#newnats) [NewNull](#newnull) [NewRabbitMQ](#newrabbitmq) [NewRedis](#newredis) [NewSQS](#newsqs) [NewStatsCollector](#newstatscollector) [NewSync](#newsync) [NewWorkerpool](#newworkerpool) |
| **Observability** | [Active](#active) [Archived](#archived) [Failed](#failed) [MultiObserver](#multiobserver) [Observe](#observe) [PauseQueue](#pausequeue) [Paused](#paused) [Pending](#pending) [Processed](#processed) [Queue](#queue) [Queues](#queues) [ResumeQueue](#resumequeue) [Scheduled](#scheduled) [Snapshot](#snapshot) [SnapshotQueue](#snapshotqueue) [SupportsNativeStats](#supportsnativestats) [SupportsPause](#supportspause) [Throughput](#throughput) |
| **Other** | [Dispatch](#dispatch) [DispatchCtx](#dispatchctx) [Driver](#driver) [NewQueueWithDefaults](#newqueuewithdefaults) [Records](#records) [Register](#register) [Reset](#reset) [Shutdown](#shutdown) [StartWorkers](#startworkers) [Workers](#workers) |
| **Queue** | [AssertCount](#assertcount) [AssertDispatched](#assertdispatched) [AssertDispatchedOn](#assertdispatchedon) [AssertDispatchedTimes](#assertdispatchedtimes) [AssertNotDispatched](#assertnotdispatched) [AssertNothingDispatched](#assertnothingdispatched) |
| **Task** | [Backoff](#backoff) [Bind](#bind) [Delay](#delay) [NewTask](#newtask) [OnQueue](#onqueue) [Payload](#payload) [PayloadBytes](#payloadbytes) [PayloadJSON](#payloadjson) [Retry](#retry) [Timeout](#timeout) [UniqueFor](#uniquefor) |


## Constructors

### <a id="new"></a>New

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

### <a id="newdatabase"></a>NewDatabase

NewDatabase creates a SQL-backed queue runtime.

```go
q, err := queue.NewDatabase("sqlite", "file:queue.db?_busy_timeout=5000")
if err != nil {
	return
}
```

### <a id="newfake"></a>NewFake

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

### <a id="newnats"></a>NewNATS

NewNATS creates a NATS-backed queue runtime.

```go
q, err := queue.NewNATS("nats://127.0.0.1:4222")
if err != nil {
	return
}
```

### <a id="newnull"></a>NewNull

NewNull creates a drop-only queue runtime.

```go
q, err := queue.NewNull()
if err != nil {
	return
}
```

### <a id="newrabbitmq"></a>NewRabbitMQ

NewRabbitMQ creates a RabbitMQ-backed queue runtime.

```go
q, err := queue.NewRabbitMQ("amqp://guest:guest@127.0.0.1:5672/")
if err != nil {
	return
}
```

### <a id="newredis"></a>NewRedis

NewRedis creates a Redis-backed queue runtime.

```go
q, err := queue.NewRedis("127.0.0.1:6379")
if err != nil {
	return
}
```

### <a id="newsqs"></a>NewSQS

NewSQS creates an SQS-backed queue runtime.

```go
q, err := queue.NewSQS("us-east-1")
if err != nil {
	return
}
```

### <a id="newstatscollector"></a>NewStatsCollector

NewStatsCollector creates an event collector for queue counters.

```go
collector := queue.NewStatsCollector()
```

### <a id="newsync"></a>NewSync

NewSync creates a synchronous in-process queue runtime.

```go
q, err := queue.NewSync()
if err != nil {
	return
}
```

### <a id="newworkerpool"></a>NewWorkerpool

NewWorkerpool creates an in-process workerpool queue runtime.

```go
q, err := queue.NewWorkerpool()
if err != nil {
	return
}
```

## Observability

### <a id="active"></a>Active

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

### <a id="archived"></a>Archived

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

### <a id="failed"></a>Failed

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

### <a id="multiobserver"></a>MultiObserver

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

### <a id="observe"></a>Observe

Observe calls the wrapped function.

_Example: observer func logging hook_

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

_Example: observe event_

```go
collector := queue.NewStatsCollector()
collector.Observe(queue.Event{
	Kind:   queue.EventEnqueueAccepted,
	Driver: queue.DriverSync,
	Queue:  "default",
	Time:   time.Now(),
})
```

### <a id="pausequeue"></a>PauseQueue

PauseQueue pauses queue consumption for drivers that support it.

```go
q, _ := queue.NewSync()
snapshot, _ := queue.SnapshotQueue(context.Background(), q, nil)
fmt.Println(snapshot.Paused("default"))
// Output: 1
```

### <a id="paused"></a>Paused

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

### <a id="pending"></a>Pending

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

### <a id="processed"></a>Processed

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

### <a id="queue"></a>Queue

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

### <a id="queues"></a>Queues

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

### <a id="resumequeue"></a>ResumeQueue

ResumeQueue resumes queue consumption for drivers that support it.

```go
q, _ := queue.NewSync()
snapshot, _ := queue.SnapshotQueue(context.Background(), q, nil)
fmt.Println(snapshot.Paused("default"))
// Output: 0
```

### <a id="scheduled"></a>Scheduled

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

### <a id="snapshot"></a>Snapshot

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

### <a id="snapshotqueue"></a>SnapshotQueue

SnapshotQueue returns driver-native stats, falling back to collector data.

```go
q, _ := queue.NewSync()
snapshot, _ := queue.SnapshotQueue(context.Background(), q, nil)
_, ok := snapshot.Queue("default")
fmt.Println(ok)
// Output: true
```

### <a id="supportsnativestats"></a>SupportsNativeStats

SupportsNativeStats reports whether a queue runtime exposes native stats snapshots.

```go
q, _ := queue.NewSync()
fmt.Println(queue.SupportsNativeStats(q))
// Output: true
```

### <a id="supportspause"></a>SupportsPause

SupportsPause reports whether a queue runtime supports PauseQueue/ResumeQueue.

```go
q, _ := queue.NewSync()
fmt.Println(queue.SupportsPause(q))
// Output: true
```

### <a id="throughput"></a>Throughput

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

## Other

### <a id="dispatch"></a>Dispatch

Dispatch records a dispatch using background context.

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
task := queue.NewTask("emails:send").
	Payload(EmailPayload{ID: 1}).
	OnQueue("default").
	Delay(10 * time.Millisecond)
```

### <a id="dispatchctx"></a>DispatchCtx

DispatchCtx records a dispatch or returns context error.

### <a id="driver"></a>Driver

Driver returns fake queue driver identity.

```go
q, err := queue.NewSync()
if err != nil {
	return
}
driverAware, ok := q.(interface{ Driver() queue.Driver })
if !ok {
	return
}
fmt.Println(driverAware.Driver())
// Output: sync
```

### <a id="newqueuewithdefaults"></a>NewQueueWithDefaults

NewQueueWithDefaults creates a queue runtime and sets the default queue name.

### <a id="records"></a>Records

Records returns a copy of all dispatch records.

### <a id="register"></a>Register

Register is a no-op for fake queue.

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
```

### <a id="reset"></a>Reset

Reset clears all recorded dispatches.

### <a id="shutdown"></a>Shutdown

Shutdown is a no-op for fake queue.

```go
q, err := queue.NewWorkerpool()
if err != nil {
	return
}
```

### <a id="startworkers"></a>StartWorkers

StartWorkers is a no-op for fake queue.

```go
q, err := queue.NewWorkerpool()
if err != nil {
	return
}
```

### <a id="workers"></a>Workers

Workers is a no-op for fake queue.

## Queue

### <a id="assertcount"></a>AssertCount

AssertCount fails when dispatch count is not expected.

```go
fake := queue.NewFake()
fake.AssertCount(nil, 1)
```

### <a id="assertdispatched"></a>AssertDispatched

AssertDispatched fails when taskType was not dispatched.

```go
fake := queue.NewFake()
fake.AssertDispatched(nil, "emails:send")
```

### <a id="assertdispatchedon"></a>AssertDispatchedOn

AssertDispatchedOn fails when taskType was not dispatched on queueName.

```go
fake := queue.NewFake()
	queue.NewTask("emails:send").
		OnQueue("critical"),
)
fake.AssertDispatchedOn(nil, "critical", "emails:send")
```

### <a id="assertdispatchedtimes"></a>AssertDispatchedTimes

AssertDispatchedTimes fails when taskType dispatch count does not match expected.

```go
fake := queue.NewFake()
fake.AssertDispatchedTimes(nil, "emails:send", 2)
```

### <a id="assertnotdispatched"></a>AssertNotDispatched

AssertNotDispatched fails when taskType was dispatched.

```go
fake := queue.NewFake()
fake.AssertNotDispatched(nil, "emails:cancel")
```

### <a id="assertnothingdispatched"></a>AssertNothingDispatched

AssertNothingDispatched fails when any dispatch was recorded.

```go
fake := queue.NewFake()
fake.AssertNothingDispatched(nil)
```

## Task

### <a id="backoff"></a>Backoff

Backoff sets delay between retries.

```go
task := queue.NewTask("emails:send").Backoff(500 * time.Millisecond)
```

### <a id="bind"></a>Bind

Bind unmarshals task payload JSON into dst.

```go
type EmailPayload struct {
	ID int `json:"id"`
}
task := queue.NewTask("emails:send").Payload(EmailPayload{ID: 1})
var payload EmailPayload
```

### <a id="delay"></a>Delay

Delay defers execution by duration.

```go
task := queue.NewTask("emails:send").Delay(300 * time.Millisecond)
```

### <a id="newtask"></a>NewTask

NewTask creates a task value with a required task type.

```go
task := queue.NewTask("emails:send")
```

### <a id="onqueue"></a>OnQueue

OnQueue sets the target queue name.

```go
task := queue.NewTask("emails:send").OnQueue("critical")
```

### <a id="payload"></a>Payload

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

### <a id="payloadbytes"></a>PayloadBytes

PayloadBytes returns a copy of task payload bytes.

```go
task := queue.NewTask("emails:send").Payload([]byte(`{"id":1}`))
payload := task.PayloadBytes()
```

### <a id="payloadjson"></a>PayloadJSON

PayloadJSON marshals payload as JSON.

```go
task := queue.NewTask("emails:send").PayloadJSON(map[string]int{"id": 1})
```

### <a id="retry"></a>Retry

Retry sets max retry attempts.

_Example: retry_

```go
task := queue.NewTask("emails:send").Retry(4)
```

_Example: retry count getter_

```go
snapshot := queue.StatsSnapshot{
	ByQueue: map[string]queue.QueueCounters{
		"default": {Retry: 1},
	},
}
fmt.Println(snapshot.Retry("default"))
// Output: 1
```

### <a id="timeout"></a>Timeout

Timeout sets per-task execution timeout.

```go
task := queue.NewTask("emails:send").Timeout(10 * time.Second)
```

### <a id="uniquefor"></a>UniqueFor

UniqueFor enables uniqueness dedupe within the given TTL.

```go
task := queue.NewTask("emails:send").UniqueFor(45 * time.Second)
```
<!-- api:embed:end -->
