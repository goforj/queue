package redisqueue

import (
	"fmt"

	"github.com/goforj/queue"
	"github.com/goforj/queue/internal/driverbridge"
	"github.com/goforj/queue/queueconfig"
	"github.com/goforj/queue/queuecore"
	backend "github.com/hibiken/asynq"
)

// ServerLogger is the Redis worker server logger contract.
// Deprecated: use queue.Logger via Config.DriverBaseConfig.Logger.
type ServerLogger = queue.Logger

// ServerLogLevel configures Redis worker server log verbosity.
type ServerLogLevel int

const (
	// ServerLogLevelDefault leaves backend default log level unchanged.
	ServerLogLevelDefault ServerLogLevel = iota
	ServerLogLevelDebug
	ServerLogLevelInfo
	ServerLogLevelWarn
	ServerLogLevelError
	ServerLogLevelFatal
)

// Config configures the Redis driver module constructor.
type Config struct {
	queueconfig.DriverBaseConfig
	Addr           string
	Password       string
	DB             int
	Queues         map[string]int
	ServerLogger   ServerLogger
	ServerLogLevel ServerLogLevel
}

// New creates a high-level Queue using the Redis backend.
// @group Constructors
//
// Example: redis shorthand constructor
//
//	q, err := redisqueue.New(
//		"127.0.0.1:6379",
//		queue.WithWorkers(4), // optional; default: runtime.NumCPU() (min 1)
//	)
//	if err != nil {
//		return
//	}
//	_ = q
func New(addr string, opts ...queue.Option) (*queue.Queue, error) {
	return NewWithConfig(Config{Addr: addr}, opts...)
}

// NewWithConfig creates a high-level Queue using an explicit Redis driver config.
// @group Constructors
//
// Example: redis config constructor
//
//	q, err := redisqueue.NewWithConfig(
//		redisqueue.Config{
//			DriverBaseConfig: queueconfig.DriverBaseConfig{
//				DefaultQueue: "critical", // default if empty: "default"
//				Observer:     nil,        // default: nil
//			},
//			Addr: "127.0.0.1:6379", // required
//			Password: "",           // optional; default empty
//			DB: 0,                  // optional; default 0
//			ServerLogger: nil,      // optional; default backend logger
//			ServerLogLevel: redisqueue.ServerLogLevelDefault, // optional
//		},
//		queue.WithWorkers(4), // optional; default: runtime.NumCPU() (min 1)
//	)
//	if err != nil {
//		return
//	}
//	_ = q
func NewWithConfig(cfg Config, opts ...queue.Option) (*queue.Queue, error) {
	if cfg.Addr == "" {
		return nil, fmt.Errorf("redis addr is required")
	}
	rootCfg := queue.Config{
		Driver:       queue.DriverRedis,
		DefaultQueue: cfg.DefaultQueue,
		Observer:     cfg.Observer,
		Logger:       cfg.Logger,
	}
	driverBackend := newRedisQueue(newRedisClient(cfg), newRedisInspector(cfg), newRedisTimelineStore(cfg), true)
	q, err := driverbridge.NewQueueFromDriver(rootCfg, driverBackend, func(workers int) (any, error) {
		return newRedisWorker(
			backend.NewServer(backend.RedisClientOpt{
				Addr:     cfg.Addr,
				Password: cfg.Password,
				DB:       cfg.DB,
			}, serverConfig(cfg, workers)),
			backend.NewServeMux(),
			cfg.Observer,
		), nil
	}, opts...)
	if err != nil {
		return nil, err
	}
	return q, nil
}

func serverConfig(cfg Config, workers int) backend.Config {
	serverCfg := backend.Config{Concurrency: workers}
	if queues := normalizeQueues(cfg.Queues, cfg.DefaultQueue); len(queues) > 0 {
		serverCfg.Queues = queues
	}
	if cfg.ServerLogger != nil {
		serverCfg.Logger = cfg.ServerLogger
	} else if cfg.Logger != nil {
		serverCfg.Logger = cfg.Logger
	}
	if level, ok := serverLogLevel(cfg.ServerLogLevel); ok {
		serverCfg.LogLevel = level
	}
	return serverCfg
}

func normalizeQueues(raw map[string]int, fallbackDefault string) map[string]int {
	if len(raw) == 0 {
		return map[string]int{queuecore.NormalizeQueueName(fallbackDefault): 1}
	}
	out := make(map[string]int, len(raw))
	for name, weight := range raw {
		normalized := queuecore.NormalizeQueueName(name)
		if weight <= 0 {
			continue
		}
		out[normalized] = weight
	}
	if len(out) == 0 {
		return map[string]int{queuecore.NormalizeQueueName(fallbackDefault): 1}
	}
	return out
}

func serverLogLevel(level ServerLogLevel) (backend.LogLevel, bool) {
	switch level {
	case ServerLogLevelDebug:
		return backend.DebugLevel, true
	case ServerLogLevelInfo:
		return backend.InfoLevel, true
	case ServerLogLevelWarn:
		return backend.WarnLevel, true
	case ServerLogLevelError:
		return backend.ErrorLevel, true
	case ServerLogLevelFatal:
		return backend.FatalLevel, true
	default:
		return 0, false
	}
}
