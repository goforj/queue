package bus

import (
	"context"
	"errors"
	"time"
)

var ErrNotFound = errors.New("bus record not found")

type ChainNode struct {
	NodeID string
	Job    wireJob
}

type ChainRecord struct {
	ChainID    string
	DispatchID string
	Queue      string
	Nodes      []ChainNode
	CreatedAt  time.Time
}

type ChainState struct {
	ChainID    string
	DispatchID string
	Queue      string
	Nodes      []ChainNode
	NextIndex  int
	Completed  bool
	Failed     bool
	Failure    string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

type BatchRecord struct {
	BatchID     string
	DispatchID  string
	Name        string
	Queue       string
	AllowFailed bool
	Jobs        []BatchJob
	CreatedAt   time.Time
}

type BatchJob struct {
	JobID string
	Job   wireJob
}

type BatchState struct {
	BatchID     string
	DispatchID  string
	Name        string
	Queue       string
	AllowFailed bool
	Total       int
	Pending     int
	Processed   int
	Failed      int
	Cancelled   bool
	Completed   bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type Store interface {
	CreateChain(ctx context.Context, rec ChainRecord) error
	AdvanceChain(ctx context.Context, chainID string, completedNode string) (next *ChainNode, done bool, err error)
	FailChain(ctx context.Context, chainID string, cause error) error
	GetChain(ctx context.Context, chainID string) (ChainState, error)

	CreateBatch(ctx context.Context, rec BatchRecord) error
	MarkBatchJobStarted(ctx context.Context, batchID, jobID string) error
	MarkBatchJobSucceeded(ctx context.Context, batchID, jobID string) (BatchState, bool, error)
	MarkBatchJobFailed(ctx context.Context, batchID, jobID string, cause error) (BatchState, bool, error)
	CancelBatch(ctx context.Context, batchID string) error
	GetBatch(ctx context.Context, batchID string) (BatchState, error)

	MarkCallbackInvoked(ctx context.Context, key string) (bool, error)
	Prune(ctx context.Context, before time.Time) error
}
