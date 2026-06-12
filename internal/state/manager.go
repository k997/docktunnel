package state

import (
	"context"
	"log/slog"
	"strings"
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
	// SaveInterval is the minimum time between state saves
	SaveInterval = 30 * time.Second
)

// Manager manages the state of all tunnel entries
type Manager struct {
	mu                 sync.RWMutex
	activeTunnels      map[string]*types.TunnelEntry  // key: containerID:serviceName (compound)
	pendingDeletes     map[string]*types.TunnelEntry  // key: containerID:serviceName (compound)
	flappingContainers map[string]types.FlappingState // key: containerID

	logger    *slog.Logger
	statePath string
	lastSaved time.Time
	dirty     bool // true if state has changed since last save
}

// pendingDeleteKey generates a unique map key for pending deletions using
// containerID and serviceName, allowing multiple services per container.
func pendingDeleteKey(containerID, serviceName string) string {
	return containerID + ":" + serviceName
}

// activeTunnelKey generates a unique map key for active tunnels using
// containerID and serviceName, matching pendingDeletes key format.
func activeTunnelKey(containerID, serviceName string) string {
	return containerID + ":" + serviceName
}

// containerIDFromKey extracts the container ID from a compound key.
func containerIDFromKey(key string) string {
	idx := strings.Index(key, ":")
	if idx == -1 {
		return key
	}
	return key[:idx]
}

// NewManager creates a new state manager
func NewManager(logger *slog.Logger) *Manager {
	return &Manager{
		activeTunnels:      make(map[string]*types.TunnelEntry),
		pendingDeletes:     make(map[string]*types.TunnelEntry),
		flappingContainers: make(map[string]types.FlappingState),
		logger:             logger,
		statePath:          "",
		lastSaved:          time.Time{},
		dirty:              false,
	}
}

// SetStatePath sets the state file path for persistence
func (sm *Manager) SetStatePath(path string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.statePath = path
}

// markDirty marks the state as changed and potentially saves
// Assumes mu is already held
func (sm *Manager) markDirty() {
	sm.dirty = true
}

// SaveIfDirty saves the state if it has changed and enough time has passed (T072)
func (sm *Manager) SaveIfDirty() error {
	sm.mu.Lock()

	// Release lock before calling Save (which calls GetSnapshot that needs RLock)
	dirty := sm.dirty
	enoughTimePassed := sm.lastSaved.IsZero() || time.Since(sm.lastSaved) >= SaveInterval
	statePath := sm.statePath

	sm.mu.Unlock()

	if !dirty {
		return nil
	}

	// Check if enough time has passed since last save
	if !enoughTimePassed {
		return nil
	}

	// Save the state (Save will acquire its own locks)
	if statePath != "" {
		if err := sm.Save(statePath); err != nil {
			return err
		}
		sm.mu.Lock()
		sm.lastSaved = time.Now()
		sm.dirty = false
		sm.mu.Unlock()
	}

	return nil
}

// ForceSave forces an immediate state save regardless of dirty flag or timing
func (sm *Manager) ForceSave() error {
	sm.mu.Lock()
	statePath := sm.statePath
	sm.mu.Unlock()

	if statePath != "" {
		if err := sm.Save(statePath); err != nil {
			return err
		}
		sm.mu.Lock()
		sm.lastSaved = time.Now()
		sm.dirty = false
		sm.mu.Unlock()
		return nil
	}
	return nil
}

// AddActiveTunnel adds a tunnel to the active tunnels map
func (sm *Manager) AddActiveTunnel(entry *types.TunnelEntry) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	key := activeTunnelKey(entry.ContainerID, entry.ServiceName)
	sm.activeTunnels[key] = entry
	sm.markDirty()
	sm.logger.Debug("Added active tunnel",
		"container_id", entry.ContainerID,
		"service_name", entry.ServiceName,
		"hostname", entry.Config.Hostname,
	)
}

// RemoveActiveTunnel removes all active tunnel entries for a container
func (sm *Manager) RemoveActiveTunnel(containerID string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	removed := false
	for key := range sm.activeTunnels {
		if containerIDFromKey(key) == containerID {
			delete(sm.activeTunnels, key)
			removed = true
		}
	}
	if removed {
		sm.markDirty()
		sm.logger.Debug("Removed active tunnels", "container_id", containerID)
	}
}

// GetActiveTunnel retrieves an active tunnel by container ID and service name
func (sm *Manager) GetActiveTunnel(containerID, serviceName string) (*types.TunnelEntry, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	key := activeTunnelKey(containerID, serviceName)
	entry, ok := sm.activeTunnels[key]
	return entry, ok
}

// GetActiveTunnelsByContainer returns all active tunnel entries for a container
func (sm *Manager) GetActiveTunnelsByContainer(containerID string) []*types.TunnelEntry {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	var entries []*types.TunnelEntry
	for key, entry := range sm.activeTunnels {
		if containerIDFromKey(key) == containerID {
			entries = append(entries, entry)
		}
	}
	return entries
}

// GetAllActiveTunnels returns a copy of all active tunnels.
// Keys are in compound format: "containerID:serviceName"
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

	// Add to pending deletions with compound key (containerID:serviceName)
	key := pendingDeleteKey(entry.ContainerID, entry.ServiceName)
	sm.pendingDeletes[key] = entry
	sm.markDirty()
	sm.logger.Info("Moved tunnel to pending deletion",
		"container_id", entry.ContainerID,
		"service_name", entry.ServiceName,
		"retention_type", entry.RetentionPolicy.Type,
	)
}

// GetPendingDeletion retrieves a pending deletion entry by container ID.
// Returns the first matching entry for the container.
func (sm *Manager) GetPendingDeletion(containerID string) (*types.TunnelEntry, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	for key, entry := range sm.pendingDeletes {
		if containerIDFromKey(key) == containerID {
			return entry, true
		}
	}
	return nil, false
}

// GetPendingDeletionsByContainer returns all pending deletion entries for a container.
func (sm *Manager) GetPendingDeletionsByContainer(containerID string) []*types.TunnelEntry {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	var entries []*types.TunnelEntry
	for key, entry := range sm.pendingDeletes {
		if containerIDFromKey(key) == containerID {
			entries = append(entries, entry)
		}
	}
	return entries
}

// RemovePendingDeletion removes all pending deletion entries for a container.
func (sm *Manager) RemovePendingDeletion(containerID string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	removed := false
	for key := range sm.pendingDeletes {
		if containerIDFromKey(key) == containerID {
			delete(sm.pendingDeletes, key)
			removed = true
		}
	}
	if removed {
		sm.markDirty()
		sm.logger.Debug("Removed pending deletions", "container_id", containerID)
	}
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

// RestoreActiveTunnel moves all pending deletion entries for a container back to active
func (sm *Manager) RestoreActiveTunnel(containerID string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	var found bool
	for key, entry := range sm.pendingDeletes {
		if containerIDFromKey(key) == containerID {
			delete(sm.pendingDeletes, key)
			entry.Status = types.StatusActive
			entry.DeletedAt = nil
			activeKey := activeTunnelKey(entry.ContainerID, entry.ServiceName)
			sm.activeTunnels[activeKey] = entry
			found = true
		}
	}

	if found {
		sm.markDirty()
		sm.logger.Info("Restored tunnel(s) to active",
			"container_id", containerID,
		)
	}

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
		Version:            2,
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

	// Migrate Phase 1 PendingDelete entries with Timed/Forever to StatusRetaining
	for _, entry := range sm.pendingDeletes {
		if entry.Status == types.StatusPendingDelete {
			if entry.RetentionPolicy.Type == types.Timed || entry.RetentionPolicy.Type == types.Forever {
				entry.Status = types.StatusRetaining
			}
		}
	}

	sm.logger.Info("Loaded state from snapshot",
		"version", snapshot.Version,
		"timestamp", snapshot.Timestamp,
		"active_tunnels", len(sm.activeTunnels),
		"pending_deletions", len(sm.pendingDeletes),
		"flapping_containers", len(sm.flappingContainers),
	)
}

// RunGC runs garbage collection for expired pending deletions.
// Returns the expired entries (with hostname info) so the caller can clean up ingress rules.
func (sm *Manager) RunGC(ctx context.Context) ([]*types.TunnelEntry, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	now := time.Now()
	var expiredEntries []*types.TunnelEntry

	for key, entry := range sm.pendingDeletes {
		switch entry.Status {
		case types.StatusPendingDelete:
			// Immediate or already expired — clean up
			entry.Status = types.StatusDeleted
			sm.logger.Info("Garbage collected PendingDelete entry",
				"container_id", entry.ContainerID,
				"service_name", entry.ServiceName,
			)
			delete(sm.pendingDeletes, key)
			sm.markDirty()
			expiredEntries = append(expiredEntries, entry)

		case types.StatusRetaining:
			switch entry.RetentionPolicy.Type {
			case types.Timed:
				if entry.DeletedAt != nil {
					elapsed := now.Sub(*entry.DeletedAt)
					if elapsed >= entry.RetentionPolicy.Duration {
						entry.Status = types.StatusDeleted
						sm.logger.Info("Garbage collected expired Retaining entry",
							"container_id", entry.ContainerID,
							"service_name", entry.ServiceName,
							"hostname", entry.Config.Hostname,
						)
						delete(sm.pendingDeletes, key)
						sm.markDirty()
						expiredEntries = append(expiredEntries, entry)
					}
				} else {
					// No DeletedAt — treat as expired
					entry.Status = types.StatusDeleted
					delete(sm.pendingDeletes, key)
					sm.markDirty()
					expiredEntries = append(expiredEntries, entry)
				}
			case types.Forever:
				// Never auto-clean
			}
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

	return expiredEntries, nil
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
