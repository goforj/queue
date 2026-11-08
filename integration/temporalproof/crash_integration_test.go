//go:build integration

package temporalproof

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

const proofCommitBarrier = "QUEUE_TEMPORAL_PROOF_BEFORE_TASK_COMMIT"

type blockJournalCommit struct {
	interceptor.WorkerInterceptorBase
}
type blockJournalInbound struct {
	interceptor.WorkflowInboundInterceptorBase
}
type blockJournalOutbound struct {
	interceptor.WorkflowOutboundInterceptorBase
}

// InterceptWorkflow installs a subprocess-only crash barrier before task commit.
func (*blockJournalCommit) InterceptWorkflow(_ workflow.Context, next interceptor.WorkflowInboundInterceptor) interceptor.WorkflowInboundInterceptor {
	return &blockJournalInbound{WorkflowInboundInterceptorBase: interceptor.WorkflowInboundInterceptorBase{Next: next}}
}

// Init preserves normal workflow entry and wraps only the memo command boundary.
func (b *blockJournalInbound) Init(next interceptor.WorkflowOutboundInterceptor) error {
	return b.Next.Init(&blockJournalOutbound{WorkflowOutboundInterceptorBase: interceptor.WorkflowOutboundInterceptorBase{Next: next}})
}

// UpsertMemo blocks after generating a command but before the SDK can commit
// its workflow task. This instrumentation is never part of application code.
func (b *blockJournalOutbound) UpsertMemo(ctx workflow.Context, memo map[string]any) error {
	if err := b.Next.UpsertMemo(ctx, memo); err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, proofCommitBarrier)
	select {}
}

// TestTemporalProofWorkerProcess is the killable worker entrypoint for the
// crash proof. It does not start a server or run during normal suite execution.
func TestTemporalProofWorkerProcess(t *testing.T) {
	address := os.Getenv("QUEUE_TEMPORAL_PROOF_WORKER_ADDRESS")
	if address == "" {
		t.Skip("subprocess entrypoint only")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	c, err := client.DialContext(ctx, client.Options{HostPort: address, Identity: "queue-crash-proof-worker"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	w := worker.New(c, os.Getenv("QUEUE_TEMPORAL_PROOF_TASK_QUEUE"), worker.Options{Interceptors: []interceptor.WorkerInterceptor{&blockJournalCommit{}}})
	w.RegisterWorkflowWithOptions(journalProofWorkflow, workflow.RegisterOptions{Name: proofWorkflowName})
	w.RegisterActivityWithOptions(func(context.Context, string) error { return nil }, activity.RegisterOptions{Name: "proof-witness"})
	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	<-ctx.Done()
	w.Stop()
	t.Fatal("subprocess was not killed at the explicit command barrier")
}

// TestTemporalProofCrashBeforeJournalCommit kills a real worker process after
// a memo command is generated and proves no partial delta/activity is visible.
// A separate compatible worker then recovers the same retained execution.
func TestTemporalProofCrashBeforeJournalCommit(t *testing.T) {
	requireProofServer(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	producer := newProofClient(t, client.Options{})
	reader := newProofClient(t, client.Options{})
	id := fmt.Sprintf("queue-crash-proof-%d", proofSequence.Add(1))
	run, err := producer.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: id, TaskQueue: id, WorkflowTaskTimeout: 3 * time.Second}, proofWorkflowName, "synthetic-crash-input")
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(ctx, executable, "-test.run=^TestTemporalProofWorkerProcess$", "-test.v")
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "TEMPORAL_PROOF=") && !strings.HasPrefix(entry, "TEMPORAL_PROOF_HISTORY_OUTPUT=") {
			command.Env = append(command.Env, entry)
		}
	}
	command.Env = append(command.Env, "QUEUE_TEMPORAL_PROOF_WORKER_ADDRESS="+proofAddress, "QUEUE_TEMPORAL_PROOF_TASK_QUEUE="+id)
	output, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	barrier := make(chan struct{})
	scanDone := make(chan struct{})
	go func() {
		defer close(scanDone)
		scanner := bufio.NewScanner(output)
		for scanner.Scan() {
			if scanner.Text() == proofCommitBarrier {
				close(barrier)
			}
		}
	}()
	waited := false
	defer func() {
		if !waited {
			_ = command.Process.Kill()
			<-scanDone
			_ = command.Wait()
		}
	}()
	select {
	case <-barrier:
	case <-scanDone:
		err := command.Wait()
		waited = true
		t.Fatalf("worker exited before crash barrier: %v", err)
	case <-ctx.Done():
		t.Fatal("worker did not reach crash barrier: ", ctx.Err())
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	// StdoutPipe must be fully drained before Wait closes its read end.
	<-scanDone
	err = command.Wait()
	waited = true
	if err == nil {
		t.Fatal("worker subprocess did not die")
	}
	before, _, err := readProofHistory(ctx, reader, id, run.GetRunID())
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range before.Events {
		if event.GetWorkflowPropertiesModifiedEventAttributes() != nil || event.GetActivityTaskScheduledEventAttributes() != nil {
			t.Fatal("uncommitted command escaped worker memory")
		}
	}
	poller := newProofClient(t, client.Options{})
	w := worker.New(poller, id, worker.Options{})
	w.RegisterWorkflowWithOptions(journalProofWorkflow, workflow.RegisterOptions{Name: proofWorkflowName})
	var witnesses atomic.Int32
	w.RegisterActivityWithOptions(func(context.Context, string) error { witnesses.Add(1); return nil }, activity.RegisterOptions{Name: "proof-witness"})
	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	if err := run.Get(ctx, nil); err != nil {
		w.Stop()
		t.Fatal(err)
	}
	w.Stop()
	after, _, err := readProofHistory(ctx, reader, id, run.GetRunID())
	if err != nil {
		t.Fatal(err)
	}
	if deltas := committedProofDeltas(t, after, converter.GetDefaultDataConverter()); len(deltas) != 3 {
		t.Fatalf("recovered deltas=%+v", deltas)
	}
	if witnesses.Load() != 1 {
		t.Fatalf("witness calls=%d, want 1", witnesses.Load())
	}
	t.Log("worker killed before workflow-task commit; no partial memo/activity observed; replacement worker committed three ordered deltas and one witness activity")
}
