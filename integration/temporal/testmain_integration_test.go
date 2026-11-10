//go:build integration

package temporal

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/goforj/queue/integration/testenv"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

var temporalAddress string

// TestMain owns the pinned public-driver acceptance server for the selected backend matrix.
func TestMain(m *testing.M) {
	if address := os.Getenv("QUEUE_DRIVER_WORKER_ADDRESS"); address != "" {
		temporalAddress = address
		os.Exit(m.Run())
	}
	if !testenv.BackendEnabled(os.Getenv("INTEGRATION_BACKEND"), testenv.BackendTemporal) {
		os.Exit(m.Run())
	}
	os.Exit(runTemporalSuite(m.Run))
}

// runTemporalSuite reports partial-startup and cleanup failures without borrowing another backend's resources.
func runTemporalSuite(run func() int) (code int) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: testcontainers.ContainerRequest{Image: testenv.TemporalServerImage, Cmd: []string{"server", "start-dev", "--ip", "0.0.0.0", "--headless", "--log-level", "error"}, ExposedPorts: []string{"7233/tcp"}, WaitingFor: wait.ForListeningPort("7233/tcp").WithStartupTimeout(45 * time.Second)}, Started: true})
	if container != nil {
		defer func() {
			cleanup, stop := context.WithTimeout(context.Background(), 15*time.Second)
			defer stop()
			if err := container.Terminate(cleanup); err != nil {
				fmt.Fprintln(os.Stderr, "Temporal acceptance server cleanup failed")
				code = 1
			}
		}()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "Temporal acceptance server startup failed")
		return 1
	}
	host, err := container.Host(ctx)
	if err != nil {
		return 1
	}
	port, err := container.MappedPort(ctx, "7233/tcp")
	if err != nil {
		return 1
	}
	temporalAddress = fmt.Sprintf("%s:%s", host, port.Port())
	return run()
}

// requireTemporalServer refuses to count a skipped selected backend as acceptance evidence.
func requireTemporalServer(t *testing.T) {
	t.Helper()
	if !testenv.BackendEnabled(os.Getenv("INTEGRATION_BACKEND"), testenv.BackendTemporal) {
		t.Skip("Temporal backend not selected")
	}
	if temporalAddress == "" {
		t.Fatal("selected Temporal acceptance server is unavailable")
	}
}
