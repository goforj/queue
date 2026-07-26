# Legacy API migration

The root `github.com/goforj/queue` package is the supported application API. This release removes the retired `bus` and `queuefake` compatibility packages and their duplicate public models.

## Package and configuration replacements

| Removed surface | Replacement |
| --- | --- |
| `github.com/goforj/queue/bus` jobs, workflows, middleware, stores, and events | Equivalent root `queue` types and methods |
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

## Temporal adapter boundary

The removed `github.com/goforj/queue/bus/driver/temporal` package has no root-package replacement. It adapted a caller-supplied abstract `Engine`; it was not a queue backend and did not provide the queue-backed durability guarantees documented for root workflows.

Applications that used that adapter must integrate their Temporal client or other external workflow engine directly. Applications that need queue-owned chains and batches should use the root `queue.Queue` workflow API and a supported `queue.WorkflowStore`.

This removal changes source imports only. It does not convert or delete persisted queue jobs or workflow records, change the wire protocol, or require a rollout migration.
