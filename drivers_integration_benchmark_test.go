//go:build integration

package queue

import (
	"context"
	"fmt"
	"testing"
)

func BenchmarkDriverDispatch_Integration(b *testing.B) {
	ctx := context.Background()

	runIntegrationDriverBench(b, "redis", func(b *testing.B) Queue {
		cfg := Config{
			Driver:       DriverRedis,
			RedisAddr:    integrationRedis.addr,
			DefaultQueue: uniqueQueueName("bench-redis"),
		}
		q, err := New(cfg)
		if err != nil {
			b.Fatalf("new redis queue failed: %v", err)
		}
		q.Register("bench:redis", func(context.Context, Job) error { return nil })
		if err := q.Workers(2).StartWorkers(ctx); err != nil {
			b.Fatalf("start redis workers failed: %v", err)
		}
		b.Cleanup(func() { _ = q.Shutdown(ctx) })
		return q
	}, benchJob("bench:redis", uniqueQueueName("bench-redis-q")))

	runIntegrationDriverBench(b, "nats", func(b *testing.B) Queue {
		cfg := Config{
			Driver:       DriverNATS,
			NATSURL:      integrationNATS.url,
			DefaultQueue: uniqueQueueName("bench-nats"),
		}
		q, err := New(cfg)
		if err != nil {
			b.Fatalf("new nats queue failed: %v", err)
		}
		q.Register("bench:nats", func(context.Context, Job) error { return nil })
		if err := q.Workers(2).StartWorkers(ctx); err != nil {
			b.Fatalf("start nats workers failed: %v", err)
		}
		b.Cleanup(func() { _ = q.Shutdown(ctx) })
		return q
	}, benchJob("bench:nats", uniqueQueueName("bench-nats-q")))

	runIntegrationDriverBench(b, "sqs", func(b *testing.B) Queue {
		cfg := Config{
			Driver:       DriverSQS,
			SQSRegion:    integrationSQS.region,
			SQSEndpoint:  integrationSQS.endpoint,
			SQSAccessKey: integrationSQS.accessKey,
			SQSSecretKey: integrationSQS.secretKey,
			DefaultQueue: uniqueQueueName("bench-sqs"),
		}
		q, err := New(cfg)
		if err != nil {
			b.Fatalf("new sqs queue failed: %v", err)
		}
		q.Register("bench:sqs", func(context.Context, Job) error { return nil })
		if err := q.Workers(2).StartWorkers(ctx); err != nil {
			b.Fatalf("start sqs workers failed: %v", err)
		}
		b.Cleanup(func() { _ = q.Shutdown(ctx) })
		return q
	}, benchJob("bench:sqs", uniqueQueueName("bench-sqs-q")))

	runIntegrationDriverBench(b, "rabbitmq", func(b *testing.B) Queue {
		cfg := Config{
			Driver:       DriverRabbitMQ,
			RabbitMQURL:  integrationRabbitMQ.url,
			DefaultQueue: uniqueQueueName("bench-rmq"),
		}
		q, err := New(cfg)
		if err != nil {
			b.Fatalf("new rabbitmq queue failed: %v", err)
		}
		q.Register("bench:rabbitmq", func(context.Context, Job) error { return nil })
		if err := q.Workers(2).StartWorkers(ctx); err != nil {
			b.Fatalf("start rabbitmq workers failed: %v", err)
		}
		b.Cleanup(func() { _ = q.Shutdown(ctx) })
		return q
	}, benchJob("bench:rabbitmq", uniqueQueueName("bench-rmq-q")))

	runIntegrationDriverBench(b, "mysql", func(b *testing.B) Queue {
		cfg := Config{
			Driver:         DriverDatabase,
			DatabaseDriver: "mysql",
			DatabaseDSN:    fmt.Sprintf("queue:queue@tcp(%s)/queue_test?parseTime=true", integrationMySQL.addr),
			DefaultQueue:   "default",
		}
		q, err := New(cfg)
		if err != nil {
			b.Fatalf("new mysql queue failed: %v", err)
		}
		q.Register("bench:mysql", func(context.Context, Job) error { return nil })
		if err := q.StartWorkers(ctx); err != nil {
			b.Fatalf("start mysql workers failed: %v", err)
		}
		b.Cleanup(func() { _ = q.Shutdown(ctx) })
		return q
	}, benchJob("bench:mysql", "default"))

	runIntegrationDriverBench(b, "postgres", func(b *testing.B) Queue {
		cfg := Config{
			Driver:         DriverDatabase,
			DatabaseDriver: "pgx",
			DatabaseDSN:    fmt.Sprintf("postgres://queue:queue@%s/queue_test?sslmode=disable", integrationPostgres.addr),
			DefaultQueue:   "default",
		}
		q, err := New(cfg)
		if err != nil {
			b.Fatalf("new postgres queue failed: %v", err)
		}
		q.Register("bench:postgres", func(context.Context, Job) error { return nil })
		if err := q.StartWorkers(ctx); err != nil {
			b.Fatalf("start postgres workers failed: %v", err)
		}
		b.Cleanup(func() { _ = q.Shutdown(ctx) })
		return q
	}, benchJob("bench:postgres", "default"))
}

func runIntegrationDriverBench(b *testing.B, backend string, ctor func(b *testing.B) Queue, job Job) {
	b.Run(backend, func(b *testing.B) {
		if !integrationBackendEnabled(backend) {
			b.Skipf("%s integration backend not selected", backend)
		}
		q := ctor(b)
		benchmarkDispatchLoop(b, context.Background(), q, job)
	})
}
