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

func TestReadyIntegration_AllBackends(t *testing.T) {
	fixtures := []struct {
		name string
		cfg  func(t *testing.T) any
	}{
		{name: testenv.BackendNull, cfg: func(_ *testing.T) any { return nullCfg() }},
		{name: testenv.BackendSync, cfg: func(_ *testing.T) any { return syncCfg() }},
		{name: testenv.BackendWorkerpool, cfg: func(_ *testing.T) any { return workerpoolCfg() }},
		{name: testenv.BackendSQLite, cfg: func(t *testing.T) any {
			return sqliteCfg(fmt.Sprintf("%s/ready-%d.db", t.TempDir(), time.Now().UnixNano()))
		}},
		{name: testenv.BackendRedis, cfg: func(_ *testing.T) any { return redisCfg(integrationRedis.addr) }},
		{name: testenv.BackendMySQL, cfg: func(_ *testing.T) any { return mysqlCfg(mysqlDSN(integrationMySQL.addr)) }},
		{name: testenv.BackendPostgres, cfg: func(_ *testing.T) any { return postgresCfg(postgresDSN(integrationPostgres.addr)) }},
		{name: testenv.BackendNATS, cfg: func(_ *testing.T) any { return natsCfg(integrationNATS.url) }},
		{name: testenv.BackendSQS, cfg: func(_ *testing.T) any {
			return sqsCfg(integrationSQS.region, integrationSQS.endpoint, integrationSQS.accessKey, integrationSQS.secretKey)
		}},
		{name: testenv.BackendRabbitMQ, cfg: func(_ *testing.T) any { return rabbitmqCfg(integrationRabbitMQ.url) }},
	}

	for _, fx := range fixtures {
		fx := fx
		t.Run(fx.name, func(t *testing.T) {
			if !integrationBackendEnabled(fx.name) {
				t.Skipf("%s integration backend not selected", fx.name)
			}

			q, err := newQueue(fx.cfg(t))
			if err != nil {
				t.Fatalf("new queue failed: %v", err)
			}
			t.Cleanup(func() { _ = q.Shutdown(context.Background()) })

			if err := q.Ready(context.Background()); err != nil {
				t.Fatalf("queue ready failed: %v", err)
			}
			if err := queue.Ready(context.Background(), q); err != nil {
				t.Fatalf("package ready failed: %v", err)
			}

			canceled, cancel := context.WithCancel(context.Background())
			cancel()
			if err := q.Ready(canceled); !errors.Is(err, context.Canceled) {
				t.Fatalf("expected canceled ready context, got %v", err)
			}
		})
	}
}
