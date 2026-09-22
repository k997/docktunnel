# Phase 3: State Model Upgrade — Per-Service Keys

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-step.

**Goal:** Upgrade state primary key from `containerID` to `containerID + serviceName`, so each service in a multi-service container has independent lifecycle management.

**Architecture:** Change `activeTunnels` map key from `containerID` to `containerID:serviceName` compound key (matching existing `pendingDeletes` format). Controller creates one `TunnelEntry` per parsed service, each with its own retention policy. State file version bumps from 1 to 2 with migration on load.

**Tech Stack:** Go 1.24, existing state manager, existing controller.

**Design spec:** `docs/superpowers/specs/2026-06-12-improvement-roadmap-design.md` Section "Phase 3"

---

## Current State

- `activeTunnels` key: `containerID` — one entry per container, last service wins
- `pendingDeletes` key: `containerID:serviceName` — already compound
- `handleContainerStart` creates a single `TunnelEntry` with only the first hostname
- `handleContainerStop` transitions once per container with a single retention policy
- Retention policy is per-container (from first `docktunnel.*.retention` label found)
- `Sync()` does not populate `stateManager.activeTunnels` — stop fallback creates entries from `containerRules`
- `IngressSpec.Retention` is parsed per-service in the label package but discarded in the controller

## Key Model After Phase 3

- `activeTunnels` key: `containerID:serviceName` — matches `pendingDeletes` format
- `pendingDeletes` key: `containerID:serviceName` — unchanged
- `containerRules` (`containerID → []hostname`) — retained for aggregate container-level queries
- `TunnelEntry.ServiceName` — populated from `Parse()` map key
- Per-service retention from `docktunnel.<service>.retention` label, fallback to `docktunnel.retention`, then `Immediate`

---

### Task 1: State Manager — Compound keys for activeTunnels

**Files:**
- Modify: `internal/state/manager.go`
- Modify: `internal/state/manager_test.go`

- [ ] **Step 1: Add `activeTunnelKey` helper and change `AddActiveTunnel`**

In `internal/state/manager.go`, add the `activeTunnelKey` function right after the existing `pendingDeleteKey`:

```go
// activeTunnelKey generates a unique map key for active tunnels using
// containerID and serviceName, matching pendingDeletes key format.
func activeTunnelKey(containerID, serviceName string) string {
	return containerID + ":" + serviceName
}
```

Change `AddActiveTunnel` to use compound key:

```go
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
```

- [ ] **Step 2: Update `GetActiveTunnel` signature to accept serviceName**

```go
// GetActiveTunnel retrieves an active tunnel by container ID and service name
func (sm *Manager) GetActiveTunnel(containerID, serviceName string) (*types.TunnelEntry, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	key := activeTunnelKey(containerID, serviceName)
	entry, ok := sm.activeTunnels[key]
	return entry, ok
}
```

- [ ] **Step 3: Add `GetActiveTunnelsByContainer` method**

```go
// GetActiveTunnelsByContainer returns all active tunnel entries for a container.
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
```

Note: `containerIDFromKey` already exists and extracts the prefix before `:`.

- [ ] **Step 4: Update `RemoveActiveTunnel` to remove all services for a container**

```go
// RemoveActiveTunnel removes all active tunnel entries for a container.
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
```

- [ ] **Step 5: Update `GetAllActiveTunnels` — no logic change, keys are now compound**

```go
// GetAllActiveTunnels returns a copy of all active tunnels
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
```

No logic change needed — just works with new keys. Update comment.

- [ ] **Step 6: Update `RestoreActiveTunnel` to use compound keys**

```go
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
```

Key change: uses `activeTunnelKey` with entry's `ServiceName` instead of bare `containerID`.

- [ ] **Step 7: Write tests for compound-key activeTunnels**

In `internal/state/manager_test.go`, add these tests:

```go
func TestAddActiveTunnel_CompoundKey(t *testing.T) {
	sm := NewManager(slog.Default())

	// Add two services for the same container
	webEntry := &types.TunnelEntry{
		ContainerID: "c1",
		ServiceName: "web",
		Status:      types.StatusActive,
		Config:      types.TunnelConfiguration{Hostname: "web.example.com"},
	}
	apiEntry := &types.TunnelEntry{
		ContainerID: "c1",
		ServiceName: "api",
		Status:      types.StatusActive,
		Config:      types.TunnelConfiguration{Hostname: "api.example.com"},
	}

	sm.AddActiveTunnel(webEntry)
	sm.AddActiveTunnel(apiEntry)

	// Both should be retrievable individually
	got, ok := sm.GetActiveTunnel("c1", "web")
	if !ok || got.Config.Hostname != "web.example.com" {
		t.Errorf("expected web entry, got ok=%v hostname=%s", ok, got.Config.Hostname)
	}
	got2, ok2 := sm.GetActiveTunnel("c1", "api")
	if !ok2 || got2.Config.Hostname != "api.example.com" {
		t.Errorf("expected api entry, got ok=%v hostname=%s", ok2, got2.Config.Hostname)
	}

	// Container-level query should return both
	entries := sm.GetActiveTunnelsByContainer("c1")
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	hostnames := map[string]bool{}
	for _, e := range entries {
		hostnames[e.Config.Hostname] = true
	}
	if !hostnames["web.example.com"] || !hostnames["api.example.com"] {
		t.Errorf("expected both web.example.com and api.example.com, got %v", hostnames)
	}
}

func TestRemoveActiveTunnel_RemovesAllServices(t *testing.T) {
	sm := NewManager(slog.Default())

	sm.AddActiveTunnel(&types.TunnelEntry{ContainerID: "c1", ServiceName: "web", Status: types.StatusActive})
	sm.AddActiveTunnel(&types.TunnelEntry{ContainerID: "c1", ServiceName: "api", Status: types.StatusActive})

	sm.RemoveActiveTunnel("c1")

	_, ok1 := sm.GetActiveTunnel("c1", "web")
	_, ok2 := sm.GetActiveTunnel("c1", "api")
	if ok1 || ok2 {
		t.Error("both entries should be removed")
	}
	entries := sm.GetActiveTunnelsByContainer("c1")
	if len(entries) != 0 {
		t.Errorf("expected 0 entries after remove, got %d", len(entries))
	}
}

func TestRestoreActiveTunnel_MultiService(t *testing.T) {
	sm := NewManager(slog.Default())
	now := time.Now()

	sm.AddPendingDeletion(&types.TunnelEntry{
		ContainerID: "c1", ServiceName: "web",
		Status: types.StatusRetaining,
		Config: types.TunnelConfiguration{Hostname: "web.example.com"},
		DeletedAt: &now,
	})
	sm.AddPendingDeletion(&types.TunnelEntry{
		ContainerID: "c1", ServiceName: "api",
		Status: types.StatusRetaining,
		Config: types.TunnelConfiguration{Hostname: "api.example.com"},
		DeletedAt: &now,
	})

	err := sm.RestoreActiveTunnel("c1")
	if err != nil {
		t.Fatalf("RestoreActiveTunnel failed: %v", err)
	}

	// Both should be restored
	webEntry, ok1 := sm.GetActiveTunnel("c1", "web")
	apiEntry, ok2 := sm.GetActiveTunnel("c1", "api")
	if !ok1 || !ok2 {
		t.Fatal("both entries should be restored to active")
	}
	if webEntry.Status != types.StatusActive || apiEntry.Status != types.StatusActive {
		t.Error("restored entries should be Active")
	}
	if webEntry.DeletedAt != nil || apiEntry.DeletedAt != nil {
		t.Error("DeletedAt should be nil after restoration")
	}

	// Pending deletes should be empty for this container
	_, pendingExists := sm.GetPendingDeletion("c1")
	if pendingExists {
		t.Error("pending deletes should be empty after restoration")
	}
}
```

Update existing tests that call `GetActiveTunnel(containerID)` to pass two args: `GetActiveTunnel(containerID, serviceName)`. Where `serviceName` wasn't tracked before, use `"web"` as the default (matching existing test entries that set `ServiceName: "web"`).

Specific changes in `manager_test.go`:
- `TestAddActiveTunnel`: change `sm.GetActiveTunnel("test-container-1")` → `sm.GetActiveTunnel("test-container-1", "web")`
- `TestRemoveActiveTunnel`: add `ServiceName: "web"` to entry (already has it? check). No change needed for the call since RemoveActiveTunnel still takes containerID only.
- `TestRestoreActiveTunnel`: change `sm.GetActiveTunnel("test-container-1")` → `sm.GetActiveTunnel("test-container-1", "web")`
- `TestGetSnapshot`: change `snapshot.ActiveTunnels` key from `"active-1"` to `"active-1:web"`, add `ServiceName: "web"` to the entry
- `TestLoadFromSnapshot`: change key in `ActiveTunnels` map from `"active-1"` to `"active-1:web"`, add `ServiceName: "web"`, change `sm.GetActiveTunnel("active-1")` → `sm.GetActiveTunnel("active-1", "web")`
- `TestContainerRestartCancelsRetention`: change `sm.GetActiveTunnel("restart-test")` → `sm.GetActiveTunnel("restart-test", "web")`
- `TestGC_*` tests: these use `GetPendingDeletion` which still takes containerID only — no changes needed
- `TestGetStats`: no changes needed

- [ ] **Step 8: Run tests to verify**

Run: `go test ./internal/state/ -v`
Expected: all tests PASS

- [ ] **Step 9: Commit**

```bash
git add internal/state/manager.go internal/state/manager_test.go
git commit -m "refactor(state): change activeTunnels key to compound containerID:serviceName"
```

---

### Task 2: Transition — Add serviceName parameter

**Files:**
- Modify: `internal/state/transition.go`
- Modify: `internal/state/transition_test.go`

- [ ] **Step 1: Update `Transition` and `transitionLocked` signatures**

```go
func (sm *Manager) Transition(containerID, serviceName string, event types.TransitionEvent, policy *types.RetentionPolicy) ([]types.Action, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	return sm.transitionLocked(containerID, serviceName, event, policy)
}

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
```

- [ ] **Step 2: Update `transitionStoppedLocked`**

```go
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
```

Key changes: uses `activeTunnelKey(containerID, serviceName)` instead of bare `containerID` for lookup; uses `pendingDeleteKey(containerID, serviceName)` explicitly.

- [ ] **Step 3: Update `transitionStartedLocked` — now targets specific service**

```go
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
```

Key change: direct lookup by `pendingDeleteKey(containerID, serviceName)` instead of iterating all pending deletes. Much cleaner with compound keys.

- [ ] **Step 4: Update `transitionRetentionExpiredLocked`**

```go
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
```

- [ ] **Step 5: Update `transitionCleanupCompleteLocked`**

```go
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
```

- [ ] **Step 6: Update all tests in transition_test.go**

Every `Transition` call gains a `serviceName` argument. All existing tests use `"web"` as the service name (matching their TunnelEntry's `ServiceName` field).

Pattern:
```go
// Before:
sm.Transition("c1", types.EventContainerStopped, &policy)
// After:
sm.Transition("c1", "web", types.EventContainerStopped, &policy)
```

Update all calls in `transition_test.go`:
- `TestTransition_Stopped_Immediate`: `sm.Transition("c1", "web", ...)`
- `TestTransition_Stopped_Timed`: `sm.Transition("c1", "web", ...)`
- `TestTransition_Stopped_Forever`: `sm.Transition("c1", "web", ...)`
- `TestTransition_Started_FromRetaining`: `sm.Transition("c1", "web", ...)`
- `TestTransition_RetentionExpired`: `sm.Transition("c1", "web", ...)`
- `TestTransition_CleanupComplete`: `sm.Transition("c1", "web", ...)`
- `TestTransition_Stopped_NoEntry`: `sm.Transition("nonexistent", "web", ...)`
- `TestTransition_MarksDirty`: `sm.Transition("c1", "web", ...)`

Also update `GetActiveTunnel` calls: `sm.GetActiveTunnel("c1")` → `sm.GetActiveTunnel("c1", "web")`.

Add a new test for multi-service independence:

```go
func TestTransition_Stopped_MultiService_Independent(t *testing.T) {
	sm := setupTestManager()
	sm.AddActiveTunnel(&types.TunnelEntry{
		ContainerID: "c1", ServiceName: "web",
		Config: types.TunnelConfiguration{Hostname: "web.example.com"},
		Status: types.StatusActive,
		RetentionPolicy: types.RetentionPolicy{Type: types.Timed, Duration: 1 * time.Hour},
	})
	sm.AddActiveTunnel(&types.TunnelEntry{
		ContainerID: "c1", ServiceName: "api",
		Config: types.TunnelConfiguration{Hostname: "api.example.com"},
		Status: types.StatusActive,
		RetentionPolicy: types.RetentionPolicy{Type: types.Immediate},
	})

	// Stop web service with Timed retention — should retain
	webActions, _ := sm.Transition("c1", "web", types.EventContainerStopped, &types.RetentionPolicy{Type: types.Timed, Duration: 1 * time.Hour})
	if len(webActions) != 0 {
		t.Fatalf("expected 0 actions for Timed web, got %v", webActions)
	}

	// Stop api service with Immediate retention — should delete
	apiActions, _ := sm.Transition("c1", "api", types.EventContainerStopped, &types.RetentionPolicy{Type: types.Immediate})
	if len(apiActions) != 1 || apiActions[0].Kind != types.ActionDeleteRoute {
		t.Fatalf("expected 1 ActionDeleteRoute for Immediate api, got %v", apiActions)
	}
	if apiActions[0].Hostname != "api.example.com" {
		t.Errorf("expected api.example.com, got %s", apiActions[0].Hostname)
	}

	// Web should be in pending deletes as Retaining
	webPending, _ := sm.GetActiveTunnel("c1", "web")
	if webPending != nil {
		t.Error("web should be removed from active")
	}
	// Api should be in pending deletes as PendingDelete
	apiPending, ok := sm.GetPendingDeletion("c1")
	if !ok {
		t.Fatal("expected pending entry for c1")
	}
	// GetPendingDeletion returns first match — verify by checking both entries
	entries := sm.GetPendingDeletionsByContainer("c1")
	retainingCount := 0
	pendingDeleteCount := 0
	for _, e := range entries {
		if e.Status == types.StatusRetaining {
			retainingCount++
		}
		if e.Status == types.StatusPendingDelete {
			pendingDeleteCount++
		}
	}
	if retainingCount != 1 || pendingDeleteCount != 1 {
		t.Errorf("expected 1 Retaining + 1 PendingDelete, got %d Retaining + %d PendingDelete", retainingCount, pendingDeleteCount)
	}
}
```

- [ ] **Step 7: Run tests to verify**

Run: `go test ./internal/state/ -v`
Expected: all tests PASS

- [ ] **Step 8: Commit**

```bash
git add internal/state/transition.go internal/state/transition_test.go
git commit -m "refactor(state): add serviceName parameter to Transition methods"
```

---

### Task 3: Controller — Per-service state management

**Files:**
- Modify: `internal/controller/controller.go`
- Modify: `internal/controller/controller_test.go`

- [ ] **Step 1: Add per-service retention policy helper**

Add to `controller.go`:

```go
// getServiceRetentionPolicy parses the retention policy for a specific service.
// Priority: docktunnel.<service>.retention > docktunnel.retention > Immediate.
func (c *Controller) getServiceRetentionPolicy(labels map[string]string, serviceName string) types.RetentionPolicy {
	// Check per-service retention: docktunnel.<serviceName>.retention
	if v, ok := labels["docktunnel."+serviceName+".retention"]; ok {
		if policy, err := label.ParseRetentionPolicy(v); err == nil {
			return policy
		}
	}

	// Check global retention: docktunnel.retention
	if v, ok := labels["docktunnel.retention"]; ok {
		if policy, err := label.ParseRetentionPolicy(v); err == nil {
			return policy
		}
	}

	// Default: Immediate
	return types.RetentionPolicy{Type: types.Immediate}
}
```

- [ ] **Step 2: Change `registerContainerRules` return type**

Change signature from `([]string, error)` to `(map[string]string, error)` where the map is `serviceName → hostname`:

```go
// registerContainerRules parses labels, validates, and registers ingress rules for a container.
// Returns a map of serviceName → hostname for per-service state management.
func (c *Controller) registerContainerRules(ctx context.Context, event events.Event) (map[string]string, error) {
	parsedRules, err := label.Parse(event.ContainerInfo)
	if err != nil {
		return nil, fmt.Errorf("failed to parse container labels for container %s: %w", event.ContainerID, err)
	}

	c.mu.RLock()
	if err := c.ruleValidator.Validate(parsedRules, c.ingressRules); err != nil {
		c.mu.RUnlock()
		return nil, fmt.Errorf("invalid ingress rules for container %s: %w", event.ContainerID, err)
	}
	c.mu.RUnlock()

	serviceHostnames := make(map[string]string)
	for serviceName, rule := range parsedRules {
		if rule.Hostname.Value != "" {
			c.mu.Lock()
			c.ingressRules[rule.Hostname.Value] = *rule
			c.mu.Unlock()
			serviceHostnames[serviceName] = rule.Hostname.Value
		}
	}

	// Update containerRules for aggregate lookup
	hostnames := make([]string, 0, len(serviceHostnames))
	for _, h := range serviceHostnames {
		hostnames = append(hostnames, h)
	}
	c.mu.Lock()
	c.containerRules[event.ContainerID] = hostnames
	c.mu.Unlock()

	return serviceHostnames, nil
}
```

- [ ] **Step 3: Update `handleContainerStart` for per-service entries**

```go
// handleContainerStart handles container start events
func (c *Controller) handleContainerStart(ctx context.Context, event events.Event) error {
	if !c.isDocktunnelEnabled(event) {
		return nil
	}
	slog.Info("Handling container start event", "containerID", event.ContainerID)

	// Restore any retaining/pending entries for this container's services
	c.stateManager.RestoreActiveTunnel(event.ContainerID)

	// Check flapping
	if c.isFlapping(event.ContainerID) {
		slog.Warn("Container is flapping, ignoring start event", "containerID", event.ContainerID)
		return nil
	}

	serviceHostnames, err := c.registerContainerRules(ctx, event)
	if err != nil {
		slog.Error("Failed to register container rules", "error", err, "containerID", event.ContainerID)
		return err
	}

	// Create one TunnelEntry per service with per-service retention policy
	labels := event.ContainerInfo.Config.Labels
	now := time.Now()
	for serviceName, hostname := range serviceHostnames {
		policy := c.getServiceRetentionPolicy(labels, serviceName)
		tunnelEntry := &types.TunnelEntry{
			ContainerID:     event.ContainerID,
			ServiceName:     serviceName,
			RetentionPolicy: policy,
			Status:          types.StatusActive,
			CreatedAt:       now,
			LastSyncAt:      now,
			Config:          types.TunnelConfiguration{Hostname: hostname},
		}
		c.stateManager.AddActiveTunnel(tunnelEntry)
	}

	c.mu.Lock()
	c.updateContainerHealth(event.ContainerID, true)
	c.mu.Unlock()

	return c.syncToCloudflare(ctx)
}
```

Key changes:
- Uses `RestoreActiveTunnel` (container-level) instead of per-service `Transition` — simpler since all services for a container come back on start
- Creates one entry per service from `serviceHostnames` map
- Per-service retention policy via `getServiceRetentionPolicy`
- Sets `ServiceName` and `Config.Hostname` from parsed data

- [ ] **Step 4: Update `handleContainerStop` for per-service transitions**

```go
// handleContainerStop handles container stop events
func (c *Controller) handleContainerStop(ctx context.Context, event events.Event) error {
	c.mu.RLock()
	_, hasRules := c.containerRules[event.ContainerID]
	c.mu.RUnlock()

	if !hasRules {
		return nil
	}

	slog.Info("Handling container stop event", "containerID", event.ContainerID)

	if c.isFlapping(event.ContainerID) {
		slog.Warn("Container is flapping, ignoring stop event", "containerID", event.ContainerID)
		return nil
	}

	// Get all active entries for this container
	entries := c.stateManager.GetActiveTunnelsByContainer(event.ContainerID)

	// Fallback: if no active entries (state lost), create per-service entries from containerRules
	if len(entries) == 0 {
		if event.ContainerInfo != nil && event.ContainerInfo.Config != nil && event.ContainerInfo.Config.Labels != nil {
			labels := event.ContainerInfo.Config.Labels
			// Parse labels to get service names
			parsedRules, err := label.Parse(event.ContainerInfo)
			if err == nil {
				now := time.Now()
				for serviceName := range parsedRules {
					policy := c.getServiceRetentionPolicy(labels, serviceName)
					c.stateManager.AddActiveTunnel(&types.TunnelEntry{
						ContainerID:     event.ContainerID,
						ServiceName:     serviceName,
						RetentionPolicy: policy,
						Status:          types.StatusActive,
						CreatedAt:       now,
						LastSyncAt:      now,
					})
				}
				entries = c.stateManager.GetActiveTunnelsByContainer(event.ContainerID)
			}
		}
		if len(entries) == 0 {
			// Last resort: no info, use Immediate for all hostnames
			c.mu.RLock()
			hostnames := c.containerRules[event.ContainerID]
			c.mu.RUnlock()
			now := time.Now()
			for i, hostname := range hostnames {
				c.stateManager.AddActiveTunnel(&types.TunnelEntry{
					ContainerID:     event.ContainerID,
					ServiceName:     fmt.Sprintf("svc%d", i),
					RetentionPolicy: types.RetentionPolicy{Type: types.Immediate},
					Status:          types.StatusActive,
					CreatedAt:       now,
					LastSyncAt:      now,
					Config:          types.TunnelConfiguration{Hostname: hostname},
				})
			}
			entries = c.stateManager.GetActiveTunnelsByContainer(event.ContainerID)
		}
	}

	// Transition each service independently
	var allActions []types.Action
	for _, entry := range entries {
		actions, err := c.stateManager.Transition(
			event.ContainerID, entry.ServiceName,
			types.EventContainerStopped, &entry.RetentionPolicy,
		)
		if err != nil {
			slog.Error("State transition failed",
				"container_id", event.ContainerID,
				"service_name", entry.ServiceName,
				"error", err)
			continue
		}
		allActions = append(allActions, actions...)
	}

	// Execute actions: clear containerRules, handle ingress based on actions
	c.mu.Lock()
	delete(c.containerRules, event.ContainerID)
	for _, action := range actions {
		if action.Kind == types.ActionDeleteRoute && action.Hostname != "" {
			delete(c.ingressRules, action.Hostname)
		}
	}
	c.updateContainerHealth(event.ContainerID, false)
	c.mu.Unlock()

	return c.syncToCloudflare(ctx)
}
```

Wait — the inner loop uses `actions` from the last `Transition` call, not `allActions`. Fix the action execution to use `allActions`:

```go
	// Execute actions: clear containerRules, handle ingress based on actions
	c.mu.Lock()
	delete(c.containerRules, event.ContainerID)
	for _, action := range allActions {
		if action.Kind == types.ActionDeleteRoute && action.Hostname != "" {
			delete(c.ingressRules, action.Hostname)
		}
	}
	c.updateContainerHealth(event.ContainerID, false)
	c.mu.Unlock()

	return c.syncToCloudflare(ctx)
```

- [ ] **Step 5: Update `Sync()` to populate per-service active entries**

In `Sync()`, after parsing all rules and before validation, add per-service state entries:

After the block that builds `containerHostnames`, add:

```go
	// Populate per-service state entries for running containers
	for _, event := range eventsList {
		if !c.isDocktunnelEnabled(event) {
			continue
		}
		parsedRules, err := label.Parse(event.ContainerInfo)
		if err != nil {
			continue
		}
		labels := event.ContainerInfo.Config.Labels
		now := time.Now()
		for serviceName, rule := range parsedRules {
			hostname := ""
			if rule.Hostname.Value != "" {
				hostname = rule.Hostname.Value
			}
			policy := c.getServiceRetentionPolicy(labels, serviceName)
			key := event.ContainerID + ":" + serviceName
			if _, exists := c.stateManager.GetActiveTunnel(event.ContainerID, serviceName); !exists {
				c.stateManager.AddActiveTunnel(&types.TunnelEntry{
					ContainerID:     event.ContainerID,
					ServiceName:     serviceName,
					RetentionPolicy: policy,
					Status:          types.StatusActive,
					CreatedAt:       now,
					LastSyncAt:      now,
					Config:          types.TunnelConfiguration{Hostname: hostname},
				})
			}
			_ = key
		}
	}
```

Note: This must be placed BEFORE the validation and ingressRules update steps. The existing parsedRules variable from the first loop can be reused — but `Sync()` currently parses twice. To avoid double-parsing, restructure `Sync()` to collect per-service info in the first pass.

Actually, `Sync()` already has a first pass that does reconciliation and a second pass that parses rules. Let me merge the reconciliation into the main loop. Here's the restructured `Sync()`:

```go
func (c *Controller) Sync(ctx context.Context) error {
	tunnel := c.cloudflareManager.GetTunnel()
	if tunnel == nil {
		return fmt.Errorf("tunnel is not available")
	}

	slog.Info("Starting synchronization", "tunnelID", tunnel.ID)

	eventsList, err := c.dockerManager.ScanRunningContainers(ctx)
	if err != nil {
		return fmt.Errorf("failed to scan running containers: %w", err)
	}

	slog.Info("Found containers with docktunnel labels", "count", len(eventsList))

	// Restore containers that restarted during downtime
	for _, event := range eventsList {
		if !c.isDocktunnelEnabled(event) {
			continue
		}
		if _, exists := c.stateManager.GetPendingDeletion(event.ContainerID); exists {
			slog.Info("Container restarted during downtime, restoring from pending deletion",
				"containerID", event.ContainerID)
			c.stateManager.RestoreActiveTunnel(event.ContainerID)
		}
	}

	// Collect all ingress rules and per-service state
	allParsedRules := make(map[string]*zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress)
	containerHostnames := make(map[string][]string)
	now := time.Now()

	for _, event := range eventsList {
		if !c.isDocktunnelEnabled(event) {
			continue
		}

		parsedRules, err := label.Parse(event.ContainerInfo)
		if err != nil {
			slog.Error("Failed to parse container labels during sync",
				"containerID", event.ContainerID, "error", err)
			continue
		}

		labels := event.ContainerInfo.Config.Labels
		var hostnames []string

		for serviceName, rule := range parsedRules {
			if _, exists := allParsedRules[serviceName]; exists {
				slog.Warn("Duplicate service name found during sync, skipping",
					"serviceName", serviceName, "containerID", event.ContainerID)
				continue
			}
			allParsedRules[serviceName] = rule

			if rule.Hostname.Value != "" {
				hostnames = append(hostnames, rule.Hostname.Value)
			}

			// Populate per-service state entries if not already present
			if _, exists := c.stateManager.GetActiveTunnel(event.ContainerID, serviceName); !exists {
				policy := c.getServiceRetentionPolicy(labels, serviceName)
				hostname := ""
				if rule.Hostname.Value != "" {
					hostname = rule.Hostname.Value
				}
				c.stateManager.AddActiveTunnel(&types.TunnelEntry{
					ContainerID:     event.ContainerID,
					ServiceName:     serviceName,
					RetentionPolicy: policy,
					Status:          types.StatusActive,
					CreatedAt:       now,
					LastSyncAt:      now,
					Config:          types.TunnelConfiguration{Hostname: hostname},
				})
			}
		}

		if len(hostnames) > 0 {
			containerHostnames[event.ContainerID] = hostnames
		}
	}

	if err := c.ruleValidator.Validate(allParsedRules, nil); err != nil {
		slog.Error("Invalid ingress rules during sync", "error", err)
		return fmt.Errorf("invalid ingress rules during sync: %w", err)
	}

	c.mu.Lock()
	c.ingressRules = make(map[string]zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress)
	for _, rule := range allParsedRules {
		if rule.Hostname.Value != "" {
			c.ingressRules[rule.Hostname.Value] = *rule
		}
	}
	c.containerRules = containerHostnames
	c.mu.Unlock()

	return c.syncToCloudflare(ctx)
}
```

- [ ] **Step 6: Remove old `getContainerRetentionPolicy` method**

The `getContainerRetentionPolicy` method is replaced by `getServiceRetentionPolicy`. Remove the old method.

- [ ] **Step 7: Update `handleHealthHealthy` caller of `registerContainerRules`**

`handleHealthHealthy` calls `registerContainerRules` but only cares about success/failure, not the hostnames. Update to ignore the new return type:

```go
func (c *Controller) handleHealthHealthy(ctx context.Context, event events.Event) error {
	if !c.isDocktunnelEnabled(event) {
		return nil
	}
	slog.Info("Container became healthy, exposing services", "containerID", event.ContainerID)

	if c.isFlapping(event.ContainerID) {
		slog.Warn("Container is flapping, ignoring health healthy event", "containerID", event.ContainerID)
		return nil
	}

	if _, err := c.registerContainerRules(ctx, event); err != nil {
		slog.Error("Failed to register container rules on health event",
			"error", err, "containerID", event.ContainerID)
		return err
	}

	return c.syncToCloudflare(ctx)
}
```

No change needed — already ignores return value (uses `_`).

- [ ] **Step 8: Update controller tests**

Update all tests in `controller_test.go` that call `GetActiveTunnel`:

- `TestRetentionPolicyPersistedOnStart`: change `ctrl.stateManager.GetActiveTunnel("retain-container")` → `ctrl.stateManager.GetActiveTunnel("retain-container", "web")`
- `TestStopUsesStoredRetentionPolicyWithoutContainerInfo`: change `ctrl.stateManager.GetActiveTunnel("stop-container")` → needs serviceName. Current code creates entry without ServiceName — add `ServiceName: "web"` to the entry and update the call: `ctrl.stateManager.GetActiveTunnel("stop-container", "web")`
- `TestStartupReconciliation`: change `ctrl.stateManager.GetActiveTunnel(containerID)` → `ctrl.stateManager.GetActiveTunnel(containerID, "web")`
- `TestStop_Immediate_DeletesRouteViaTransition`: add `ServiceName: "web"` to the AddActiveTunnel call, update `GetActiveTunnel` calls
- `TestStop_Timed_KeepsRouteViaTransition`: same
- `TestStart_RestoresFromRetainingViaTransition`: change `ctrl.stateManager.GetActiveTunnel("c1")` → `ctrl.stateManager.GetActiveTunnel("c1", "web")`

- [ ] **Step 9: Run tests to verify**

Run: `go test ./internal/controller/ -v`
Expected: all tests PASS

- [ ] **Step 10: Commit**

```bash
git add internal/controller/controller.go internal/controller/controller_test.go
git commit -m "feat(controller): per-service state management with independent lifecycle"
```

---

### Task 4: Persistence — State file version 2 and migration

**Files:**
- Modify: `internal/state/manager.go` (LoadFromSnapshot, GetSnapshot)
- Modify: `internal/state/persistence.go` (version constant)
- Modify: `internal/state/manager_test.go` (migration tests)

- [ ] **Step 1: Update GetSnapshot to use version 2 and compound keys**

In `GetSnapshot`, the version should be 2:

```go
func (sm *Manager) GetSnapshot() *types.StateSnapshot {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

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
```

- [ ] **Step 2: Update LoadFromSnapshot with v1 → v2 migration**

When loading a v1 snapshot, `activeTunnels` keys are bare `containerID`. We need to migrate them to `containerID:serviceName` format using the entry's `ServiceName` field. If `ServiceName` is empty (old data), generate one from the hostname or use `"default"`.

```go
func (sm *Manager) LoadFromSnapshot(snapshot *types.StateSnapshot) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Migration: v1 activeTunnels keys (bare containerID) → v2 compound keys
	if snapshot.Version < 2 {
		migrated := make(map[string]*types.TunnelEntry, len(snapshot.ActiveTunnels))
		for key, entry := range snapshot.ActiveTunnels {
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

	sm.flappingContainers = snapshot.FlappingContainers

	sm.logger.Info("Loaded state from snapshot",
		"version", snapshot.Version,
		"timestamp", snapshot.Timestamp,
		"active_tunnels", len(sm.activeTunnels),
		"pending_deletions", len(sm.pendingDeletes),
		"flapping_containers", len(sm.flappingContainers),
	)
}
```

- [ ] **Step 3: Add migration failure handling — degrade to empty state**

Wrap the LoadFromSnapshot with a try-recover pattern in the `Load` method. Actually, looking at the existing `Load` in `persistence.go`, let me check it.

The `Load` method in persistence.go calls `LoadFromSnapshot`. If migration fails, we should catch the panic and degrade to empty state. Add a recovery wrapper:

In `manager.go`, update `LoadFromSnapshot` to be safer. If `snapshot` is nil, just initialize empty maps:

```go
func (sm *Manager) LoadFromSnapshot(snapshot *types.StateSnapshot) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Degrade to empty state on nil snapshot
	if snapshot == nil {
		sm.activeTunnels = make(map[string]*types.TunnelEntry)
		sm.pendingDeletes = make(map[string]*types.TunnelEntry)
		sm.flappingContainers = make(map[string]types.FlappingState)
		sm.logger.Warn("Nil snapshot received, starting with empty state")
		return
	}

	// ... rest of migration logic
}
```

For migration errors, the existing pattern in `main.go` already handles `LoadState` failure by continuing with clean state. No additional changes needed there.

- [ ] **Step 4: Update existing tests and add migration test**

Update `TestGetSnapshot` to expect version 2 and compound keys in `ActiveTunnels`:

```go
func TestGetSnapshot(t *testing.T) {
	sm := NewManager(slog.Default())

	sm.AddActiveTunnel(&types.TunnelEntry{
		ContainerID: "active-1",
		ServiceName: "web",
		Status:      types.StatusActive,
	})

	now := time.Now()
	pendingEntry := &types.TunnelEntry{
		ContainerID: "pending-1",
		ServiceName: "web",
		Status:      types.StatusPendingDelete,
		DeletedAt:   &now,
	}
	sm.AddPendingDeletion(pendingEntry)

	snapshot := sm.GetSnapshot()

	assert.NotNil(t, snapshot)
	assert.Equal(t, 2, snapshot.Version)
	assert.Len(t, snapshot.ActiveTunnels, 1)
	assert.Len(t, snapshot.PendingDeletions, 1)
	assert.Contains(t, snapshot.ActiveTunnels, "active-1:web")
	assert.Contains(t, snapshot.PendingDeletions, "pending-1:web")
}
```

Update `TestLoadFromSnapshot`:

```go
func TestLoadFromSnapshot(t *testing.T) {
	sm := NewManager(slog.Default())

	now := time.Now()
	snapshot := &types.StateSnapshot{
		Version: 2,
		ActiveTunnels: map[string]*types.TunnelEntry{
			"active-1:web": {
				ContainerID: "active-1",
				ServiceName: "web",
				Status:      types.StatusActive,
			},
		},
		PendingDeletions: map[string]*types.TunnelEntry{
			"pending-1:web": {
				ContainerID: "pending-1",
				ServiceName: "web",
				Status:      types.StatusPendingDelete,
				DeletedAt:   &now,
			},
		},
		FlappingContainers: map[string]types.FlappingState{
			"flapping-1": {
				CoolingUntil: now.Add(1 * time.Hour),
			},
		},
	}

	sm.LoadFromSnapshot(snapshot)

	entry, ok := sm.GetActiveTunnel("active-1", "web")
	assert.True(t, ok)
	assert.Equal(t, "active-1", entry.ContainerID)

	pending, ok := sm.GetPendingDeletion("pending-1")
	assert.True(t, ok)
	assert.Equal(t, "pending-1", pending.ContainerID)
}
```

Add v1 → v2 migration test:

```go
func TestLoadFromSnapshot_MigratesV1ToV2Keys(t *testing.T) {
	sm := NewManager(slog.Default())

	now := time.Now()
	snapshot := &types.StateSnapshot{
		Version: 1,
		ActiveTunnels: map[string]*types.TunnelEntry{
			// v1 key: bare containerID
			"container-1": {
				ContainerID: "container-1",
				ServiceName: "web",
				Status:      types.StatusActive,
				Config:      types.TunnelConfiguration{Hostname: "web.example.com"},
			},
		},
		PendingDeletions: map[string]*types.TunnelEntry{},
	}

	sm.LoadFromSnapshot(snapshot)

	// Should be accessible with compound key
	entry, ok := sm.GetActiveTunnel("container-1", "web")
	if !ok {
		t.Fatal("expected entry with compound key after v1 migration")
	}
	if entry.Config.Hostname != "web.example.com" {
		t.Errorf("expected web.example.com, got %s", entry.Config.Hostname)
	}
}

func TestLoadFromSnapshot_MigratesV1EmptyServiceName(t *testing.T) {
	sm := NewManager(slog.Default())

	snapshot := &types.StateSnapshot{
		Version: 1,
		ActiveTunnels: map[string]*types.TunnelEntry{
			"container-1": {
				ContainerID: "container-1",
				ServiceName: "", // Empty — should be assigned "default"
				Status:      types.StatusActive,
			},
		},
		PendingDeletions: map[string]*types.TunnelEntry{},
	}

	sm.LoadFromSnapshot(snapshot)

	entry, ok := sm.GetActiveTunnel("container-1", "default")
	if !ok {
		t.Fatal("expected entry with 'default' service name after v1 migration")
	}
	if entry.ServiceName != "default" {
		t.Errorf("expected ServiceName='default', got %s", entry.ServiceName)
	}
}
```

- [ ] **Step 5: Run tests to verify**

Run: `go test ./internal/state/ -v`
Expected: all tests PASS

- [ ] **Step 6: Commit**

```bash
git add internal/state/manager.go internal/state/manager_test.go internal/state/persistence.go
git commit -m "feat(state): version 2 state file with v1 migration for per-service keys"
```

---

### Task 5: Verification

- [ ] **Step 1: Run full test suite**

Run: `go test ./... -v`
Expected: all tests PASS

- [ ] **Step 2: Run go vet**

Run: `go vet ./...`
Expected: no issues

- [ ] **Step 3: Run gofmt**

Run: `gofmt -s -w .`
Then: `git diff --stat`
Expected: no formatting changes (or only whitespace)

- [ ] **Step 4: Final commit if formatting changed**

```bash
git add -A
git commit -m "style: gofmt all files"
```

---

## Acceptance Criteria

- [ ] `activeTunnels` uses `containerID:serviceName` compound keys
- [ ] Each service in a multi-service container has independent state, retention policy, and lifecycle
- [ ] Stopping a container transitions each service independently (Immediate → delete, Timed → retain)
- [ ] Starting a container creates per-service entries with per-service retention
- [ ] State file version bumped to 2 with automatic v1 → v2 migration
- [ ] Migration assigns `"default"` serviceName to v1 entries with empty ServiceName
- [ ] `Sync()` populates per-service state entries for running containers
- [ ] All existing tests pass with updated signatures
- [ ] Multi-service independence verified by test
