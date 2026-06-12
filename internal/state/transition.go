package state

import (
	"time"

	"docktunnel/pkg/types"
)

// Transition handles state transitions for a container based on events and retention policy.
// Returns actions for the controller to execute (e.g., delete routes, DNS records).
// Thread-safe: acquires lock and delegates to transitionLocked.
func (sm *Manager) Transition(containerID string, event types.TransitionEvent, policy *types.RetentionPolicy) ([]types.Action, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	return sm.transitionLocked(containerID, event, policy)
}

// transitionLocked performs the actual state transition logic.
// Assumes mu is already held.
func (sm *Manager) transitionLocked(containerID string, event types.TransitionEvent, policy *types.RetentionPolicy) ([]types.Action, error) {
	switch event {
	case types.EventContainerStopped:
		return sm.transitionStoppedLocked(containerID, policy)
	case types.EventContainerStarted:
		return sm.transitionStartedLocked(containerID)
	case types.EventRetentionExpired:
		return sm.transitionRetentionExpiredLocked(containerID)
	case types.EventCleanupComplete:
		return sm.transitionCleanupCompleteLocked(containerID)
	default:
		sm.logger.Warn("Unknown transition event", "event", event, "container_id", containerID)
		return nil, nil
	}
}

// transitionStoppedLocked handles container stop events with retention policy.
// Assumes mu is already held.
func (sm *Manager) transitionStoppedLocked(containerID string, policy *types.RetentionPolicy) ([]types.Action, error) {
	entry, exists := sm.activeTunnels[containerID]
	if !exists {
		// No entry to transition - container may not have docktunnel labels
		return nil, nil
	}

	now := time.Now()
	entry.DeletedAt = &now
	entry.RetentionPolicy = *policy

	// Determine actions based on policy type
	if policy != nil && (policy.Type == types.Timed || policy.Type == types.Forever) {
		// Move to retaining state - no immediate deletion
		entry.Status = types.StatusRetaining
		key := pendingDeleteKey(entry.ContainerID, entry.ServiceName)
		delete(sm.activeTunnels, containerID)
		sm.pendingDeletes[key] = entry
		sm.markDirty()
		sm.logger.Info("Transitioned to retaining",
			"container_id", containerID,
			"service_name", entry.ServiceName,
			"retention_type", policy.Type,
			"duration", policy.Duration,
		)
		return nil, nil
	}

	// Immediate or nil policy - move to pending delete
	entry.Status = types.StatusPendingDelete
	key := pendingDeleteKey(entry.ContainerID, entry.ServiceName)
	delete(sm.activeTunnels, containerID)
	sm.pendingDeletes[key] = entry
	sm.markDirty()

	action := types.Action{
		Kind:        types.ActionDeleteRoute,
		ContainerID: entry.ContainerID,
		ServiceName: entry.ServiceName,
		Hostname:    entry.Config.Hostname,
	}

	sm.logger.Info("Transitioned to pending delete",
		"container_id", containerID,
		"service_name", entry.ServiceName,
		"hostname", entry.Config.Hostname,
	)

	return []types.Action{action}, nil
}

// transitionStartedLocked handles container start events.
// Restores entries from pending deletes back to active state.
// Assumes mu is already held.
func (sm *Manager) transitionStartedLocked(containerID string) ([]types.Action, error) {
	// Find any pending delete entries for this container
	var foundKey string
	var foundEntry *types.TunnelEntry

	for key, entry := range sm.pendingDeletes {
		if containerIDFromKey(key) == containerID {
			foundKey = key
			foundEntry = entry
			break
		}
	}

	if foundEntry == nil {
		// No pending entry - new container or already active
		sm.logger.Debug("No pending entry to restore",
			"container_id", containerID,
		)
		return nil, nil
	}

	// Restore to active state
	foundEntry.Status = types.StatusActive
	foundEntry.DeletedAt = nil

	// Move back to active tunnels
	delete(sm.pendingDeletes, foundKey)
	sm.activeTunnels[containerID] = foundEntry
	sm.markDirty()

	sm.logger.Info("Restored tunnel to active",
		"container_id", containerID,
		"service_name", foundEntry.ServiceName,
		"hostname", foundEntry.Config.Hostname,
	)

	return nil, nil
}

// transitionRetentionExpiredLocked handles retention timer expiry events.
// Transitions entries from retaining to pending delete.
// Assumes mu is already held.
func (sm *Manager) transitionRetentionExpiredLocked(containerID string) ([]types.Action, error) {
	// Find retaining entry for this container
	var foundEntry *types.TunnelEntry

	for _, entry := range sm.pendingDeletes {
		if entry.Status == types.StatusRetaining && entry.ContainerID == containerID {
			foundEntry = entry
			break
		}
	}

	if foundEntry == nil {
		// No retaining entry found
		sm.logger.Debug("No retaining entry found for expiry",
			"container_id", containerID,
		)
		return nil, nil
	}

	// Transition to pending delete
	foundEntry.Status = types.StatusPendingDelete
	sm.markDirty()

	action := types.Action{
		Kind:        types.ActionDeleteRoute,
		ContainerID: foundEntry.ContainerID,
		ServiceName: foundEntry.ServiceName,
		Hostname:    foundEntry.Config.Hostname,
	}

	sm.logger.Info("Retention expired, transitioned to pending delete",
		"container_id", containerID,
		"service_name", foundEntry.ServiceName,
		"hostname", foundEntry.Config.Hostname,
	)

	return []types.Action{action}, nil
}

// transitionCleanupCompleteLocked handles cleanup completion events.
// Removes entries that have been fully cleaned up.
// Assumes mu is already held.
func (sm *Manager) transitionCleanupCompleteLocked(containerID string) ([]types.Action, error) {
	// Find pending delete entry for this container
	var foundKey string
	var foundEntry *types.TunnelEntry

	for key, entry := range sm.pendingDeletes {
		if containerIDFromKey(key) == containerID && entry.Status == types.StatusPendingDelete {
			foundKey = key
			foundEntry = entry
			break
		}
	}

	if foundEntry == nil {
		// No pending delete entry found
		sm.logger.Debug("No pending delete entry found for cleanup",
			"container_id", containerID,
		)
		return nil, nil
	}

	// Mark as deleted and remove from state
	foundEntry.Status = types.StatusDeleted
	delete(sm.pendingDeletes, foundKey)
	sm.markDirty()

	sm.logger.Info("Cleanup complete, entry removed",
		"container_id", containerID,
		"service_name", foundEntry.ServiceName,
		"hostname", foundEntry.Config.Hostname,
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
