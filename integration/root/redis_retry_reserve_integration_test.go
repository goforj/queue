//go:build integration

package root_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/goforj/queue"
	"github.com/goforj/queue/busruntime"
	"github.com/goforj/queue/integration/testenv"
)

// TestRedisIntegration_FinalUncommittedRedeliversSameAttempt proves the reserved transport slot against the real Asynq processor.
func TestRedisIntegration_FinalUncommittedRedeliversSameAttempt(t *testing.T) {
	if !integrationBackendEnabled(testenv.BackendRedis) {
		t.Skip("redis integration backend not selected")
	}
	ensureRedis(t)
	queueName := uniqueQueueName("redis-uncommitted-final")
	runtime, err := newQueueRuntime(withDefaultQueue(redisCfg(integrationRedis.addr), queueName))
	if err != nil {
		t.Fatalf("new redis runtime: %v", err)
	}
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = runtime.Shutdown(shutdownCtx)
	})

	jobType := queueName + ":job"
	done := make(chan struct{})
	var (
		mu       sync.Mutex
		attempts []int
	)
	runtime.Register(jobType, func(_ context.Context, job queue.Job) error {
		mu.Lock()
		attempts = append(attempts, queue.DriverOptions(job).Attempt)
		call := len(attempts)
		mu.Unlock()
		if call == 1 {
			return busruntime.Uncommitted(errors.New("workflow store unavailable"))
		}
		close(done)
		return nil
	})
	if err := runtime.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start redis workers: %v", err)
	}
	if err := runtime.Dispatch(queue.NewJob(jobType).OnQueue(queueName).Retry(0)); err != nil {
		t.Fatalf("dispatch zero-retry job: %v", err)
	}

	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("uncommitted final attempt was not redelivered")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(attempts) != 2 || attempts[0] != 0 || attempts[1] != 0 {
		t.Fatalf("application attempts = %v, want [0 0]", attempts)
	}
}
