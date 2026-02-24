//go:build integration

package root_test

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	code := m.Run()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if integrationNATS.container != nil {
		_ = integrationNATS.container.Terminate(ctx)
	}
	if integrationRabbitMQ.container != nil {
		_ = integrationRabbitMQ.container.Terminate(ctx)
	}
	if integrationSQS.container != nil {
		_ = integrationSQS.container.Terminate(ctx)
	}
	if integrationRedis.container != nil {
		_ = integrationRedis.container.Terminate(ctx)
	}
	if integrationPostgres.container != nil {
		_ = integrationPostgres.container.Terminate(ctx)
	}
	if integrationMySQL.container != nil {
		_ = integrationMySQL.container.Terminate(ctx)
	}
	os.Exit(code)
}
