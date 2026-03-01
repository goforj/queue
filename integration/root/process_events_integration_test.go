//go:build integration

package root_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goforj/queue"
	"github.com/goforj/queue/integration/testenv"
)

type processEventRecorder struct {
	mu     sync.Mutex
	events []queue.Event
}

func (r *processEventRecorder) Observe(event queue.Event) {
	switch event.Kind {
	case queue.EventProcessStarted, queue.EventProcessSucceeded, queue.EventProcessFailed:
		r.mu.Lock()
		r.events = append(r.events, event)
		r.mu.Unlock()
	}
}

func (r *processEventRecorder) count(kind queue.EventKind) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	count := 0
	for _, event := range r.events {
		if event.Kind == kind {
			count++
		}
	}
	return count
}

func (r *processEventRecorder) has(kind queue.EventKind, predicate func(queue.Event) bool) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, event := range r.events {
		if event.Kind == kind && predicate(event) {
			return true
		}
	}
	return false
}

func TestObservabilityIntegration_ProcessEvents_AllBackends(t *testing.T) {
	fixtures := []struct {
		name     string
		queue    string
		workers  int
		newQueue func(t *testing.T, observer queue.Observer) QueueRuntime
	}{
		{
			name:    testenv.BackendRedis,
			queue:   "default",
			workers: 2,
			newQueue: func(t *testing.T, observer queue.Observer) QueueRuntime {
				ensureRedis(t)
				q, err := newQueueRuntime(withObserver(redisCfg(integrationRedis.addr), observer))
				if err != nil {
					t.Fatalf("new redis queue failed: %v", err)
				}
				return q
			},
		},
		{
			name:    testenv.BackendMySQL,
			queue:   "obs_events_mysql",
			workers: 2,
			newQueue: func(t *testing.T, observer queue.Observer) QueueRuntime {
				ensureMySQLDB(t)
				q, err := newQueueRuntime(withObserver(mysqlCfg(mysqlDSN(integrationMySQL.addr)), observer))
				if err != nil {
					t.Fatalf("new mysql queue failed: %v", err)
				}
				return q
			},
		},
		{
			name:    testenv.BackendPostgres,
			queue:   "obs_events_postgres",
			workers: 2,
			newQueue: func(t *testing.T, observer queue.Observer) QueueRuntime {
				ensurePostgresDB(t)
				q, err := newQueueRuntime(withObserver(postgresCfg(postgresDSN(integrationPostgres.addr)), observer))
				if err != nil {
					t.Fatalf("new postgres queue failed: %v", err)
				}
				return q
			},
		},
		{
			name:    testenv.BackendSQLite,
			queue:   "obs_events_sqlite",
			workers: 2,
			newQueue: func(t *testing.T, observer queue.Observer) QueueRuntime {
				dsn := fmt.Sprintf("%s/obs-events-%d.db", t.TempDir(), time.Now().UnixNano())
				q, err := newQueueRuntime(withObserver(sqliteCfg(dsn), observer))
				if err != nil {
					t.Fatalf("new sqlite queue failed: %v", err)
				}
				return q
			},
		},
		{
			name:    testenv.BackendNATS,
			queue:   "obs_events_nats",
			workers: 2,
			newQueue: func(t *testing.T, observer queue.Observer) QueueRuntime {
				ensureNATS(t)
				q, err := newQueueRuntime(withObserver(natsCfg(integrationNATS.url), observer))
				if err != nil {
					t.Fatalf("new nats queue failed: %v", err)
				}
				return q
			},
		},
		{
			name:    testenv.BackendSQS,
			queue:   "obs_events_sqs",
			workers: 2,
			newQueue: func(t *testing.T, observer queue.Observer) QueueRuntime {
				ensureSQS(t)
				q, err := newQueueRuntime(withObserver(
					withDefaultQueue(
						sqsCfg(integrationSQS.region, integrationSQS.endpoint, integrationSQS.accessKey, integrationSQS.secretKey),
						"obs_events_sqs",
					),
					observer,
				))
				if err != nil {
					t.Fatalf("new sqs queue failed: %v", err)
				}
				return q
			},
		},
		{
			name:    testenv.BackendRabbitMQ,
			queue:   "obs_events_rabbitmq",
			workers: 2,
			newQueue: func(t *testing.T, observer queue.Observer) QueueRuntime {
				ensureRabbitMQ(t)
				q, err := newQueueRuntime(withObserver(withDefaultQueue(rabbitmqCfg(integrationRabbitMQ.url), "obs_events_rabbitmq"), observer))
				if err != nil {
					t.Fatalf("new rabbitmq queue failed: %v", err)
				}
				return q
			},
		},
	}

	for _, fx := range fixtures {
		fx := fx
		t.Run(fx.name, func(t *testing.T) {
			if !integrationBackendEnabled(fx.name) {
				t.Skipf("%s integration backend not selected", fx.name)
			}

			recorder := &processEventRecorder{}
			q := fx.newQueue(t, recorder)
			t.Cleanup(func() { _ = q.Shutdown(context.Background()) })

			okType := "job:obs:events:ok:" + fx.name
			failType := "job:obs:events:fail:" + fx.name
			okDone := make(chan struct{}, 1)
			var failedCalls atomic.Int64

			q.Register(okType, func(_ context.Context, _ queue.Job) error {
				select {
				case okDone <- struct{}{}:
				default:
				}
				return nil
			})
			q.Register(failType, func(_ context.Context, _ queue.Job) error {
				failedCalls.Add(1)
				return errors.New("events boom")
			})

			requireScenarioNoErr(t, "start_worker", withWorkers(q, fx.workers).StartWorkers(context.Background()))

			requireScenarioNoErr(t, "dispatch_success", q.DispatchCtx(context.Background(),
				queue.NewJob(okType).Payload(scenarioPayload{ID: 1, Name: "events-ok"}).OnQueue(fx.queue).Retry(2),
			))
			select {
			case <-okDone:
			case <-time.After(12 * time.Second):
				t.Fatal("timed out waiting for success job processing")
			}

			requireScenarioNoErr(t, "dispatch_fail", q.DispatchCtx(context.Background(),
				queue.NewJob(failType).Payload(scenarioPayload{ID: 2, Name: "events-fail"}).OnQueue(fx.queue).Retry(0),
			))
			waitForObservabilityScenario(t, "failed_job_attempt", 12*time.Second, func() bool {
				return failedCalls.Load() >= 1
			})

			waitForObservabilityScenario(t, "process_event_kinds", 12*time.Second, func() bool {
				return recorder.count(queue.EventProcessStarted) >= 2 &&
					recorder.count(queue.EventProcessSucceeded) >= 1 &&
					recorder.count(queue.EventProcessFailed) >= 1
			})

			requireScenarioTrue(t, "process_started_queue",
				recorder.has(queue.EventProcessStarted, func(event queue.Event) bool {
					return event.Queue == fx.queue && event.JobType == okType
				}),
				"expected process_started with queue=%q type=%q", fx.queue, okType,
			)
			requireScenarioTrue(t, "process_succeeded_fields",
				recorder.has(queue.EventProcessSucceeded, func(event queue.Event) bool {
					return event.Queue == fx.queue && event.JobType == okType && event.MaxRetry >= 2 && event.Duration >= 0
				}),
				"expected process_succeeded with queue=%q type=%q max_retry>=2", fx.queue, okType,
			)
			requireScenarioTrue(t, "process_failed_fields",
				recorder.has(queue.EventProcessFailed, func(event queue.Event) bool {
					return event.Queue == fx.queue && event.JobType == failType && event.MaxRetry == 0 && event.Err != nil && event.Duration >= 0
				}),
				"expected process_failed with queue=%q type=%q max_retry=0", fx.queue, failType,
			)
		})
	}
}
