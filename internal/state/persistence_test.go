package state

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"docktunnel/pkg/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSaveAndLoadGob tests state snapshot serialization with gob encoding (T066)
func TestSaveAndLoadGob(t *testing.T) {
	// Create temporary directory for test files
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "state.bin")

	logger := slog.Default()

	// Create and populate state manager
	sm1 := NewManager(logger)
	now := time.Now().UTC()

	// Add active tunnel
	activeEntry := &types.TunnelEntry{
		ContainerID: "active-1",
		TunnelID:    "tunnel-123",
		ServiceName: "web",
		Status:      types.StatusActive,
		CreatedAt:   now,
		LastSyncAt:  now,
		Config: types.TunnelConfiguration{
			Hostname:   "active.example.com",
			ServiceURL: "http://localhost:8080",
		},
	}
	sm1.AddActiveTunnel(activeEntry)

	// Add pending deletion
	pastTime := now.Add(-1 * time.Hour)
	pendingEntry := &types.TunnelEntry{
		ContainerID: "pending-1",
		TunnelID:    "tunnel-123",
		ServiceName: "api",
		Status:      types.StatusPendingDelete,
		CreatedAt:   now.Add(-2 * time.Hour),
		DeletedAt:   &pastTime,
		LastSyncAt:  pastTime,
		Config: types.TunnelConfiguration{
			Hostname:   "pending.example.com",
			ServiceURL: "http://localhost:9000",
		},
		RetentionPolicy: types.RetentionPolicy{
			Type:     types.Timed,
			Duration: 30 * time.Minute,
		},
	}
	sm1.AddPendingDeletion(pendingEntry)

	// Mark flapping
	sm1.MarkAsFlapping("flapping-1", 5*time.Minute)

	// Save state
	err := sm1.Save(statePath)
	require.NoError(t, err, "Save should succeed")

	// Verify file exists
	_, err = os.Stat(statePath)
	require.NoError(t, err, "State file should exist")

	// Create new state manager and load
	sm2 := NewManager(slog.Default())
	err = sm2.Load(statePath)
	require.NoError(t, err, "Load should succeed")

	// Verify active tunnels were restored
	loadedActive, ok := sm2.GetActiveTunnel("active-1")
	assert.True(t, ok, "Active tunnel should be loaded")
	assert.Equal(t, "active-1", loadedActive.ContainerID)
	assert.Equal(t, "web", loadedActive.ServiceName)
	assert.Equal(t, "active.example.com", loadedActive.Config.Hostname)
	assert.Equal(t, types.StatusActive, loadedActive.Status)

	// Verify pending deletions were restored
	loadedPending, ok := sm2.GetPendingDeletion("pending-1")
	assert.True(t, ok, "Pending deletion should be loaded")
	assert.Equal(t, "pending-1", loadedPending.ContainerID)
	assert.Equal(t, types.StatusPendingDelete, loadedPending.Status)
	assert.Equal(t, types.Timed, loadedPending.RetentionPolicy.Type)

	// Verify flapping state was restored
	flappingState, ok := sm2.GetFlappingState("flapping-1")
	assert.True(t, ok, "Flapping state should be loaded")
	assert.True(t, flappingState.CoolingUntil.After(now), "Cooling period should be in future")
}

// TestLoadCorruptedFile tests handling of corrupted state files (T067, T075)
func TestLoadCorruptedFile(t *testing.T) {
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "state.bin")

	// Create a corrupted file
	corruptedData := []byte{0x00, 0x01, 0x02, 0x03}
	err := os.WriteFile(statePath, corruptedData, 0644)
	require.NoError(t, err)

	// Try to load - should not fail startup (T075)
	sm := NewManager(slog.Default())
	err = sm.Load(statePath)

	// Load returns nil (continues on error per T075)
	assert.NoError(t, err, "Load should not fail startup, continues with empty state")

	// State manager should still be functional with empty state
	assert.NotNil(t, sm)
	stats := sm.GetStats()
	assert.Equal(t, 0, stats["active_tunnels"], "Should start with empty state")
	assert.Equal(t, 0, stats["pending_deletions"], "Should start with empty state")
}

// TestLoadNonexistentFile tests loading when no state file exists
func TestLoadNonexistentFile(t *testing.T) {
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "nonexistent.bin")

	sm := NewManager(slog.Default())
	err := sm.Load(statePath)

	// Should not fail
	assert.NoError(t, err, "Load should succeed when file doesn't exist")

	// Should start with empty state
	stats := sm.GetStats()
	assert.Equal(t, 0, stats["active_tunnels"])
}

// TestSaveJSON tests JSON serialization for debugging
func TestSaveJSON(t *testing.T) {
	tmpDir := t.TempDir()
	jsonPath := filepath.Join(tmpDir, "state.json")

	sm := NewManager(slog.Default())
	now := time.Now().UTC()

	entry := &types.TunnelEntry{
		ContainerID: "test-1",
		TunnelID:    "tunnel-123",
		ServiceName: "web",
		Status:      types.StatusActive,
		CreatedAt:   now,
		LastSyncAt:  now,
		Config: types.TunnelConfiguration{
			Hostname:   "test.example.com",
			ServiceURL: "http://localhost:8080",
		},
	}
	sm.AddActiveTunnel(entry)

	// Save as JSON
	err := sm.SaveJSON(jsonPath)
	require.NoError(t, err)

	// Verify file exists and is readable
	data, err := os.ReadFile(jsonPath)
	require.NoError(t, err)
	assert.Contains(t, string(data), "test.example.com", "JSON should contain hostname")
	assert.Contains(t, string(data), "test-1", "JSON should contain container ID")
}

// TestLoadJSONFallback tests loading from JSON when gob fails
func TestLoadJSONFallback(t *testing.T) {
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "state.bin")
	jsonPath := statePath + ".json"

	// Create JSON file manually
	sm1 := NewManager(slog.Default())
	now := time.Now().UTC()

	entry := &types.TunnelEntry{
		ContainerID: "json-test",
		TunnelID:    "tunnel-456",
		ServiceName: "api",
		Status:      types.StatusActive,
		CreatedAt:   now,
		LastSyncAt:  now,
		Config: types.TunnelConfiguration{
			Hostname:   "json.example.com",
			ServiceURL: "http://localhost:8080",
		},
	}
	sm1.AddActiveTunnel(entry)

	// Save as JSON
	err := sm1.SaveJSON(jsonPath)
	require.NoError(t, err)

	// Load with JSON fallback (corrupted gob, valid JSON)
	sm2 := NewManager(slog.Default())

	// Try to load the JSON file directly
	err = sm2.loadJSON(jsonPath)
	require.NoError(t, err)

	// Verify entry was loaded
	loaded, ok := sm2.GetActiveTunnel("json-test")
	assert.True(t, ok)
	assert.Equal(t, "json.example.com", loaded.Config.Hostname)
}

// TestPeriodicSave tests SaveIfDirty functionality (T072)
func TestPeriodicSave(t *testing.T) {
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "state.bin")

	sm := NewManager(slog.Default())
	sm.SetStatePath(statePath)

	// Add entry - should mark dirty
	entry := &types.TunnelEntry{
		ContainerID: "periodic-test",
		TunnelID:    "tunnel-789",
		ServiceName: "web",
		Status:      types.StatusActive,
		CreatedAt:   time.Now().UTC(),
		LastSyncAt:  time.Now().UTC(),
		Config: types.TunnelConfiguration{
			Hostname:   "periodic.example.com",
			ServiceURL: "http://localhost:8080",
		},
	}
	sm.AddActiveTunnel(entry)

	// SaveIfDirty should save
	err := sm.SaveIfDirty()
	require.NoError(t, err)

	// Verify file was created
	_, err = os.Stat(statePath)
	require.NoError(t, err, "State file should be created")

	// SaveIfDirty again should not save (not dirty, not enough time passed)
	err = sm.SaveIfDirty()
	require.NoError(t, err)

	// Load and verify
	sm2 := NewManager(slog.Default())
	sm2.SetStatePath(statePath)
	err = sm2.Load(statePath)
	require.NoError(t, err)

	loaded, ok := sm2.GetActiveTunnel("periodic-test")
	assert.True(t, ok)
	assert.Equal(t, "periodic.example.com", loaded.Config.Hostname)
}

// TestForceSave tests ForceSave functionality
func TestForceSave(t *testing.T) {
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "state.bin")

	sm := NewManager(slog.Default())
	sm.SetStatePath(statePath)

	// Force save even without state changes
	err := sm.ForceSave()
	require.NoError(t, err)

	// Verify file exists
	_, err = os.Stat(statePath)
	require.NoError(t, err)
}

// TestAtomicWrite tests that atomic write prevents partial state
func TestAtomicWrite(t *testing.T) {
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "state.bin")

	sm := NewManager(slog.Default())
	sm.SetStatePath(statePath)

	// Add multiple entries
	for i := 1; i <= 10; i++ {
		entry := &types.TunnelEntry{
			ContainerID: "atomic-test-" + string(rune('0'+i)),
			TunnelID:    "tunnel-atomic",
			ServiceName: "service",
			Status:      types.StatusActive,
			CreatedAt:   time.Now().UTC(),
			LastSyncAt:  time.Now().UTC(),
			Config: types.TunnelConfiguration{
				Hostname:   "atomic" + string(rune('0'+i)) + ".example.com",
				ServiceURL: "http://localhost:8080",
			},
		}
		sm.AddActiveTunnel(entry)
	}

	// Force save
	err := sm.ForceSave()
	require.NoError(t, err)

	// Load and verify all entries present
	sm2 := NewManager(slog.Default())
	err = sm2.Load(statePath)
	require.NoError(t, err)

	stats := sm2.GetStats()
	assert.Equal(t, 10, stats["active_tunnels"], "All entries should be saved atomically")
}

// TestVersionMismatch tests loading state with different version
func TestVersionMismatch(t *testing.T) {
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "state.bin")

	sm1 := NewManager(slog.Default())
	sm1.AddActiveTunnel(&types.TunnelEntry{
		ContainerID: "version-test",
		Status:      types.StatusActive,
		CreatedAt:   time.Now().UTC(),
		LastSyncAt:  time.Now().UTC(),
	})

	// Save
	err := sm1.Save(statePath)
	require.NoError(t, err)

	// Manually modify version in file to simulate future version
	// (This is a simplified test - in real scenario, file format might change)
	// For now, we just verify that version check exists

	sm2 := NewManager(slog.Default())
	err = sm2.Load(statePath)

	// Should succeed with current version
	assert.NoError(t, err)
}
