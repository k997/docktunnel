package controller

import (
	"context"

	"docktunnel/internal/events"
	"docktunnel/internal/metrics"
	eventTypes "github.com/docker/docker/api/types/events"
)

// dispatcher routes Docker events to the appropriate handler. Stateless;
// holds a Controller pointer for handler dispatch.
type dispatcher struct {
	c *Controller
}

func newDispatcher(c *Controller) *dispatcher {
	return &dispatcher{c: c}
}

// Dispatch is the unified entry point for all event processing.
func (d *dispatcher) Dispatch(ctx context.Context, event events.Event) error {
	err := d.dispatchInner(ctx, event)

	result := metrics.ResultSuccess
	if err != nil {
		result = metrics.ResultFailure
	}
	metrics.RecordEvent(string(event.Type), result)

	return err
}

// dispatchInner is the original dispatch switch.
func (d *dispatcher) dispatchInner(ctx context.Context, event events.Event) error {
	switch event.Type {
	case eventTypes.ActionStart:
		return d.c.handleContainerStart(ctx, event)
	case eventTypes.ActionStop:
		return d.c.handleContainerStop(ctx, event)
	case eventTypes.ActionDie:
		return d.c.handleContainerStop(ctx, event)
	case events.ActionHealthHealthy:
		return d.c.handleHealthHealthy(ctx, event)
	case events.ActionHealthUnhealthy:
		return d.c.handleHealthUnhealthy(ctx, event)
	case events.ActionHealthStarting:
		return d.c.handleHealthUnhealthy(ctx, event)
	case events.ActionResync:
		return d.c.handleResync(ctx)
	default:
		return nil
	}
}

// isDocktunnelEnabled returns true if the container has docktunnel.enable=true.
func (d *dispatcher) isDocktunnelEnabled(event events.Event) bool {
	if event.ContainerInfo == nil || event.ContainerInfo.Config == nil || event.ContainerInfo.Config.Labels == nil {
		return false
	}
	return event.ContainerInfo.Config.Labels["docktunnel.enable"] == "true"
}
