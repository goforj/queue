//go:build integration

package queue

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestWorkerContract_RedisIntegration(t *testing.T) {
	if !integrationBackendEnabled("redis") {
		t.Skip("redis integration backend not selected")
	}
	factory := workerContractFactory{
		name: "redis",
		setup: func(t *testing.T) (Worker, func(task Task) error) {
			worker, err := NewWorker(WorkerConfig{
				Driver:    DriverRedis,
				RedisAddr: integrationRedis.addr,
				Workers:   1,
			})
			if err != nil {
				t.Fatalf("new redis worker failed: %v", err)
			}
			producer, err := New(Config{
				Driver:    DriverRedis,
				RedisAddr: integrationRedis.addr,
			})
			if err != nil {
				t.Fatalf("new redis queue failed: %v", err)
			}
			t.Cleanup(func() { _ = producer.Shutdown(context.Background()) })
			return worker, func(task Task) error {
				return producer.Enqueue(context.Background(), task)
			}
		},
	}
	runWorkerContractSuite(t, factory)
}

func TestWorkerContract_DatabaseIntegration(t *testing.T) {
	cases := []struct {
		name    string
		backend string
		cfg     func(t *testing.T) DatabaseConfig
	}{
		{
			name:    "sqlite",
			backend: "sqlite",
			cfg: func(t *testing.T) DatabaseConfig {
				return DatabaseConfig{
					DriverName:   "sqlite",
					DSN:          fmt.Sprintf("%s/worker-contract-integration-%d.db", t.TempDir(), time.Now().UnixNano()),
					Workers:      1,
					PollInterval: 10 * time.Millisecond,
				}
			},
		},
		{
			name:    "mysql",
			backend: "mysql",
			cfg: func(_ *testing.T) DatabaseConfig {
				return DatabaseConfig{
					DriverName:   "mysql",
					DSN:          fmt.Sprintf("queue:queue@tcp(%s)/queue_test?parseTime=true", integrationMySQL.addr),
					Workers:      1,
					PollInterval: 10 * time.Millisecond,
				}
			},
		},
		{
			name:    "postgres",
			backend: "postgres",
			cfg: func(_ *testing.T) DatabaseConfig {
				return DatabaseConfig{
					DriverName:   "pgx",
					DSN:          fmt.Sprintf("postgres://queue:queue@%s/queue_test?sslmode=disable", integrationPostgres.addr),
					Workers:      1,
					PollInterval: 10 * time.Millisecond,
				}
			},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if !integrationBackendEnabled(tc.backend) {
				t.Skipf("%s integration backend not selected", tc.backend)
			}
			factory := workerContractFactory{
				name: "database-" + tc.name,
				setup: func(t *testing.T) (Worker, func(task Task) error) {
					cfg := tc.cfg(t)
					worker, err := NewWorker(WorkerConfig{
						Driver:         DriverDatabase,
						DatabaseDriver: cfg.DriverName,
						DatabaseDSN:    cfg.DSN,
						Workers:        cfg.Workers,
						PollInterval:   cfg.PollInterval,
					})
					if err != nil {
						t.Fatalf("new database worker failed: %v", err)
					}
					producer, err := New(Config{
						Driver:         DriverDatabase,
						DatabaseDriver: cfg.DriverName,
						DatabaseDSN:    cfg.DSN,
					})
					if err != nil {
						t.Fatalf("new database queue failed: %v", err)
					}
					t.Cleanup(func() { _ = producer.Shutdown(context.Background()) })
					return worker, func(task Task) error {
						return producer.Enqueue(context.Background(), task)
					}
				},
			}
			runWorkerContractSuite(t, factory)
		})
	}
}

func TestWorkerContract_NATSIntegration(t *testing.T) {
	if !integrationBackendEnabled("nats") {
		t.Skip("nats integration backend not selected")
	}
	factory := workerContractFactory{
		name: "nats",
		setup: func(t *testing.T) (Worker, func(task Task) error) {
			worker, err := NewWorker(WorkerConfig{
				Driver:  DriverNATS,
				NATSURL: integrationNATS.url,
			})
			if err != nil {
				t.Fatalf("new nats worker failed: %v", err)
			}
			producer, err := New(Config{
				Driver:  DriverNATS,
				NATSURL: integrationNATS.url,
			})
			if err != nil {
				t.Fatalf("new nats queue failed: %v", err)
			}
			t.Cleanup(func() { _ = producer.Shutdown(context.Background()) })
			return worker, func(task Task) error {
				return producer.Enqueue(context.Background(), task)
			}
		},
	}
	runWorkerContractSuite(t, factory)
}

func TestWorkerContract_SQSIntegration(t *testing.T) {
	if !integrationBackendEnabled("sqs") {
		t.Skip("sqs integration backend not selected")
	}
	factory := workerContractFactory{
		name: "sqs",
		setup: func(t *testing.T) (Worker, func(task Task) error) {
			worker, err := NewWorker(WorkerConfig{
				Driver:       DriverSQS,
				SQSEndpoint:  integrationSQS.endpoint,
				SQSRegion:    integrationSQS.region,
				SQSAccessKey: integrationSQS.accessKey,
				SQSSecretKey: integrationSQS.secretKey,
			})
			if err != nil {
				t.Fatalf("new sqs worker failed: %v", err)
			}
			producer, err := New(Config{
				Driver:       DriverSQS,
				SQSEndpoint:  integrationSQS.endpoint,
				SQSRegion:    integrationSQS.region,
				SQSAccessKey: integrationSQS.accessKey,
				SQSSecretKey: integrationSQS.secretKey,
			})
			if err != nil {
				t.Fatalf("new sqs queue failed: %v", err)
			}
			t.Cleanup(func() { _ = producer.Shutdown(context.Background()) })
			return worker, func(task Task) error {
				return producer.Enqueue(context.Background(), task)
			}
		},
	}
	runWorkerContractSuite(t, factory)
}

func TestWorkerContract_RabbitMQIntegration(t *testing.T) {
	if !integrationBackendEnabled("rabbitmq") {
		t.Skip("rabbitmq integration backend not selected")
	}
	factory := workerContractFactory{
		name: "rabbitmq",
		setup: func(t *testing.T) (Worker, func(task Task) error) {
			worker, err := NewWorker(WorkerConfig{
				Driver:       DriverRabbitMQ,
				RabbitMQURL:  integrationRabbitMQ.url,
				DefaultQueue: "default",
			})
			if err != nil {
				t.Fatalf("new rabbitmq worker failed: %v", err)
			}
			producer, err := New(Config{
				Driver:      DriverRabbitMQ,
				RabbitMQURL: integrationRabbitMQ.url,
			})
			if err != nil {
				t.Fatalf("new rabbitmq queue failed: %v", err)
			}
			t.Cleanup(func() { _ = producer.Shutdown(context.Background()) })
			return worker, func(task Task) error {
				return producer.Enqueue(context.Background(), task)
			}
		},
	}
	runWorkerContractSuite(t, factory)
}
