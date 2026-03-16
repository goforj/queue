package queue

import "testing"

func TestNoopLoggerImplementsLogger(t *testing.T) {
	var logger Logger = NoopLogger{}
	if logger == nil {
		t.Fatal("expected noop logger")
	}
	logger.Debug("debug")
	logger.Info("info")
	logger.Warn("warn")
	logger.Error("error")
	logger.Fatal("fatal")
}
