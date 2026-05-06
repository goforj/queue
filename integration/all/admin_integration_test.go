//go:build integration

package all_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/goforj/queue"
	"github.com/goforj/queue/integration/testenv"
)

func TestQueueAdminIntegration_AllBackends(t *testing.T) {
	fixtures := []struct {
		name     string
		supports bool
		history  bool
		cfg      func(t *testing.T) any
	}{
		{name: testenv.BackendNull, supports: false, history: false, cfg: func(_ *testing.T) any { return nullCfg() }},
		{name: testenv.BackendSync, supports: false, history: true, cfg: func(_ *testing.T) any { return syncCfg() }},
		{name: testenv.BackendWorkerpool, supports: false, history: true, cfg: func(_ *testing.T) any { return workerpoolCfg() }},
		{name: testenv.BackendSQLite, supports: true, cfg: func(t *testing.T) any {
			return withDefaultQueue(sqliteCfg(fmt.Sprintf("%s/admin-%d.db", t.TempDir(), time.Now().UnixNano())), "admin_sqlite")
		}},
		{name: testenv.BackendRedis, supports: true, cfg: func(_ *testing.T) any {
			return withDefaultQueue(redisCfg(integrationRedis.addr), "admin_redis")
		}},
		{name: testenv.BackendMySQL, supports: true, cfg: func(_ *testing.T) any {
			return withDefaultQueue(mysqlCfg(mysqlDSN(integrationMySQL.addr)), "admin_mysql")
		}},
		{name: testenv.BackendPostgres, supports: true, cfg: func(_ *testing.T) any {
			return withDefaultQueue(postgresCfg(postgresDSN(integrationPostgres.addr)), "admin_postgres")
		}},
		{name: testenv.BackendNATS, supports: false, history: false, cfg: func(_ *testing.T) any {
			return withDefaultQueue(natsCfg(integrationNATS.url), "admin_nats")
		}},
		{name: testenv.BackendSQS, supports: false, history: false, cfg: func(_ *testing.T) any {
			return withDefaultQueue(sqsCfg(integrationSQS.region, integrationSQS.endpoint, integrationSQS.accessKey, integrationSQS.secretKey), "admin_sqs")
		}},
		{name: testenv.BackendRabbitMQ, supports: false, history: false, cfg: func(_ *testing.T) any {
			return withDefaultQueue(rabbitmqCfg(integrationRabbitMQ.url), "admin_rabbitmq")
		}},
	}

	for _, fx := range fixtures {
		fx := fx
		t.Run(fx.name, func(t *testing.T) {
			if !integrationBackendEnabled(fx.name) {
				t.Skipf("%s integration backend not selected", fx.name)
			}

			cfg := fx.cfg(t)
			q, err := newQueue(cfg)
			if err != nil {
				t.Fatalf("new queue failed: %v", err)
			}
			t.Cleanup(func() { _ = q.Shutdown(context.Background()) })

			if !fx.supports {
				assertAdminUnsupported(t, q, fx.history)
				return
			}
			if !queue.SupportsQueueAdmin(q) {
				t.Fatal("expected queue admin support")
			}

			queueName := uniqueQueueName("admin")
			jobType := "job:admin:" + fx.name

			if fx.name == testenv.BackendSQLite || fx.name == testenv.BackendMySQL || fx.name == testenv.BackendPostgres {
				// Ensure SQL schema exists via a short bootstrap runtime while
				// leaving the test runtime usable for admin-only assertions.
				bootstrap, err := newQueue(cfg)
				if err != nil {
					t.Fatalf("new bootstrap queue failed: %v", err)
				}
				if err := bootstrap.StartWorkers(context.Background()); err != nil {
					t.Fatalf("start workers for schema failed: %v", err)
				}
				if err := bootstrap.Shutdown(context.Background()); err != nil {
					t.Fatalf("stop workers after schema failed: %v", err)
				}
			}

			if _, err := q.Dispatch(queue.NewJob(jobType).Payload([]byte(`"admin"`)).OnQueue(queueName)); err != nil {
				t.Fatalf("dispatch admin test job failed: %v", err)
			}

			list, err := q.ListJobs(context.Background(), queue.ListJobsOptions{Queue: queueName, State: queue.JobStatePending, Page: 1, PageSize: 50})
			if err != nil {
				t.Fatalf("list jobs failed: %v", err)
			}
			if len(list.Jobs) == 0 {
				t.Fatalf("expected at least one pending job, got %+v", list)
			}

			jobID := list.Jobs[0].ID
			if err := q.CancelJob(context.Background(), jobID); err != nil {
				t.Fatalf("cancel job failed: %v", err)
			}

			archived, err := q.ListJobs(context.Background(), queue.ListJobsOptions{Queue: queueName, State: queue.JobStateArchived, Page: 1, PageSize: 50})
			if err != nil {
				t.Fatalf("list archived jobs failed: %v", err)
			}
			if len(archived.Jobs) == 0 {
				t.Fatalf("expected canceled job in archived state, got %+v", archived)
			}

			if err := q.RetryJob(context.Background(), queueName, jobID); err != nil {
				t.Fatalf("retry job failed: %v", err)
			}
			if err := q.DeleteJob(context.Background(), queueName, jobID); err != nil {
				t.Fatalf("delete job failed: %v", err)
			}
			if err := q.ClearQueue(context.Background(), queueName); err != nil {
				t.Fatalf("clear queue failed: %v", err)
			}

			history, err := q.History(context.Background(), queueName, queue.QueueHistoryHour)
			if err != nil {
				t.Fatalf("queue history failed: %v", err)
			}
			if fx.name == testenv.BackendRedis && len(history) == 0 {
				t.Fatalf("expected queue history points, got %+v", history)
			}
		})
	}
}

func assertAdminUnsupported(t *testing.T, q *queue.Queue, historySupported bool) {
	t.Helper()
	if _, err := q.ListJobs(context.Background(), queue.ListJobsOptions{Queue: "default", State: queue.JobStatePending}); !errors.Is(err, queue.ErrQueueAdminUnsupported) {
		t.Fatalf("expected admin unsupported for ListJobs, got %v", err)
	}
	if err := q.RetryJob(context.Background(), "default", "job-1"); !errors.Is(err, queue.ErrQueueAdminUnsupported) {
		t.Fatalf("expected admin unsupported for RetryJob, got %v", err)
	}
	if err := q.CancelJob(context.Background(), "job-1"); !errors.Is(err, queue.ErrQueueAdminUnsupported) {
		t.Fatalf("expected admin unsupported for CancelJob, got %v", err)
	}
	if err := q.DeleteJob(context.Background(), "default", "job-1"); !errors.Is(err, queue.ErrQueueAdminUnsupported) {
		t.Fatalf("expected admin unsupported for DeleteJob, got %v", err)
	}
	if err := q.ClearQueue(context.Background(), "default"); !errors.Is(err, queue.ErrQueueAdminUnsupported) {
		t.Fatalf("expected admin unsupported for ClearQueue, got %v", err)
	}
	if historySupported {
		if _, err := q.History(context.Background(), "default", queue.QueueHistoryHour); err != nil {
			t.Fatalf("expected history support, got %v", err)
		}
		return
	}
	if _, err := q.History(context.Background(), "default", queue.QueueHistoryHour); !errors.Is(err, queue.ErrQueueAdminUnsupported) {
		t.Fatalf("expected admin unsupported for History, got %v", err)
	}
}
