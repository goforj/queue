package redisqueue

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/goforj/queue/busruntime"
	"github.com/goforj/queue/queueconfig"
	backend "github.com/hibiken/asynq"
)

type serverLoggerStub struct{}

func (serverLoggerStub) Debug(...interface{}) {}
func (serverLoggerStub) Info(...interface{})  {}
func (serverLoggerStub) Warn(...interface{})  {}
func (serverLoggerStub) Error(...interface{}) {}
func (serverLoggerStub) Fatal(...interface{}) {}

func TestServerConfig_Defaults(t *testing.T) {
	cfg := serverConfig(Config{}, 7)
	if cfg.Concurrency != 7 {
		t.Fatalf("expected concurrency 7, got %d", cfg.Concurrency)
	}
	if cfg.Logger != nil {
		t.Fatal("expected nil logger by default")
	}
	if cfg.LogLevel != 0 {
		t.Fatalf("expected unset log level, got %v", cfg.LogLevel)
	}
	if !reflect.DeepEqual(cfg.Queues, map[string]int{"default": 1}) {
		t.Fatalf("expected default queue map, got %#v", cfg.Queues)
	}
	if cfg.ShutdownTimeout != 0 {
		t.Fatalf("expected unset shutdown timeout by default, got %s", cfg.ShutdownTimeout)
	}
}

func TestServerConfig_LoggerAndLogLevelPassthrough(t *testing.T) {
	logger := serverLoggerStub{}
	cfg := serverConfig(Config{
		DriverBaseConfig: queueconfig.DriverBaseConfig{
			Logger: logger,
		},
		ServerLogLevel: ServerLogLevelError,
		Queues: map[string]int{
			"critical": 5,
			"default":  3,
			"low":      1,
		},
	}, 3)
	if cfg.Concurrency != 3 {
		t.Fatalf("expected concurrency 3, got %d", cfg.Concurrency)
	}
	if cfg.Logger == nil {
		t.Fatal("expected logger passthrough")
	}
	if cfg.LogLevel != backend.ErrorLevel {
		t.Fatalf("expected error log level, got %v", cfg.LogLevel)
	}
	if !reflect.DeepEqual(cfg.Queues, map[string]int{"critical": 5, "default": 3, "low": 1}) {
		t.Fatalf("unexpected queues map: %#v", cfg.Queues)
	}
}

func TestServerConfig_GenericLoggerPassthrough(t *testing.T) {
	logger := serverLoggerStub{}
	cfg := serverConfig(Config{
		DriverBaseConfig: queueconfig.DriverBaseConfig{
			Logger: logger,
		},
	}, 2)
	if cfg.Concurrency != 2 {
		t.Fatalf("expected concurrency 2, got %d", cfg.Concurrency)
	}
	if cfg.Logger == nil {
		t.Fatal("expected generic logger passthrough")
	}
}

func TestServerConfig_ShutdownTimeoutPassthrough(t *testing.T) {
	cfg := serverConfig(Config{ShutdownTimeout: 5 * time.Second}, 2)
	if cfg.ShutdownTimeout != 5*time.Second {
		t.Fatalf("expected shutdown timeout passthrough, got %s", cfg.ShutdownTimeout)
	}
}

// TestServerConfig_UncommittedErrorsDoNotCountAsFailures verifies the configured predicate preserves retry count while Asynq still has transport capacity.
func TestServerConfig_UncommittedErrorsDoNotCountAsFailures(t *testing.T) {
	isFailure := serverConfig(Config{}, 1).IsFailure
	if isFailure == nil {
		t.Fatal("expected failure classifier")
	}
	cause := errors.New("outcome store unavailable")
	if isFailure(nil) {
		t.Fatal("nil result must not count as a failure")
	}
	if !isFailure(cause) {
		t.Fatal("application error must count as a failure")
	}
	if isFailure(busruntime.Uncommitted(cause)) {
		t.Fatal("uncommitted error must not count as a failure")
	}
	if isFailure(fmt.Errorf("commit callback: %w", busruntime.Uncommitted(cause))) {
		t.Fatal("wrapped uncommitted error must not count as a failure")
	}
	if isFailure(backend.ErrLeaseExpired) {
		t.Fatal("lease recovery must not consume the application retry counter")
	}
	if !isFailure(busruntime.Permanent(cause)) {
		t.Fatal("permanent application error must count as a failure")
	}
}

// TestRedisRetryDelaySeparatesInfrastructureFromApplicationBackoff verifies recovery does not inherit randomized application delays.
func TestRedisRetryDelaySeparatesInfrastructureFromApplicationBackoff(t *testing.T) {
	cause := errors.New("failed")
	if got := redisRetryDelay(0, busruntime.Uncommitted(cause), backend.NewTask("job", nil)); got != time.Second {
		t.Fatalf("uncommitted retry delay = %v, want 1s", got)
	}
	if got := redisRetryDelay(0, backend.ErrLeaseExpired, backend.NewTask("job", nil)); got != time.Second {
		t.Fatalf("lease recovery delay = %v, want 1s", got)
	}
	if got := redisRetryDelay(0, cause, backend.NewTask("job", nil)); got < 15*time.Second {
		t.Fatalf("application retry delay = %v, want Asynq default", got)
	}
}

func TestNormalizeQueues(t *testing.T) {
	got := normalizeQueues(map[string]int{"": 2, " critical ": 3, "zero": 0, "neg": -1}, "")
	want := map[string]int{"default": 2, "critical": 3}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalize queues mismatch: got=%#v want=%#v", got, want)
	}

	fallback := normalizeQueues(nil, "low")
	if !reflect.DeepEqual(fallback, map[string]int{"low": 1}) {
		t.Fatalf("fallback queues mismatch: got=%#v", fallback)
	}

	targetQueues := normalizeQueues(map[string]int{"reports": 2, "billing_critical": 1}, "billing_default")
	wantTargetQueues := map[string]int{"billing_reports": 2, "billing_critical": 1}
	if !reflect.DeepEqual(targetQueues, wantTargetQueues) {
		t.Fatalf("target queues mismatch: got=%#v want=%#v", targetQueues, wantTargetQueues)
	}
}

func TestServerLogLevel(t *testing.T) {
	tests := []struct {
		level   ServerLogLevel
		want    backend.LogLevel
		wantSet bool
	}{
		{level: ServerLogLevelDefault, wantSet: false},
		{level: ServerLogLevelDebug, want: backend.DebugLevel, wantSet: true},
		{level: ServerLogLevelInfo, want: backend.InfoLevel, wantSet: true},
		{level: ServerLogLevelWarn, want: backend.WarnLevel, wantSet: true},
		{level: ServerLogLevelError, want: backend.ErrorLevel, wantSet: true},
		{level: ServerLogLevelFatal, want: backend.FatalLevel, wantSet: true},
	}
	for _, tc := range tests {
		got, ok := serverLogLevel(tc.level)
		if ok != tc.wantSet {
			t.Fatalf("level=%v expected set=%t got set=%t", tc.level, tc.wantSet, ok)
		}
		if ok && got != tc.want {
			t.Fatalf("level=%v expected mapped=%v got=%v", tc.level, tc.want, got)
		}
	}
}
