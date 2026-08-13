package controller

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/cloudflare/cloudflare-go/v5/zero_trust"
)

// reconciler drifts current state against desired state on a periodic
// ticker. Stateless; operates on Controller's maps under c.mu.
type reconciler struct {
	c   *Controller
	log *slog.Logger
}

func newReconciler(c *Controller, log *slog.Logger) *reconciler {
	return &reconciler{c: c, log: log}
}

// handleResync performs a full state synchronization after Docker daemon reconnection.
func (r *reconciler) handleResync(ctx context.Context) error {
	r.log.Info("Handling resync event after Docker reconnection")
	return r.c.Sync(ctx)
}

// ruleTriple is the normalized (hostname, path, service) projection of an
// ingress rule used for drift comparison.
type ruleTriple struct {
	hostname string
	path     string
	service  string
}

// Reconcile computes desired state from running containers plus retaining
// entries (via buildDesiredRules), diffs against both the in-memory state and
// the live Cloudflare configuration, and triggers a sync only on drift (B2).
func (r *reconciler) Reconcile(ctx context.Context) error {
	if r.c.dockerManager == nil {
		return nil
	}

	tunnel := r.c.cloudflareManager.GetTunnel()
	if tunnel == nil {
		return fmt.Errorf("tunnel is not available")
	}

	// skipCooling=true: containers in a flapping cooling period are excluded
	// so periodic reconciliation cannot hollow out the cooling logic (B11).
	desired, err := r.c.buildDesiredRules(ctx, true)
	if err != nil {
		return fmt.Errorf("reconcile scan failed: %w", err)
	}

	// Diff desired vs in-memory current state; update in-memory maps on
	// drift (same convergence path as before, but retaining rules now come
	// from persisted state instead of an in-memory heuristic).
	r.c.mu.RLock()
	hasDiff := !rulesEqual(desired.rules, r.c.ingressRules)
	r.c.mu.RUnlock()

	if hasDiff {
		r.log.Info("Reconcile: in-memory drift detected, updating desired state",
			"current_rules", len(r.c.ingressRules),
			"desired_rules", len(desired.rules))
		r.c.mu.Lock()
		r.c.ingressRules = desired.rules
		r.c.containerRules = desired.containerHostnames
		r.c.mu.Unlock()
	}

	// Three-way diff: compare normalized desired state against the live
	// Cloudflare configuration (B2). On GetConfiguration failure, log and
	// skip this round — the in-memory convergence path above still runs.
	actual, err := r.c.cloudflareManager.GetConfiguration(ctx)
	if err != nil {
		r.log.Error("Failed to fetch live tunnel config for reconcile",
			"error", err)
		return nil
	}

	normalizedDesired := normalizeDesiredRules(desired.rules)
	normalizedActual := normalizeActualRules(actual)
	if ruleTriplesEqual(normalizedDesired, normalizedActual) {
		r.log.Debug("Reconcile: no drift vs Cloudflare")
		return nil
	}

	r.log.Debug("Reconcile: drift vs Cloudflare detected, triggering sync",
		"desired_count", len(normalizedDesired),
		"actual_count", len(normalizedActual),
		"desired_only", diffTriples(normalizedDesired, normalizedActual),
		"actual_only", diffTriples(normalizedActual, normalizedDesired))

	if r.c.syncWorker != nil {
		r.c.syncWorker.TriggerSync()
	}
	r.c.refreshActualState(ctx)

	return nil
}

// rulesEqual reports whether two desired-rule maps (keyed by ingressKey)
// describe the same routes. Keys already encode (hostname, path); services
// are compared for drift.
func rulesEqual(a, b map[string]zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress) bool {
	if len(a) != len(b) {
		return false
	}
	for key, ruleA := range a {
		ruleB, exists := b[key]
		if !exists || ruleA.Service.Value != ruleB.Service.Value {
			return false
		}
	}
	return true
}

// normalizeDesiredRules projects the desired rule map into a sorted list of
// (hostname, path, service) triples, dropping entries without a hostname.
func normalizeDesiredRules(rules map[string]zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress) []ruleTriple {
	var out []ruleTriple
	for _, rule := range rules {
		if rule.Hostname.Value == "" {
			continue
		}
		out = append(out, ruleTriple{
			hostname: rule.Hostname.Value,
			path:     rule.Path.Value,
			service:  rule.Service.Value,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].hostname != out[j].hostname {
			return out[i].hostname < out[j].hostname
		}
		if out[i].path != out[j].path {
			return out[i].path < out[j].path
		}
		return out[i].service < out[j].service
	})
	return out
}

// normalizeActualRules projects the live Cloudflare config into a sorted list
// of (hostname, path, service) triples, ignoring catch-all rules (empty
// hostname or http_status:404 service) as they are always present.
func normalizeActualRules(rules []zero_trust.TunnelCloudflaredConfigurationGetResponseConfigIngress) []ruleTriple {
	var out []ruleTriple
	for _, rule := range rules {
		if rule.Hostname == "" || rule.Service == "http_status:404" {
			continue
		}
		out = append(out, ruleTriple{
			hostname: rule.Hostname,
			path:     rule.Path,
			service:  rule.Service,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].hostname != out[j].hostname {
			return out[i].hostname < out[j].hostname
		}
		if out[i].path != out[j].path {
			return out[i].path < out[j].path
		}
		return out[i].service < out[j].service
	})
	return out
}

func ruleTriplesEqual(a, b []ruleTriple) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// diffTriples returns the elements of a not present in b (by triple value).
func diffTriples(a, b []ruleTriple) []string {
	inB := make(map[ruleTriple]bool, len(b))
	for _, t := range b {
		inB[t] = true
	}
	var out []string
	for _, t := range a {
		if !inB[t] {
			out = append(out, t.hostname+t.path)
		}
	}
	return out
}

// ReconcileEnabled returns whether periodic reconciliation is enabled.
func (r *reconciler) ReconcileEnabled() bool {
	r.c.mu.RLock()
	defer r.c.mu.RUnlock()
	return r.c.reconcileEnabled
}

// ReconcileInterval returns the reconciliation interval.
func (r *reconciler) ReconcileInterval() time.Duration {
	r.c.mu.RLock()
	defer r.c.mu.RUnlock()
	return r.c.reconcileInterval
}
