//go:build integration

package temporal

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/goforj/queue/driver/temporalqueue"
	"go.temporal.io/api/common/v1"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

// typedPayloadResultActivity uses the SDK's typed-activity shortcut for preencoded output.
func typedPayloadResultActivity(_ context.Context, size int) (*common.Payloads, error) {
	return &common.Payloads{Payloads: []*common.Payload{{Metadata: map[string][]byte{"encoding": []byte("binary/plain")}, Data: make([]byte, size)}}}, nil
}

// typedPayloadResultWorkflow leaves result decoding unused so the persisted acceptance boundary is observable.
func typedPayloadResultWorkflow(ctx workflow.Context, size int) error {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 5 * time.Second, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 1}})
	return workflow.ExecuteActivity(ctx, typedPayloadResultActivity, size).Get(ctx, nil)
}

// TestTemporalDriverTypedPayloadResultBounds checks actual SDK bypass handling with both accepted and rejected native results.
func TestTemporalDriverTypedPayloadResultBounds(t *testing.T) {
	requireTemporalServer(t)
	name := fmt.Sprintf("typed-payload-result-%d", time.Now().UnixNano())
	q := newDriverResource(t, name, func(r worker.Registry) error {
		r.RegisterWorkflow(typedPayloadResultWorkflow)
		r.RegisterActivity(typedPayloadResultActivity)
		return nil
	})
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	if err := q.StartWorkers(ctx); err != nil {
		t.Fatal(err)
	}
	sdk, err := temporalqueue.Client(q)
	if err != nil {
		t.Fatal(err)
	}
	for _, size := range []int{512, 300 << 10} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			run, err := sdk.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: fmt.Sprintf("%s-%d", name, size), TaskQueue: name}, typedPayloadResultWorkflow, size)
			if err != nil {
				t.Fatal(err)
			}
			err = run.Get(ctx, nil)
			oversized := size > temporalqueue.DefaultSerializationLimits().MaxPayloadBytes
			if oversized {
				var application *temporal.ApplicationError
				if !errors.As(err, &application) || application.Type() != "goforj.invalid.v1" || !application.NonRetryable() {
					t.Fatal("oversized preencoded activity result accepted or misclassified", err)
				}
			} else if err != nil {
				t.Fatal("valid preencoded result rejected", err)
			}
			completed, failed := 0, 0
			history := sdk.GetWorkflowHistory(ctx, run.GetID(), run.GetRunID(), false, enums.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
			for history.HasNext() {
				event, err := history.Next()
				if err != nil {
					t.Fatal(err)
				}
				if event.GetActivityTaskCompletedEventAttributes() != nil {
					completed++
				}
				if event.GetActivityTaskFailedEventAttributes() != nil {
					failed++
				}
			}
			if oversized && (completed != 0 || failed != 1) {
				t.Fatal("oversized activity result reached completed history", completed, failed)
			}
			if !oversized && (completed != 1 || failed != 0) {
				t.Fatal("valid activity result missing from history", completed, failed)
			}
		})
	}
}
