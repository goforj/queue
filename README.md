<p align="center">
  <img src="./docs/images/logo.png?v=1" width="420" alt="queue logo">
</p>

<p align="center">
    queue gives your services one queue API with Redis, SQL, NATS, and in-process drivers.
</p>

<p align="center">
    <a href="https://pkg.go.dev/github.com/goforj/queue"><img src="https://pkg.go.dev/badge/github.com/goforj/queue.svg" alt="Go Reference"></a>
    <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-blue.svg" alt="License: MIT"></a>
    <a href="https://github.com/goforj/queue/actions"><img src="https://github.com/goforj/queue/actions/workflows/test.yml/badge.svg" alt="Go Test"></a>
    <a href="https://golang.org"><img src="https://img.shields.io/badge/go-1.23+-blue?logo=go" alt="Go version"></a>
    <img src="https://img.shields.io/github/v/tag/goforj/queue?label=version&sort=semver" alt="Latest tag">
    <a href="https://goreportcard.com/report/github.com/goforj/queue"><img src="https://goreportcard.com/badge/github.com/goforj/queue" alt="Go Report Card"></a>
<!-- test-count:embed:start -->
    <img src="https://img.shields.io/badge/tests-127-brightgreen" alt="Tests">
<!-- test-count:embed:end -->
</p>

## What queue is

queue is a backend-agnostic job queue runtime. Your application code depends on `queue.Queue` and fluent `queue.Task` values. The driver decides whether work runs via Redis/Asynq, a SQL table, NATS, an in-process worker pool, or synchronously in the caller.

## Drivers

### Redis (production)
- Uses Asynq for durable queues, retries, delays, and uniqueness.
- Best for production or any place you already run Redis.

### Database (PostgreSQL, MySQL, SQLite)
- Persists jobs in a lightweight `queue_jobs` table and polls for work.
- Auto-migrates schema by default in worker/database execution paths.
- Supports retries, per-task timeouts, backoff, delays, and uniqueness.

### NATS
- Publishes tasks over NATS subjects with queue-based routing.
- Use `queue.Worker` to consume and run handlers.

### Workerpool (in-process async)
- Runs tasks on background goroutines with a bounded channel.
- Call `Start` once per process and `Shutdown` on exit to drain work.
- Honors task metadata such as `Delay`, `Timeout`, `Retry`, `Backoff`, and `UniqueFor`.

### Sync (in-process inline)
- Executes handlers immediately in the caller goroutine.
- Useful for tests or very small services without background workers.

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
| **OnQueue(name)** | Empty | Sets target queue name. For Redis/Database/NATS, enqueue requires an explicit queue. Sync/Workerpool do not use queue routing semantics. |
| **Timeout(d)** | Unset | Applies per-task timeout. Workerpool may still apply `WorkerConfig.DefaultTaskTimeout` when task timeout is not set. |
| **Retry(n)** | `0` | Sets max retries (attempts = `1 + n`). |
| **Backoff(d)** | Unset | Wait duration between retries. Redis enqueue returns `ErrBackoffUnsupported`. |
| **Delay(d)** | `0` | Schedules future execution; `0` means run immediately. |
| **UniqueFor(ttl)** | `0` | Deduplicates by `Type + Payload` for the TTL window. |

## How workers attach

### Sync driver

No separate worker exists. The handler runs inline during `Enqueue`.

```go
q, _ := queue.New(queue.Config{Driver: queue.DriverSync})
q.Register("emails:send", func(ctx context.Context, task queue.Task) error {
    return sendEmail(ctx, task.PayloadBytes())
})
_ = q.Enqueue(context.Background(), queue.NewTask("emails:send").Payload([]byte("hello")).OnQueue("default"))
```

### Workerpool driver

The worker is in-process. Attach by registering handlers and starting the queue runtime.

```go
q, _ := queue.New(queue.Config{
    Driver: queue.DriverWorkerpool,
})
q.Register("emails:send", func(ctx context.Context, task queue.Task) error {
    return sendEmail(ctx, task.PayloadBytes())
})
_ = q.Start(context.Background())
defer q.Shutdown(context.Background())

task := queue.NewTask("emails:send").
    Payload(map[string]any{"id": 456}).
    OnQueue("default").
    Retry(2).
    Backoff(250 * time.Millisecond)

_ = q.Enqueue(context.Background(), task)
```

### Database driver

Same attachment model as workerpool, but jobs are durable in SQL.

```go
q, _ := queue.New(queue.Config{
    Driver: queue.DriverDatabase,
    DatabaseDriver: "sqlite",
    DatabaseDSN: "file:queue.db?_busy_timeout=5000",
})
q.Register("emails:send", func(ctx context.Context, task queue.Task) error {
    return sendEmail(ctx, task.PayloadBytes())
})
_ = q.Start(context.Background())
defer q.Shutdown(context.Background())

task := queue.NewTask("emails:send").
    Payload(map[string]any{"id": 789}).
    OnQueue("critical").
    Delay(300 * time.Millisecond).
    Timeout(10 * time.Second).
    Retry(4).
    Backoff(500 * time.Millisecond).
    UniqueFor(45 * time.Second)

_ = q.Enqueue(context.Background(), task)
```

### Redis driver

Attach workers through `queue.Worker` so handlers stay on the queue abstraction.

```go
worker, _ := queue.NewWorker(queue.WorkerConfig{
    Driver: queue.DriverRedis,
    RedisAddr: "127.0.0.1:6379",
    Workers: 10,
})
worker.Register("emails:send", func(ctx context.Context, task queue.Task) error {
    return sendEmail(ctx, task.PayloadBytes())
})
_ = worker.Start()
defer worker.Shutdown()
```

### NATS driver

Attach workers through `queue.Worker` and publish with `queue.Queue`.

```go
worker, _ := queue.NewWorker(queue.WorkerConfig{
    Driver: queue.DriverNATS,
    NATSURL: "nats://127.0.0.1:4222",
})
worker.Register("emails:send", func(ctx context.Context, task queue.Task) error {
    return sendEmail(ctx, task.PayloadBytes())
})
_ = worker.Start()
defer worker.Shutdown()
```

## Running workers and shutdown

- Workerpool: call `Start` once; `Shutdown` drains in-flight and delayed tasks with context timeouts respected.
- Database: `Start` spins worker goroutines; `Enqueue` will auto-start if needed. `Shutdown` waits for workers and closes owned DB handles.
- Redis: call `worker.Start()` to begin consuming and `worker.Shutdown()` for graceful stop.
- NATS: call `worker.Start()` to subscribe and `worker.Shutdown()` to drain/close.

## Driver selection via config

Use `queue.Config` with `New` and `queue.WorkerConfig` with `NewWorker`.  
These are intentionally separate so enqueue-side settings and worker execution settings are documented independently.

### Config support matrix

Legend: `✓` supported, `-` ignored, `o` optional.

| Config field | Sync | Workerpool | Database | Redis | NATS | Notes |
|--:|:--:|:--:|:--:|:--:|:--:|:--|
| **Driver** | ✓ | ✓ | ✓ | ✓ | ✓ | Selects backend. |
| **DefaultQueue** | - | - | ✓ | - | - | Queue runtime config field; task-level `OnQueue(...)` controls enqueue target. |
| **Database** | - | - | o | - | - | Existing `*sql.DB` handle; if set, driver/DSN can be omitted. |
| **DatabaseDriver** | - | - | ✓ | - | - | `sqlite`, `mysql`, or `pgx`. |
| **DatabaseDSN** | - | - | ✓ | - | - | Connection string for database driver. |
| **RedisAddr** | - | - | - | ✓ | - | Required for Redis queue enqueueing. |
| **RedisPassword** | - | - | - | o | - | Redis auth password. |
| **RedisDB** | - | - | - | o | - | Redis logical DB index. |
| **NATSURL** | - | - | - | - | ✓ | Required for NATS queue enqueueing. |

### WorkerConfig support matrix

Legend: `✓` supported, `-` ignored, `o` optional.

| WorkerConfig field | Sync | Workerpool | Database | Redis | NATS | Notes |
|--:|:--:|:--:|:--:|:--:|:--:|:--|
| **Driver** | ✓ | ✓ | ✓ | ✓ | ✓ | Selects backend. |
| **Workers** | - | ✓ | ✓ | ✓ | - | Redis uses this for Asynq worker concurrency. |
| **QueueCapacity** | - | ✓ | - | - | - | In-memory pending queue capacity for workerpool workers. |
| **DefaultTaskTimeout** | - | ✓ | - | - | - | Workerpool default task timeout unless task overrides with `Timeout(...)`. |
| **PollInterval** | - | - | ✓ | - | - | Job polling interval for database worker loop. |
| **DefaultQueue** | - | - | ✓ | - | - | Queue runtime config field; task-level `OnQueue(...)` controls enqueue target. |
| **AutoMigrate** | - | - | ✓ | - | - | Creates/updates DB schema on start. |
| **Database** | - | - | o | - | - | Existing `*sql.DB` handle; if set, driver/DSN can be omitted. |
| **DatabaseDriver** | - | - | ✓ | - | - | `sqlite`, `mysql`, or `pgx`. |
| **DatabaseDSN** | - | - | ✓ | - | - | Connection string for database worker. |
| **RedisAddr** | - | - | - | ✓ | - | Required for Redis worker startup. |
| **RedisPassword** | - | - | - | o | - | Redis auth password. |
| **RedisDB** | - | - | - | o | - | Redis logical DB index. |
| **NATSURL** | - | - | - | - | ✓ | Required for NATS worker startup. |

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
