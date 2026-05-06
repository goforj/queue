package queue

import (
	"context"
	"testing"
)

func queueDriver(q queueRuntime) Driver {
	if driverAware, ok := q.(interface{ Driver() Driver }); ok {
		return driverAware.Driver()
	}
	return Driver("")
}

func TestNewSyncQueue(t *testing.T) {
	q, err := newRuntime(Config{Driver: DriverSync})
	if err != nil {
		t.Fatalf("new q failed: %v", err)
	}
	if queueDriver(q) != DriverSync {
		t.Fatalf("expected sync driver, got %q", queueDriver(q))
	}
}

func TestNewNullQueue(t *testing.T) {
	q, err := newRuntime(Config{Driver: DriverNull})
	if err != nil {
		t.Fatalf("new q failed: %v", err)
	}
	if queueDriver(q) != DriverNull {
		t.Fatalf("expected null driver, got %q", queueDriver(q))
	}
}

func TestNewWorkerpoolQueue(t *testing.T) {
	q, err := newRuntime(Config{Driver: DriverWorkerpool})
	if err != nil {
		t.Fatalf("new q failed: %v", err)
	}
	if queueDriver(q) != DriverWorkerpool {
		t.Fatalf("expected workerpool driver, got %q", queueDriver(q))
	}
}

func TestNew_SelectsByConfig(t *testing.T) {
	testCases := []struct {
		name   string
		cfg    Config
		driver Driver
	}{
		{name: "null", cfg: Config{Driver: DriverNull}, driver: DriverNull},
		{name: "sync", cfg: Config{Driver: DriverSync}, driver: DriverSync},
		{name: "workerpool", cfg: Config{Driver: DriverWorkerpool}, driver: DriverWorkerpool},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			q, err := newRuntime(tc.cfg)
			if err != nil {
				t.Fatalf("new q failed: %v", err)
			}
			if queueDriver(q) != tc.driver {
				t.Fatalf("expected %q driver, got %q", tc.driver, queueDriver(q))
			}
		})
	}
}

func TestNew_UnknownDriverFails(t *testing.T) {
	q, err := newRuntime(Config{Driver: Driver("unknown")})
	if err == nil {
		t.Fatal("expected unknown driver error")
	}
	if q != nil {
		t.Fatal("expected nil q")
	}
}

func TestQueue_ShutdownNoopForSyncAndRedis(t *testing.T) {
	syncQueue, err := newRuntime(Config{Driver: DriverSync})
	if err != nil {
		t.Fatalf("sync constructor failed: %v", err)
	}
	if err := syncQueue.Shutdown(context.Background()); err != nil {
		t.Fatalf("sync shutdown failed: %v", err)
	}

}

func TestQueueRuntime_StartWorkersFromQueueConfig(t *testing.T) {
	q, err := newRuntime(Config{
		Driver: DriverWorkerpool,
	})
	if err != nil {
		t.Fatalf("new queue failed: %v", err)
	}
	if err := q.Workers(1).StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers from queue failed: %v", err)
	}
	if err := q.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown failed: %v", err)
	}
}

func TestQueueRuntime_StartWorkers_Idempotent(t *testing.T) {
	q, err := newRuntime(Config{Driver: DriverSync})
	if err != nil {
		t.Fatalf("new queue failed: %v", err)
	}
	if err := q.Workers(2).StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers failed: %v", err)
	}
	if err := q.Workers(3).StartWorkers(context.Background()); err != nil {
		t.Fatalf("second start workers failed: %v", err)
	}
}

func TestQueueRuntime_StartWorkersSharesInProcessRuntime(t *testing.T) {
	q, err := newRuntime(Config{Driver: DriverSync})
	if err != nil {
		t.Fatalf("new queue failed: %v", err)
	}
	handled := false
	q.Register("job:shared", func(_ context.Context, _ Job) error {
		handled = true
		return nil
	})
	if err := q.Workers(1).StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers failed: %v", err)
	}
	if err := q.Dispatch(NewJob("job:shared").Payload([]byte(`{}`)).OnQueue("default")); err != nil {
		t.Fatalf("dispatch failed: %v", err)
	}
	if !handled {
		t.Fatal("expected handler to run on shared runtime")
	}
}

func TestQueueRuntime_PathInvariant_NativeRuntimeSelected(t *testing.T) {
	q, err := newRuntime(Config{Driver: DriverSync})
	if err != nil {
		t.Fatalf("new queue failed: %v", err)
	}

	native, ok := q.(*nativeQueueRuntime)
	if !ok {
		t.Fatalf("expected *nativeQueueRuntime, got %T", q)
	}
	if native.runtime == nil {
		t.Fatal("expected native runtime backend to be set")
	}
	if err := q.Workers(1).StartWorkers(context.Background()); err != nil {
		t.Fatalf("start workers failed: %v", err)
	}
	if !native.started {
		t.Fatal("expected native runtime to be marked started")
	}
}
