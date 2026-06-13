package state

import (
	"context"
	"errors"
	"log/slog"
	"os"
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

	var executed []types.Action
	executor := func(a types.Action) error {
		executed = append(executed, a)
		return nil
	}

	go sm.RunCompensation(ctx, executor)

	// Wait for execution
	time.Sleep(500 * time.Millisecond)

	if len(executed) != 1 {
		t.Fatalf("expected 1 execution, got %d", len(executed))
	}
	if executed[0].Hostname != "app.example.com" {
		t.Errorf("expected hostname app.example.com, got %s", executed[0].Hostname)
	}

	// Record should be removed after successful execution
	records := sm.GetAllPendingActions()
	if len(records) != 0 {
		t.Errorf("expected 0 pending actions after success, got %d", len(records))
	}
}

func TestRunCompensation_RetryableFailureBacksOff(t *testing.T) {
	sm := NewManager(slog.New(slog.NewTextHandler(os.Stdout, nil)))
	sm.compInitialDelay = 50 * time.Millisecond
	sm.compMaxDelay = 10 * time.Millisecond
	sm.compMaxRetries = 10
	sm.compPollInterval = 10 * time.Millisecond

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

	// Wait for at least one retry but not long enough to exhaust
	time.Sleep(100 * time.Millisecond)

	records := sm.GetAllPendingActions()
	if len(records) != 1 {
		t.Fatalf("expected 1 pending action, got %d", len(records))
	}
	for _, rec := range records {
		if rec.Dead {
			t.Error("record should not be dead yet")
		}
		if rec.RetryCount == 0 {
			t.Error("expected RetryCount > 0 after failed retry")
		}
	}
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

	dead := sm.GetDeadActions()
	if len(dead) != 1 {
		t.Fatalf("expected 1 dead action, got %d", len(dead))
	}
}
