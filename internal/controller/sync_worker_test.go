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

// keep atomic import used; will be referenced by later tests
var _ = atomic.Bool{}
