package state

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"docktunnel/internal/metrics"
	"docktunnel/pkg/types"
)

const (
	// SaveInterval is the minimum time between state saves
	SaveInterval = 30 * time.Second
)

// Manager manages the state of all tunnel entries
type Manager struct {
	mu               sync.RWMutex
	activeTunnels    map[string]*types.TunnelEntry // key: containerID:serviceName (compound)
	pendingDeletes   map[string]*types.TunnelEntry // key: containerID:serviceName (compound)
	pendingActions   map[string]*types.CompensationRecord
	compInitialDelay time.Duration
	compMaxDelay     time.Duration
	compMaxRetries   int
	compPollInterval time.Duration
	compMaxQueueSize int
	backupCount      int
	validateOnLoad   bool

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
		activeTunnels:    make(map[string]*types.TunnelEntry),
		pendingDeletes:   make(map[string]*types.TunnelEntry),
		pendingActions:   make(map[string]*types.CompensationRecord),
		logger:           logger,
		statePath:        "",
		lastSaved:        time.Time{},
		dirty:            false,
		compInitialDelay: 30 * time.Second,
		compMaxDelay:     30 * time.Minute,
		compMaxRetries:   10,
		compPollInterval: 30 * time.Second,
		compMaxQueueSize: 1000,
		backupCount:      3,
		validateOnLoad:   true,
	}
}

// SetStatePath sets the state file path for persistence
func (sm *Manager) SetStatePath(path string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.statePath = path
}

// BackupCount returns the number of backup files to keep
func (sm *Manager) BackupCount() int {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.backupCount
}

// ValidateOnLoad returns whether state validation should be performed on load
func (sm *Manager) ValidateOnLoad() bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.validateOnLoad
}

// SetBackupCount sets the number of backup files to keep
func (sm *Manager) SetBackupCount(n int) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.backupCount = n
}

// SetValidateOnLoad sets whether state validation should be performed on load
func (sm *Manager) SetValidateOnLoad(enabled bool) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.validateOnLoad = enabled
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

	// Remove from active if present — use the compound key so per-service
	// entries are removed individually instead of leaking the entry (B13).
	delete(sm.activeTunnels, activeTunnelKey(entry.ContainerID, entry.ServiceName))

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

// ListRetaining returns a read-only snapshot of all entries currently in
// retention (StatusRetaining). Entries are deep-copied so callers cannot
// mutate live state. Expiry is handled by RunGC; callers treat every entry
// returned here as still-valid retention (B1).
func (sm *Manager) ListRetaining() []*types.TunnelEntry {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	var out []*types.TunnelEntry
	for _, entry := range sm.pendingDeletes {
		if entry.Status != types.StatusRetaining {
			continue
		}
		copied := *entry
		if entry.DeletedAt != nil {
			t := *entry.DeletedAt
			copied.DeletedAt = &t
		}
		if entry.Config.RuleJSON != nil {
			copied.Config.RuleJSON = append([]byte(nil), entry.Config.RuleJSON...)
		}
		out = append(out, &copied)
	}
	return out
}

// RestoreActiveTunnel moves all pending deletion entries for a container back to active.
//
// Also cancels any pending ActionDeleteRoute records for the container: if a
// stop event enqueued a delete and the container came back before the
// compensation queue ran, the queued delete would otherwise fire after
// handleContainerStart re-registered the route, deleting a live route.
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

	// Cancel pending delete-route compensation records for this container.
	// Without this, a stop-then-start cycle could leave a stale delete in the
	// queue that fires after the container has re-registered its route.
	cancelledActions := 0
	for id, rec := range sm.pendingActions {
		if rec.Action.ContainerID == containerID && rec.Action.Kind == types.ActionDeleteRoute {
			delete(sm.pendingActions, id)
			cancelledActions++
		}
	}

	if found || cancelledActions > 0 {
		sm.markDirty()
		sm.logger.Info("Restored tunnel(s) to active",
			"container_id", containerID,
			"cancelled_pending_deletes", cancelledActions,
		)
	}

	return nil
}

// GetSnapshot returns a snapshot of the current state.
//
// Map values are deep-copied: *TunnelEntry and *CompensationRecord are cloned
// by value under the read lock. Save/SaveJSON runs gob encoding outside this
// lock, so without deep copies the encoder would race with concurrent
// mutations (transitionStoppedLocked mutating entry.Status, etc.) and could
// emit a torn snapshot.
func (sm *Manager) GetSnapshot() *types.StateSnapshot {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	activeTunnels := make(map[string]*types.TunnelEntry, len(sm.activeTunnels))
	for k, v := range sm.activeTunnels {
		copied := *v
		if v.DeletedAt != nil {
			t := *v.DeletedAt
			copied.DeletedAt = &t
		}
		if v.Config.RuleJSON != nil {
			copied.Config.RuleJSON = append([]byte(nil), v.Config.RuleJSON...)
		}
		activeTunnels[k] = &copied
	}

	pendingDeletions := make(map[string]*types.TunnelEntry, len(sm.pendingDeletes))
	for k, v := range sm.pendingDeletes {
		copied := *v
		if v.DeletedAt != nil {
			t := *v.DeletedAt
			copied.DeletedAt = &t
		}
		if v.Config.RuleJSON != nil {
			copied.Config.RuleJSON = append([]byte(nil), v.Config.RuleJSON...)
		}
		pendingDeletions[k] = &copied
	}

	pendingActions := make(map[string]*types.CompensationRecord, len(sm.pendingActions))
	for k, v := range sm.pendingActions {
		copied := *v
		pendingActions[k] = &copied
	}

	return &types.StateSnapshot{
		Version:          3,
		Timestamp:        time.Now().UTC(),
		ActiveTunnels:    activeTunnels,
		PendingDeletions: pendingDeletions,
		PendingActions:   pendingActions,
	}
}

// LoadFromSnapshot loads state from a snapshot
func (sm *Manager) LoadFromSnapshot(snapshot *types.StateSnapshot) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Degrade to empty state on nil snapshot
	if snapshot == nil {
		sm.activeTunnels = make(map[string]*types.TunnelEntry)
		sm.pendingDeletes = make(map[string]*types.TunnelEntry)
		sm.logger.Warn("Nil snapshot received, starting with empty state")
		return
	}

	// Migration: v1 activeTunnels keys (bare containerID) → v2 compound keys
	if snapshot.Version < 2 {
		migrated := make(map[string]*types.TunnelEntry, len(snapshot.ActiveTunnels))
		for _, entry := range snapshot.ActiveTunnels {
			serviceName := entry.ServiceName
			if serviceName == "" {
				serviceName = "default"
				entry.ServiceName = serviceName
				sm.logger.Warn("Migrating v1 active entry with empty ServiceName",
					"container_id", entry.ContainerID,
					"assigned_service", serviceName)
			}
			newKey := activeTunnelKey(entry.ContainerID, serviceName)
			migrated[newKey] = entry
		}
		sm.activeTunnels = migrated
		sm.logger.Info("Migrated activeTunnels from v1 to v2 compound keys",
			"entries", len(migrated))
	} else {
		sm.activeTunnels = snapshot.ActiveTunnels
	}

	sm.pendingDeletes = snapshot.PendingDeletions

	// Migrate Phase 1 PendingDelete entries with Timed/Forever to StatusRetaining
	for _, entry := range sm.pendingDeletes {
		if entry.Status == types.StatusPendingDelete {
			if entry.RetentionPolicy.Type == types.Timed || entry.RetentionPolicy.Type == types.Forever {
				entry.Status = types.StatusRetaining
			}
		}
	}

	// Restore pending actions (migrate v2 → v3)
	if snapshot.PendingActions != nil {
		sm.pendingActions = snapshot.PendingActions
	} else {
		sm.pendingActions = make(map[string]*types.CompensationRecord)
	}

	sm.logger.Info("Loaded state from snapshot",
		"version", snapshot.Version,
		"timestamp", snapshot.Timestamp,
		"active_tunnels", len(sm.activeTunnels),
		"pending_deletions", len(sm.pendingDeletes),
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

	return expiredEntries, nil
}

// GetStats returns statistics about the current state
func (sm *Manager) GetStats() map[string]int {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	return map[string]int{
		"active_tunnels":    len(sm.activeTunnels),
		"pending_deletions": len(sm.pendingDeletes),
	}
}

// SetCompensationConfig configures retry parameters for the compensation queue.
func (sm *Manager) SetCompensationConfig(initialDelay, maxDelay time.Duration, maxRetries int, pollInterval time.Duration) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.compInitialDelay = initialDelay
	sm.compMaxDelay = maxDelay
	sm.compMaxRetries = maxRetries
	sm.compPollInterval = pollInterval
}

// SetCompensationQueueCap sets the maximum number of pending compensation records.
// Enqueue requests beyond this cap are rejected and counted in the overflow metric.
func (sm *Manager) SetCompensationQueueCap(size int) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if size > 0 {
		sm.compMaxQueueSize = size
	}
}

// classifyError returns a stable string label for use as the error_class log field.
// Returns "permanent" for PermanentError, "retryable" for RetryableError, and
// "unknown" for plain errors (which are treated as retryable but unclassified).
func classifyError(err error) string {
	if err == nil {
		return ""
	}
	var permanent *types.PermanentError
	if errors.As(err, &permanent) {
		return "permanent"
	}
	var retryable *types.RetryableError
	if errors.As(err, &retryable) {
		return "retryable"
	}
	return "unknown"
}

// newCompensationID returns a collision-resistant ID for a compensation record.
// Uses crypto/rand so rapid enqueues for the same container/service cannot collide.
func newCompensationID(action types.Action) string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// Fallback to timestamp if rand fails (should not happen in practice).
		return action.ContainerID + ":" + action.ServiceName + ":" + time.Now().UTC().Format("20060102150405.000000000")
	}
	return action.ContainerID + ":" + action.ServiceName + ":" + hex.EncodeToString(buf[:])
}

// EnqueueAction records a failed action for retry. It classifies the error
// to decide whether the action is retryable or permanently dead. If the queue
// is at capacity, the action is rejected and counted in the overflow metric.
func (sm *Manager) EnqueueAction(action types.Action, err error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sm.compMaxQueueSize > 0 && len(sm.pendingActions) >= sm.compMaxQueueSize {
		metrics.IncCompensationQueueOverflow()
		sm.logger.Warn("Compensation queue full, rejecting enqueue",
			"action", action.Kind,
			"hostname", action.Hostname,
			"queue_size", len(sm.pendingActions),
			"queue_cap", sm.compMaxQueueSize,
			"error_class", classifyError(err),
			"error", err)
		return
	}

	now := time.Now().UTC()
	id := newCompensationID(action)

	rec := &types.CompensationRecord{
		ID:          id,
		Action:      action,
		RetryCount:  0,
		MaxRetries:  sm.compMaxRetries,
		NextRetryAt: now.Add(sm.compInitialDelay),
		LastError:   err.Error(),
		CreatedAt:   now,
		Dead:        false,
	}

	errorClass := classifyError(err)
	if errorClass == "permanent" {
		rec.Dead = true
		sm.logger.Error("Action permanently failed, marking dead",
			"action", action.Kind,
			"hostname", action.Hostname,
			"error_class", errorClass,
			"error", err)
	} else {
		sm.logger.Warn("Action enqueued for compensation retry",
			"action", action.Kind,
			"hostname", action.Hostname,
			"error_class", errorClass,
			"error", err)
	}

	sm.pendingActions[id] = rec
	sm.markDirty()
}

// GetAllPendingActions returns a copy of all compensation records.
//
// Each *CompensationRecord is cloned by value under the read lock. The
// compensation goroutine (processCompensationQueue) mutates RetryCount,
// NextRetryAt, LastError and Dead outside this lock — returning live pointers
// would let callers race with those writes. See TestGetAllPendingActions_ReturnsCopies.
func (sm *Manager) GetAllPendingActions() []*types.CompensationRecord {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	result := make([]*types.CompensationRecord, 0, len(sm.pendingActions))
	for _, rec := range sm.pendingActions {
		copied := *rec
		result = append(result, &copied)
	}
	return result
}

// HasPendingActionKind reports whether the compensation queue already holds a
// non-dead record of the given kind. Used to deduplicate repeated enqueues of
// the same compensation (e.g. one ActionSync per sync failure burst, B3).
func (sm *Manager) HasPendingActionKind(kind types.ActionKind) bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	for _, rec := range sm.pendingActions {
		if rec.Action.Kind == kind && !rec.Dead {
			return true
		}
	}
	return false
}

// GetDeadActions returns compensation records that are permanently failed.
func (sm *Manager) GetDeadActions() []types.CompensationRecord {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	var dead []types.CompensationRecord
	for _, rec := range sm.pendingActions {
		if rec.Dead {
			dead = append(dead, *rec)
		}
	}
	return dead
}

// RunCompensation runs a background loop that retries failed actions.
// Blocks until ctx is cancelled.
func (sm *Manager) RunCompensation(ctx context.Context, executor func(types.Action) error) {
	for {
		sm.mu.RLock()
		interval := sm.compPollInterval
		sm.mu.RUnlock()

		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}

		sm.processCompensationQueue(executor)
	}
}

// processCompensationQueue processes due compensation records.
func (sm *Manager) processCompensationQueue(executor func(types.Action) error) {
	sm.mu.Lock()

	for id, rec := range sm.pendingActions {
		if rec.Dead {
			continue
		}
		// 使用进入循环时的时间判断是否到期
		if time.Now().Before(rec.NextRetryAt) {
			continue
		}

		action := rec.Action
		sm.mu.Unlock()
		err := executor(action)
		sm.mu.Lock()

		// After re-acquiring lock, re-fetch the record in case it was modified
		rec, exists := sm.pendingActions[id]
		if !exists {
			continue
		}

		if err == nil {
			delete(sm.pendingActions, id)
			sm.markDirty()
			sm.logger.Info("Compensation action succeeded",
				"action", action.Kind,
				"hostname", action.Hostname,
				"error_class", "none")
			continue
		}

		rec.LastError = err.Error()
		errorClass := classifyError(err)

		var permanent *types.PermanentError
		if errors.As(err, &permanent) {
			rec.Dead = true
			sm.markDirty()
			sm.logger.Error("Compensation action permanently failed",
				"action", action.Kind,
				"hostname", action.Hostname,
				"error_class", errorClass,
				"error", err)
			continue
		}

		rec.RetryCount++
		if rec.RetryCount >= rec.MaxRetries {
			rec.Dead = true
			sm.markDirty()
			sm.logger.Error("Compensation action exhausted retries",
				"action", action.Kind,
				"hostname", action.Hostname,
				"error_class", errorClass,
				"retries", rec.RetryCount,
				"error", err)
			continue
		}

		backoff := sm.compInitialDelay * time.Duration(1<<uint(rec.RetryCount))
		if backoff > sm.compMaxDelay {
			backoff = sm.compMaxDelay
		}
		// 重新取 now：executor 可能很慢（涉及 CF API 调用），用循环入口的 now
		// 会让 NextRetryAt 偏早，多次慢重试后 backoff 失真叠加。
		rec.NextRetryAt = time.Now().Add(backoff)
		sm.markDirty()
		sm.logger.Warn("Compensation action failed, will retry",
			"action", action.Kind,
			"hostname", action.Hostname,
			"error_class", errorClass,
			"retry_count", rec.RetryCount,
			"next_retry_at", rec.NextRetryAt,
			"error", err)
	}

	// Clean up Dead records so permanently-failed or retry-exhausted actions
	// cannot occupy the queue forever (B3/B15).
	cleaned := 0
	for id, rec := range sm.pendingActions {
		if rec.Dead {
			delete(sm.pendingActions, id)
			cleaned++
		}
	}
	if cleaned > 0 {
		sm.markDirty()
		sm.logger.Info("Cleaned up dead compensation records", "count", cleaned)
	}

	sm.mu.Unlock()
}
