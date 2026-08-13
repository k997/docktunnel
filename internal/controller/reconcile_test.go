package controller

import (
	"context"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"docktunnel/internal/events"

	"github.com/cloudflare/cloudflare-go/v5/zero_trust"
)

// countingSyncWorker builds a started syncWorker whose syncFn increments the
// counter and returns nil. FlushSync is used to wait for triggered cycles.
func countingSyncWorker(t *testing.T, calls *atomic.Int32) (*syncWorker, context.Context, context.CancelFunc) {
	t.Helper()
	w := newSyncWorker(5*time.Millisecond, func(context.Context) error {
		calls.Add(1)
		return nil
	}, slog.Default())
	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)
	t.Cleanup(func() {
		cancel()
		w.Stop()
	})
	return w, ctx, cancel
}

// TestReconcile_TriggersSyncOnCFDrift verifies the three-way diff: when the
// live Cloudflare config differs from the desired state, Reconcile triggers a
// sync (B2).
func TestReconcile_TriggersSyncOnCFDrift(t *testing.T) {
	scanner := &mockDockerScanner{events: []events.Event{{
		Type:        "start",
		ContainerID: "c1",
		ContainerInfo: testContainerInfo(map[string]string{
			"docktunnel.web.hostname": "app.example.com",
			"docktunnel.web.service":  "http://origin:8080",
		}),
	}}}
	mgr := &mockCloudflareManager{
		tunnel:          &zero_trust.TunnelCloudflaredGetResponse{ID: "t1"},
		getConfigResult: []zero_trust.TunnelCloudflaredConfigurationGetResponseConfigIngress{}, // CF has nothing
	}
	ctrl := NewController(scanner, mgr, ControllerOptions{DebounceDuration: 5 * time.Millisecond})

	var syncCalls atomic.Int32
	w, _, _ := countingSyncWorker(t, &syncCalls)
	ctrl.syncWorker = w

	if err := ctrl.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	// Desired has app.example.com, CF has nothing → drift → TriggerSync.
	// Give the 5ms debounce time to fire.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && syncCalls.Load() == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if got := syncCalls.Load(); got == 0 {
		t.Error("Reconcile should have triggered a sync on CF drift")
	}
}

// TestReconcile_NoSyncWhenCFMatchesDesired verifies that no sync is triggered
// when the live config already matches the desired state.
func TestReconcile_NoSyncWhenCFMatchesDesired(t *testing.T) {
	scanner := &mockDockerScanner{events: []events.Event{{
		Type:        "start",
		ContainerID: "c1",
		ContainerInfo: testContainerInfo(map[string]string{
			"docktunnel.web.hostname": "app.example.com",
			"docktunnel.web.service":  "http://origin:8080",
		}),
	}}}
	mgr := &mockCloudflareManager{
		tunnel: &zero_trust.TunnelCloudflaredGetResponse{ID: "t1"},
		getConfigResult: []zero_trust.TunnelCloudflaredConfigurationGetResponseConfigIngress{
			{Hostname: "app.example.com", Service: "http://origin:8080"},
		},
	}
	ctrl := NewController(scanner, mgr, ControllerOptions{DebounceDuration: 5 * time.Millisecond})

	var syncCalls atomic.Int32
	w, _, _ := countingSyncWorker(t, &syncCalls)
	ctrl.syncWorker = w

	if err := ctrl.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	time.Sleep(50 * time.Millisecond) // well past the debounce
	if got := syncCalls.Load(); got != 0 {
		t.Errorf("Reconcile should not trigger a sync when CF matches, got %d", got)
	}
}

// TestReconcile_SkipsRoundOnGetConfigurationFailure verifies that a failed
// GetConfiguration logs and skips the CF diff (B2) without panicking.
func TestReconcile_SkipsRoundOnGetConfigurationFailure(t *testing.T) {
	scanner := &mockDockerScanner{events: []events.Event{{
		Type:        "start",
		ContainerID: "c1",
		ContainerInfo: testContainerInfo(map[string]string{
			"docktunnel.web.hostname": "app.example.com",
			"docktunnel.web.service":  "http://origin:8080",
		}),
	}}}
	mgr := &mockCloudflareManager{
		tunnel:          &zero_trust.TunnelCloudflaredGetResponse{ID: "t1"},
		getConfigErr:    context.DeadlineExceeded,
		getConfigResult: nil,
	}
	ctrl := NewController(scanner, mgr, ControllerOptions{DebounceDuration: 5 * time.Millisecond})

	var syncCalls atomic.Int32
	w, _, _ := countingSyncWorker(t, &syncCalls)
	ctrl.syncWorker = w

	if err := ctrl.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile should skip the round on GetConfiguration failure, got error: %v", err)
	}

	// In-memory desired should still have been updated (old convergence path).
	ctrl.mu.RLock()
	_, exists := ctrl.ingressRules[ingressKey("app.example.com", "")]
	ctrl.mu.RUnlock()
	if !exists {
		t.Error("in-memory desired state should be updated even when GetConfiguration fails")
	}

	time.Sleep(50 * time.Millisecond)
	if got := syncCalls.Load(); got != 0 {
		t.Errorf("no sync should be triggered when the CF diff is skipped, got %d", got)
	}
}

// TestReconcile_SkipsCoolingContainer verifies periodic reconciliation does
// not re-register routes of flapping containers in their cooling period (B11).
func TestReconcile_SkipsCoolingContainer(t *testing.T) {
	scanner := &mockDockerScanner{events: []events.Event{{
		Type:        "start",
		ContainerID: "c-cooling",
		ContainerInfo: testContainerInfo(map[string]string{
			"docktunnel.web.hostname": "cooling.example.com",
			"docktunnel.web.service":  "http://origin:8080",
		}),
	}}}
	mgr := &mockCloudflareManager{
		tunnel: &zero_trust.TunnelCloudflaredGetResponse{ID: "t1"},
	}
	ctrl := NewController(scanner, mgr, ControllerOptions{DebounceDuration: 5 * time.Millisecond})

	// Container is in its cooling period.
	ctrl.containerHealth["c-cooling"] = &ContainerHealth{
		IsFlapping:   true,
		CoolingUntil: time.Now().Add(time.Hour),
	}

	var syncCalls atomic.Int32
	w, _, _ := countingSyncWorker(t, &syncCalls)
	ctrl.syncWorker = w

	if err := ctrl.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	ctrl.mu.RLock()
	_, exists := ctrl.ingressRules[ingressKey("cooling.example.com", "")]
	ctrl.mu.RUnlock()
	if exists {
		t.Error("cooling container's route must not be re-added by Reconcile")
	}
}
