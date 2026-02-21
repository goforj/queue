//go:build integration

package queue

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

func TestObservabilityIntegration_AllBackends(t *testing.T) {
	fixtures := []struct {
		name     string
		queue    string
		native   bool
		workers  int
		newQueue func(t *testing.T, collector *StatsCollector) Queue
	}{
		{
			name:    "redis",
			queue:   "default",
			native:  true,
			workers: 2,
			newQueue: func(t *testing.T, collector *StatsCollector) Queue {
				q, err := New(Config{
					Driver:    DriverRedis,
					RedisAddr: integrationRedis.addr,
					Observer:  collector,
				})
				if err != nil {
					t.Fatalf("new redis queue failed: %v", err)
				}
				return q
			},
		},
		{
			name:    "mysql",
			queue:   "obs_mysql",
			native:  true,
			workers: 2,
			newQueue: func(t *testing.T, collector *StatsCollector) Queue {
				q, err := New(Config{
					Driver:         DriverDatabase,
					DatabaseDriver: "mysql",
					DatabaseDSN:    fmt.Sprintf("queue:queue@tcp(%s)/queue_test?parseTime=true", integrationMySQL.addr),
					Observer:       collector,
				})
				if err != nil {
					t.Fatalf("new mysql queue failed: %v", err)
				}
				return q
			},
		},
		{
			name:    "postgres",
			queue:   "obs_postgres",
			native:  true,
			workers: 2,
			newQueue: func(t *testing.T, collector *StatsCollector) Queue {
				q, err := New(Config{
					Driver:         DriverDatabase,
					DatabaseDriver: "pgx",
					DatabaseDSN:    fmt.Sprintf("postgres://queue:queue@%s/queue_test?sslmode=disable", integrationPostgres.addr),
					Observer:       collector,
				})
				if err != nil {
					t.Fatalf("new postgres queue failed: %v", err)
				}
				return q
			},
		},
		{
			name:    "sqlite",
			queue:   "obs_sqlite",
			native:  true,
			workers: 2,
			newQueue: func(t *testing.T, collector *StatsCollector) Queue {
				dsn := fmt.Sprintf("%s/obs-%d.db", t.TempDir(), time.Now().UnixNano())
				q, err := New(Config{
					Driver:         DriverDatabase,
					DatabaseDriver: "sqlite",
					DatabaseDSN:    dsn,
					Observer:       collector,
				})
				if err != nil {
					t.Fatalf("new sqlite queue failed: %v", err)
				}
				return q
			},
		},
		{
			name:    "nats",
			queue:   "obs_nats",
			workers: 2,
			newQueue: func(t *testing.T, collector *StatsCollector) Queue {
				q, err := New(Config{
					Driver:   DriverNATS,
					NATSURL:  integrationNATS.url,
					Observer: collector,
				})
				if err != nil {
					t.Fatalf("new nats queue failed: %v", err)
				}
				return q
			},
		},
		{
			name:    "sqs",
			queue:   "obs_sqs",
			workers: 2,
			newQueue: func(t *testing.T, collector *StatsCollector) Queue {
				q, err := New(Config{
					Driver:       DriverSQS,
					DefaultQueue: "obs_sqs",
					SQSEndpoint:  integrationSQS.endpoint,
					SQSRegion:    integrationSQS.region,
					SQSAccessKey: integrationSQS.accessKey,
					SQSSecretKey: integrationSQS.secretKey,
					Observer:     collector,
				})
				if err != nil {
					t.Fatalf("new sqs queue failed: %v", err)
				}
				return q
			},
		},
		{
			name:    "rabbitmq",
			queue:   "obs_rabbitmq",
			workers: 2,
			newQueue: func(t *testing.T, collector *StatsCollector) Queue {
				q, err := New(Config{
					Driver:       DriverRabbitMQ,
					DefaultQueue: "obs_rabbitmq",
					RabbitMQURL:  integrationRabbitMQ.url,
					Observer:     collector,
				})
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

			collector := NewStatsCollector()
			q := fx.newQueue(t, collector)
			t.Cleanup(func() { _ = q.Shutdown(context.Background()) })

			okType := "job:obs:ok:" + fx.name
			failType := "job:obs:fail:" + fx.name
			okDone := make(chan struct{}, 1)
			var failedCalls atomic.Int64

			t.Run("scenario_register_handlers", func(t *testing.T) {
				q.Register(okType, func(_ context.Context, _ Job) error {
					select {
					case okDone <- struct{}{}:
					default:
					}
					return nil
				})
				q.Register(failType, func(_ context.Context, _ Job) error {
					failedCalls.Add(1)
					return errors.New("obs boom")
				})
			})

			t.Run("scenario_start_worker", func(t *testing.T) {
				requireScenarioNoErr(t, "start_worker", q.Workers(fx.workers).StartWorkers(context.Background()))
			})

			t.Run("scenario_dispatch_success", func(t *testing.T) {
				okJob := NewJob(okType).
					Payload(scenarioPayload{ID: 1, Name: "obs-ok"}).
					OnQueue(fx.queue)
				requireScenarioNoErr(t, "dispatch_success", q.DispatchCtx(context.Background(), okJob))
				select {
				case <-okDone:
				case <-time.After(12 * time.Second):
					t.Fatal("timed out waiting for observed success job processing")
				}
			})

			t.Run("scenario_dispatch_retry_archive", func(t *testing.T) {
				failJob := NewJob(failType).
					Payload(scenarioPayload{ID: 2, Name: "obs-fail"}).
					OnQueue(fx.queue).
					Retry(0)
				requireScenarioNoErr(t, "dispatch_retry_archive", q.DispatchCtx(context.Background(), failJob))
				waitForObservabilityScenario(t, "retry_archive_attempts", 12*time.Second, func() bool {
					return failedCalls.Load() >= 1
				})
			})

			if fx.name != "redis" {
				t.Run("scenario_dispatch_retried_job", func(t *testing.T) {
					retryJob := NewJob(failType).
						Payload(scenarioPayload{ID: 3, Name: "obs-retry"}).
						OnQueue(fx.queue).
						Retry(1).
						Backoff(20 * time.Millisecond)
					requireScenarioNoErr(t, "dispatch_retried_job", q.DispatchCtx(context.Background(), retryJob))
					waitForObservabilityScenario(t, "retried_job_attempts", 12*time.Second, func() bool {
						return failedCalls.Load() >= 3
					})
				})
			}

			t.Run("scenario_assert_collector_values", func(t *testing.T) {
				var counters QueueCounters
				waitForObservabilityScenario(t, "collector_counters_available", 10*time.Second, func() bool {
					snapshot := collector.Snapshot()
					var ok bool
					counters, ok = snapshot.Queue(fx.queue)
					return ok
				})
				requireScenarioTrue(t, "collector_processed", counters.Processed >= 1, "processed=%d expected>=1", counters.Processed)
				requireScenarioTrue(t, "collector_failed", counters.Failed >= 1, "failed=%d expected>=1", counters.Failed)
				requireScenarioTrue(t, "collector_archived", counters.Archived >= 1, "archived=%d expected>=1", counters.Archived)
				if fx.name != "redis" {
					requireScenarioTrue(t, "collector_retried", counters.Retry >= 1, "retry=%d expected>=1", counters.Retry)
				}
				waitForObservabilityScenario(t, "collector_drained", 8*time.Second, func() bool {
					snapshot := collector.Snapshot()
					return snapshot.Pending(fx.queue) == 0 && snapshot.Active(fx.queue) == 0
				})
				snapshot := collector.Snapshot()
				throughput, ok := snapshot.Throughput(fx.queue)
				requireScenarioTrue(t, "collector_throughput_present", ok, "throughput missing for queue=%q", fx.queue)
				requireScenarioTrue(t, "collector_hour_processed", throughput.Hour.Processed >= 1, "hour_processed=%d expected>=1", throughput.Hour.Processed)
				requireScenarioTrue(t, "collector_hour_failed", throughput.Hour.Failed >= 1, "hour_failed=%d expected>=1", throughput.Hour.Failed)
				requireScenarioTrue(t, "collector_getter_processed", snapshot.Processed(fx.queue) == counters.Processed, "getter_processed=%d counters_processed=%d", snapshot.Processed(fx.queue), counters.Processed)
				requireScenarioTrue(t, "collector_getter_failed", snapshot.Failed(fx.queue) == counters.Failed, "getter_failed=%d counters_failed=%d", snapshot.Failed(fx.queue), counters.Failed)
				if fx.name != "redis" {
					requireScenarioTrue(t, "collector_getter_retry", snapshot.RetryCount(fx.queue) == counters.Retry, "getter_retry=%d counters_retry=%d", snapshot.RetryCount(fx.queue), counters.Retry)
				}
			})

			t.Run("scenario_assert_snapshotqueue", func(t *testing.T) {
				snapFromQueue, err := SnapshotQueue(context.Background(), q, collector)
				requireScenarioNoErr(t, "snapshot_queue", err)
				nativeCounters, queueOK := snapFromQueue.Queue(fx.queue)
				requireScenarioTrue(t, "snapshot_queue_present", queueOK, "queue=%q not found in snapshot", fx.queue)
				if fx.native {
					switch fx.name {
					case "redis":
						requireScenarioTrue(t, "snapshot_native_redis_processed", nativeCounters.Processed >= 1, "processed=%d expected>=1", nativeCounters.Processed)
						requireScenarioTrue(t, "snapshot_native_redis_failed", nativeCounters.Failed >= 1, "failed=%d expected>=1", nativeCounters.Failed)
					case "mysql", "postgres", "sqlite":
						waitForObservabilityScenario(t, "snapshot_native_db_drained", 8*time.Second, func() bool {
							latest, latestErr := SnapshotQueue(context.Background(), q, collector)
							if latestErr != nil {
								return false
							}
							counters, ok := latest.Queue(fx.queue)
							if !ok {
								return false
							}
							nativeCounters = counters
							return counters.Pending == 0 && counters.Active == 0
						})
					}
				}
			})
		})
	}
}

func TestObservabilityIntegration_RedisPauseResume(t *testing.T) {
	if !integrationBackendEnabled("redis") {
		t.Skip("redis integration backend not selected")
	}
	collector := NewStatsCollector()
	q, err := New(Config{
		Driver:    DriverRedis,
		RedisAddr: integrationRedis.addr,
		Observer:  collector,
	})
	if err != nil {
		t.Fatalf("new redis queue failed: %v", err)
	}
	defer q.Shutdown(context.Background())

	queueName := uniqueQueueName("obs-pause")
	if err := PauseQueue(context.Background(), q, queueName); err != nil {
		t.Fatalf("pause queue failed: %v", err)
	}
	if err := ResumeQueue(context.Background(), q, queueName); err != nil {
		t.Fatalf("resume queue failed: %v", err)
	}

	snapshot := collector.Snapshot()
	counters, ok := snapshot.Queue(queueName)
	if !ok {
		t.Fatalf("expected queue counters for %q", queueName)
	}
	if counters.Paused != 0 {
		t.Fatalf("expected paused counter to return to 0, got %d", counters.Paused)
	}
}

func TestSnapshotQueue_NoProviderNoCollector(t *testing.T) {
	_, snapshotErr := SnapshotQueue(context.Background(), noStatsQueue{}, nil)
	if snapshotErr == nil {
		t.Fatal("expected snapshot error when provider and collector are unavailable")
	}
	if snapshotErr.Error() == "" {
		t.Fatalf("unexpected snapshot error: %v", snapshotErr)
	}
}

func TestObservabilityIntegration_PauseResumeSupport_AllBackends(t *testing.T) {
	fixtures := []struct {
		name     string
		supports bool
		newQueue func(t *testing.T, collector *StatsCollector) Queue
	}{
		{
			name:     "redis",
			supports: true,
			newQueue: func(t *testing.T, collector *StatsCollector) Queue {
				q, err := New(Config{
					Driver:    DriverRedis,
					RedisAddr: integrationRedis.addr,
					Observer:  collector,
				})
				if err != nil {
					t.Fatalf("new redis queue failed: %v", err)
				}
				return q
			},
		},
		{
			name:     "mysql",
			supports: false,
			newQueue: func(t *testing.T, collector *StatsCollector) Queue {
				q, err := New(Config{
					Driver:         DriverDatabase,
					DatabaseDriver: "mysql",
					DatabaseDSN:    fmt.Sprintf("queue:queue@tcp(%s)/queue_test?parseTime=true", integrationMySQL.addr),
					Observer:       collector,
				})
				if err != nil {
					t.Fatalf("new mysql queue failed: %v", err)
				}
				return q
			},
		},
		{
			name:     "postgres",
			supports: false,
			newQueue: func(t *testing.T, collector *StatsCollector) Queue {
				q, err := New(Config{
					Driver:         DriverDatabase,
					DatabaseDriver: "pgx",
					DatabaseDSN:    fmt.Sprintf("postgres://queue:queue@%s/queue_test?sslmode=disable", integrationPostgres.addr),
					Observer:       collector,
				})
				if err != nil {
					t.Fatalf("new postgres queue failed: %v", err)
				}
				return q
			},
		},
		{
			name:     "sqlite",
			supports: false,
			newQueue: func(t *testing.T, collector *StatsCollector) Queue {
				q, err := New(Config{
					Driver:         DriverDatabase,
					DatabaseDriver: "sqlite",
					DatabaseDSN:    fmt.Sprintf("%s/pause-%d.db", t.TempDir(), time.Now().UnixNano()),
					Observer:       collector,
				})
				if err != nil {
					t.Fatalf("new sqlite queue failed: %v", err)
				}
				return q
			},
		},
		{
			name:     "nats",
			supports: false,
			newQueue: func(t *testing.T, collector *StatsCollector) Queue {
				q, err := New(Config{
					Driver:   DriverNATS,
					NATSURL:  integrationNATS.url,
					Observer: collector,
				})
				if err != nil {
					t.Fatalf("new nats queue failed: %v", err)
				}
				return q
			},
		},
		{
			name:     "sqs",
			supports: false,
			newQueue: func(t *testing.T, collector *StatsCollector) Queue {
				q, err := New(Config{
					Driver:       DriverSQS,
					SQSEndpoint:  integrationSQS.endpoint,
					SQSRegion:    integrationSQS.region,
					SQSAccessKey: integrationSQS.accessKey,
					SQSSecretKey: integrationSQS.secretKey,
					Observer:     collector,
				})
				if err != nil {
					t.Fatalf("new sqs queue failed: %v", err)
				}
				return q
			},
		},
		{
			name:     "rabbitmq",
			supports: false,
			newQueue: func(t *testing.T, collector *StatsCollector) Queue {
				q, err := New(Config{
					Driver:      DriverRabbitMQ,
					RabbitMQURL: integrationRabbitMQ.url,
					Observer:    collector,
				})
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
			collector := NewStatsCollector()
			q := fx.newQueue(t, collector)
			defer q.Shutdown(context.Background())

			queueName := uniqueQueueName("obs-pause-" + fx.name)
			pauseErr := PauseQueue(context.Background(), q, queueName)
			resumeErr := ResumeQueue(context.Background(), q, queueName)

			if fx.supports {
				requireScenarioNoErr(t, "pause_supported", pauseErr)
				requireScenarioNoErr(t, "resume_supported", resumeErr)
				snapshot := collector.Snapshot()
				counters, ok := snapshot.Queue(queueName)
				requireScenarioTrue(t, "pause_events_observed", ok, "queue %q not found in collector snapshot", queueName)
				requireScenarioTrue(t, "pause_back_to_zero", counters.Paused == 0, "paused=%d expected=0", counters.Paused)
				return
			}

			requireScenarioTrue(t, "pause_unsupported", errors.Is(pauseErr, ErrPauseUnsupported), "expected ErrPauseUnsupported, got %v", pauseErr)
			requireScenarioTrue(t, "resume_unsupported", errors.Is(resumeErr, ErrPauseUnsupported), "expected ErrPauseUnsupported, got %v", resumeErr)
		})
	}
}

func waitForObservabilityScenario(t *testing.T, scenario string, timeout time.Duration, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	requireScenarioTrue(t, scenario, false, "timed out after %s", timeout)
}

type noStatsQueue struct{}

func (noStatsQueue) Driver() Driver                     { return DriverSync }
func (noStatsQueue) StartWorkers(context.Context) error { return nil }
func (noStatsQueue) Workers(int) Queue                  { return noStatsQueue{} }
func (noStatsQueue) Shutdown(context.Context) error     { return nil }
func (noStatsQueue) Register(string, Handler)           {}
func (noStatsQueue) Dispatch(any) error                 { return nil }
func (noStatsQueue) DispatchCtx(context.Context, any) error {
	return nil
}
