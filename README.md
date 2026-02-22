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
    <img src="https://img.shields.io/badge/tests-384-brightgreen" alt="Tests">
<!-- test-count:embed:end -->
</p>

## Installation

```bash
go get github.com/goforj/queue
```

## bus vs queue

- `bus` is the higher-level API. Use it when you want workflow features like chains, batches, middleware, callbacks, and unified lifecycle events.
- `queue` is the lower-level runtime API (drivers, jobs, workers).
- If unsure, start with `bus`.

## Quick Start (Bus)

```go
import (
	"context"

	"github.com/goforj/queue"
	"github.com/goforj/queue/bus"
)

type EmailPayload struct {
	ID int `json:"id"`
}

func main() {
	q, _ := queue.NewWorkerpool()
	b, _ := bus.New(q)

	b.Register("reports:generate", func(ctx context.Context, jc bus.Context) error {
		return nil
	})
	b.Register("reports:upload", func(ctx context.Context, jc bus.Context) error {
		var payload EmailPayload
		if err := jc.Bind(&payload); err != nil {
			return err
		}
		return nil
	})
	b.Register("users:notify_report_ready", func(ctx context.Context, jc bus.Context) error {
		return nil
	})

	_ = b.StartWorkers(context.Background())
	defer b.Shutdown(context.Background())

	chainID, _ := b.Chain(
		// 1) generate report data
		bus.NewJob("reports:generate", map[string]any{"report_id": "rpt_123"}),
		// 2) upload report artifact after generate succeeds
		bus.NewJob("reports:upload", EmailPayload{ID: 123}),
		// 3) notify user only after upload succeeds
		bus.NewJob("users:notify_report_ready", map[string]any{"user_id": 123}),
	).OnQueue("critical").Dispatch(context.Background())
	_ = chainID
}
```

## Quick Start (Queue only)

```go
import (
	"context"

	"github.com/goforj/queue"
)

func main() {
	q, _ := queue.NewWorkerpool()

	q.Register("emails:send", func(ctx context.Context, job queue.Job) error {
		return nil
	})

	_ = q.Workers(2).StartWorkers(context.Background())
	defer q.Shutdown(context.Background())

	_ = q.Dispatch(queue.NewJob("emails:send").OnQueue("default"))
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
_ = q.Dispatch(job)

// In handlers, use Bind to decode payload into a struct.
q.Register("emails:send", func(ctx context.Context, job queue.Job) error {
	var payload EmailPayload
	if err := job.Bind(&payload); err != nil {
		return err
	}
	return nil
})
```

## Drivers

| Driver / Backend | Mode | Notes | Durable | Async | Delay | Unique | Backoff | Timeout |
| ---: | :--- | :--- | :---: | :---: | :---: | :---: | :---: | :---: |
| <img src="https://img.shields.io/badge/null-%23666?style=flat" alt="Null"> | Drop-only | Discards dispatched jobs; useful for disabled queue modes and smoke tests. | - | - | - | - | - | - |
| <img src="https://img.shields.io/badge/sync-%23999999?logo=gnometerminal&logoColor=white" alt="Sync"> | Inline (caller) | Deterministic local execution with no external infra. | - | - | - | ✓ | - | ✓ |
| <img src="https://img.shields.io/badge/workerpool-%23696969?logo=clockify&logoColor=white" alt="Workerpool"> | In-process pool | Local async behavior without external broker/database. | - | ✓ | ✓ | ✓ | ✓ | ✓ |
| <img src="https://img.shields.io/badge/database-%23336791?logo=postgresql&logoColor=white" alt="Database"> | SQL (pg/mysql/sqlite) | Durable queue with SQL storage. | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| <img src="https://img.shields.io/badge/redis-%23DC382D?logo=redis&logoColor=white" alt="Redis"> | Redis/Asynq | Production Redis backend (Asynq semantics). | ✓ | ✓ | ✓ | ✓ | - | ✓ |
| <img src="https://img.shields.io/badge/NATS-007ACC?style=flat" alt="NATS"> | Broker target | NATS transport with queue-subject routing. | - | ✓ | ✓ | ✓ | ✓ | ✓ |
| <img src="https://img.shields.io/badge/SQS-FF9900?style=flat" alt="SQS"> | Broker target | AWS SQS transport with endpoint overrides for localstack/testing. | - | ✓ | ✓ | ✓ | ✓ | ✓ |
| <img src="https://img.shields.io/badge/rabbitmq-%23FF6600?logo=rabbitmq&logoColor=white" alt="RabbitMQ"> | Broker target | RabbitMQ transport and worker consumption. | - | ✓ | ✓ | ✓ | ✓ | ✓ |

## Bus middleware example

```go
audit := bus.MiddlewareFunc(func(ctx context.Context, jc bus.Context, next bus.Next) error {
    return next(ctx, jc)
})

skipHealth := bus.SkipWhen{
    Predicate: func(_ context.Context, jc bus.Context) bool { return jc.JobType == "health:ping" },
}

fatalize := bus.FailOnError{
    When: func(err error) bool { return err != nil },
}

q, _ := queue.NewWorkerpool()
b, _ := bus.New(q, bus.WithMiddleware(audit, skipHealth, fatalize))
_ = b
```

## Core Concepts

| Concept | Purpose | Primary API |
| --- | --- | --- |
| Job | Typed work unit for app handlers | `bus.NewJob`, `Dispatch` |
| Chain | Ordered workflow (A then B then C) | `Chain(...).Dispatch(...)` |
| Batch | Parallel workflow with callbacks | `Batch(...).Then/Catch/Finally` |
| Middleware | Cross-cutting execution policy | `WithMiddleware(...)` |
| Events | Lifecycle hooks and observability | `WithObserver(...)` |
| Backends | Driver/runtime transport | `queue.New*` constructors |

## Queue Backends

Use `queue` constructors to choose transport/runtime. Your bus handlers and jobs remain unchanged.

| Backend | Constructor |
| ---: | --- |
| In-process sync | `queue.NewSync()` |
| In-process worker pool | `queue.NewWorkerpool()` |
| SQL durable queue | `queue.NewDatabase(driver, dsn)` |
| Redis/Asynq | `queue.NewRedis(addr)` |
| NATS | `queue.NewNATS(url)` |
| SQS | `queue.NewSQS(region)` |
| RabbitMQ | `queue.NewRabbitMQ(url)` |
| Drop-only (disabled mode) | `queue.NewNull()` |

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

q, _ := queue.NewWorkerpool()
b, _ := bus.New(q, bus.WithObserver(observer))
_ = b
```

### Distributed counters and source of truth

- `StatsCollector` counters are process-local and event-driven.
- In multi-process deployments, aggregate metrics externally (OTel/Prometheus/etc.).
- Prefer backend-native stats when available.
- `queue.SupportsNativeStats(q)` indicates native driver snapshot support.
- `queue.SnapshotQueue(ctx, q, collector, queueName)` merges native + collector where possible.

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

### Queue kitchen sink event logging

```go
logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
observer := queue.ObserverFunc(func(event queue.Event) {
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

q, _ := queue.New(queue.Config{
	Driver:    queue.DriverRedis,
	RedisAddr: "127.0.0.1:6379",
	Observer:  observer,
})
_ = q
```

### Bus kitchen sink event logging

```go
logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
observer := bus.ObserverFunc(func(event bus.Event) {
	ids := fmt.Sprintf("dispatch=%s job=%s chain=%s batch=%s", event.DispatchID, event.JobID, event.ChainID, event.BatchID)
	jobInfo := fmt.Sprintf("job_type=%s queue=%s attempt=%d", event.JobType, event.Queue, event.Attempt)

	switch event.Kind {
	case bus.EventDispatchStarted:
		logger.Info("Dispatch started", "msg", fmt.Sprintf("%s %s", ids, jobInfo), "at", event.Time.Format(time.RFC3339Nano))
	case bus.EventDispatchSucceeded:
		logger.Info("Dispatch enqueued", "msg", fmt.Sprintf("%s %s", ids, jobInfo))
	case bus.EventDispatchFailed:
		logger.Error("Dispatch failed", "msg", fmt.Sprintf("%s %s", ids, jobInfo), "error", event.Err)
	case bus.EventJobStarted:
		logger.Info("Job started", "msg", fmt.Sprintf("%s %s", ids, jobInfo))
	case bus.EventJobSucceeded:
		logger.Info("Job succeeded", "msg", fmt.Sprintf("%s %s duration=%s", ids, jobInfo, event.Duration))
	case bus.EventJobFailed:
		logger.Error("Job failed", "msg", fmt.Sprintf("%s %s duration=%s", ids, jobInfo, event.Duration), "error", event.Err)
	case bus.EventChainStarted:
		logger.Info("Chain started", "msg", ids)
	case bus.EventChainAdvanced:
		logger.Info("Chain advanced", "msg", ids)
	case bus.EventChainCompleted:
		logger.Info("Chain completed", "msg", ids)
	case bus.EventChainFailed:
		logger.Error("Chain failed", "msg", ids, "error", event.Err)
	case bus.EventBatchStarted:
		logger.Info("Batch started", "msg", ids)
	case bus.EventBatchProgressed:
		logger.Info("Batch progressed", "msg", ids)
	case bus.EventBatchCompleted:
		logger.Info("Batch completed", "msg", ids)
	case bus.EventBatchFailed:
		logger.Error("Batch failed", "msg", ids, "error", event.Err)
	case bus.EventBatchCancelled:
		logger.Warn("Batch cancelled", "msg", ids)
	case bus.EventCallbackStarted:
		logger.Info("Callback started", "msg", ids)
	case bus.EventCallbackSucceeded:
		logger.Info("Callback succeeded", "msg", ids)
	case bus.EventCallbackFailed:
		logger.Error("Callback failed", "msg", ids, "error", event.Err)
	default:
		logger.Info("Bus event", "msg", fmt.Sprintf("kind=%s %s %s", event.Kind, ids, jobInfo))
	}
})

q, _ := queue.New(queue.Config{
	Driver:    queue.DriverRedis,
	RedisAddr: "127.0.0.1:6379",
})
b, _ := bus.New(q, bus.WithObserver(observer))
_ = b
```

### Observability capabilities by driver

| Driver | Native Stats | Pause/Resume |
| --- | :---: | :---: |
| null | - | - |
| sync | - | - |
| workerpool | - | - |
| database | ✓ | - |
| redis | ✓ | ✓ |
| nats | - | - |
| sqs | - | - |
| rabbitmq | - | - |

### Queue events reference

| EventKind | Meaning |
| --- | --- |
| enqueue_accepted | Job accepted by driver for enqueue. |
| enqueue_rejected | Job enqueue failed. |
| enqueue_duplicate | Duplicate job rejected due to uniqueness key. |
| enqueue_canceled | Context cancellation prevented enqueue. |
| process_started | Worker began processing job. |
| process_succeeded | Handler returned success. |
| process_failed | Handler returned error. |
| process_retried | Driver scheduled retry attempt. |
| process_archived | Job moved to terminal failure state. |
| queue_paused | Queue was paused (driver supports pause). |
| queue_resumed | Queue was resumed. |

### Bus events reference

| EventKind | Meaning |
| --- | --- |
| dispatch_started | Bus accepted a dispatch request and created a dispatch record. |
| dispatch_succeeded | Dispatch was successfully enqueued to the underlying queue runtime. |
| dispatch_failed | Dispatch failed before job execution could start. |
| job_started | A bus job handler started execution. |
| job_succeeded | A bus job handler completed successfully. |
| job_failed | A bus job handler returned an error. |
| chain_started | A chain workflow was created and started. |
| chain_advanced | Chain progressed from one node to the next node. |
| chain_completed | Chain reached terminal success. |
| chain_failed | Chain reached terminal failure. |
| batch_started | A batch workflow was created and started. |
| batch_progressed | Batch state changed as jobs completed/failed. |
| batch_completed | Batch reached terminal success (or allowed-failure completion). |
| batch_failed | Batch reached terminal failure. |
| batch_cancelled | Batch was cancelled before normal completion. |
| callback_started | Chain/batch callback execution started. |
| callback_succeeded | Chain/batch callback completed successfully. |
| callback_failed | Chain/batch callback returned an error. |

## Testing By Audience

### Application tests

Use `bus.NewFake()` when testing app behavior around dispatch/chain/batch.

```go
fake := bus.NewFake()
_, _ = fake.Dispatch(context.Background(), bus.NewJob("emails:send", nil))
fake.AssertDispatched(nil, "emails:send")
```

### Runtime/driver tests

Use `queue.NewFake()` when testing queue/job-level dispatch semantics.

```go
fake := queue.NewFake()
_ = fake.Dispatch(queue.NewJob("emails:send").OnQueue("default"))
fake.AssertDispatched(nil, "emails:send")
```

## Queue Runtime (Lower Level)

If you do not need workflow orchestration, use `queue.Queue` directly.

```go
q, _ := queue.NewWorkerpool()
q.Register("emails:send", func(ctx context.Context, job queue.Job) error {
    return nil
})
_ = q.Workers(2).StartWorkers(context.Background())
defer q.Shutdown(context.Background())
_ = q.Dispatch(queue.NewJob("emails:send").OnQueue("default"))
```

Matrix status and backend integration notes are tracked in `docs/integration-scenarios.md`.
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
| **Job** | [Job.Backoff](#queue-job-backoff) [Job.Bind](#queue-job-bind) [Job.Delay](#queue-job-delay) [NewJob](#queue-newjob) [Job.OnQueue](#queue-job-onqueue) [Job.Payload](#queue-job-payload) [Job.PayloadBytes](#queue-job-payloadbytes) [Job.PayloadJSON](#queue-job-payloadjson) [Job.Retry](#queue-job-retry) [Job.Timeout](#queue-job-timeout) [Job.UniqueFor](#queue-job-uniquefor) |
| **Observability** | [StatsSnapshot.Active](#queue-statssnapshot-active) [StatsSnapshot.Archived](#queue-statssnapshot-archived) [StatsSnapshot.Failed](#queue-statssnapshot-failed) [MultiObserver](#queue-multiobserver) [ChannelObserver.Observe](#queue-channelobserver-observe) [Observer.Observe](#queue-observer-observe) [ObserverFunc.Observe](#queue-observerfunc-observe) [StatsCollector.Observe](#queue-statscollector-observe) [PauseQueue](#queue-pausequeue) [StatsSnapshot.Paused](#queue-statssnapshot-paused) [StatsSnapshot.Pending](#queue-statssnapshot-pending) [StatsSnapshot.Processed](#queue-statssnapshot-processed) [StatsSnapshot.Queue](#queue-statssnapshot-queue) [StatsSnapshot.Queues](#queue-statssnapshot-queues) [ResumeQueue](#queue-resumequeue) [StatsSnapshot.RetryCount](#queue-statssnapshot-retrycount) [StatsSnapshot.Scheduled](#queue-statssnapshot-scheduled) [StatsCollector.Snapshot](#queue-statscollector-snapshot) [SnapshotQueue](#queue-snapshotqueue) [SupportsNativeStats](#queue-supportsnativestats) [SupportsPause](#queue-supportspause) [StatsSnapshot.Throughput](#queue-statssnapshot-throughput) |
| **Queue** | [Queue.Dispatch](#queue-queue-dispatch) [Queue.DispatchCtx](#queue-queue-dispatchctx) [Queue.Driver](#queue-queue-driver) [Queue.Register](#queue-queue-register) [Queue.Shutdown](#queue-queue-shutdown) [Queue.StartWorkers](#queue-queue-startworkers) [Queue.Workers](#queue-queue-workers) |
| **Testing** | [FakeQueue.AssertCount](#queue-fakequeue-assertcount) [FakeQueue.AssertDispatched](#queue-fakequeue-assertdispatched) [FakeQueue.AssertDispatchedOn](#queue-fakequeue-assertdispatchedon) [FakeQueue.AssertDispatchedTimes](#queue-fakequeue-assertdispatchedtimes) [FakeQueue.AssertNotDispatched](#queue-fakequeue-assertnotdispatched) [FakeQueue.AssertNothingDispatched](#queue-fakequeue-assertnothingdispatched) [FakeQueue.Dispatch](#queue-fakequeue-dispatch) [FakeQueue.DispatchCtx](#queue-fakequeue-dispatchctx) [FakeQueue.Driver](#queue-fakequeue-driver) [NewFake](#queue-newfake) [FakeQueue.Records](#queue-fakequeue-records) [FakeQueue.Register](#queue-fakequeue-register) [FakeQueue.Reset](#queue-fakequeue-reset) [FakeQueue.Shutdown](#queue-fakequeue-shutdown) [FakeQueue.StartWorkers](#queue-fakequeue-startworkers) [FakeQueue.Workers](#queue-fakequeue-workers) |



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
audit := bus.MiddlewareFunc(func(ctx context.Context, jc bus.Context, next bus.Next) error {
	return next(ctx, jc)
})
skipHealth := bus.SkipWhen{
	Predicate: func(_ context.Context, jc bus.Context) bool { return jc.JobType == "health:ping" },
}
fatalize := bus.FailOnError{
	When: func(err error) bool { return err != nil },
}
b, _ := bus.New(q, bus.WithMiddleware(audit, skipHealth, fatalize))
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
q.Register("emails:send", func(ctx context.Context, job queue.Job) error {
	var payload EmailPayload
	if err := job.Bind(&payload); err != nil {
		return err
	}
	return nil
})
defer q.Shutdown(context.Background())
	context.Background(),
	queue.NewJob("emails:send").
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
q.Register("emails:send", func(ctx context.Context, job queue.Job) error {
	var payload EmailPayload
	if err := job.Bind(&payload); err != nil {
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

### Job

#### <a id="queue-job-backoff"></a>queue.Job.Backoff

Backoff sets delay between retries.

```go
job := queue.NewJob("emails:send").Backoff(500 * time.Millisecond)
```

#### <a id="queue-job-bind"></a>queue.Job.Bind

Bind unmarshals job payload JSON into dst.

```go
type EmailPayload struct {
	ID int `json:"id"`
}
job := queue.NewJob("emails:send").Payload(EmailPayload{ID: 1})
var payload EmailPayload
```

#### <a id="queue-job-delay"></a>queue.Job.Delay

Delay defers execution by duration.

```go
job := queue.NewJob("emails:send").Delay(300 * time.Millisecond)
```

#### <a id="queue-newjob"></a>queue.NewJob

NewJob creates a job value with a required job type.

```go
job := queue.NewJob("emails:send")
```

#### <a id="queue-job-onqueue"></a>queue.Job.OnQueue

OnQueue sets the target queue name.

```go
job := queue.NewJob("emails:send").OnQueue("critical")
```

#### <a id="queue-job-payload"></a>queue.Job.Payload

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

#### <a id="queue-job-payloadbytes"></a>queue.Job.PayloadBytes

PayloadBytes returns a copy of job payload bytes.

```go
job := queue.NewJob("emails:send").Payload([]byte(`{"id":1}`))
payload := job.PayloadBytes()
```

#### <a id="queue-job-payloadjson"></a>queue.Job.PayloadJSON

PayloadJSON marshals payload as JSON.

```go
job := queue.NewJob("emails:send").PayloadJSON(map[string]int{"id": 1})
```

#### <a id="queue-job-retry"></a>queue.Job.Retry

Retry sets max retry attempts.

```go
job := queue.NewJob("emails:send").Retry(4)
```

#### <a id="queue-job-timeout"></a>queue.Job.Timeout

Timeout sets per-job execution timeout.

```go
job := queue.NewJob("emails:send").Timeout(10 * time.Second)
```

#### <a id="queue-job-uniquefor"></a>queue.Job.UniqueFor

UniqueFor enables uniqueness dedupe within the given TTL.

```go
job := queue.NewJob("emails:send").UniqueFor(45 * time.Second)
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

#### <a id="queue-channelobserver-observe"></a>queue.ChannelObserver.Observe

Observe forwards an event to the configured channel.

```go
ch := make(chan queue.Event, 1)
observer := queue.ChannelObserver{Events: ch}
observer.Observe(queue.Event{Kind: queue.EventProcessStarted, Queue: "default"})
event := <-ch
```

#### <a id="queue-observer-observe"></a>queue.Observer.Observe

Observe handles a queue runtime event.

```go
var observer queue.Observer
observer.Observe(queue.Event{
	Kind:   queue.EventEnqueueAccepted,
	Driver: queue.DriverSync,
	Queue:  "default",
})
```

#### <a id="queue-observerfunc-observe"></a>queue.ObserverFunc.Observe

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

### Queue

#### <a id="queue-queue-dispatch"></a>queue.Queue.Dispatch

Dispatch submits a typed job payload using the default queue.

```go
var q queue.Queue
err := q.Dispatch(
	queue.NewJob("emails:send").
		Payload(map[string]any{"id": 1}).
		OnQueue("default"),
)
```

#### <a id="queue-queue-dispatchctx"></a>queue.Queue.DispatchCtx

DispatchCtx submits a typed job payload using the provided context.

```go
var q queue.Queue
err := q.DispatchCtx(
	context.Background(),
	queue.NewJob("emails:send").OnQueue("default"),
)
```

#### <a id="queue-queue-driver"></a>queue.Queue.Driver

Driver returns the active queue driver.

```go
var q queue.Queue
driver := q.Driver()
```

#### <a id="queue-queue-register"></a>queue.Queue.Register

Register associates a handler with a job type.

```go
var q queue.Queue
q.Register("emails:send", func(context.Context, queue.Job) error { return nil })
```

#### <a id="queue-queue-shutdown"></a>queue.Queue.Shutdown

Shutdown drains running work and releases resources.

```go
var q queue.Queue
err := q.Shutdown(context.Background())
```

#### <a id="queue-queue-startworkers"></a>queue.Queue.StartWorkers

StartWorkers starts worker execution.

```go
var q queue.Queue
err := q.StartWorkers(context.Background())
```

#### <a id="queue-queue-workers"></a>queue.Queue.Workers

Workers sets desired worker concurrency before StartWorkers.

```go
var q queue.Queue
q = q.Workers(4)
```

### Testing

#### <a id="queue-fakequeue-assertcount"></a>queue.FakeQueue.AssertCount

AssertCount fails when dispatch count is not expected.

```go
fake := queue.NewFake()
fake.AssertCount(nil, 1)
```

#### <a id="queue-fakequeue-assertdispatched"></a>queue.FakeQueue.AssertDispatched

AssertDispatched fails when jobType was not dispatched.

```go
fake := queue.NewFake()
fake.AssertDispatched(nil, "emails:send")
```

#### <a id="queue-fakequeue-assertdispatchedon"></a>queue.FakeQueue.AssertDispatchedOn

AssertDispatchedOn fails when jobType was not dispatched on queueName.

```go
fake := queue.NewFake()
	queue.NewJob("emails:send").
		OnQueue("critical"),
)
fake.AssertDispatchedOn(nil, "critical", "emails:send")
```

#### <a id="queue-fakequeue-assertdispatchedtimes"></a>queue.FakeQueue.AssertDispatchedTimes

AssertDispatchedTimes fails when jobType dispatch count does not match expected.

```go
fake := queue.NewFake()
fake.AssertDispatchedTimes(nil, "emails:send", 2)
```

#### <a id="queue-fakequeue-assertnotdispatched"></a>queue.FakeQueue.AssertNotDispatched

AssertNotDispatched fails when jobType was dispatched.

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

#### <a id="queue-fakequeue-dispatch"></a>queue.FakeQueue.Dispatch

Dispatch records a typed job payload in-memory using the fake default queue.

```go
fake := queue.NewFake()
err := fake.Dispatch(queue.NewJob("emails:send").OnQueue("default"))
```

#### <a id="queue-fakequeue-dispatchctx"></a>queue.FakeQueue.DispatchCtx

DispatchCtx submits a typed job payload using the provided context.

```go
fake := queue.NewFake()
ctx := context.Background()
err := fake.DispatchCtx(ctx, queue.NewJob("emails:send").OnQueue("default"))
fmt.Println(err == nil)
// Output: true
```

#### <a id="queue-fakequeue-driver"></a>queue.FakeQueue.Driver

Driver returns the active queue driver.

```go
fake := queue.NewFake()
driver := fake.Driver()
```

#### <a id="queue-newfake"></a>queue.NewFake

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

#### <a id="queue-fakequeue-records"></a>queue.FakeQueue.Records

Records returns a copy of all dispatch records.

```go
fake := queue.NewFake()
records := fake.Records()
fmt.Println(len(records), records[0].Job.Type)
// Output: 1 emails:send
```

#### <a id="queue-fakequeue-register"></a>queue.FakeQueue.Register

Register associates a handler with a job type.

```go
fake := queue.NewFake()
fake.Register("emails:send", func(context.Context, queue.Job) error { return nil })
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

#### <a id="queue-fakequeue-shutdown"></a>queue.FakeQueue.Shutdown

Shutdown drains running work and releases resources.

```go
fake := queue.NewFake()
err := fake.Shutdown(context.Background())
```

#### <a id="queue-fakequeue-startworkers"></a>queue.FakeQueue.StartWorkers

StartWorkers starts worker execution.

```go
fake := queue.NewFake()
err := fake.StartWorkers(context.Background())
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
