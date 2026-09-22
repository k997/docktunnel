# Phase 5: Persistence Closed Loop — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add backup rotation, enhanced snapshot validation, and corruption recovery to the gob state persistence layer.

**Architecture:** Three additions to the existing `internal/state/persistence.go` — a `rotateBackups()` method called before every `Save()`, a `validateSnapshot()` function called after every decode, and a modified `Load()` that tries backup files when the primary fails. Config fields added to `internal/config/config.go` for `backupCount` and `validateOnLoad`, wired through to the state manager via `main.go`.

**Tech Stack:** Go 1.24, `path/filepath`, `os`, `sort`, `strings`, `errors`, `time`, `testing`, `testify`

---

### Task 1: Add `backupCount` and `validateOnLoad` fields to Manager

**Files:**
- Modify: `internal/state/manager.go:28-82`

- [ ] **Step 1: Write the failing test**

Add to `internal/state/persistence_test.go`:

```go
func TestNewManager_DefaultBackupCount(t *testing.T) {
	sm := NewManager(slog.Default())
	assert.Equal(t, 3, sm.BackupCount(), "default backupCount should be 3")
}

func TestNewManager_DefaultValidateOnLoad(t *testing.T) {
	sm := NewManager(slog.Default())
	assert.True(t, sm.ValidateOnLoad(), "default validateOnLoad should be true")
}

func TestSetBackupCount(t *testing.T) {
	sm := NewManager(slog.Default())
	sm.SetBackupCount(5)
	assert.Equal(t, 5, sm.BackupCount())
}

func TestSetValidateOnLoad(t *testing.T) {
	sm := NewManager(slog.Default())
	sm.SetValidateOnLoad(false)
	assert.False(t, sm.ValidateOnLoad())
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/state/ -run "TestNewManager_DefaultBackupCount|TestSetBackupCount|TestNewManager_DefaultValidateOnLoad|TestSetValidateOnLoad" -v`
Expected: FAIL — `BackupCount`, `ValidateOnLoad`, `SetBackupCount`, `SetValidateOnLoad` undefined.

- [ ] **Step 3: Write minimal implementation**

In `internal/state/manager.go`, add two fields to the Manager struct (after `compPollInterval` at line 37):

```go
backupCount     int
validateOnLoad  bool
```

In `NewManager` (line 67), add to the return struct:

```go
backupCount:    3,
validateOnLoad: true,
```

Add two getter methods and two setter methods after `SetStatePath` (after line 89):

```go
// BackupCount returns the configured backup count.
func (sm *Manager) BackupCount() int {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.backupCount
}

// ValidateOnLoad returns whether snapshot validation is enabled.
func (sm *Manager) ValidateOnLoad() bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.validateOnLoad
}

// SetBackupCount sets the number of backup files to keep.
func (sm *Manager) SetBackupCount(n int) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.backupCount = n
}

// SetValidateOnLoad enables or disables snapshot validation on load.
func (sm *Manager) SetValidateOnLoad(enabled bool) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.validateOnLoad = enabled
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/state/ -run "TestNewManager_DefaultBackupCount|TestSetBackupCount|TestNewManager_DefaultValidateOnLoad|TestSetValidateOnLoad" -v`
Expected: PASS

- [ ] **Step 5: Run full test suite**

Run: `go test ./internal/state/ -v`
Expected: All existing tests still pass.

- [ ] **Step 6: Commit**

```bash
git add internal/state/manager.go internal/state/persistence_test.go
git commit -m "feat(state): add backupCount and validateOnLoad fields to Manager"
```

---

### Task 2: Implement `validateSnapshot()`

**Files:**
- Modify: `internal/state/persistence.go` (add function after `RegisterGobTypes`)

- [ ] **Step 1: Write the failing tests**

Add to `internal/state/persistence_test.go`:

```go
func TestValidateSnapshot_Valid(t *testing.T) {
	now := time.Now().UTC()
	snapshot := &types.StateSnapshot{
		Version:   3,
		Timestamp: now,
		ActiveTunnels: map[string]*types.TunnelEntry{
			"abc123:web": {
				ContainerID: "abc123",
				ServiceName: "web",
				Status:      types.StatusActive,
				Config:      types.TunnelConfiguration{Hostname: "web.example.com"},
			},
		},
		PendingDeletions: map[string]*types.TunnelEntry{
			"def456:api": {
				ContainerID: "def456",
				ServiceName: "api",
				Status:      types.StatusPendingDelete,
				Config:      types.TunnelConfiguration{Hostname: "api.example.com"},
			},
		},
		PendingActions: map[string]*types.CompensationRecord{
			"rec1": {ID: "rec1", CreatedAt: now, Action: types.Action{Kind: types.ActionDeleteRoute}},
		},
	}
	err := validateSnapshot(snapshot)
	assert.NoError(t, err)
}

func TestValidateSnapshot_ZeroTimestamp(t *testing.T) {
	snapshot := &types.StateSnapshot{
		Version:   3,
		Timestamp: time.Time{},
	}
	err := validateSnapshot(snapshot)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "timestamp")
}

func TestValidateSnapshot_FutureTimestamp(t *testing.T) {
	snapshot := &types.StateSnapshot{
		Version:   3,
		Timestamp: time.Now().UTC().Add(10 * time.Minute),
	}
	err := validateSnapshot(snapshot)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "future")
}

func TestValidateSnapshot_KeyMismatch(t *testing.T) {
	snapshot := &types.StateSnapshot{
		Version:   3,
		Timestamp: time.Now().UTC(),
		ActiveTunnels: map[string]*types.TunnelEntry{
			"wrong-key": {
				ContainerID: "abc123",
				ServiceName: "web",
				Status:      types.StatusActive,
			},
		},
	}
	err := validateSnapshot(snapshot)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "key mismatch")
}

func TestValidateSnapshot_InvalidCompensationRecord(t *testing.T) {
	snapshot := &types.StateSnapshot{
		Version:   3,
		Timestamp: time.Now().UTC(),
		PendingActions: map[string]*types.CompensationRecord{
			"rec1": {ID: "", CreatedAt: time.Time{}},
		},
	}
	err := validateSnapshot(snapshot)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "compensation record")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/state/ -run "TestValidateSnapshot" -v`
Expected: FAIL — `validateSnapshot` undefined.

- [ ] **Step 3: Write minimal implementation**

Add to `internal/state/persistence.go`, after the `RegisterGobTypes` function:

```go
// validateSnapshot checks a loaded snapshot for data integrity issues.
// Returns a multi-error describing all problems found, or nil if valid.
func validateSnapshot(snapshot *types.StateSnapshot) error {
	var errs []string

	// 1. Timestamp check
	if snapshot.Timestamp.IsZero() {
		errs = append(errs, "timestamp is zero")
	} else if snapshot.Timestamp.After(time.Now().UTC().Add(5*time.Minute)) {
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
```

Note: add `"strings"` to the imports if not already present (it is already imported via `manager.go` but `persistence.go` needs it too — check current imports and add if missing).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/state/ -run "TestValidateSnapshot" -v`
Expected: PASS

- [ ] **Step 5: Run full test suite**

Run: `go test ./internal/state/ -v`
Expected: All tests pass.

- [ ] **Step 6: Commit**

```bash
git add internal/state/persistence.go internal/state/persistence_test.go
git commit -m "feat(state): add validateSnapshot for startup data integrity checks"
```

---

### Task 3: Integrate `validateSnapshot` into `loadGob()` and `loadJSON()`

**Files:**
- Modify: `internal/state/persistence.go:122-187` (loadGob and loadJSON)

- [ ] **Step 1: Write the failing test**

Add to `internal/state/persistence_test.go`:

```go
func TestLoadGob_ValidatesSnapshot(t *testing.T) {
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "state.bin")

	// Save a valid snapshot
	sm1 := NewManager(slog.Default())
	sm1.AddActiveTunnel(&types.TunnelEntry{
		ContainerID: "abc",
		ServiceName: "web",
		Status:      types.StatusActive,
		CreatedAt:   time.Now().UTC(),
		LastSyncAt:  time.Now().UTC(),
		Config:      types.TunnelConfiguration{Hostname: "web.example.com"},
	})
	require.NoError(t, sm1.Save(statePath))

	// Load with validation enabled (default)
	sm2 := NewManager(slog.Default())
	err := sm2.Load(statePath)
	assert.NoError(t, err)

	_, ok := sm2.GetActiveTunnel("abc", "web")
	assert.True(t, ok, "valid snapshot should load successfully")
}

func TestLoadGob_ValidationSkippedWhenDisabled(t *testing.T) {
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "state.bin")

	// Create a snapshot with future timestamp manually
	snapshot := &types.StateSnapshot{
		Version:   3,
		Timestamp: time.Now().UTC().Add(1 * time.Hour), // far future
		ActiveTunnels: map[string]*types.TunnelEntry{
			"abc:web": {ContainerID: "abc", ServiceName: "web", Status: types.StatusActive},
		},
	}

	// Write the gob file directly
	file, err := os.Create(statePath)
	require.NoError(t, err)
	require.NoError(t, gob.NewEncoder(file).Encode(snapshot))
	file.Close()

	// Load with validation disabled — should succeed
	sm := NewManager(slog.Default())
	sm.SetValidateOnLoad(false)
	err = sm.Load(statePath)
	assert.NoError(t, err, "should load even with invalid data when validation disabled")

	_, ok := sm.GetActiveTunnel("abc", "web")
	assert.True(t, ok, "entry should be loaded despite invalid timestamp")
}
```

Note: add `"encoding/gob"` to the test file imports.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/state/ -run "TestLoadGob_ValidatesSnapshot|TestLoadGob_ValidationSkippedWhenDisabled" -v`
Expected: `TestLoadGob_ValidatesSnapshot` passes (validation not yet called), `TestLoadGob_ValidationSkippedWhenDisabled` may fail because loadGob doesn't check validateOnLoad yet. At least one should demonstrate the new behavior.

- [ ] **Step 3: Write minimal implementation**

Modify `loadGob()` in `internal/state/persistence.go`. After the version check (line 138) and before `sm.LoadFromSnapshot(&snapshot)` (line 141), add:

```go
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
```

Apply the same change to `loadJSON()`. After the version check (line 170) and before `sm.LoadFromSnapshot(&snapshot)` (line 174), add the identical block.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/state/ -run "TestLoadGob_ValidatesSnapshot|TestLoadGob_ValidationSkippedWhenDisabled" -v`
Expected: PASS

- [ ] **Step 5: Run full test suite**

Run: `go test ./internal/state/ -v`
Expected: All tests pass. Note: `TestLoadCorruptedFile` should still pass because gob decode fails before validation runs.

- [ ] **Step 6: Commit**

```bash
git add internal/state/persistence.go internal/state/persistence_test.go
git commit -m "feat(state): integrate validateSnapshot into loadGob and loadJSON"
```

---

### Task 4: Implement `rotateBackups()`

**Files:**
- Modify: `internal/state/persistence.go` (add method and helper)

- [ ] **Step 1: Write the failing tests**

Add to `internal/state/persistence_test.go`:

```go
func TestRotateBackups_CreatesTimestampedBackup(t *testing.T) {
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "state.bin")

	// Create initial state file
	sm := NewManager(slog.Default())
	sm.AddActiveTunnel(&types.TunnelEntry{
		ContainerID: "abc",
		ServiceName: "web",
		Status:      types.StatusActive,
		CreatedAt:   time.Now().UTC(),
		Config:      types.TunnelConfiguration{Hostname: "web.example.com"},
	})
	require.NoError(t, sm.Save(statePath))
	require.FileExists(t, statePath)

	// Save again — should create a backup
	require.NoError(t, sm.Save(statePath))
	require.FileExists(t, statePath)

	// Check that a backup file was created
	matches, err := filepath.Glob(statePath + ".bak.*")
	require.NoError(t, err)
	assert.Len(t, matches, 1, "should have exactly one backup")
	assert.Contains(t, filepath.Base(matches[0]), ".bak.")
}

func TestRotateBackups_PrunesOldBackups(t *testing.T) {
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "state.bin")

	sm := NewManager(slog.Default())
	sm.SetBackupCount(2)
	sm.AddActiveTunnel(&types.TunnelEntry{
		ContainerID: "abc",
		ServiceName: "web",
		Status:      types.StatusActive,
		CreatedAt:   time.Now().UTC(),
		Config:      types.TunnelConfiguration{Hostname: "web.example.com"},
	})

	// Save 4 times — should only keep 2 backups
	for i := 0; i < 4; i++ {
		time.Sleep(1100 * time.Millisecond) // ensure different timestamps
		require.NoError(t, sm.Save(statePath))
	}

	matches, err := filepath.Glob(statePath + ".bak.*")
	require.NoError(t, err)
	assert.Len(t, matches, 2, "should have exactly 2 backups (backupCount=2)")
}

func TestRotateBackups_DisabledWhenZero(t *testing.T) {
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "state.bin")

	sm := NewManager(slog.Default())
	sm.SetBackupCount(0)
	sm.AddActiveTunnel(&types.TunnelEntry{
		ContainerID: "abc",
		ServiceName: "web",
		Status:      types.StatusActive,
		CreatedAt:   time.Now().UTC(),
		Config:      types.TunnelConfiguration{Hostname: "web.example.com"},
	})

	require.NoError(t, sm.Save(statePath))

	matches, err := filepath.Glob(statePath + ".bak.*")
	require.NoError(t, err)
	assert.Len(t, matches, 0, "no backups when backupCount=0")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/state/ -run "TestRotateBackups" -v`
Expected: FAIL — no backup files are being created yet.

- [ ] **Step 3: Write minimal implementation**

Add to `internal/state/persistence.go` (new methods, after `rotateBackups`):

First, add `"path/filepath"` and `"sort"` to the imports if not present (check current imports).

```go
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
```

Now integrate `rotateBackups` into `Save()`. In the `Save()` method, after `snapshot := sm.GetSnapshot()` (line 34) and before `tmpFile := statePath + ".tmp"` (line 43), add:

```go
	// Rotate backups before writing new state
	sm.rotateBackups(statePath)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/state/ -run "TestRotateBackups" -v`
Expected: PASS

Note: `TestRotateBackups_PrunesOldBackups` sleeps 4.4s total. This is acceptable for correctness. If too slow, reduce sleep to `1010 * time.Millisecond` (1s + 10ms buffer for filename uniqueness).

- [ ] **Step 5: Run full test suite**

Run: `go test ./internal/state/ -v`
Expected: All tests pass.

- [ ] **Step 6: Commit**

```bash
git add internal/state/persistence.go internal/state/persistence_test.go
git commit -m "feat(state): add rotateBackups with timestamped backup files"
```

---

### Task 5: Implement backup fallback in `Load()`

**Files:**
- Modify: `internal/state/persistence.go:86-118` (Load method)

- [ ] **Step 1: Write the failing tests**

Add to `internal/state/persistence_test.go`:

```go
func TestLoad_FallsBackToBackup(t *testing.T) {
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "state.bin")

	sm := NewManager(slog.Default())
	sm.AddActiveTunnel(&types.TunnelEntry{
		ContainerID: "abc",
		ServiceName: "web",
		Status:      types.StatusActive,
		CreatedAt:   time.Now().UTC(),
		Config:      types.TunnelConfiguration{Hostname: "web.example.com"},
	})

	// Save — creates the state file
	require.NoError(t, sm.Save(statePath))

	// Verify backup exists
	matches, err := filepath.Glob(statePath + ".bak.*")
	require.NoError(t, err)
	require.Len(t, matches, 1, "should have one backup")

	// Corrupt the primary state file
	require.NoError(t, os.WriteFile(statePath, []byte{0x00, 0x01, 0x02}, 0644))

	// Load should fall back to the backup
	sm2 := NewManager(slog.Default())
	err = sm2.Load(statePath)
	assert.NoError(t, err, "should load from backup when primary is corrupt")

	loaded, ok := sm2.GetActiveTunnel("abc", "web")
	assert.True(t, ok, "entry should be restored from backup")
	assert.Equal(t, "web.example.com", loaded.Config.Hostname)
}

func TestLoad_AllBackupsCorrupt_DegradestoEmpty(t *testing.T) {
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "state.bin")

	// Create a corrupted primary file
	require.NoError(t, os.WriteFile(statePath, []byte{0x00, 0x01, 0x02}, 0644))

	// Create a corrupted backup file
	require.NoError(t, os.WriteFile(statePath+".bak.20260613-120000", []byte{0xFF, 0xFE}, 0644))

	sm := NewManager(slog.Default())
	err := sm.Load(statePath)
	assert.NoError(t, err, "should degrade to empty state without error")

	stats := sm.GetStats()
	assert.Equal(t, 0, stats["active_tunnels"], "should start with empty state")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/state/ -run "TestLoad_FallsBackToBackup|TestLoad_AllBackupsCorrupt" -v`
Expected: `TestLoad_FallsBackToBackup` FAIL — Load doesn't try backup files yet.

- [ ] **Step 3: Write minimal implementation**

Replace the `Load()` method in `internal/state/persistence.go` (lines 86-118) with:

```go
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
```

Add the `tryLoadBackups` helper method:

```go
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
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/state/ -run "TestLoad_FallsBackToBackup|TestLoad_AllBackupsCorrupt" -v`
Expected: PASS

- [ ] **Step 5: Run full test suite**

Run: `go test ./internal/state/ -v`
Expected: All tests pass, including existing `TestLoadCorruptedFile` and `TestSaveAndLoadGob`.

- [ ] **Step 6: Commit**

```bash
git add internal/state/persistence.go internal/state/persistence_test.go
git commit -m "feat(state): add backup fallback chain in Load for corruption recovery"
```

---

### Task 6: Add persistence config to Config struct

**Files:**
- Modify: `internal/config/config.go:15-80` (Config struct, New, SanitizeForLog)

- [ ] **Step 1: Write the failing test**

Add to `internal/config/config_test.go` (create if not exists, or add to existing):

```go
func TestPersistenceDefaults(t *testing.T) {
	// Set minimal required env vars
	t.Setenv("DOCKTUNNEL_CLOUDFLARE_API_TOKEN", "test-token-at-least-20-chars")
	t.Setenv("DOCKTUNNEL_CLOUDFLARE_ACCOUNT_ID", "test-account-id")

	cfg, err := New()
	require.NoError(t, err)

	assert.Equal(t, 3, cfg.Persistence.BackupCount, "default backupCount should be 3")
	assert.True(t, cfg.Persistence.ValidateOnLoad, "default validateOnLoad should be true")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/ -run "TestPersistenceDefaults" -v`
Expected: FAIL — `Persistence` field undefined on Config.

- [ ] **Step 3: Write minimal implementation**

Add to the `Config` struct in `internal/config/config.go`, after the `Compensation` field (after line 79):

```go
		Persistence struct {
			BackupCount    int  `mapstructure:"backupCount"`
			ValidateOnLoad bool `mapstructure:"validateOnLoad"`
		} `mapstructure:"persistence"`
```

Add viper defaults in `New()`, after the compensation defaults (after line 219):

```go
		// Persistence defaults
		v.SetDefault("persistence.backupCount", 3)
		v.SetDefault("persistence.validateOnLoad", true)
```

Add a getter method after `GetCompensationConfig`:

```go
// GetPersistenceConfig returns persistence configuration with defaults applied.
func (c *Config) GetPersistenceConfig() (backupCount int, validateOnLoad bool) {
	backupCount = c.Persistence.BackupCount
	if backupCount == 0 {
		backupCount = 3
	}
	// ValidateOnLoad defaults to true — only false if explicitly set
	// Since viper sets default to true, zero-value means unmarshalled as false explicitly
	return backupCount, c.Persistence.ValidateOnLoad
}
```

Add to `SanitizeForLog`:

```go
			"persistence": map[string]interface{}{
				"backupCount":    c.Persistence.BackupCount,
				"validateOnLoad": c.Persistence.ValidateOnLoad,
			},
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/config/ -run "TestPersistenceDefaults" -v`
Expected: PASS

- [ ] **Step 5: Run full config test suite**

Run: `go test ./internal/config/ -v`
Expected: All tests pass.

- [ ] **Step 6: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): add persistence config with backupCount and validateOnLoad"
```

---

### Task 7: Wire persistence config through Controller and main.go

**Files:**
- Modify: `internal/controller/controller.go` (add passthrough methods)
- Modify: `cmd/docktunnel/main.go` (add config wiring)

- [ ] **Step 1: Write the failing test**

No separate test needed — the wiring is tested by the integration in main.go. Instead, verify compilation.

- [ ] **Step 2: Add passthrough methods to Controller**

In `internal/controller/controller.go`, after `SetCompensationConfig` (around line 437), add:

```go
// SetPersistenceConfig configures backup and validation settings.
func (c *Controller) SetPersistenceConfig(backupCount int, validateOnLoad bool) {
	c.stateManager.SetBackupCount(backupCount)
	c.stateManager.SetValidateOnLoad(validateOnLoad)
}
```

- [ ] **Step 3: Wire in main.go**

In `cmd/docktunnel/main.go`, after the compensation config block (after line 95), add:

```go
	// Configure persistence settings
	backupCount, validateOnLoad := cfg.GetPersistenceConfig()
	controller.SetPersistenceConfig(backupCount, validateOnLoad)
```

- [ ] **Step 4: Verify compilation and tests**

Run: `go build ./cmd/docktunnel/ && go test ./internal/... -v`
Expected: Build succeeds, all tests pass.

- [ ] **Step 5: Commit**

```bash
git add internal/controller/controller.go cmd/docktunnel/main.go
git commit -m "feat: wire persistence config through Controller and main.go"
```

---

### Task 8: End-to-end verification and gofmt

**Files:**
- All modified files

- [ ] **Step 1: Run full test suite**

Run: `go test ./... -v`
Expected: All tests pass.

- [ ] **Step 2: Run go vet**

Run: `go vet ./...`
Expected: No issues.

- [ ] **Step 3: Run gofmt**

Run: `gofmt -s -w internal/state/persistence.go internal/state/manager.go internal/state/persistence_test.go internal/config/config.go internal/config/config_test.go internal/controller/controller.go cmd/docktunnel/main.go`

- [ ] **Step 4: Run gofmt check**

Run: `gofmt -s -l internal/ cmd/`
Expected: No output (all files formatted).

- [ ] **Step 5: Commit formatting fixes if any**

```bash
git add -A
git diff --cached --quiet || git commit -m "style: gofmt all files"
```

- [ ] **Step 6: Verify final git log**

Run: `git log --oneline -10`
Expected: 7-8 commits (plan + 6 implementation + optional formatting).
