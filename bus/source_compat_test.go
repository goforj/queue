package bus_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/goforj/queue"
	"github.com/goforj/queue/bus"
	"github.com/goforj/queue/bus/driver/temporal"
)

var (
	_ bus.Bus            = (*sourceCompatBus)(nil)
	_ bus.Store          = (*sourceCompatStore)(nil)
	_ bus.Observer       = sourceCompatObserver{}
	_ bus.Middleware     = sourceCompatMiddleware{}
	_ bus.ChainBuilder   = (*sourceCompatChainBuilder)(nil)
	_ bus.BatchBuilder   = (*sourceCompatBatchBuilder)(nil)
	_ queue.ChainBuilder = (*sourceCompatRootChainBuilder)(nil)
	_ queue.BatchBuilder = (*sourceCompatRootBatchBuilder)(nil)
	_ bus.Bus            = (*temporal.Adapter)(nil)
)

var (
	sourceCompatBusContextFromRoot      bus.Context            = queue.Message{SchemaVersion: 1}
	sourceCompatRootMessageFromBus      queue.Message          = bus.Context{SchemaVersion: 1}
	sourceCompatBusResultFromRoot       bus.DispatchResult     = queue.DispatchResult{DispatchID: "root"}
	sourceCompatRootResultFromBus       queue.DispatchResult   = bus.DispatchResult{DispatchID: "bus"}
	sourceCompatBusOptionsFromRoot      bus.JobOptions         = queue.StoredJobOptions{Queue: "root"}
	sourceCompatRootOptionsFromBus      queue.StoredJobOptions = bus.JobOptions{Queue: "bus"}
	sourceCompatBusChainStateFromRoot   bus.ChainState         = queue.ChainState{ChainID: "root-chain"}
	sourceCompatRootChainStateFromBus   queue.ChainState       = bus.ChainState{ChainID: "bus-chain"}
	sourceCompatBusBatchStateFromRoot   bus.BatchState         = queue.BatchState{BatchID: "root-batch"}
	sourceCompatRootBatchStateFromBus   queue.BatchState       = bus.BatchState{BatchID: "bus-batch"}
	sourceCompatRootMiddleware          queue.Middleware       = sourceCompatMiddleware{}
	sourceCompatBusMiddlewareFromRoot   bus.Middleware         = sourceCompatRootMiddleware
	sourceCompatRootMiddlewareRoundTrip queue.Middleware       = sourceCompatBusMiddlewareFromRoot
	sourceCompatRootStore               queue.WorkflowStore    = &sourceCompatStore{}
	sourceCompatBusStoreFromRoot        bus.Store              = sourceCompatRootStore
	sourceCompatRootStoreRoundTrip      queue.WorkflowStore    = sourceCompatBusStoreFromRoot
	sourceCompatRootMiddlewareFunc      queue.MiddlewareFunc   = bus.MiddlewareFunc(func(ctx context.Context, message bus.Context, next bus.Next) error {
		return next(ctx, message)
	})
	sourceCompatBusMiddlewareFunc bus.MiddlewareFunc = sourceCompatRootMiddlewareFunc
)

type sourceCompatBus struct{}

type sourceCompatJobOptions bus.JobOptions

type sourceCompatDispatchResult bus.DispatchResult

type sourceCompatChainRecord bus.ChainRecord

type sourceCompatChainState bus.ChainState

type sourceCompatBatchRecord bus.BatchRecord

type sourceCompatBatchState bus.BatchState

type sourceCompatSQLStoreConfig bus.SQLStoreConfig

// Register accepts the legacy named bus handler contract.
func (*sourceCompatBus) Register(string, bus.Handler) {}

// Dispatch returns a legacy dispatch result for a legacy job value.
func (*sourceCompatBus) Dispatch(context.Context, bus.Job) (bus.DispatchResult, error) {
	return bus.DispatchResult{}, nil
}

// Chain returns a custom builder with the legacy self-returning method set.
func (*sourceCompatBus) Chain(...bus.Job) bus.ChainBuilder {
	return &sourceCompatChainBuilder{}
}

// Batch returns a custom builder with the legacy self-returning method set.
func (*sourceCompatBus) Batch(...bus.Job) bus.BatchBuilder {
	return &sourceCompatBatchBuilder{}
}

// StartWorkers preserves the legacy lifecycle signature.
func (*sourceCompatBus) StartWorkers(context.Context) error { return nil }

// Shutdown preserves the legacy lifecycle signature.
func (*sourceCompatBus) Shutdown(context.Context) error { return nil }

// FindBatch returns the legacy batch state type.
func (*sourceCompatBus) FindBatch(context.Context, string) (bus.BatchState, error) {
	return bus.BatchState{}, nil
}

// FindChain returns the legacy chain state type.
func (*sourceCompatBus) FindChain(context.Context, string) (bus.ChainState, error) {
	return bus.ChainState{}, nil
}

// Prune preserves the legacy workflow retention signature.
func (*sourceCompatBus) Prune(context.Context, time.Time) error { return nil }

type sourceCompatStore struct{}

// CreateChain accepts the legacy chain record type.
func (*sourceCompatStore) CreateChain(context.Context, bus.ChainRecord) error { return nil }

// AdvanceChain returns the legacy opaque chain node type.
func (*sourceCompatStore) AdvanceChain(context.Context, string, string) (*bus.ChainNode, bool, error) {
	return nil, false, nil
}

// FailChain preserves the legacy chain failure signature.
func (*sourceCompatStore) FailChain(context.Context, string, error) error { return nil }

// GetChain returns the legacy chain state type.
func (*sourceCompatStore) GetChain(context.Context, string) (bus.ChainState, error) {
	return bus.ChainState{}, nil
}

// CreateBatch accepts the legacy batch record type.
func (*sourceCompatStore) CreateBatch(context.Context, bus.BatchRecord) error { return nil }

// MarkBatchJobStarted preserves the legacy batch start signature.
func (*sourceCompatStore) MarkBatchJobStarted(context.Context, string, string) error { return nil }

// MarkBatchJobSucceeded returns the legacy batch state and completion flag.
func (*sourceCompatStore) MarkBatchJobSucceeded(context.Context, string, string) (bus.BatchState, bool, error) {
	return bus.BatchState{}, false, nil
}

// MarkBatchJobFailed preserves the legacy failure-cause argument and result types.
func (*sourceCompatStore) MarkBatchJobFailed(context.Context, string, string, error) (bus.BatchState, bool, error) {
	return bus.BatchState{}, false, nil
}

// CancelBatch preserves the legacy batch cancellation signature.
func (*sourceCompatStore) CancelBatch(context.Context, string) error { return nil }

// GetBatch returns the legacy batch state type.
func (*sourceCompatStore) GetBatch(context.Context, string) (bus.BatchState, error) {
	return bus.BatchState{}, nil
}

// MarkCallbackInvoked preserves the legacy callback idempotency signature.
func (*sourceCompatStore) MarkCallbackInvoked(context.Context, string) (bool, error) {
	return true, nil
}

// Prune preserves the legacy store retention signature.
func (*sourceCompatStore) Prune(context.Context, time.Time) error { return nil }

type sourceCompatObserver struct{}

// Observe accepts the keyed legacy event model without relying on its rejected unkeyed layout.
func (sourceCompatObserver) Observe(context.Context, bus.Event) {}

type sourceCompatMiddleware struct{}

// Handle accepts the legacy context and continuation types.
func (sourceCompatMiddleware) Handle(ctx context.Context, message bus.Context, next bus.Next) error {
	return next(ctx, message)
}

type sourceCompatChainBuilder struct{}

// OnQueue returns the legacy chain builder interface.
func (builder *sourceCompatChainBuilder) OnQueue(string) bus.ChainBuilder { return builder }

// Catch accepts the legacy chain state callback.
func (builder *sourceCompatChainBuilder) Catch(func(context.Context, bus.ChainState, error) error) bus.ChainBuilder {
	return builder
}

// Finally accepts the legacy terminal chain callback.
func (builder *sourceCompatChainBuilder) Finally(func(context.Context, bus.ChainState) error) bus.ChainBuilder {
	return builder
}

// Dispatch preserves the legacy chain dispatch signature.
func (*sourceCompatChainBuilder) Dispatch(context.Context) (string, error) { return "", nil }

type sourceCompatBatchBuilder struct{}

// Name returns the legacy batch builder interface.
func (builder *sourceCompatBatchBuilder) Name(string) bus.BatchBuilder { return builder }

// OnQueue returns the legacy batch builder interface.
func (builder *sourceCompatBatchBuilder) OnQueue(string) bus.BatchBuilder { return builder }

// AllowFailures returns the legacy batch builder interface.
func (builder *sourceCompatBatchBuilder) AllowFailures() bus.BatchBuilder { return builder }

// Progress accepts the legacy batch progress callback.
func (builder *sourceCompatBatchBuilder) Progress(func(context.Context, bus.BatchState) error) bus.BatchBuilder {
	return builder
}

// Then accepts the legacy successful batch callback.
func (builder *sourceCompatBatchBuilder) Then(func(context.Context, bus.BatchState) error) bus.BatchBuilder {
	return builder
}

// Catch accepts the legacy failed batch callback.
func (builder *sourceCompatBatchBuilder) Catch(func(context.Context, bus.BatchState, error) error) bus.BatchBuilder {
	return builder
}

// Finally accepts the legacy terminal batch callback.
func (builder *sourceCompatBatchBuilder) Finally(func(context.Context, bus.BatchState) error) bus.BatchBuilder {
	return builder
}

// Dispatch preserves the legacy batch dispatch signature.
func (*sourceCompatBatchBuilder) Dispatch(context.Context) (string, error) { return "", nil }

type sourceCompatRootChainBuilder struct{}

// OnQueue returns the root chain builder interface.
func (builder *sourceCompatRootChainBuilder) OnQueue(string) queue.ChainBuilder { return builder }

// Catch accepts the root chain state callback.
func (builder *sourceCompatRootChainBuilder) Catch(func(context.Context, queue.ChainState, error) error) queue.ChainBuilder {
	return builder
}

// Finally accepts the root terminal chain callback.
func (builder *sourceCompatRootChainBuilder) Finally(func(context.Context, queue.ChainState) error) queue.ChainBuilder {
	return builder
}

// Dispatch preserves the root chain dispatch signature.
func (*sourceCompatRootChainBuilder) Dispatch(context.Context) (string, error) { return "", nil }

type sourceCompatRootBatchBuilder struct{}

// Name returns the root batch builder interface.
func (builder *sourceCompatRootBatchBuilder) Name(string) queue.BatchBuilder { return builder }

// OnQueue returns the root batch builder interface.
func (builder *sourceCompatRootBatchBuilder) OnQueue(string) queue.BatchBuilder { return builder }

// AllowFailures returns the root batch builder interface.
func (builder *sourceCompatRootBatchBuilder) AllowFailures() queue.BatchBuilder { return builder }

// Progress accepts the root batch progress callback.
func (builder *sourceCompatRootBatchBuilder) Progress(func(context.Context, queue.BatchState) error) queue.BatchBuilder {
	return builder
}

// Then accepts the root successful batch callback.
func (builder *sourceCompatRootBatchBuilder) Then(func(context.Context, queue.BatchState) error) queue.BatchBuilder {
	return builder
}

// Catch accepts the root failed batch callback.
func (builder *sourceCompatRootBatchBuilder) Catch(func(context.Context, queue.BatchState, error) error) queue.BatchBuilder {
	return builder
}

// Finally accepts the root terminal batch callback.
func (builder *sourceCompatRootBatchBuilder) Finally(func(context.Context, queue.BatchState) error) queue.BatchBuilder {
	return builder
}

// Dispatch preserves the root batch dispatch signature.
func (*sourceCompatRootBatchBuilder) Dispatch(context.Context) (string, error) { return "", nil }

// TestBusV1SourceCompatibility freezes the external source forms retained by the deprecated forwarding facade.
func TestBusV1SourceCompatibility(t *testing.T) {
	fixedTime := time.Unix(1_704_067_200, 123_000_000)
	keyedOptions := bus.JobOptions{
		Queue:     "critical",
		Delay:     time.Second,
		Timeout:   2 * time.Second,
		Retry:     3,
		Backoff:   4 * time.Second,
		UniqueFor: 5 * time.Second,
	}
	unkeyedOptions := bus.JobOptions(sourceCompatJobOptions{"bulk", 6 * time.Second, 7 * time.Second, 8, 9 * time.Second, 10 * time.Second})
	keyedJob := bus.Job{Type: "reports:keyed", Payload: map[string]int{"id": 1}, Options: keyedOptions}
	unkeyedJob := bus.Job{"reports:unkeyed", map[string]int{"id": 2}, unkeyedOptions}
	mutableJob := bus.NewJob("reports:mutable", nil)
	mutableJob.Type = "reports:mutated"
	mutableJob.Payload = []byte("payload")
	mutableJob.Options = keyedOptions
	mutableJob.Options.Queue = "mutated"
	mutableJob.Options.Retry = 11
	if keyedJob.Type != "reports:keyed" || unkeyedJob.Type != "reports:unkeyed" || mutableJob.Type != "reports:mutated" || mutableJob.Options.Queue != "mutated" || mutableJob.Options.Retry != 11 {
		t.Fatalf("legacy job source forms changed: keyed=%+v unkeyed=%+v mutable=%+v", keyedJob, unkeyedJob, mutableJob)
	}
	if unkeyedOptions.Queue != "bulk" || unkeyedOptions.Delay != 6*time.Second || unkeyedOptions.Timeout != 7*time.Second || unkeyedOptions.Retry != 8 || unkeyedOptions.Backoff != 9*time.Second || unkeyedOptions.UniqueFor != 10*time.Second {
		t.Fatalf("legacy unkeyed job option order changed: %+v", unkeyedOptions)
	}
	storedJob := bus.StoredJob{Type: "reports:stored", Payload: []byte(`{"id":3}`), Options: keyedOptions}
	storedNode := bus.ChainNode{NodeID: "stored-node", Job: storedJob}
	var selectedOptions bus.JobOptions = storedNode.Job.Options
	storedNode.Job.Options = selectedOptions
	var rootStoredJob queue.StoredJob = storedNode.Job
	var busStoredJob bus.StoredJob = rootStoredJob
	if busStoredJob.Type != "reports:stored" || busStoredJob.Options.Queue != "critical" {
		t.Fatalf("legacy stored job selectors changed: %+v", busStoredJob)
	}

	keyedResult := bus.DispatchResult{DispatchID: "dispatch-keyed"}
	unkeyedResult := bus.DispatchResult(sourceCompatDispatchResult{"dispatch-unkeyed"})
	if keyedResult.DispatchID != "dispatch-keyed" || unkeyedResult.DispatchID != "dispatch-unkeyed" {
		t.Fatalf("legacy dispatch result source forms changed: keyed=%+v unkeyed=%+v", keyedResult, unkeyedResult)
	}

	keyedChainRecord := bus.ChainRecord{
		ChainID:    "chain-keyed",
		DispatchID: "dispatch-keyed",
		Queue:      "critical",
		Nodes:      nil,
		CreatedAt:  fixedTime,
	}
	unkeyedChainRecord := bus.ChainRecord(sourceCompatChainRecord{"chain-unkeyed", "dispatch-unkeyed", "bulk", nil, fixedTime})
	keyedChainState := bus.ChainState{
		ChainID:    "chain-state-keyed",
		DispatchID: "dispatch-state-keyed",
		Queue:      "critical",
		Nodes:      nil,
		NextIndex:  2,
		Completed:  true,
		Failed:     false,
		Failure:    "",
		CreatedAt:  fixedTime,
		UpdatedAt:  fixedTime.Add(time.Second),
	}
	unkeyedChainState := bus.ChainState(sourceCompatChainState{"chain-state-unkeyed", "dispatch-state-unkeyed", "bulk", nil, 1, false, true, "failed", fixedTime, fixedTime.Add(2 * time.Second)})
	if keyedChainRecord.ChainID != "chain-keyed" || unkeyedChainRecord.Queue != "bulk" || keyedChainState.NextIndex != 2 || !unkeyedChainState.Failed || unkeyedChainState.Failure != "failed" {
		t.Fatalf("legacy chain source forms changed: records=%+v/%+v states=%+v/%+v", keyedChainRecord, unkeyedChainRecord, keyedChainState, unkeyedChainState)
	}

	keyedBatchRecord := bus.BatchRecord{
		BatchID:     "batch-keyed",
		DispatchID:  "batch-dispatch-keyed",
		Name:        "keyed batch",
		Queue:       "critical",
		AllowFailed: true,
		Jobs:        nil,
		CreatedAt:   fixedTime,
	}
	unkeyedBatchRecord := bus.BatchRecord(sourceCompatBatchRecord{"batch-unkeyed", "batch-dispatch-unkeyed", "unkeyed batch", "bulk", false, nil, fixedTime})
	keyedBatchState := bus.BatchState{
		BatchID:     "batch-state-keyed",
		DispatchID:  "batch-state-dispatch-keyed",
		Name:        "keyed state",
		Queue:       "critical",
		AllowFailed: true,
		Total:       4,
		Pending:     1,
		Processed:   3,
		Failed:      1,
		Cancelled:   false,
		Completed:   true,
		CreatedAt:   fixedTime,
		UpdatedAt:   fixedTime.Add(time.Second),
	}
	unkeyedBatchState := bus.BatchState(sourceCompatBatchState{"batch-state-unkeyed", "batch-state-dispatch-unkeyed", "unkeyed state", "bulk", false, 5, 2, 3, 1, true, true, fixedTime, fixedTime.Add(2 * time.Second)})
	if keyedBatchRecord.BatchID != "batch-keyed" || unkeyedBatchRecord.Name != "unkeyed batch" || keyedBatchState.Processed != 3 || !unkeyedBatchState.Cancelled || unkeyedBatchState.Total != 5 {
		t.Fatalf("legacy batch source forms changed: records=%+v/%+v states=%+v/%+v", keyedBatchRecord, unkeyedBatchRecord, keyedBatchState, unkeyedBatchState)
	}

	keyedSQLConfig := bus.SQLStoreConfig{DB: (*sql.DB)(nil), DriverName: "sqlite", DSN: "file:keyed", AutoMigrate: true}
	unkeyedSQLConfig := bus.SQLStoreConfig(sourceCompatSQLStoreConfig{nil, "sqlite", "file:unkeyed", true})
	if keyedSQLConfig.DriverName != "sqlite" || keyedSQLConfig.DSN != "file:keyed" || !keyedSQLConfig.AutoMigrate || unkeyedSQLConfig.DSN != "file:unkeyed" || !unkeyedSQLConfig.AutoMigrate {
		t.Fatalf("legacy SQL store config source forms changed: keyed=%+v unkeyed=%+v", keyedSQLConfig, unkeyedSQLConfig)
	}

	options := []bus.Option{
		nil,
		bus.WithObserver(sourceCompatObserver{}),
		bus.WithStore(&sourceCompatStore{}),
		bus.WithClock(func() time.Time { return fixedTime }),
		bus.WithMiddleware(sourceCompatMiddleware{}),
	}
	if len(options) != 5 || options[0] != nil || options[1] == nil || options[2] == nil || options[3] == nil || options[4] == nil {
		t.Fatalf("legacy option slice changed: %+v", options)
	}

	fake := bus.NewFake()
	var concreteFake *bus.Fake = fake
	if _, err := concreteFake.Dispatch(context.Background(), keyedJob); err != nil {
		t.Fatalf("dispatch through legacy fake: %v", err)
	}
	if _, err := concreteFake.Chain(keyedJob, unkeyedJob).Dispatch(context.Background()); err != nil {
		t.Fatalf("chain through legacy fake: %v", err)
	}
	if _, err := concreteFake.Batch(keyedJob, unkeyedJob).Dispatch(context.Background()); err != nil {
		t.Fatalf("batch through legacy fake: %v", err)
	}
	keyedSpec := bus.BatchSpec{JobTypes: []string{"reports:keyed", "reports:unkeyed"}}
	unkeyedSpec := bus.BatchSpec{[]string{"reports:keyed", "reports:unkeyed"}}
	if len(keyedSpec.JobTypes) != 2 || len(unkeyedSpec.JobTypes) != 2 {
		t.Fatalf("legacy batch spec source forms changed: keyed=%+v unkeyed=%+v", keyedSpec, unkeyedSpec)
	}
	concreteFake.AssertDispatched(t, "reports:keyed")
	concreteFake.AssertChained(t, keyedSpec.JobTypes)
	concreteFake.AssertBatched(t, func(spec bus.BatchSpec) bool {
		return len(spec.JobTypes) == 2 && spec.JobTypes[0] == "reports:keyed" && spec.JobTypes[1] == "reports:unkeyed"
	})

	temporalAdapter, err := temporal.New(temporal.Config{})
	if err != nil {
		t.Fatalf("construct temporal compatibility adapter: %v", err)
	}
	var temporalBus bus.Bus = temporalAdapter
	if temporalBus == nil {
		t.Fatal("temporal adapter no longer satisfies bus.Bus")
	}

	_ = []any{
		sourceCompatBusContextFromRoot,
		sourceCompatRootMessageFromBus,
		sourceCompatBusResultFromRoot,
		sourceCompatRootResultFromBus,
		sourceCompatBusOptionsFromRoot,
		sourceCompatRootOptionsFromBus,
		sourceCompatBusChainStateFromRoot,
		sourceCompatRootChainStateFromBus,
		sourceCompatBusBatchStateFromRoot,
		sourceCompatRootBatchStateFromBus,
		sourceCompatRootMiddlewareRoundTrip,
		sourceCompatRootStoreRoundTrip,
		sourceCompatBusMiddlewareFunc,
	}
}

// TestBuilderInterfacesRemainSourceDistinct pins the legacy self-returning method sets.
func TestBuilderInterfacesRemainSourceDistinct(t *testing.T) {
	var legacyChain bus.ChainBuilder = &sourceCompatChainBuilder{}
	var rootChain queue.ChainBuilder = &sourceCompatRootChainBuilder{}
	if _, ok := any(legacyChain).(queue.ChainBuilder); ok {
		t.Fatal("legacy chain builder unexpectedly satisfies the root self-returning contract")
	}
	if _, ok := any(rootChain).(bus.ChainBuilder); ok {
		t.Fatal("root chain builder unexpectedly satisfies the legacy self-returning contract")
	}

	var legacyBatch bus.BatchBuilder = &sourceCompatBatchBuilder{}
	var rootBatch queue.BatchBuilder = &sourceCompatRootBatchBuilder{}
	if _, ok := any(legacyBatch).(queue.BatchBuilder); ok {
		t.Fatal("legacy batch builder unexpectedly satisfies the root self-returning contract")
	}
	if _, ok := any(rootBatch).(bus.BatchBuilder); ok {
		t.Fatal("root batch builder unexpectedly satisfies the legacy self-returning contract")
	}
}
