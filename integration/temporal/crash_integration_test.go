//go:build integration

package temporal

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/goforj/queue"
	"github.com/goforj/queue/driver/temporalqueue"
	"github.com/goforj/queue/queueconfig"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/workflow"
)

// crashWitness records an external effect independently of worker process memory.
type crashWitness struct {
	JobID        string
	InvocationID string
	Attempt      int
}

// crashCommitInterceptor holds an uncommitted production journal command at an explicit process barrier.
type crashCommitInterceptor struct {
	interceptor.WorkerInterceptorBase
}

// crashCommitInbound installs a command-only subprocess barrier.
type crashCommitInbound struct {
	interceptor.WorkflowInboundInterceptorBase
}

// crashCommitOutbound blocks after command creation and before the workflow task can commit.
type crashCommitOutbound struct {
	interceptor.WorkflowOutboundInterceptorBase
}

// InterceptWorkflow preserves production workflow entry and registration.
func (*crashCommitInterceptor) InterceptWorkflow(_ workflow.Context, next interceptor.WorkflowInboundInterceptor) interceptor.WorkflowInboundInterceptor {
	return &crashCommitInbound{WorkflowInboundInterceptorBase: interceptor.WorkflowInboundInterceptorBase{Next: next}}
}

// Init instruments the actual SDK outbound boundary without changing the coordinator.
func (i *crashCommitInbound) Init(next interceptor.WorkflowOutboundInterceptor) error {
	return i.Next.Init(&crashCommitOutbound{WorkflowOutboundInterceptorBase: interceptor.WorkflowOutboundInterceptorBase{Next: next}})
}

// UpsertMemo reports generated-but-uncommitted production journal commands.
func (o *crashCommitOutbound) UpsertMemo(ctx workflow.Context, memo map[string]any) error {
	if err := o.Next.UpsertMemo(ctx, memo); err != nil {
		return err
	}
	fmt.Println("QUEUE_DRIVER_COMMIT_BARRIER")
	select {}
}

// TestTemporalDriverWorkerProcess runs only as the killable public-driver worker subprocess.
func TestTemporalDriverWorkerProcess(t *testing.T) {
	address := os.Getenv("QUEUE_DRIVER_WORKER_ADDRESS")
	if address == "" {
		t.Skip("subprocess entrypoint")
	}
	cfg := temporalqueue.Config{DriverBaseConfig: queueconfig.DriverBaseConfig{DefaultQueue: os.Getenv("QUEUE_DRIVER_WORKER_QUEUE")}, ClientOptions: client.Options{HostPort: address}, DefaultActivityTimeout: 2 * time.Second, ShutdownTimeout: time.Second}
	if os.Getenv("QUEUE_DRIVER_COMMIT_BARRIER") == "1" {
		cfg.WorkerOptions.Interceptors = []interceptor.WorkerInterceptor{&crashCommitInterceptor{}}
	}
	q, err := temporalqueue.NewWithConfig(cfg, queue.WithWorkers(2))
	if err != nil {
		t.Fatal(err)
	}

	if role := os.Getenv("QUEUE_DRIVER_CLIENT_ROLE"); role != "" {
		defer q.Shutdown(context.Background())
		if role == "producer" {
			var id string
			if os.Getenv("QUEUE_DRIVER_HOOK") == "1" {
				id, err = queue.Continue(q.Chain(queue.NewJob("business"))).OnCompletion(queue.NewJob("effect").Retry(1).Timeout(2 * time.Second)).Dispatch(t.Context())
			} else {
				var result queue.DispatchResult
				result, err = q.Dispatch(queue.NewJob("effect").Retry(1).Timeout(2 * time.Second))
				id = result.DispatchID
			}
			if err != nil {
				t.Fatal(err)
			}
			fmt.Println("QUEUE_DRIVER_ACCEPTED " + id)
			return
		}
		id := os.Getenv("QUEUE_DRIVER_WORKFLOW_ID")
		c, _ := temporalqueue.Client(q)
		if err := c.GetWorkflow(t.Context(), id, "").Get(t.Context(), nil); err != nil {
			t.Fatal(err)
		}
		if os.Getenv("QUEUE_DRIVER_HOOK") == "1" {
			state, err := q.FindChain(t.Context(), id)
			if err != nil || !state.Completed {
				t.Fatalf("reader state %+v %v", state, err)
			}
			outcomes, err := q.FindContinuations(t.Context(), id)
			if err != nil || len(outcomes) != 1 || outcomes[0].Status != "succeeded" {
				t.Fatalf("reader outcomes %+v %v", outcomes, err)
			}
		}
		fmt.Println("QUEUE_DRIVER_READ_COMPLETE " + id)
		return
	}
	q.Register("business", func(context.Context, queue.Message) error { return nil })
	q.Register("effect", func(ctx context.Context, m queue.Message) error {
		witness := crashWitness{JobID: m.JobID, Attempt: m.Attempt}
		if invocation, ok := m.Continuation(); ok {
			witness.InvocationID = invocation.ID
		}
		data, _ := json.Marshal(witness)
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, os.Getenv("QUEUE_DRIVER_WITNESS"), bytes.NewReader(data))
		if err != nil {
			return err
		}
		response, err := http.DefaultClient.Do(request)
		if response != nil {
			response.Body.Close()
		}
		return err
	})
	if err := q.StartWorkers(t.Context()); err != nil {
		t.Fatal(err)
	}
	fmt.Println("QUEUE_DRIVER_READY")
	<-t.Context().Done()
}

// startCrashWorker joins process output before wait and exposes explicit registration/command barriers.
func startCrashWorker(t *testing.T, name, url string, commit bool) (<-chan string, func()) {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(binary, "-test.run=^TestTemporalDriverWorkerProcess$", "-test.timeout=90s", "-test.v")
	command.Env = append(os.Environ(), "QUEUE_DRIVER_WORKER_ADDRESS="+temporalAddress, "QUEUE_DRIVER_WORKER_QUEUE="+name, "QUEUE_DRIVER_WITNESS="+url)
	if commit {
		command.Env = append(command.Env, "QUEUE_DRIVER_COMMIT_BARRIER=1")
	}
	output, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var diagnostics bytes.Buffer
	command.Stderr = &diagnostics
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	lines := make(chan string, 16)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer close(lines)
		scanner := bufio.NewScanner(output)
		for scanner.Scan() {
			if strings.HasPrefix(scanner.Text(), "QUEUE_DRIVER_") {
				lines <- scanner.Text()
			}
		}
	}()
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		_ = command.Process.Kill()
		<-done
		if err := command.Wait(); err == nil {
			t.Error("worker was not killed")
		}
	}
	t.Cleanup(stop)
	select {
	case line := <-lines:
		if line != "QUEUE_DRIVER_READY" {
			t.Fatalf("worker startup: %s", line)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("worker never registered")
	}
	return lines, stop
}

// TestTemporalDriverCrashAfterExternalEffect proves stable identity and durable retry after actual process death.
func TestTemporalDriverCrashAfterExternalEffect(t *testing.T) {
	requireTemporalServer(t)
	for _, hook := range []bool{false, true} {
		t.Run(fmt.Sprintf("continuation=%t", hook), func(t *testing.T) {
			name := fmt.Sprintf("driver-crash-%d", time.Now().UnixNano())
			q := newDriverResource(t, name, nil)
			effects := make(chan crashWitness, 4)
			release := make(chan struct{})
			witness := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var effect crashWitness
				if err := json.NewDecoder(r.Body).Decode(&effect); err != nil {
					t.Error(err)
					return
				}
				effects <- effect
				<-release
				w.WriteHeader(http.StatusOK)
			}))
			defer witness.Close()
			defer close(release)
			_, kill := startCrashWorker(t, name, witness.URL, false)
			id := runCrashClient(t, name, "producer", hook, "")
			var first crashWitness
			select {
			case first = <-effects:
			case <-time.After(20 * time.Second):
				t.Fatal("worker did not reach external effect")
			}
			kill()
			reader := newDriverResource(t, name, nil)
			reader.Register("business", func(context.Context, queue.Message) error { return nil })
			recovered := make(chan crashWitness, 1)
			reader.Register("effect", func(_ context.Context, m queue.Message) error {
				value := crashWitness{JobID: m.JobID, Attempt: m.Attempt}
				if invocation, ok := m.Continuation(); ok {
					value.InvocationID = invocation.ID
				}
				recovered <- value
				return nil
			})
			if err := reader.StartWorkers(t.Context()); err != nil {
				t.Fatal(err)
			}
			select {
			case second := <-recovered:
				if first.JobID != second.JobID || first.InvocationID != second.InvocationID || first.Attempt != 0 || second.Attempt != 1 {
					t.Fatalf("identity or budget lost: first=%+v recovered=%+v", first, second)
				}
			case <-time.After(20 * time.Second):
				t.Fatal("accepted effect did not recover")
			}
			c, _ := temporalqueue.Client(reader)
			if err := c.GetWorkflow(t.Context(), id, "").Get(t.Context(), nil); err != nil {
				t.Fatal(err)
			}
			if actual := runCrashClient(t, name, "reader", hook, id); actual != id {
				t.Fatal("separate reader identity mismatch")
			}
			if hook {
				outcomes, err := q.FindContinuations(t.Context(), id)
				if err != nil || len(outcomes) != 1 || outcomes[0].Status != "succeeded" {
					t.Fatalf("recovered outcomes %+v %v", outcomes, err)
				}
			}
		})
	}
}

// TestTemporalDriverCrashBeforeJournalCommit proves generated commands cannot partially publish durable hook state.
func TestTemporalDriverCrashBeforeJournalCommit(t *testing.T) {
	requireTemporalServer(t)
	name := fmt.Sprintf("driver-commit-%d", time.Now().UnixNano())
	q := newDriverResource(t, name, nil)
	lines, kill := startCrashWorker(t, name, "", true)
	id, err := queue.Continue(q.Chain(queue.NewJob("business"))).OnCompletion(queue.NewJob("business")).Dispatch(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	select {
	case line := <-lines:
		if line != "QUEUE_DRIVER_COMMIT_BARRIER" {
			t.Fatal(line)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("no uncommitted command barrier")
	}
	kill()
	before, err := q.FindContinuations(t.Context(), id)
	if err != nil || len(before) != 0 {
		t.Fatalf("uncommitted intents visible %+v %v", before, err)
	}
	q.Register("business", func(context.Context, queue.Message) error { return nil })
	if err := q.StartWorkers(t.Context()); err != nil {
		t.Fatal(err)
	}
	c, _ := temporalqueue.Client(q)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	if err := c.GetWorkflow(ctx, id, "").Get(ctx, nil); err != nil {
		t.Fatal(err)
	}
	after, err := q.FindContinuations(t.Context(), id)
	if err != nil || len(after) != 1 || after[0].Status != "succeeded" {
		t.Fatalf("recovered commit %+v %v", after, err)
	}
}

// runCrashClient uses independent producer and reader processes with no original worker memory.
func runCrashClient(t *testing.T, name, role string, hook bool, id string) string {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(binary, "-test.run=^TestTemporalDriverWorkerProcess$", "-test.timeout=30s", "-test.v")
	command.Env = append(os.Environ(), "QUEUE_DRIVER_WORKER_ADDRESS="+temporalAddress, "QUEUE_DRIVER_WORKER_QUEUE="+name, "QUEUE_DRIVER_CLIENT_ROLE="+role, "QUEUE_DRIVER_WORKFLOW_ID="+id)
	if hook {
		command.Env = append(command.Env, "QUEUE_DRIVER_HOOK=1")
	}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("independent %s process failed: %v %s", role, err, output)
	}
	prefix := "QUEUE_DRIVER_ACCEPTED "
	if role == "reader" {
		prefix = "QUEUE_DRIVER_READ_COMPLETE "
	}
	for _, line := range strings.Split(string(output), "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimPrefix(line, prefix)
		}
	}
	t.Fatalf("independent %s returned no identity", role)
	return ""
}
