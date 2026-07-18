package sqlqueuecore

import (
	"bytes"
	"errors"
	"testing"

	"github.com/goforj/queue/busruntime"
	"github.com/goforj/queue/queuecore"
)

// TestDatabaseDeliveryJobRestoresAttemptMetadata verifies SQL persistence reaches the shared orchestration context intact.
func TestDatabaseDeliveryJobRestoresAttemptMetadata(t *testing.T) {
	wantPayload := []byte(`{"report_id":42}`)
	job := databaseDeliveryJob(&dbJob{
		jobType:   "reports:build",
		payload:   wantPayload,
		queueName: "critical",
		attempt:   2,
		maxRetry:  4,
	})
	opts := queuecore.DriverOptions(job)
	if job.Type != "reports:build" || !bytes.Equal(job.PayloadBytes(), wantPayload) {
		t.Fatalf("delivery job = type:%q payload:%q", job.Type, job.PayloadBytes())
	}
	if opts.QueueName != "critical" || opts.Attempt != 2 || opts.MaxRetry == nil || *opts.MaxRetry != 4 {
		t.Fatalf("delivery options = %+v", opts)
	}
}

// TestClassifyDatabaseFailure verifies persisted attempt counters distinguish application retry from infrastructure redelivery.
func TestClassifyDatabaseFailure(t *testing.T) {
	cause := errors.New("failed")
	tests := []struct {
		name         string
		job          dbJob
		err          error
		want         databaseFailureSettlement
		wantClassErr bool
	}{
		{
			name: "application retry advances attempt",
			job:  dbJob{attempt: 1, maxRetry: 3, backoffMillis: 250},
			err:  cause,
			want: databaseFailureSettlement{state: "pending", attempt: 2, availableAt: 1250},
		},
		{
			name: "permanent failure becomes dead early",
			job:  dbJob{attempt: 0, maxRetry: 3, backoffMillis: 250},
			err:  busruntime.Permanent(cause),
			want: databaseFailureSettlement{state: "dead", attempt: 1},
		},
		{
			name: "exhausted failure becomes dead",
			job:  dbJob{attempt: 3, maxRetry: 3, backoffMillis: 250},
			err:  cause,
			want: databaseFailureSettlement{state: "dead", attempt: 4},
		},
		{
			name: "uncommitted redelivery preserves attempt",
			job:  dbJob{attempt: 2, maxRetry: 3, backoffMillis: 250},
			err:  busruntime.Uncommitted(cause),
			want: databaseFailureSettlement{state: "pending", attempt: 2, availableAt: 1250},
		},
		{
			name:         "success cannot enter failure persistence",
			job:          dbJob{attempt: 0, maxRetry: 3},
			wantClassErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := classifyDatabaseFailure(&test.job, test.err, 1000)
			if test.wantClassErr {
				if err == nil {
					t.Fatalf("classifyDatabaseFailure() = %+v, nil; want error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("classifyDatabaseFailure(): %v", err)
			}
			if got != test.want {
				t.Fatalf("settlement = %+v, want %+v", got, test.want)
			}
		})
	}
}
