package queue

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/goforj/queue/busruntime"
)

// fakeWorkflowFailingPayload exercises deferred Job build failures.
type fakeWorkflowFailingPayload struct{}

// MarshalJSON forces deferred payload validation to fail at the canonical boundary.
func (fakeWorkflowFailingPayload) MarshalJSON() ([]byte, error) {
	return nil, errors.New("fake payload failure")
}

// fakeWorkflowBlockingContext pauses the initial fake delivery after workflow
// state creation so Reset can contend with the complete dispatch operation.
type fakeWorkflowBlockingContext struct {
	context.Context
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

// Err exposes cancellation only after the test releases the initial delivery.
func (c *fakeWorkflowBlockingContext) Err() error {
	c.once.Do(func() { close(c.entered) })
	<-c.release
	return context.Canceled
}

// fakeWorkflowCancelAfterContext rejects delivery only after the configured
// number of batch members have passed their acceptance check.
type fakeWorkflowCancelAfterContext struct {
	context.Context
	accepted int
	mu       sync.Mutex
	checks   int
}

// Err lets the first accepted checks proceed before exposing cancellation.
func (c *fakeWorkflowCancelAfterContext) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.checks++
	if c.checks > c.accepted {
		return context.Canceled
	}
	return nil
}

// fakeWorkflowDispatchResult carries the blocked builder result across the test goroutine.
type fakeWorkflowDispatchResult struct {
	id  string
	err error
}

// TestFakeQueueWorkflowRecordsUseCanonicalEngine verifies fake assertions expose
// the same stored options, queue precedence, identifiers, and payload encoding as production.
func TestFakeQueueWorkflowRecordsUseCanonicalEngine(t *testing.T) {
	fake := NewFake()
	chainBuilder := fake.Chain(
		NewJob("reports:build").Payload(nil).Delay(time.Second).Timeout(2*time.Second).Retry(0).Backoff(3*time.Second).UniqueFor(4*time.Second),
		NewJob("reports:publish").Payload(json.RawMessage(`{"id":2}`)).OnQueue("dedicated"),
	).OnQueue("workflow")
	batchBuilder := fake.Batch(
		NewJob("emails:first").Payload(map[string]int{"id": 1}),
		NewJob("emails:second").Payload(map[string]int{"id": 2}).OnQueue("priority"),
	).Name("nightly").OnQueue("bulk").AllowFailures()

	if got := len(fake.ChainRecords()); got != 0 {
		t.Fatalf("chain records before Dispatch = %d, want 0", got)
	}
	if got := len(fake.BatchRecords()); got != 0 {
		t.Fatalf("batch records before Dispatch = %d, want 0", got)
	}

	chainID, err := chainBuilder.Dispatch(context.Background())
	if err != nil {
		t.Fatalf("dispatch chain: %v", err)
	}
	batchID, err := batchBuilder.Dispatch(context.Background())
	if err != nil {
		t.Fatalf("dispatch batch: %v", err)
	}
	if chainID == "" || batchID == "" || chainID == batchID {
		t.Fatalf("workflow IDs = %q/%q, want distinct nonempty values", chainID, batchID)
	}
	if got := len(fake.Records()); got != 0 {
		t.Fatalf("physical workflow envelopes leaked into direct records: %d", got)
	}

	chains := fake.ChainRecords()
	if len(chains) != 1 {
		t.Fatalf("chain records = %d, want 1", len(chains))
	}
	chain := chains[0]
	if chain.ChainID != chainID || chain.DispatchID == "" || chain.Queue != "workflow" {
		t.Fatalf("chain identity = %+v", chain)
	}
	if len(chain.Nodes) != 2 {
		t.Fatalf("chain nodes = %d, want 2", len(chain.Nodes))
	}
	if got := string(chain.Nodes[0].Job.Payload); got != "null" {
		t.Fatalf("nil workflow payload = %q, want null", got)
	}
	if chain.Nodes[0].Job.Options.Queue != "workflow" || chain.Nodes[0].Job.Options.Retry != 0 {
		t.Fatalf("defaulted first node = %+v", chain.Nodes[0].Job)
	}
	if options := chain.Nodes[0].Job.Options; options.Delay != time.Second || options.Timeout != 2*time.Second || options.Backoff != 3*time.Second || options.UniqueFor != 4*time.Second {
		t.Fatalf("first node delivery policy = %+v", options)
	}
	if chain.Nodes[1].Job.Options.Queue != "dedicated" || string(chain.Nodes[1].Job.Payload) != `{"id":2}` {
		t.Fatalf("explicit second node = %+v", chain.Nodes[1].Job)
	}

	batches := fake.BatchRecords()
	if len(batches) != 1 {
		t.Fatalf("batch records = %d, want 1", len(batches))
	}
	batch := batches[0]
	if batch.BatchID != batchID || batch.DispatchID == "" || batch.Name != "nightly" || batch.Queue != "bulk" || !batch.AllowFailed {
		t.Fatalf("batch identity/options = %+v", batch)
	}
	if len(batch.Jobs) != 2 || batch.Jobs[0].Job.Options.Queue != "bulk" || batch.Jobs[1].Job.Options.Queue != "priority" {
		t.Fatalf("batch queue precedence = %+v", batch.Jobs)
	}

	chainState, err := fake.FindChain(context.Background(), chainID)
	if err != nil || chainState.DispatchID != chain.DispatchID || len(chainState.Nodes) != 2 {
		t.Fatalf("find chain = %+v, %v", chainState, err)
	}
	batchState, err := fake.FindBatch(context.Background(), batchID)
	if err != nil || batchState.DispatchID != batch.DispatchID || batchState.Total != 2 || batchState.Pending != 2 {
		t.Fatalf("find batch = %+v, %v", batchState, err)
	}
	fake.AssertChained(t, []string{"reports:build", "reports:publish"})
	fake.AssertBatchCount(t, 1)
	fake.AssertBatched(t, func(record BatchRecord) bool {
		fake.Reset()
		return record.Name == "nightly" && record.AllowFailed
	})
}

// TestFakeQueueWorkflowRecordsOnlyAcceptedDispatches verifies abandoned and
// rejected builders cannot satisfy chain or batch assertions.
func TestFakeQueueWorkflowRecordsOnlyAcceptedDispatches(t *testing.T) {
	fake := NewFake()
	_ = fake.Chain(NewJob("abandoned:chain"))
	_ = fake.Batch(NewJob("abandoned:batch"))

	tests := []struct {
		name     string
		dispatch func(context.Context) (string, error)
	}{
		{name: "empty chain", dispatch: fake.Chain().Dispatch},
		{name: "empty batch", dispatch: fake.Batch().Dispatch},
		{name: "invalid chain option", dispatch: fake.Chain(NewJob("bad:chain").Retry(-1)).Dispatch},
		{name: "invalid batch option", dispatch: fake.Batch(NewJob("bad:batch").Timeout(-1)).Dispatch},
		{name: "malformed chain payload", dispatch: fake.Chain(NewJob("bad:chain-json").Payload(json.RawMessage(`{`))).Dispatch},
		{name: "malformed batch payload", dispatch: fake.Batch(NewJob("bad:batch-json").Payload(json.RawMessage(`{`))).Dispatch},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := test.dispatch(context.Background()); err == nil {
				t.Fatal("Dispatch error = nil, want validation failure")
			}
		})
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	rejectedChainID, err := fake.Chain(NewJob("cancelled:chain")).Dispatch(canceled)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled chain error = %v, want context.Canceled", err)
	}
	rejectedBatchID, err := fake.Batch(NewJob("cancelled:batch")).Dispatch(canceled)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled batch error = %v, want context.Canceled", err)
	}
	if _, err := fake.FindChain(context.Background(), rejectedChainID); !errors.Is(err, ErrWorkflowNotFound) {
		t.Fatalf("rejected chain state error = %v, want ErrWorkflowNotFound", err)
	}
	if _, err := fake.FindBatch(context.Background(), rejectedBatchID); !errors.Is(err, ErrWorkflowNotFound) {
		t.Fatalf("rejected batch state error = %v, want ErrWorkflowNotFound", err)
	}

	if got := len(fake.ChainRecords()); got != 0 {
		t.Fatalf("rejected chain records = %d, want 0", got)
	}
	if got := len(fake.BatchRecords()); got != 0 {
		t.Fatalf("rejected batch records = %d, want 0", got)
	}
}

// TestFakeQueueBatchRejectsPartialInitialFanout verifies accepting an earlier
// member cannot publish a batch when a later initial delivery is canceled.
func TestFakeQueueBatchRejectsPartialInitialFanout(t *testing.T) {
	fake := NewFake()
	ctx := &fakeWorkflowCancelAfterContext{
		Context:  context.Background(),
		accepted: 1,
	}
	batchID, err := fake.Batch(
		NewJob("batch:first"),
		NewJob("batch:second"),
	).Dispatch(ctx)
	if !errors.Is(err, context.Canceled) || busruntime.IsUncommitted(err) {
		t.Fatalf("partial batch error = %v, want committed context cancellation", err)
	}
	if batchID == "" {
		t.Fatal("partial batch returned an empty lookup ID")
	}
	if got := len(fake.BatchRecords()); got != 0 {
		t.Fatalf("partial batch records = %d, want 0", got)
	}
	if got := len(fake.Records()); got != 0 {
		t.Fatalf("partial batch leaked %d protocol deliveries", got)
	}
	if _, err := fake.FindBatch(context.Background(), batchID); !errors.Is(err, ErrWorkflowNotFound) {
		t.Fatalf("partial batch lookup error = %v, want ErrWorkflowNotFound", err)
	}
}

// TestFakeQueueWorkflowBuilderRedispatchRecordsEachAcceptance verifies reusable
// builders retain production behavior instead of recording only construction.
func TestFakeQueueWorkflowBuilderRedispatchRecordsEachAcceptance(t *testing.T) {
	fake := NewFake()
	chain := fake.Chain(NewJob("chain:repeat"))
	firstChainID, err := chain.Dispatch(context.Background())
	if err != nil {
		t.Fatalf("dispatch first chain: %v", err)
	}
	secondChainID, err := chain.Dispatch(context.Background())
	if err != nil {
		t.Fatalf("dispatch second chain: %v", err)
	}
	batch := fake.Batch(NewJob("batch:repeat"))
	firstBatchID, err := batch.Dispatch(context.Background())
	if err != nil {
		t.Fatalf("dispatch first batch: %v", err)
	}
	secondBatchID, err := batch.Dispatch(context.Background())
	if err != nil {
		t.Fatalf("dispatch second batch: %v", err)
	}
	if firstChainID == secondChainID || firstBatchID == secondBatchID {
		t.Fatalf("reused builder IDs collided: chains=%q/%q batches=%q/%q", firstChainID, secondChainID, firstBatchID, secondBatchID)
	}
	if len(fake.ChainRecords()) != 2 || len(fake.BatchRecords()) != 2 {
		t.Fatalf("reused builder records = chains:%d batches:%d", len(fake.ChainRecords()), len(fake.BatchRecords()))
	}
}

// TestFakeQueueDispatchValidationMetadataAndIsolation verifies direct records
// reject deferred errors, preserve direct correlation, and own mutable options.
func TestFakeQueueDispatchValidationMetadataAndIsolation(t *testing.T) {
	fake := NewFake()
	if err := fake.Dispatch(NewJob("invalid:retry").Retry(-1)); err == nil {
		t.Fatal("negative retry dispatch error = nil")
	}
	if err := fake.Dispatch(NewJob("invalid:payload").Payload(fakeWorkflowFailingPayload{})); err == nil {
		t.Fatal("failing payload dispatch error = nil")
	}
	fake.AssertNothingDispatched(t)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := fake.WithContext(canceled).Dispatch(NewJob("invalid:before-context").Retry(-1)); err == nil || errors.Is(err, context.Canceled) {
		t.Fatalf("invalid canceled dispatch error = %v, want validation precedence", err)
	}

	job := NewJob("reports:build").Payload([]byte(`{"id":1}`)).Timeout(time.Second).Retry(2).Backoff(time.Millisecond)
	if err := fake.Dispatch(job); err != nil {
		t.Fatalf("dispatch job: %v", err)
	}
	originalOptions := DriverOptions(job)
	*originalOptions.Timeout = 9 * time.Second
	*originalOptions.MaxRetry = 9
	*originalOptions.Backoff = 9 * time.Second
	recordedOptions := DriverOptions(fake.Records()[0].Job)
	if *recordedOptions.Timeout != time.Second || *recordedOptions.MaxRetry != 2 || *recordedOptions.Backoff != time.Millisecond {
		t.Fatalf("input mutation changed record: %+v", recordedOptions)
	}
	*recordedOptions.Timeout = 7 * time.Second
	if got := *DriverOptions(fake.Records()[0].Job).Timeout; got != time.Second {
		t.Fatalf("returned record mutation changed stored timeout: %v", got)
	}

	metadata := busruntime.DeliveryMetadata{
		SchemaVersion: busruntime.DeliveryMetadataVersion,
		DispatchID:    "dsp_fake",
		JobID:         "job_fake",
		ChainID:       "chn_fake",
		BatchID:       "bat_fake",
		Queue:         "critical",
	}
	payload := []byte{0x00, 0xff, 0x01}
	if err := fake.BusDispatchDirect(context.Background(), "binary:job", payload, metadata, busruntime.JobOptions{Queue: "critical", Retry: 0}); err != nil {
		t.Fatalf("direct bus dispatch: %v", err)
	}
	payload[0] = 0x7f
	direct := fake.Records()[1].Job
	if got := direct.PayloadBytes(); len(got) != 3 || got[0] != 0x00 || got[1] != 0xff {
		t.Fatalf("direct payload = %v", got)
	}
	if got := DriverMetadata(direct); got != metadata {
		t.Fatalf("direct metadata = %+v, want %+v", got, metadata)
	}
	if retry := DriverOptions(direct).MaxRetry; retry == nil || *retry != 0 {
		t.Fatalf("direct explicit retry = %v, want pointer to zero", retry)
	}
}

// TestFakeQueueWorkflowSnapshotsAndReset verifies returned nested records are
// isolated and Reset clears every canonical projection and lookup.
func TestFakeQueueWorkflowSnapshotsAndReset(t *testing.T) {
	fake := NewFake()
	if err := fake.Dispatch(NewJob("direct:job")); err != nil {
		t.Fatalf("dispatch direct job: %v", err)
	}
	chainID, err := fake.Chain(NewJob("chain:job").Payload(json.RawMessage(`{"value":1}`))).Dispatch(context.Background())
	if err != nil {
		t.Fatalf("dispatch chain: %v", err)
	}
	batchID, err := fake.Batch(NewJob("batch:job").Payload(json.RawMessage(`{"value":2}`))).Dispatch(context.Background())
	if err != nil {
		t.Fatalf("dispatch batch: %v", err)
	}

	chains := fake.ChainRecords()
	batches := fake.BatchRecords()
	chains[0].Nodes[0].Job.Payload[0] = 'x'
	batches[0].Jobs[0].Job.Payload[0] = 'x'
	if got := string(fake.ChainRecords()[0].Nodes[0].Job.Payload); got != `{"value":1}` {
		t.Fatalf("mutated chain snapshot changed stored payload: %q", got)
	}
	if got := string(fake.BatchRecords()[0].Jobs[0].Job.Payload); got != `{"value":2}` {
		t.Fatalf("mutated batch snapshot changed stored payload: %q", got)
	}
	chainState, err := fake.FindChain(context.Background(), chainID)
	if err != nil {
		t.Fatalf("find chain for isolation: %v", err)
	}
	chainState.Nodes[0].Job.Payload[0] = 'x'
	chainStateAgain, err := fake.FindChain(context.Background(), chainID)
	if err != nil || string(chainStateAgain.Nodes[0].Job.Payload) != `{"value":1}` {
		t.Fatalf("mutated lookup changed stored chain: %+v, %v", chainStateAgain, err)
	}

	fake.Reset()
	if len(fake.Records()) != 0 || len(fake.ChainRecords()) != 0 || len(fake.BatchRecords()) != 0 {
		t.Fatalf("Reset retained records: direct=%d chain=%d batch=%d", len(fake.Records()), len(fake.ChainRecords()), len(fake.BatchRecords()))
	}
	if _, err := fake.FindChain(context.Background(), chainID); !errors.Is(err, ErrWorkflowNotFound) {
		t.Fatalf("FindChain after Reset error = %v, want ErrWorkflowNotFound", err)
	}
	if _, err := fake.FindBatch(context.Background(), batchID); !errors.Is(err, ErrWorkflowNotFound) {
		t.Fatalf("FindBatch after Reset error = %v, want ErrWorkflowNotFound", err)
	}
}

// TestFakeQueueResetWaitsForWorkflowDispatch verifies Reset cannot replace the
// store between engine creation and rejection cleanup for chains or batches.
func TestFakeQueueResetWaitsForWorkflowDispatch(t *testing.T) {
	tests := []struct {
		name     string
		dispatch func(*FakeQueue, context.Context) (string, error)
		find     func(*FakeQueue, string) error
	}{
		{
			name: "chain",
			dispatch: func(fake *FakeQueue, ctx context.Context) (string, error) {
				return fake.Chain(NewJob("chain:blocked")).Dispatch(ctx)
			},
			find: func(fake *FakeQueue, workflowID string) error {
				_, err := fake.FindChain(context.Background(), workflowID)
				return err
			},
		},
		{
			name: "batch",
			dispatch: func(fake *FakeQueue, ctx context.Context) (string, error) {
				return fake.Batch(NewJob("batch:blocked")).Dispatch(ctx)
			},
			find: func(fake *FakeQueue, workflowID string) error {
				_, err := fake.FindBatch(context.Background(), workflowID)
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fake := NewFake()
			blocked := &fakeWorkflowBlockingContext{
				Context: context.Background(),
				entered: make(chan struct{}),
				release: make(chan struct{}),
			}
			dispatchDone := make(chan fakeWorkflowDispatchResult, 1)
			go func() {
				workflowID, err := test.dispatch(fake, blocked)
				dispatchDone <- fakeWorkflowDispatchResult{id: workflowID, err: err}
			}()

			select {
			case <-blocked.entered:
			case <-time.After(2 * time.Second):
				t.Fatal("initial fake delivery did not block")
			}
			if fake.state.workflowOps.TryLock() {
				fake.state.workflowOps.Unlock()
				close(blocked.release)
				<-dispatchDone
				t.Fatal("workflow dispatch did not hold the Reset operation guard")
			}

			resetStarted := make(chan struct{})
			resetDone := make(chan struct{})
			go func() {
				close(resetStarted)
				fake.Reset()
				close(resetDone)
			}()
			<-resetStarted
			close(blocked.release)

			var result fakeWorkflowDispatchResult
			select {
			case result = <-dispatchDone:
			case <-time.After(2 * time.Second):
				t.Fatal("blocked fake dispatch did not return")
			}
			if !errors.Is(result.err, context.Canceled) {
				t.Fatalf("dispatch error = %v, want context.Canceled", result.err)
			}
			select {
			case <-resetDone:
			case <-time.After(2 * time.Second):
				t.Fatal("Reset did not finish after workflow dispatch returned")
			}
			if err := test.find(fake, result.id); !errors.Is(err, ErrWorkflowNotFound) {
				t.Fatalf("workflow lookup after Reset error = %v, want ErrWorkflowNotFound", err)
			}
		})
	}
}

// TestFakeQueueConcurrentViews exercises shared recording, workflow dispatch,
// snapshots, and reset under the race detector.
func TestFakeQueueConcurrentViews(t *testing.T) {
	fake := NewFake()
	var wait sync.WaitGroup
	errorsSeen := make(chan error, 12*40)
	for worker := 0; worker < 12; worker++ {
		worker := worker
		wait.Add(1)
		go func() {
			defer wait.Done()
			for iteration := 0; iteration < 40; iteration++ {
				switch worker % 5 {
				case 0:
					errorsSeen <- fake.WithContext(context.Background()).Dispatch(NewJob("direct:concurrent"))
				case 1:
					chainID, err := fake.Chain(NewJob("chain:concurrent")).Dispatch(context.Background())
					errorsSeen <- err
					_, _ = fake.FindChain(context.Background(), chainID)
				case 2:
					batchID, err := fake.Batch(NewJob("batch:concurrent")).Dispatch(context.Background())
					errorsSeen <- err
					_, _ = fake.FindBatch(context.Background(), batchID)
				case 3:
					canceled, cancel := context.WithCancel(context.Background())
					cancel()
					_, err := fake.Chain(NewJob("chain:rejected-concurrent")).Dispatch(canceled)
					if errors.Is(err, context.Canceled) && !busruntime.IsUncommitted(err) {
						err = nil
					}
					errorsSeen <- err
				case 4:
					canceled, cancel := context.WithCancel(context.Background())
					cancel()
					_, err := fake.Batch(NewJob("batch:rejected-concurrent")).Dispatch(canceled)
					if errors.Is(err, context.Canceled) && !busruntime.IsUncommitted(err) {
						err = nil
					}
					errorsSeen <- err
				}
				_ = fake.Records()
				_ = fake.ChainRecords()
				_ = fake.BatchRecords()
				_ = fake.Prune(context.Background(), time.Now())
				if iteration%19 == 0 {
					fake.Reset()
				}
			}
		}()
	}
	wait.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Errorf("concurrent fake dispatch: %v", err)
		}
	}
}
