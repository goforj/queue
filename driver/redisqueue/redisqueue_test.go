package redisqueue

import (
	"testing"

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
}

func TestServerConfig_LoggerAndLogLevelPassthrough(t *testing.T) {
	logger := serverLoggerStub{}
	cfg := serverConfig(Config{
		ServerLogger:   logger,
		ServerLogLevel: ServerLogLevelError,
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
