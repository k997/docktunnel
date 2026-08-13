package state

import (
	"time"

	"docktunnel/pkg/types"
)

// Transition handles state transitions for a container service based on events and retention policy.
// Returns actions for the controller to execute (e.g., delete routes, DNS records).
// Thread-safe: acquires lock and delegates to transitionLocked.
func (sm *Manager) Transition(containerID, serviceName string, event types.TransitionEvent, policy *types.RetentionPolicy) ([]types.Action, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	return sm.transitionLocked(containerID, serviceName, event, policy)
}

// transitionLocked performs the actual state transition logic.
// Assumes mu is already held.
//
// Only EventContainerStopped has a production caller (handleContainerStop and
// the disconnect difference-set in buildDesiredRules). The other events
// (EventContainerStarted / EventRetentionExpired / EventCleanupComplete) and
// their transition helpers were removed as dead code (B18); the enum values
// are retained for snapshot/go test compatibility.
func (sm *Manager) transitionLocked(containerID, serviceName string, event types.TransitionEvent, policy *types.RetentionPolicy) ([]types.Action, error) {
	switch event {
	case types.EventContainerStopped:
		return sm.transitionStoppedLocked(containerID, serviceName, policy)
	default:
		sm.logger.Warn("Unknown transition event", "event", event, "container_id", containerID, "service_name", serviceName)
		return nil, nil
	}
}

// transitionStoppedLocked handles container stop events with retention policy.
// Assumes mu is already held.
func (sm *Manager) transitionStoppedLocked(containerID, serviceName string, policy *types.RetentionPolicy) ([]types.Action, error) {
	key := activeTunnelKey(containerID, serviceName)
	entry, exists := sm.activeTunnels[key]
	if !exists {
		return nil, nil
	}

	now := time.Now()
	entry.DeletedAt = &now
	entry.RetentionPolicy = *policy

	pendingKey := pendingDeleteKey(containerID, serviceName)

	if policy != nil && (policy.Type == types.Timed || policy.Type == types.Forever) {
		entry.Status = types.StatusRetaining
		delete(sm.activeTunnels, key)
		sm.pendingDeletes[pendingKey] = entry
		sm.markDirty()
		sm.logger.Info("Transitioned to retaining",
			"container_id", containerID,
			"service_name", serviceName,
			"retention_type", policy.Type,
			"duration", policy.Duration,
		)
		return nil, nil
	}

	entry.Status = types.StatusPendingDelete
	delete(sm.activeTunnels, key)
	sm.pendingDeletes[pendingKey] = entry
	sm.markDirty()

	action := types.Action{
		Kind:        types.ActionDeleteRoute,
		ContainerID: entry.ContainerID,
		ServiceName: entry.ServiceName,
		Hostname:    entry.Config.Hostname,
	}

	sm.logger.Info("Transitioned to pending delete",
		"container_id", containerID,
		"service_name", serviceName,
		"hostname", entry.Config.Hostname,
	)

	return []types.Action{action}, nil
}

// IsDirty returns whether the state has changed since the last save.
// Thread-safe: uses RLock.
func (sm *Manager) IsDirty() bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.dirty
}
