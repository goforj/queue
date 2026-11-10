package temporalqueue

import "go.temporal.io/sdk/workflow"

// portableRoutingChangeID preserves replay of histories accepted before resource-bound entry validation.
const portableRoutingChangeID = "goforj.queue.resource-routing"

// portableWorkflows captures immutable resource policy without retaining application services in workflow code.
type portableWorkflows struct {
	namespace     string
	queue         string
	workflowQueue string
	activityQueue string
}

// portableWorkflows snapshots routing once for registration shared by workers and replay.
func (b *backend) portableWorkflows() portableWorkflows {
	return portableWorkflows{namespace: b.clientBinding.Namespace(), queue: b.taskQueue, workflowQueue: b.workflowQueue, activityQueue: b.activityQueue}
}

// validateDelivery rejects routes outside this resource before any activity command is created.
func (p portableWorkflows) validateDelivery(d delivery) error {
	if d.WorkflowQueue != p.workflowQueue || d.ActivityQueue != p.activityQueue || d.Metadata.Queue != p.queue {
		return invalidAdapterError()
	}
	return nil
}

// jobWorkflow binds newly accepted portable jobs to their registered resource while retaining old-history replay.
func (p portableWorkflows) jobWorkflow(ctx workflow.Context, d delivery) error {
	if workflow.GetVersion(ctx, portableRoutingChangeID, workflow.DefaultVersion, 1) == 1 {
		if workflow.GetInfo(ctx).Namespace != p.namespace {
			return invalidAdapterError()
		}
		if err := p.validateDelivery(d); err != nil {
			return err
		}
	}
	return jobWorkflow(ctx, d)
}

// coordinatorWorkflow validates every member and hook target before the coordinator can publish or schedule work.
func (p portableWorkflows) coordinatorWorkflow(ctx workflow.Context, def workflowDefinition) error {
	if workflow.GetVersion(ctx, portableRoutingChangeID, workflow.DefaultVersion, 1) == 1 {
		if workflow.GetInfo(ctx).Namespace != p.namespace || def.Resource != p.namespace+":"+p.queue || def.Queue != p.queue || def.WorkflowQueue != p.workflowQueue || len(def.Jobs) > maxMembers || len(def.Hooks) > 32 {
			return invalidAdapterError()
		}
		for _, job := range def.Jobs {
			if err := p.validateDelivery(job); err != nil {
				return err
			}
		}
		for _, hook := range def.Hooks {
			if err := p.validateDelivery(hook.Job); err != nil {
				return err
			}
		}
	}
	return coordinatorWorkflow(ctx, def)
}
