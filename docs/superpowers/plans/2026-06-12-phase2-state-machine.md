# Phase 2: Unified State Machine & Retention Semantics — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Replace ad-hoc retention logic with a unified state machine (Active → Retaining → PendingDelete → Deleted) that guarantees consistent behavior across Ingress, DNS, and State layers.

**Architecture:** Add a `Transition()` method on `state.Manager` that is pure state computation (no Cloudflare API calls). Controller calls Transition(), gets back actions, and executes them. The four entry statuses replace the current mix of "in pendingDeletes but with different retention policies."

**Tech Stack:** Go 1.24, existing state manager, existing controller.

**Design spec:** `docs/superpowers/specs/2026-06-12-improvement-roadmap-design.md` Section "Phase 2"

---

## Current State

- `EntryStatus` has: `StatusActive`, `StatusPendingDelete`, `StatusDeleted`, `StatusFlapping`
- `handleContainerStop` uses inline retention logic: Immediate → delete now, Timed/Forever → move to `pendingDeletes` with `StatusPendingDelete`
- `RunGC` iterates `pendingDeletes` and checks retention policy type to decide deletion
- `handleContainerStart` calls `RestoreActiveTunnel()` to cancel retention
- Problem: `StatusPendingDelete` is overloaded — used for both "retaining" and "pending cleanup"

## Key Model

- `activeTunnels` (key: containerID) → Active entries
- `pendingDeletes` (key: containerID:serviceName) → entries with status Retaining OR PendingDelete
- RunGC distinguishes by `entry.Status` instead of `entry.RetentionPolicy.Type`

---

### Task 1: Add StatusRetaining, TransitionEvent, and Action types

**Files:**
- Modify: `pkg/types/tunnel.go`
- Test: `pkg/types/tunnel_test.go` (new)

- [ ] **Step 1: Add StatusRetaining to EntryStatus enum**

In `pkg/types/tunnel.go`, add `StatusRetaining` between `StatusPendingDelete` and `StatusDeleted`:

```go
const (
	StatusActive        EntryStatus = iota // Container running, route active
	StatusPendingDelete                    // Immediate or expired, awaiting cleanup
	StatusRetaining                        // Container stopped, retention in progress
	StatusDeleted                          // Removed from Cloudflare
	StatusFlapping                         // In cooling period
)
```

- [ ] **Step 2: Add TransitionEvent enum**

```go
// TransitionEvent represents lifecycle events that trigger state transitions
type TransitionEvent int

const (
	EventContainerStarted TransitionEvent = iota
	EventContainerStopped
	EventRetentionExpired
	EventCleanupComplete
)
```

- [ ] **Step 3: Add Action type**

```go
// ActionKind represents the type of action the controller should perform after a transition
type ActionKind int

const (
	ActionNone       ActionKind = iota
	ActionDeleteRoute            // Delete ingress rule and DNS for hostname
)

// Action represents an action the controller should perform after a state transition
type Action struct {
	Kind        ActionKind
	ContainerID string
	ServiceName string
	Hostname    string
}
```

- [ ] **Step 4: Write tests for new types**

Create `pkg/types/tunnel_test.go`:

```go
package types

import "testing"

func TestTransitionEventValues(t *testing.T) {
	events := []TransitionEvent{EventContainerStarted, EventContainerStopped, EventRetentionExpired, EventCleanupComplete}
	for i, e := range events {
		if int(e) != i {
			t.Errorf("TransitionEvent %d has unexpected value %d", i, e)
		}
	}
}

func TestActionKindValues(t *testing.T) {
	kinds := []ActionKind{ActionNone, ActionDeleteRoute}
	for i, k := range kinds {
		if int(k) != i {
			t.Errorf("ActionKind %d has unexpected value %d", i, k)
		}
	}
}

func TestEntryStatusRetaining(t *testing.T) {
	if StatusRetaining == StatusActive || StatusRetaining == StatusPendingDelete || StatusRetaining == StatusDeleted {
		t.Error("StatusRetaining must be distinct from other statuses")
	}
}
```

- [ ] **Step 5: Run tests and commit**

Run: `go test ./pkg/types/ -v`
Expected: PASS

---

### Task 2: Implement Transition() method on state.Manager

**Files:**
- Create: `internal/state/transition.go`
- Test: `internal/state/transition_test.go` (new)

This is the core state machine. `Transition()` acquires the lock, delegates to `transitionLocked()`, and returns actions.

- [ ] **Step 1: Write failing tests for Transition**

Create `internal/state/transition_test.go`:

```go
package state

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"docktunnel/pkg/types"
)

func setupTestManager() *Manager {
	return NewManager(slog.Default())
}

func TestTransition_Stopped_Immediate(t *testing.T) {
	sm := setupTestManager()
	sm.AddActiveTunnel(&types.TunnelEntry{
		ContainerID: "c1",
		ServiceName: "web",
		Config:      types.TunnelConfiguration{Hostname: "app.example.com"},
		Status:      types.StatusActive,
	})

	actions, err := sm.Transition("c1", types.EventContainerStopped, &types.RetentionPolicy{Type: types.Immediate})
	if err != nil {
		t.Fatalf("Transition failed: %v", err)
	}

	// Should return DeleteRoute action
	if len(actions) != 1 || actions[0].Kind != types.ActionDeleteRoute {
		t.Fatalf("expected 1 ActionDeleteRoute, got %v", actions)
	}
	if actions[0].Hostname != "app.example.com" {
		t.Errorf("expected hostname app.example.com, got %s", actions[0].Hostname)
	}

	// Entry should be in pendingDeletes with PendingDelete status
	pending, exists := sm.GetPendingDeletion("c1")
	if !exists {
		t.Fatal("expected entry in pending deletes")
	}
	if pending.Status != types.StatusPendingDelete {
		t.Errorf("expected StatusPendingDelete, got %d", pending.Status)
	}

	// Entry should be removed from active
	_, activeExists := sm.GetActiveTunnel("c1")
	if activeExists {
		t.Error("entry should not be in active tunnels")
	}
}

func TestTransition_Stopped_Timed(t *testing.T) {
	sm := setupTestManager()
	sm.AddActiveTunnel(&types.TunnelEntry{
		ContainerID: "c1",
		ServiceName: "web",
		Config:      types.TunnelConfiguration{Hostname: "app.example.com"},
		Status:      types.StatusActive,
	})

	actions, err := sm.Transition("c1", types.EventContainerStopped, &types.RetentionPolicy{Type: types.Timed, Duration: 1 * time.Hour})
	if err != nil {
		t.Fatalf("Transition failed: %v", err)
	}

	// Should return NO actions (routes stay active)
	if len(actions) != 0 {
		t.Fatalf("expected 0 actions for Timed retention, got %v", actions)
	}

	// Entry should be in pendingDeletes with Retaining status
	pending, exists := sm.GetPendingDeletion("c1")
	if !exists {
		t.Fatal("expected entry in pending deletes")
	}
	if pending.Status != types.StatusRetaining {
		t.Errorf("expected StatusRetaining, got %d", pending.Status)
	}
	if pending.DeletedAt == nil {
		t.Error("expected DeletedAt to be set")
	}
}

func TestTransition_Stopped_Forever(t *testing.T) {
	sm := setupTestManager()
	sm.AddActiveTunnel(&types.TunnelEntry{
		ContainerID: "c1",
		ServiceName: "web",
		Config:      types.TunnelConfiguration{Hostname: "app.example.com"},
		Status:      types.StatusActive,
	})

	actions, err := sm.Transition("c1", types.EventContainerStopped, &types.RetentionPolicy{Type: types.Forever})
	if err != nil {
		t.Fatalf("Transition failed: %v", err)
	}

	if len(actions) != 0 {
		t.Fatalf("expected 0 actions, got %v", actions)
	}

	pending, exists := sm.GetPendingDeletion("c1")
	if !exists {
		t.Fatal("expected entry in pending deletes")
	}
	if pending.Status != types.StatusRetaining {
		t.Errorf("expected StatusRetaining, got %d", pending.Status)
	}
}

func TestTransition_Started_FromRetaining(t *testing.T) {
	sm := setupTestManager()
	now := time.Now()
	sm.AddPendingDeletion(&types.TunnelEntry{
		ContainerID:     "c1",
		ServiceName:     "web",
		Config:          types.TunnelConfiguration{Hostname: "app.example.com"},
		Status:          types.StatusRetaining,
		RetentionPolicy: types.RetentionPolicy{Type: types.Timed, Duration: 1 * time.Hour},
		DeletedAt:       &now,
	})

	actions, err := sm.Transition("c1", types.EventContainerStarted, nil)
	if err != nil {
		t.Fatalf("Transition failed: %v", err)
	}

	if len(actions) != 0 {
		t.Fatalf("expected 0 actions, got %v", actions)
	}

	// Should be restored to active
	entry, exists := sm.GetActiveTunnel("c1")
	if !exists {
		t.Fatal("expected entry in active tunnels")
	}
	if entry.Status != types.StatusActive {
		t.Errorf("expected StatusActive, got %d", entry.Status)
	}
	if entry.DeletedAt != nil {
		t.Error("DeletedAt should be nil after restoration")
	}

	// Should be removed from pending deletes
	_, pendingExists := sm.GetPendingDeletion("c1")
	if pendingExists {
		t.Error("entry should not be in pending deletes")
	}
}

func TestTransition_RetentionExpired(t *testing.T) {
	sm := setupTestManager()
	past := time.Now().Add(-2 * time.Hour)
	sm.AddPendingDeletion(&types.TunnelEntry{
		ContainerID:     "c1",
		ServiceName:     "web",
		Config:          types.TunnelConfiguration{Hostname: "app.example.com"},
		Status:          types.StatusRetaining,
		RetentionPolicy: types.RetentionPolicy{Type: types.Timed, Duration: 1 * time.Hour},
		DeletedAt:       &past,
	})

	actions, err := sm.Transition("c1", types.EventRetentionExpired, nil)
	if err != nil {
		t.Fatalf("Transition failed: %v", err)
	}

	if len(actions) != 1 || actions[0].Kind != types.ActionDeleteRoute {
		t.Fatalf("expected 1 ActionDeleteRoute, got %v", actions)
	}

	pending, exists := sm.GetPendingDeletion("c1")
	if !exists {
		t.Fatal("expected entry still in pending deletes")
	}
	if pending.Status != types.StatusPendingDelete {
		t.Errorf("expected StatusPendingDelete after expiry, got %d", pending.Status)
	}
}

func TestTransition_CleanupComplete(t *testing.T) {
	sm := setupTestManager()
	sm.AddPendingDeletion(&types.TunnelEntry{
		ContainerID: "c1",
		ServiceName: "web",
		Config:      types.TunnelConfiguration{Hostname: "app.example.com"},
		Status:      types.StatusPendingDelete,
	})

	actions, err := sm.Transition("c1", types.EventCleanupComplete, nil)
	if err != nil {
		t.Fatalf("Transition failed: %v", err)
	}

	if len(actions) != 0 {
		t.Fatalf("expected 0 actions, got %v", actions)
	}

	// Entry should be fully removed
	_, pendingExists := sm.GetPendingDeletion("c1")
	if pendingExists {
		t.Error("entry should be removed after cleanup complete")
	}
}

func TestTransition_Stopped_NoEntry(t *testing.T) {
	sm := setupTestManager()

	actions, err := sm.Transition("nonexistent", types.EventContainerStopped, &types.RetentionPolicy{Type: types.Immediate})
	if err != nil {
		t.Fatalf("Transition failed: %v", err)
	}

	if len(actions) != 0 {
		t.Fatalf("expected 0 actions for nonexistent container, got %v", actions)
	}
}

func TestTransition_MarksDirty(t *testing.T) {
	sm := setupTestManager()
	sm.AddActiveTunnel(&types.TunnelEntry{
		ContainerID: "c1",
		ServiceName: "web",
		Config:      types.TunnelConfiguration{Hostname: "app.example.com"},
		Status:      types.StatusActive,
	})

	_, _ = sm.Transition("c1", types.EventContainerStopped, &types.RetentionPolicy{Type: types.Immediate})

	if !sm.IsDirty() {
		t.Error("Transition should mark state as dirty")
	}
}
```

Note: `IsDirty()` method needs to be added to state.Manager — add it as part of this task.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/state/ -run TestTransition -v`
Expected: FAIL (Transition not defined)

- [ ] **Step 3: Implement Transition**

Create `internal/state/transition.go`:

```go
package state

import (
	"docktunnel/pkg/types"
	"time"
)

// Transition is the state machine entry point. It updates entry status, marks dirty,
// and returns actions for the Controller to execute.
// Key rule: Transition does NOT call Cloudflare API — pure state computation.
func (sm *Manager) Transition(containerID string, event types.TransitionEvent, policy *types.RetentionPolicy) ([]types.Action, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	return sm.transitionLocked(containerID, event, policy), nil
}

// transitionLocked implements transition logic with lock already held.
func (sm *Manager) transitionLocked(containerID string, event types.TransitionEvent, policy *types.RetentionPolicy) []types.Action {
	switch event {
	case types.EventContainerStopped:
		return sm.transitionStoppedLocked(containerID, policy)
	case types.EventContainerStarted:
		return sm.transitionStartedLocked(containerID)
	case types.EventRetentionExpired:
		return sm.transitionRetentionExpiredLocked(containerID)
	case types.EventCleanupComplete:
		return sm.transitionCleanupCompleteLocked(containerID)
	}
	return nil
}

func (sm *Manager) transitionStoppedLocked(containerID string, policy *types.RetentionPolicy) []types.Action {
	entry, exists := sm.activeTunnels[containerID]
	if !exists {
		return nil
	}

	now := time.Now()

	if policy != nil && policy.Type != types.Immediate {
		// Timed or Forever: Active → Retaining
		entry.Status = types.StatusRetaining
		entry.DeletedAt = &now
		entry.RetentionPolicy = *policy

		// Move from active to pending deletes
		key := pendingDeleteKey(entry.ContainerID, entry.ServiceName)
		sm.pendingDeletes[key] = entry
		delete(sm.activeTunnels, containerID)
		sm.markDirty()

		sm.logger.Info("Transition: Active → Retaining",
			"container_id", containerID,
			"retention_type", policy.Type,
		)
		return nil
	}

	// Immediate: Active → PendingDelete
	entry.Status = types.StatusPendingDelete
	entry.DeletedAt = &now

	// Move from active to pending deletes
	key := pendingDeleteKey(entry.ContainerID, entry.ServiceName)
	sm.pendingDeletes[key] = entry
	delete(sm.activeTunnels, containerID)
	sm.markDirty()

	sm.logger.Info("Transition: Active → PendingDelete",
		"container_id", containerID,
	)

	return []types.Action{
		{
			Kind:        types.ActionDeleteRoute,
			ContainerID: containerID,
			ServiceName: entry.ServiceName,
			Hostname:    entry.Config.Hostname,
		},
	}
}

func (sm *Manager) transitionStartedLocked(containerID string) []types.Action {
	// Check if in pending deletes (Retaining or PendingDelete)
	for key, entry := range sm.pendingDeletes {
		if containerIDFromKey(key) == containerID {
			entry.Status = types.StatusActive
			entry.DeletedAt = nil
			delete(sm.pendingDeletes, key)
			sm.activeTunnels[containerID] = entry
			sm.markDirty()

			sm.logger.Info("Transition: → Active",
				"container_id", containerID,
			)
			return nil
		}
	}
	return nil
}

func (sm *Manager) transitionRetentionExpiredLocked(containerID string) []types.Action {
	for key, entry := range sm.pendingDeletes {
		if containerIDFromKey(key) == containerID && entry.Status == types.StatusRetaining {
			entry.Status = types.StatusPendingDelete
			sm.markDirty()

			sm.logger.Info("Transition: Retaining → PendingDelete (expired)",
				"container_id", containerID,
			)

			return []types.Action{
				{
					Kind:        types.ActionDeleteRoute,
					ContainerID: containerID,
					ServiceName: entry.ServiceName,
					Hostname:    entry.Config.Hostname,
				},
			}
		}
	}
	return nil
}

func (sm *Manager) transitionCleanupCompleteLocked(containerID string) []types.Action {
	for key, entry := range sm.pendingDeletes {
		if containerIDFromKey(key) == containerID && entry.Status == types.StatusPendingDelete {
			entry.Status = types.StatusDeleted
			delete(sm.pendingDeletes, key)
			sm.markDirty()

			sm.logger.Info("Transition: PendingDelete → Deleted",
				"container_id", containerID,
			)
			return nil
		}
	}
	return nil
}

// IsDirty returns whether the state has changed since last save.
func (sm *Manager) IsDirty() bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.dirty
}

// ExpireRetentionLocked checks Retaining entries for Timed policy expiry.
// Called from RunGC with lock already held.
// Returns entries that have expired and should be cleaned up.
func (sm *Manager) ExpireRetentionLocked(now time.Time) []*types.TunnelEntry {
	var expired []*types.TunnelEntry

	for key, entry := range sm.pendingDeletes {
		if entry.Status != types.StatusRetaining {
			continue
		}

		if entry.RetentionPolicy.Type == types.Timed && entry.DeletedAt != nil {
			elapsed := now.Sub(*entry.DeletedAt)
			if elapsed >= entry.RetentionPolicy.Duration {
				// Retaining → PendingDelete → Deleted (all at once for GC)
				entry.Status = types.StatusDeleted
				delete(sm.pendingDeletes, key)
				sm.markDirty()
				expired = append(expired, entry)

				sm.logger.Info("GC: Retaining → Deleted (retention expired)",
					"container_id", entry.ContainerID,
					"service_name", entry.ServiceName,
					"hostname", entry.Config.Hostname,
				)
			}
		}
	}

	return expired
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/state/ -run TestTransition -v`
Expected: PASS

- [ ] **Step 5: Commit**

---

### Task 3: Refactor handleContainerStop to use Transition()

**Files:**
- Modify: `internal/controller/controller.go` (handleContainerStop method, lines ~335-448)

Replace the inline retention logic with a call to `sm.Transition()`. The controller still manages ingressRules and containerRules directly, but state management is delegated to the state machine.

- [ ] **Step 1: Write failing test for stop via Transition**

Add to `internal/controller/controller_test.go`:

```go
func TestStop_Immediate_DeletesRouteViaTransition(t *testing.T) {
	ctrl := NewController(nil, &mockCloudflareManager{
		tunnel: &zero_trust.TunnelCloudflaredGetResponse{ID: "test-tunnel"},
	}, ControllerOptions{
		DebounceDuration: 2 * time.Second,
	})

	// Set up: container has rules and active tunnel with Immediate policy
	ctrl.ingressRules["app.example.com"] = zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{
		Hostname: cloudflare.F("app.example.com"),
		Service:  cloudflare.F("http://localhost:8080"),
	}
	ctrl.containerRules["c1"] = []string{"app.example.com"}
	ctrl.stateManager.AddActiveTunnel(&types.TunnelEntry{
		ContainerID:     "c1",
		RetentionPolicy: types.RetentionPolicy{Type: types.Immediate},
		Status:          types.StatusActive,
		Config:          types.TunnelConfiguration{Hostname: "app.example.com"},
	})

	stopEvent := events.Event{Type: "stop", ContainerID: "c1", ContainerInfo: nil}
	err := ctrl.Dispatch(context.Background(), stopEvent)
	if err != nil {
		t.Fatalf("stop dispatch failed: %v", err)
	}

	// Ingress should be deleted
	ctrl.mu.RLock()
	_, ingressExists := ctrl.ingressRules["app.example.com"]
	ctrl.mu.RUnlock()
	if ingressExists {
		t.Error("ingress should be deleted for Immediate retention")
	}

	// Container rules should be cleared
	ctrl.mu.RLock()
	_, rulesExist := ctrl.containerRules["c1"]
	ctrl.mu.RUnlock()
	if rulesExist {
		t.Error("containerRules should be cleared")
	}

	// State should be PendingDelete
	pending, _ := ctrl.stateManager.GetPendingDeletion("c1")
	if pending != nil && pending.Status != types.StatusPendingDelete {
		t.Errorf("expected StatusPendingDelete, got %d", pending.Status)
	}
}

func TestStop_Timed_KeepsRouteViaTransition(t *testing.T) {
	ctrl := NewController(nil, &mockCloudflareManager{
		tunnel: &zero_trust.TunnelCloudflaredGetResponse{ID: "test-tunnel"},
	}, ControllerOptions{
		DebounceDuration: 2 * time.Second,
	})

	ctrl.ingressRules["app.example.com"] = zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{
		Hostname: cloudflare.F("app.example.com"),
		Service:  cloudflare.F("http://localhost:8080"),
	}
	ctrl.containerRules["c1"] = []string{"app.example.com"}
	ctrl.stateManager.AddActiveTunnel(&types.TunnelEntry{
		ContainerID:     "c1",
		RetentionPolicy: types.RetentionPolicy{Type: types.Timed, Duration: 1 * time.Hour},
		Status:          types.StatusActive,
		Config:          types.TunnelConfiguration{Hostname: "app.example.com"},
	})

	stopEvent := events.Event{Type: "stop", ContainerID: "c1", ContainerInfo: nil}
	err := ctrl.Dispatch(context.Background(), stopEvent)
	if err != nil {
		t.Fatalf("stop dispatch failed: %v", err)
	}

	// Ingress should be KEPT (Timed retention)
	ctrl.mu.RLock()
	_, ingressExists := ctrl.ingressRules["app.example.com"]
	ctrl.mu.RUnlock()
	if !ingressExists {
		t.Error("ingress should be kept for Timed retention")
	}

	// Container rules should be cleared
	ctrl.mu.RLock()
	_, rulesExist := ctrl.containerRules["c1"]
	ctrl.mu.RUnlock()
	if rulesExist {
		t.Error("containerRules should be cleared for Timed retention")
	}

	// State should be Retaining
	pending, _ := ctrl.stateManager.GetPendingDeletion("c1")
	if pending != nil && pending.Status != types.StatusRetaining {
		t.Errorf("expected StatusRetaining, got %d", pending.Status)
	}
}
```

- [ ] **Step 2: Run tests to see current behavior (may pass or fail)**

Run: `go test ./internal/controller/ -run "TestStop_Immediate|TestStop_Timed" -v`

- [ ] **Step 3: Refactor handleContainerStop**

Replace the inline retention logic (lines ~354-448) with:

```go
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

	// Get retention policy: prefer stateManager, fallback to labels, then Immediate
	var policy types.RetentionPolicy
	if activeEntry, exists := c.stateManager.GetActiveTunnel(event.ContainerID); exists {
		policy = activeEntry.RetentionPolicy
	} else if event.ContainerInfo != nil && event.ContainerInfo.Config != nil && event.ContainerInfo.Config.Labels != nil {
		policy = c.getContainerRetentionPolicy(event)
		// Ensure active entry exists for Transition (fallback path)
		now := time.Now()
		hostnames := c.containerRules[event.ContainerID]
		entry := &types.TunnelEntry{
			ContainerID:     event.ContainerID,
			RetentionPolicy: policy,
			Status:          types.StatusActive,
			CreatedAt:       now,
			LastSyncAt:      now,
		}
		if len(hostnames) > 0 {
			entry.Config.Hostname = hostnames[0]
		}
		c.stateManager.AddActiveTunnel(entry)
	} else {
		policy = types.RetentionPolicy{Type: types.Immediate}
	}

	slog.Info("Container retention policy",
		"containerID", event.ContainerID,
		"policyType", policy.Type,
		"duration", policy.Duration)

	// Transition state machine
	actions, err := c.stateManager.Transition(event.ContainerID, types.EventContainerStopped, &policy)
	if err != nil {
		return fmt.Errorf("state transition failed: %w", err)
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

- [ ] **Step 4: Run all controller tests**

Run: `go test ./internal/controller/ -v`
Expected: ALL PASS

- [ ] **Step 5: Commit**

---

### Task 4: Refactor handleContainerStart to use Transition()

**Files:**
- Modify: `internal/controller/controller.go` (handleContainerStart method, lines ~217-272)

Replace the `RestoreActiveTunnel` call with `Transition(ContainerStarted)`.

- [ ] **Step 1: Write failing test for restart via Transition**

Add to `internal/controller/controller_test.go`:

```go
func TestStart_RestoresFromRetainingViaTransition(t *testing.T) {
	ctrl := NewController(nil, &mockCloudflareManager{
		tunnel: &zero_trust.TunnelCloudflaredGetResponse{ID: "test-tunnel"},
	}, ControllerOptions{
		DebounceDuration: 2 * time.Second,
	})

	// Set up: container in Retaining state (stopped with Timed retention)
	past := time.Now().Add(-30 * time.Minute)
	ctrl.stateManager.AddPendingDeletion(&types.TunnelEntry{
		ContainerID:     "c1",
		ServiceName:     "web",
		Config:          types.TunnelConfiguration{Hostname: "app.example.com"},
		Status:          types.StatusRetaining,
		RetentionPolicy: types.RetentionPolicy{Type: types.Timed, Duration: 1 * time.Hour},
		DeletedAt:       &past,
	})

	containerInfo := &containerTypes.InspectResponse{
		ContainerJSONBase: &containerTypes.ContainerJSONBase{
			HostConfig: &containerTypes.HostConfig{NetworkMode: "bridge"},
		},
		Config: &containerTypes.Config{
			Labels: map[string]string{
				"docktunnel.enable":       "true",
				"docktunnel.web.hostname": "app.example.com",
				"docktunnel.web.service":  "http://localhost:8080",
			},
		},
		NetworkSettings: &containerTypes.NetworkSettings{
			Networks: map[string]*network.EndpointSettings{
				"bridge": {IPAddress: "172.17.0.2"},
			},
		},
	}

	startEvent := events.Event{Type: "start", ContainerID: "c1", ContainerInfo: containerInfo}
	err := ctrl.Dispatch(context.Background(), startEvent)
	if err != nil {
		t.Fatalf("start dispatch failed: %v", err)
	}

	// Should be in active tunnels now
	entry, exists := ctrl.stateManager.GetActiveTunnel("c1")
	if !exists {
		t.Fatal("expected entry in active tunnels after restart")
	}
	if entry.Status != types.StatusActive {
		t.Errorf("expected StatusActive, got %d", entry.Status)
	}
	if entry.DeletedAt != nil {
		t.Error("DeletedAt should be nil after restoration")
	}

	// Should NOT be in pending deletes
	_, pendingExists := ctrl.stateManager.GetPendingDeletion("c1")
	if pendingExists {
		t.Error("entry should not be in pending deletes after restart")
	}
}
```

- [ ] **Step 2: Refactor handleContainerStart**

Replace the pending deletion check block (lines ~225-238) with:

```go
	// Restore from Retaining/PendingDelete via state machine
	c.stateManager.Transition(event.ContainerID, types.EventContainerStarted, nil)
```

The full method becomes:

```go
func (c *Controller) handleContainerStart(ctx context.Context, event events.Event) error {
	if !c.isDocktunnelEnabled(event) {
		return nil
	}
	slog.Info("Handling container start event", "containerID", event.ContainerID)

	// Restore from Retaining/PendingDelete via state machine
	c.stateManager.Transition(event.ContainerID, types.EventContainerStarted, nil)

	if c.isFlapping(event.ContainerID) {
		slog.Warn("Container is flapping, ignoring start event", "containerID", event.ContainerID)
		return nil
	}

	hostnames, err := c.registerContainerRules(ctx, event)
	if err != nil {
		slog.Error("Failed to register container rules", "error", err, "containerID", event.ContainerID)
		return err
	}

	policy := c.getContainerRetentionPolicy(event)
	now := time.Now()
	tunnelEntry := &types.TunnelEntry{
		ContainerID:     event.ContainerID,
		RetentionPolicy: policy,
		Status:          types.StatusActive,
		CreatedAt:       now,
		LastSyncAt:      now,
	}
	if len(hostnames) > 0 {
		tunnelEntry.Config.Hostname = hostnames[0]
	}
	c.stateManager.AddActiveTunnel(tunnelEntry)

	c.mu.Lock()
	c.updateContainerHealth(event.ContainerID, true)
	c.mu.Unlock()

	return c.syncToCloudflare(ctx)
}
```

- [ ] **Step 3: Run all controller tests**

Run: `go test ./internal/controller/ -v`
Expected: ALL PASS

- [ ] **Step 4: Commit**

---

### Task 5: Refactor RunGC to use Retaining status

**Files:**
- Modify: `internal/state/manager.go` (RunGC method, lines ~440-492)

Replace the retention-policy-based branching with status-based branching. Separate Retaining (Timed/Forever) from PendingDelete (Immediate/expired).

- [ ] **Step 1: Write failing tests**

Add to `internal/state/manager_test.go` (or transition_test.go):

```go
func TestRunGC_RetainingTimed_Expired(t *testing.T) {
	sm := NewManager(slog.Default())
	past := time.Now().Add(-2 * time.Hour)
	sm.AddPendingDeletion(&types.TunnelEntry{
		ContainerID:     "c1",
		ServiceName:     "web",
		Config:          types.TunnelConfiguration{Hostname: "app.example.com"},
		Status:          types.StatusRetaining,
		RetentionPolicy: types.RetentionPolicy{Type: types.Timed, Duration: 1 * time.Hour},
		DeletedAt:       &past,
	})

	expired, err := sm.RunGC(context.Background())
	if err != nil {
		t.Fatalf("RunGC failed: %v", err)
	}

	if len(expired) != 1 {
		t.Fatalf("expected 1 expired entry, got %d", len(expired))
	}
	if expired[0].ContainerID != "c1" {
		t.Errorf("expected c1, got %s", expired[0].ContainerID)
	}

	// Entry should be fully removed
	_, exists := sm.GetPendingDeletion("c1")
	if exists {
		t.Error("expired entry should be removed")
	}
}

func TestRunGC_RetainingTimed_NotExpired(t *testing.T) {
	sm := NewManager(slog.Default())
	recent := time.Now().Add(-5 * time.Minute)
	sm.AddPendingDeletion(&types.TunnelEntry{
		ContainerID:     "c1",
		ServiceName:     "web",
		Config:          types.TunnelConfiguration{Hostname: "app.example.com"},
		Status:          types.StatusRetaining,
		RetentionPolicy: types.RetentionPolicy{Type: types.Timed, Duration: 1 * time.Hour},
		DeletedAt:       &recent,
	})

	expired, err := sm.RunGC(context.Background())
	if err != nil {
		t.Fatalf("RunGC failed: %v", err)
	}

	if len(expired) != 0 {
		t.Fatalf("expected 0 expired entries, got %d", len(expired))
	}

	// Entry should still exist
	_, exists := sm.GetPendingDeletion("c1")
	if !exists {
		t.Error("non-expired Retaining entry should still exist")
	}
}

func TestRunGC_RetainingForever_NeverExpires(t *testing.T) {
	sm := NewManager(slog.Default())
	past := time.Now().Add(-365 * 24 * time.Hour) // a year ago
	sm.AddPendingDeletion(&types.TunnelEntry{
		ContainerID:     "c1",
		ServiceName:     "web",
		Config:          types.TunnelConfiguration{Hostname: "app.example.com"},
		Status:          types.StatusRetaining,
		RetentionPolicy: types.RetentionPolicy{Type: types.Forever},
		DeletedAt:       &past,
	})

	expired, err := sm.RunGC(context.Background())
	if err != nil {
		t.Fatalf("RunGC failed: %v", err)
	}

	if len(expired) != 0 {
		t.Fatalf("Forever entries should never expire, got %d expired", len(expired))
	}
}

func TestRunGC_PendingDelete_CleanedUp(t *testing.T) {
	sm := NewManager(slog.Default())
	sm.AddPendingDeletion(&types.TunnelEntry{
		ContainerID:     "c1",
		ServiceName:     "web",
		Config:          types.TunnelConfiguration{Hostname: "app.example.com"},
		Status:          types.StatusPendingDelete,
		RetentionPolicy: types.RetentionPolicy{Type: types.Immediate},
	})

	expired, err := sm.RunGC(context.Background())
	if err != nil {
		t.Fatalf("RunGC failed: %v", err)
	}

	if len(expired) != 1 {
		t.Fatalf("expected 1 expired entry, got %d", len(expired))
	}

	_, exists := sm.GetPendingDeletion("c1")
	if exists {
		t.Error("PendingDelete entry should be removed")
	}
}
```

- [ ] **Step 2: Refactor RunGC**

Replace the RunGC method body with status-based logic:

```go
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
```

- [ ] **Step 3: Run all state tests**

Run: `go test ./internal/state/ -v`
Expected: ALL PASS

- [ ] **Step 4: Commit**

---

### Task 6: Update persistence for new status type

**Files:**
- Modify: `internal/state/persistence.go` (RegisterGobTypes)
- Modify: `internal/state/manager.go` (LoadFromSnapshot migration)

- [ ] **Step 1: Register new gob types**

In `RegisterGobTypes()`, add `TransitionEvent` and `ActionKind` registrations:

```go
gob.Register(types.TransitionEvent(0))
gob.Register(types.ActionKind(0))
gob.Register(types.Action{})
```

- [ ] **Step 2: Add migration in LoadFromSnapshot**

After loading from snapshot, migrate Phase 1 PendingDelete entries to Phase 2 statuses:

```go
// In LoadFromSnapshot, after loading maps:
// Migrate Phase 1 PendingDelete entries with Timed/Forever to StatusRetaining
for key, entry := range sm.pendingDeletes {
	if entry.Status == types.StatusPendingDelete {
		if entry.RetentionPolicy.Type == types.Timed || entry.RetentionPolicy.Type == types.Forever {
			entry.Status = types.StatusRetaining
		}
	}
}
```

- [ ] **Step 3: Write migration test**

```go
func TestLoadFromSnapshot_MigratesPendingDeleteToRetaining(t *testing.T) {
	sm := NewManager(slog.Default())
	now := time.Now()

	snapshot := &types.StateSnapshot{
		Version:  1,
		ActiveTunnels: map[string]*types.TunnelEntry{
			"c2": {ContainerID: "c2", Status: types.StatusActive},
		},
		PendingDeletions: map[string]*types.TunnelEntry{
			"c1:web": {
				ContainerID:     "c1",
				ServiceName:     "web",
				Status:          types.StatusPendingDelete, // Phase 1 status
				RetentionPolicy: types.RetentionPolicy{Type: types.Timed, Duration: 1 * time.Hour},
				DeletedAt:       &now,
			},
			"c3:api": {
				ContainerID:     "c3",
				ServiceName:     "api",
				Status:          types.StatusPendingDelete, // Phase 1 status
				RetentionPolicy: types.RetentionPolicy{Type: types.Immediate},
			},
		},
	}

	sm.LoadFromSnapshot(snapshot)

	// Timed should be migrated to Retaining
	entry, _ := sm.GetPendingDeletion("c1")
	if entry.Status != types.StatusRetaining {
		t.Errorf("Timed entry should be migrated to StatusRetaining, got %d", entry.Status)
	}

	// Immediate should stay PendingDelete
	entry3, _ := sm.GetPendingDeletion("c3")
	if entry3.Status != types.StatusPendingDelete {
		t.Errorf("Immediate entry should stay StatusPendingDelete, got %d", entry3.Status)
	}
}
```

- [ ] **Step 4: Run tests and commit**

Run: `go test ./internal/state/ -v`

---

### Task 7: Final verification

**Files:** All modified files

- [ ] **Step 1: Run full test suite**

Run: `go test ./... -v`
Expected: ALL PASS

- [ ] **Step 2: Run go vet**

Run: `go vet ./...`
Expected: no issues

- [ ] **Step 3: Run gofmt**

Run: `gofmt -s -w .`

- [ ] **Step 4: Final commit**

---

## Acceptance Criteria

1. All three retention policies (Immediate/Timed/Forever) × all lifecycle stages (stop/restart/expiry) produce correct state transitions
2. No "Ingress deleted but DNS remains" or reverse inconsistency
3. `Forever` entries never auto-cleaned
4. Process restart correctly recovers Retaining state (Timed recalculates remaining time, Forever restores as Retaining)
5. All existing tests continue to pass
