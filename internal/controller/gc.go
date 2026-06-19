package controller

import (
	"context"
	"fmt"
	"log/slog"

	"docktunnel/internal/metrics"
	"docktunnel/pkg/types"
)

// gc runs retention garbage collection. Stateless shim.
type gc struct {
	c   *Controller
	log *slog.Logger
}

func newGC(c *Controller, log *slog.Logger) *gc {
	return &gc{c: c, log: log}
}

// RunGarbageCollection runs garbage collection for expired retention policies (T062, T063)
func (g *gc) RunGarbageCollection(ctx context.Context) error {
	expiredEntries, err := g.c.stateManager.RunGC(ctx)
	if err != nil {
		return fmt.Errorf("state manager GC failed: %w", err)
	}

	if len(expiredEntries) == 0 {
		g.updateRetentionGauge()
		return nil
	}

	slog.Info("Garbage collection found expired entries", "count", len(expiredEntries))

	removed := 0
	g.c.mu.Lock()
	for _, entry := range expiredEntries {
		hostname := entry.Config.Hostname
		if hostname != "" {
			if _, exists := g.c.ingressRules[hostname]; exists {
				delete(g.c.ingressRules, hostname)
				removed++
				slog.Info("Removed expired route from ingress rules",
					"container_id", entry.ContainerID,
					"hostname", hostname)
			}
		}
	}
	g.c.mu.Unlock()

	if removed > 0 {
		metrics.AddGCDeletions(removed)
	}

	if err := g.c.syncToCloudflare(ctx); err != nil {
		return fmt.Errorf("failed to sync after GC: %w", err)
	}

	slog.Info("Garbage collection completed successfully",
		"expired_count", len(expiredEntries))

	g.updateRetentionGauge()

	return nil
}

// updateRetentionGauge counts entries by lifecycle status and updates
// the docktunnel_retention_entries gauge.
func (g *gc) updateRetentionGauge() {
	snapshot := g.c.stateManager.GetSnapshot()
	counts := map[string]int{
		"Active":        0,
		"Retaining":     0,
		"PendingDelete": 0,
	}
	for _, entry := range snapshot.ActiveTunnels {
		if entry.Status == types.StatusActive {
			counts["Active"]++
		}
	}
	for _, entry := range snapshot.PendingDeletions {
		switch entry.Status {
		case types.StatusRetaining:
			counts["Retaining"]++
		case types.StatusPendingDelete:
			counts["PendingDelete"]++
		}
	}
	for status, n := range counts {
		metrics.SetRetentionEntries(status, n)
	}
}
