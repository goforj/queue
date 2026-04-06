package redisqueue

import (
	"reflect"
	"testing"
	"time"

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
		ServerLogger:   logger,
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
