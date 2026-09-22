# Phase 5: Persistence Closed Loop

**Date:** 2026-06-13
**Status:** Approved
**Depends on:** Phase 4 (compensation queue, state version 3)

## Problem

The state persistence system has no backup or recovery mechanism. If the gob state file is corrupted (disk error, partial write, process kill during rename), the system degrades to an empty state and all retention/flapping/compensation data is lost. There is also no validation of loaded data — a truncated or malformed file could load partially and cause subtle runtime bugs.

## Approach

Add backup rotation, enhanced validation, and a fallback chain to the existing persistence layer. No new state file version is needed — this is purely a save/load enhancement.

Scope is limited to the gob persistence path. The JSON fallback remains as-is (manual debugging tool).

---

## 1. Backup Rotation

**File:** `internal/state/persistence.go`

Before every `Save()`, rotate existing state files:

1. Rename current state file → `.bak.{timestamp}` (e.g., `.bak.20260613-143052`)
2. Scan directory for all `.bak.*` files matching the state file name
3. Sort by timestamp (newest first), delete oldest entries beyond `backupCount`
4. Write new state file via existing tmp + rename

**New method:**

```go
func (sm *Manager) rotateBackups(statePath string) error
```

Called at the start of `Save()`, after acquiring the snapshot but before writing the tmp file. Rotation failures are logged as warnings but do not prevent the save from proceeding — a failed rename of an old backup should not block state persistence.

**Backup filename format:** `state.bin.bak.{YYYYMMDD-HHmmSS}` — uses the current time at rotation moment. Example: `/var/lib/docktunnel/state.bin.bak.20260613-143052`.

**Cleanup logic:** After creating a new backup, glob for `{statePath}.bak.*` files, sort by timestamp suffix (lexicographic sort works since format is fixed-width), and delete the oldest files exceeding `backupCount`. This naturally prunes old backups on each save cycle.

**Config:**

```go
// On Manager struct
backupCount int
```

Default: 3. Zero disables backup rotation entirely.

**Initialization:** `NewManager` sets `backupCount: 3`. `SetBackupCount(n int)` method for config override.

---

## 2. Enhanced Startup Validation

**File:** `internal/state/persistence.go`

**New method:**

```go
func validateSnapshot(snapshot *types.StateSnapshot) error
```

Returns a multi-error describing all validation failures, or nil if valid.

**Checks:**

1. **Timestamp**: `snapshot.Timestamp` must not be zero, and must not be more than 5 minutes in the future (tolerance for clock skew between save and load machines).
2. **Key consistency**: For each entry in `ActiveTunnels` and `PendingDeletions`, the compound key must equal `entry.ContainerID + ":" + entry.ServiceName`. Mismatched keys indicate corrupted data.
3. **Compensation records**: Each `CompensationRecord` must have non-empty `ID` and non-zero `CreatedAt`.

**Integration:** Called in `loadGob()` and `loadJSON()` after decoding but before `LoadFromSnapshot()`. On validation failure:
- Log each specific validation error at warn level.
- Return the validation error (triggers fallback chain in `Load()`).

**Config:** `validateOnLoad` bool on Manager. Default: true. When false, skip validation (useful for manual state file editing during debugging).

---

## 3. Corruption Recovery

**File:** `internal/state/persistence.go`

**Fallback chain in `Load()`:**

```
primary gob → .bak.* (newest first) → JSON fallback → empty state
```

Modified `Load()` logic:

1. Try `loadGob(statePath)` — primary file.
2. On failure, glob for `{statePath}.bak.*` files, sort by timestamp suffix (newest first), and try `loadGob()` on each. Stop on first success.
3. If all backups fail, try JSON fallback (existing behavior).
4. If JSON also fails, degrade to empty state.

Each fallback attempt logged at info level (trying backup) or warn level (backup also failed). On successful backup restore, log at info level which backup was used.

The backup files are gob-format only — no separate JSON backups. The JSON fallback remains the existing manual debugging mechanism.

---

## 4. Configuration

**File:** `internal/config/config.go`

```go
type PersistenceConfig struct {
    BackupCount    int  `mapstructure:"backupCount"`
    ValidateOnLoad bool `mapstructure:"validateOnLoad"`
}
```

Embedded in the existing Config struct.

**YAML example:**

```yaml
persistence:
  backupCount: 3
  validateOnLoad: true
```

**Defaults:**

| Field | Default | Rationale |
|-------|---------|-----------|
| BackupCount | 3 | Last 3 good states for recovery |
| ValidateOnLoad | true | Catch corruption early |

**Integration:** `main.go` calls `stateManager.SetBackupCount()` and `stateManager.SetValidateOnLoad()` after creating the manager.

---

## 5. State Version

No version bump. Phase 5 changes only affect the save/load path and add no new fields to `StateSnapshot`. All v3 snapshots are compatible.

---

## Out of Scope

- Immediate save on critical events (periodic `SaveIfDirty()` is sufficient)
- Per-field checksums or HMAC verification
- Backup compaction or compression
- Metrics on backup/restore operations (Phase 6 territory)
- Backup file aging / max-age cleanup
