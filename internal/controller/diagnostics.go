package controller

import (
	"log/slog"
	"time"

	"docktunnel/internal/diagnostics"
)

// diagnosticsHelper serves the /debug/state endpoint. Stateless; reads
// from Controller's desired state (under c.mu) and the actual-state
// cache owned by syncer.
type diagnosticsHelper struct {
	c *Controller
}

func newDiagnosticsHelper(c *Controller) *diagnosticsHelper {
	return &diagnosticsHelper{c: c}
}

// GetDebugState returns the current desired vs actual state for the /debug/state endpoint.
func (d *diagnosticsHelper) GetDebugState() diagnostics.DebugStateResponse {
	// Lazy-init for &Controller{} test literals. Controller delegators
	// (Sync/GetIngressRules/etc.) use the same guard; either path is safe.
	if d.c.syncer == nil {
		d.c.syncer = newSyncer(d.c, slog.Default())
	}

	d.c.mu.RLock()
	desired := d.snapshotRuleViewsLocked()
	d.c.mu.RUnlock()

	d.c.syncer.actualStateMu.RLock()
	defer d.c.syncer.actualStateMu.RUnlock()

	actual := d.c.syncer.lastKnownActualRules
	source := "empty"
	if actual != nil {
		source = "live_cache"
	}

	return diagnostics.DebugStateResponse{
		Timestamp:    time.Now(),
		DesiredState: desired,
		ActualState:  actual,
		Source:       source,
		Diff:         diagnostics.ComputeDiff(desired, actual),
	}
}

// snapshotRuleViewsLocked builds a slice of RuleView from c.ingressRules.
// Caller must hold c.mu (read or write).
func (d *diagnosticsHelper) snapshotRuleViewsLocked() []diagnostics.RuleView {
	views := make([]diagnostics.RuleView, 0, len(d.c.ingressRules))
	for _, rule := range d.c.ingressRules {
		views = append(views, diagnostics.RuleView{
			Hostname: rule.Hostname.Value,
			Service:  rule.Service.Value,
			Path:     rule.Path.Value,
		})
	}
	return views
}
