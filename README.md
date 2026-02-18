<p align="center">
  <img src="./docs/images/logo.png?v=1" width="420" alt="queue logo">
</p>

<p align="center">
    queue gives your services one queue API with Redis, SQL, and in-process drivers.
</p>

<p align="center">
    <a href="https://pkg.go.dev/github.com/goforj/queue"><img src="https://pkg.go.dev/badge/github.com/goforj/queue.svg" alt="Go Reference"></a>
    <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-blue.svg" alt="License: MIT"></a>
    <a href="https://github.com/goforj/queue/actions"><img src="https://github.com/goforj/queue/actions/workflows/test.yml/badge.svg" alt="Go Test"></a>
    <a href="https://golang.org"><img src="https://img.shields.io/badge/go-1.23+-blue?logo=go" alt="Go version"></a>
    <img src="https://img.shields.io/github/v/tag/goforj/queue?label=version&sort=semver" alt="Latest tag">
    <a href="https://goreportcard.com/report/github.com/goforj/queue"><img src="https://goreportcard.com/badge/github.com/goforj/queue" alt="Go Report Card"></a>
<!-- test-count:embed:start -->
    <img src="https://img.shields.io/badge/tests-97-brightgreen" alt="Tests">
<!-- test-count:embed:end -->
</p>

## What queue is

queue is a backend-agnostic job dispatcher. Your application code only depends on `queue.Dispatcher`, `queue.Task`, and fluent enqueue options. The driver decides whether work runs via Redis/Asynq, a SQL table, an in-process worker pool, or synchronously in the caller.

## Drivers

### Redis (production)
- Uses Asynq for durable queues, retries, delays, and uniqueness.
- Best for production or any place you already run Redis.

### Database (PostgreSQL, MySQL, SQLite)
- Persists jobs in a lightweight `queue_jobs` table and polls for work.
- Auto-migrates schema by default in worker/database execution paths.
- Supports retries, per-task timeouts, backoff, delays, and uniqueness.

### Workerpool (in-process async)
- Runs tasks on background goroutines with a bounded channel.
- Call `Start` once per process and `Shutdown` on exit to drain work.
- Honors `WithDelay`, `WithTimeout`, `WithMaxRetry`, `WithBackoff`, and `WithUnique`.

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

func main() {
    dispatcher, _ := queue.NewDispatcher(queue.DispatcherConfig{
        Driver: queue.DriverWorkerpool,
    })

    dispatcher.Register("emails:send", func(ctx context.Context, task queue.Task) error {
        return sendEmail(ctx, task.Payload)
    })

    _ = dispatcher.Start(context.Background())
    defer dispatcher.Shutdown(context.Background())

    _ = dispatcher.Enqueue(context.Background(), queue.Task{
        Type:    "emails:send",
        Payload: []byte("hello"),
    },
        queue.WithQueue("critical"),
        queue.WithDelay(5*time.Second),
        queue.WithTimeout(20*time.Second),
        queue.WithMaxRetry(3),
    )
}
```

Switch to Redis without changing job code:

```go
dispatcher, _ := queue.NewDispatcher(queue.DispatcherConfig{
    Driver: queue.DriverRedis,
    RedisAddr: "127.0.0.1:6379",
})
```

Use SQL for durable local queues:

```go
dispatcher, _ := queue.NewDispatcher(queue.DispatcherConfig{
    Driver: queue.DriverDatabase,
    DatabaseDriver: "sqlite",
    DatabaseDSN: "file:queue.db?_busy_timeout=5000",
})
```

## Enqueue options

- `WithQueue(name)` sets a queue name (Redis: Asynq queue; Database: column; Workerpool/Sync: in-memory grouping).
- `WithTimeout(d)` applies a per-task timeout; respected by all drivers.
- `WithMaxRetry(n)` sets max retries (attempts = 1 + n).
- `WithBackoff(d)` waits between retries; unsupported on Redis dispatcher (returns `ErrBackoffUnsupported`).
- `WithDelay(d)` schedules future execution.
- `WithUnique(ttl)` deduplicates by `Type + Payload` for the TTL window.

## How workers attach

### Sync driver

No separate worker exists. The handler runs inline during `Enqueue`.

```go
dispatcher, _ := queue.NewDispatcher(queue.DispatcherConfig{Driver: queue.DriverSync})
dispatcher.Register("emails:send", func(ctx context.Context, task queue.Task) error {
    return sendEmail(ctx, task.Payload)
})
_ = dispatcher.Enqueue(context.Background(), queue.Task{Type: "emails:send"})
```

### Workerpool driver

The worker is in-process. Attach by registering handlers and starting the dispatcher.

```go
dispatcher, _ := queue.NewDispatcher(queue.DispatcherConfig{
    Driver: queue.DriverWorkerpool,
})
dispatcher.Register("emails:send", func(ctx context.Context, task queue.Task) error {
    return sendEmail(ctx, task.Payload)
})
_ = dispatcher.Start(context.Background())
defer dispatcher.Shutdown(context.Background())
```

### Database driver

Same attachment model as workerpool, but jobs are durable in SQL.

```go
dispatcher, _ := queue.NewDispatcher(queue.DispatcherConfig{
    Driver: queue.DriverDatabase,
    DatabaseDriver: "sqlite",
    DatabaseDSN: "file:queue.db?_busy_timeout=5000",
})
dispatcher.Register("emails:send", func(ctx context.Context, task queue.Task) error {
    return sendEmail(ctx, task.Payload)
})
_ = dispatcher.Start(context.Background())
defer dispatcher.Shutdown(context.Background())
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
    return sendEmail(ctx, task.Payload)
})
_ = worker.Start()
defer worker.Shutdown()
```

## Running workers and shutdown

- Workerpool: call `Start` once; `Shutdown` drains in-flight and delayed tasks with context timeouts respected.
- Database: `Start` spins worker goroutines; `Enqueue` will auto-start if needed. `Shutdown` waits for workers and closes owned DB handles.
- Redis: call `worker.Start()` to begin consuming and `worker.Shutdown()` for graceful stop.

## Driver selection via config

Use `queue.DispatcherConfig` with `NewDispatcher` and `queue.WorkerConfig` with `NewWorker`.  
These are intentionally separate so enqueue-side settings and worker execution settings are documented independently.

### DispatcherConfig support matrix

Legend: `✓` supported, `-` ignored, `o` optional.

| DispatcherConfig field | Sync | Workerpool | Database | Redis | Notes |
|:--|:--:|:--:|:--:|:--:|:--|
| `Driver` | ✓ | ✓ | ✓ | ✓ | Selects backend. |
| `DefaultQueue` | - | - | ✓ | - | Default queue column value in database driver. |
| `Database` | - | - | o | - | Existing `*sql.DB` handle; if set, driver/DSN can be omitted. |
| `DatabaseDriver` | - | - | ✓ | - | `sqlite`, `mysql`, or `pgx`. |
| `DatabaseDSN` | - | - | ✓ | - | Connection string for database driver. |
| `RedisAddr` | - | - | - | ✓ | Required for Redis dispatcher enqueueing. |
| `RedisPassword` | - | - | - | o | Redis auth password. |
| `RedisDB` | - | - | - | o | Redis logical DB index. |

### WorkerConfig support matrix

Legend: `✓` supported, `-` ignored, `o` optional.

| WorkerConfig field | Sync | Workerpool | Database | Redis | Notes |
|:--|:--:|:--:|:--:|:--:|:--|
| `Driver` | ✓ | ✓ | ✓ | ✓ | Selects backend. |
| `Workers` | - | ✓ | ✓ | ✓ | Redis uses this for Asynq worker concurrency. |
| `QueueCapacity` | - | ✓ | - | - | In-memory pending queue capacity for workerpool workers. |
| `DefaultTaskTimeout` | - | ✓ | - | - | Workerpool default task timeout unless task overrides with `WithTimeout`. |
| `PollInterval` | - | - | ✓ | - | Job polling interval for database worker loop. |
| `DefaultQueue` | - | - | ✓ | - | Default queue when none is set on enqueue. |
| `AutoMigrate` | - | - | ✓ | - | Creates/updates DB schema on start. |
| `Database` | - | - | o | - | Existing `*sql.DB` handle; if set, driver/DSN can be omitted. |
| `DatabaseDriver` | - | - | ✓ | - | `sqlite`, `mysql`, or `pgx`. |
| `DatabaseDSN` | - | - | ✓ | - | Connection string for database worker. |
| `RedisAddr` | - | - | - | ✓ | Required for Redis worker startup. |
| `RedisPassword` | - | - | - | o | Redis auth password. |
| `RedisDB` | - | - | - | o | Redis logical DB index. |

## API reference

The API section below is autogenerated; do not edit between the markers.

<!-- api:embed:start -->

## API Index

| Group | Functions |
|------:|:-----------|
| **Constructors** | [NewQueue](#newqueue) [NewWorker](#newworker) |
| **Options** | [WithBackoff](#withbackoff) [WithDelay](#withdelay) [WithMaxRetry](#withmaxretry) [WithQueue](#withqueue) [WithTimeout](#withtimeout) [WithUnique](#withunique) |
| **Queue** | [Dispatch](#dispatch) [Driver](#driver) [Register](#register) [Shutdown](#shutdown) [Start](#start) |


## Constructors

### <a id="newqueue"></a>NewQueue

NewQueue creates a queuer based on QueueConfig.Driver.

```go
queuer, err := queue.NewQueue(queue.QueueConfig{Driver: queue.DriverSync})
if err != nil {
	return
}
queuer.Register("emails:send", func(ctx context.Context, task queue.Task) error {
	return nil
})
_ = queuer.Dispatch("emails:send", []byte(`{"id":1}`))
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
worker.Register("emails:send", func(ctx context.Context, task queue.Task) error {
	return nil
})
_ = worker.Start()
_ = worker.Shutdown()
```

## Options

### <a id="withbackoff"></a>WithBackoff

WithBackoff sets delay between retry attempts.

```go
dispatcher, err := queue.NewQueue(queue.QueueConfig{Driver: queue.DriverSync})
if err != nil {
	return
}
dispatcher.Register("emails:send", func(ctx context.Context, task queue.Task) error { return nil })
err = dispatcher.Dispatch("emails:send", []byte(`{"id":1}`), queue.WithBackoff(2*time.Second))
_ = err
```

### <a id="withdelay"></a>WithDelay

WithDelay schedules processing after a delay.

```go
dispatcher, err := queue.NewQueue(queue.QueueConfig{Driver: queue.DriverSync})
if err != nil {
	return
}
dispatcher.Register("emails:send", func(ctx context.Context, task queue.Task) error { return nil })
err = dispatcher.Dispatch("emails:send", []byte(`{"id":1}`), queue.WithDelay(10*time.Second))
_ = err
```

### <a id="withmaxretry"></a>WithMaxRetry

WithMaxRetry sets maximum retry attempts.

```go
dispatcher, err := queue.NewQueue(queue.QueueConfig{Driver: queue.DriverSync})
if err != nil {
	return
}
dispatcher.Register("emails:send", func(ctx context.Context, task queue.Task) error { return nil })
err = dispatcher.Dispatch("emails:send", []byte(`{"id":1}`), queue.WithMaxRetry(3))
_ = err
```

### <a id="withqueue"></a>WithQueue

WithQueue routes a task to a named queue.

```go
dispatcher, err := queue.NewQueue(queue.QueueConfig{Driver: queue.DriverSync})
if err != nil {
	return
}
dispatcher.Register("emails:send", func(ctx context.Context, task queue.Task) error { return nil })
err = dispatcher.Dispatch("emails:send", []byte(`{"id":1}`), queue.WithQueue("critical"))
_ = err
```

### <a id="withtimeout"></a>WithTimeout

WithTimeout sets per-task execution timeout.

```go
dispatcher, err := queue.NewQueue(queue.QueueConfig{Driver: queue.DriverSync})
if err != nil {
	return
}
dispatcher.Register("emails:send", func(ctx context.Context, task queue.Task) error { return nil })
err = dispatcher.Dispatch("emails:send", []byte(`{"id":1}`), queue.WithTimeout(15*time.Second))
_ = err
```

### <a id="withunique"></a>WithUnique

WithUnique deduplicates by task type and payload for a TTL window.

```go
dispatcher, err := queue.NewQueue(queue.QueueConfig{Driver: queue.DriverSync})
if err != nil {
	return
}
dispatcher.Register("emails:send", func(ctx context.Context, task queue.Task) error { return nil })
err = dispatcher.Dispatch("emails:send", []byte(`{"id":1}`), queue.WithUnique(30*time.Second))
_ = err
```

## Queue

### <a id="dispatch"></a>Dispatch

Dispatch schedules or executes a task using the local driver.

```go
queuer, err := queue.NewQueue(queue.QueueConfig{Driver: queue.DriverSync})
if err != nil {
	return
}
queuer.Register("emails:send", func(ctx context.Context, task queue.Task) error { return nil })
_ = queuer.Dispatch("emails:send", []byte(`{"id":1}`), queue.WithDelay(10*time.Millisecond))
```

### <a id="driver"></a>Driver

Driver returns the local dispatcher's driver mode.

```go
dispatcher, err := queue.NewQueue(queue.QueueConfig{Driver: queue.DriverSync})
if err != nil {
	return
}
fmt.Println(dispatcher.Driver())
// Output: sync
```

### <a id="register"></a>Register

Register adds a task handler to the local dispatcher.

```go
dispatcher, err := queue.NewQueue(queue.QueueConfig{Driver: queue.DriverSync})
if err != nil {
	return
}
dispatcher.Register("emails:send", func(ctx context.Context, task queue.Task) error { return nil })
```

### <a id="shutdown"></a>Shutdown

Shutdown drains delayed and active local workerpool tasks.

```go
dispatcher, err := queue.NewQueue(queue.QueueConfig{
	Driver: queue.DriverWorkerpool,
})
if err != nil {
	return
}
_ = dispatcher.Start(context.Background())
_ = dispatcher.Shutdown(context.Background())
```

### <a id="start"></a>Start

Start initializes worker goroutines for workerpool mode.

```go
dispatcher, err := queue.NewQueue(queue.QueueConfig{
	Driver: queue.DriverWorkerpool,
})
if err != nil {
	return
}
_ = dispatcher.Start(context.Background())
```
<!-- api:embed:end -->
