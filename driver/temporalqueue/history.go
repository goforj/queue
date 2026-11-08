package temporalqueue

import (
	"bytes"
	"context"
	"errors"
	"io"

	"github.com/goforj/queue/internal/workflow"
	"go.temporal.io/api/serviceerror"

	"go.temporal.io/api/common/v1"
	"go.temporal.io/api/history/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"google.golang.org/protobuf/proto"
)

// historyPages bounds each server page and total original-history material before reduction.
type historyPages struct {
	ctx                  context.Context
	client               client.Client
	namespace, id, runID string
	page                 []*history.HistoryEvent
	cursor               int
	token                []byte
	started, finished    bool
	pages, bytes         int
	err                  error
}

// HasNext performs bounded pagination without retaining previously reduced history pages.
func (h *historyPages) HasNext() bool {
	if h.err != nil {
		return true
	}
	for h.cursor == len(h.page) && !h.finished {
		if h.started && len(h.token) == 0 {
			h.finished = true
			break
		}
		if h.pages >= 1000 {
			h.err = errors.New("Temporal history page budget exceeded")
			return true
		}
		response, err := h.client.WorkflowService().GetWorkflowExecutionHistory(h.ctx, &workflowservice.GetWorkflowExecutionHistoryRequest{Namespace: h.namespace, Execution: &common.WorkflowExecution{WorkflowId: h.id, RunId: h.runID}, MaximumPageSize: 128, NextPageToken: h.token})
		if err != nil {
			h.err = err
			return true
		}
		h.pages++
		h.bytes += proto.Size(response)
		if h.bytes > 64<<20 || len(response.RawHistory) > 0 {
			h.err = errors.New("Temporal original history exceeds decoding budget")
			return true
		}
		if len(response.NextPageToken) > 0 && bytes.Equal(h.token, response.NextPageToken) {
			h.err = errors.New("Temporal history pagination made no progress")
			return true
		}
		h.page = response.GetHistory().GetEvents()
		h.cursor = 0
		h.token = append(h.token[:0], response.NextPageToken...)
		h.started = true
	}
	return h.cursor < len(h.page)
}

// Next returns one authoritative event or the error that stopped bounded pagination.
func (h *historyPages) Next() (*history.HistoryEvent, error) {
	if h.err != nil {
		err := h.err
		h.err = nil
		h.finished = true
		return nil, err
	}
	if !h.HasNext() {
		return nil, io.EOF
	}
	if h.err != nil {
		return h.Next()
	}
	event := h.page[h.cursor]
	h.cursor++
	return event, nil
}

// historyReadError preserves actionable service categories without copying server diagnostics or treating outages as absence.
func historyReadError(ctx context.Context, err error) error {
	var missing *serviceerror.NotFound
	var denied *serviceerror.PermissionDenied
	var invalid *serviceerror.InvalidArgument
	switch {
	case errors.As(err, &missing):
		return workflow.ErrNotFound
	case errors.As(err, &denied):
		return serviceerror.NewPermissionDenied("Temporal history access denied", "")
	case errors.As(err, &invalid):
		return serviceerror.NewInvalidArgument("Temporal history request invalid")
	case ctx.Err() != nil:
		return ctx.Err()
	default:
		return serviceerror.NewUnavailable("Temporal history unavailable")
	}
}
