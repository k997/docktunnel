package state

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"docktunnel/pkg/types"
)

const (
	// FlappingWindow is the time window to observe for flapping detection
	FlappingWindow = 60 * time.Second
	// FlappingThreshold is the number of transitions to trigger flapping
	FlappingThreshold = 5
	// CoolingPeriod is the initial cooling period for flapping containers
	CoolingPeriod = 300 * time.Second
	// MaxCoolingPeriod is the maximum cooling period
	MaxCoolingPeriod = 1800 * time.Second
)

// Manager manages the state of all tunnel entries
type Manager struct {
	mu                sync.RWMutex
	activeTunnels     map[string]*types.TunnelEntry     // key: containerID
	pendingDeletes    map[string]*types.TunnelEntry     // key: containerID
	flappingContainers map[string]types.FlappingState   // key: containerID

	logger *slog.Logger
}

// NewManager creates a new state manager
func NewManager(logger *slog.Logger) *Manager {
	return &Manager{
		activeTunnels:      make(map[string]*types.TunnelEntry),
		pendingDeletes:     make(map[string]*types.TunnelEntry),
		flappingContainers: make(map[string]types.FlappingState),
		logger:             logger,
	}
}

// AddActiveTunnel adds a tunnel to the active tunnels map
func (sm *Manager) AddActiveTunnel(entry *types.TunnelEntry) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sm.activeTunnels[entry.ContainerID] = entry
	sm.logger.Debug("Added active tunnel",
		"container_id", entry.ContainerID,
		"service_name", entry.ServiceName,
		"hostname", entry.Config.Hostname,
	)
}

// RemoveActiveTunnel removes a tunnel from the active tunnels map
func (sm *Manager) RemoveActiveTunnel(containerID string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if _, exists := sm.activeTunnels[containerID]; exists {
		delete(sm.activeTunnels, containerID)
		sm.logger.Debug("Removed active tunnel", "container_id", containerID)
	}
}

// GetActiveTunnel retrieves an active tunnel by container ID
func (sm *Manager) GetActiveTunnel(containerID string) (*types.TunnelEntry, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	entry, ok := sm.activeTunnels[containerID]
	return entry, ok
}

// GetAllActiveTunnels returns a copy of all active tunnels
func (sm *Manager) GetAllActiveTunnels() map[string]*types.TunnelEntry {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	result := make(map[string]*types.TunnelEntry, len(sm.activeTunnels))
	for k, v := range sm.activeTunnels {
		result[k] = v
	}
	return result
}

// AddPendingDeletion moves a tunnel to the pending deletion map
func (sm *Manager) AddPendingDeletion(entry *types.TunnelEntry) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Remove from active if present
	delete(sm.activeTunnels, entry.ContainerID)

	// Add to pending deletions
	sm.pendingDeletes[entry.ContainerID] = entry
	sm.logger.Info("Moved tunnel to pending deletion",
		"container_id", entry.ContainerID,
		"service_name", entry.ServiceName,
		"retention_type", entry.RetentionPolicy.Type,
	)
}

// RemovePendingDeletion removes a tunnel from the pending deletion map
func (sm *Manager) RemovePendingDeletion(containerID string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if _, exists := sm.pendingDeletes[containerID]; exists {
		delete(sm.pendingDeletes, containerID)
		sm.logger.Debug("Removed pending deletion", "container_id", containerID)
	}
}

// GetPendingDeletion retrieves a pending deletion entry by container ID
func (sm *Manager) GetPendingDeletion(containerID string) (*types.TunnelEntry, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	entry, ok := sm.pendingDeletes[containerID]
	return entry, ok
}

// GetAllPendingDeletions returns a copy of all pending deletions
func (sm *Manager) GetAllPendingDeletions() map[string]*types.TunnelEntry {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	result := make(map[string]*types.TunnelEntry, len(sm.pendingDeletes))
	for k, v := range sm.pendingDeletes {
		result[k] = v
	}
	return result
}

// RestoreActiveTunnel moves a tunnel from pending deletion back to active
func (sm *Manager) RestoreActiveTunnel(containerID string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	entry, exists := sm.pendingDeletes[containerID]
	if !exists {
		return nil // Not in pending deletions, nothing to restore
	}

	// Move back to active
	delete(sm.pendingDeletes, containerID)
	entry.Status = types.StatusActive
	entry.DeletedAt = nil
	sm.activeTunnels[containerID] = entry

	sm.logger.Info("Restored tunnel to active",
		"container_id", containerID,
		"service_name", entry.ServiceName,
	)

	return nil
}

// CheckFlapping checks if a container is flapping based on state transitions
func (sm *Manager) CheckFlapping(containerID string) bool {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	state, exists := sm.flappingContainers[containerID]
	if !exists {
		return false
	}

	// If no cooling period is set, container is not flapping yet
	if state.CoolingUntil.IsZero() {
		return false
	}

	// Check if still in cooling period
	if time.Now().Before(state.CoolingUntil) {
		return true
	}

	// Cooling period expired, remove from flapping state
	delete(sm.flappingContainers, containerID)
	sm.logger.Info("Flapping cooling period expired",
		"container_id", containerID,
	)
	return false
}

// RecordTransition records a state transition for flapping detection
func (sm *Manager) RecordTransition(containerID string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	state, exists := sm.flappingContainers[containerID]
	if !exists {
		state = types.FlappingState{
			Transitions: []time.Time{},
		}
	}

	// Add current transition
	now := time.Now()
	state.Transitions = append(state.Transitions, now)

	// Remove old transitions outside the window
	cutoff := now.Add(-FlappingWindow)
	var validTransitions []time.Time
	for _, t := range state.Transitions {
		if t.After(cutoff) {
			validTransitions = append(validTransitions, t)
		}
	}
	state.Transitions = validTransitions

	// Check if threshold exceeded
	if len(state.Transitions) >= FlappingThreshold {
		// Trigger flapping
		state.LastFlapped = now
		state.CoolingUntil = now.Add(CoolingPeriod)
		state.Transitions = []time.Time{} // Reset transitions

		sm.flappingContainers[containerID] = state
		sm.logger.Warn("Container flapping detected",
			"container_id", containerID,
			"transitions", len(validTransitions),
			"cooling_until", state.CoolingUntil,
		)
		return
	}

	sm.flappingContainers[containerID] = state
}

// MarkAsFlapping manually marks a container as flapping
func (sm *Manager) MarkAsFlapping(containerID string, coolingDuration time.Duration) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if coolingDuration <= 0 {
		coolingDuration = CoolingPeriod
	}
	if coolingDuration > MaxCoolingPeriod {
		coolingDuration = MaxCoolingPeriod
	}

	now := time.Now()
	sm.flappingContainers[containerID] = types.FlappingState{
		Transitions:  []time.Time{},
		LastFlapped:  now,
		CoolingUntil: now.Add(coolingDuration),
	}

	sm.logger.Warn("Manually marked container as flapping",
		"container_id", containerID,
		"cooling_until", now.Add(coolingDuration),
	)
}

// GetFlappingState retrieves the flapping state for a container
func (sm *Manager) GetFlappingState(containerID string) (types.FlappingState, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	state, ok := sm.flappingContainers[containerID]
	return state, ok
}

// GetSnapshot returns a snapshot of the current state
func (sm *Manager) GetSnapshot() *types.StateSnapshot {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	// Create deep copies of maps
	activeTunnels := make(map[string]*types.TunnelEntry, len(sm.activeTunnels))
	for k, v := range sm.activeTunnels {
		activeTunnels[k] = v
	}

	pendingDeletions := make(map[string]*types.TunnelEntry, len(sm.pendingDeletes))
	for k, v := range sm.pendingDeletes {
		pendingDeletions[k] = v
	}

	flappingContainers := make(map[string]types.FlappingState, len(sm.flappingContainers))
	for k, v := range sm.flappingContainers {
		flappingContainers[k] = v
	}

	return &types.StateSnapshot{
		Version:            1,
		Timestamp:          time.Now().UTC(),
		ActiveTunnels:      activeTunnels,
		PendingDeletions:   pendingDeletions,
		FlappingContainers: flappingContainers,
	}
}

// LoadFromSnapshot loads state from a snapshot
func (sm *Manager) LoadFromSnapshot(snapshot *types.StateSnapshot) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sm.activeTunnels = snapshot.ActiveTunnels
	sm.pendingDeletes = snapshot.PendingDeletions
	sm.flappingContainers = snapshot.FlappingContainers

	sm.logger.Info("Loaded state from snapshot",
		"version", snapshot.Version,
		"timestamp", snapshot.Timestamp,
		"active_tunnels", len(sm.activeTunnels),
		"pending_deletions", len(sm.pendingDeletes),
		"flapping_containers", len(sm.flappingContainers),
	)
}

// RunGC runs garbage collection for expired pending deletions
// This should be called periodically (e.g., every 60 seconds)
func (sm *Manager) RunGC(ctx context.Context) ([]string, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	now := time.Now()
	var expiredContainers []string

	for containerID, entry := range sm.pendingDeletes {
		shouldDelete := false

		switch entry.RetentionPolicy.Type {
		case types.Immediate:
			shouldDelete = true

		case types.Timed:
			if entry.DeletedAt != nil {
				elapsed := now.Sub(*entry.DeletedAt)
				if elapsed >= entry.RetentionPolicy.Duration {
					shouldDelete = true
				}
			} else {
				// DeletedAt not set, delete now
				shouldDelete = true
			}

		case types.Forever:
			// Never auto-delete
			continue
		}

		if shouldDelete {
			expiredContainers = append(expiredContainers, containerID)
			entry.Status = types.StatusDeleted
			sm.logger.Info("Garbage collected tunnel entry",
				"container_id", containerID,
				"service_name", entry.ServiceName,
				"retention_type", entry.RetentionPolicy.Type,
			)
			delete(sm.pendingDeletes, containerID)
		}
	}

	// Clean up expired flapping states
	for containerID, state := range sm.flappingContainers {
		if now.After(state.CoolingUntil) {
			delete(sm.flappingContainers, containerID)
			sm.logger.Debug("Removed expired flapping state",
				"container_id", containerID,
			)
		}
	}

	return expiredContainers, nil
}

// GetStats returns statistics about the current state
func (sm *Manager) GetStats() map[string]int {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	return map[string]int{
		"active_tunnels":      len(sm.activeTunnels),
		"pending_deletions":   len(sm.pendingDeletes),
		"flapping_containers": len(sm.flappingContainers),
	}
}
