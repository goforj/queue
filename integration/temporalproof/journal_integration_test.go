//go:build integration

package temporalproof

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"go.temporal.io/api/common/v1"
	"go.temporal.io/api/history/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const (
	proofImage = "temporalio/temporal@sha256:2c344b4a39b4489fc6944db095f628f0c30659836faf11780a4dc435599e80e3"
)

var proofAddress string
var proofSequence atomic.Int64

// TestMain owns only this experiment's pinned local server and its cleanup.
func TestMain(m *testing.M) {
	if os.Getenv("TEMPORAL_PROOF") != "1" {
		os.Exit(m.Run())
	}
	worker.SetStickyWorkflowCacheSize(0)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        proofImage,
			Cmd:          []string{"server", "start-dev", "--ip", "0.0.0.0", "--headless", "--log-level", "error"},
			ExposedPorts: []string{"7233/tcp"},
			WaitingFor:   wait.ForListeningPort("7233/tcp").WithStartupTimeout(45 * time.Second),
		}, Started: true,
	})
	if err != nil {
		cancel()
		fmt.Fprintf(os.Stderr, "Temporal proof server startup: %v\n", err)
		os.Exit(1)
	}
	host, err := container.Host(ctx)
	if err == nil {
		port, portErr := container.MappedPort(ctx, "7233/tcp")
		err = portErr
		proofAddress = fmt.Sprintf("%s:%s", host, port.Port())
	}
	cancel()
	code := 1
	if err == nil {
		code = m.Run()
	} else {
		fmt.Fprintf(os.Stderr, "Temporal proof server address: %v\n", err)
	}
	cleanup, stop := context.WithTimeout(context.Background(), 15*time.Second)
	if err := container.Terminate(cleanup); err != nil {
		fmt.Fprintf(os.Stderr, "Temporal proof server cleanup: %v\n", err)
		code = 1
	}
	stop()
	os.Exit(code)
}

// requireProofServer keeps prototype experiments explicit instead of claiming
// the ordinary backend suite has exercised Temporal when it has not.
func requireProofServer(t *testing.T) {
	t.Helper()
	if proofAddress == "" {
		t.Skip("set TEMPORAL_PROOF=1 to run pinned real-server feasibility proofs")
	}
}

// newProofClient creates separate producer, worker and reader connections.
func newProofClient(t *testing.T, options client.Options) client.Client {
	t.Helper()
	options.HostPort = proofAddress
	options.Identity = "queue-protocol-proof"
	// Keep the SDK handshake inside the same explicit setup budget under the full backend suite's load.
	options.ConnectionOptions.GetSystemInfoTimeout = 20 * time.Second
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	c, err := client.DialContext(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

// readProofHistory pins one exact run, forces pagination and bounds total work.
func readProofHistory(ctx context.Context, c client.Client, id, runID string) (*history.History, int, error) {
	request := &workflowservice.GetWorkflowExecutionHistoryRequest{Namespace: "default", Execution: &common.WorkflowExecution{WorkflowId: id, RunId: runID}, MaximumPageSize: 2}
	result := &history.History{}
	for pages := 1; pages <= 64; pages++ {
		response, err := c.WorkflowService().GetWorkflowExecutionHistory(ctx, request)
		if err != nil {
			return nil, pages, err
		}
		if proto.Size(result)+proto.Size(response.History) > 1<<20 {
			return nil, pages, fmt.Errorf("proof history exceeded 1 MiB bound")
		}
		result.Events = append(result.Events, response.History.Events...)
		if len(response.NextPageToken) == 0 {
			return result, pages, nil
		}
		request.NextPageToken = response.NextPageToken
	}
	return nil, 64, fmt.Errorf("proof history exceeded 64-page bound")
}

// TestTemporalProofSameKeyJournal exercises accepted input before polling,
// same-key updates, pagination, worker-free reads and semantic replay controls.
func TestTemporalProofSameKeyJournal(t *testing.T) {
	runJournalProof(t, converter.GetDefaultDataConverter(), true)
}

// TestTemporalProofEncryptedJournal proves memo/start decoding and semantic
// replay can use a consistent bounded encrypted converter without live workers.
func TestTemporalProofEncryptedJournal(t *testing.T) {
	runJournalProof(t, encryptedProofConverter(t), false)
}

// runJournalProof uses one serialization policy for independent clients and replay.
func runJournalProof(t *testing.T, dc converter.DataConverter, export bool) {
	requireProofServer(t)
	producer := newProofClient(t, client.Options{DataConverter: dc})
	pollerClient := newProofClient(t, client.Options{DataConverter: dc})
	reader := newProofClient(t, client.Options{DataConverter: dc})
	id := fmt.Sprintf("queue-journal-proof-%d", proofSequence.Add(1))
	queue := id + "-tasks"
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	run, err := producer.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: id, TaskQueue: queue}, proofWorkflowName, "synthetic-input")
	if err != nil {
		t.Fatal(err)
	}
	initial, _, err := readProofHistory(ctx, reader, id, run.GetRunID())
	if err != nil {
		t.Fatal(err)
	}
	if len(initial.Events) == 0 || initial.Events[0].GetWorkflowExecutionStartedEventAttributes() == nil {
		t.Fatal("accepted input unavailable before polling")
	}
	var accepted string
	if err := dc.FromPayloads(initial.Events[0].GetWorkflowExecutionStartedEventAttributes().Input, &accepted); err != nil || accepted != "synthetic-input" {
		t.Fatalf("initial input = %q, err = %v", accepted, err)
	}
	w := worker.New(pollerClient, queue, worker.Options{})
	w.RegisterWorkflowWithOptions(journalProofWorkflow, workflow.RegisterOptions{Name: proofWorkflowName})
	w.RegisterActivityWithOptions(func(context.Context, string) error { return nil }, activity.RegisterOptions{Name: "proof-witness"})
	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	if err := run.Get(ctx, nil); err != nil {
		w.Stop()
		t.Fatal(err)
	}
	w.Stop()
	retained, pages, err := readProofHistory(ctx, reader, id, run.GetRunID())
	if err != nil {
		t.Fatal(err)
	}
	if pages <= 1 {
		t.Fatalf("pagination was not exercised: %d page", pages)
	}
	want := []proofDelta{{1, 1, "pending"}, {1, 2, "pending"}, {1, 3, "pending"}}
	actual := committedProofDeltas(t, retained, dc)
	if !reflect.DeepEqual(actual, want) {
		t.Fatalf("retained deltas=%+v, want %+v", actual, want)
	}
	assertSemanticReplay(t, retained, dc)
	if output := os.Getenv("TEMPORAL_PROOF_HISTORY_OUTPUT"); export && output != "" {
		encoded, err := protojson.MarshalOptions{Indent: "  "}.Marshal(retained)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(output, encoded, 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("SDK v1.45.0; CLI 1.8.0/server 1.31.2; %d history pages; same-key deltas retained; SDK-only negative control accepted and semantic comparison rejected", pages)
}
