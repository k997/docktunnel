package state

import (
	"errors"
	"log/slog"
	"os"
	"testing"

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
