package controller

import (
	"context"
	"errors"
	"log/slog"
	"sync"
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
	w.Start(ctx)
	defer w.Stop()
	defer cancel()

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
	w.Start(ctx)
	defer w.Stop()
	defer cancel()

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

func TestSyncWorker_FlushSyncBlocks(t *testing.T) {
	syncStarted := make(chan struct{})
	syncProceed := make(chan struct{})
	var startedOnce sync.Once
	w := newTestWorker(t, 5*time.Millisecond, func(context.Context) error {
		startedOnce.Do(func() { close(syncStarted) })
		<-syncProceed
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)
	defer w.Stop()
	defer cancel()

	w.TriggerSync()
	<-syncStarted // wait for syncFn to be in flight

	flushDone := make(chan struct{})
	go func() {
		_ = w.FlushSync(ctx)
		close(flushDone)
	}()

	select {
	case <-flushDone:
		t.Fatal("FlushSync returned before syncFn finished")
	case <-time.After(20 * time.Millisecond):
		// expected: FlushSync is blocking
	}

	close(syncProceed) // release syncFn
	select {
	case <-flushDone:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("FlushSync did not return within 500ms after syncFn finished")
	}
}

func TestSyncWorker_FlushSyncReturnsSyncErr(t *testing.T) {
	sentinel := errors.New("boom")
	w := newTestWorker(t, 5*time.Millisecond, func(context.Context) error {
		return sentinel
	})
	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)
	defer w.Stop()
	defer cancel()

	w.TriggerSync()
	err := w.FlushSync(ctx)
	if !errors.Is(err, sentinel) {
		t.Errorf("expected FlushSync to return sentinel error, got %v", err)
	}
}

func TestSyncWorker_FlushSyncCtxTimeout(t *testing.T) {
	proceed := make(chan struct{})
	w := newTestWorker(t, 5*time.Millisecond, func(context.Context) error {
		<-proceed
		return nil
	})
	bg, bgCancel := context.WithCancel(context.Background())
	w.Start(bg)
	defer w.Stop()
	defer bgCancel()

	w.TriggerSync()
	time.Sleep(20 * time.Millisecond) // let syncFn block on proceed

	flushCtx, flushCancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer flushCancel()
	err := w.FlushSync(flushCtx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected DeadlineExceeded, got %v", err)
	}
	close(proceed)
}

// TestSyncWorker_FlushSyncNoHangOnSlowScheduler is a regression test for
// the trigger-then-register race that was fixed by merging trigger and
// flusher-registration into a single channel send. Before the fix,
// yielding between the two sends would cause ~18% of FlushSync calls
// to hang until ctx timeout.
func TestSyncWorker_FlushSyncNoHangOnSlowScheduler(t *testing.T) {
	var callCount int32
	w := newTestWorker(t, 5*time.Millisecond, func(context.Context) error {
		atomic.AddInt32(&callCount, 1)
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)
	defer w.Stop()
	defer cancel()

	// Hammer FlushSync with no other trigger source. If the race
	// exists, many of these will hang until ctx timeout.
	for i := 0; i < 50; i++ {
		flushCtx, flushCancel := context.WithTimeout(ctx, 500*time.Millisecond)
		if err := w.FlushSync(flushCtx); err != nil {
			flushCancel()
			t.Fatalf("FlushSync call %d returned err: %v (race regression?)", i, err)
		}
		flushCancel()
	}

	if got := atomic.LoadInt32(&callCount); got < 50 {
		t.Errorf("expected at least 50 syncFn calls (one per flush), got %d", got)
	}
}

func TestSyncWorker_PanicRecovery(t *testing.T) {
	var callCount int32
	w := newTestWorker(t, 5*time.Millisecond, func(context.Context) error {
		n := atomic.AddInt32(&callCount, 1)
		if n == 1 {
			panic("simulated SDK panic")
		}
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)
	defer w.Stop()
	defer cancel()

	w.TriggerSync()
	if err := w.FlushSync(ctx); err == nil {
		t.Errorf("expected FlushSync to surface panic-wrapped error, got nil")
	}

	// Worker should still be alive.
	w.TriggerSync()
	if err := w.FlushSync(ctx); err != nil {
		t.Errorf("expected second sync to succeed, got %v", err)
	}
	if got := atomic.LoadInt32(&callCount); got != 2 {
		t.Errorf("expected 2 syncFn calls (panic + recovery), got %d", got)
	}
}

func TestSyncWorker_ReTriggerAfterSync(t *testing.T) {
	var callCount int32
	firstStarted := make(chan struct{})
	firstProceed := make(chan struct{})
	w := newTestWorker(t, 5*time.Millisecond, func(context.Context) error {
		n := atomic.AddInt32(&callCount, 1)
		if n == 1 {
			close(firstStarted)
			<-firstProceed
		}
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	w.Start(ctx)
	defer w.Stop()
	defer cancel()

	w.TriggerSync()
	<-firstStarted

	// While syncFn #1 is blocked, queue another trigger.
	w.TriggerSync()

	close(firstProceed) // release #1

	// FlushSync should wait for #2 to also complete.
	if err := w.FlushSync(ctx); err != nil {
		t.Fatalf("FlushSync returned err: %v", err)
	}
	if got := atomic.LoadInt32(&callCount); got != 2 {
		t.Errorf("expected 2 syncFn calls (re-trigger), got %d", got)
	}
}

// keep atomic import used; will be referenced by later tests
var _ = atomic.Bool{}
