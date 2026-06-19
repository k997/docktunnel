package controller

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"
)

// errWorkerNotStarted is returned by FlushSync when called before Start.
var errWorkerNotStarted = errors.New("sync worker not started")

// syncWorker serializes Cloudflare writes via a single goroutine. All
// callers funnel through TriggerSync (async, debounced) or FlushSync
// (sync, waits for at least one sync cycle to complete).
type syncWorker struct {
	debounce time.Duration
	syncFn   func(context.Context) error
	log      *slog.Logger

	triggerCh  chan struct{}      // size 1; signals "sync wanted"
	flushQueue chan chan struct{} // unbuffered; FlushSync registers its done chan
	stopCh     chan struct{}      // closed when run() exits

	started atomic.Bool
}

func newSyncWorker(debounce time.Duration, syncFn func(context.Context) error, log *slog.Logger) *syncWorker {
	return &syncWorker{
		debounce:   debounce,
		syncFn:     syncFn,
		log:        log,
		triggerCh:  make(chan struct{}, 1),
		flushQueue: make(chan chan struct{}),
		stopCh:     make(chan struct{}),
	}
}

// Start launches the worker goroutine. Idempotent.
func (w *syncWorker) Start(ctx context.Context) {
	if !w.started.CompareAndSwap(false, true) {
		return
	}
	go w.run(ctx)
}

// TriggerSync signals that a sync is desired. Non-blocking; safe to
// call from event handlers. Multiple triggers coalesce.
func (w *syncWorker) TriggerSync() {
	select {
	case w.triggerCh <- struct{}{}:
	default:
		// channel full; worker will re-check after current sync
	}
}

// FlushSync triggers a sync and blocks until the worker has processed
// at least one sync cycle. Returns the last sync error (or nil), or
// ctx.Err() on timeout/cancel.
func (w *syncWorker) FlushSync(ctx context.Context) error {
	if !w.started.Load() {
		return errWorkerNotStarted
	}
	// Will be filled in by Task 3 — for now, return not-implemented.
	return errors.New("FlushSync not implemented yet")
}

// Stop blocks until the worker goroutine has exited. Returns
// immediately if Start was never called.
func (w *syncWorker) Stop() {
	if !w.started.Load() {
		return
	}
	select {
	case <-w.stopCh:
	case <-time.After(5 * time.Second):
		w.log.Error("syncWorker.Stop timed out after 5s")
	}
}

// run is filled in by Task 2.
func (w *syncWorker) run(ctx context.Context) {
	defer close(w.stopCh)
	<-ctx.Done()
}

// (runOnce will be added in Task 2; fmt-safe placeholder kept here to
// avoid unused import errors during this slice of work.)
var _ = fmt.Sprintf
