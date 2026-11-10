//go:build integration

package temporal

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goforj/queue/driver/temporalqueue"
	"github.com/goforj/queue/queueconfig"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

// replayInitializationFixture fails only replacement-worker initialization, after an original workflow task has committed.
type replayInitializationFixture struct {
	interceptor.WorkerInterceptorBase
	mode   string
	failed atomic.Bool
}

// InterceptWorkflow keeps factory panic rejection distinct from initialization error handling.
func (f *replayInitializationFixture) InterceptWorkflow(_ workflow.Context, next interceptor.WorkflowInboundInterceptor) interceptor.WorkflowInboundInterceptor {
	if f.failed.Load() && f.mode == "factory panic" {
		panic("synthetic replay factory failure")
	}
	return &replayInitializationInbound{WorkflowInboundInterceptorBase: interceptor.WorkflowInboundInterceptorBase{Next: next}, fixture: f}
}

// replayInitializationInbound retains the configured failure point through SDK initialization.
type replayInitializationInbound struct {
	interceptor.WorkflowInboundInterceptorBase
	fixture *replayInitializationFixture
}

// Init models a replacement adapter that cannot initialize without changing native workflow code.
func (i *replayInitializationInbound) Init(next interceptor.WorkflowOutboundInterceptor) error {
	if i.fixture.failed.Load() {
		return errors.New("synthetic replay initialization failure")
	}
	return i.Next.Init(next)
}

// replayContextFactoryFixture separates contextual factory rejection from ordinary payload decoding errors.
type replayContextFactoryFixture struct {
	converter.DataConverter
	mode   string
	failed atomic.Bool
}

// DecodingLimits retains the fixture's bounded conversion contract for every successful contextual copy.
func (*replayContextFactoryFixture) DecodingLimits() temporalqueue.SerializationLimits {
	return temporalqueue.DefaultSerializationLimits()
}

// MaxEncodedOverhead retains the default converter's reservation policy.
func (*replayContextFactoryFixture) MaxEncodedOverhead() int { return 256 }

// WithContext preserves the complete SDK legacy contextual interface without failing activity/client binding.
func (f *replayContextFactoryFixture) WithContext(context.Context) converter.DataConverter { return f }

// WithWorkflowContext rejects only replacement-worker legacy factory construction.
func (f *replayContextFactoryFixture) WithWorkflowContext(workflow.Context) converter.DataConverter {
	if f.failed.Load() {
		switch f.mode {
		case "workflow panic":
			panic("synthetic contextual factory failure")
		case "workflow nil":
			return nil
		}
	}
	return f
}

// WithSerializationContext rejects only replacement contextual factory construction, leaving payload conversion unchanged.
func (f *replayContextFactoryFixture) WithSerializationContext(converter.SerializationContext) converter.DataConverter {
	if f.failed.Load() {
		switch f.mode {
		case "serialization panic":
			panic("synthetic serialization factory failure")
		case "serialization nil":
			return nil
		}
	}
	return f
}

// initialReplayStateWorkflow supplies committed initial state without any command that could mask premature replay closure.
func initialReplayStateWorkflow(ctx workflow.Context) (int, error) {
	total := 7
	if err := workflow.SetQueryHandler(ctx, "total", func() (int, error) { return total, nil }); err != nil {
		return 0, err
	}
	var finish bool
	workflow.GetSignalChannel(ctx, "finish").Receive(ctx, &finish)
	return total, nil
}

// TestTemporalDriverAcceptedUpdateSurvivesReplayOutage retains a committed update through adapter failure and worker replacement.
func TestTemporalDriverAcceptedUpdateSurvivesReplayOutage(t *testing.T) {
	verifyNativeReplayAdapterRecovery(t, false, "", "")
}

// TestTemporalDriverAcceptedWorkflowSurvivesReplayOutage retains an existing workflow when its original-start headers cannot temporarily decode.
func TestTemporalDriverAcceptedWorkflowSurvivesReplayOutage(t *testing.T) {
	verifyNativeReplayAdapterRecovery(t, true, "", "")
}

// TestTemporalDriverInterceptorInitializationSurvivesReplayFailure retains accepted state across replacement interceptor failures.
func TestTemporalDriverInterceptorInitializationSurvivesReplayFailure(t *testing.T) {
	for _, mode := range []string{"initialization error", "factory panic"} {
		t.Run(mode, func(t *testing.T) { verifyNativeReplayAdapterRecovery(t, false, mode, "") })
	}
}

// TestTemporalDriverContextFactorySurvivesReplayFailure retains accepted native state when a converter factory rejects during replay.
func TestTemporalDriverContextFactorySurvivesReplayFailure(t *testing.T) {
	for _, mode := range []string{"workflow panic", "workflow nil", "serialization panic", "serialization nil"} {
		t.Run(mode, func(t *testing.T) { verifyNativeReplayAdapterRecovery(t, false, "", mode) })
	}
}

// verifyNativeReplayAdapterRecovery prevents adapter failure from replacing committed native workflow state or outcomes.
func verifyNativeReplayAdapterRecovery(t *testing.T, startHeader bool, initialization, contextual string) {
	requireTemporalServer(t)
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	name := fmt.Sprintf("driver-native-replay-%t-%d", startHeader, time.Now().UnixNano())
	definition := nativeMessageWorkflow
	if startHeader || initialization != "" || contextual != "" {
		definition = initialReplayStateWorkflow
	}
	propagator := &nativeBoundaryPropagator{}
	initializationFixture := &replayInitializationFixture{mode: initialization}
	contextFixture := &replayContextFactoryFixture{DataConverter: converter.GetDefaultDataConverter(), mode: contextual}
	cfg := temporalqueue.Config{DriverBaseConfig: queueconfig.DriverBaseConfig{DefaultQueue: name}, ClientOptions: client.Options{HostPort: temporalAddress, ContextPropagators: []workflow.ContextPropagator{propagator}}, DefaultActivityTimeout: time.Second, ShutdownTimeout: time.Second, Register: func(r worker.Registry) error { r.RegisterWorkflow(definition); return nil }}
	if contextual != "" {
		cfg.ClientOptions.DataConverter = contextFixture
	}
	if initialization != "" {
		cfg.WorkerOptions.Interceptors = []interceptor.WorkerInterceptor{initializationFixture}
	}
	first, err := temporalqueue.NewWithConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Shutdown(context.Background()) })
	if err := first.StartWorkers(ctx); err != nil {
		t.Fatal(err)
	}
	sdk, err := temporalqueue.Client(first)
	if err != nil {
		t.Fatal(err)
	}
	selected := context.WithValue(ctx, nativeAvailabilityHeaderKey{}, true)
	startCtx := ctx
	if startHeader {
		startCtx = selected
	}
	run, err := sdk.ExecuteWorkflow(startCtx, client.StartWorkflowOptions{ID: name, TaskQueue: name}, definition)
	if err != nil {
		t.Fatal(err)
	}
	var value int
	if startHeader || initialization != "" || contextual != "" {
		query, err := sdk.QueryWorkflow(ctx, name, run.GetRunID(), "total")
		if err != nil {
			t.Fatal(err)
		}
		if err := query.Get(&value); err != nil || value != 7 {
			t.Fatal(value, err)
		}
	} else {
		update, err := sdk.UpdateWorkflow(selected, client.UpdateWorkflowOptions{WorkflowID: name, RunID: run.GetRunID(), UpdateName: "add", Args: []any{7}, WaitForStage: client.WorkflowUpdateStageCompleted})
		if err != nil {
			t.Fatal(err)
		}
		if err := update.Get(ctx, &value); err != nil || value != 7 {
			t.Fatal(value, err)
		}
	}

	committed := false
	original := sdk.GetWorkflowHistory(ctx, name, run.GetRunID(), false, enums.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
	for original.HasNext() {
		event, err := original.Next()
		if err != nil {
			t.Fatal(err)
		}
		if event.GetWorkflowTaskCompletedEventAttributes() != nil {
			committed = true
		}
	}
	if !committed {
		t.Fatal("fixture has no committed original workflow task to replay")
	}
	if err := first.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	second, err := temporalqueue.NewWithConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Shutdown(context.Background()) })
	sdk2, err := temporalqueue.Client(second)
	if err != nil {
		t.Fatal(err)
	}
	controller, err := client.Dial(client.Options{HostPort: temporalAddress})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(controller.Close)
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = controller.TerminateWorkflow(cleanup, name, run.GetRunID(), "synthetic native replay cleanup")
	})
	propagator.unavailable.Store(true)
	initializationFixture.failed.Store(true)
	contextFixture.failed.Store(true)
	replay, err := temporalqueue.NewWorkflowReplayer(second)
	if err != nil {
		t.Fatal(err)
	}
	if err := captureReplayFactoryError(func() error {
		return replay.ReplayWorkflowExecution(ctx, sdk2.WorkflowService(), nil, "default", workflow.Execution{ID: name, RunID: run.GetRunID()})
	}); err == nil {
		t.Fatal("replay accepted a changed accepted native state during propagation outage")
	}
	description, err := sdk2.DescribeWorkflowExecution(ctx, name, run.GetRunID())
	if err != nil {
		t.Fatal(err)
	}
	watermark := description.WorkflowExecutionInfo.HistoryLength
	if err := second.StartWorkers(ctx); err != nil {
		t.Fatal(err)
	}
	if err := controller.SignalWorkflow(ctx, name, run.GetRunID(), "finish", true); err != nil {
		t.Fatal(err)
	}
	observed, stop := context.WithTimeout(ctx, 15*time.Second)
	defer stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	failedTask := false
	for !failedTask {
		history := sdk2.GetWorkflowHistory(observed, name, run.GetRunID(), false, enums.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
		for history.HasNext() {
			event, err := history.Next()
			if err != nil {
				t.Fatal(err)
			}
			// A factory can abort before the SDK builds a failure response, leaving task timeout as the recorded refusal.
			if event.EventId > watermark && (event.GetWorkflowTaskFailedEventAttributes() != nil || event.GetWorkflowTaskTimedOutEventAttributes() != nil) {
				failedTask = true
			}
		}
		if !failedTask {
			select {
			case <-tick.C:
			case <-observed.Done():
				t.Fatal("replacement worker did not fail the rejected native replay task", observed.Err())
			}
		}
	}
	description, err = sdk2.DescribeWorkflowExecution(ctx, name, run.GetRunID())
	if err != nil {
		t.Fatal(err)
	}
	if description.WorkflowExecutionInfo.Status != enums.WORKFLOW_EXECUTION_STATUS_RUNNING {
		t.Fatal("workflow closed during rejected native replay", description.WorkflowExecutionInfo.Status)
	}
	propagator.unavailable.Store(false)
	initializationFixture.failed.Store(false)
	contextFixture.failed.Store(false)
	if err := sdk2.GetWorkflow(ctx, name, run.GetRunID()).Get(ctx, &value); err != nil {
		t.Fatal(err)
	}
	if value != 7 {
		t.Fatalf("accepted native state lost in successful workflow result: got=%d want=7", value)
	}
	if err := replay.ReplayWorkflowExecution(ctx, sdk2.WorkflowService(), nil, "default", workflow.Execution{ID: name, RunID: run.GetRunID()}); err != nil {
		t.Fatal("retained native history failed replay after adapter recovery", err)
	}
}

// captureReplayFactoryError treats either native SDK factory exception form as a refused replay.
func captureReplayFactoryError(call func() error) (result error) {
	defer func() {
		if recover() != nil {
			result = errors.New("replay factory threw an exception")
		}
	}()
	return call()
}
