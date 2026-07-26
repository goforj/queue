# Legacy API migration

The root `github.com/goforj/queue` package is the supported application API. This release removes the retired `bus` and `queuefake` compatibility packages and their duplicate public models.

## Package and configuration replacements

| Removed surface | Replacement |
| --- | --- |
| `github.com/goforj/queue/bus` jobs, workflows, middleware, stores, and events | Root `queue` types and methods, with the signature changes below |
| `github.com/goforj/queue/queuefake.New()` | `queue.NewFake()` |
| `queue.Config.Observer` | Pass `queue.WithObserver(...)` to the constructor |
| Root workflow observer aliases | `queue.Observer`, `queue.ObserverFunc`, and `queue.Event` |
| Redis `ServerLogger` | `DriverBaseConfig.Logger` |

For example, replace observer configuration in the root config:

```go
q, err := queue.New(
	queue.Config{Driver: queue.DriverWorkerpool},
	queue.WithObserver(observer),
)
```

Workflow builders remain on `*queue.Queue`, so calls such as `Chain`, `Batch`, `Register`, `FindChain`, and `FindBatch` migrate by changing their package-owned job, message, state, and callback types to the root equivalents. `queue.NewFake()` provides the canonical recording fake for direct jobs and workflows.

### Jobs and payload encoding

`bus.NewJob` accepted a payload as its second argument and encoded that value when the job was dispatched. Root jobs use a fluent payload method and encode immediately:

```go
// Before
job := bus.NewJob("reports:build", payload).OnQueue("critical")

// After
job := queue.NewJob("reports:build").Payload(payload).OnQueue("critical")
```

This timing difference matters if the application mutates `payload` between job construction and dispatch: the root job retains the bytes produced by `Payload`, while the removed bus job observed the value at dispatch time. Marshal failures are retained on the root job and returned when it is dispatched.

Root `queue.Job` does not expose the former public `Payload` and `Options` fields. Replace composite literals with `queue.NewJob` plus `Payload`, `OnQueue`, `Delay`, `Timeout`, `Retry`, `Backoff`, and `UniqueFor`.

### Fakes

`queue.NewFake()` combines direct and workflow recording, so the former `Queue()` and `Workflow()` accessors are unnecessary. The common assertion methods remain on `*queue.FakeQueue`.

The removed `Count`, `CountJob`, and `CountOn` helpers have no direct methods. Use `len(fake.Records())`, inspect `fake.Records()`, or use `AssertCount`, `AssertDispatchedTimes`, and `AssertDispatchedOn`. `AssertBatched` now receives the complete `queue.BatchRecord` instead of the former `bus.BatchSpec` projection:

```go
fake.AssertBatched(t, func(record queue.BatchRecord) bool {
	return len(record.Jobs) == 2
})
```

### Raw runtime construction

The removed `bus.New(busruntime.Runtime)` and `bus.NewWithStore(busruntime.Runtime, ...)` constructors have no root-package replacement. They exposed an orchestration-only transport seam that bypassed the canonical `queue.Queue` driver lifecycle, readiness, administration, and direct-delivery contracts; preserving it would retain a second public runtime model.

Applications using that advanced seam must move to a supported root or driver-module constructor. A custom transport must be implemented as a supported queue driver before upgrading; because driver construction is internal, out-of-tree transports should remain on the last compatible release while they are ported or contributed as a repository driver. Do not import `internal/driverbridge`.

## Temporal adapter boundary

The removed `github.com/goforj/queue/bus/driver/temporal` package has no root-package replacement. It adapted a caller-supplied abstract `Engine`; it was not a queue backend and did not provide the queue-backed durability guarantees documented for root workflows.

Applications that used that adapter must integrate their Temporal client or other external workflow engine directly. Applications that need queue-owned chains and batches should use the root `queue.Queue` workflow API and a supported `queue.WorkflowStore`.

The Temporal adapter removal changes source imports and construction only. It does not convert or delete persisted queue jobs or workflow records, change the queue wire protocol, or require a queue rollout migration.
