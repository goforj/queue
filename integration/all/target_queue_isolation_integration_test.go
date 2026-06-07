//go:build integration

package all_test

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	. "github.com/goforj/queue"
	"github.com/goforj/queue/integration/testenv"
)

func TestIntegrationQueue_TargetQueueIsolation(t *testing.T) {
	fixtures := []struct {
		name string
		cfg  func(t *testing.T, defaultQueue string, sqliteDSN string) any
	}{
		{
			name: testenv.BackendRedis,
			cfg: func(_ *testing.T, defaultQueue string, _ string) any {
				return withDefaultQueue(redisCfg(integrationRedis.addr), defaultQueue)
			},
		},
		{
			name: testenv.BackendMySQL,
			cfg: func(_ *testing.T, defaultQueue string, _ string) any {
				return withDefaultQueue(mysqlCfg(mysqlDSN(integrationMySQL.addr)), defaultQueue)
			},
		},
		{
			name: testenv.BackendPostgres,
			cfg: func(_ *testing.T, defaultQueue string, _ string) any {
				return withDefaultQueue(postgresCfg(postgresDSN(integrationPostgres.addr)), defaultQueue)
			},
		},
		{
			name: testenv.BackendSQLite,
			cfg: func(_ *testing.T, defaultQueue string, sqliteDSN string) any {
				return withDefaultQueue(sqliteCfg(sqliteDSN), defaultQueue)
			},
		},
		{
			name: testenv.BackendNATS,
			cfg: func(_ *testing.T, defaultQueue string, _ string) any {
				return withDefaultQueue(natsCfg(integrationNATS.url), defaultQueue)
			},
		},
		{
			name: testenv.BackendSQS,
			cfg: func(_ *testing.T, defaultQueue string, _ string) any {
				return withDefaultQueue(sqsCfg(integrationSQS.region, integrationSQS.endpoint, integrationSQS.accessKey, integrationSQS.secretKey), defaultQueue)
			},
		},
		{
			name: testenv.BackendRabbitMQ,
			cfg: func(_ *testing.T, defaultQueue string, _ string) any {
				return withDefaultQueue(rabbitmqCfg(integrationRabbitMQ.url), defaultQueue)
			},
		},
	}

	for _, fx := range fixtures {
		fx := fx
		t.Run(fx.name, func(t *testing.T) {
			if !integrationBackendEnabled(fx.name) {
				t.Skipf("%s integration backend not selected", fx.name)
			}
			t.Parallel()

			base := uniqueQueueName("target-isolation")
			appDefault := base + "_app_default"
			billingDefault := base + "_billing_default"
			logicalQueue := "reports"
			appReports := PhysicalQueueName(appDefault, logicalQueue)
			billingReports := PhysicalQueueName(billingDefault, logicalQueue)
			jobType := "target:isolation:" + base
			sqliteDSN := fmt.Sprintf("%s/target-isolation.db", t.TempDir())

			appProducer, err := newQueueRuntime(fx.cfg(t, appDefault, sqliteDSN))
			if err != nil {
				t.Fatalf("new app producer failed: %v", err)
			}
			billingProducer, err := newQueueRuntime(fx.cfg(t, billingDefault, sqliteDSN))
			if err != nil {
				t.Fatalf("new billing producer failed: %v", err)
			}
			appWorker, err := newQueueRuntime(fx.cfg(t, appReports, sqliteDSN))
			if err != nil {
				t.Fatalf("new app worker failed: %v", err)
			}
			billingWorker, err := newQueueRuntime(fx.cfg(t, billingReports, sqliteDSN))
			if err != nil {
				t.Fatalf("new billing worker failed: %v", err)
			}
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				_ = appProducer.Shutdown(ctx)
				_ = billingProducer.Shutdown(ctx)
				_ = appWorker.Shutdown(ctx)
				_ = billingWorker.Shutdown(ctx)
			})

			var appCount atomic.Int32
			var billingCount atomic.Int32
			appWorker.Register(jobType, func(context.Context, Job) error {
				appCount.Add(1)
				return nil
			})
			billingWorker.Register(jobType, func(context.Context, Job) error {
				billingCount.Add(1)
				return nil
			})
			if err := withWorkers(appWorker, 2).StartWorkers(context.Background()); err != nil {
				t.Fatalf("start app worker failed: %v", err)
			}
			if err := withWorkers(billingWorker, 2).StartWorkers(context.Background()); err != nil {
				t.Fatalf("start billing worker failed: %v", err)
			}

			if err := appProducer.Dispatch(NewJob(jobType).Payload([]byte(`{"target":"app"}`)).OnQueue(logicalQueue)); err != nil {
				t.Fatalf("dispatch app job failed: %v", err)
			}
			if err := billingProducer.Dispatch(NewJob(jobType).Payload([]byte(`{"target":"billing"}`)).OnQueue(logicalQueue)); err != nil {
				t.Fatalf("dispatch billing job failed: %v", err)
			}

			waitForQueueWorkflow(t, 20*time.Second, "target-isolated jobs processed", func() bool {
				return appCount.Load() == 1 && billingCount.Load() == 1
			})
			time.Sleep(300 * time.Millisecond)
			if appCount.Load() != 1 || billingCount.Load() != 1 {
				t.Fatalf("expected isolated processing counts app=1 billing=1, got app=%d billing=%d", appCount.Load(), billingCount.Load())
			}
		})
	}
}
