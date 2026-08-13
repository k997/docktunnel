package state

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"docktunnel/pkg/types"
)

func TestEnqueueAction_RetryableError(t *testing.T) {
	sm := NewManager(slog.New(slog.NewTextHandler(os.Stdout, nil)))
	action := types.Action{
		Kind:        types.ActionDeleteRoute,
		ContainerID: "c1",
		ServiceName: "web",
		Hostname:    "app.example.com",
	}

	sm.EnqueueAction(action, &types.RetryableError{Err: errors.New("503")})

	records := sm.GetAllPendingActions()
	if len(records) != 1 {
		t.Fatalf("expected 1 pending action, got %d", len(records))
	}
	for _, rec := range records {
		if rec.Dead {
			t.Error("record should not be dead")
		}
		if rec.RetryCount != 0 {
			t.Errorf("expected RetryCount 0, got %d", rec.RetryCount)
		}
		if rec.NextRetryAt.IsZero() {
			t.Error("expected NextRetryAt to be set")
		}
		if rec.Action.Hostname != "app.example.com" {
			t.Errorf("expected hostname app.example.com, got %s", rec.Action.Hostname)
		}
	}
}

func TestEnqueueAction_PermanentError(t *testing.T) {
	sm := NewManager(slog.New(slog.NewTextHandler(os.Stdout, nil)))
	action := types.Action{
		Kind:     types.ActionDeleteRoute,
		Hostname: "app.example.com",
	}

	sm.EnqueueAction(action, &types.PermanentError{Err: errors.New("403")})

	records := sm.GetAllPendingActions()
	if len(records) != 1 {
		t.Fatalf("expected 1 pending action, got %d", len(records))
	}
	for _, rec := range records {
		if !rec.Dead {
			t.Error("record should be dead")
		}
	}
}

func TestEnqueueAction_PlainError_TreatedAsRetryable(t *testing.T) {
	sm := NewManager(slog.New(slog.NewTextHandler(os.Stdout, nil)))
	action := types.Action{
		Kind:     types.ActionDeleteRoute,
		Hostname: "app.example.com",
	}

	sm.EnqueueAction(action, errors.New("unknown failure"))

	records := sm.GetAllPendingActions()
	if len(records) != 1 {
		t.Fatalf("expected 1 pending action, got %d", len(records))
	}
	for _, rec := range records {
		if rec.Dead {
			t.Error("plain error should be treated as retryable")
		}
	}
}

func TestGetDeadActions(t *testing.T) {
	sm := NewManager(slog.New(slog.NewTextHandler(os.Stdout, nil)))

	sm.EnqueueAction(types.Action{Kind: types.ActionDeleteRoute, Hostname: "dead.example.com"},
		&types.PermanentError{Err: errors.New("403")})
	sm.EnqueueAction(types.Action{Kind: types.ActionDeleteRoute, Hostname: "alive.example.com"},
		&types.RetryableError{Err: errors.New("503")})

	dead := sm.GetDeadActions()
	if len(dead) != 1 {
		t.Fatalf("expected 1 dead action, got %d", len(dead))
	}
	if dead[0].Action.Hostname != "dead.example.com" {
		t.Errorf("expected dead.example.com, got %s", dead[0].Action.Hostname)
	}
}

func TestRunCompensation_SuccessfulRetry(t *testing.T) {
	sm := NewManager(slog.New(slog.NewTextHandler(os.Stdout, nil)))
	sm.compInitialDelay = 1 * time.Millisecond
	sm.compPollInterval = 10 * time.Millisecond

	action := types.Action{
		Kind:     types.ActionDeleteRoute,
		Hostname: "app.example.com",
	}
	sm.EnqueueAction(action, &types.RetryableError{Err: errors.New("503")})

	// Force NextRetryAt to past so it's immediately due
	for _, rec := range sm.pendingActions {
		rec.NextRetryAt = time.Now().Add(-1 * time.Second)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// The executor runs on the compensation goroutine while the test goroutine
	// asserts on the result — protect the shared slice with a mutex instead of
	// a bare append + time.Sleep (which is a data race under -race).
	var mu sync.Mutex
	var executed []types.Action
	executor := func(a types.Action) error {
		mu.Lock()
		executed = append(executed, a)
		mu.Unlock()
		return nil
	}

	go sm.RunCompensation(ctx, executor)

	// Wait for the action to be executed (bounded, no fixed sleep).
	eventually(t, 2*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(executed) == 1
	})

	mu.Lock()
	if len(executed) != 1 {
		t.Fatalf("expected 1 execution, got %d", len(executed))
	}
	if executed[0].Hostname != "app.example.com" {
		t.Errorf("expected hostname app.example.com, got %s", executed[0].Hostname)
	}
	mu.Unlock()

	// Record removal happens on the compensation goroutine after the executor
	// returns — poll instead of assuming it landed before our next read.
	eventually(t, 2*time.Second, func() bool {
		return len(sm.GetAllPendingActions()) == 0
	})
}

// eventually polls cond until it returns true or the timeout elapses.
func eventually(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met within timeout")
}

func TestRunCompensation_RetryableFailureBacksOff(t *testing.T) {
	sm := NewManager(slog.New(slog.NewTextHandler(os.Stdout, nil)))
	sm.compInitialDelay = 20 * time.Millisecond
	sm.compMaxDelay = 50 * time.Millisecond
	sm.compMaxRetries = 1000 // far above what the observation window can reach
	sm.compPollInterval = 5 * time.Millisecond

	action := types.Action{
		Kind:     types.ActionDeleteRoute,
		Hostname: "app.example.com",
	}
	sm.EnqueueAction(action, &types.RetryableError{Err: errors.New("503")})

	// Force NextRetryAt to past
	for _, rec := range sm.pendingActions {
		rec.NextRetryAt = time.Now().Add(-1 * time.Second)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	executor := func(a types.Action) error {
		return &types.RetryableError{Err: errors.New("still failing")}
	}

	go sm.RunCompensation(ctx, executor)

	// Wait until at least one retry has happened while the record is still
	// alive (RetryCount is far below MaxRetries, so it cannot exhaust during
	// the observation window). Bounded polling replaces the previous fixed
	// time.Sleep(100ms), which was both timing-flaky and — because
	// GetAllPendingActions used to return live pointers — raced with the
	// compensation goroutine under -race.
	eventually(t, 2*time.Second, func() bool {
		records := sm.GetAllPendingActions()
		if len(records) != 1 {
			return false
		}
		rec := records[0]
		return rec.RetryCount > 0 && !rec.Dead
	})
}

func TestRunCompensation_ExhaustedRetriesMarksDead(t *testing.T) {
	sm := NewManager(slog.New(slog.NewTextHandler(os.Stdout, nil)))
	sm.compInitialDelay = 1 * time.Millisecond
	sm.compMaxDelay = 1 * time.Millisecond
	sm.compMaxRetries = 2
	sm.compPollInterval = 10 * time.Millisecond

	action := types.Action{
		Kind:     types.ActionDeleteRoute,
		Hostname: "app.example.com",
	}
	sm.EnqueueAction(action, &types.RetryableError{Err: errors.New("503")})

	// Force NextRetryAt to past and set retry count near limit
	for _, rec := range sm.pendingActions {
		rec.NextRetryAt = time.Now().Add(-1 * time.Second)
		rec.RetryCount = 2 // at max already
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	executor := func(a types.Action) error {
		return &types.RetryableError{Err: errors.New("still failing")}
	}

	go sm.RunCompensation(ctx, executor)
	time.Sleep(500 * time.Millisecond)

	// Retry-exhausted records are marked Dead and then cleaned up at the end
	// of the compensation round (B3/B15) — the queue must not retain them
	// forever.
	if got := len(sm.GetAllPendingActions()); got != 0 {
		t.Errorf("expected retry-exhausted record to be cleaned up, got %d pending", got)
	}
}

func TestSnapshotIncludesPendingActions(t *testing.T) {
	sm := NewManager(slog.New(slog.NewTextHandler(os.Stdout, nil)))
	sm.EnqueueAction(types.Action{
		Kind:     types.ActionDeleteRoute,
		Hostname: "app.example.com",
	}, &types.RetryableError{Err: errors.New("503")})

	snap := sm.GetSnapshot()
	if len(snap.PendingActions) != 1 {
		t.Fatalf("expected 1 pending action in snapshot, got %d", len(snap.PendingActions))
	}
}

func TestLoadFromSnapshot_RestoresPendingActions(t *testing.T) {
	sm := NewManager(slog.New(slog.NewTextHandler(os.Stdout, nil)))
	sm.EnqueueAction(types.Action{
		Kind:        types.ActionDeleteRoute,
		ContainerID: "c1",
		ServiceName: "web",
		Hostname:    "app.example.com",
	}, &types.RetryableError{Err: errors.New("503")})

	snap := sm.GetSnapshot()

	sm2 := NewManager(slog.New(slog.NewTextHandler(os.Stdout, nil)))
	sm2.LoadFromSnapshot(snap)

	records := sm2.GetAllPendingActions()
	if len(records) != 1 {
		t.Fatalf("expected 1 pending action after load, got %d", len(records))
	}
}

func TestLoadFromSnapshot_MigratesV2ToV3(t *testing.T) {
	sm := NewManager(slog.New(slog.NewTextHandler(os.Stdout, nil)))

	snap := &types.StateSnapshot{
		Version:          2,
		Timestamp:        time.Now().UTC(),
		ActiveTunnels:    map[string]*types.TunnelEntry{},
		PendingDeletions: map[string]*types.TunnelEntry{},
		PendingActions:   nil,
	}

	sm.LoadFromSnapshot(snap)

	records := sm.GetAllPendingActions()
	if len(records) != 0 {
		t.Fatalf("expected 0 pending actions for v2 migration, got %d", len(records))
	}
	if sm.pendingActions == nil {
		t.Error("pendingActions should be initialized, not nil")
	}
}

func TestEnqueueAction_RespectsQueueCap(t *testing.T) {
	sm := NewManager(slog.New(slog.NewTextHandler(os.Stdout, nil)))
	sm.SetCompensationQueueCap(2)

	// First two should succeed
	sm.EnqueueAction(types.Action{Kind: types.ActionDeleteRoute, Hostname: "a.example.com"},
		&types.RetryableError{Err: errors.New("503")})
	sm.EnqueueAction(types.Action{Kind: types.ActionDeleteRoute, Hostname: "b.example.com"},
		&types.RetryableError{Err: errors.New("503")})

	if got := len(sm.GetAllPendingActions()); got != 2 {
		t.Fatalf("expected 2 pending actions, got %d", got)
	}

	// Third should be rejected (queue full)
	sm.EnqueueAction(types.Action{Kind: types.ActionDeleteRoute, Hostname: "c.example.com"},
		&types.RetryableError{Err: errors.New("503")})

	if got := len(sm.GetAllPendingActions()); got != 2 {
		t.Fatalf("expected queue to remain at 2 after cap rejection, got %d", got)
	}

	for _, rec := range sm.GetAllPendingActions() {
		if rec.Action.Hostname == "c.example.com" {
			t.Error("rejected action should not be present in queue")
		}
	}
}

func TestEnqueueAction_GeneratesUniqueIDs(t *testing.T) {
	sm := NewManager(slog.New(slog.NewTextHandler(os.Stdout, nil)))

	// Enqueue 100 rapid actions with identical container/service fields.
	// Time-based IDs would risk collision; crypto/rand IDs must not.
	for range 100 {
		sm.EnqueueAction(types.Action{
			Kind:        types.ActionDeleteRoute,
			ContainerID: "c1",
			ServiceName: "web",
			Hostname:    "app.example.com",
		}, &types.RetryableError{Err: errors.New("503")})
	}

	records := sm.GetAllPendingActions()
	if len(records) != 100 {
		t.Fatalf("expected 100 unique records, got %d", len(records))
	}

	seen := make(map[string]struct{}, len(records))
	for _, rec := range records {
		if _, dup := seen[rec.ID]; dup {
			t.Errorf("duplicate ID generated: %s", rec.ID)
		}
		seen[rec.ID] = struct{}{}
	}
}

func TestClassifyError(t *testing.T) {
	if got := classifyError(nil); got != "" {
		t.Errorf("expected empty string for nil error, got %q", got)
	}
	if got := classifyError(&types.PermanentError{Err: errors.New("403")}); got != "permanent" {
		t.Errorf("expected permanent, got %q", got)
	}
	if got := classifyError(&types.RetryableError{Err: errors.New("503")}); got != "retryable" {
		t.Errorf("expected retryable, got %q", got)
	}
	if got := classifyError(errors.New("unknown")); got != "unknown" {
		t.Errorf("expected unknown, got %q", got)
	}
}
