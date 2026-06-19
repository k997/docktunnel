package controller

import (
	"log/slog"
	"sync"
	"time"
)

// ContainerHealth 记录容器的健康状态信息
type ContainerHealth struct {
	RestartCount int
	LastRestart  time.Time
	IsFlapping   bool
	CoolingUntil time.Time
}

// healthTracker detects container flapping and applies cooling periods.
// Owns its own mutex separate from Controller.mu to avoid lock-ordering
// constraints between handler-driven state mutations and health checks.
//
// Data fields (containerHealth, flappingWindow, etc.) remain on Controller
// because controller_test.go constructs &Controller{} literals that set
// them. Production code MUST access them through healthTracker methods,
// not directly.
type healthTracker struct {
	c   *Controller
	log *slog.Logger

	mu sync.Mutex
}

func newHealthTracker(c *Controller, log *slog.Logger) *healthTracker {
	return &healthTracker{c: c, log: log}
}

// isFlapping 检查容器是否处于抖动状态
func (h *healthTracker) isFlapping(containerID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	health, exists := h.c.containerHealth[containerID]
	if !exists {
		return false
	}

	if health.IsFlapping && time.Now().Before(health.CoolingUntil) {
		return true
	}

	return false
}

// updateContainerHealth 更新容器健康状态
func (h *healthTracker) updateContainerHealth(containerID string, isStartEvent bool) {
	h.mu.Lock()
	defer h.mu.Unlock()

	now := time.Now()

	health, exists := h.c.containerHealth[containerID]
	if !exists {
		health = &ContainerHealth{}
		h.c.containerHealth[containerID] = health
	}

	if isStartEvent {
		if now.Sub(health.LastRestart) <= h.c.flappingWindow {
			health.RestartCount++

			if health.RestartCount >= h.c.flappingThreshold {
				health.IsFlapping = true
				coolingMultiplier := 1 << uint(health.RestartCount-h.c.flappingThreshold)
				coolingDuration := min(time.Duration(coolingMultiplier)*h.c.coolingPeriod, h.c.maxCoolingPeriod)
				health.CoolingUntil = now.Add(coolingDuration)

				slog.Warn("Container marked as flapping",
					"containerID", containerID,
					"restartCount", health.RestartCount,
					"coolingUntil", health.CoolingUntil)
			}
		} else {
			health.RestartCount = 1
		}

		health.LastRestart = now
	} else {
		slog.Debug("Container stopped", "containerID", containerID)
	}

	if health.IsFlapping && now.After(health.CoolingUntil) {
		health.IsFlapping = false
		health.RestartCount = 0
		slog.Info("Container cooling period ended, flapping status reset", "containerID", containerID)
	}
}
