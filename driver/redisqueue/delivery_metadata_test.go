package redisqueue

import (
	"bytes"
	"context"
	"testing"

	"github.com/goforj/queue"
	"github.com/goforj/queue/internal/driverbridge"
	backend "github.com/hibiken/asynq"
)

// TestRedisQueueDispatchCarriesDriverMetadataWithoutRetry pins the additive Asynq header wire independently of retry policy.
func TestRedisQueueDispatchCarriesDriverMetadataWithoutRetry(t *testing.T) {
	client := &redisEnqueueClientStub{}
	driver := &redisQueue{client: client}
	payload := []byte{0x00, 0xff, 0x7f}
	metadata := queue.DriverJobMetadata{
		SchemaVersion: queue.DriverJobMetadataVersion,
		DispatchID:    "dsp_redis_direct",
		JobID:         "job_redis_direct",
		ChainID:       "chn_redis_direct",
		BatchID:       "bat_redis_direct",
		Queue:         "critical",
	}
	job := queue.DriverWithMetadata(
		queue.NewJob("reports:build").Payload(payload).OnQueue("critical"),
		metadata,
	)

	if err := driver.Dispatch(context.Background(), job); err != nil {
		t.Fatalf("dispatch direct job: %v", err)
	}
	if client.task == nil {
		t.Fatal("dispatch did not enqueue an Asynq task")
	}
	if client.task.Type() != job.Type || !bytes.Equal(client.task.Payload(), payload) {
		t.Fatalf("physical task = type:%q payload:%v, want type:%q payload:%v", client.task.Type(), client.task.Payload(), job.Type, payload)
	}
	headers := client.task.Headers()
	if len(headers) != 1 {
		t.Fatalf("headers = %#v, want only direct-delivery metadata", headers)
	}
	const wantMetadata = `{"schema_version":1,"dispatch_id":"dsp_redis_direct","job_id":"job_redis_direct","chain_id":"chn_redis_direct","batch_id":"bat_redis_direct","queue":"critical"}`
	if got := headers[redisDriverJobMetadataHeader]; got != wantMetadata {
		t.Fatalf("driver metadata header = %q, want %q", got, wantMetadata)
	}
	if _, ok := headers[redisApplicationMaxRetryHeader]; ok {
		t.Fatalf("job without Retry gained application retry header: %#v", headers)
	}
}

// TestRedisQueueDispatchKeepsMetadataAlongsideRetryReserve verifies both private headers survive one task representation.
func TestRedisQueueDispatchKeepsMetadataAlongsideRetryReserve(t *testing.T) {
	client := &redisEnqueueClientStub{}
	driver := &redisQueue{client: client}
	metadata := queue.DriverJobMetadata{
		SchemaVersion: queue.DriverJobMetadataVersion,
		DispatchID:    "dsp_retry",
		JobID:         "job_retry",
		Queue:         "critical",
	}
	job := queue.DriverWithMetadata(
		queue.NewJob("reports:retry").Payload([]byte("payload")).OnQueue("critical").Retry(2),
		metadata,
	)

	if err := driver.Dispatch(context.Background(), job); err != nil {
		t.Fatalf("dispatch retrying direct job: %v", err)
	}
	headers := client.task.Headers()
	if len(headers) != 2 {
		t.Fatalf("headers = %#v, want metadata and retry reserve", headers)
	}
	if got := headers[redisApplicationMaxRetryHeader]; got != "2" {
		t.Fatalf("application retry header = %q, want 2", got)
	}
	reconstructed := redisJobWithDriverMetadata(queue.NewJob(job.Type).Payload(job.PayloadBytes()), headers)
	if got := queue.DriverMetadata(reconstructed); got != metadata {
		t.Fatalf("reconstructed metadata = %+v, want %+v", got, metadata)
	}
	if got := headers[redisApplicationMaxRetryHeader]; got != "2" {
		t.Fatalf("metadata reconstruction changed retry header to %q", got)
	}
}

// TestRedisWorkerReconstructsDriverMetadataForHandlerAndObserver proves the native worker restores correlation before either consumer path.
func TestRedisWorkerReconstructsDriverMetadataForHandlerAndObserver(t *testing.T) {
	server := &serverStub{}
	var events []queue.Event
	observer := queue.ObserverFunc(func(_ context.Context, event queue.Event) {
		events = append(events, event)
	})
	worker := newRedisWorker(server, backend.NewServeMux(), observer)
	wantMetadata := queue.DriverJobMetadata{
		SchemaVersion: queue.DriverJobMetadataVersion,
		DispatchID:    "dsp_handler",
		JobID:         "job_handler",
		ChainID:       "chn_handler",
		BatchID:       "bat_handler",
		Queue:         "critical",
	}
	payload := []byte(`{"report_id":17}`)
	var handled bool
	worker.Register("reports:direct", func(_ context.Context, job queue.Job) error {
		handled = true
		if job.Type != "reports:direct" || !bytes.Equal(job.PayloadBytes(), payload) {
			t.Fatalf("handler job = type:%q payload:%q", job.Type, job.PayloadBytes())
		}
		if got := queue.DriverMetadata(job); got != wantMetadata {
			t.Fatalf("handler metadata = %+v, want %+v", got, wantMetadata)
		}
		return nil
	})
	if err := worker.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start worker: %v", err)
	}
	task := backend.NewTaskWithHeaders("reports:direct", payload, map[string]string{
		redisDriverJobMetadataHeader: `{"schema_version":1,"dispatch_id":"dsp_handler","job_id":"job_handler","chain_id":"chn_handler","batch_id":"bat_handler","queue":"critical"}`,
	})
	if err := server.lastStartHandler.ProcessTask(context.Background(), task); err != nil {
		t.Fatalf("process direct task: %v", err)
	}
	if !handled {
		t.Fatal("direct handler was not called")
	}
	if len(events) != 2 {
		t.Fatalf("events = %+v, want start and success", events)
	}
	for _, event := range events {
		if event.JobType != "reports:direct" || event.DispatchID != wantMetadata.DispatchID || event.JobID != wantMetadata.JobID || event.ChainID != wantMetadata.ChainID || event.BatchID != wantMetadata.BatchID {
			t.Fatalf("observer did not prefer direct metadata: %+v", event)
		}
	}
}

// TestRedisDriverMetadataReachesMessageHandler exercises the complete root Message path through an Asynq task reconstructed as a Job.
func TestRedisDriverMetadataReachesMessageHandler(t *testing.T) {
	client := &redisEnqueueClientStub{}
	producer := &redisQueue{client: client}
	server := &serverStub{}
	q, err := driverbridge.NewQueueFromDriver(
		queue.Config{Driver: queue.DriverRedis, DefaultQueue: "default"},
		nil,
		producer,
		func(int) (any, error) {
			return newRedisWorker(server, backend.NewServeMux(), nil), nil
		},
	)
	if err != nil {
		t.Fatalf("construct queue: %v", err)
	}
	var message queue.Message
	q.Register("reports:message", func(_ context.Context, delivered queue.Message) error {
		message = delivered
		return nil
	})
	if err := q.StartWorkers(context.Background()); err != nil {
		t.Fatalf("start queue workers: %v", err)
	}
	t.Cleanup(func() {
		if err := q.Shutdown(context.Background()); err != nil {
			t.Errorf("shutdown queue: %v", err)
		}
	})

	result, err := q.Dispatch(queue.NewJob("reports:message").Payload([]byte(`{"report_id":23}`)).OnQueue("critical"))
	if err != nil {
		t.Fatalf("dispatch message: %v", err)
	}
	if client.task == nil {
		t.Fatal("dispatch did not produce an Asynq task")
	}
	if err := server.lastStartHandler.ProcessTask(context.Background(), client.task); err != nil {
		t.Fatalf("process reconstructed task: %v", err)
	}
	if message.SchemaVersion == 0 || message.DispatchID != result.DispatchID || message.JobID == "" || message.JobType != "reports:message" {
		t.Fatalf("message correlation = %+v, dispatch = %+v", message, result)
	}
	var payload struct {
		ReportID int `json:"report_id"`
	}
	if err := message.Bind(&payload); err != nil {
		t.Fatalf("bind delivered message: %v", err)
	}
	if payload.ReportID != 23 {
		t.Fatalf("message payload = %+v, want report_id 23", payload)
	}
}

// TestRedisWorkerIgnoresUnusableDriverMetadata verifies corrupt and future headers preserve ordinary delivery and raw observation.
func TestRedisWorkerIgnoresUnusableDriverMetadata(t *testing.T) {
	tests := []struct {
		name   string
		header string
	}{
		{name: "malformed", header: `{"schema_version":`},
		{name: "unknown version", header: `{"schema_version":2,"dispatch_id":"spoofed"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := &serverStub{}
			var events []queue.Event
			observer := queue.ObserverFunc(func(_ context.Context, event queue.Event) {
				events = append(events, event)
			})
			worker := newRedisWorker(server, backend.NewServeMux(), observer)
			worker.Register("reports:fallback", func(_ context.Context, job queue.Job) error {
				if metadata := queue.DriverMetadata(job); metadata.SchemaVersion != 0 {
					t.Fatalf("handler trusted unusable metadata: %+v", metadata)
				}
				if got := string(job.PayloadBytes()); got != "ordinary-payload" {
					t.Fatalf("handler payload = %q, want ordinary-payload", got)
				}
				return nil
			})
			if err := worker.StartWorkers(context.Background()); err != nil {
				t.Fatalf("start worker: %v", err)
			}
			task := backend.NewTaskWithHeaders("reports:fallback", []byte("ordinary-payload"), map[string]string{
				redisDriverJobMetadataHeader: test.header,
			})
			if err := server.lastStartHandler.ProcessTask(context.Background(), task); err != nil {
				t.Fatalf("process fallback task: %v", err)
			}
			if len(events) != 2 {
				t.Fatalf("events = %+v, want start and success", events)
			}
			for _, event := range events {
				if event.JobType != "reports:fallback" || event.DispatchID != "" || event.JobID != "" || event.ChainID != "" || event.BatchID != "" {
					t.Fatalf("unusable metadata changed raw observation: %+v", event)
				}
			}
		})
	}
}
