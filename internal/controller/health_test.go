package controller

import (
	"log/slog"
	"testing"
	"time"
)

func TestHealthTracker_isFlapping_NotFlappingByDefault(t *testing.T) {
	c := &Controller{
		containerHealth:   make(map[string]*ContainerHealth),
		flappingWindow:    time.Minute,
		flappingThreshold: 3,
		coolingPeriod:     time.Minute,
		maxCoolingPeriod:  5 * time.Minute,
	}
	h := newHealthTracker(c, slog.Default())

	if h.isFlapping("nonexistent") {
		t.Error("expected isFlapping=false for unknown container")
	}
}

func TestHealthTracker_updateContainerHealth_MarksFlappingAfterThreshold(t *testing.T) {
	c := &Controller{
		containerHealth:   make(map[string]*ContainerHealth),
		flappingWindow:    time.Minute,
		flappingThreshold: 3,
		coolingPeriod:     time.Minute,
		maxCoolingPeriod:  5 * time.Minute,
	}
	h := newHealthTracker(c, slog.Default())

	containerID := "test-container-1"
	// First two starts within the window don't trip the threshold.
	h.updateContainerHealth(containerID, true)
	h.updateContainerHealth(containerID, true)
	if h.isFlapping(containerID) {
		t.Error("expected isFlapping=false before threshold reached")
	}
	// Third start in the window crosses threshold (RestartCount=3).
	h.updateContainerHealth(containerID, true)
	if !h.isFlapping(containerID) {
		t.Error("expected isFlapping=true after threshold reached")
	}
}

func TestHealthTracker_newHealthTracker_ReturnsNonNil(t *testing.T) {
	c := &Controller{containerHealth: make(map[string]*ContainerHealth)}
	h := newHealthTracker(c, slog.Default())
	if h == nil {
		t.Fatal("expected non-nil healthTracker")
	}
	if h.c == nil {
		t.Error("expected healthTracker.c to be set")
	}
}
