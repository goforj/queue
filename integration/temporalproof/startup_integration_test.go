//go:build integration

package temporalproof

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
)

type partialProofContainer struct {
	testcontainers.Container
	startup           context.Context
	terminated        int
	cleanupErr        error
	cleanupContextErr error
	startupContextErr error
	cleanupBudget     time.Duration
}

// Terminate verifies failed startup receives fresh bounded cleanup instead of
// inheriting a canceled creation context or relying on the optional reaper.
func (c *partialProofContainer) Terminate(ctx context.Context, _ ...testcontainers.TerminateOption) error {
	c.terminated++
	c.cleanupContextErr = ctx.Err()
	c.startupContextErr = c.startup.Err()
	if deadline, ok := ctx.Deadline(); ok {
		c.cleanupBudget = time.Until(deadline)
	}
	return c.cleanupErr
}

// TestProofSuiteCleansFailedStartup injects the dependency's partial-resource
// failure mode without starting a container or depending on Docker/Ryuk timing.
func TestProofSuiteCleansFailedStartup(t *testing.T) {
	for _, test := range []struct {
		name       string
		created    bool
		cleanupErr error
	}{
		{name: "failed before creation"},
		{name: "failed after creation", created: true},
		{name: "cleanup also fails", created: true, cleanupErr: errors.New("synthetic cleanup failure")},
	} {
		t.Run(test.name, func(t *testing.T) {
			resource := &partialProofContainer{cleanupErr: test.cleanupErr}
			called := false
			code := runProofSuite(func() int { called = true; return 0 }, func(ctx context.Context, _ testcontainers.GenericContainerRequest) (testcontainers.Container, error) {
				resource.startup = ctx
				if test.created {
					return resource, errors.New("synthetic startup failure")
				}
				return nil, errors.New("synthetic provider failure")
			})
			if code != 1 || called {
				t.Fatalf("startup failure exit=%d, suite invoked=%t", code, called)
			}
			if !test.created {
				if resource.terminated != 0 {
					t.Fatal("cleanup ran without a created resource")
				}
				return
			}
			if resource.terminated != 1 {
				t.Fatalf("partial resource terminated %d times", resource.terminated)
			}
			if !errors.Is(resource.startupContextErr, context.Canceled) || resource.cleanupContextErr != nil {
				t.Fatalf("startup context=%v, cleanup context=%v", resource.startupContextErr, resource.cleanupContextErr)
			}
			if resource.cleanupBudget <= 0 || resource.cleanupBudget > 15*time.Second {
				t.Fatalf("cleanup budget=%s", resource.cleanupBudget)
			}
		})
	}
}
