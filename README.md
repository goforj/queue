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
    <img src="https://img.shields.io/badge/tests-65-brightgreen" alt="Tests">
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
- Auto-migrates schema by default; set `AutoMigrate` to false to manage DDL yourself.
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
    dispatcher, _ := queue.NewDispatcher(queue.Config{
        Driver: queue.DriverWorkerpool,
        Workerpool: queue.WorkerpoolConfig{Workers: 4, Buffer: 128, TaskTimeout: 30 * time.Second},
    }, nil)

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
redis := asynq.NewClient(asynq.RedisClientOpt{Addr: "127.0.0.1:6379"})
dispatcher := queue.NewRedisDispatcher(redis)
```

Use SQL for durable local queues:

```go
dispatcher, _ := queue.NewDispatcher(queue.Config{
    Driver: queue.DriverDatabase,
    Database: queue.DatabaseConfig{
        DriverName: "sqlite",
        DSN:        "file:queue.db?_busy_timeout=5000",
        Workers:    4,
    },
}, nil)
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
dispatcher := queue.NewSyncDispatcher()
dispatcher.Register("emails:send", func(ctx context.Context, task queue.Task) error {
    return sendEmail(ctx, task.Payload)
})
_ = dispatcher.Enqueue(context.Background(), queue.Task{Type: "emails:send"})
```

### Workerpool driver

The worker is in-process. Attach by registering handlers and starting the dispatcher.

```go
dispatcher := queue.NewWorkerpoolDispatcher(queue.WorkerpoolConfig{Workers: 4, Buffer: 128})
dispatcher.Register("emails:send", func(ctx context.Context, task queue.Task) error {
    return sendEmail(ctx, task.Payload)
})
_ = dispatcher.Start(context.Background())
defer dispatcher.Shutdown(context.Background())
```

### Database driver

Same attachment model as workerpool, but jobs are durable in SQL.

```go
dispatcher, _ := queue.NewDatabaseDispatcher(queue.DatabaseConfig{
    DriverName: "sqlite",
    DSN:        "file:queue.db?_busy_timeout=5000",
    Workers:    4,
})
dispatcher.Register("emails:send", func(ctx context.Context, task queue.Task) error {
    return sendEmail(ctx, task.Payload)
})
_ = dispatcher.Start(context.Background())
defer dispatcher.Shutdown(context.Background())
```

### Redis driver

This package's Redis dispatcher is enqueue-side only.  
Attach workers in a separate Asynq server process by registering handlers on a mux.

```go
mux := asynq.NewServeMux()
mux.HandleFunc("emails:send", func(ctx context.Context, t *asynq.Task) error {
    return sendEmail(ctx, t.Payload())
})

server := asynq.NewServer(
    asynq.RedisClientOpt{Addr: "127.0.0.1:6379"},
    asynq.Config{Concurrency: 10},
)
if err := server.Run(mux); err != nil {
    panic(err)
}
```

## Running workers and shutdown

- Workerpool: call `Start` once; `Shutdown` drains in-flight and delayed tasks with context timeouts respected.
- Database: `Start` spins worker goroutines; `Enqueue` will auto-start if needed. `Shutdown` waits for workers and closes owned DB handles.
- Redis: dispatcher only enqueues; run your Asynq server separately and register handlers on its mux.

## Driver selection via config

`queue.Config` pairs the selected driver with driver-specific settings. The same application code can switch drivers by changing config or environment wiring; handler registration stays identical.

## API reference

The API section below is autogenerated; do not edit between the markers.

<!-- api:embed:start -->

## API Index

| Group | Functions |
|------:|:-----------|
| **Constructors** | [NewDatabaseDispatcher](#newdatabasedispatcher) [NewDispatcher](#newdispatcher) [NewRedisDispatcher](#newredisdispatcher) [NewSyncDispatcher](#newsyncdispatcher) [NewWorkerpoolDispatcher](#newworkerpooldispatcher) |
| **Dispatcher** | [Driver](#driver) [Enqueue](#enqueue) [Register](#register) [Shutdown](#shutdown) [Start](#start) |
| **Options** | [WithBackoff](#withbackoff) [WithDelay](#withdelay) [WithMaxRetry](#withmaxretry) [WithQueue](#withqueue) [WithTimeout](#withtimeout) [WithUnique](#withunique) |


## Constructors

### <a id="newdatabasedispatcher"></a>NewDatabaseDispatcher

NewDatabaseDispatcher creates a durable SQL-backed dispatcher.

### <a id="newdispatcher"></a>NewDispatcher

NewDispatcher creates a dispatcher based on Config.Driver.

### <a id="newredisdispatcher"></a>NewRedisDispatcher

NewRedisDispatcher creates a redis-backed dispatcher using an asynq-compatible enqueuer.

### <a id="newsyncdispatcher"></a>NewSyncDispatcher

NewSyncDispatcher creates a synchronous in-process dispatcher.

### <a id="newworkerpooldispatcher"></a>NewWorkerpoolDispatcher

NewWorkerpoolDispatcher creates an in-memory asynchronous workerpool dispatcher.

## Dispatcher

### <a id="driver"></a>Driver

Driver returns the local dispatcher's driver mode.

```go
dispatcher := queue.NewSyncDispatcher()
fmt.Println(dispatcher.Driver())
// Output: sync
```

### <a id="enqueue"></a>Enqueue

Enqueue schedules or executes a task using the local driver.

```go
dispatcher := queue.NewSyncDispatcher()
dispatcher.Register("emails:send", func(ctx context.Context, task queue.Task) error { return nil })
_ = dispatcher.Enqueue(context.Background(), queue.Task{Type: "emails:send"}, queue.WithDelay(10*time.Millisecond))
```

### <a id="register"></a>Register

Register adds a task handler to the local dispatcher.

```go
dispatcher := queue.NewSyncDispatcher()
dispatcher.Register("emails:send", func(ctx context.Context, task queue.Task) error { return nil })
```

### <a id="shutdown"></a>Shutdown

Shutdown drains delayed and active local workerpool tasks.

```go
dispatcher := queue.NewWorkerpoolDispatcher(queue.WorkerpoolConfig{Workers: 1, Buffer: 4})
_ = dispatcher.Start(context.Background())
_ = dispatcher.Shutdown(context.Background())
```

### <a id="start"></a>Start

Start initializes worker goroutines for workerpool mode.

```go
dispatcher := queue.NewWorkerpoolDispatcher(queue.WorkerpoolConfig{Workers: 1, Buffer: 4})
_ = dispatcher.Start(context.Background())
```

## Options

### <a id="withbackoff"></a>WithBackoff

WithBackoff sets delay between retry attempts.

```go
dispatcher := queue.NewSyncDispatcher()
dispatcher.Register("emails:send", func(ctx context.Context, task queue.Task) error { return nil })
ctx := context.Background()
err := dispatcher.Enqueue(ctx, queue.Task{Type: "emails:send"}, queue.WithBackoff(2*time.Second))
_ = err
```

### <a id="withdelay"></a>WithDelay

WithDelay schedules processing after a delay.

```go
dispatcher := queue.NewSyncDispatcher()
dispatcher.Register("emails:send", func(ctx context.Context, task queue.Task) error { return nil })
ctx := context.Background()
err := dispatcher.Enqueue(ctx, queue.Task{Type: "emails:send"}, queue.WithDelay(10*time.Second))
_ = err
```

### <a id="withmaxretry"></a>WithMaxRetry

WithMaxRetry sets maximum retry attempts.

```go
dispatcher := queue.NewSyncDispatcher()
dispatcher.Register("emails:send", func(ctx context.Context, task queue.Task) error { return nil })
ctx := context.Background()
err := dispatcher.Enqueue(ctx, queue.Task{Type: "emails:send"}, queue.WithMaxRetry(3))
_ = err
```

### <a id="withqueue"></a>WithQueue

WithQueue routes a task to a named queue.

```go
dispatcher := queue.NewSyncDispatcher()
dispatcher.Register("emails:send", func(ctx context.Context, task queue.Task) error { return nil })
ctx := context.Background()
err := dispatcher.Enqueue(ctx, queue.Task{Type: "emails:send"}, queue.WithQueue("critical"))
_ = err
```

### <a id="withtimeout"></a>WithTimeout

WithTimeout sets per-task execution timeout.

```go
dispatcher := queue.NewSyncDispatcher()
dispatcher.Register("emails:send", func(ctx context.Context, task queue.Task) error { return nil })
ctx := context.Background()
err := dispatcher.Enqueue(ctx, queue.Task{Type: "emails:send"}, queue.WithTimeout(15*time.Second))
_ = err
```

### <a id="withunique"></a>WithUnique

WithUnique deduplicates by task type and payload for a TTL window.

```go
dispatcher := queue.NewSyncDispatcher()
dispatcher.Register("emails:send", func(ctx context.Context, task queue.Task) error { return nil })
ctx := context.Background()
err := dispatcher.Enqueue(ctx, queue.Task{Type: "emails:send", Payload: []byte(`{"id":1}`)}, queue.WithUnique(30*time.Second))
_ = err
```
<!-- api:embed:end -->
