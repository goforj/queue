//go:build integration

package all_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	. "github.com/goforj/queue"
)

func TestIntegrationQueue_AllBackends(t *testing.T) {
	fx := []struct {
		name     string
		executes bool
		newQ     func(t *testing.T) (*Queue, string)
	}{
		{
			name:     "null",
			executes: false,
			newQ: func(t *testing.T) (*Queue, string) {
				q, err := newQueue(nullCfg())
				if err != nil {
					t.Fatalf("new null queue failed: %v", err)
				}
				return q, uniqueQueueName("queue-null")
			},
		},
		{
			name:     "sync",
			executes: true,
			newQ: func(t *testing.T) (*Queue, string) {
				q, err := newQueue(syncCfg())
				if err != nil {
					t.Fatalf("new sync queue failed: %v", err)
				}
				return q, uniqueQueueName("queue-sync")
			},
		},
		{
			name:     "workerpool",
			executes: true,
			newQ: func(t *testing.T) (*Queue, string) {
				q, err := newQueue(workerpoolCfg())
				if err != nil {
					t.Fatalf("new workerpool queue failed: %v", err)
				}
				return q, uniqueQueueName("queue-workerpool")
			},
		},
		{
			name:     "redis",
			executes: true,
			newQ: func(t *testing.T) (*Queue, string) {
				q, err := newQueue(withDefaultQueue(redisCfg(integrationRedis.addr), "default"))
				if err != nil {
					t.Fatalf("new redis queue failed: %v", err)
				}
				return q, "default"
			},
		},
		{
			name:     "mysql",
			executes: true,
			newQ: func(t *testing.T) (*Queue, string) {
				q, err := newQueue(mysqlCfg(mysqlDSN(integrationMySQL.addr)))
				if err != nil {
					t.Fatalf("new mysql queue failed: %v", err)
				}
				return q, uniqueQueueName("queue-mysql")
			},
		},
		{
			name:     "postgres",
			executes: true,
			newQ: func(t *testing.T) (*Queue, string) {
				q, err := newQueue(postgresCfg(postgresDSN(integrationPostgres.addr)))
				if err != nil {
					t.Fatalf("new postgres queue failed: %v", err)
				}
				return q, uniqueQueueName("queue-postgres")
			},
		},
		{
			name:     "sqlite",
			executes: true,
			newQ: func(t *testing.T) (*Queue, string) {
				q, err := newQueue(sqliteCfg(fmt.Sprintf("%s/queue-integration-%d.db", t.TempDir(), time.Now().UnixNano())))
				if err != nil {
					t.Fatalf("new sqlite queue failed: %v", err)
				}
				return q, uniqueQueueName("queue-sqlite")
			},
		},
		{
			name:     "nats",
			executes: true,
			newQ: func(t *testing.T) (*Queue, string) {
				q, err := newQueue(natsCfg(integrationNATS.url))
				if err != nil {
					t.Fatalf("new nats queue failed: %v", err)
				}
				return q, uniqueQueueName("queue-nats")
			},
		},
		{
			name:     "sqs",
			executes: true,
			newQ: func(t *testing.T) (*Queue, string) {
				physical := uniqueQueueName("queue-sqs")
				q, err := newQueue(withDefaultQueue(sqsCfg(integrationSQS.region, integrationSQS.endpoint, integrationSQS.accessKey, integrationSQS.secretKey), physical))
				if err != nil {
					t.Fatalf("new sqs queue failed: %v", err)
				}
				return q, physical
			},
		},
		{
			name:     "rabbitmq",
			executes: true,
			newQ: func(t *testing.T) (*Queue, string) {
				physical := uniqueQueueName("queue-rabbitmq")
				q, err := newQueue(withDefaultQueue(rabbitmqCfg(integrationRabbitMQ.url), physical))
				if err != nil {
					t.Fatalf("new rabbitmq queue failed: %v", err)
				}
				return q, physical
			},
		},
	}

	for _, backend := range fx {
		backend := backend
		t.Run(backend.name, func(t *testing.T) {
			if !integrationBackendEnabled(backend.name) {
				t.Skipf("%s integration backend not selected", backend.name)
			}
			t.Parallel()

			q, queueName := backend.newQ(t)
			q = q.Workers(4)
			if err := q.StartWorkers(context.Background()); err != nil {
				t.Fatalf("start workers failed: %v", err)
			}
			defer func() {
				shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				_ = q.Shutdown(shutdownCtx)
			}()

			if !backend.executes {
				testQueueWorkflowNullScenario(t, q, queueName)
				return
			}

			testQueueWorkflowDispatchScenario(t, q, queueName)
			testQueueWorkflowChainScenario(t, q, queueName)
			testQueueWorkflowBatchScenario(t, q, queueName)
			testQueueWorkflowPruneScenario(t, q, queueName)
		})
	}
}

func testQueueWorkflowNullScenario(t *testing.T, q *Queue, queueName string) {
	t.Helper()

	if _, err := q.Dispatch(NewJob("queue:null:dispatch").OnQueue(queueName)); err != nil {
		t.Fatalf("null scenario: dispatch failed: %v", err)
	}

	chainID, err := q.Chain(
		NewJob("queue:null:chain:step1"),
		NewJob("queue:null:chain:step2"),
	).OnQueue(queueName).Dispatch(context.Background())
	if err != nil {
		t.Fatalf("null scenario: chain dispatch failed: %v", err)
	}
	chain, err := q.FindChain(context.Background(), chainID)
	if err != nil {
		t.Fatalf("null scenario: find chain failed: %v", err)
	}
	if chain.Completed {
		t.Fatal("null scenario: expected chain to remain incomplete")
	}

	batchID, err := q.Batch(
		NewJob("queue:null:batch:step1"),
		NewJob("queue:null:batch:step2"),
	).OnQueue(queueName).Dispatch(context.Background())
	if err != nil {
		t.Fatalf("null scenario: batch dispatch failed: %v", err)
	}
	batch, err := q.FindBatch(context.Background(), batchID)
	if err != nil {
		t.Fatalf("null scenario: find batch failed: %v", err)
	}
	if batch.Completed {
		t.Fatal("null scenario: expected batch to remain incomplete")
	}
}

func testQueueWorkflowDispatchScenario(t *testing.T, q *Queue, queueName string) {
	t.Helper()

	type payload struct {
		URL string `json:"url"`
	}
	seen := make(chan string, 1)
	jobType := uniqueQueueJobType("queue:dispatch")

	q.Register(jobType, func(_ context.Context, j Message) error {
		var p payload
		if err := j.Bind(&p); err != nil {
			return err
		}
		select {
		case seen <- p.URL:
		default:
		}
		return nil
	})

	_, err := q.Dispatch(NewJob(jobType).Payload(payload{
		URL: "https://goforj.dev/health",
	}).OnQueue(queueName))
	if err != nil {
		t.Fatalf("dispatch scenario: dispatch failed: %v", err)
	}

	select {
	case got := <-seen:
		if got != "https://goforj.dev/health" {
			t.Fatalf("dispatch scenario: unexpected url %q", got)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("dispatch scenario: timed out waiting for handler")
	}
}

func testQueueWorkflowChainScenario(t *testing.T, q *Queue, queueName string) {
	t.Helper()

	var mu sync.Mutex
	order := make([]string, 0, 3)
	appendOrder := func(name string) {
		mu.Lock()
		order = append(order, name)
		mu.Unlock()
	}

	step1 := uniqueQueueJobType("queue:chain:step1")
	step2 := uniqueQueueJobType("queue:chain:step2")
	step3 := uniqueQueueJobType("queue:chain:step3")
	q.Register(step1, func(context.Context, Message) error { appendOrder("step1"); return nil })
	q.Register(step2, func(context.Context, Message) error { appendOrder("step2"); return nil })
	q.Register(step3, func(context.Context, Message) error { appendOrder("step3"); return nil })

	chainID, err := q.Chain(
		NewJob(step1),
		NewJob(step2),
		NewJob(step3),
	).OnQueue(queueName).Dispatch(context.Background())
	if err != nil {
		t.Fatalf("chain scenario: dispatch failed: %v", err)
	}

	waitForQueueWorkflow(t, 20*time.Second, "chain completed", func() bool {
		st, err := q.FindChain(context.Background(), chainID)
		return err == nil && st.Completed
	})

	mu.Lock()
	got := append([]string(nil), order...)
	mu.Unlock()
	if len(got) != 3 || got[0] != "step1" || got[1] != "step2" || got[2] != "step3" {
		t.Fatalf("chain scenario: unexpected execution order %v", got)
	}
}

func testQueueWorkflowBatchScenario(t *testing.T, q *Queue, queueName string) {
	t.Helper()

	type batchPayload struct {
		ID int `json:"id"`
	}
	var processed atomic.Int32
	jobType := uniqueQueueJobType("queue:batch:work")
	q.Register(jobType, func(_ context.Context, j Message) error {
		var p batchPayload
		if err := j.Bind(&p); err != nil {
			return err
		}
		processed.Add(1)
		return nil
	})

	batchID, err := q.Batch(
		NewJob(jobType).Payload(batchPayload{ID: 1}),
		NewJob(jobType).Payload(batchPayload{ID: 2}),
		NewJob(jobType).Payload(batchPayload{ID: 3}),
	).OnQueue(queueName).Dispatch(context.Background())
	if err != nil {
		t.Fatalf("batch scenario: dispatch failed: %v", err)
	}

	waitForQueueWorkflow(t, 20*time.Second, "batch completed", func() bool {
		st, err := q.FindBatch(context.Background(), batchID)
		return err == nil && st.Completed && st.Processed == 3 && st.Pending == 0
	})

	if got := processed.Load(); got != 3 {
		t.Fatalf("batch scenario: expected 3 processed jobs, got %d", got)
	}
}

func testQueueWorkflowPruneScenario(t *testing.T, q *Queue, queueName string) {
	t.Helper()

	stepType := uniqueQueueJobType("queue:prune:step")
	q.Register(stepType, func(context.Context, Message) error { return nil })

	chainID, err := q.Chain(NewJob(stepType), NewJob(stepType)).OnQueue(queueName).Dispatch(context.Background())
	if err != nil {
		t.Fatalf("prune scenario: chain dispatch failed: %v", err)
	}
	batchID, err := q.Batch(NewJob(stepType), NewJob(stepType)).OnQueue(queueName).Dispatch(context.Background())
	if err != nil {
		t.Fatalf("prune scenario: batch dispatch failed: %v", err)
	}

	waitForQueueWorkflow(t, 20*time.Second, "prune chain completed", func() bool {
		st, err := q.FindChain(context.Background(), chainID)
		return err == nil && st.Completed
	})
	waitForQueueWorkflow(t, 20*time.Second, "prune batch completed", func() bool {
		st, err := q.FindBatch(context.Background(), batchID)
		return err == nil && st.Completed
	})

	if err := q.Prune(context.Background(), time.Now().Add(1*time.Minute)); err != nil {
		t.Fatalf("prune scenario: prune failed: %v", err)
	}

	if _, err := q.FindChain(context.Background(), chainID); !errors.Is(err, ErrWorkflowNotFound) {
		t.Fatalf("prune scenario: expected chain not found after prune, got %v", err)
	}
	if _, err := q.FindBatch(context.Background(), batchID); !errors.Is(err, ErrWorkflowNotFound) {
		t.Fatalf("prune scenario: expected batch not found after prune, got %v", err)
	}
}

func waitForQueueWorkflow(t *testing.T, timeout time.Duration, label string, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if ready() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", label)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func uniqueQueueJobType(prefix string) string {
	return fmt.Sprintf("%s:%d", prefix, time.Now().UnixNano())
}
