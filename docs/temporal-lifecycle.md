# Native SDK lifecycle ownership

The Temporal driver owns its worker set, its owned client handles, and callback admission. The Temporal SDK owns its process-wide sticky workflow cache. These are separate lifetimes.

This is an implementation decision that resolves the original proposal's blanket interceptor-drain requirement. Waiting for every suspended native workflow coroutine would prevent ordinary worker shutdown. Purging the global cache from one queue would affect other queue resources and application-created SDK workers. The driver does neither; native SDK execution and isolation are preserved.

| Owner | Lifetime |
| --- | --- |
| Queue resource | Workers, owned client handles, admitted activities, synchronous query/signal/update-validation callbacks, adapter factories/initialization, and protected conversion/logging/metrics callbacks |
| Binding owner | Borrowed binding and native SDK calls using that binding; close borrowers and join callers before closing it |
| Application/SDK host | Native workflow/update coroutines retained in the SDK cache, including deferred interceptor span finalization and dependencies captured across workflow yields |

`Shutdown` closes new admission, joins the worker stop arbiter, waits for admitted resource callbacks, and then closes owned handles. A deadline reports incomplete resource shutdown; keep those dependencies alive and call `Shutdown` again to join completion. A successful shutdown permits cleanup of resource-owned dependencies.

Native workflow and update code must remain replay-safe. Use activities for resource-owned application I/O. Tracers, exporters and other dependencies captured by native coroutine defers must have a process/cache lifetime or safely tolerate late finalization. Queue shutdown does not own arbitrary application dependencies captured by SDK interceptors.

The SDK exposes a global `worker.PurgeStickyWorkflowCache`, which is legal only after **all** SDK workers in the process stop. Purge starts asynchronous coroutine cleanup; returning from purge is not a callback join. The application may coordinate that global lifecycle, but an individual queue cannot infer ownership of unrelated SDK workers. Ordinary queue shutdown never calls it.

The pinned SDK ownership is visible in its [public cache API](https://github.com/temporalio/sdk-go/blob/v1.45.0/worker/worker.go), [shared worker cache](https://github.com/temporalio/sdk-go/blob/v1.45.0/internal/internal_worker_cache.go), and [asynchronous coroutine cleanup](https://github.com/temporalio/sdk-go/blob/v1.45.0/internal/internal_workflow.go). These implementation references are evidence, not APIs applications should import.

The checked-in [native tracing lifecycle test](../integration/temporal/native_cache_lifecycle_integration_test.go) uses the actual SDK tracing interceptor. It stops the queue successfully, retains its application tracer, then observes deferred span finalization during legal SDK cache cleanup. Global cleanup runs in an isolated test subprocess, so it cannot evict another test's workers. Activity and synchronous-message drain have separate real-server tests.

```sh
INTEGRATION_BACKEND=temporal GOWORK=off go -C integration test -race -tags=integration ./temporal -run '^TestTemporalDriverNativeCacheOwnership$'
```
