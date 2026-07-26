//go:build integration

package root_test

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goforj/queue"
	"github.com/goforj/queue/integration/testenv"
)

// TestCanonicalWorkflowContract_AllExecutableBackends proves the root queue
// API owns chain and batch orchestration without the retired bus facade.
func TestCanonicalWorkflowContract_AllExecutableBackends(t *testing.T) {
	fixtures := []struct {
		name     string
		queue    string
		newQueue func(t *testing.T) *queue.Queue
	}{
		{name: testenv.BackendSync, queue: "default", newQueue: func(t *testing.T) *queue.Queue {
			q, err := testenv.NewQueue(syncCfg(), queue.WithWorkers(1))
			if err != nil {
				t.Fatalf("new sync queue: %v", err)
			}
			return q
		}},
		{name: testenv.BackendWorkerpool, queue: "default", newQueue: func(t *testing.T) *queue.Queue {
			q, err := testenv.NewQueue(workerpoolCfg(), queue.WithWorkers(1))
			if err != nil {
				t.Fatalf("new workerpool queue: %v", err)
			}
			return q
		}},
		{name: testenv.BackendRedis, queue: "workflow_contract_redis", newQueue: func(t *testing.T) *queue.Queue {
			ensureRedis(t)
			q, err := testenv.NewQueue(withDefaultQueue(redisCfg(integrationRedis.addr), "workflow_contract_redis"), queue.WithWorkers(1))
			if err != nil {
				t.Fatalf("new redis queue: %v", err)
			}
			return q
		}},
		{name: testenv.BackendNATS, queue: "workflow_contract_nats", newQueue: func(t *testing.T) *queue.Queue {
			ensureNATS(t)
			q, err := testenv.NewQueue(withDefaultQueue(natsCfg(integrationNATS.url), "workflow_contract_nats"), queue.WithWorkers(1))
			if err != nil {
				t.Fatalf("new nats queue: %v", err)
			}
			return q
		}},
		{name: testenv.BackendSQS, queue: "workflow_contract_sqs", newQueue: func(t *testing.T) *queue.Queue {
			ensureSQS(t)
			q, err := testenv.NewQueue(withDefaultQueue(sqsCfg(integrationSQS.region, integrationSQS.endpoint, integrationSQS.accessKey, integrationSQS.secretKey), "workflow_contract_sqs"), queue.WithWorkers(1))
			if err != nil {
				t.Fatalf("new sqs queue: %v", err)
			}
			return q
		}},
		{name: testenv.BackendRabbitMQ, queue: "workflow_contract_rabbitmq", newQueue: func(t *testing.T) *queue.Queue {
			ensureRabbitMQ(t)
			q, err := testenv.NewQueue(withDefaultQueue(rabbitmqCfg(integrationRabbitMQ.url), "workflow_contract_rabbitmq"), queue.WithWorkers(1))
			if err != nil {
				t.Fatalf("new RabbitMQ queue: %v", err)
			}
			return q
		}},
		{name: testenv.BackendSQLite, queue: "workflow_contract_sqlite", newQueue: func(t *testing.T) *queue.Queue {
			q, err := testenv.NewQueue(withDefaultQueue(sqliteCfg(fmt.Sprintf("%s/workflow-contract.db", t.TempDir())), "workflow_contract_sqlite"), queue.WithWorkers(1))
			if err != nil {
				t.Fatalf("new sqlite queue: %v", err)
			}
			return q
		}},
		{name: testenv.BackendMySQL, queue: "workflow_contract_mysql", newQueue: func(t *testing.T) *queue.Queue {
			ensureMySQLDB(t)
			q, err := testenv.NewQueue(withDefaultQueue(mysqlCfg(mysqlDSN(integrationMySQL.addr)), "workflow_contract_mysql"), queue.WithWorkers(1))
			if err != nil {
				t.Fatalf("new mysql queue: %v", err)
			}
			return q
		}},
		{name: testenv.BackendPostgres, queue: "workflow_contract_postgres", newQueue: func(t *testing.T) *queue.Queue {
			ensurePostgresDB(t)
			q, err := testenv.NewQueue(withDefaultQueue(postgresCfg(postgresDSN(integrationPostgres.addr)), "workflow_contract_postgres"), queue.WithWorkers(1))
			if err != nil {
				t.Fatalf("new postgres queue: %v", err)
			}
			return q
		}},
	}

	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			if !integrationBackendEnabled(fixture.name) {
				t.Skipf("%s integration backend not selected", fixture.name)
			}
			q := fixture.newQueue(t)
			t.Cleanup(func() { _ = q.Shutdown(context.Background()) })

			var chainCalls atomic.Int32
			var batchCalls atomic.Int32
			q.Register("workflow:chain:first", func(context.Context, queue.Message) error {
				chainCalls.Add(1)
				return nil
			})
			q.Register("workflow:chain:second", func(context.Context, queue.Message) error {
				chainCalls.Add(1)
				return nil
			})
			q.Register("workflow:batch", func(context.Context, queue.Message) error {
				batchCalls.Add(1)
				return nil
			})
			if err := q.StartWorkers(context.Background()); err != nil {
				t.Fatalf("start workers: %v", err)
			}

			chainID, err := q.Chain(
				queue.NewJob("workflow:chain:first"),
				queue.NewJob("workflow:chain:second"),
			).OnQueue(fixture.queue).Dispatch(context.Background())
			if err != nil {
				t.Fatalf("dispatch chain: %v", err)
			}
			waitForCanonicalWorkflow(t, "chain completion", func() bool {
				state, findErr := q.FindChain(context.Background(), chainID)
				return findErr == nil && state.Completed && !state.Failed && chainCalls.Load() == 2
			})

			batchID, err := q.Batch(
				queue.NewJob("workflow:batch"),
				queue.NewJob("workflow:batch"),
			).OnQueue(fixture.queue).Dispatch(context.Background())
			if err != nil {
				t.Fatalf("dispatch batch: %v", err)
			}
			waitForCanonicalWorkflow(t, "batch completion", func() bool {
				state, findErr := q.FindBatch(context.Background(), batchID)
				return findErr == nil && state.Completed && state.Processed == 2 && state.Failed == 0 && batchCalls.Load() == 2
			})

			runCanonicalWorkflowFailureContract(t, q, fixture.queue)
		})
	}
}

// runCanonicalWorkflowFailureContract proves terminal failures invoke catch and finally exactly once through the root API.
func runCanonicalWorkflowFailureContract(t *testing.T, q *queue.Queue, queueName string) {
	t.Helper()
	suffix := time.Now().UnixNano()
	chainSuccessType := fmt.Sprintf("workflow:chain:failure:first:%d", suffix)
	chainFailureType := fmt.Sprintf("workflow:chain:failure:second:%d", suffix)
	batchFailureType := fmt.Sprintf("workflow:batch:failure:%d", suffix)
	q.Register(chainSuccessType, func(context.Context, queue.Message) error { return nil })
	q.Register(chainFailureType, func(context.Context, queue.Message) error { return errors.New("chain step failed") })
	q.Register(batchFailureType, func(context.Context, queue.Message) error { return errors.New("batch member failed") })

	var chainCatchCalls atomic.Int32
	var chainFinallyCalls atomic.Int32
	chainID, _ := q.Chain(
		queue.NewJob(chainSuccessType),
		queue.NewJob(chainFailureType),
	).OnQueue(queueName).
		Catch(func(context.Context, queue.ChainState, error) error {
			chainCatchCalls.Add(1)
			return nil
		}).
		Finally(func(context.Context, queue.ChainState) error {
			chainFinallyCalls.Add(1)
			return nil
		}).
		Dispatch(context.Background())
	if chainID == "" {
		t.Fatal("failed chain did not return an ID")
	}
	waitForCanonicalWorkflow(t, "chain failure callbacks", func() bool {
		state, err := q.FindChain(context.Background(), chainID)
		return err == nil && state.Failed && state.Failure != "" && chainCatchCalls.Load() == 1 && chainFinallyCalls.Load() == 1
	})

	var batchCatchCalls atomic.Int32
	var batchFinallyCalls atomic.Int32
	batchID, _ := q.Batch(
		queue.NewJob(batchFailureType),
		queue.NewJob(batchFailureType),
	).OnQueue(queueName).
		Catch(func(context.Context, queue.BatchState, error) error {
			batchCatchCalls.Add(1)
			return nil
		}).
		Finally(func(context.Context, queue.BatchState) error {
			batchFinallyCalls.Add(1)
			return nil
		}).
		Dispatch(context.Background())
	if batchID == "" {
		t.Fatal("failed batch did not return an ID")
	}
	waitForCanonicalWorkflow(t, "batch failure callbacks", func() bool {
		state, err := q.FindBatch(context.Background(), batchID)
		return err == nil && state.Completed && state.Cancelled && batchCatchCalls.Load() == 1 && batchFinallyCalls.Load() == 1
	})
}

// waitForCanonicalWorkflow keeps local asynchronous runtimes and synchronous runtimes under one contract assertion.
func waitForCanonicalWorkflow(t *testing.T, scenario string, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s did not complete", scenario)
}
