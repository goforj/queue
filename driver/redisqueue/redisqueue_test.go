package redisqueue

import (
	"testing"

	"github.com/hibiken/asynq"
)

type asynqLoggerStub struct{}

func (asynqLoggerStub) Debug(...interface{}) {}
func (asynqLoggerStub) Info(...interface{})  {}
func (asynqLoggerStub) Warn(...interface{})  {}
func (asynqLoggerStub) Error(...interface{}) {}
func (asynqLoggerStub) Fatal(...interface{}) {}

func TestAsynqServerConfig_Defaults(t *testing.T) {
	cfg := asynqServerConfig(Config{}, 7)
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

func TestAsynqServerConfig_LoggerAndLogLevelPassthrough(t *testing.T) {
	logger := asynqLoggerStub{}
	cfg := asynqServerConfig(Config{
		AsynqLogger:   logger,
		AsynqLogLevel: asynq.ErrorLevel,
	}, 3)
	if cfg.Concurrency != 3 {
		t.Fatalf("expected concurrency 3, got %d", cfg.Concurrency)
	}
	if cfg.Logger == nil {
		t.Fatal("expected logger passthrough")
	}
	if cfg.LogLevel != asynq.ErrorLevel {
		t.Fatalf("expected error log level, got %v", cfg.LogLevel)
	}
}
