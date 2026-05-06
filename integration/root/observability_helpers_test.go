//go:build integration

package root_test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

var integrationRedis struct {
	container testcontainers.Container
	addr      string
}

func ensureRedis(t testing.TB) {
	t.Helper()
	if integrationRedis.addr != "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	req := testcontainers.ContainerRequest{
		Image:        "redis:7",
		ExposedPorts: []string{"6379/tcp"},
		WaitingFor:   wait.ForListeningPort("6379/tcp").WithStartupTimeout(30 * time.Second),
	}
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("start redis container: %v", err)
	}
	host, err := container.Host(ctx)
	if err != nil {
		_ = container.Terminate(ctx)
		t.Fatalf("redis host: %v", err)
	}
	port, err := container.MappedPort(ctx, "6379/tcp")
	if err != nil {
		_ = container.Terminate(ctx)
		t.Fatalf("redis port: %v", err)
	}
	integrationRedis.container = container
	integrationRedis.addr = net.JoinHostPort(host, port.Port())
}

func requireScenarioNoErr(t *testing.T, step string, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("[%s] %v", step, err)
	}
}

func dispatchErr[T any](result T, err error) error {
	return err
}

func requireScenarioTrue(t *testing.T, step string, ok bool, format string, args ...any) {
	t.Helper()
	if !ok {
		t.Fatalf("[%s] "+format, append([]any{step}, args...)...)
	}
}
