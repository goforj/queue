<p align="center">
  <img src="./docs/images/logo.png?v=1" width="420" alt="queue logo">
</p>

<p align="center">
    queue gives your services one queue API with Redis, SQL, and in-process drivers.
</p>

<p align="center">
  <a href="https://pkg.go.dev/github.com/goforj/queue"><img src="https://pkg.go.dev/badge/github.com/goforj/queue.svg" alt="Go Reference"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-blue.svg" alt="License: MIT"></a>
  <a href="https://github.com/goforj/queue/actions/workflows/test.yml"><img src="https://github.com/goforj/queue/actions/workflows/test.yml/badge.svg?branch=main" alt="CI: unit+race+integration matrix"></a>
  <a href="https://codecov.io/gh/goforj/queue"><img src="https://codecov.io/gh/goforj/queue/branch/main/graph/badge.svg" alt="Coverage"></a>
  <a href="https://goreportcard.com/report/github.com/goforj/queue"><img src="https://goreportcard.com/badge/github.com/goforj/queue" alt="Go Report Card"></a>
    <img src="https://img.shields.io/github/v/tag/goforj/queue?label=version&sort=semver" alt="Latest tag">
    <a href="https://golang.org"><img src="https://img.shields.io/badge/go-1.23+-blue?logo=go" alt="Go version"></a>
</p>

<p align="center">
  <a href="https://github.com/goforj/queue/actions/workflows/test.yml"><img src="https://img.shields.io/badge/integration%20matrix-7%2F7%20backends-brightgreen" alt="Integration matrix backends"></a>
  <a href="https://github.com/goforj/queue/actions/workflows/soak.yml"><img src="https://img.shields.io/badge/soak%2Fchaos-7%2F7%20backends-brightgreen" alt="Soak & chaos backends"></a>
  <img src="https://img.shields.io/badge/tests-unit%20%E2%9C%85%20|%20race%20%E2%9C%85%20|%20integration%20%E2%9C%85%20|%20scenarios%20%E2%9C%85-brightgreen" alt="Test suites">
  <img src="https://img.shields.io/badge/options-delay%20|%20backoff%20|%20timeout%20|%20retry%20|%20unique%20|%20queue-brightgreen" alt="Options covered">
</p>

## What queue is

queue is a backend-agnostic job dispatcher. Your application code only depends on `queue.Dispatcher`, `queue.Task`, and fluent enqueue options. The driver decides whether work runs via Redis/Asynq, a SQL table, an in-process worker pool, or synchronously in the caller.

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
| **Arguments** | [Arg](#arg) |
| **Construction** | [Command](#command) |
| **Context** | [WithContext](#withcontext) [WithDeadline](#withdeadline) [WithTimeout](#withtimeout) |
| **Debugging** | [Args](#args) [ShellEscaped](#shellescaped) [String](#string) |
| **Decoding** | [Decode](#decode) [DecodeJSON](#decodejson) [DecodeWith](#decodewith) [DecodeYAML](#decodeyaml) [FromCombined](#fromcombined) [FromStderr](#fromstderr) [FromStdout](#fromstdout) [Into](#into) [Trim](#trim) |
| **Environment** | [Env](#env) [EnvAppend](#envappend) [EnvInherit](#envinherit) [EnvList](#envlist) [EnvOnly](#envonly) |
| **Errors** | [Error](#error) [Unwrap](#unwrap) |
| **Execution** | [CombinedOutput](#combinedoutput) [Output](#output) [OutputBytes](#outputbytes) [OutputTrimmed](#outputtrimmed) [Run](#run) [Start](#start) [OnExecCmd](#onexeccmd) |
| **Input** | [StdinBytes](#stdinbytes) [StdinFile](#stdinfile) [StdinReader](#stdinreader) [StdinString](#stdinstring) |
| **OS Controls** | [CreationFlags](#creationflags) [HideWindow](#hidewindow) [Pdeathsig](#pdeathsig) [Setpgid](#setpgid) [Setsid](#setsid) |
| **Pipelining** | [Pipe](#pipe) [PipeBestEffort](#pipebesteffort) [PipeStrict](#pipestrict) [PipelineResults](#pipelineresults) |
| **Process** | [GracefulShutdown](#gracefulshutdown) [Interrupt](#interrupt) [KillAfter](#killafter) [Send](#send) [Terminate](#terminate) [Wait](#wait) |
| **Results** | [IsExitCode](#isexitcode) [IsSignal](#issignal) [OK](#ok) |
| **Shadow Print** | [ShadowOff](#shadowoff) [ShadowOn](#shadowon) [ShadowPrint](#shadowprint) [WithFormatter](#withformatter) [WithMask](#withmask) [WithPrefix](#withprefix) |
| **Streaming** | [OnStderr](#onstderr) [OnStdout](#onstdout) [StderrWriter](#stderrwriter) [StdoutWriter](#stdoutwriter) |
| **WorkingDir** | [Dir](#dir) |


## Arguments

### <a id="arg"></a>Arg

Arg appends arguments to the command.

```go
cmd := execx.Command("printf").Arg("hello")
out, _ := cmd.Output()
fmt.Print(out)
// hello
```

## Construction

### <a id="command"></a>Command

Command constructs a new command without executing it.

```go
cmd := execx.Command("printf", "hello")
out, _ := cmd.Output()
fmt.Print(out)
// hello
```

## Context

### <a id="withcontext"></a>WithContext

WithContext binds the command to a context.

```go
ctx, cancel := context.WithTimeout(context.Background(), time.Second)
defer cancel()
res, _ := execx.Command("go", "env", "GOOS").WithContext(ctx).Run()
fmt.Println(res.ExitCode == 0)
// #bool true
```

### <a id="withdeadline"></a>WithDeadline

WithDeadline binds the command to a deadline.

```go
res, _ := execx.Command("go", "env", "GOOS").WithDeadline(time.Now().Add(2 * time.Second)).Run()
fmt.Println(res.ExitCode == 0)
// #bool true
```

### <a id="withtimeout"></a>WithTimeout

WithTimeout binds the command to a timeout.

```go
res, _ := execx.Command("go", "env", "GOOS").WithTimeout(2 * time.Second).Run()
fmt.Println(res.ExitCode == 0)
// #bool true
```

## Debugging

### <a id="args"></a>Args

Args returns the argv slice used for execution.

```go
cmd := execx.Command("go", "env", "GOOS")
fmt.Println(strings.Join(cmd.Args(), " "))
// #string go env GOOS
```

### <a id="shellescaped"></a>ShellEscaped

ShellEscaped returns a shell-escaped string for logging only.

```go
cmd := execx.Command("echo", "hello world", "it's")
fmt.Println(cmd.ShellEscaped())
// #string echo 'hello world' "it's"
```

### <a id="string"></a>String

String returns a human-readable representation of the command.

```go
cmd := execx.Command("echo", "hello world", "it's")
fmt.Println(cmd.String())
// #string echo "hello world" it's
```

## Decoding

### <a id="decode"></a>Decode

Decode configures a custom decoder for this command.
Decoding reads from stdout by default; use FromStdout, FromStderr, or FromCombined to select a source.

```go
type payload struct {
    Name string
}
decoder := execx.DecoderFunc(func(data []byte, dst any) error {
    out, ok := dst.(*payload)
    if !ok {
        return fmt.Errorf("expected *payload")
    }
    _, val, ok := strings.Cut(string(data), "=")
    if !ok {
        return fmt.Errorf("invalid payload")
    }
    out.Name = val
    return nil
})
var out payload
_ = execx.Command("printf", "name=gopher").
    Decode(decoder).
    Into(&out)
fmt.Println(out.Name)
// #string gopher
```

### <a id="decodejson"></a>DecodeJSON

DecodeJSON configures JSON decoding for this command.
Decoding reads from stdout by default; use FromStdout, FromStderr, or FromCombined to select a source.

```go
type payload struct {
    Name string `json:"name"`
}
var out payload
_ = execx.Command("printf", `{"name":"gopher"}`).
    DecodeJSON().
    Into(&out)
fmt.Println(out.Name)
// #string gopher
```

### <a id="decodewith"></a>DecodeWith

DecodeWith executes the command and decodes stdout into dst.

```go
type payload struct {
    Name string `json:"name"`
}
var out payload
_ = execx.Command("printf", `{"name":"gopher"}`).
    DecodeWith(&out, execx.DecoderFunc(json.Unmarshal))
fmt.Println(out.Name)
// #string gopher
```

### <a id="decodeyaml"></a>DecodeYAML

DecodeYAML configures YAML decoding for this command.
Decoding reads from stdout by default; use FromStdout, FromStderr, or FromCombined to select a source.

```go
type payload struct {
    Name string `yaml:"name"`
}
var out payload
_ = execx.Command("printf", "name: gopher").
    DecodeYAML().
    Into(&out)
fmt.Println(out.Name)
// #string gopher
```

### <a id="fromcombined"></a>FromCombined

FromCombined decodes from combined stdout+stderr.
