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
func (sm *Manager) transitionLocked(containerID, serviceName string, event types.TransitionEvent, policy *types.RetentionPolicy) ([]types.Action, error) {
	switch event {
	case types.EventContainerStopped:
		return sm.transitionStoppedLocked(containerID, serviceName, policy)
	case types.EventContainerStarted:
		return sm.transitionStartedLocked(containerID, serviceName)
	case types.EventRetentionExpired:
		return sm.transitionRetentionExpiredLocked(containerID, serviceName)
	case types.EventCleanupComplete:
		return sm.transitionCleanupCompleteLocked(containerID, serviceName)
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

// transitionStartedLocked handles container start events for a specific service.
// Restores entry from pending deletes back to active state.
// Assumes mu is already held.
func (sm *Manager) transitionStartedLocked(containerID, serviceName string) ([]types.Action, error) {
	pendingKey := pendingDeleteKey(containerID, serviceName)
	entry, exists := sm.pendingDeletes[pendingKey]
	if !exists {
		sm.logger.Debug("No pending entry to restore",
			"container_id", containerID,
			"service_name", serviceName,
		)
		return nil, nil
	}

	entry.Status = types.StatusActive
	entry.DeletedAt = nil

	delete(sm.pendingDeletes, pendingKey)
	activeKey := activeTunnelKey(containerID, serviceName)
	sm.activeTunnels[activeKey] = entry
	sm.markDirty()

	sm.logger.Info("Restored tunnel to active",
		"container_id", containerID,
		"service_name", serviceName,
		"hostname", entry.Config.Hostname,
	)

	return nil, nil
}

// transitionRetentionExpiredLocked handles retention timer expiry for a specific service.
// Transitions entry from retaining to pending delete.
// Assumes mu is already held.
func (sm *Manager) transitionRetentionExpiredLocked(containerID, serviceName string) ([]types.Action, error) {
	pendingKey := pendingDeleteKey(containerID, serviceName)
	entry, exists := sm.pendingDeletes[pendingKey]
	if !exists || entry.Status != types.StatusRetaining {
		sm.logger.Debug("No retaining entry found for expiry",
			"container_id", containerID,
			"service_name", serviceName,
		)
		return nil, nil
	}

	entry.Status = types.StatusPendingDelete
	sm.markDirty()

	action := types.Action{
		Kind:        types.ActionDeleteRoute,
		ContainerID: entry.ContainerID,
		ServiceName: entry.ServiceName,
		Hostname:    entry.Config.Hostname,
	}

	sm.logger.Info("Retention expired, transitioned to pending delete",
		"container_id", containerID,
		"service_name", serviceName,
		"hostname", entry.Config.Hostname,
	)

	return []types.Action{action}, nil
}

// transitionCleanupCompleteLocked handles cleanup completion for a specific service.
// Removes entry that has been fully cleaned up.
// Assumes mu is already held.
func (sm *Manager) transitionCleanupCompleteLocked(containerID, serviceName string) ([]types.Action, error) {
	pendingKey := pendingDeleteKey(containerID, serviceName)
	entry, exists := sm.pendingDeletes[pendingKey]
	if !exists || entry.Status != types.StatusPendingDelete {
		sm.logger.Debug("No pending delete entry found for cleanup",
			"container_id", containerID,
			"service_name", serviceName,
		)
		return nil, nil
	}

	entry.Status = types.StatusDeleted
	delete(sm.pendingDeletes, pendingKey)
	sm.markDirty()

	sm.logger.Info("Cleanup complete, entry removed",
		"container_id", containerID,
		"service_name", serviceName,
		"hostname", entry.Config.Hostname,
	)

	return nil, nil
}

// IsDirty returns whether the state has changed since the last save.
// Thread-safe: uses RLock.
func (sm *Manager) IsDirty() bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.dirty
}
