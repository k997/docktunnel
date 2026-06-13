package state

import (
	"encoding/gob"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"docktunnel/pkg/types"

	"log/slog"
)

const (
	// StateFileDefault is the default path for state persistence
	StateFileDefault = "/var/lib/docktunnel/state.bin"
	// StateFileJSON is the JSON fallback path
	StateFileJSON = "/var/lib/docktunnel/state.json"
	// StateVersion is the current state format version
	StateVersion = 3
)

// Save persists the state manager's snapshot to disk (T070)
// Uses gob encoding with atomic write (tmp file + rename)
func (sm *Manager) Save(statePath string) error {
	if statePath == "" {
		statePath = StateFileDefault
	}

	// Get snapshot from state manager
	// Note: GetSnapshot acquires its own lock, so we shouldn't hold sm.mu here
	snapshot := sm.GetSnapshot()

	// Rotate backups before writing new state
	sm.rotateBackups(statePath)

	// Ensure directory exists
	stateDir := filepath.Dir(statePath)
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		return fmt.Errorf("failed to create state directory: %w", err)
	}

	// Write to temporary file first (atomic write)
	tmpFile := statePath + ".tmp"
	file, err := os.Create(tmpFile)
	if err != nil {
		return fmt.Errorf("failed to create temp state file: %w", err)
	}

	// Use gob encoding for binary format
	encoder := gob.NewEncoder(file)
	if err := encoder.Encode(snapshot); err != nil {
		file.Close()
		os.Remove(tmpFile)
		return fmt.Errorf("failed to encode state snapshot: %w", err)
	}

	// Sync to disk and close
	if err := file.Sync(); err != nil {
		file.Close()
		os.Remove(tmpFile)
		return fmt.Errorf("failed to sync state file: %w", err)
	}
	if err := file.Close(); err != nil {
		os.Remove(tmpFile)
		return fmt.Errorf("failed to close state file: %w", err)
	}

	// Atomic rename
	if err := os.Rename(tmpFile, statePath); err != nil {
		return fmt.Errorf("failed to rename temp state file: %w", err)
	}

	slog.Debug("State snapshot saved successfully",
		"path", statePath,
		"version", snapshot.Version,
		"timestamp", snapshot.Timestamp,
		"active_tunnels", len(snapshot.ActiveTunnels),
		"pending_deletions", len(snapshot.PendingDeletions),
	)

	return nil
}

// Load loads a persisted state snapshot from disk (T071)
// Tries gob encoding first, falls back to backups, then JSON, handles corruption (T075)
func (sm *Manager) Load(statePath string) error {
	if statePath == "" {
		statePath = StateFileDefault
	}

	// Try gob format first
	if _, err := os.Stat(statePath); err == nil {
		if err := sm.loadGob(statePath); err != nil {
			slog.Warn("Failed to load gob state file",
				"path", statePath,
				"error", err)

			// Try backup files (newest first)
			if loadedPath := sm.tryLoadBackups(statePath); loadedPath != "" {
				return nil
			}

			// Try JSON fallback
			jsonPath := statePath + ".json"
			if _, err := os.Stat(jsonPath); err == nil {
				if err := sm.loadJSON(jsonPath); err != nil {
					slog.Error("Failed to load both gob and JSON state files",
						"gob_path", statePath,
						"json_path", jsonPath,
						"error", err)
					slog.Info("Continuing with empty state due to load failures")
					return nil
				}
			} else {
				slog.Info("No JSON fallback found, continuing with empty state")
				return nil
			}
		}
		return nil
	}

	slog.Info("No existing state file found, starting with empty state", "path", statePath)
	return nil
}

// tryLoadBackups attempts to load state from backup files, newest first.
// Returns the path of the successfully loaded backup, or empty string if all fail.
func (sm *Manager) tryLoadBackups(statePath string) string {
	pattern := statePath + ".bak.*"
	matches, err := filepath.Glob(pattern)
	if err != nil || len(matches) == 0 {
		return ""
	}

	// Sort newest first
	sort.Sort(sort.Reverse(sort.StringSlice(matches)))

	for _, backupPath := range matches {
		slog.Info("Trying backup state file", "path", backupPath)
		if err := sm.loadGob(backupPath); err != nil {
			slog.Warn("Backup file also failed to load",
				"path", backupPath,
				"error", err)
			continue
		}
		slog.Info("Successfully loaded state from backup",
			"path", backupPath)
		return backupPath
	}

	slog.Warn("All backup files failed to load", "tried", len(matches))
	return ""
}

// loadGob loads state from gob-encoded file
func (sm *Manager) loadGob(statePath string) error {
	file, err := os.Open(statePath)
	if err != nil {
		return fmt.Errorf("failed to open state file: %w", err)
	}
	defer file.Close()

	var snapshot types.StateSnapshot
	decoder := gob.NewDecoder(file)
	if err := decoder.Decode(&snapshot); err != nil {
		return fmt.Errorf("failed to decode gob state: %w", err)
	}

	// Validate version (allow backward compatibility for migration)
	if snapshot.Version > StateVersion {
		return fmt.Errorf("unsupported state version: %d (max supported: %d)", snapshot.Version, StateVersion)
	}

	// Validate snapshot if enabled
	sm.mu.RLock()
	shouldValidate := sm.validateOnLoad
	sm.mu.RUnlock()

	if shouldValidate {
		if err := validateSnapshot(&snapshot); err != nil {
			sm.logger.Warn("Snapshot validation failed",
				"path", statePath,
				"error", err)
			return fmt.Errorf("snapshot validation failed: %w", err)
		}
	}

	// Load into state manager
	sm.LoadFromSnapshot(&snapshot)

	slog.Info("State loaded successfully from gob",
		"path", statePath,
		"version", snapshot.Version,
		"timestamp", snapshot.Timestamp,
		"active_tunnels", len(snapshot.ActiveTunnels),
		"pending_deletions", len(snapshot.PendingDeletions),
		"flapping_containers", len(snapshot.FlappingContainers),
	)

	return nil
}

// loadJSON loads state from JSON file (fallback format)
func (sm *Manager) loadJSON(statePath string) error {
	file, err := os.Open(statePath)
	if err != nil {
		return fmt.Errorf("failed to open JSON state file: %w", err)
	}
	defer file.Close()

	var snapshot types.StateSnapshot
	decoder := json.NewDecoder(file)
	if err := decoder.Decode(&snapshot); err != nil {
		return fmt.Errorf("failed to decode JSON state: %w", err)
	}

	// Validate version (allow backward compatibility for migration)
	if snapshot.Version > StateVersion {
		return fmt.Errorf("unsupported state version: %d (max supported: %d)", snapshot.Version, StateVersion)
	}

	// Validate snapshot if enabled
	sm.mu.RLock()
	shouldValidate := sm.validateOnLoad
	sm.mu.RUnlock()

	if shouldValidate {
		if err := validateSnapshot(&snapshot); err != nil {
			sm.logger.Warn("Snapshot validation failed",
				"path", statePath,
				"error", err)
			return fmt.Errorf("snapshot validation failed: %w", err)
		}
	}

	// Load into state manager
	sm.LoadFromSnapshot(&snapshot)

	slog.Info("State loaded successfully from JSON",
		"path", statePath,
		"version", snapshot.Version,
		"timestamp", snapshot.Timestamp,
		"active_tunnels", len(snapshot.ActiveTunnels),
		"pending_deletions", len(snapshot.PendingDeletions),
		"flapping_containers", len(snapshot.FlappingContainers),
	)

	return nil
}

// SaveJSON persists state as JSON (for debugging/human inspection)
func (sm *Manager) SaveJSON(statePath string) error {
	if statePath == "" {
		statePath = StateFileJSON
	}

	snapshot := sm.GetSnapshot()

	// Ensure directory exists
	stateDir := filepath.Dir(statePath)
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		return fmt.Errorf("failed to create state directory: %w", err)
	}

	// Write to temporary file first
	tmpFile := statePath + ".tmp"
	file, err := os.Create(tmpFile)
	if err != nil {
		return fmt.Errorf("failed to create temp JSON state file: %w", err)
	}

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(snapshot); err != nil {
		file.Close()
		os.Remove(tmpFile)
		return fmt.Errorf("failed to encode JSON state: %w", err)
	}

	// Sync and close
	if err := file.Sync(); err != nil {
		file.Close()
		os.Remove(tmpFile)
		return fmt.Errorf("failed to sync JSON state file: %w", err)
	}
	if err := file.Close(); err != nil {
		os.Remove(tmpFile)
		return fmt.Errorf("failed to close JSON state file: %w", err)
	}

	// Atomic rename
	if err := os.Rename(tmpFile, statePath); err != nil {
		return fmt.Errorf("failed to rename temp JSON state file: %w", err)
	}

	slog.Debug("State JSON snapshot saved successfully",
		"path", statePath,
		"active_tunnels", len(snapshot.ActiveTunnels),
	)

	return nil
}

// RegisterGobTypes registers all types used for gob encoding
// This should be called once at application startup
func RegisterGobTypes() {
	gob.Register(time.Time{})
	gob.Register(types.StateSnapshot{})
	gob.Register(types.TunnelEntry{})
	gob.Register(types.TunnelConfiguration{})
	gob.Register(types.OriginRequestConfig{})
	gob.Register(types.AccessConfig{})
	gob.Register(types.RetentionPolicy{})
	gob.Register(types.PolicyType(0))
	gob.Register(types.EntryStatus(0))
	gob.Register(types.FlappingState{})
	gob.Register(map[string]*types.TunnelEntry{})
	gob.Register(map[string]types.FlappingState{})
	gob.Register(types.TransitionEvent(0))
	gob.Register(types.ActionKind(0))
	gob.Register(types.Action{})
	gob.Register(types.CompensationRecord{})
	gob.Register(map[string]*types.CompensationRecord{})
}

// validateSnapshot checks a loaded snapshot for data integrity issues.
// Returns a multi-error describing all problems found, or nil if valid.
func validateSnapshot(snapshot *types.StateSnapshot) error {
	var errs []string

	// 1. Timestamp check
	if snapshot.Timestamp.IsZero() {
		errs = append(errs, "timestamp is zero")
	} else if snapshot.Timestamp.After(time.Now().UTC().Add(5 * time.Minute)) {
		errs = append(errs, fmt.Sprintf("timestamp is in the future: %v", snapshot.Timestamp))
	}

	// 2. Key consistency — ActiveTunnels
	for key, entry := range snapshot.ActiveTunnels {
		expected := entry.ContainerID + ":" + entry.ServiceName
		if key != expected {
			errs = append(errs, fmt.Sprintf("activeTunnels key mismatch: key=%q expected=%q", key, expected))
		}
	}

	// 2. Key consistency — PendingDeletions
	for key, entry := range snapshot.PendingDeletions {
		expected := entry.ContainerID + ":" + entry.ServiceName
		if key != expected {
			errs = append(errs, fmt.Sprintf("pendingDeletions key mismatch: key=%q expected=%q", key, expected))
		}
	}

	// 3. Compensation records
	for mapKey, rec := range snapshot.PendingActions {
		if rec.ID == "" {
			errs = append(errs, fmt.Sprintf("compensation record %q has empty ID", mapKey))
		}
		if rec.CreatedAt.IsZero() {
			errs = append(errs, fmt.Sprintf("compensation record %q has zero CreatedAt", mapKey))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("snapshot validation failed: %s", strings.Join(errs, "; "))
	}
	return nil
}

// rotateBackups renames the current state file to a timestamped backup
// and prunes old backups beyond backupCount. Returns the first error encountered,
// but continues on individual failures.
func (sm *Manager) rotateBackups(statePath string) error {
	sm.mu.RLock()
	count := sm.backupCount
	sm.mu.RUnlock()

	if count <= 0 {
		return nil
	}

	// Only rotate if the current state file exists
	if _, err := os.Stat(statePath); err != nil {
		return nil // no file to rotate
	}

	// Rename current file to timestamped backup
	ts := time.Now().UTC().Format("20060102-150405")
	backupPath := statePath + ".bak." + ts
	if err := os.Rename(statePath, backupPath); err != nil {
		sm.logger.Warn("Failed to rotate state file to backup",
			"from", statePath,
			"to", backupPath,
			"error", err)
		return err
	}

	// Prune old backups beyond backupCount
	sm.pruneBackups(statePath, count)
	return nil
}

// pruneBackups removes the oldest backup files, keeping at most count.
func (sm *Manager) pruneBackups(statePath string, count int) {
	pattern := statePath + ".bak.*"
	matches, err := filepath.Glob(pattern)
	if err != nil {
		sm.logger.Warn("Failed to glob backup files", "pattern", pattern, "error", err)
		return
	}

	if len(matches) <= count {
		return
	}

	// Sort newest first (lexicographic works because timestamp is fixed-width)
	sort.Sort(sort.Reverse(sort.StringSlice(matches)))

	// Delete oldest files beyond count
	for _, path := range matches[count:] {
		if err := os.Remove(path); err != nil {
			sm.logger.Warn("Failed to remove old backup", "path", path, "error", err)
		}
	}
}
