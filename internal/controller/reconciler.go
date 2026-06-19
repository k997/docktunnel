package controller

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"docktunnel/internal/label"

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

// Reconcile computes desired state from running containers plus retaining entries,
// diffs against current state, and syncs only on drift.
func (r *reconciler) Reconcile(ctx context.Context) error {
	if r.c.dockerManager == nil {
		return nil
	}

	tunnel := r.c.cloudflareManager.GetTunnel()
	if tunnel == nil {
		return fmt.Errorf("tunnel is not available")
	}

	eventsList, err := r.c.dockerManager.ScanRunningContainers(ctx)
	if err != nil {
		return fmt.Errorf("reconcile scan failed: %w", err)
	}

	desiredRules := make(map[string]zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress)
	desiredContainerRules := make(map[string][]string)

	for _, event := range eventsList {
		if !r.c.isDocktunnelEnabled(event) {
			continue
		}
		parsedRules, err := label.Parse(event.ContainerInfo)
		if err != nil {
			slog.Error("Failed to parse labels during reconcile",
				"action", "parse_labels",
				"result", "failure",
				"containerID", event.ContainerID,
				"error", err)
			continue
		}

		var hostnames []string
		for _, rule := range parsedRules {
			if rule.Hostname.Value != "" {
				desiredRules[rule.Hostname.Value] = *rule
				hostnames = append(hostnames, rule.Hostname.Value)
			}
		}
		if len(hostnames) > 0 {
			desiredContainerRules[event.ContainerID] = hostnames
		}
	}

	// Preserve retaining rules (in ingressRules but not in containerRules)
	r.c.mu.RLock()
	currentContainerHostnames := make(map[string]bool)
	for _, hostnames := range r.c.containerRules {
		for _, h := range hostnames {
			currentContainerHostnames[h] = true
		}
	}
	for hostname, rule := range r.c.ingressRules {
		if !currentContainerHostnames[hostname] {
			if _, inDesired := desiredRules[hostname]; !inDesired {
				desiredRules[hostname] = rule
			}
		}
	}
	r.c.mu.RUnlock()

	// Diff desired vs current
	r.c.mu.RLock()
	hasDiff := len(desiredRules) != len(r.c.ingressRules)
	if !hasDiff {
		for hostname, desiredRule := range desiredRules {
			existing, exists := r.c.ingressRules[hostname]
			if !exists || existing.Service.Value != desiredRule.Service.Value {
				hasDiff = true
				break
			}
		}
	}
	r.c.mu.RUnlock()

	if !hasDiff {
		slog.Debug("Reconcile: no drift detected")
		return nil
	}

	slog.Info("Reconcile: drift detected, syncing",
		"current_rules", len(r.c.ingressRules),
		"desired_rules", len(desiredRules))

	r.c.mu.Lock()
	r.c.ingressRules = desiredRules
	r.c.containerRules = desiredContainerRules
	r.c.mu.Unlock()

	if err := r.c.syncToCloudflare(ctx); err != nil {
		return err
	}

	r.c.refreshActualState(ctx)

	return nil
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
