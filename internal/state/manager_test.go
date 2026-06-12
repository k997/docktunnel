package state

import (
	"context"
	"testing"
	"time"

	"log/slog"

	"docktunnel/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewManager(t *testing.T) {
	logger := slog.Default()
	sm := NewManager(logger)

	assert.NotNil(t, sm)
	assert.NotNil(t, sm.activeTunnels)
	assert.NotNil(t, sm.pendingDeletes)
	assert.NotNil(t, sm.flappingContainers)
}

func TestAddActiveTunnel(t *testing.T) {
	sm := NewManager(slog.Default())

	entry := &types.TunnelEntry{
		ContainerID: "test-container-1",
		TunnelID:    "tunnel-123",
		ServiceName: "web",
		Status:      types.StatusActive,
		CreatedAt:   time.Now().UTC(),
		LastSyncAt:  time.Now().UTC(),
		Config: types.TunnelConfiguration{
			Hostname:   "test.example.com",
			ServiceURL: "http://localhost:8080",
		},
	}

	sm.AddActiveTunnel(entry)

	retrieved, ok := sm.GetActiveTunnel("test-container-1")
	assert.True(t, ok)
	assert.Equal(t, entry.ContainerID, retrieved.ContainerID)
	assert.Equal(t, entry.ServiceName, retrieved.ServiceName)
	assert.Equal(t, types.StatusActive, retrieved.Status)
}

func TestRemoveActiveTunnel(t *testing.T) {
	sm := NewManager(slog.Default())

	entry := &types.TunnelEntry{
		ContainerID: "test-container-1",
		Status:      types.StatusActive,
	}

	sm.AddActiveTunnel(entry)
	sm.RemoveActiveTunnel("test-container-1")

	_, ok := sm.GetActiveTunnel("test-container-1")
	assert.False(t, ok)
}

func TestGetAllActiveTunnels(t *testing.T) {
	sm := NewManager(slog.Default())

	// Add multiple tunnels
	for i := 1; i <= 3; i++ {
		entry := &types.TunnelEntry{
			ContainerID: "test-container-" + string(rune('0'+i)),
			Status:      types.StatusActive,
		}
		sm.AddActiveTunnel(entry)
	}

	all := sm.GetAllActiveTunnels()
	assert.Len(t, all, 3)
}

func TestAddPendingDeletion(t *testing.T) {
	sm := NewManager(slog.Default())

	now := time.Now()
	entry := &types.TunnelEntry{
		ContainerID: "test-container-1",
		ServiceName: "web",
		Status:      types.StatusPendingDelete,
		DeletedAt:   &now,
		RetentionPolicy: types.RetentionPolicy{
			Type:     types.Timed,
			Duration: 30 * time.Minute,
		},
	}

	sm.AddPendingDeletion(entry)

	// Should not be in active tunnels
	_, ok := sm.GetActiveTunnel("test-container-1")
	assert.False(t, ok)

	// Should be in pending deletions
	retrieved, ok := sm.GetPendingDeletion("test-container-1")
	assert.True(t, ok)
	assert.Equal(t, entry.ContainerID, retrieved.ContainerID)
	assert.Equal(t, types.StatusPendingDelete, retrieved.Status)
}

func TestAddPendingDeletion_MultiService(t *testing.T) {
	sm := NewManager(slog.Default())

	now := time.Now()

	webEntry := &types.TunnelEntry{
		ContainerID: "container-1",
		ServiceName: "web",
		Status:      types.StatusPendingDelete,
		DeletedAt:   &now,
		Config:      types.TunnelConfiguration{Hostname: "web.example.com"},
		RetentionPolicy: types.RetentionPolicy{
			Type:     types.Timed,
			Duration: 30 * time.Minute,
		},
	}
	apiEntry := &types.TunnelEntry{
		ContainerID: "container-1",
		ServiceName: "api",
		Status:      types.StatusPendingDelete,
		DeletedAt:   &now,
		Config:      types.TunnelConfiguration{Hostname: "api.example.com"},
		RetentionPolicy: types.RetentionPolicy{
			Type:     types.Timed,
			Duration: 30 * time.Minute,
		},
	}

	sm.AddPendingDeletion(webEntry)
	sm.AddPendingDeletion(apiEntry)

	// Both services should be retrievable
	entries := sm.GetPendingDeletionsByContainer("container-1")
	assert.Len(t, entries, 2, "Both services should be stored, not overwritten")

	hostnames := map[string]bool{}
	for _, e := range entries {
		hostnames[e.Config.Hostname] = true
	}
	assert.True(t, hostnames["web.example.com"])
	assert.True(t, hostnames["api.example.com"])
}

func TestRestoreActiveTunnel(t *testing.T) {
	sm := NewManager(slog.Default())

	now := time.Now()
	entry := &types.TunnelEntry{
		ContainerID: "test-container-1",
		ServiceName: "web",
		Status:      types.StatusPendingDelete,
		DeletedAt:   &now,
	}

	sm.AddPendingDeletion(entry)
	err := sm.RestoreActiveTunnel("test-container-1")

	require.NoError(t, err)

	// Should be back in active tunnels
	retrieved, ok := sm.GetActiveTunnel("test-container-1")
	assert.True(t, ok)
	assert.Equal(t, types.StatusActive, retrieved.Status)
	assert.Nil(t, retrieved.DeletedAt)

	// Should not be in pending deletions
	_, ok = sm.GetPendingDeletion("test-container-1")
	assert.False(t, ok)
}

func TestFlappingDetection(t *testing.T) {
	sm := NewManager(slog.Default())
	containerID := "test-flapping-container"

	// Record transitions below threshold
	for i := 0; i < 4; i++ {
		sm.RecordTransition(containerID)
	}

	// Should not be flapping yet
	isFlapping := sm.CheckFlapping(containerID)
	assert.False(t, isFlapping, "Should not be flapping with 4 transitions")

	// Add one more transition to exceed threshold
	sm.RecordTransition(containerID)

	// Should now be flapping
	isFlapping = sm.CheckFlapping(containerID)
	assert.True(t, isFlapping, "Should be flapping with 5 transitions")

	// Check flapping state
	state, ok := sm.GetFlappingState(containerID)
	assert.True(t, ok, "Flapping state should exist")
	assert.True(t, state.LastFlapped.Before(time.Now()), "LastFlapped should be in the past")
	assert.True(t, state.CoolingUntil.After(time.Now()), "CoolingUntil should be in the future")
}

func TestFlappingExpiration(t *testing.T) {
	sm := NewManager(slog.Default())
	containerID := "test-expiring-container"

	// Manually mark as flapping with short cooling period
	sm.MarkAsFlapping(containerID, 100*time.Millisecond)

	// Should be flapping
	assert.True(t, sm.CheckFlapping(containerID))

	// Wait for cooling period to expire
	time.Sleep(150 * time.Millisecond)

	// Should no longer be flapping
	assert.False(t, sm.CheckFlapping(containerID))
}

// containsExpiredEntry checks if the expired entries contain one for the given containerID
func containsExpiredEntry(entries []*types.TunnelEntry, containerID string) bool {
	for _, e := range entries {
		if e.ContainerID == containerID {
			return true
		}
	}
	return false
}

func TestGetSnapshot(t *testing.T) {
	sm := NewManager(slog.Default())

	// Add active tunnel
	activeEntry := &types.TunnelEntry{
		ContainerID: "active-1",
		Status:      types.StatusActive,
	}
	sm.AddActiveTunnel(activeEntry)

	// Add pending deletion
	now := time.Now()
	pendingEntry := &types.TunnelEntry{
		ContainerID: "pending-1",
		ServiceName: "web",
		Status:      types.StatusPendingDelete,
		DeletedAt:   &now,
	}
	sm.AddPendingDeletion(pendingEntry)

	// Get snapshot
	snapshot := sm.GetSnapshot()

	assert.NotNil(t, snapshot)
	assert.Equal(t, 1, snapshot.Version)
	assert.Len(t, snapshot.ActiveTunnels, 1)
	assert.Len(t, snapshot.PendingDeletions, 1)
	assert.Contains(t, snapshot.ActiveTunnels, "active-1")
	// Key is compound: "pending-1:web"
	assert.Contains(t, snapshot.PendingDeletions, "pending-1:web")
}

func TestLoadFromSnapshot(t *testing.T) {
	sm := NewManager(slog.Default())

	// Create a snapshot with compound keys
	now := time.Now()
	snapshot := &types.StateSnapshot{
		Version:   1,
		Timestamp: now,
		ActiveTunnels: map[string]*types.TunnelEntry{
			"active-1": {
				ContainerID: "active-1",
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

	// Load snapshot
	sm.LoadFromSnapshot(snapshot)

	// Verify loaded state
	entry, ok := sm.GetActiveTunnel("active-1")
	assert.True(t, ok)
	assert.Equal(t, "active-1", entry.ContainerID)

	pending, ok := sm.GetPendingDeletion("pending-1")
	assert.True(t, ok)
	assert.Equal(t, "pending-1", pending.ContainerID)

	flapping, ok := sm.GetFlappingState("flapping-1")
	assert.True(t, ok)
	assert.True(t, flapping.CoolingUntil.After(now))
}

func TestRunGC_ImmediatePolicy(t *testing.T) {
	sm := NewManager(slog.Default())

	now := time.Now()
	entry := &types.TunnelEntry{
		ContainerID: "immediate-1",
		ServiceName: "web",
		Status:      types.StatusPendingDelete,
		DeletedAt:   &now,
		RetentionPolicy: types.RetentionPolicy{
			Type: types.Immediate,
		},
	}
	sm.AddPendingDeletion(entry)

	expired, err := sm.RunGC(context.Background())
	require.NoError(t, err)
	assert.True(t, containsExpiredEntry(expired, "immediate-1"), "Immediate entry should be expired")

	// Should be removed
	_, ok := sm.GetPendingDeletion("immediate-1")
	assert.False(t, ok)
}

func TestRunGC_TimedPolicy(t *testing.T) {
	sm := NewManager(slog.Default())

	// Create entry with expired retention
	past := time.Now().Add(-1 * time.Hour)
	entry := &types.TunnelEntry{
		ContainerID: "timed-1",
		ServiceName: "web",
		Status:      types.StatusPendingDelete,
		DeletedAt:   &past,
		RetentionPolicy: types.RetentionPolicy{
			Type:     types.Timed,
			Duration: 30 * time.Minute,
		},
	}
	sm.AddPendingDeletion(entry)

	expired, err := sm.RunGC(context.Background())
	require.NoError(t, err)
	assert.True(t, containsExpiredEntry(expired, "timed-1"), "Expired timed entry should be expired")

	// Should be removed
	_, ok := sm.GetPendingDeletion("timed-1")
	assert.False(t, ok)
}

func TestRunGC_TimedPolicyNotExpired(t *testing.T) {
	sm := NewManager(slog.Default())

	// Create entry with unexpired retention
	recent := time.Now().Add(-5 * time.Minute)
	entry := &types.TunnelEntry{
		ContainerID: "timed-not-expired",
		ServiceName: "web",
		Status:      types.StatusPendingDelete,
		DeletedAt:   &recent,
		RetentionPolicy: types.RetentionPolicy{
			Type:     types.Timed,
			Duration: 30 * time.Minute,
		},
	}
	sm.AddPendingDeletion(entry)

	expired, err := sm.RunGC(context.Background())
	require.NoError(t, err)
	assert.False(t, containsExpiredEntry(expired, "timed-not-expired"), "Unexpired entry should not be expired")

	// Should still be present
	_, ok := sm.GetPendingDeletion("timed-not-expired")
	assert.True(t, ok)
}

func TestRunGC_ForeverPolicy(t *testing.T) {
	sm := NewManager(slog.Default())

	now := time.Now()
	entry := &types.TunnelEntry{
		ContainerID: "forever-1",
		ServiceName: "web",
		Status:      types.StatusPendingDelete,
		DeletedAt:   &now,
		RetentionPolicy: types.RetentionPolicy{
			Type: types.Forever,
		},
	}
	sm.AddPendingDeletion(entry)

	expired, err := sm.RunGC(context.Background())
	require.NoError(t, err)
	assert.False(t, containsExpiredEntry(expired, "forever-1"), "Forever entries should not be expired")

	// Should still be present (forever policy)
	_, ok := sm.GetPendingDeletion("forever-1")
	assert.True(t, ok)
}

func TestGetStats(t *testing.T) {
	sm := NewManager(slog.Default())

	// Add active tunnels
	sm.AddActiveTunnel(&types.TunnelEntry{ContainerID: "active-1", Status: types.StatusActive})
	sm.AddActiveTunnel(&types.TunnelEntry{ContainerID: "active-2", Status: types.StatusActive})

	// Add pending deletion
	now := time.Now()
	sm.AddPendingDeletion(&types.TunnelEntry{
		ContainerID: "pending-1",
		ServiceName: "web",
		Status:      types.StatusPendingDelete,
		DeletedAt:   &now,
	})

	// Mark flapping
	sm.MarkAsFlapping("flapping-1", 5*time.Minute)

	stats := sm.GetStats()
	assert.Equal(t, 2, stats["active_tunnels"])
	assert.Equal(t, 1, stats["pending_deletions"])
	assert.Equal(t, 1, stats["flapping_containers"])
}

func TestFlappingWindowCleanup(t *testing.T) {
	sm := NewManager(slog.Default())
	containerID := "test-window-cleanup"

	// Record old transitions outside window
	oldTime := time.Now().Add(-2 * time.Minute)
	for i := 0; i < 3; i++ {
		sm.RecordTransition(containerID)
	}

	// Manually set old transitions to simulate expired ones
	sm.mu.Lock()
	if state, exists := sm.flappingContainers[containerID]; exists {
		for i := range state.Transitions {
			state.Transitions[i] = oldTime
		}
	}
	sm.mu.Unlock()

	// Record new transition within window (should trigger cleanup of old ones)
	sm.RecordTransition(containerID)

	// Verify old transitions were cleaned up by checking transition count
	state, ok := sm.GetFlappingState(containerID)
	assert.True(t, ok, "Flapping state should exist")
	assert.Len(t, state.Transitions, 1, "Old transitions should be cleaned up, leaving only the new one")

	// Add enough new transitions to trigger flapping
	for i := 0; i < 4; i++ {
		sm.RecordTransition(containerID)
	}

	// Now should be flapping (1 old + 4 new = 5 total)
	isFlapping := sm.CheckFlapping(containerID)
	assert.True(t, isFlapping, "Should be flapping after 5 transitions within window")
}

// TestContainerRestartCancelsRetention tests that a container restart
// cancels the retention timer and restores ACTIVE status (T057)
func TestContainerRestartCancelsRetention(t *testing.T) {
	sm := NewManager(slog.Default())

	// Add a tunnel to active tunnels
	entry := &types.TunnelEntry{
		ContainerID: "restart-test",
		ServiceName: "web",
		Status:      types.StatusActive,
		Config: types.TunnelConfiguration{
			Hostname: "test.example.com",
		},
	}
	sm.AddActiveTunnel(entry)

	// Simulate container stop - move to pending deletion
	now := time.Now()
	entry.DeletedAt = &now
	entry.Status = types.StatusPendingDelete
	entry.RetentionPolicy = types.RetentionPolicy{
		Type:     types.Timed,
		Duration: 30 * time.Minute,
	}
	sm.AddPendingDeletion(entry)

	// Verify it's in pending deletions
	_, ok := sm.GetPendingDeletion("restart-test")
	assert.True(t, ok, "Should be in pending deletions")

	// Simulate container restart - restore to active
	err := sm.RestoreActiveTunnel("restart-test")
	require.NoError(t, err)

	// Verify it's back in active tunnels
	retrieved, ok := sm.GetActiveTunnel("restart-test")
	assert.True(t, ok, "Should be back in active tunnels")
	assert.Equal(t, types.StatusActive, retrieved.Status, "Status should be Active")
	assert.Nil(t, retrieved.DeletedAt, "DeletedAt should be cleared")

	// Verify it's no longer in pending deletions
	_, ok = sm.GetPendingDeletion("restart-test")
	assert.False(t, ok, "Should not be in pending deletions anymore")
}

// TestGC_PreservesForeverEntries tests that Forever retention policy
// entries are preserved during garbage collection (T056)
func TestGC_PreservesForeverEntries(t *testing.T) {
	sm := NewManager(slog.Default())

	// Add entries with different retention policies
	now := time.Now()

	// Forever entry
	foreverEntry := &types.TunnelEntry{
		ContainerID: "forever-entry",
		ServiceName: "web",
		Status:      types.StatusPendingDelete,
		DeletedAt:   &now,
		RetentionPolicy: types.RetentionPolicy{
			Type: types.Forever,
		},
	}
	sm.AddPendingDeletion(foreverEntry)

	// Run GC
	expired, err := sm.RunGC(context.Background())
	require.NoError(t, err)

	// Forever entry should NOT be expired
	assert.False(t, containsExpiredEntry(expired, "forever-entry"), "Forever entries should not be garbage collected")

	// Should still be in pending deletions
	_, ok := sm.GetPendingDeletion("forever-entry")
	assert.True(t, ok, "Forever entry should still be in pending deletions")
}

// TestGC_ExpireTimedEntries tests that timed retention policy entries
// are expired when their timer elapses (T056)
func TestGC_ExpireTimedEntries(t *testing.T) {
	sm := NewManager(slog.Default())

	// Add a timed entry that expired long ago
	oldTime := time.Now().Add(-2 * time.Hour)
	expiredEntry := &types.TunnelEntry{
		ContainerID: "expired-timed",
		ServiceName: "web",
		Status:      types.StatusPendingDelete,
		DeletedAt:   &oldTime,
		RetentionPolicy: types.RetentionPolicy{
			Type:     types.Timed,
			Duration: 30 * time.Minute,
		},
	}
	sm.AddPendingDeletion(expiredEntry)

	// Run GC
	expired, err := sm.RunGC(context.Background())
	require.NoError(t, err)

	// Should be expired
	assert.True(t, containsExpiredEntry(expired, "expired-timed"), "Expired timed entry should be garbage collected")

	// Should no longer be in pending deletions
	_, ok := sm.GetPendingDeletion("expired-timed")
	assert.False(t, ok, "Expired entry should be removed from pending deletions")
}

// TestGC_PreserveUnexpiredTimedEntries tests that unexpired timed
// retention policy entries are preserved (T056)
func TestGC_PreserveUnexpiredTimedEntries(t *testing.T) {
	sm := NewManager(slog.Default())

	// Add a timed entry that hasn't expired yet
	recentTime := time.Now().Add(-5 * time.Minute)
	unexpiredEntry := &types.TunnelEntry{
		ContainerID: "unexpired-timed",
		ServiceName: "web",
		Status:      types.StatusPendingDelete,
		DeletedAt:   &recentTime,
		RetentionPolicy: types.RetentionPolicy{
			Type:     types.Timed,
			Duration: 30 * time.Minute,
		},
	}
	sm.AddPendingDeletion(unexpiredEntry)

	// Run GC
	expired, err := sm.RunGC(context.Background())
	require.NoError(t, err)

	// Should NOT be expired
	assert.False(t, containsExpiredEntry(expired, "unexpired-timed"), "Unexpired timed entry should not be garbage collected")

	// Should still be in pending deletions
	_, ok := sm.GetPendingDeletion("unexpired-timed")
	assert.True(t, ok, "Unexpired entry should still be in pending deletions")
}
