package temporalqueue

import (
	"context"
	"crypto/tls"
	"go.temporal.io/sdk/workflow"
	"math"
	"testing"
	"time"

	"github.com/goforj/queue"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
)

// testConfig uses a lazy client so policy tests cannot accidentally require a server.
func testConfig() Config {
	return Config{DefaultActivityTimeout: time.Second, ShutdownTimeout: time.Second}
}

// TestConfigRejectsConflicts verifies invalid resources fail before connecting or accepting work.
func TestConfigRejectsConflicts(t *testing.T) {
	cases := map[string]func(*Config){
		"missing activity timeout": func(c *Config) { c.DefaultActivityTimeout = 0 },
		"short activity timeout":   func(c *Config) { c.DefaultActivityTimeout = time.Millisecond },
		"missing shutdown timeout": func(c *Config) { c.ShutdownTimeout = 0 },
		"sessions":                 func(c *Config) { c.WorkerOptions.EnableSessionWorker = true },
		"fatal callback":           func(c *Config) { c.WorkerOptions.OnFatalError = func(error) {} },
		"workflow disabled":        func(c *Config) { c.WorkerOptions.DisableWorkflowWorker = true },
		"local only":               func(c *Config) { c.WorkerOptions.LocalActivityWorkerOnly = true },
		"stop over budget":         func(c *Config) { c.WorkerOptions.WorkerStopTimeout = 2 * time.Second },
		"negative stop budget":     func(c *Config) { c.WorkerOptions.WorkerStopTimeout = -time.Second },
		"unverified TLS":           func(c *Config) { c.ClientOptions.ConnectionOptions.TLS = &tls.Config{InsecureSkipVerify: true} },
		"plaintext credential":     func(c *Config) { c.ClientOptions.Credentials = client.NewAPIKeyStaticCredentials("test-credential") },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := testConfig()
			mutate(&cfg)
			if q, err := NewWithConfig(cfg); err == nil {
				_ = q.Shutdown(t.Context())
				t.Fatal("invalid resource accepted")
			}
		})
	}
}

// TestLazyResourceOwnership verifies no-worker shutdown and borrowed-binding lifetime.
func TestLazyResourceOwnership(t *testing.T) {
	binding, err := NewClientBinding(client.Options{}, SerializationLimits{})
	if err != nil {
		t.Fatal(err)
	}
	cfg := testConfig()
	cfg.Binding = binding
	q, err := NewWithConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Client(q); err != nil {
		t.Fatal(err)
	}
	if err := binding.Close(t.Context()); err == nil {
		t.Fatal("closed borrowed client")
	}
	if err := q.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := q.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := binding.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := NewWithConfig(cfg); err == nil {
		t.Fatal("reused closed binding")
	}
}

// TestConstructorAndFluentCapacityRejectConflict verifies deferred errors retain cleanup.
func TestConstructorAndFluentCapacityRejectConflict(t *testing.T) {
	cfg := testConfig()
	cfg.WorkerOptions = worker.Options{MaxConcurrentActivityExecutionSize: 3}
	if _, err := NewWithConfig(cfg, queue.WithWorkers(2)); err == nil {
		t.Fatal("constructor conflict accepted")
	}
	q, err := NewWithConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	q.WithWorkers(2)
	if err := q.Ready(t.Context()); err == nil {
		t.Fatal("readiness ignored deferred conflict")
	}
	if err := q.StartWorkers(t.Context()); err == nil {
		t.Fatal("startup ignored deferred conflict")
	}
	if err := q.Shutdown(context.Background()); err == nil {
		t.Fatal("shutdown lost deferred conflict")
	}
}

// TestWorkerTopologyRejectsUnsafePolicies covers role, deployment, capacity and numeric validation before clients exist.
func TestWorkerTopologyRejectsUnsafePolicies(t *testing.T) {
	for name, mutate := range map[string]func(*Config){
		"invalid UTF-8 route": func(c *Config) { c.DefaultQueue = "queue\xff" },
		"partial split":       func(c *Config) { c.WorkflowTaskQueue = "workflow" },
		"invalid route":       func(c *Config) { c.WorkflowTaskQueue = " trailing "; c.ActivityTaskQueue = "activity" },
		"split combined options": func(c *Config) {
			c.WorkflowTaskQueue = "workflow"
			c.ActivityTaskQueue = "activity"
			c.WorkerOptions.MaxConcurrentActivityExecutionSize = 4
		},
		"workflow remote options": func(c *Config) {
			c.WorkflowTaskQueue = "workflow"
			c.ActivityTaskQueue = "activity"
			c.WorkflowWorkerOptions.MaxConcurrentActivityExecutionSize = 4
		},
		"negative capacity":      func(c *Config) { c.WorkerOptions.MaxConcurrentActivityExecutionSize = -1 },
		"excess capacity":        func(c *Config) { c.WorkerOptions.MaxConcurrentActivityExecutionSize = 65537 },
		"one workflow poller":    func(c *Config) { c.WorkerOptions.MaxConcurrentWorkflowTaskPollers = 1 },
		"one workflow execution": func(c *Config) { c.WorkerOptions.MaxConcurrentWorkflowTaskExecutionSize = 1 },
		"excess pollers":         func(c *Config) { c.WorkerOptions.MaxConcurrentActivityTaskPollers = 129 },
		"negative rate":          func(c *Config) { c.WorkerOptions.WorkerActivitiesPerSecond = -1 },
		"nan rate":               func(c *Config) { c.WorkerOptions.WorkerLocalActivitiesPerSecond = math.NaN() },
		"infinite rate":          func(c *Config) { c.WorkerOptions.TaskQueueActivitiesPerSecond = math.Inf(1) },
		"incomplete deployment":  func(c *Config) { c.WorkerOptions.DeploymentOptions.UseVersioning = true },
		"unversioned default": func(c *Config) {
			c.WorkerOptions.DeploymentOptions.DefaultVersioningBehavior = workflow.VersioningBehaviorPinned
		},
		"conflicting build ids": func(c *Config) {
			c.WorkerOptions.DeploymentOptions.Version.BuildID = "deployment-build"
			c.WorkerOptions.BuildID = "legacy-build"
		},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := testConfig()
			mutate(&cfg)
			if q, err := NewWithConfig(cfg); err == nil {
				_ = q.Shutdown(t.Context())
				t.Fatal("unsafe policy accepted")
			}
		})
	}
	for name, option := range map[string]queue.Option{"store": queue.WithStore(nil), "clock": queue.WithClock(nil), "resource": queue.WithResourceID("scope"), "legacy": queue.WithLegacyDirectEnvelope(), "capacity": queue.WithWorkers(0)} {
		t.Run(name, func(t *testing.T) {
			if q, err := NewWithConfig(testConfig(), option); err == nil {
				_ = q.Shutdown(t.Context())
				t.Fatal("incompatible root override accepted")
			}
		})
	}
}
