package workflow

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func testStoreFactories(t *testing.T) map[string]func(t *testing.T) Store {
	t.Helper()
	return map[string]func(t *testing.T) Store{
		"memory": func(t *testing.T) Store {
			t.Helper()
			return NewMemoryStore()
		},
		"sql_sqlite": func(t *testing.T) Store {
			t.Helper()
			dsn := filepath.Join(t.TempDir(), "store-contract.db") + "?_pragma=busy_timeout%3d5000"
			store, err := NewSQLStore(SQLStoreConfig{
				DriverName: "sqlite",
				DSN:        dsn,
			})
			if err != nil {
				t.Fatalf("new sql store: %v", err)
			}
			t.Cleanup(func() { _ = store.(*sqlStore).db.Close() })
			return store
		},
	}
}

// waitStoreContractOperations bounds lock-sensitive probes so a regression is
// reported by the focused contract rather than the package-wide test timeout.
func waitStoreContractOperations(t *testing.T, wg *sync.WaitGroup) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for concurrent store operations")
	}
}

// requireOutcomeStore keeps the compatibility Store contract unchanged while
// asserting that every built-in implementation provides stronger arbitration.
func requireOutcomeStore(t *testing.T, store Store) outcomeStore {
	t.Helper()
	outcomes, ok := store.(outcomeStore)
	if !ok {
		t.Fatalf("built-in store %T does not implement outcomeStore", store)
	}
	return outcomes
}

// TestStoreContract_RejectsAmbiguousChainRecords protects the immutable order
// required by atomic per-node success and failure compare-and-swap operations.
func TestStoreContract_RejectsAmbiguousChainRecords(t *testing.T) {
	for name, factory := range testStoreFactories(t) {
		t.Run(name, func(t *testing.T) {
			store := factory(t)
			ctx := context.Background()
			for _, test := range []struct {
				name   string
				record ChainRecord
			}{
				{name: "empty chain id", record: ChainRecord{Nodes: []ChainNode{{NodeID: "node-0"}}}},
				{name: "no nodes", record: ChainRecord{ChainID: "chain-no-nodes"}},
				{name: "empty node id", record: ChainRecord{ChainID: "chain-empty-node", Nodes: []ChainNode{{}}}},
				{name: "duplicate node id", record: ChainRecord{ChainID: "chain-duplicate-node", Nodes: []ChainNode{{NodeID: "node-shared"}, {NodeID: "node-shared"}}}},
			} {
				t.Run(test.name, func(t *testing.T) {
					if err := store.CreateChain(ctx, test.record); err == nil {
						t.Fatal("ambiguous chain record was accepted")
					}
					if _, err := store.GetChain(ctx, test.record.ChainID); !errors.Is(err, ErrNotFound) {
						t.Fatalf("invalid chain persisted: %v", err)
					}
				})
			}
		})
	}
}

// TestStoreContract_RejectsAmbiguousBatchRecords protects the stable member
// identity required by first-writer outcome arbitration.
func TestStoreContract_RejectsAmbiguousBatchRecords(t *testing.T) {
	for name, factory := range testStoreFactories(t) {
		t.Run(name, func(t *testing.T) {
			store := factory(t)
			ctx := context.Background()
			for _, test := range []struct {
				name   string
				record BatchRecord
			}{
				{name: "empty batch id", record: BatchRecord{Jobs: []BatchJob{{JobID: "job-0"}}}},
				{name: "no jobs", record: BatchRecord{BatchID: "batch-no-jobs"}},
				{name: "empty job id", record: BatchRecord{BatchID: "batch-empty-job", Jobs: []BatchJob{{}}}},
				{name: "duplicate job id", record: BatchRecord{BatchID: "batch-duplicate-job", Jobs: []BatchJob{{JobID: "job-shared"}, {JobID: "job-shared"}}}},
			} {
				t.Run(test.name, func(t *testing.T) {
					if err := store.CreateBatch(ctx, test.record); err == nil {
						t.Fatal("ambiguous batch record was accepted")
					}
					if test.record.BatchID == "" {
						return
					}
					if _, err := store.GetBatch(ctx, test.record.BatchID); !errors.Is(err, ErrNotFound) {
						t.Fatalf("invalid batch persisted: %v", err)
					}
				})
			}
		})
	}
}

// TestStoreContract_BatchStartRejectsUnknownMember prevents a malformed
// delivery from creating a synthetic member before outcome settlement.
func TestStoreContract_BatchStartRejectsUnknownMember(t *testing.T) {
	for name, factory := range testStoreFactories(t) {
		t.Run(name, func(t *testing.T) {
			store := factory(t)
			ctx := context.Background()
			const batchID = "batch-start-membership"
			if err := store.CreateBatch(ctx, BatchRecord{BatchID: batchID, Jobs: []BatchJob{{JobID: "job-known"}}}); err != nil {
				t.Fatalf("create batch: %v", err)
			}
			before, err := store.GetBatch(ctx, batchID)
			if err != nil {
				t.Fatalf("get batch before unknown start: %v", err)
			}
			if err := store.MarkBatchJobStarted(ctx, batchID, "job-missing"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("unknown member start error = %v, want ErrNotFound", err)
			}
			after, err := store.GetBatch(ctx, batchID)
			if err != nil {
				t.Fatalf("get batch after unknown start: %v", err)
			}
			if after.Pending != before.Pending || after.Processed != before.Processed || after.Failed != before.Failed || after.Completed != before.Completed || !after.UpdatedAt.Equal(before.UpdatedAt) {
				t.Fatalf("unknown member start changed batch: before=%+v after=%+v", before, after)
			}
			if err := store.MarkBatchJobStarted(ctx, batchID, "job-known"); err != nil {
				t.Fatalf("start known member: %v", err)
			}
			if err := store.MarkBatchJobStarted(ctx, batchID, "job-known"); err != nil {
				t.Fatalf("replay known member start: %v", err)
			}
		})
	}
}

// TestStoreContract_ChainRecordOwnership prevents callers from changing the
// node identity or payload that outcome arbitration treats as immutable.
func TestStoreContract_ChainRecordOwnership(t *testing.T) {
	for name, factory := range testStoreFactories(t) {
		t.Run(name, func(t *testing.T) {
			store := factory(t)
			ctx := context.Background()
			record := ChainRecord{
				ChainID: "chain-record-ownership",
				Nodes: []ChainNode{
					{NodeID: "node-owned", Job: StoredJob{Payload: []byte("owned")}},
					{NodeID: "node-successor", Job: StoredJob{Payload: []byte("successor")}},
				},
			}
			if err := store.CreateChain(ctx, record); err != nil {
				t.Fatalf("create chain: %v", err)
			}
			record.Nodes[0].NodeID = "node-mutated"
			record.Nodes[0].Job.Payload[0] = '!'
			state, err := store.GetChain(ctx, record.ChainID)
			if err != nil {
				t.Fatalf("get chain: %v", err)
			}
			if state.Nodes[0].NodeID != "node-owned" || string(state.Nodes[0].Job.Payload) != "owned" {
				t.Fatalf("creation record aliases state: %+v", state.Nodes[0])
			}
			state.Nodes[0].NodeID = "node-return-mutated"
			state.Nodes[0].Job.Payload[0] = '?'
			state, err = store.GetChain(ctx, record.ChainID)
			if err != nil {
				t.Fatalf("get chain again: %v", err)
			}
			if state.Nodes[0].NodeID != "node-owned" || string(state.Nodes[0].Job.Payload) != "owned" {
				t.Fatalf("returned state aliases store: %+v", state.Nodes[0])
			}
			next, done, err := store.AdvanceChain(ctx, record.ChainID, "node-owned")
			if err != nil || done || next == nil {
				t.Fatalf("advance to successor = next:%+v done:%t err:%v", next, done, err)
			}
			next.NodeID = "node-successor-mutated"
			next.Job.Payload[0] = '!'
			state, err = store.GetChain(ctx, record.ChainID)
			if err != nil {
				t.Fatalf("get chain after successor mutation: %v", err)
			}
			if state.Nodes[1].NodeID != "node-successor" || string(state.Nodes[1].Job.Payload) != "successor" {
				t.Fatalf("returned successor aliases store: %+v", state.Nodes[1])
			}
		})
	}
}

// TestChainNodeDispositionsRejectInvalidPersistedIndex covers corrupt state
// that no valid creation or transition path can produce intentionally.
func TestChainNodeDispositionsRejectInvalidPersistedIndex(t *testing.T) {
	for _, nextIndex := range []int{-1, 1} {
		state := ChainState{ChainID: "chain-invalid-index", Nodes: []ChainNode{{NodeID: "node-0"}}, NextIndex: nextIndex}
		if _, _, _, err := chainNodeAdvanceDisposition(state, "node-0"); err == nil {
			t.Fatalf("advance accepted next index %d", nextIndex)
		}
		if _, _, err := chainNodeFailureDisposition(state, "node-0"); err == nil {
			t.Fatalf("failure accepted next index %d", nextIndex)
		}
	}
}

// TestStoreContract_ChainNodeOutcomeOwnership proves a physical redelivery
// cannot replace the first result committed for a sequential node.
func TestStoreContract_ChainNodeOutcomeOwnership(t *testing.T) {
	for name, factory := range testStoreFactories(t) {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
			defer cancel()

			t.Run("success first", func(t *testing.T) {
				store := factory(t)
				outcomes := requireOutcomeStore(t, store)
				const chainID = "chain-success-first"
				if err := store.CreateChain(ctx, ChainRecord{ChainID: chainID, Nodes: []ChainNode{{NodeID: "node-0"}, {NodeID: "node-1"}}}); err != nil {
					t.Fatalf("create chain: %v", err)
				}
				if _, done, err := store.AdvanceChain(ctx, chainID, "node-0"); err != nil || done {
					t.Fatalf("advance first node = done:%t err:%v", done, err)
				}
				state, owned, err := outcomes.FailChainNode(ctx, chainID, "node-0", errors.New("late failure"))
				if err != nil || owned {
					t.Fatalf("late failure = owned:%t err:%v", owned, err)
				}
				if state.NextIndex != 1 || state.Completed || state.Failed {
					t.Fatalf("success-first state = %+v", state)
				}
			})

			t.Run("failure first and replay", func(t *testing.T) {
				store := factory(t)
				outcomes := requireOutcomeStore(t, store)
				const chainID = "chain-failure-first"
				if err := store.CreateChain(ctx, ChainRecord{ChainID: chainID, Nodes: []ChainNode{{NodeID: "node-0"}, {NodeID: "node-1"}}}); err != nil {
					t.Fatalf("create chain: %v", err)
				}
				firstCause := errors.New("first failure")
				state, owned, err := outcomes.FailChainNode(ctx, chainID, "node-0", firstCause)
				if err != nil || !owned || !state.Failed || state.NextIndex != 0 {
					t.Fatalf("first failure = owned:%t state:%+v err:%v", owned, state, err)
				}
				state, owned, err = outcomes.FailChainNode(ctx, chainID, "node-0", errors.New("replacement failure"))
				if err != nil || !owned || state.Failure != firstCause.Error() {
					t.Fatalf("failure replay = owned:%t state:%+v err:%v", owned, state, err)
				}
				if _, done, err := store.AdvanceChain(ctx, chainID, "node-0"); err != nil || !done {
					t.Fatalf("late success = done:%t err:%v", done, err)
				}
			})

			t.Run("stale and invalid nodes", func(t *testing.T) {
				store := factory(t)
				outcomes := requireOutcomeStore(t, store)
				const chainID = "chain-node-validation"
				if err := store.CreateChain(ctx, ChainRecord{ChainID: chainID, Nodes: []ChainNode{{NodeID: "node-0"}, {NodeID: "node-1"}, {NodeID: "node-2"}}}); err != nil {
					t.Fatalf("create chain: %v", err)
				}
				if _, _, err := outcomes.FailChainNode(ctx, chainID, "node-1", errors.New("future failure")); err == nil {
					t.Fatal("future failure was accepted")
				}
				if _, _, err := store.AdvanceChain(ctx, chainID, "node-1"); err == nil {
					t.Fatal("future success was accepted")
				}
				if _, _, err := store.AdvanceChain(ctx, chainID, "missing-node"); err == nil {
					t.Fatal("unknown success was accepted")
				}
				if _, _, err := store.AdvanceChain(ctx, chainID, "node-0"); err != nil {
					t.Fatalf("advance current node: %v", err)
				}
				laterCause := errors.New("later node failed")
				if _, owned, err := outcomes.FailChainNode(ctx, chainID, "node-1", laterCause); err != nil || !owned {
					t.Fatalf("fail current node = owned:%t err:%v", owned, err)
				}
				state, owned, err := outcomes.FailChainNode(ctx, chainID, "node-0", errors.New("stale failure"))
				if err != nil || owned || !state.Failed || state.Failure != laterCause.Error() || state.NextIndex != 1 {
					t.Fatalf("stale failure = owned:%t state:%+v err:%v", owned, state, err)
				}
				if _, _, err := store.AdvanceChain(ctx, chainID, "node-2"); err == nil {
					t.Fatal("future success after failure was accepted")
				}
				if _, _, err := outcomes.FailChainNode(ctx, chainID, "node-2", errors.New("future failure")); err == nil {
					t.Fatal("future failure after failure was accepted")
				}
			})
		})
	}
}

// TestStoreContract_TerminalChainRejectsUnknownNodes keeps malformed
// deliveries from inheriting the idempotent result of a real terminal node.
func TestStoreContract_TerminalChainRejectsUnknownNodes(t *testing.T) {
	for name, factory := range testStoreFactories(t) {
		t.Run(name, func(t *testing.T) {
			for _, terminal := range []string{"completed", "failed"} {
				t.Run(terminal, func(t *testing.T) {
					ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
					defer cancel()
					store := factory(t)
					outcomes := requireOutcomeStore(t, store)
					chainID := "chain-terminal-unknown-" + terminal
					if err := store.CreateChain(ctx, ChainRecord{
						ChainID: chainID,
						Nodes: []ChainNode{
							{NodeID: "node-0"},
							{NodeID: "node-1"},
						},
					}); err != nil {
						t.Fatalf("create chain: %v", err)
					}
					if _, _, err := store.AdvanceChain(ctx, chainID, "node-0"); err != nil {
						t.Fatalf("advance first node: %v", err)
					}
					if terminal == "completed" {
						if _, done, err := store.AdvanceChain(ctx, chainID, "node-1"); err != nil || !done {
							t.Fatalf("complete chain = done:%t err:%v", done, err)
						}
					} else {
						if _, owned, err := outcomes.FailChainNode(ctx, chainID, "node-1", errors.New("terminal failure")); err != nil || !owned {
							t.Fatalf("fail chain = owned:%t err:%v", owned, err)
						}
					}
					before, err := store.GetChain(ctx, chainID)
					if err != nil {
						t.Fatalf("get terminal chain: %v", err)
					}
					if _, _, err := store.AdvanceChain(ctx, chainID, "node-missing"); err == nil {
						t.Fatal("unknown success inherited terminal state")
					}
					if _, _, err := outcomes.FailChainNode(ctx, chainID, "node-missing", errors.New("unknown failure")); err == nil {
						t.Fatal("unknown failure inherited terminal state")
					}
					after, err := store.GetChain(ctx, chainID)
					if err != nil {
						t.Fatalf("get chain after unknown deliveries: %v", err)
					}
					if after.NextIndex != before.NextIndex || after.Completed != before.Completed || after.Failed != before.Failed || after.Failure != before.Failure || !after.UpdatedAt.Equal(before.UpdatedAt) {
						t.Fatalf("unknown delivery changed terminal chain: before=%+v after=%+v", before, after)
					}
				})
			}
		})
	}
}

// TestStoreContract_ConcurrentChainNodeOutcomeOwnership repeatedly races both
// outcomes and accepts only one of the two valid linearized states.
func TestStoreContract_ConcurrentChainNodeOutcomeOwnership(t *testing.T) {
	for name, factory := range testStoreFactories(t) {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			for iteration := range 12 {
				store := factory(t)
				outcomes := requireOutcomeStore(t, store)
				chainID := fmt.Sprintf("chain-outcome-race-%02d", iteration)
				if err := store.CreateChain(ctx, ChainRecord{ChainID: chainID, Nodes: []ChainNode{{NodeID: "node-0"}, {NodeID: "node-1"}}}); err != nil {
					t.Fatalf("create chain: %v", err)
				}
				start := make(chan struct{})
				errs := make(chan error, 32)
				var wg sync.WaitGroup
				for delivery := range 32 {
					wg.Add(1)
					go func(fail bool) {
						defer wg.Done()
						<-start
						if fail {
							_, _, err := outcomes.FailChainNode(ctx, chainID, "node-0", errors.New("raced failure"))
							errs <- err
							return
						}
						_, _, err := store.AdvanceChain(ctx, chainID, "node-0")
						errs <- err
					}(delivery%2 == 0)
				}
				close(start)
				waitStoreContractOperations(t, &wg)
				close(errs)
				for err := range errs {
					if err != nil {
						t.Fatalf("race operation: %v", err)
					}
				}
				state, err := store.GetChain(ctx, chainID)
				if err != nil {
					t.Fatalf("get raced chain: %v", err)
				}
				successWon := state.NextIndex == 1 && !state.Failed && !state.Completed
				failureWon := state.NextIndex == 0 && state.Failed && !state.Completed
				if !successWon && !failureWon {
					t.Fatalf("non-linearized raced state = %+v", state)
				}
			}
		})
	}
}

// TestStoreContract_ConcurrentFinalChainNodeOutcomeOwnership races failure
// against the two-step SQL advancement that also marks the chain completed.
func TestStoreContract_ConcurrentFinalChainNodeOutcomeOwnership(t *testing.T) {
	for name, factory := range testStoreFactories(t) {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			for iteration := range 8 {
				store := factory(t)
				outcomes := requireOutcomeStore(t, store)
				chainID := fmt.Sprintf("chain-final-outcome-race-%02d", iteration)
				if err := store.CreateChain(ctx, ChainRecord{ChainID: chainID, Nodes: []ChainNode{{NodeID: "node-final"}}}); err != nil {
					t.Fatalf("create chain: %v", err)
				}
				start := make(chan struct{})
				errs := make(chan error, 32)
				var wg sync.WaitGroup
				for delivery := range 32 {
					wg.Add(1)
					go func(fail bool) {
						defer wg.Done()
						<-start
						if fail {
							_, _, err := outcomes.FailChainNode(ctx, chainID, "node-final", errors.New("raced final failure"))
							errs <- err
							return
						}
						_, _, err := store.AdvanceChain(ctx, chainID, "node-final")
						errs <- err
					}(delivery%2 == 0)
				}
				close(start)
				waitStoreContractOperations(t, &wg)
				close(errs)
				for err := range errs {
					if err != nil {
						t.Fatalf("race final chain outcome: %v", err)
					}
				}
				state, err := store.GetChain(ctx, chainID)
				if err != nil {
					t.Fatalf("get raced final chain: %v", err)
				}
				successWon := state.NextIndex == 1 && state.Completed && !state.Failed
				failureWon := state.NextIndex == 0 && !state.Completed && state.Failed
				if !successWon && !failureWon {
					t.Fatalf("non-linearized final chain state = %+v", state)
				}
			}
		})
	}
}

// TestStoreContract_BatchJobOutcomeOwnership proves contradictory redelivery
// cannot change either member state or the aggregate's logical winner.
func TestStoreContract_BatchJobOutcomeOwnership(t *testing.T) {
	for name, factory := range testStoreFactories(t) {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
			defer cancel()
			for _, first := range []BatchJobOutcome{BatchJobSucceeded, BatchJobFailed} {
				t.Run(string(first), func(t *testing.T) {
					store := factory(t)
					outcomes := requireOutcomeStore(t, store)
					batchID := "batch-outcome-" + string(first)
					if err := store.CreateBatch(ctx, BatchRecord{BatchID: batchID, AllowFailed: true, Jobs: []BatchJob{{JobID: "job-0"}, {JobID: "job-1"}}}); err != nil {
						t.Fatalf("create batch: %v", err)
					}
					state, owned, err := outcomes.SettleBatchJob(ctx, batchID, "job-0", first, errors.New("first cause"))
					if err != nil || !owned {
						t.Fatalf("first outcome = owned:%t err:%v", owned, err)
					}
					if state.Pending != 1 || state.Processed != 1 || state.Failed != boolInt(first == BatchJobFailed) {
						t.Fatalf("first outcome state = %+v", state)
					}
					if _, owned, err := outcomes.SettleBatchJob(ctx, batchID, "job-0", first, nil); err != nil || !owned {
						t.Fatalf("same-outcome replay = owned:%t err:%v", owned, err)
					}
					opposite := BatchJobFailed
					if first == BatchJobFailed {
						opposite = BatchJobSucceeded
					}
					state, owned, err = outcomes.SettleBatchJob(ctx, batchID, "job-0", opposite, errors.New("opposite cause"))
					if err != nil || owned || state.Pending != 1 || state.Processed != 1 || state.Failed != boolInt(first == BatchJobFailed) {
						t.Fatalf("opposite replay = owned:%t state:%+v err:%v", owned, state, err)
					}
				})
			}
			t.Run("invalid outcome", func(t *testing.T) {
				store := factory(t)
				outcomes := requireOutcomeStore(t, store)
				const batchID = "batch-invalid-outcome"
				if err := store.CreateBatch(ctx, BatchRecord{BatchID: batchID, Jobs: []BatchJob{{JobID: "job-0"}}}); err != nil {
					t.Fatalf("create batch: %v", err)
				}
				if _, _, err := outcomes.SettleBatchJob(ctx, batchID, "job-0", BatchJobOutcome("unknown"), nil); err == nil {
					t.Fatal("invalid batch outcome was accepted")
				}
				state, err := store.GetBatch(ctx, batchID)
				if err != nil {
					t.Fatalf("get batch: %v", err)
				}
				if state.Pending != 1 || state.Processed != 0 || state.Failed != 0 || state.Completed {
					t.Fatalf("invalid outcome changed state: %+v", state)
				}
			})
			t.Run("missing member", func(t *testing.T) {
				store := factory(t)
				outcomes := requireOutcomeStore(t, store)
				const batchID = "batch-missing-member"
				if err := store.CreateBatch(ctx, BatchRecord{BatchID: batchID, Jobs: []BatchJob{{JobID: "job-known"}}}); err != nil {
					t.Fatalf("create batch: %v", err)
				}
				if _, _, err := outcomes.SettleBatchJob(ctx, batchID, "job-missing", BatchJobSucceeded, nil); !errors.Is(err, ErrNotFound) {
					t.Fatalf("missing member outcome error = %v, want ErrNotFound", err)
				}
				state, err := store.GetBatch(ctx, batchID)
				if err != nil {
					t.Fatalf("get batch: %v", err)
				}
				if state.Pending != 1 || state.Processed != 0 || state.Failed != 0 || state.Completed {
					t.Fatalf("missing member outcome changed state: %+v", state)
				}
			})
		})
	}
}

// TestStoreContract_ConcurrentBatchJobOutcomeOwnership makes every delivery
// observe one immutable member winner while aggregate counters advance once.
func TestStoreContract_ConcurrentBatchJobOutcomeOwnership(t *testing.T) {
	for name, factory := range testStoreFactories(t) {
		t.Run(name, func(t *testing.T) {
			store := factory(t)
			outcomes := requireOutcomeStore(t, store)
			ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
			defer cancel()
			const batchID = "batch-concurrent-outcome"
			if err := store.CreateBatch(ctx, BatchRecord{BatchID: batchID, AllowFailed: true, Jobs: []BatchJob{{JobID: "job-shared"}, {JobID: "job-pending"}}}); err != nil {
				t.Fatalf("create batch: %v", err)
			}
			start := make(chan struct{})
			errs := make(chan error, 32)
			var wg sync.WaitGroup
			for delivery := range 32 {
				outcome := BatchJobSucceeded
				if delivery%2 == 0 {
					outcome = BatchJobFailed
				}
				wg.Add(1)
				go func(outcome BatchJobOutcome) {
					defer wg.Done()
					<-start
					_, _, err := outcomes.SettleBatchJob(ctx, batchID, "job-shared", outcome, errors.New("raced outcome"))
					errs <- err
				}(outcome)
			}
			close(start)
			waitStoreContractOperations(t, &wg)
			close(errs)
			for err := range errs {
				if err != nil {
					t.Fatalf("concurrent settlement: %v", err)
				}
			}
			state, err := store.GetBatch(ctx, batchID)
			if err != nil {
				t.Fatalf("get batch: %v", err)
			}
			if state.Pending != 1 || state.Processed != 1 || (state.Failed != 0 && state.Failed != 1) || state.Completed {
				t.Fatalf("concurrent outcome state = %+v", state)
			}
			_, successOwned, err := outcomes.SettleBatchJob(ctx, batchID, "job-shared", BatchJobSucceeded, nil)
			if err != nil {
				t.Fatalf("replay success: %v", err)
			}
			_, failureOwned, err := outcomes.SettleBatchJob(ctx, batchID, "job-shared", BatchJobFailed, errors.New("replayed failure"))
			if err != nil {
				t.Fatalf("replay failure: %v", err)
			}
			if successOwned == failureOwned || successOwned != (state.Failed == 0) {
				t.Fatalf("replay ownership = success:%t failure:%t state:%+v", successOwned, failureOwned, state)
			}
		})
	}
}

// TestStoreContract_ConcurrentTerminalBatchJobOutcomeOwnership races both
// categories through final completion and fail-fast cancellation branches.
func TestStoreContract_ConcurrentTerminalBatchJobOutcomeOwnership(t *testing.T) {
	for name, factory := range testStoreFactories(t) {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			for _, policy := range []struct {
				name          string
				allowFailures bool
			}{
				{name: "allow_failures", allowFailures: true},
				{name: "fail_fast", allowFailures: false},
			} {
				t.Run(policy.name, func(t *testing.T) {
					for iteration := range 6 {
						store := factory(t)
						outcomes := requireOutcomeStore(t, store)
						batchID := fmt.Sprintf("batch-terminal-outcome-%s-%02d", policy.name, iteration)
						if err := store.CreateBatch(ctx, BatchRecord{
							BatchID:     batchID,
							AllowFailed: policy.allowFailures,
							Jobs:        []BatchJob{{JobID: "job-final"}},
						}); err != nil {
							t.Fatalf("create batch: %v", err)
						}
						start := make(chan struct{})
						errs := make(chan error, 32)
						var wg sync.WaitGroup
						for delivery := range 32 {
							outcome := BatchJobSucceeded
							if delivery%2 == 0 {
								outcome = BatchJobFailed
							}
							wg.Add(1)
							go func(outcome BatchJobOutcome) {
								defer wg.Done()
								<-start
								_, _, err := outcomes.SettleBatchJob(ctx, batchID, "job-final", outcome, errors.New("raced terminal outcome"))
								errs <- err
							}(outcome)
						}
						close(start)
						waitStoreContractOperations(t, &wg)
						close(errs)
						for err := range errs {
							if err != nil {
								t.Fatalf("race terminal batch outcome: %v", err)
							}
						}
						state, err := store.GetBatch(ctx, batchID)
						if err != nil {
							t.Fatalf("get terminal batch: %v", err)
						}
						if state.Pending != 0 || state.Processed != 1 || !state.Completed || (state.Failed != 0 && state.Failed != 1) {
							t.Fatalf("terminal batch state = %+v", state)
						}
						wantCancelled := state.Failed == 1 && !policy.allowFailures
						if state.Cancelled != wantCancelled {
							t.Fatalf("terminal batch cancellation = %t, want %t for state %+v", state.Cancelled, wantCancelled, state)
						}
					}
				})
			}
		})
	}
}

// boolInt keeps aggregate expectations readable without obscuring the outcome
// condition inside test tables.
func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

// TestStoreContract_ConcurrentDuplicateBatchSettlement proves redelivery can
// claim one member only once even when every delivery observes it concurrently.
func TestStoreContract_ConcurrentDuplicateBatchSettlement(t *testing.T) {
	for name, factory := range testStoreFactories(t) {
		t.Run(name, func(t *testing.T) {
			s := factory(t)
			ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
			defer cancel()
			const batchID = "batch-concurrent-duplicate"
			if err := s.CreateBatch(ctx, BatchRecord{
				BatchID:     batchID,
				DispatchID:  "dispatch-concurrent-duplicate",
				AllowFailed: true,
				Jobs: []BatchJob{
					{JobID: "job-shared", Job: StoredJob{Type: "reports:shared"}},
					{JobID: "job-final", Job: StoredJob{Type: "reports:final"}},
				},
				CreatedAt: time.Now(),
			}); err != nil {
				t.Fatalf("create batch: %v", err)
			}

			const deliveries = 32
			start := make(chan struct{})
			errs := make(chan error, deliveries)
			var wg sync.WaitGroup
			for range deliveries {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					_, _, err := s.MarkBatchJobSucceeded(ctx, batchID, "job-shared")
					errs <- err
				}()
			}
			close(start)
			waitStoreContractOperations(t, &wg)
			close(errs)
			for err := range errs {
				if err != nil {
					t.Fatalf("concurrent duplicate settlement: %v", err)
				}
			}

			state, err := s.GetBatch(ctx, batchID)
			if err != nil {
				t.Fatalf("get batch: %v", err)
			}
			if state.Pending != 1 || state.Processed != 1 || state.Failed != 0 || state.Completed {
				t.Fatalf("duplicate settlement state = %+v, want one processed and one pending", state)
			}
		})
	}
}

// TestStoreContract_ConcurrentDistinctBatchSettlement proves aggregate
// counters cannot overwrite one another when independent members finish.
func TestStoreContract_ConcurrentDistinctBatchSettlement(t *testing.T) {
	for name, factory := range testStoreFactories(t) {
		t.Run(name, func(t *testing.T) {
			s := factory(t)
			ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
			defer cancel()
			const jobCount = 32
			jobs := make([]BatchJob, jobCount)
			for i := range jobs {
				jobs[i] = BatchJob{
					JobID: fmt.Sprintf("job-%02d", i),
					Job:   StoredJob{Type: "reports:member"},
				}
			}
			for _, policy := range []struct {
				name          string
				allowFailures bool
			}{
				{name: "allow_failures", allowFailures: true},
				{name: "fail_fast", allowFailures: false},
			} {
				t.Run(policy.name, func(t *testing.T) {
					batchID := "batch-concurrent-distinct-" + policy.name
					if err := s.CreateBatch(ctx, BatchRecord{
						BatchID:     batchID,
						DispatchID:  "dispatch-concurrent-distinct-" + policy.name,
						AllowFailed: policy.allowFailures,
						Jobs:        jobs,
						CreatedAt:   time.Now(),
					}); err != nil {
						t.Fatalf("create batch: %v", err)
					}

					start := make(chan struct{})
					errs := make(chan error, jobCount)
					var wg sync.WaitGroup
					for i, job := range jobs {
						wg.Add(1)
						go func(index int, member BatchJob) {
							defer wg.Done()
							<-start
							var err error
							if index%2 == 0 {
								_, _, err = s.MarkBatchJobSucceeded(ctx, batchID, member.JobID)
							} else {
								_, _, err = s.MarkBatchJobFailed(ctx, batchID, member.JobID, errors.New("member failed"))
							}
							errs <- err
						}(i, job)
					}
					close(start)
					waitStoreContractOperations(t, &wg)
					close(errs)
					for err := range errs {
						if err != nil {
							t.Fatalf("concurrent distinct settlement: %v", err)
						}
					}

					state, err := s.GetBatch(ctx, batchID)
					if err != nil {
						t.Fatalf("get batch: %v", err)
					}
					wantCancelled := !policy.allowFailures
					if state.Pending != 0 || state.Processed != jobCount || state.Failed != jobCount/2 || !state.Completed || state.Cancelled != wantCancelled {
						t.Fatalf("concurrent settlement state = %+v, want exact aggregate counters and cancelled=%t", state, wantCancelled)
					}
				})
			}
		})
	}
}

// TestStoreContract_DuplicateSuccessCannotBecomeFailure keeps the first
// committed member outcome authoritative across inconsistent redelivery.
func TestStoreContract_DuplicateSuccessCannotBecomeFailure(t *testing.T) {
	for name, factory := range testStoreFactories(t) {
		t.Run(name, func(t *testing.T) {
			s := factory(t)
			ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
			defer cancel()
			const batchID = "batch-immutable-outcome"
			if err := s.CreateBatch(ctx, BatchRecord{
				BatchID:     batchID,
				DispatchID:  "dispatch-immutable-outcome",
				AllowFailed: false,
				Jobs: []BatchJob{
					{JobID: "job-first", Job: StoredJob{Type: "reports:first"}},
					{JobID: "job-second", Job: StoredJob{Type: "reports:second"}},
				},
				CreatedAt: time.Now(),
			}); err != nil {
				t.Fatalf("create batch: %v", err)
			}
			if _, done, err := s.MarkBatchJobSucceeded(ctx, batchID, "job-first"); err != nil || done {
				t.Fatalf("mark first success = done:%t err:%v, want active batch", done, err)
			}

			state, done, err := s.MarkBatchJobFailed(ctx, batchID, "job-first", errors.New("inconsistent duplicate"))
			if err != nil {
				t.Fatalf("mark inconsistent duplicate: %v", err)
			}
			if done || state.Pending != 1 || state.Processed != 1 || state.Failed != 0 || state.Cancelled || state.Completed {
				t.Fatalf("inconsistent duplicate state = %+v done:%t, want original success retained", state, done)
			}
		})
	}
}

// TestStoreContract_ConcurrentDuplicateChainAdvance proves every redelivery
// observes the same current successor after one node claim wins.
func TestStoreContract_ConcurrentDuplicateChainAdvance(t *testing.T) {
	for name, factory := range testStoreFactories(t) {
		t.Run(name, func(t *testing.T) {
			s := factory(t)
			ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
			defer cancel()
			const chainID = "chain-concurrent-duplicate"
			if err := s.CreateChain(ctx, ChainRecord{
				ChainID:    chainID,
				DispatchID: "dispatch-concurrent-chain",
				Nodes: []ChainNode{
					{NodeID: "node-first", Job: StoredJob{Type: "reports:first"}},
					{NodeID: "node-second", Job: StoredJob{Type: "reports:second"}},
				},
				CreatedAt: time.Now(),
			}); err != nil {
				t.Fatalf("create chain: %v", err)
			}

			const deliveries = 32
			start := make(chan struct{})
			errs := make(chan error, deliveries)
			var wg sync.WaitGroup
			for range deliveries {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					next, done, err := s.AdvanceChain(ctx, chainID, "node-first")
					if err == nil && (done || next == nil || next.NodeID != "node-second") {
						err = fmt.Errorf("next = %+v done:%t, want node-second", next, done)
					}
					errs <- err
				}()
			}
			close(start)
			waitStoreContractOperations(t, &wg)
			close(errs)
			for err := range errs {
				if err != nil {
					t.Fatalf("concurrent duplicate advance: %v", err)
				}
			}

			state, err := s.GetChain(ctx, chainID)
			if err != nil {
				t.Fatalf("get chain: %v", err)
			}
			if state.NextIndex != 1 || state.Completed || state.Failed {
				t.Fatalf("concurrent chain state = %+v, want one committed node", state)
			}
		})
	}
}

func TestStoreContract_NotFound(t *testing.T) {
	for name, factory := range testStoreFactories(t) {
		t.Run(name, func(t *testing.T) {
			s := factory(t)
			ctx := context.Background()

			if _, err := s.GetChain(ctx, "missing"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("expected chain ErrNotFound, got %v", err)
			}
			if _, err := s.GetBatch(ctx, "missing"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("expected batch ErrNotFound, got %v", err)
			}
		})
	}
}

func TestStoreContract_ChainAdvanceIdempotent(t *testing.T) {
	for name, factory := range testStoreFactories(t) {
		t.Run(name, func(t *testing.T) {
			s := factory(t)
			ctx := context.Background()
			chainID := "chain-contract"

			if err := s.CreateChain(ctx, ChainRecord{
				ChainID:    chainID,
				DispatchID: "d1",
				Queue:      "default",
				Nodes: []ChainNode{
					{NodeID: "n1", Job: StoredJob{Type: "monitor:poll"}},
					{NodeID: "n2", Job: StoredJob{Type: "monitor:downsample"}},
				},
				CreatedAt: time.Now(),
			}); err != nil {
				t.Fatalf("create chain: %v", err)
			}

			next, done, err := s.AdvanceChain(ctx, chainID, "n1")
			if err != nil {
				t.Fatalf("advance first: %v", err)
			}
			if done || next == nil || next.NodeID != "n2" {
				t.Fatalf("expected next n2 on first advance, done=%v next=%+v", done, next)
			}

			next, done, err = s.AdvanceChain(ctx, chainID, "n1")
			if err != nil {
				t.Fatalf("advance duplicate: %v", err)
			}
			if done || next == nil || next.NodeID != "n2" {
				t.Fatalf("expected idempotent duplicate advance, done=%v next=%+v", done, next)
			}

			next, done, err = s.AdvanceChain(ctx, chainID, "n2")
			if err != nil {
				t.Fatalf("advance final: %v", err)
			}
			if !done || next != nil {
				t.Fatalf("expected chain done with nil next, done=%v next=%+v", done, next)
			}
		})
	}
}

// TestStoreContract_CompletedChainRejectsLateFailure keeps the first terminal
// outcome authoritative when a competing delivery reports failure too late.
func TestStoreContract_CompletedChainRejectsLateFailure(t *testing.T) {
	for name, factory := range testStoreFactories(t) {
		t.Run(name, func(t *testing.T) {
			store := factory(t)
			ctx := context.Background()
			const chainID = "chain-completed-before-failure"
			if err := store.CreateChain(ctx, ChainRecord{
				ChainID:    chainID,
				DispatchID: "dispatch-completed-before-failure",
				Nodes:      []ChainNode{{NodeID: "node-only", Job: StoredJob{Type: "reports:only"}}},
				CreatedAt:  time.Now(),
			}); err != nil {
				t.Fatalf("create chain: %v", err)
			}
			if next, done, err := store.AdvanceChain(ctx, chainID, "node-only"); err != nil || !done || next != nil {
				t.Fatalf("complete chain = next:%+v done:%t err:%v", next, done, err)
			}
			if err := store.FailChain(ctx, chainID, errors.New("late competing failure")); err != nil {
				t.Fatalf("fail completed chain: %v", err)
			}
			state, err := store.GetChain(ctx, chainID)
			if err != nil {
				t.Fatalf("get chain: %v", err)
			}
			if !state.Completed || state.Failed || state.Failure != "" {
				t.Fatalf("late failure changed completed chain: %+v", state)
			}
		})
	}
}

func TestStoreContract_BatchTerminalBehavior(t *testing.T) {
	for name, factory := range testStoreFactories(t) {
		t.Run(name, func(t *testing.T) {
			s := factory(t)
			ctx := context.Background()
			batchID := "batch-contract"

			if err := s.CreateBatch(ctx, BatchRecord{
				BatchID:     batchID,
				DispatchID:  "d1",
				Name:        "contract",
				Queue:       "default",
				AllowFailed: false,
				Jobs: []BatchJob{
					{JobID: "j1", Job: StoredJob{Type: "monitor:poll"}},
					{JobID: "j2", Job: StoredJob{Type: "monitor:downsample"}},
				},
				CreatedAt: time.Now(),
			}); err != nil {
				t.Fatalf("create batch: %v", err)
			}

			st, done, err := s.MarkBatchJobSucceeded(ctx, batchID, "j1")
			if err != nil {
				t.Fatalf("mark success: %v", err)
			}
			if done {
				t.Fatal("expected batch not done after first success")
			}
			if st.Pending != 1 || st.Processed != 1 || st.Failed != 0 {
				t.Fatalf("unexpected mid state: %+v", st)
			}

			st, done, err = s.MarkBatchJobFailed(ctx, batchID, "j2", errors.New("boom"))
			if err != nil {
				t.Fatalf("mark failed: %v", err)
			}
			if !done {
				t.Fatal("expected batch done on failure when allow_failed=false")
			}
			if !st.Completed || !st.Cancelled || st.Failed != 1 {
				t.Fatalf("unexpected terminal state: %+v", st)
			}
		})
	}
}

func TestStoreContract_CallbackMarkerIdempotent(t *testing.T) {
	for name, factory := range testStoreFactories(t) {
		t.Run(name, func(t *testing.T) {
			s := factory(t)
			ctx := context.Background()
			key := "batch_finally:contract"

			first, err := s.MarkCallbackInvoked(ctx, key)
			if err != nil {
				t.Fatalf("first callback marker: %v", err)
			}
			if !first {
				t.Fatal("expected first callback marker=true")
			}

			second, err := s.MarkCallbackInvoked(ctx, key)
			if err != nil {
				t.Fatalf("second callback marker: %v", err)
			}
			if second {
				t.Fatal("expected second callback marker=false")
			}
		})
	}
}

func TestStoreContract_PruneClearsOldCallbackMarkers(t *testing.T) {
	for name, factory := range testStoreFactories(t) {
		t.Run(name, func(t *testing.T) {
			s := factory(t)
			ctx := context.Background()
			key := "batch_finally:contract-prune"

			first, err := s.MarkCallbackInvoked(ctx, key)
			if err != nil {
				t.Fatalf("first callback marker: %v", err)
			}
			if !first {
				t.Fatal("expected first callback marker=true")
			}

			// Future cutoff ensures just-inserted marker is considered old.
			if err := s.Prune(ctx, time.Now().Add(1*time.Minute)); err != nil {
				t.Fatalf("prune markers: %v", err)
			}

			again, err := s.MarkCallbackInvoked(ctx, key)
			if err != nil {
				t.Fatalf("callback marker after prune: %v", err)
			}
			if !again {
				t.Fatal("expected callback marker to be insertable again after prune")
			}
		})
	}
}

func TestStoreContract_BatchAllowFailuresContinues(t *testing.T) {
	for name, factory := range testStoreFactories(t) {
		t.Run(name, func(t *testing.T) {
			s := factory(t)
			ctx := context.Background()
			batchID := "batch-allow-fail-contract"

			if err := s.CreateBatch(ctx, BatchRecord{
				BatchID:     batchID,
				DispatchID:  "d1",
				Name:        "allow-fail",
				Queue:       "default",
				AllowFailed: true,
				Jobs: []BatchJob{
					{JobID: "j1", Job: StoredJob{Type: "monitor:poll"}},
					{JobID: "j2", Job: StoredJob{Type: "monitor:downsample"}},
					{JobID: "j3", Job: StoredJob{Type: "monitor:alert"}},
				},
				CreatedAt: time.Now(),
			}); err != nil {
				t.Fatalf("create batch: %v", err)
			}

			st, done, err := s.MarkBatchJobFailed(ctx, batchID, "j1", errors.New("boom"))
			if err != nil {
				t.Fatalf("mark first failed: %v", err)
			}
			if done {
				t.Fatal("expected batch to continue when allow_failed=true")
			}
			if st.Cancelled {
				t.Fatal("expected batch not cancelled when allow_failed=true")
			}

			st, done, err = s.MarkBatchJobSucceeded(ctx, batchID, "j2")
			if err != nil {
				t.Fatalf("mark second success: %v", err)
			}
			if done {
				t.Fatal("expected batch still not done after second job")
			}

			st, done, err = s.MarkBatchJobSucceeded(ctx, batchID, "j3")
			if err != nil {
				t.Fatalf("mark third success: %v", err)
			}
			if !done || !st.Completed {
				t.Fatalf("expected batch completed, done=%v state=%+v", done, st)
			}
			if st.Failed != 1 || st.Processed != 3 || st.Pending != 0 {
				t.Fatalf("unexpected final counters: %+v", st)
			}
		})
	}
}

func TestStoreContract_BatchDuplicateTerminalUpdateDoesNotDoubleCount(t *testing.T) {
	for name, factory := range testStoreFactories(t) {
		t.Run(name, func(t *testing.T) {
			s := factory(t)
			ctx := context.Background()
			batchID := "batch-dup-contract"

			if err := s.CreateBatch(ctx, BatchRecord{
				BatchID:     batchID,
				DispatchID:  "d1",
				Name:        "dup",
				Queue:       "default",
				AllowFailed: true,
				Jobs: []BatchJob{
					{JobID: "j1", Job: StoredJob{Type: "monitor:poll"}},
				},
				CreatedAt: time.Now(),
			}); err != nil {
				t.Fatalf("create batch: %v", err)
			}

			st, done, err := s.MarkBatchJobSucceeded(ctx, batchID, "j1")
			if err != nil {
				t.Fatalf("mark success first: %v", err)
			}
			if !done || !st.Completed {
				t.Fatalf("expected completed after first success, done=%v state=%+v", done, st)
			}

			st, done, err = s.MarkBatchJobSucceeded(ctx, batchID, "j1")
			if err != nil {
				t.Fatalf("mark success duplicate: %v", err)
			}
			if !done || !st.Completed {
				t.Fatalf("expected completed after duplicate success, done=%v state=%+v", done, st)
			}
			if st.Processed != 1 || st.Pending != 0 || st.Failed != 0 {
				t.Fatalf("expected counters unchanged after duplicate terminal update, got %+v", st)
			}
		})
	}
}
