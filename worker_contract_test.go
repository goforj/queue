package queue

import (
	"context"
	"fmt"
	"testing"
	"time"
)

type workerContractFactory struct {
	name  string
	setup func(t *testing.T) (Worker, func(task Task) error)
}

func runWorkerContractSuite(t *testing.T, factory workerContractFactory) {
	t.Helper()

	t.Run("lifecycle_start_shutdown_idempotent", func(t *testing.T) {
		worker, _ := factory.setup(t)
		if err := worker.Start(); err != nil {
			t.Fatalf("first start failed: %v", err)
		}
		if err := worker.Start(); err != nil {
			t.Fatalf("second start failed: %v", err)
		}
		if err := worker.Shutdown(); err != nil {
			t.Fatalf("first shutdown failed: %v", err)
		}
		if err := worker.Shutdown(); err != nil {
			t.Fatalf("second shutdown failed: %v", err)
		}
	})

	t.Run("register_and_process", func(t *testing.T) {
		worker, enqueue := factory.setup(t)
		type payload struct {
			ID int `json:"id"`
		}
		seen := make(chan payload, 1)
		taskType := fmt.Sprintf("job:worker:contract:%d", time.Now().UnixNano())
		worker.Register(taskType, func(_ context.Context, task Task) error {
			var in payload
			if err := task.Bind(&in); err != nil {
				return err
			}
			seen <- in
			return nil
		})
		if err := worker.Start(); err != nil {
			t.Fatalf("worker start failed: %v", err)
		}
		t.Cleanup(func() { _ = worker.Shutdown() })

		want := payload{ID: 77}
		if err := enqueue(NewTask(taskType).Payload(want).OnQueue("default")); err != nil {
			t.Fatalf("enqueue failed: %v", err)
		}
		select {
		case got := <-seen:
			if got != want {
				t.Fatalf("payload mismatch: got %+v want %+v", got, want)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("worker did not process task")
		}
	})
}

func TestWorkerContractCoverage_AllDriversAccountedFor(t *testing.T) {
	declared := declaredDriversFromSource(t)
	accounted := map[Driver]string{
		DriverSync:       "local worker contract suite",
		DriverWorkerpool: "local worker contract suite",
		DriverDatabase:   "local/integration worker contract suites",
		DriverRedis:      "integration worker contract suite",
	}
	for _, d := range declared {
		if _, ok := accounted[d]; !ok {
			t.Fatalf("driver %q is not mapped to a worker contract suite; update worker option coverage tests", d)
		}
	}
}

func TestWorkerContract_Local(t *testing.T) {
	factories := []workerContractFactory{
		{
			name: "sync",
			setup: func(t *testing.T) (Worker, func(task Task) error) {
				worker, err := NewWorker(WorkerConfig{Driver: DriverSync})
				if err != nil {
					t.Fatalf("new sync worker failed: %v", err)
				}
				return worker, enqueueViaAdapter(t, worker)
			},
		},
		{
			name: "workerpool",
			setup: func(t *testing.T) (Worker, func(task Task) error) {
				worker, err := NewWorker(WorkerConfig{
					Driver:        DriverWorkerpool,
					Workers:       1,
					QueueCapacity: 4,
				})
				if err != nil {
					t.Fatalf("new workerpool worker failed: %v", err)
				}
				return worker, enqueueViaAdapter(t, worker)
			},
		},
		{
			name: "database-sqlite",
			setup: func(t *testing.T) (Worker, func(task Task) error) {
				dsn := fmt.Sprintf("%s/worker-contract-%d.db", t.TempDir(), time.Now().UnixNano())
				worker, err := NewWorker(WorkerConfig{
					Driver:         DriverDatabase,
					DatabaseDriver: "sqlite",
					DatabaseDSN:    dsn,
					Workers:        1,
					PollInterval:   10 * time.Millisecond,
				})
				if err != nil {
					t.Fatalf("new sqlite worker failed: %v", err)
				}
				return worker, enqueueViaAdapter(t, worker)
			},
		},
	}

	for _, factory := range factories {
		factory := factory
		t.Run(factory.name, func(t *testing.T) {
			runWorkerContractSuite(t, factory)
		})
	}
}

func enqueueViaAdapter(t *testing.T, worker Worker) func(task Task) error {
	t.Helper()
	adapter, ok := worker.(*queueWorkerAdapter)
	if !ok {
		t.Fatalf("worker %T is not queueWorkerAdapter", worker)
	}
	return func(task Task) error {
		return adapter.q.Enqueue(context.Background(), task)
	}
}
