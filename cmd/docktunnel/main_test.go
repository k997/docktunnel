package main

import (
	"sync/atomic"
	"testing"
)

// TestRunWithRecover_SwallowsPanic verifies the B7 helper: a panicking
// function is recovered, logged, and the caller continues.
func TestRunWithRecover_SwallowsPanic(t *testing.T) {
	var ran atomic.Bool

	// Must not propagate the panic.
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("runWithRecover leaked a panic: %v", r)
			}
		}()
		runWithRecover("test-panic", func() {
			panic("simulated goroutine panic")
		})
		ran.Store(true)
	}()

	if !ran.Load() {
		t.Error("code after runWithRecover should have executed")
	}
}

// TestRunWithRecover_RunsNormalFunction verifies the helper executes normal
// functions without interference.
func TestRunWithRecover_RunsNormalFunction(t *testing.T) {
	var ran atomic.Bool
	runWithRecover("test-normal", func() { ran.Store(true) })
	if !ran.Load() {
		t.Error("function passed to runWithRecover should have run")
	}
}

// TestShouldSkipCleanup verifies the B5 cleanup gating: cleanup runs only when
// onExit=true AND strategy=graceful-cleanup. Config validation may still
// accept force-cleanup/none, but main treats them as unknown → skip.
func TestShouldSkipCleanup(t *testing.T) {
	tests := []struct {
		name     string
		onExit   bool
		strategy string
		wantSkip bool
	}{
		{"onExit false skips even with graceful strategy", false, "graceful-cleanup", true},
		{"onExit true + graceful runs cleanup", true, "graceful-cleanup", false},
		{"fast-exit skips", true, "fast-exit", true},
		{"force-cleanup treated as unknown → skip", true, "force-cleanup", true},
		{"none treated as unknown → skip", true, "none", true},
		{"empty strategy skips", true, "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldSkipCleanup(tt.onExit, tt.strategy); got != tt.wantSkip {
				t.Errorf("shouldSkipCleanup(%v, %q) = %v, want %v",
					tt.onExit, tt.strategy, got, tt.wantSkip)
			}
		})
	}
}
