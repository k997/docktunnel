package controller

import (
	"context"
	"log/slog"
	"time"

	"docktunnel/internal/metrics"
	"docktunnel/pkg/types"
)

// compensation drives the failed-action retry queue. Stateless shim.
type compensation struct {
	c   *Controller
	log *slog.Logger
}

func newCompensation(c *Controller, log *slog.Logger) *compensation {
	return &compensation{c: c, log: log}
}

// ExecuteAction executes a single action (e.g., delete route and sync).
// Used by the compensation queue to retry failed actions.
//
// Race guard: before deleting the ingress rule for a hostname, check whether
// the rule is still registered. If a stop event enqueued this delete and the
// container then restarted (handleContainerStart re-added the same hostname),
// the queued delete would otherwise fire after the container is back up and
// remove the live route. Skipping the delete when the rule is present lets
// the start path win.
func (comp *compensation) ExecuteAction(ctx context.Context, action types.Action) error {
	if action.Kind == types.ActionDeleteRoute && action.Hostname != "" {
		comp.c.mu.Lock()
		_, stillRegistered := comp.c.ingressRules[action.Hostname]
		if stillRegistered {
			comp.c.mu.Unlock()
			slog.Info("Skipping compensation delete: hostname is currently registered (container likely restarted)",
				"action", action.Kind,
				"hostname", action.Hostname,
				"container_id", action.ContainerID,
			)
			return nil
		}
		delete(comp.c.ingressRules, action.Hostname)
		comp.c.mu.Unlock()
	}
	return comp.c.syncToCloudflare(ctx)
}

// RunCompensationLoop starts the compensation queue background loop.
// Blocks until ctx is cancelled.
func (comp *compensation) RunCompensationLoop(ctx context.Context) {
	executor := func(action types.Action) error {
		return comp.c.ExecuteAction(ctx, action)
	}

	gaugeDone := make(chan struct{})
	go func() {
		defer close(gaugeDone)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				pending := comp.c.stateManager.GetAllPendingActions()
				metrics.SetCompensationQueueLength(len(pending))
			case <-ctx.Done():
				return
			}
		}
	}()

	comp.c.stateManager.RunCompensation(ctx, executor)
	<-gaugeDone
}

// SetCompensationConfig configures the compensation queue parameters.
func (comp *compensation) SetCompensationConfig(initialDelay, maxDelay time.Duration, maxRetries int, pollInterval time.Duration) {
	comp.c.stateManager.SetCompensationConfig(initialDelay, maxDelay, maxRetries, pollInterval)
}

// SetCompensationQueueCap sets the maximum compensation queue size.
func (comp *compensation) SetCompensationQueueCap(size int) {
	comp.c.stateManager.SetCompensationQueueCap(size)
}

// SetPersistenceConfig configures backup and validation settings.
func (comp *compensation) SetPersistenceConfig(backupCount int, validateOnLoad bool) {
	comp.c.stateManager.SetBackupCount(backupCount)
	comp.c.stateManager.SetValidateOnLoad(validateOnLoad)
}
