package controller

import (
	"context"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

func newTestWorker(t *testing.T, debounce time.Duration, syncFn func(context.Context) error) *syncWorker {
	t.Helper()
	return newSyncWorker(debounce, syncFn, slog.Default())
}

func TestSyncWorker_StopReturnsImmediatelyIfNotStarted(t *testing.T) {
	w := newTestWorker(t, 10*time.Millisecond, func(context.Context) error { return nil })
	done := make(chan struct{})
	go func() {
		w.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Stop() on never-started worker blocked for >100ms")
	}
}

func TestSyncWorker_FlushBeforeStartReturnsError(t *testing.T) {
	w := newTestWorker(t, 10*time.Millisecond, func(context.Context) error { return nil })
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	if err := w.FlushSync(ctx); err == nil {
		t.Fatal("expected FlushSync to error before Start, got nil")
	}
}

func TestSyncWorker_TriggersCoalesce(t *testing.T) {
	var callCount int32
	w := newTestWorker(t, 20*time.Millisecond, func(context.Context) error {
		atomic.AddInt32(&callCount, 1)
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)
	defer w.Stop()

	for i := 0; i < 10; i++ {
		w.TriggerSync()
	}
	// Give debounce + sync time to land. Flush will be added in Task 3;
	// here we sleep as a coarse check.
	time.Sleep(100 * time.Millisecond)

	if got := atomic.LoadInt32(&callCount); got != 1 {
		t.Errorf("expected 1 sync call (coalesced), got %d", got)
	}
}

func TestSyncWorker_DebounceHonored(t *testing.T) {
	var callCount int32
	w := newTestWorker(t, 50*time.Millisecond, func(context.Context) error {
		atomic.AddInt32(&callCount, 1)
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w.Start(ctx)
	defer w.Stop()

	w.TriggerSync()
	time.Sleep(20 * time.Millisecond) // less than debounce
	if got := atomic.LoadInt32(&callCount); got != 0 {
		t.Errorf("expected 0 calls during debounce window, got %d", got)
	}
	time.Sleep(80 * time.Millisecond) // past debounce
	if got := atomic.LoadInt32(&callCount); got != 1 {
		t.Errorf("expected 1 call after debounce elapsed, got %d", got)
	}
}

func TestSyncWorker_ShutdownClean(t *testing.T) {
	w := newTestWorker(t, 10*time.Millisecond, func(context.Context) error { return nil })
	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)

	done := make(chan struct{})
	go func() {
		w.Stop()
		close(done)
	}()
	cancel()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Stop() did not return within 500ms of ctx cancel")
	}
}

// keep atomic import used; will be referenced by later tests
var _ = atomic.Bool{}
