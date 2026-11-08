package temporalqueue

import (
	"context"
	"errors"
	"sort"

	"go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/workflow"
	"google.golang.org/protobuf/proto"
)

// BoundedContextPropagator declares the maximum decoded header expansion enforced by an adapter.
// Implementations must remain replay-safe in workflow methods and bound their own decoding work.
type BoundedContextPropagator interface {
	workflow.ContextPropagator
	// MaxDecodedBytes bounds total allocation caused by untrusted propagated headers.
	MaxDecodedBytes() int
}

// headerRejectionKey passes deterministic extraction failure to workflow entry without a task retry loop.
type headerRejectionKey struct{}

// guardedPropagator isolates untrusted headers before invoking user adapters.
type guardedPropagator struct {
	inner BoundedContextPropagator
	gate  *admission
}

// MaxDecodedBytes preserves the approved contract when a resource adds its own lifetime fence.
func (p *guardedPropagator) MaxDecodedBytes() int { return p.inner.MaxDecodedBytes() }

// headerBuffer bounds and sorts SDK header iteration for deterministic workflow adapters.
type headerBuffer struct{ values map[string]*common.Payload }

// Set captures proposed headers before they can enter SDK requests.
func (h *headerBuffer) Set(key string, value *common.Payload) { h.values[key] = value }

// Get returns only the isolated preflight-validated payload.
func (h *headerBuffer) Get(key string) (*common.Payload, bool) {
	value, ok := h.values[key]
	return value, ok
}

// ForEachKey uses lexical order so workflow extraction does not inherit Go map order.
func (h *headerBuffer) ForEachKey(fn func(string, *common.Payload) error) error {
	keys := make([]string, 0, len(h.values))
	for key := range h.values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if err := fn(key, h.values[key]); err != nil {
			return err
		}
	}
	return nil
}

// checkedHeaders validates counts and encoded bytes before cloning or custom decoding.
func checkedHeaders(reader workflow.HeaderReader) (*headerBuffer, error) {
	result := &headerBuffer{values: make(map[string]*common.Payload)}
	total := 0
	err := reader.ForEachKey(func(key string, value *common.Payload) error {
		if len(result.values) >= 16 || len(key) > 128 || value == nil {
			return errors.New("Temporal header limits exceeded")
		}
		size := proto.Size(value)
		if size > 4096 || size > 32768-total {
			return errors.New("Temporal header limits exceeded")
		}
		total += size
		result.values[key] = proto.Clone(value).(*common.Payload)
		return nil
	})
	return result, err
}

// propagateFailure replaces adapter-controlled diagnostics and panics with one safe classification.
func propagateFailure(err *error) {
	if recover() != nil {
		*err = invalidAdapterError()
		return
	}
	if *err != nil {
		if adapterUnavailable(*err) {
			*err = unavailableAdapterError()
		} else {
			*err = invalidAdapterError()
		}
	}
}

// Inject validates the complete outgoing header set before forwarding it to the SDK.
func (p *guardedPropagator) Inject(ctx context.Context, writer workflow.HeaderWriter) (err error) {
	defer propagateFailure(&err)
	release, err := p.gate.enter()
	if err != nil {
		return err
	}
	defer release()
	buffer := &headerBuffer{values: make(map[string]*common.Payload)}
	if _, err = convertAdapter(func() (struct{}, error) {
		buffer.values = make(map[string]*common.Payload)
		return struct{}{}, p.inner.Inject(ctx, buffer)
	}); err != nil {
		return err
	}
	checked, err := checkedHeaders(buffer)
	if err != nil {
		return err
	}
	return checked.ForEachKey(func(key string, value *common.Payload) error { writer.Set(key, value); return nil })
}

// Extract bounds remote input and retains adapter lifetime through actual callback return.
func (p *guardedPropagator) Extract(ctx context.Context, reader workflow.HeaderReader) (result context.Context, err error) {
	defer propagateFailure(&err)
	checked, err := checkedHeaders(reader)
	if err != nil {
		return nil, err
	}
	release, err := p.gate.enter()
	if err != nil {
		return nil, err
	}
	defer release()
	result, err = convertAdapter(func() (context.Context, error) { return p.inner.Extract(ctx, checked) })
	if result == nil && err == nil {
		err = errors.New("Temporal context adapter returned nil context")
	}
	return result, err
}

// InjectFromWorkflow preserves replay-safe propagation with bounded output.
func (p *guardedPropagator) InjectFromWorkflow(ctx workflow.Context, writer workflow.HeaderWriter) (err error) {
	defer propagateFailure(&err)
	release, err := p.gate.enter()
	if err != nil {
		return err
	}
	defer release()
	buffer := &headerBuffer{values: make(map[string]*common.Payload)}
	if _, err = convertAdapter(func() (struct{}, error) {
		buffer.values = make(map[string]*common.Payload)
		return struct{}{}, p.inner.InjectFromWorkflow(ctx, buffer)
	}); err != nil {
		return err
	}
	checked, err := checkedHeaders(buffer)
	if err != nil {
		return err
	}
	return checked.ForEachKey(func(key string, value *common.Payload) error { writer.Set(key, value); return nil })
}

// ExtractToWorkflow converts malformed-adapter failures into a deterministic workflow-entry rejection.
func (p *guardedPropagator) ExtractToWorkflow(ctx workflow.Context, reader workflow.HeaderReader) (result workflow.Context, err error) {
	result = ctx
	defer func() {
		if recover() != nil || err != nil || result == nil {
			category := "invalid"
			if adapterUnavailable(err) {
				category = "unavailable"
			}
			result = workflow.WithValue(ctx, headerRejectionKey{}, category)
			err = nil
		}
	}()
	checked, err := checkedHeaders(reader)
	if err != nil {
		return nil, err
	}
	release, err := p.gate.enter()
	if err != nil {
		return nil, err
	}
	defer release()
	return convertAdapter(func() (workflow.Context, error) { return p.inner.ExtractToWorkflow(ctx, checked) })
}
