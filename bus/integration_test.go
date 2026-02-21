//go:build integration

package bus_test

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goforj/queue"
	"github.com/goforj/queue/bus"
	testcontainers "github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

var integrationRedis struct {
	container testcontainers.Container
	addr      string
}

var integrationMySQL struct {
	container testcontainers.Container
	addr      string
}

var integrationPostgres struct {
	container testcontainers.Container
	addr      string
}

var integrationNATS struct {
	container testcontainers.Container
	url       string
}

var integrationSQS struct {
	container testcontainers.Container
	endpoint  string
	region    string
	accessKey string
	secretKey string
}

var integrationRabbitMQ struct {
	container testcontainers.Container
	url       string
}

func TestMain(m *testing.M) {
	ctx := context.Background()
	backends := selectedIntegrationBackends()

	if backends["redis"] {
		c, addr, err := startRedisContainer(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to start redis integration container: %v\n", err)
			os.Exit(1)
		}
		integrationRedis.container = c
		integrationRedis.addr = addr
	}
	if backends["mysql"] {
		c, addr, err := startMySQLContainer(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to start mysql integration container: %v\n", err)
			shutdownContainers(ctx)
			os.Exit(1)
		}
		integrationMySQL.container = c
		integrationMySQL.addr = addr
	}
	if backends["postgres"] {
		c, addr, err := startPostgresContainer(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to start postgres integration container: %v\n", err)
			shutdownContainers(ctx)
			os.Exit(1)
		}
		integrationPostgres.container = c
		integrationPostgres.addr = addr
	}
	if backends["nats"] {
		c, url, err := startNATSContainer(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to start nats integration container: %v\n", err)
			shutdownContainers(ctx)
			os.Exit(1)
		}
		integrationNATS.container = c
		integrationNATS.url = url
	}
	if backends["sqs"] {
		c, endpoint, err := startSQSContainer(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to start sqs integration container: %v\n", err)
			shutdownContainers(ctx)
			os.Exit(1)
		}
		integrationSQS.container = c
		integrationSQS.endpoint = endpoint
		integrationSQS.region = "us-east-1"
		integrationSQS.accessKey = "test"
		integrationSQS.secretKey = "test"
	}
	if backends["rabbitmq"] {
		c, url, err := startRabbitMQContainer(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to start rabbitmq integration container: %v\n", err)
			shutdownContainers(ctx)
			os.Exit(1)
		}
		integrationRabbitMQ.container = c
		integrationRabbitMQ.url = url
	}

	code := m.Run()
	shutdownContainers(ctx)
	os.Exit(code)
}

func shutdownContainers(ctx context.Context) {
	shutdownCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	for _, c := range []testcontainers.Container{
		integrationRabbitMQ.container,
		integrationSQS.container,
		integrationNATS.container,
		integrationPostgres.container,
		integrationMySQL.container,
		integrationRedis.container,
	} {
		if c != nil {
			_ = c.Terminate(shutdownCtx)
		}
	}
}

func selectedIntegrationBackends() map[string]bool {
	selected := map[string]bool{
		"null":       true,
		"sync":       true,
		"workerpool": true,
		"redis":      true,
		"mysql":      true,
		"postgres":   true,
		"sqlite":     true,
		"nats":       true,
		"sqs":        true,
		"rabbitmq":   true,
	}
	value := strings.TrimSpace(strings.ToLower(os.Getenv("INTEGRATION_BACKEND")))
	if value == "" || value == "all" {
		return selected
	}
	for key := range selected {
		selected[key] = false
	}
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		selected[part] = true
	}
	return selected
}

func integrationBackendEnabled(name string) bool {
	return selectedIntegrationBackends()[strings.ToLower(name)]
}

func TestIntegrationBus_AllBackends(t *testing.T) {
	fx := []struct {
		name     string
		executes bool
		newQ     func(t *testing.T) queue.Queue
	}{
		{
			name:     "null",
			executes: false,
			newQ: func(t *testing.T) queue.Queue {
				q, err := queue.New(queue.Config{Driver: queue.DriverNull})
				if err != nil {
					t.Fatalf("new null queue failed: %v", err)
				}
				return q
			},
		},
		{
			name:     "sync",
			executes: true,
			newQ: func(t *testing.T) queue.Queue {
				q, err := queue.New(queue.Config{Driver: queue.DriverSync})
				if err != nil {
					t.Fatalf("new sync queue failed: %v", err)
				}
				return q
			},
		},
		{
			name:     "workerpool",
			executes: true,
			newQ: func(t *testing.T) queue.Queue {
				q, err := queue.New(queue.Config{Driver: queue.DriverWorkerpool})
				if err != nil {
					t.Fatalf("new workerpool queue failed: %v", err)
				}
				return q
			},
		},
		{
			name:     "redis",
			executes: true,
			newQ: func(t *testing.T) queue.Queue {
				q, err := queue.New(queue.Config{
					Driver:    queue.DriverRedis,
					RedisAddr: integrationRedis.addr,
				})
				if err != nil {
					t.Fatalf("new redis queue failed: %v", err)
				}
				return q
			},
		},
		{
			name:     "mysql",
			executes: true,
			newQ: func(t *testing.T) queue.Queue {
				q, err := queue.New(queue.Config{
					Driver:         queue.DriverDatabase,
					DatabaseDriver: "mysql",
					DatabaseDSN:    fmt.Sprintf("queue:queue@tcp(%s)/queue_test?parseTime=true", integrationMySQL.addr),
				})
				if err != nil {
					t.Fatalf("new mysql queue failed: %v", err)
				}
				return q
			},
		},
		{
			name:     "postgres",
			executes: true,
			newQ: func(t *testing.T) queue.Queue {
				q, err := queue.New(queue.Config{
					Driver:         queue.DriverDatabase,
					DatabaseDriver: "pgx",
					DatabaseDSN:    fmt.Sprintf("postgres://queue:queue@%s/queue_test?sslmode=disable", integrationPostgres.addr),
				})
				if err != nil {
					t.Fatalf("new postgres queue failed: %v", err)
				}
				return q
			},
		},
		{
			name:     "sqlite",
			executes: true,
			newQ: func(t *testing.T) queue.Queue {
				q, err := queue.New(queue.Config{
					Driver:         queue.DriverDatabase,
					DatabaseDriver: "sqlite",
					DatabaseDSN:    fmt.Sprintf("%s/bus-integration-%d.db", t.TempDir(), time.Now().UnixNano()),
				})
				if err != nil {
					t.Fatalf("new sqlite queue failed: %v", err)
				}
				return q
			},
		},
		{
			name:     "nats",
			executes: true,
			newQ: func(t *testing.T) queue.Queue {
				q, err := queue.New(queue.Config{
					Driver:  queue.DriverNATS,
					NATSURL: integrationNATS.url,
				})
				if err != nil {
					t.Fatalf("new nats queue failed: %v", err)
				}
				return q
			},
		},
		{
			name:     "sqs",
			executes: true,
			newQ: func(t *testing.T) queue.Queue {
				q, err := queue.New(queue.Config{
					Driver:       queue.DriverSQS,
					SQSEndpoint:  integrationSQS.endpoint,
					SQSRegion:    integrationSQS.region,
					SQSAccessKey: integrationSQS.accessKey,
					SQSSecretKey: integrationSQS.secretKey,
				})
				if err != nil {
					t.Fatalf("new sqs queue failed: %v", err)
				}
				return q
			},
		},
		{
			name:     "rabbitmq",
			executes: true,
			newQ: func(t *testing.T) queue.Queue {
				q, err := queue.New(queue.Config{
					Driver:      queue.DriverRabbitMQ,
					RabbitMQURL: integrationRabbitMQ.url,
				})
				if err != nil {
					t.Fatalf("new rabbitmq queue failed: %v", err)
				}
				return q
			},
		},
	}

	for _, backend := range fx {
		backend := backend
		t.Run(backend.name, func(t *testing.T) {
			if !integrationBackendEnabled(backend.name) {
				t.Skipf("%s integration backend not selected", backend.name)
			}

			q := backend.newQ(t)
			b, err := bus.New(q)
			if err != nil {
				t.Fatalf("new bus failed: %v", err)
			}
			if err := b.StartWorkers(context.Background()); err != nil {
				t.Fatalf("start bus workers failed: %v", err)
			}
			defer func() {
				shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				_ = b.Shutdown(shutdownCtx)
			}()

			queueName := uniqueQueueName("bus-integration")
			if backend.name == "redis" {
				queueName = "default"
			}
			if !backend.executes {
				testBusNullScenario(t, b, queueName)
				return
			}
			testBusDispatchScenario(t, b, queueName)
			testBusChainScenario(t, b, queueName)
			testBusBatchScenario(t, b, queueName)
		})
	}
}

func testBusNullScenario(t *testing.T, b bus.Bus, queueName string) {
	t.Helper()

	if _, err := b.Dispatch(context.Background(), bus.NewJob("bus:null:dispatch", map[string]string{
		"probe": "true",
	}).OnQueue(queueName)); err != nil {
		t.Fatalf("null scenario: dispatch failed: %v", err)
	}

	chainID, err := b.Chain(
		bus.NewJob("bus:null:chain:step1", nil),
		bus.NewJob("bus:null:chain:step2", nil),
	).OnQueue(queueName).Dispatch(context.Background())
	if err != nil {
		t.Fatalf("null scenario: chain dispatch failed: %v", err)
	}
	chain, err := b.FindChain(context.Background(), chainID)
	if err != nil {
		t.Fatalf("null scenario: find chain failed: %v", err)
	}
	if chain.Completed {
		t.Fatal("null scenario: expected chain to remain incomplete")
	}

	batchID, err := b.Batch(
		bus.NewJob("bus:null:batch:step1", nil),
		bus.NewJob("bus:null:batch:step2", nil),
	).OnQueue(queueName).Dispatch(context.Background())
	if err != nil {
		t.Fatalf("null scenario: batch dispatch failed: %v", err)
	}
	batch, err := b.FindBatch(context.Background(), batchID)
	if err != nil {
		t.Fatalf("null scenario: find batch failed: %v", err)
	}
	if batch.Completed {
		t.Fatal("null scenario: expected batch to remain incomplete")
	}
}

func testBusDispatchScenario(t *testing.T, b bus.Bus, queueName string) {
	t.Helper()
	type PollPayload struct {
		URL string `json:"url"`
	}
	seen := make(chan string, 1)
	b.Register("bus:dispatch:poll", func(_ context.Context, jc bus.Context) error {
		var payload PollPayload
		if err := jc.Bind(&payload); err != nil {
			return err
		}
		seen <- payload.URL
		return nil
	})

	_, err := b.Dispatch(context.Background(), bus.NewJob("bus:dispatch:poll", PollPayload{
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

func testBusChainScenario(t *testing.T, b bus.Bus, queueName string) {
	t.Helper()
	var mu sync.Mutex
	order := make([]string, 0, 3)

	appendOrder := func(name string) {
		mu.Lock()
		order = append(order, name)
		mu.Unlock()
	}

	b.Register("bus:chain:step1", func(context.Context, bus.Context) error {
		appendOrder("step1")
		return nil
	})
	b.Register("bus:chain:step2", func(context.Context, bus.Context) error {
		appendOrder("step2")
		return nil
	})
	b.Register("bus:chain:step3", func(context.Context, bus.Context) error {
		appendOrder("step3")
		return nil
	})

	chainID, err := b.Chain(
		bus.NewJob("bus:chain:step1", nil),
		bus.NewJob("bus:chain:step2", nil),
		bus.NewJob("bus:chain:step3", nil),
	).OnQueue(queueName).Dispatch(context.Background())
	if err != nil {
		t.Fatalf("chain scenario: dispatch failed: %v", err)
	}

	waitFor(t, 20*time.Second, "chain completed", func() bool {
		st, err := b.FindChain(context.Background(), chainID)
		return err == nil && st.Completed
	})

	mu.Lock()
	got := append([]string(nil), order...)
	mu.Unlock()
	if len(got) != 3 || got[0] != "step1" || got[1] != "step2" || got[2] != "step3" {
		t.Fatalf("chain scenario: unexpected execution order %v", got)
	}
}

func testBusBatchScenario(t *testing.T, b bus.Bus, queueName string) {
	t.Helper()
	type BatchPayload struct {
		ID int `json:"id"`
	}
	var processed atomic.Int32
	b.Register("bus:batch:work", func(_ context.Context, jc bus.Context) error {
		var payload BatchPayload
		if err := jc.Bind(&payload); err != nil {
			return err
		}
		processed.Add(1)
		return nil
	})

	batchID, err := b.Batch(
		bus.NewJob("bus:batch:work", BatchPayload{ID: 1}),
		bus.NewJob("bus:batch:work", BatchPayload{ID: 2}),
		bus.NewJob("bus:batch:work", BatchPayload{ID: 3}),
	).OnQueue(queueName).Dispatch(context.Background())
	if err != nil {
		t.Fatalf("batch scenario: dispatch failed: %v", err)
	}

	waitFor(t, 20*time.Second, "batch completed", func() bool {
		st, err := b.FindBatch(context.Background(), batchID)
		return err == nil && st.Completed && st.Processed == 3 && st.Pending == 0
	})

	if got := processed.Load(); got != 3 {
		t.Fatalf("batch scenario: expected 3 processed jobs, got %d", got)
	}
}

func waitFor(t *testing.T, timeout time.Duration, label string, ready func() bool) {
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

func uniqueQueueName(prefix string) string {
	return fmt.Sprintf("%s-%d-%d", prefix, time.Now().UnixNano(), rand.Intn(100000))
}

func startRedisContainer(ctx context.Context) (testcontainers.Container, string, error) {
	req := testcontainers.ContainerRequest{
		Image:        "redis:7-alpine",
		ExposedPorts: []string{"6379/tcp"},
		WaitingFor:   wait.ForListeningPort("6379/tcp").WithStartupTimeout(60 * time.Second),
	}
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: req, Started: true})
	if err != nil {
		return nil, "", err
	}
	host, err := c.Host(ctx)
	if err != nil {
		return nil, "", err
	}
	port, err := c.MappedPort(ctx, "6379/tcp")
	if err != nil {
		return nil, "", err
	}
	return c, fmt.Sprintf("%s:%s", host, port.Port()), nil
}

func startMySQLContainer(ctx context.Context) (testcontainers.Container, string, error) {
	req := testcontainers.ContainerRequest{
		Image:        "mysql:8",
		ExposedPorts: []string{"3306/tcp"},
		Env: map[string]string{
			"MYSQL_ROOT_PASSWORD": "root",
			"MYSQL_DATABASE":      "queue_test",
			"MYSQL_USER":          "queue",
			"MYSQL_PASSWORD":      "queue",
		},
		WaitingFor: wait.ForListeningPort("3306/tcp").WithStartupTimeout(120 * time.Second),
	}
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: req, Started: true})
	if err != nil {
		return nil, "", err
	}
	host, err := c.Host(ctx)
	if err != nil {
		return nil, "", err
	}
	port, err := c.MappedPort(ctx, "3306/tcp")
	if err != nil {
		return nil, "", err
	}
	return c, fmt.Sprintf("%s:%s", host, port.Port()), nil
}

func startPostgresContainer(ctx context.Context) (testcontainers.Container, string, error) {
	req := testcontainers.ContainerRequest{
		Image:        "postgres:15",
		ExposedPorts: []string{"5432/tcp"},
		Env: map[string]string{
			"POSTGRES_USER":     "queue",
			"POSTGRES_PASSWORD": "queue",
			"POSTGRES_DB":       "queue_test",
		},
		WaitingFor: wait.ForListeningPort("5432/tcp").WithStartupTimeout(90 * time.Second),
	}
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: req, Started: true})
	if err != nil {
		return nil, "", err
	}
	host, err := c.Host(ctx)
	if err != nil {
		return nil, "", err
	}
	port, err := c.MappedPort(ctx, "5432/tcp")
	if err != nil {
		return nil, "", err
	}
	return c, fmt.Sprintf("%s:%s", host, port.Port()), nil
}

func startNATSContainer(ctx context.Context) (testcontainers.Container, string, error) {
	req := testcontainers.ContainerRequest{
		Image:        "nats:2",
		ExposedPorts: []string{"4222/tcp"},
		WaitingFor:   wait.ForListeningPort("4222/tcp").WithStartupTimeout(60 * time.Second),
	}
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: req, Started: true})
	if err != nil {
		return nil, "", err
	}
	host, err := c.Host(ctx)
	if err != nil {
		return nil, "", err
	}
	port, err := c.MappedPort(ctx, "4222/tcp")
	if err != nil {
		return nil, "", err
	}
	return c, fmt.Sprintf("nats://%s:%s", host, port.Port()), nil
}

func startSQSContainer(ctx context.Context) (testcontainers.Container, string, error) {
	req := testcontainers.ContainerRequest{
		Image:        "localstack/localstack:3.0",
		ExposedPorts: []string{"4566/tcp"},
		Env: map[string]string{
			"SERVICES": "sqs",
		},
		WaitingFor: wait.ForListeningPort("4566/tcp").WithStartupTimeout(120 * time.Second),
	}
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: req, Started: true})
	if err != nil {
		return nil, "", err
	}
	host, err := c.Host(ctx)
	if err != nil {
		return nil, "", err
	}
	port, err := c.MappedPort(ctx, "4566/tcp")
	if err != nil {
		return nil, "", err
	}
	return c, fmt.Sprintf("http://%s:%s", host, port.Port()), nil
}

func startRabbitMQContainer(ctx context.Context) (testcontainers.Container, string, error) {
	req := testcontainers.ContainerRequest{
		Image:        "rabbitmq:3.12-management",
		ExposedPorts: []string{"5672/tcp"},
		WaitingFor:   wait.ForListeningPort("5672/tcp").WithStartupTimeout(90 * time.Second),
	}
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: req, Started: true})
	if err != nil {
		return nil, "", err
	}
	host, err := c.Host(ctx)
	if err != nil {
		return nil, "", err
	}
	port, err := c.MappedPort(ctx, "5672/tcp")
	if err != nil {
		return nil, "", err
	}
	return c, fmt.Sprintf("amqp://guest:guest@%s:%s/", host, port.Port()), nil
}
