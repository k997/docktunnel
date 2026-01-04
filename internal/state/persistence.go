package state

import (
	"encoding/gob"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	StateVersion = 1
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
// Tries gob encoding first, falls back to JSON, handles corruption (T075)
func (sm *Manager) Load(statePath string) error {
	if statePath == "" {
		statePath = StateFileDefault
	}

	// Try gob format first
	if _, err := os.Stat(statePath); err == nil {
		if err := sm.loadGob(statePath); err != nil {
			slog.Warn("Failed to load gob state file, trying JSON fallback",
				"path", statePath,
				"error", err)
			// Try JSON fallback
			jsonPath := statePath + ".json"
			if _, err := os.Stat(jsonPath); err == nil {
				if err := sm.loadJSON(jsonPath); err != nil {
					slog.Error("Failed to load both gob and JSON state files",
						"gob_path", statePath,
						"json_path", jsonPath,
						"error", err)
					// Return nil to allow startup to continue (T075)
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

	// Validate version
	if snapshot.Version != StateVersion {
		return fmt.Errorf("unsupported state version: %d (expected: %d)", snapshot.Version, StateVersion)
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

	// Validate version
	if snapshot.Version != StateVersion {
		return fmt.Errorf("unsupported state version: %d (expected: %d)", snapshot.Version, StateVersion)
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
}
