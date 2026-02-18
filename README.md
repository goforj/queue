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
    <img src="https://img.shields.io/badge/tests-158-brightgreen" alt="Tests">
<!-- test-count:embed:end -->
</p>

## What queue is

queue is a backend-agnostic job queue runtime. Your application code depends on `queue.Queue` and fluent `queue.Task` values. The driver decides whether work runs via Redis/Asynq, a SQL table, NATS, SQS, RabbitMQ, an in-process worker pool, or synchronously in the caller.

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

### SQS
- Publishes tasks to AWS SQS queues (or localstack for local integration).
- Use `queue.Worker` to poll and process handlers.

### RabbitMQ
- Publishes tasks to RabbitMQ queues.
- Use `queue.Worker` to consume and process handlers.

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
    Driver: queue.DriverRedis,
    RedisAddr: "127.0.0.1:6379",
    Observer: collector,
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

```go
logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
observer := queue.ObserverFunc(func(event queue.Event) {
    logger.Info("queue event",
        "kind", event.Kind,
        "driver", event.Driver,
        "queue", event.Queue,
        "task_type", event.TaskType,
        "task_key", event.TaskKey,
        "attempt", event.Attempt,
        "max_retry", event.MaxRetry,
        "duration", event.Duration,
        "err", event.Err,
    )
})

q, _ := queue.New(queue.Config{
    Driver: queue.DriverRedis,
    RedisAddr: "127.0.0.1:6379",
    Observer: observer,
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
        // ignore noisy events
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

| Event kind | Meaning |
|:--|:--|
| `EventEnqueueAccepted` | Task was accepted by enqueue. |
| `EventEnqueueRejected` | Task enqueue failed with an error. |
| `EventEnqueueDuplicate` | Task enqueue was rejected as duplicate (`UniqueFor`). |
| `EventEnqueueCanceled` | Enqueue was canceled by context timeout/cancelation. |
| `EventProcessStarted` | Worker started handling a task. |
| `EventProcessSucceeded` | Worker completed a task successfully. |
| `EventProcessFailed` | Worker attempt failed. |
| `EventProcessRetried` | Failed attempt was requeued for another attempt. |
| `EventProcessArchived` | Failed task reached terminal failure (no retries left). |
| `EventQueuePaused` | Queue consumption was paused. |
| `EventQueueResumed` | Queue consumption was resumed. |

For full field-level semantics and guarantees, see [`events.md`](events.md).

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

### SQS driver

Attach workers through `queue.Worker` and publish with `queue.Queue`.

```go
worker, _ := queue.NewWorker(queue.WorkerConfig{
    Driver: queue.DriverSQS,
    SQSRegion: "us-east-1",
    SQSEndpoint: "http://127.0.0.1:4566",
    SQSAccessKey: "test",
    SQSSecretKey: "test",
})
worker.Register("emails:send", func(ctx context.Context, task queue.Task) error {
    return sendEmail(ctx, task.PayloadBytes())
})
_ = worker.Start()
defer worker.Shutdown()
```

### RabbitMQ driver

Attach workers through `queue.Worker` and publish with `queue.Queue`.

```go
worker, _ := queue.NewWorker(queue.WorkerConfig{
    Driver: queue.DriverRabbitMQ,
    RabbitMQURL: "amqp://guest:guest@127.0.0.1:5672/",
    DefaultQueue: "default",
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
- SQS: call `worker.Start()` to poll queues and `worker.Shutdown()` for graceful stop.
- RabbitMQ: call `worker.Start()` to consume queues and `worker.Shutdown()` for graceful stop.

## Driver selection via config

Use `queue.Config` with `New` and `queue.WorkerConfig` with `NewWorker`.  
These are intentionally separate so enqueue-side settings and worker execution settings are documented independently.

### Config support

Legend: `✓` supported, `-` ignored, `o` optional.

| Field group | Config | WorkerConfig | Notes |
|--:|:--|:--|:--|
| **Core** | Driver | Driver | Backend selector. |
| **Workerpool** | - | Workers, QueueCapacity, DefaultTaskTimeout | In-process async worker tuning. |
| **Database** | DefaultQueue, Database, DatabaseDriver, DatabaseDSN | DefaultQueue, PollInterval, AutoMigrate, Database, DatabaseDriver, DatabaseDSN, Workers | SQL queue runtime/worker options. |
| **Redis** | RedisAddr, RedisPassword, RedisDB | RedisAddr, RedisPassword, RedisDB, Workers | Asynq queue/worker options. |
| **NATS** | NATSURL | NATSURL | NATS connection URL. |
| **SQS** | SQSRegion, SQSEndpoint, SQSAccessKey, SQSSecretKey | SQSRegion, SQSEndpoint, SQSAccessKey, SQSSecretKey, DefaultQueue | AWS/localstack connection + queue name. |
| **RabbitMQ** | RabbitMQURL | RabbitMQURL, DefaultQueue | AMQP URL + consumer queue name. |

### Config by backend

`Driver` is required for all backends.

| Backend | Config fields |
|:--|:--|
| Sync | None |
| Workerpool | None |
| Database | DefaultQueue, Database, DatabaseDriver, DatabaseDSN |
| Redis | RedisAddr, RedisPassword, RedisDB |
| NATS | NATSURL |
| SQS | SQSRegion (default: us-east-1), SQSEndpoint, SQSAccessKey, SQSSecretKey |
| RabbitMQ | RabbitMQURL |

### WorkerConfig by backend

`Driver` is required for all backends.

| Backend | WorkerConfig fields |
|:--|:--|
| Sync | None |
| Workerpool | Workers (default: runtime.NumCPU), QueueCapacity (default: Workers), DefaultTaskTimeout |
| Database | Workers (default: runtime.NumCPU), PollInterval (default: 50ms), DefaultQueue (default: default), AutoMigrate (default: true), Database, DatabaseDriver, DatabaseDSN |
| Redis | Workers (default: runtime.NumCPU), RedisAddr, RedisPassword, RedisDB |
| NATS | NATSURL |
| SQS | DefaultQueue (default: default), SQSRegion (default: us-east-1), SQSEndpoint, SQSAccessKey, SQSSecretKey |
| RabbitMQ | DefaultQueue (default: default), RabbitMQURL |

## API reference

The API section below is autogenerated; do not edit between the markers.

<!-- api:embed:start -->

## API Index

| Group | Functions |
|------:|:-----------|
| **Constructors** | [New](#new) [NewStatsCollector](#newstatscollector) [NewWorker](#newworker) |
| **Observability** | [Active](#active) [Archived](#archived) [Failed](#failed) [MultiObserver](#multiobserver) [Observe](#observe) [PauseQueue](#pausequeue) [Paused](#paused) [Pending](#pending) [Processed](#processed) [Queue](#queue) [Queues](#queues) [ResumeQueue](#resumequeue) [Scheduled](#scheduled) [Snapshot](#snapshot) [SnapshotQueue](#snapshotqueue) [SupportsNativeStats](#supportsnativestats) [SupportsPause](#supportspause) [Throughput](#throughput) |
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

### <a id="newstatscollector"></a>NewStatsCollector

NewStatsCollector creates an event collector for queue counters.

```go
collector := queue.NewStatsCollector()
_ = collector
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
q, _ := queue.New(queue.Config{Driver: queue.DriverSync})
_ = queue.PauseQueue(context.Background(), q, "default")
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
q, _ := queue.New(queue.Config{Driver: queue.DriverSync})
_ = queue.PauseQueue(context.Background(), q, "default")
_ = queue.ResumeQueue(context.Background(), q, "default")
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
q, _ := queue.New(queue.Config{Driver: queue.DriverSync})
snapshot, _ := queue.SnapshotQueue(context.Background(), q, nil)
_, ok := snapshot.Queue("default")
fmt.Println(ok)
// Output: true
```

### <a id="supportsnativestats"></a>SupportsNativeStats

SupportsNativeStats reports whether a queue runtime exposes native stats snapshots.

```go
q, _ := queue.New(queue.Config{Driver: queue.DriverSync})
fmt.Println(queue.SupportsNativeStats(q))
// Output: true
```

### <a id="supportspause"></a>SupportsPause

SupportsPause reports whether a queue runtime supports PauseQueue/ResumeQueue.

```go
q, _ := queue.New(queue.Config{Driver: queue.DriverSync})
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

_Example: retry_

```go
task := queue.NewTask("emails:send").Retry(4)
_ = task
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
_ = task
```

### <a id="uniquefor"></a>UniqueFor

UniqueFor enables uniqueness dedupe within the given TTL.

```go
task := queue.NewTask("emails:send").UniqueFor(45 * time.Second)
_ = task
```
<!-- api:embed:end -->
