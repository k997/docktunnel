package controller

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"docktunnel/pkg/types"

	"github.com/cloudflare/cloudflare-go/v5/zero_trust"
)

var errSyncBoom = errors.New("sync boom")

func TestSyncWorker_OnErrorCalledOnSyncFailure(t *testing.T) {
	var calls atomic.Int32
	w := newSyncWorker(5*time.Millisecond, func(context.Context) error {
		return errSyncBoom
	}, slog.Default())
	w.SetOnError(func(err error) {
		if errors.Is(err, errSyncBoom) {
			calls.Add(1)
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)
	defer func() { cancel(); w.Stop() }()

	w.TriggerSync()
	if err := w.FlushSync(ctx); err == nil {
		t.Fatal("expected FlushSync to surface the sync error")
	}

	if got := calls.Load(); got != 1 {
		t.Errorf("expected onError to be called once, got %d", got)
	}
}

func TestSyncWorker_OnErrorNotCalledOnSuccess(t *testing.T) {
	var calls atomic.Int32
	w := newSyncWorker(5*time.Millisecond, func(context.Context) error { return nil }, slog.Default())
	w.SetOnError(func(error) { calls.Add(1) })

	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)
	defer func() { cancel(); w.Stop() }()

	w.TriggerSync()
	if err := w.FlushSync(ctx); err != nil {
		t.Fatalf("unexpected sync error: %v", err)
	}
	if got := calls.Load(); got != 0 {
		t.Errorf("onError must not fire on success, got %d calls", got)
	}
}

// TestSyncFailure_EnqueuesActionSync wires the worker's onError callback
// (installed by NewController) to the compensation queue: a failing
// UpdateConfiguration must produce exactly one ActionSync record (B3).
func TestSyncFailure_EnqueuesActionSync(t *testing.T) {
	mgr := &mockCloudflareManager{
		tunnel:    &zero_trust.TunnelCloudflaredGetResponse{ID: "t1"},
		updateErr: errSyncBoom,
	}
	ctrl := NewController(nil, mgr, ControllerOptions{DebounceDuration: 5 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	ctrl.Start(ctx)
	defer func() { cancel(); ctrl.StopSyncWorker() }()

	// Trigger twice; the second trigger fires after the first failure, so the
	// dedup check must prevent a second ActionSync record.
	ctrl.syncWorker.TriggerSync()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !ctrl.stateManager.HasPendingActionKind(types.ActionSync) {
		time.Sleep(10 * time.Millisecond)
	}
	if !ctrl.stateManager.HasPendingActionKind(types.ActionSync) {
		t.Fatal("expected an ActionSync compensation record after sync failure")
	}

	ctrl.syncWorker.TriggerSync()
	time.Sleep(50 * time.Millisecond)

	pending := ctrl.stateManager.GetAllPendingActions()
	count := 0
	for _, rec := range pending {
		if rec.Action.Kind == types.ActionSync {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly 1 ActionSync record (deduplicated), got %d", count)
	}
}

// TestCompensation_ExecuteActionSync verifies the compensation executor can
// retry a full sync via FlushSync and surfaces sync errors (B3).
func TestCompensation_ExecuteActionSync(t *testing.T) {
	var syncErr error
	ctrl := NewController(nil, &mockCloudflareManager{}, ControllerOptions{DebounceDuration: 5 * time.Millisecond})
	ctrl.syncWorker = newSyncWorker(5*time.Millisecond, func(context.Context) error {
		return syncErr
	}, slog.Default())

	ctx, cancel := context.WithCancel(context.Background())
	ctrl.Start(ctx)
	defer func() { cancel(); ctrl.StopSyncWorker() }()

	// Success path.
	syncErr = nil
	if err := ctrl.ExecuteAction(ctx, types.Action{Kind: types.ActionSync}); err != nil {
		t.Fatalf("ActionSync should succeed when sync succeeds: %v", err)
	}

	// Failure path — FlushSync must surface the worker error so the record is
	// retried.
	syncErr = errSyncBoom
	if err := ctrl.ExecuteAction(ctx, types.Action{Kind: types.ActionSync}); !errors.Is(err, errSyncBoom) {
		t.Errorf("expected sync error from ExecuteAction(ActionSync), got %v", err)
	}
}
