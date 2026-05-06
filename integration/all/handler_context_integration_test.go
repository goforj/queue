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
	"github.com/goforj/queue/driver/mysqlqueue"
	"github.com/goforj/queue/driver/natsqueue"
	"github.com/goforj/queue/driver/postgresqueue"
	"github.com/goforj/queue/driver/rabbitmqqueue"
	"github.com/goforj/queue/driver/redisqueue"
	"github.com/goforj/queue/driver/sqlitequeue"
	"github.com/goforj/queue/driver/sqsqueue"
	"github.com/goforj/queue/integration/testenv"
)

type handlerContextRecorder struct {
	mu            sync.Mutex
	queueName      string
	key           any
	want          string
	total         map[EventKind]int
	decorated     map[EventKind]int
}

func (r *handlerContextRecorder) setQueueName(queueName string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.queueName = queueName
}

func newHandlerContextRecorder(queueName string, key any, want string) *handlerContextRecorder {
	return &handlerContextRecorder{
		queueName:      queueName,
		key:           key,
		want:          want,
		total:         make(map[EventKind]int),
		decorated:     make(map[EventKind]int),
	}
}

func (r *handlerContextRecorder) Observe(ctx context.Context, event Event) {
	switch event.Kind {
	case EventProcessStarted, EventProcessSucceeded, EventProcessFailed:
	default:
		return
	}
	if event.Queue != r.queueName {
		if r.queueName != "" {
			return
		}
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.total[event.Kind]++
	if got, _ := ctx.Value(r.key).(string); got == r.want {
		r.decorated[event.Kind]++
	}
}

func (r *handlerContextRecorder) decoratedEqualsTotal(kind EventKind) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	total := r.total[kind]
	return total > 0 && r.decorated[kind] == total
}

func withDefaultQueueAll[T any](cfg T, name string) T {
	switch c := any(&cfg).(type) {
	case *Config:
		c.DefaultQueue = name
	case *redisqueue.Config:
		c.DefaultQueue = name
	case *natsqueue.Config:
		c.DefaultQueue = name
	case *sqsqueue.Config:
		c.DefaultQueue = name
	case *rabbitmqqueue.Config:
		c.DefaultQueue = name
	case *mysqlqueue.Config:
		c.DefaultQueue = name
	case *postgresqueue.Config:
		c.DefaultQueue = name
	case *sqlitequeue.Config:
		c.DefaultQueue = name
	}
	return cfg
}

func withObserverAll[T any](cfg T, observer Observer) T {
	switch c := any(&cfg).(type) {
	case *Config:
		c.Observer = observer
	case *redisqueue.Config:
		c.Observer = observer
	case *natsqueue.Config:
		c.Observer = observer
	case *sqsqueue.Config:
		c.Observer = observer
	case *rabbitmqqueue.Config:
		c.Observer = observer
	case *mysqlqueue.Config:
		c.Observer = observer
	case *postgresqueue.Config:
		c.Observer = observer
	case *sqlitequeue.Config:
		c.Observer = observer
	}
	return cfg
}

func TestIntegrationQueue_HandlerContextDecorator_AllBackends(t *testing.T) {
	type ctxKey struct{}
	key := ctxKey{}
	const want = "jobs"

	fixtures := []struct {
		name   string
		newQ   func(t *testing.T, observer Observer) (*Queue, string)
	}{
		{
			name: testenv.BackendSync,
			newQ: func(t *testing.T, observer Observer) (*Queue, string) {
				q, err := newQueue(withObserverAll(syncCfg(), observer), WithHandlerContextDecorator(func(ctx context.Context) context.Context {
					return context.WithValue(ctx, key, want)
				}))
				if err != nil {
					t.Fatalf("new sync queue failed: %v", err)
				}
				return q, uniqueQueueName("handler-context-sync")
			},
		},
		{
			name: testenv.BackendWorkerpool,
			newQ: func(t *testing.T, observer Observer) (*Queue, string) {
				q, err := newQueue(withObserverAll(workerpoolCfg(), observer), WithHandlerContextDecorator(func(ctx context.Context) context.Context {
					return context.WithValue(ctx, key, want)
				}))
				if err != nil {
					t.Fatalf("new workerpool queue failed: %v", err)
				}
				return q, uniqueQueueName("handler-context-workerpool")
			},
		},
		{
			name: testenv.BackendRedis,
			newQ: func(t *testing.T, observer Observer) (*Queue, string) {
				physical := uniqueQueueName("handler-context-redis")
				q, err := newQueue(
					withObserverAll(withDefaultQueueAll(redisCfg(integrationRedis.addr), physical), observer),
					WithHandlerContextDecorator(func(ctx context.Context) context.Context {
						return context.WithValue(ctx, key, want)
					}),
				)
				if err != nil {
					t.Fatalf("new redis queue failed: %v", err)
				}
				return q, physical
			},
		},
		{
			name: testenv.BackendMySQL,
			newQ: func(t *testing.T, observer Observer) (*Queue, string) {
				q, err := newQueue(
					withObserverAll(mysqlCfg(mysqlDSN(integrationMySQL.addr)), observer),
					WithHandlerContextDecorator(func(ctx context.Context) context.Context {
						return context.WithValue(ctx, key, want)
					}),
				)
				if err != nil {
					t.Fatalf("new mysql queue failed: %v", err)
				}
				return q, uniqueQueueName("handler-context-mysql")
			},
		},
		{
			name: testenv.BackendPostgres,
			newQ: func(t *testing.T, observer Observer) (*Queue, string) {
				q, err := newQueue(
					withObserverAll(postgresCfg(postgresDSN(integrationPostgres.addr)), observer),
					WithHandlerContextDecorator(func(ctx context.Context) context.Context {
						return context.WithValue(ctx, key, want)
					}),
				)
				if err != nil {
					t.Fatalf("new postgres queue failed: %v", err)
				}
				return q, uniqueQueueName("handler-context-postgres")
			},
		},
		{
			name: testenv.BackendSQLite,
			newQ: func(t *testing.T, observer Observer) (*Queue, string) {
				q, err := newQueue(
					withObserverAll(sqliteCfg(fmt.Sprintf("%s/handler-context-%d.db", t.TempDir(), time.Now().UnixNano())), observer),
					WithHandlerContextDecorator(func(ctx context.Context) context.Context {
						return context.WithValue(ctx, key, want)
					}),
				)
				if err != nil {
					t.Fatalf("new sqlite queue failed: %v", err)
				}
				return q, uniqueQueueName("handler-context-sqlite")
			},
		},
		{
			name: testenv.BackendNATS,
			newQ: func(t *testing.T, observer Observer) (*Queue, string) {
				q, err := newQueue(
					withObserverAll(natsCfg(integrationNATS.url), observer),
					WithHandlerContextDecorator(func(ctx context.Context) context.Context {
						return context.WithValue(ctx, key, want)
					}),
				)
				if err != nil {
					t.Fatalf("new nats queue failed: %v", err)
				}
				return q, uniqueQueueName("handler-context-nats")
			},
		},
		{
			name: testenv.BackendSQS,
			newQ: func(t *testing.T, observer Observer) (*Queue, string) {
				physical := uniqueQueueName("handler-context-sqs")
				q, err := newQueue(
					withObserverAll(withDefaultQueueAll(sqsCfg(integrationSQS.region, integrationSQS.endpoint, integrationSQS.accessKey, integrationSQS.secretKey), physical), observer),
					WithHandlerContextDecorator(func(ctx context.Context) context.Context {
						return context.WithValue(ctx, key, want)
					}),
				)
				if err != nil {
					t.Fatalf("new sqs queue failed: %v", err)
				}
				return q, physical
			},
		},
		{
			name: testenv.BackendRabbitMQ,
			newQ: func(t *testing.T, observer Observer) (*Queue, string) {
				physical := uniqueQueueName("handler-context-rabbitmq")
				q, err := newQueue(
					withObserverAll(withDefaultQueueAll(rabbitmqCfg(integrationRabbitMQ.url), physical), observer),
					WithHandlerContextDecorator(func(ctx context.Context) context.Context {
						return context.WithValue(ctx, key, want)
					}),
				)
				if err != nil {
					t.Fatalf("new rabbitmq queue failed: %v", err)
				}
				return q, physical
			},
		},
	}

	for _, fx := range fixtures {
		fx := fx
		t.Run(fx.name, func(t *testing.T) {
			if !integrationBackendEnabled(fx.name) {
				t.Skipf("%s integration backend not selected", fx.name)
			}

			okType := uniqueQueueJobType("handler-context:ok:" + fx.name)
			failType := uniqueQueueJobType("handler-context:fail:" + fx.name)
			recorder := newHandlerContextRecorder("", key, want)
			q, queueName := fx.newQ(t, recorder)
			if fx.name != testenv.BackendRedis {
				recorder.setQueueName(queueName)
			}
			q = q.WithWorkers(2)
			if err := q.StartWorkers(context.Background()); err != nil {
				t.Fatalf("start workers failed: %v", err)
			}
			defer func() {
				shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				_ = q.Shutdown(shutdownCtx)
			}()

			okDone := make(chan struct{}, 1)
			var failedCalls atomic.Int64

			q.Register(okType, func(_ context.Context, _ Message) error {
				select {
				case okDone <- struct{}{}:
				default:
				}
				return nil
			})
			q.Register(failType, func(_ context.Context, _ Message) error {
				failedCalls.Add(1)
				return errors.New("boom")
			})

			if _, err := q.Dispatch(NewJob(okType).OnQueue(queueName).Retry(1)); err != nil {
				t.Fatalf("dispatch success job failed: %v", err)
			}
			select {
			case <-okDone:
			case <-time.After(12 * time.Second):
				t.Fatal("timed out waiting for success job")
			}

			_, _ = q.Dispatch(NewJob(failType).OnQueue(queueName).Retry(0))
			waitForContextScenario(t, 12*time.Second, func() bool {
				return failedCalls.Load() >= 1
			})

			waitForContextScenario(t, 12*time.Second, func() bool {
				return recorder.decoratedEqualsTotal(EventProcessStarted) &&
					recorder.decoratedEqualsTotal(EventProcessSucceeded) &&
					recorder.decoratedEqualsTotal(EventProcessFailed)
			})
		})
	}
}

func waitForContextScenario(t *testing.T, timeout time.Duration, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("timed out waiting for context propagation condition")
}
