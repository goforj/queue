package temporalqueue

import (
	"errors"

	"github.com/nexus-rpc/sdk-go/nexus"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

// registrationPlan snapshots application registration once before creating role-specific registries.
type registrationPlan struct {
	workflows  []registeredWorkflow
	activities []registeredActivity
	services   []*nexus.Service
}

// registeredWorkflow retains SDK workflow options without retaining worker ownership.
type registeredWorkflow struct {
	fn      any
	options workflow.RegisterOptions
}

// registeredActivity retains SDK activity options, including struct method prefixes.
type registeredActivity struct {
	fn      any
	options activity.RegisterOptions
}

// RegisterWorkflow records the SDK default workflow registration.
func (p *registrationPlan) RegisterWorkflow(fn interface{}) {
	p.RegisterWorkflowWithOptions(fn, workflow.RegisterOptions{})
}

// RegisterWorkflowWithOptions captures exact SDK workflow registration options.
func (p *registrationPlan) RegisterWorkflowWithOptions(fn interface{}, options workflow.RegisterOptions) {
	p.workflows = append(p.workflows, registeredWorkflow{fn: fn, options: options})
}

// RegisterActivity records the SDK default activity registration.
func (p *registrationPlan) RegisterActivity(fn interface{}) {
	p.RegisterActivityWithOptions(fn, activity.RegisterOptions{})
}

// RegisterActivityWithOptions captures exact SDK activity registration options.
func (p *registrationPlan) RegisterActivityWithOptions(fn interface{}, options activity.RegisterOptions) {
	p.activities = append(p.activities, registeredActivity{fn: fn, options: options})
}

// RegisterNexusService captures a service for the role that actually polls Nexus tasks.
func (p *registrationPlan) RegisterNexusService(service *nexus.Service) {
	p.services = append(p.services, service)
}

// RegisterDynamicWorkflow rejects reserved-name shadowing in every role.
func (p *registrationPlan) RegisterDynamicWorkflow(interface{}, workflow.DynamicRegisterOptions) {
	panic("dynamic Temporal workflows are unsupported")
}

// RegisterDynamicActivity rejects reserved-name shadowing in every role.
func (p *registrationPlan) RegisterDynamicActivity(interface{}, activity.DynamicRegisterOptions) {
	panic("dynamic Temporal activities are unsupported")
}

// applicationPlan validates catalog ownership before any SDK poller is created.
func (b *backend) applicationPlan() (*registrationPlan, error) {
	b.planOnce.Do(func() { b.nativePlan, b.planErr = b.buildApplicationPlan() })
	return b.nativePlan, b.planErr
}

// buildApplicationPlan captures one immutable application catalog for workers and replay.
func (b *backend) buildApplicationPlan() (plan *registrationPlan, err error) {
	defer func() {
		if recover() != nil {
			plan = nil
			err = errors.New("Temporal application registration rejected")
		}
	}()
	plan = &registrationPlan{}
	registry := &registrationRegistry{inner: plan, names: make(map[string]bool)}
	if b.cfg.Register != nil {
		if err := b.cfg.Register(registry); err != nil {
			return nil, errors.New("Temporal application registration failed")
		}
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	registry.sealed = true
	b.mu.Lock()
	b.nativeNames = nil
	for name := range registry.names {
		b.nativeNames = append(b.nativeNames, name)
	}
	b.mu.Unlock()
	return plan, nil
}

// apply populates only executable roles while retaining native local activities on workflow workers.
func (p *registrationPlan) apply(b *backend, w worker.Worker, role string) (err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("Temporal SDK registration rejected")
		}
	}()
	if role != "activity" {
		w.RegisterWorkflowWithOptions(jobWorkflow, workflow.RegisterOptions{Name: jobWorkflowType})
		w.RegisterWorkflowWithOptions(coordinatorWorkflow, workflow.RegisterOptions{Name: coordinatorWorkflowType})
		for _, registration := range p.workflows {
			w.RegisterWorkflowWithOptions(registration.fn, registration.options)
		}
	}
	if role != "workflow" {
		w.RegisterActivityWithOptions(b.executeDelivery, activity.RegisterOptions{Name: deliveryActivityType})
	}
	for _, registration := range p.activities {
		w.RegisterActivityWithOptions(registration.fn, registration.options)
	}
	if role != "workflow" {
		for _, service := range p.services {
			w.RegisterNexusService(service)
		}
	}
	return nil
}
