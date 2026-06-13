package state

import (
	"log/slog"
	"os"
	"testing"
	"time"

	"docktunnel/pkg/types"
)

func setupTestManager() *Manager {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	return NewManager(logger)
}

func TestTransition_Stopped_Immediate(t *testing.T) {
	sm := setupTestManager()
	sm.AddActiveTunnel(&types.TunnelEntry{
		ContainerID: "c1",
		ServiceName: "web",
		Config:      types.TunnelConfiguration{Hostname: "app.example.com"},
		Status:      types.StatusActive,
	})

	actions, err := sm.Transition("c1", "web", types.EventContainerStopped, &types.RetentionPolicy{Type: types.Immediate})
	if err != nil {
		t.Fatalf("Transition failed: %v", err)
	}

	if len(actions) != 1 || actions[0].Kind != types.ActionDeleteRoute {
		t.Fatalf("expected 1 ActionDeleteRoute, got %v", actions)
	}
	if actions[0].Hostname != "app.example.com" {
		t.Errorf("expected hostname app.example.com, got %s", actions[0].Hostname)
	}

	pending, exists := sm.GetPendingDeletion("c1")
	if !exists {
		t.Fatal("expected entry in pending deletes")
	}
	if pending.Status != types.StatusPendingDelete {
		t.Errorf("expected StatusPendingDelete, got %d", pending.Status)
	}

	_, activeExists := sm.GetActiveTunnel("c1", "web")
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

	actions, err := sm.Transition("c1", "web", types.EventContainerStopped, &types.RetentionPolicy{Type: types.Timed, Duration: 1 * time.Hour})
	if err != nil {
		t.Fatalf("Transition failed: %v", err)
	}

	if len(actions) != 0 {
		t.Fatalf("expected 0 actions for Timed retention, got %v", actions)
	}

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

	actions, err := sm.Transition("c1", "web", types.EventContainerStopped, &types.RetentionPolicy{Type: types.Forever})
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

	actions, err := sm.Transition("c1", "web", types.EventContainerStarted, nil)
	if err != nil {
		t.Fatalf("Transition failed: %v", err)
	}

	if len(actions) != 0 {
		t.Fatalf("expected 0 actions, got %v", actions)
	}

	entry, exists := sm.GetActiveTunnel("c1", "web")
	if !exists {
		t.Fatal("expected entry in active tunnels")
	}
	if entry.Status != types.StatusActive {
		t.Errorf("expected StatusActive, got %d", entry.Status)
	}
	if entry.DeletedAt != nil {
		t.Error("DeletedAt should be nil after restoration")
	}

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

	actions, err := sm.Transition("c1", "web", types.EventRetentionExpired, nil)
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

	actions, err := sm.Transition("c1", "web", types.EventCleanupComplete, nil)
	if err != nil {
		t.Fatalf("Transition failed: %v", err)
	}

	if len(actions) != 0 {
		t.Fatalf("expected 0 actions, got %v", actions)
	}

	_, pendingExists := sm.GetPendingDeletion("c1")
	if pendingExists {
		t.Error("entry should be removed after cleanup complete")
	}
}

func TestTransition_Stopped_NoEntry(t *testing.T) {
	sm := setupTestManager()

	actions, err := sm.Transition("nonexistent", "web", types.EventContainerStopped, &types.RetentionPolicy{Type: types.Immediate})
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

	_, _ = sm.Transition("c1", "web", types.EventContainerStopped, &types.RetentionPolicy{Type: types.Immediate})

	if !sm.IsDirty() {
		t.Error("Transition should mark state as dirty")
	}
}

func TestTransition_Stopped_MultiService_Independent(t *testing.T) {
	sm := setupTestManager()
	sm.AddActiveTunnel(&types.TunnelEntry{
		ContainerID: "c1", ServiceName: "web",
		Config:          types.TunnelConfiguration{Hostname: "web.example.com"},
		Status:          types.StatusActive,
		RetentionPolicy: types.RetentionPolicy{Type: types.Timed, Duration: 1 * time.Hour},
	})
	sm.AddActiveTunnel(&types.TunnelEntry{
		ContainerID: "c1", ServiceName: "api",
		Config:          types.TunnelConfiguration{Hostname: "api.example.com"},
		Status:          types.StatusActive,
		RetentionPolicy: types.RetentionPolicy{Type: types.Immediate},
	})

	// Stop web with Timed — should retain
	webActions, _ := sm.Transition("c1", "web", types.EventContainerStopped, &types.RetentionPolicy{Type: types.Timed, Duration: 1 * time.Hour})
	if len(webActions) != 0 {
		t.Fatalf("expected 0 actions for Timed web, got %v", webActions)
	}

	// Stop api with Immediate — should delete
	apiActions, _ := sm.Transition("c1", "api", types.EventContainerStopped, &types.RetentionPolicy{Type: types.Immediate})
	if len(apiActions) != 1 || apiActions[0].Kind != types.ActionDeleteRoute {
		t.Fatalf("expected 1 ActionDeleteRoute for Immediate api, got %v", apiActions)
	}
	if apiActions[0].Hostname != "api.example.com" {
		t.Errorf("expected api.example.com, got %s", apiActions[0].Hostname)
	}

	// Verify: web in Retaining, api in PendingDelete
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
		t.Errorf("expected 1 Retaining + 1 PendingDelete, got %d Retaining + %d PendingDelete",
			retainingCount, pendingDeleteCount)
	}
}
