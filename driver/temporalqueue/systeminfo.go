package temporalqueue

import (
	"errors"

	"go.temporal.io/sdk/worker"
)

// guardedSystemInfo joins heartbeat providers that the SDK also invokes during worker rollback and stop.
type guardedSystemInfo struct {
	inner worker.SysInfoProvider
	gate  *admission
}

// CpuUsage retains the provider until its actual return and hides provider-controlled diagnostics.
func (p *guardedSystemInfo) CpuUsage(ctx *worker.SysInfoContext) (float64, error) {
	return p.usage(func() (float64, error) { return p.inner.CpuUsage(ctx) })
}

// MemoryUsage fences memory sampling against application dependency cleanup.
func (p *guardedSystemInfo) MemoryUsage(ctx *worker.SysInfoContext) (float64, error) {
	return p.usage(func() (float64, error) { return p.inner.MemoryUsage(ctx) })
}

// usage preserves successful samples while preventing provider errors and panics from escaping SDK goroutines.
func (p *guardedSystemInfo) usage(sample func() (float64, error)) (value float64, err error) {
	defer func() {
		if recover() != nil || err != nil {
			value = 0
			err = errors.New("Temporal worker system information unavailable")
		}
	}()
	release, err := p.gate.enter()
	if err != nil {
		return 0, err
	}
	defer release()
	return sample()
}
