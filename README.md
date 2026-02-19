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
    <img src="https://img.shields.io/badge/tests-133-brightgreen" alt="Tests">
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

## Drivers

| Driver | Mode | Durable | Async | Delay | Unique | Backoff | Timeout |
| ---: | :--- | :---: | :---: | :---: | :---: | :---: | :---: |
| <img src="https://img.shields.io/badge/redis-%23DC382D?logo=redis&logoColor=white" alt="Redis"> | Redis/Asynq | ✓ | ✓ | ✓ | ✓ | - | ✓ |
| <img src="https://img.shields.io/badge/database-%23336791?logo=postgresql&logoColor=white" alt="Database"> | SQL (pg/mysql/sqlite) | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| <img src="https://img.shields.io/badge/workerpool-%23696969?logo=clockify&logoColor=white" alt="Workerpool"> | In-process pool | - | ✓ | ✓ | ✓ | ✓ | ✓ |
| <img src="https://img.shields.io/badge/sync-%23999999?logo=gnometerminal&logoColor=white" alt="Sync"> | Inline (caller) | - | - | - | ✓ | - | ✓ |
| <img src="https://img.shields.io/badge/rabbitmq-%23FF6600?logo=rabbitmq&logoColor=white" alt="RabbitMQ"> | Broker target | - | ✓ | ✓ | ✓ | ✓ | ✓ |
| <img src="https://img.shields.io/badge/SQS-FF9900?style=flat" alt="SQS"> | Broker target | - | ✓ | ✓ | ✓ | ✓ | ✓ |
| <img src="https://img.shields.io/badge/NATS-007ACC?style=flat" alt="NATS"> | Broker target | - | ✓ | ✓ | ✓ | ✓ | ✓ |

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
    // Create a queue runtime (workerpool driver for in-process async execution).
    q, _ := queue.New(queue.Config{
        Driver: queue.DriverWorkerpool,
    })

    // Register a handler for the task type.
    q.Register("emails:send", emailHandler)
    
    // Alternate inline style:
    // q.Register("emails:send", func(ctx context.Context, task queue.Task) error {
    //     var payload EmailPayload
    //     if err := task.Bind(&payload); err != nil {
    //         return err
    //     }
    //     _ = payload
    //     return nil
    // })

    // Start workers and ensure graceful shutdown.
    _ = q.Start(context.Background())
    defer q.Shutdown(context.Background())

    // Build a task with payload and enqueue behavior.
    task := queue.NewTask("emails:send").
        Payload(EmailPayload{
            ID: 123,
            To: "user@example.com",
        }).
        OnQueue("critical").
        Delay(5 * time.Second).
        Timeout(20 * time.Second).
        Retry(3)

    // Enqueue the task for execution.
    _ = q.Enqueue(context.Background(), task)
}
```

Switch to Redis without changing job code:

```go
q, _ := queue.New(queue.Config{
    Driver: queue.DriverRedis,
    RedisAddr: "127.0.0.1:6379",
})
```

Use SQL for durable local queue runtimeueues:

```go
q, _ := queue.New(queue.Config{
    Driver: queue.DriverDatabase,
    DatabaseDriver: "sqlite",
    DatabaseDSN: "file:queue.db?_busy_timeout=5000",
})
```

## Task builder options

| Method | Default | Behavior |
|--:|:--|:--|
| **OnQueue(name)** | Empty | Sets target queue name. For Redis/Database/NATS/SQS/RabbitMQ, enqueue requires an explicit queue. Sync/Workerpool do not use queue routing semantics. |
| **Timeout(d)** | Unset | Applies per-task timeout. Workerpool may still apply `WorkerConfig.DefaultTaskTimeout` when task timeout is not set. |
| **Retry(n)** | `0` | Sets max retries (attempts = `1 + n`). |
| **Backoff(d)** | Unset | Wait duration between retries. Redis enqueue returns `ErrBackoffUnsupported`. |
| **Delay(d)** | `0` | Schedules future execution; `0` means run immediately. |
| **UniqueFor(ttl)** | `0` | Deduplicates by `Type + Payload` for the TTL window. |

## Observability quick guide

Attach an observer when creating a queue or worker. Use `StatsCollector` for in-memory counters and throughput windows.

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
		logger.Info("Enqueued job", "msg", fmt.Sprintf("Accepted %s", taskInfo), "scheduled", event.Scheduled, "at", event.Time.Format(time.RFC3339Nano))
	case queue.EventEnqueueRejected:
		logger.Error("Failed to enqueue job", "msg", fmt.Sprintf("Rejected %s", taskInfo), "error", event.Err)
	case queue.EventEnqueueDuplicate:
		logger.Warn("Skipped duplicate job", "msg", fmt.Sprintf("Duplicate %s", taskInfo))
	case queue.EventEnqueueCanceled:
		logger.Warn("Canceled enqueue", "msg", fmt.Sprintf("Canceled %s", taskInfo), "error", event.Err)
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

If you prefer zerolog, implement the same `Observer` interface in a small adapter and set it on `Config.Observer` / `WorkerConfig.Observer`.

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
| EventEnqueueAccepted | Task was accepted by enqueue. |
| EventEnqueueRejected | Task enqueue failed with an error. |
| EventEnqueueDuplicate | Task enqueue was rejected as duplicate (`UniqueFor`). |
| EventEnqueueCanceled | Enqueue was canceled by context timeout/cancelation. |
| EventProcessStarted | Worker started handling a task. |
| EventProcessSucceeded | Worker completed a task successfully. |
| EventProcessFailed | Worker attempt failed. |
| EventProcessRetried | Failed attempt was requeued for another attempt. |
| EventProcessArchived | Failed task reached terminal failure (no retries left). |
| EventQueuePaused | Queue consumption was paused. |
| EventQueueResumed | Queue consumption was resumed. |

For full field-level semantics and guarantees, see [`docs/events.md`](docs/events.md).

## How workers attach

Two attachment patterns:

- In-process drivers (`sync`, `workerpool`, `database`): register on `Queue`.
- Broker-backed drivers (`redis`, `nats`, `sqs`, `rabbitmq`): register on `Worker`, enqueue on `Queue`.

In-process example:

```go
q, _ := queue.New(queue.Config{Driver: queue.DriverDatabase, DatabaseDriver: "sqlite", DatabaseDSN: "file:queue.db"})
q.Register("emails:send", func(ctx context.Context, task queue.Task) error {
	return sendEmail(ctx, task.PayloadBytes())
})
_ = q.Start(context.Background())
defer q.Shutdown(context.Background())
```

Broker-backed example:

```go
q, _ := queue.New(queue.Config{Driver: queue.DriverRedis, RedisAddr: "127.0.0.1:6379"})
worker, _ := queue.NewWorker(queue.WorkerConfig{Driver: queue.DriverRedis, RedisAddr: "127.0.0.1:6379", Workers: 10})
worker.Register("emails:send", func(ctx context.Context, task queue.Task) error {
	return sendEmail(ctx, task.PayloadBytes())
})
_ = worker.Start()
defer worker.Shutdown()
```

Driver quick map:

| Driver | Register handlers on |
|:--|:--|
| sync | `Queue` |
| workerpool | `Queue` |
| database | `Queue` |
| redis | `Worker` |
| nats | `Worker` |
| sqs | `Worker` |
| rabbitmq | `Worker` |

## Running workers and shutdown

- Workerpool: call `Start` once; `Shutdown` drains in-flight and delayed tasks with context timeouts respected.
- Database: `Start` spins worker goroutines; `Enqueue` will auto-start if needed. `Shutdown` waits for workers and closes owned DB handles.
- Redis: call `worker.Start()` to begin consuming and `worker.Shutdown()` for graceful stop.
- NATS: call `worker.Start()` to subscribe and `worker.Shutdown()` to drain/close.
- SQS: call `worker.Start()` to poll queues and `worker.Shutdown()` for graceful stop.
- RabbitMQ: call `worker.Start()` to consume queues and `worker.Shutdown()` for graceful stop.

## Driver selection via config

Use `queue.Config` with `New` and `queue.WorkerConfig` with `NewWorker`.  
These are intentionally separate so enqueue-side settings and worker execution settings are documented independently.

### Config support matrix

Legend: `✓` supported, `-` ignored, `o` optional.

| Config field | Sync | Workerpool | Database | Redis | NATS | SQS | RabbitMQ | Notes |
|--:|:--:|:--:|:--:|:--:|:--:|:--:|:--:|:--|
| **Driver** | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | Selects backend. |
| **DefaultQueue** | - | - | ✓ | - | - | - | Queue runtime config field; task-level `OnQueue(...)` controls enqueue target. |
| **Database** | - | - | o | - | - | - | Existing `*sql.DB` handle; if set, driver/DSN can be omitted. |
| **DatabaseDriver** | - | - | ✓ | - | - | - | `sqlite`, `mysql`, or `pgx`. |
| **DatabaseDSN** | - | - | ✓ | - | - | - | Connection string for database driver. |
| **RedisAddr** | - | - | - | ✓ | - | - | Required for Redis queue enqueueing. |
| **RedisPassword** | - | - | - | o | - | - | Redis auth password. |
| **RedisDB** | - | - | - | o | - | - | Redis logical DB index. |
| **NATSURL** | - | - | - | - | ✓ | - | Required for NATS queue enqueueing. |
| **SQSRegion** | - | - | - | - | - | o | AWS region (defaults to `us-east-1`). |
| **SQSEndpoint** | - | - | - | - | - | o | Override endpoint (localstack/testing). |
| **SQSAccessKey** | - | - | - | - | - | o | Static access key (optional). |
| **SQSSecretKey** | - | - | - | - | - | o | - | Static secret key (optional). |
| **RabbitMQURL** | - | - | - | - | - | - | ✓ | Required for RabbitMQ queue enqueueing. |

### WorkerConfig support matrix

Legend: `✓` supported, `-` ignored, `o` optional.

| WorkerConfig field | Sync | Workerpool | Database | Redis | NATS | SQS | RabbitMQ | Notes |
|--:|:--:|:--:|:--:|:--:|:--:|:--:|:--:|:--|
| **Driver** | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | Selects backend. |
| **Workers** | - | ✓ | ✓ | ✓ | - | - | Redis uses this for Asynq worker concurrency. |
| **QueueCapacity** | - | ✓ | - | - | - | - | In-memory pending queue capacity for workerpool workers. |
| **DefaultTaskTimeout** | - | ✓ | - | - | - | - | Workerpool default task timeout unless task overrides with `Timeout(...)`. |
| **PollInterval** | - | - | ✓ | - | - | - | Job polling interval for database worker loop. |
| **DefaultQueue** | - | - | ✓ | - | - | ✓ | Queue runtime config field; task-level `OnQueue(...)` controls enqueue target. |
| **AutoMigrate** | - | - | ✓ | - | - | - | Creates/updates DB schema on start. |
| **Database** | - | - | o | - | - | - | Existing `*sql.DB` handle; if set, driver/DSN can be omitted. |
| **DatabaseDriver** | - | - | ✓ | - | - | - | `sqlite`, `mysql`, or `pgx`. |
| **DatabaseDSN** | - | - | ✓ | - | - | - | Connection string for database worker. |
| **RedisAddr** | - | - | - | ✓ | - | - | Required for Redis worker startup. |
| **RedisPassword** | - | - | - | o | - | - | Redis auth password. |
| **RedisDB** | - | - | - | o | - | - | Redis logical DB index. |
| **NATSURL** | - | - | - | - | ✓ | - | Required for NATS worker startup. |
| **SQSRegion** | - | - | - | - | - | o | AWS region (defaults to `us-east-1`). |
| **SQSEndpoint** | - | - | - | - | - | o | Override endpoint (localstack/testing). |
| **SQSAccessKey** | - | - | - | - | - | o | Static access key (optional). |
| **SQSSecretKey** | - | - | - | - | - | o | - | Static secret key (optional). |
| **RabbitMQURL** | - | - | - | - | - | - | ✓ | Required for RabbitMQ worker startup. |

## API reference

The API section below is autogenerated; do not edit between the markers.

<!-- api:embed:start -->

## API Index

| Group | Functions |
|------:|:-----------|
| **Constructors** | [New](#new) [NewWorker](#newworker) |
| **Queue** | [Driver](#driver) [Enqueue](#enqueue) [Register](#register) [Shutdown](#shutdown) [Start](#start) |
| **Task** | [Backoff](#backoff) [Bind](#bind) [Delay](#delay) [NewTask](#newtask) [OnQueue](#onqueue) [Payload](#payload) [PayloadBytes](#payloadbytes) [PayloadJSON](#payloadjson) [Retry](#retry) [Timeout](#timeout) [UniqueFor](#uniquefor) |


## Constructors

### <a id="new"></a>New

New creates a queue based on Config.Driver.

```go
q, err := queue.New(queue.Config{Driver: queue.DriverSync})
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
	_ = payload
	return nil
})
_ = q.Enqueue(
	context.Background(),
	queue.NewTask("emails:send").
		Payload(EmailPayload{ID: 1}).
		OnQueue("default"),
)
```

### <a id="newworker"></a>NewWorker

NewWorker creates a worker based on WorkerConfig.Driver.

```go
worker, err := queue.NewWorker(queue.WorkerConfig{
	Driver: queue.DriverSync,
})
if err != nil {
	return
}
type EmailPayload struct {
	ID int `json:"id"`
}
worker.Register("emails:send", func(ctx context.Context, task queue.Task) error {
	var payload EmailPayload
	if err := task.Bind(&payload); err != nil {
		return err
	}
	_ = payload
	return nil
})
_ = worker.Start()
_ = worker.Shutdown()
```

## Queue

### <a id="driver"></a>Driver

Driver returns the local queue runtime's driver mode.

```go
q, err := queue.New(queue.Config{Driver: queue.DriverSync})
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

### <a id="enqueue"></a>Enqueue

Enqueue schedules or executes a task using the local driver.

```go
q, err := queue.New(queue.Config{Driver: queue.DriverSync})
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
	_ = payload
	return nil
})
task := queue.NewTask("emails:send").
	Payload(EmailPayload{ID: 1}).
	OnQueue("default").
	Delay(10 * time.Millisecond)
_ = q.Enqueue(context.Background(), task)
```

### <a id="register"></a>Register

Register adds a task handler to the local queue runtime.

```go
q, err := queue.New(queue.Config{Driver: queue.DriverSync})
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
	_ = payload
	return nil
})
```

### <a id="shutdown"></a>Shutdown

Shutdown drains delayed and active local workerpool tasks.

```go
q, err := queue.New(queue.Config{
	Driver: queue.DriverWorkerpool,
})
if err != nil {
	return
}
_ = q.Start(context.Background())
_ = q.Shutdown(context.Background())
```

### <a id="start"></a>Start

Start initializes worker goroutines for workerpool mode.

```go
q, err := queue.New(queue.Config{
	Driver: queue.DriverWorkerpool,
})
if err != nil {
	return
}
_ = q.Start(context.Background())
```

## Task

### <a id="backoff"></a>Backoff

Backoff sets delay between retries.

```go
task := queue.NewTask("emails:send").Backoff(500 * time.Millisecond)
_ = task
```

### <a id="bind"></a>Bind

Bind unmarshals task payload JSON into dst.

```go
type EmailPayload struct {
	ID int `json:"id"`
}
task := queue.NewTask("emails:send").Payload(EmailPayload{ID: 1})
var payload EmailPayload
_ = task.Bind(&payload)
```

### <a id="delay"></a>Delay

Delay defers execution by duration.

```go
task := queue.NewTask("emails:send").Delay(300 * time.Millisecond)
_ = task
```

### <a id="newtask"></a>NewTask

NewTask creates a task value with a required task type.

```go
task := queue.NewTask("emails:send")
_ = task
```

### <a id="onqueue"></a>OnQueue

OnQueue sets the target queue name.

```go
task := queue.NewTask("emails:send").OnQueue("critical")
_ = task
```

### <a id="payload"></a>Payload

Payload sets task payload from common value types.

_Example: payload bytes_

```go
taskBytes := queue.NewTask("emails:send").Payload([]byte(`{"id":1}`))
_ = taskBytes
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
_ = taskStruct
```

_Example: payload map_

```go
taskMap := queue.NewTask("emails:send").Payload(map[string]any{
	"id":  1,
	"to":  "user@example.com",
	"meta": map[string]any{"nested": true},
})
_ = taskMap
```

### <a id="payloadbytes"></a>PayloadBytes

PayloadBytes returns a copy of task payload bytes.

```go
task := queue.NewTask("emails:send").Payload([]byte(`{"id":1}`))
payload := task.PayloadBytes()
_ = payload
```

### <a id="payloadjson"></a>PayloadJSON

PayloadJSON marshals payload as JSON.

```go
task := queue.NewTask("emails:send").PayloadJSON(map[string]int{"id": 1})
_ = task
```

### <a id="retry"></a>Retry

Retry sets max retry attempts.

```go
task := queue.NewTask("emails:send").Retry(4)
_ = task
```

### <a id="timeout"></a>Timeout

Timeout sets per-task execution timeout.

```go
task := queue.NewTask("emails:send").Timeout(10 * time.Second)
_ = task
```

### <a id="uniquefor"></a>UniqueFor

UniqueFor enables uniqueness dedupe within the given TTL.

```go
task := queue.NewTask("emails:send").UniqueFor(45 * time.Second)
_ = task
```
<!-- api:embed:end -->
