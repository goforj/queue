//go:build integration

package root_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/goforj/queue"
)

func BenchmarkDriverDispatch_Integration(b *testing.B) {
	ctx := context.Background()

	runIntegrationDriverBench(b, "redis", func(b *testing.B) queue.QueueRuntime {
		ensureRedis(b)
		cfg := withDefaultQueue(redisCfg(integrationRedis.addr), uniqueQueueName("bench-redis"))
		q, err := newQueueRuntime(cfg)
		if err != nil {
			b.Fatalf("new redis queue failed: %v", err)
		}
		q.Register("bench:redis", func(context.Context, queue.Job) error { return nil })
		if err := q.Workers(2).StartWorkers(ctx); err != nil {
			b.Fatalf("start redis workers failed: %v", err)
		}
		b.Cleanup(func() { _ = q.Shutdown(ctx) })
		return q
	}, benchJob("bench:redis", uniqueQueueName("bench-redis-q")))

	runIntegrationDriverBench(b, "nats", func(b *testing.B) queue.QueueRuntime {
		ensureNATS(b)
		cfg := withDefaultQueue(natsCfg(integrationNATS.url), uniqueQueueName("bench-nats"))
		q, err := newQueueRuntime(cfg)
		if err != nil {
			b.Fatalf("new nats queue failed: %v", err)
		}
		q.Register("bench:nats", func(context.Context, queue.Job) error { return nil })
		if err := q.Workers(2).StartWorkers(ctx); err != nil {
			b.Fatalf("start nats workers failed: %v", err)
		}
		b.Cleanup(func() { _ = q.Shutdown(ctx) })
		return q
	}, benchJob("bench:nats", uniqueQueueName("bench-nats-q")))

	runIntegrationDriverBench(b, "sqs", func(b *testing.B) queue.QueueRuntime {
		ensureSQS(b)
		cfg := withDefaultQueue(sqsCfg(
			integrationSQS.region,
			integrationSQS.endpoint,
			integrationSQS.accessKey,
			integrationSQS.secretKey,
		), uniqueQueueName("bench-sqs"))
		q, err := newQueueRuntime(cfg)
		if err != nil {
			b.Fatalf("new sqs queue failed: %v", err)
		}
		q.Register("bench:sqs", func(context.Context, queue.Job) error { return nil })
		if err := q.Workers(2).StartWorkers(ctx); err != nil {
			b.Fatalf("start sqs workers failed: %v", err)
		}
		b.Cleanup(func() { _ = q.Shutdown(ctx) })
		return q
	}, benchJob("bench:sqs", uniqueQueueName("bench-sqs-q")))

	runIntegrationDriverBench(b, "rabbitmq", func(b *testing.B) queue.QueueRuntime {
		ensureRabbitMQ(b)
		cfg := withDefaultQueue(rabbitmqCfg(integrationRabbitMQ.url), uniqueQueueName("bench-rmq"))
		q, err := newQueueRuntime(cfg)
		if err != nil {
			b.Fatalf("new rabbitmq queue failed: %v", err)
		}
		q.Register("bench:rabbitmq", func(context.Context, queue.Job) error { return nil })
		if err := q.Workers(2).StartWorkers(ctx); err != nil {
			b.Fatalf("start rabbitmq workers failed: %v", err)
		}
		b.Cleanup(func() { _ = q.Shutdown(ctx) })
		return q
	}, benchJob("bench:rabbitmq", uniqueQueueName("bench-rmq-q")))

	runIntegrationDriverBench(b, "mysql", func(b *testing.B) queue.QueueRuntime {
		ensureMySQLDB(b)
		cfg := withDefaultQueue(mysqlCfg(fmt.Sprintf("queue:queue@tcp(%s)/queue_test?parseTime=true", integrationMySQL.addr)), "default")
		q, err := newQueueRuntime(cfg)
		if err != nil {
			b.Fatalf("new mysql queue failed: %v", err)
		}
		q.Register("bench:mysql", func(context.Context, queue.Job) error { return nil })
		if err := q.StartWorkers(ctx); err != nil {
			b.Fatalf("start mysql workers failed: %v", err)
		}
		b.Cleanup(func() { _ = q.Shutdown(ctx) })
		return q
	}, benchJob("bench:mysql", "default"))

	runIntegrationDriverBench(b, "postgres", func(b *testing.B) queue.QueueRuntime {
		ensurePostgresDB(b)
		cfg := withDefaultQueue(postgresCfg(fmt.Sprintf("postgres://queue:queue@%s/queue_test?sslmode=disable", integrationPostgres.addr)), "default")
		q, err := newQueueRuntime(cfg)
		if err != nil {
			b.Fatalf("new postgres queue failed: %v", err)
		}
		q.Register("bench:postgres", func(context.Context, queue.Job) error { return nil })
		if err := q.StartWorkers(ctx); err != nil {
			b.Fatalf("start postgres workers failed: %v", err)
		}
		b.Cleanup(func() { _ = q.Shutdown(ctx) })
		return q
	}, benchJob("bench:postgres", "default"))
}

func runIntegrationDriverBench(b *testing.B, backend string, ctor func(b *testing.B) queue.QueueRuntime, job queue.Job) {
	b.Run(backend, func(b *testing.B) {
		if !integrationBackendEnabled(backend) {
			b.Skipf("%s integration backend not selected", backend)
		}
		q := ctor(b)
		benchmarkDispatchLoop(b, context.Background(), q, job)
	})
}
