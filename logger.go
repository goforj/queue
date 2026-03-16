package queue

// Logger is a generic runtime logger contract that drivers may use to surface
// their internal worker/server lifecycle output.
type Logger interface {
	Debug(args ...interface{})
	Info(args ...interface{})
	Warn(args ...interface{})
	Error(args ...interface{})
	Fatal(args ...interface{})
}

// NoopLogger disables driver-managed internal logs when passed through config.
type NoopLogger struct{}

func (NoopLogger) Debug(...interface{}) {}
func (NoopLogger) Info(...interface{})  {}
func (NoopLogger) Warn(...interface{})  {}
func (NoopLogger) Error(...interface{}) {}
func (NoopLogger) Fatal(...interface{}) {}
