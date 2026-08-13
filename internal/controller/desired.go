package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"docktunnel/internal/events"
	"docktunnel/internal/label"
	"docktunnel/pkg/types"

	"github.com/cloudflare/cloudflare-go/v5"
	"github.com/cloudflare/cloudflare-go/v5/zero_trust"
)

// ingressKey builds the storage key for an ingress rule from its hostname and
// path. The NUL separator is safe because neither DNS hostnames nor URL paths
// can contain NUL. Moving from hostname-only keys to (hostname, path) keys
// lets one hostname expose multiple paths (B10).
func ingressKey(hostname, path string) string {
	return hostname + "\x00" + path
}

// hostnameFromKey extracts the hostname part of an ingressKey.
func hostnameFromKey(key string) string {
	idx := strings.IndexByte(key, '\x00')
	if idx == -1 {
		return key
	}
	return key[:idx]
}

// setIngressRuleLocked stores a rule under its (hostname, path) key.
// Caller must hold c.mu (write).
func setIngressRuleLocked(c *Controller, rule zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress) {
	c.ingressRules[ingressKey(rule.Hostname.Value, rule.Path.Value)] = rule
}

// deleteIngressByHostnameLocked removes every ingress rule for the given
// hostname (all paths). Caller must hold c.mu (write).
func deleteIngressByHostnameLocked(c *Controller, hostname string) {
	for key := range c.ingressRules {
		if hostnameFromKey(key) == hostname {
			delete(c.ingressRules, key)
		}
	}
}

// hostnameRegisteredLocked reports whether any ingress rule exists for the
// given hostname. Caller must hold c.mu (read or write).
func hostnameRegisteredLocked(c *Controller, hostname string) bool {
	for key := range c.ingressRules {
		if hostnameFromKey(key) == hostname {
			return true
		}
	}
	return false
}

// persistedIngressRule is the JSON-serializable mirror of a Cloudflare
// ingress rule (zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress)
// persisted in TunnelEntry.Config.RuleJSON. The SDK struct wraps every field
// in param.Field without UnmarshalJSON, so it cannot round-trip through
// encoding/json directly (json.Unmarshal fails on param.Field); this mirror
// is the stable persistence format (B1). Its JSON shape matches the SDK
// MarshalJSON output for hostname/service/path, so both directions parse.
type persistedIngressRule struct {
	Hostname      string                  `json:"hostname"`
	Service       string                  `json:"service"`
	Path          string                  `json:"path,omitempty"`
	OriginRequest *persistedOriginRequest `json:"originRequest,omitempty"`
}

type persistedOriginRequest struct {
	Access                 *persistedAccess `json:"access,omitempty"`
	CAPool                 string           `json:"caPool,omitempty"`
	ConnectTimeout         int64            `json:"connectTimeout,omitempty"`
	DisableChunkedEncoding bool             `json:"disableChunkedEncoding,omitempty"`
	HTTP2Origin            bool             `json:"http2Origin,omitempty"`
	HTTPHostHeader         string           `json:"httpHostHeader,omitempty"`
	KeepAliveConnections   int64            `json:"keepAliveConnections,omitempty"`
	KeepAliveTimeout       int64            `json:"keepAliveTimeout,omitempty"`
	NoHappyEyeballs        bool             `json:"noHappyEyeballs,omitempty"`
	NoTLSVerify            bool             `json:"noTLSVerify,omitempty"`
	OriginServerName       string           `json:"originServerName,omitempty"`
	ProxyType              string           `json:"proxyType,omitempty"`
	TCPKeepAlive           int64            `json:"tcpKeepAlive,omitempty"`
	TLSTimeout             int64            `json:"tlsTimeout,omitempty"`
}

type persistedAccess struct {
	Required bool     `json:"required"`
	TeamName string   `json:"teamName,omitempty"`
	AUDTag   []string `json:"audTag,omitempty"`
}

// marshalIngressRule serializes a Cloudflare ingress rule for persistence
// (B1). Never fails for the fields the label parser can produce.
func marshalIngressRule(rule *zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress) ([]byte, error) {
	p := persistedIngressRule{
		Hostname: rule.Hostname.Value,
		Service:  rule.Service.Value,
		Path:     rule.Path.Value,
	}
	if rule.OriginRequest.Present {
		or := rule.OriginRequest.Value
		p.OriginRequest = &persistedOriginRequest{
			CAPool:                 or.CAPool.Value,
			ConnectTimeout:         or.ConnectTimeout.Value,
			DisableChunkedEncoding: or.DisableChunkedEncoding.Value,
			HTTP2Origin:            or.HTTP2Origin.Value,
			HTTPHostHeader:         or.HTTPHostHeader.Value,
			KeepAliveConnections:   or.KeepAliveConnections.Value,
			KeepAliveTimeout:       or.KeepAliveTimeout.Value,
			NoHappyEyeballs:        or.NoHappyEyeballs.Value,
			NoTLSVerify:            or.NoTLSVerify.Value,
			OriginServerName:       or.OriginServerName.Value,
			ProxyType:              or.ProxyType.Value,
			TCPKeepAlive:           or.TCPKeepAlive.Value,
			TLSTimeout:             or.TLSTimeout.Value,
		}
		if or.Access.Present {
			acc := or.Access.Value
			p.OriginRequest.Access = &persistedAccess{
				Required: acc.Required.Value,
				TeamName: acc.TeamName.Value,
				AUDTag:   acc.AUDTag.Value,
			}
		}
	}
	return json.Marshal(p)
}

// unmarshalIngressRule reconstructs a Cloudflare ingress rule from persisted
// RuleJSON. Returns an error when the data is empty or corrupt.
func unmarshalIngressRule(data []byte) (*zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty persisted rule")
	}
	var p persistedIngressRule
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("failed to unmarshal persisted rule: %w", err)
	}
	rule := &zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{
		Hostname: cloudflare.F(p.Hostname),
		Service:  cloudflare.F(p.Service),
	}
	if p.Path != "" {
		rule.Path = cloudflare.F(p.Path)
	}
	if p.OriginRequest != nil {
		or := zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngressOriginRequest{
			CAPool:                 cloudflare.F(p.OriginRequest.CAPool),
			ConnectTimeout:         cloudflare.F(p.OriginRequest.ConnectTimeout),
			DisableChunkedEncoding: cloudflare.F(p.OriginRequest.DisableChunkedEncoding),
			HTTP2Origin:            cloudflare.F(p.OriginRequest.HTTP2Origin),
			HTTPHostHeader:         cloudflare.F(p.OriginRequest.HTTPHostHeader),
			KeepAliveConnections:   cloudflare.F(p.OriginRequest.KeepAliveConnections),
			KeepAliveTimeout:       cloudflare.F(p.OriginRequest.KeepAliveTimeout),
			NoHappyEyeballs:        cloudflare.F(p.OriginRequest.NoHappyEyeballs),
			NoTLSVerify:            cloudflare.F(p.OriginRequest.NoTLSVerify),
			OriginServerName:       cloudflare.F(p.OriginRequest.OriginServerName),
			ProxyType:              cloudflare.F(p.OriginRequest.ProxyType),
			TCPKeepAlive:           cloudflare.F(p.OriginRequest.TCPKeepAlive),
			TLSTimeout:             cloudflare.F(p.OriginRequest.TLSTimeout),
		}
		if p.OriginRequest.Access != nil {
			or.Access = cloudflare.F(zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngressOriginRequestAccess{
				Required: cloudflare.F(p.OriginRequest.Access.Required),
				TeamName: cloudflare.F(p.OriginRequest.Access.TeamName),
				AUDTag:   cloudflare.F(p.OriginRequest.Access.AUDTag),
			})
		}
		rule.OriginRequest = cloudflare.F(or)
	}
	return rule, nil
}

// desiredState is the output of buildDesiredRules: the full desired ingress
// rule set keyed by ingressKey(hostname, path), the per-service rules from
// running containers (for validation), and container→hostname bookkeeping.
type desiredState struct {
	rules              map[string]zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress
	containerHostnames map[string][]string
	containerIDs       map[string]bool
}

// buildDesiredRules computes the desired ingress state from running containers
// plus persisted retention (StatusRetaining) entries. It is the single source
// of truth used by both Sync and Reconcile so the two paths cannot drift
// apart (B1).
//
// Behaviour:
//   - Containers that restarted during downtime are restored from pending
//     deletion (T074).
//   - Active entries whose container is absent from the scan are treated as
//     stopped during the disconnect window and transitioned per-service
//     (B8). The resulting ActionDeleteRoute actions are NOT executed: the
//     desired rule set is rebuilt wholesale, so per-rule deletes would race
//     the rebuild.
//   - Retention entries merged first; running containers win on key conflict.
//   - Cross-container conflicts are resolved deterministically: events are
//     processed in containerID order and the first container wins (B17).
//
// skipCooling, when true (Reconcile), skips containers in a flapping cooling
// period so periodic reconciliation does not hollow out the cooling logic
// (B11).
func (c *Controller) buildDesiredRules(ctx context.Context, skipCooling bool) (*desiredState, error) {
	if c.dockerManager == nil {
		return nil, fmt.Errorf("docker manager is not available")
	}

	eventsList, err := c.dockerManager.ScanRunningContainers(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to scan running containers: %w", err)
	}

	// Deterministic cross-container merge: process containers in stable
	// containerID order (B17).
	sort.Slice(eventsList, func(i, j int) bool {
		return eventsList[i].ContainerID < eventsList[j].ContainerID
	})

	scannedIDs := make(map[string]bool, len(eventsList))

	// 1. Restore containers that restarted during downtime from pending
	// deletion so their routes are re-registered below (T074).
	for _, event := range eventsList {
		if !c.isDocktunnelEnabled(event) {
			continue
		}
		scannedIDs[event.ContainerID] = true
		if _, exists := c.stateManager.GetPendingDeletion(event.ContainerID); exists {
			if err := c.stateManager.RestoreActiveTunnel(event.ContainerID); err != nil {
				slog.Warn("Failed to restore active tunnel for container",
					"containerID", event.ContainerID, "error", err)
			} else {
				slog.Info("Container restarted during downtime, restoring from pending deletion",
					"containerID", event.ContainerID)
			}
		}
	}

	// 2. Difference set (B8): entries still StatusActive for containers absent
	// from the scan stopped while we were disconnected. Run the same
	// per-service transitions as handleContainerStop; the returned
	// ActionDeleteRoute actions are intentionally dropped because the desired
	// rule set is rebuilt wholesale below.
	activeByContainer := make(map[string][]*types.TunnelEntry)
	for key, entry := range c.stateManager.GetAllActiveTunnels() {
		idx := strings.IndexByte(key, ':')
		if idx == -1 {
			slog.Warn("Active tunnel key without compound separator, skipping", "key", key)
			continue
		}
		containerID := key[:idx]
		activeByContainer[containerID] = append(activeByContainer[containerID], entry)
	}
	for containerID, entries := range activeByContainer {
		if scannedIDs[containerID] {
			continue
		}
		actions := c.transitionEntriesStopped(containerID, entries)
		if len(actions) > 0 {
			slog.Info("Container stopped during disconnect; delete-route actions folded into full rebuild",
				"containerID", containerID, "actions", len(actions))
		}
	}

	// 3. Merge persisted retention entries first; running containers below win
	// on key conflicts (B1). Expiry is handled by RunGC — every entry returned
	// by ListRetaining is still valid.
	desired := make(map[string]zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress)
	retainedKeys := make(map[string]bool)
	for _, entry := range c.stateManager.ListRetaining() {
		rule, err := c.ruleFromRetainingEntry(entry)
		if err != nil {
			slog.Warn("Skipping retaining entry with unparsable persisted rule",
				"containerID", entry.ContainerID,
				"service", entry.ServiceName,
				"error", err)
			continue
		}
		key := ingressKey(rule.Hostname.Value, rule.Path.Value)
		desired[key] = *rule
		retainedKeys[key] = true
	}

	// 4. Parse running containers and merge by ingressKey. First container in
	// sorted order wins on cross-container conflicts (B17). Validation is
	// per-container (review R6): one misconfigured container is skipped with
	// an error instead of aborting the whole sync for every container.
	containerHostnames := make(map[string][]string)
	owners := make(map[string]string) // ingressKey -> containerID (for conflict Warn)
	for _, event := range eventsList {
		if !c.isDocktunnelEnabled(event) {
			continue
		}
		if skipCooling && c.healthTracker != nil && c.healthTracker.IsCooling(event.ContainerID) {
			slog.Debug("Skipping cooling container during reconcile",
				"containerID", event.ContainerID)
			continue
		}
		parsedRules, err := label.Parse(event.ContainerInfo)
		if err != nil {
			slog.Error("Failed to parse container labels during sync",
				"action", "parse_labels",
				"result", "failure",
				"containerID", event.ContainerID,
				"error", err)
			continue
		}

		// Review R6: isolate misconfiguration per service instead of failing
		// the whole sync. Services without a hostname can never be routed;
		// they are skipped with a warning. The remaining hostname-bearing
		// rules are validated together against the desired state built so
		// far (deterministic containerID order), and the whole container is
		// skipped on validation failure.
		validatedRules := make(map[string]*zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress, len(parsedRules))
		for serviceName, rule := range parsedRules {
			if rule.Hostname.Value == "" {
				slog.Warn("Skipping service without hostname",
					"containerID", event.ContainerID, "service", serviceName)
				continue
			}
			validatedRules[serviceName] = rule
		}
		if len(validatedRules) == 0 {
			continue
		}
		// Validation base excludes retention-origin keys: a running container
		// may legitimately take over a retained route (running wins, B1), so
		// that conflict must not be reported as a duplicate.
		validationBase := make(map[string]zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress, len(desired))
		for k, r := range desired {
			if !retainedKeys[k] {
				validationBase[k] = r
			}
		}
		if err := c.ruleValidator.Validate(validatedRules, validationBase); err != nil {
			slog.Error("Invalid ingress rules for container, skipping container",
				"action", "validate_rules",
				"result", "failure",
				"containerID", event.ContainerID,
				"error", err)
			continue
		}

		var hostnames []string
		for serviceName, rule := range validatedRules {
			key := ingressKey(rule.Hostname.Value, rule.Path.Value)
			if prev, exists := owners[key]; exists {
				slog.Warn("Duplicate ingress rule across containers, keeping first",
					"hostname", rule.Hostname.Value,
					"path", rule.Path.Value,
					"first_container", prev,
					"duplicate_container", event.ContainerID)
				continue
			}
			owners[key] = event.ContainerID
			desired[key] = *rule
			hostnames = append(hostnames, rule.Hostname.Value)
			c.registerTunnelEntryIfMissing(event, serviceName, rule)
		}
		if len(hostnames) > 0 {
			containerHostnames[event.ContainerID] = hostnames
		}
	}

	return &desiredState{
		rules:              desired,
		containerHostnames: containerHostnames,
		containerIDs:       scannedIDs,
	}, nil
}

// transitionEntriesStopped runs the per-service state transition for a
// stopped container and returns the accumulated actions. Shared by
// handleContainerStop and the disconnect difference-set in buildDesiredRules
// (B8).
func (c *Controller) transitionEntriesStopped(containerID string, entries []*types.TunnelEntry) []types.Action {
	var allActions []types.Action
	for _, entry := range entries {
		actions, err := c.stateManager.Transition(
			containerID, entry.ServiceName,
			types.EventContainerStopped, &entry.RetentionPolicy,
		)
		if err != nil {
			slog.Error("State transition failed",
				"action", "state_transition",
				"result", "failure",
				"container_id", containerID,
				"service_name", entry.ServiceName,
				"error", err)
			continue
		}
		allActions = append(allActions, actions...)
	}
	return allActions
}

// registerTunnelEntryIfMissing creates the per-service state entry for a
// running container if it does not exist yet. The full ingress rule is
// serialized into Config.RuleJSON so retention can survive process restarts
// (B1).
func (c *Controller) registerTunnelEntryIfMissing(event events.Event, serviceName string, rule *zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress) {
	if _, exists := c.stateManager.GetActiveTunnel(event.ContainerID, serviceName); exists {
		return
	}
	ruleJSON, err := marshalIngressRule(rule)
	if err != nil {
		slog.Warn("Failed to serialize ingress rule for state persistence",
			"containerID", event.ContainerID,
			"service", serviceName,
			"error", err)
	}
	labels := event.ContainerInfo.Config.Labels
	c.stateManager.AddActiveTunnel(&types.TunnelEntry{
		ContainerID:     event.ContainerID,
		ServiceName:     serviceName,
		RetentionPolicy: c.getServiceRetentionPolicy(labels, serviceName),
		Status:          types.StatusActive,
		CreatedAt:       time.Now(),
		LastSyncAt:      time.Now(),
		Config: types.TunnelConfiguration{
			Hostname: rule.Hostname.Value,
			RuleJSON: ruleJSON,
		},
	})
}

// ruleFromRetainingEntry reconstructs the Cloudflare ingress rule persisted in
// a retention entry's Config.RuleJSON. Returns an error (caller Warns and
// skips) when the entry predates RuleJSON persistence or the JSON is corrupt.
func (c *Controller) ruleFromRetainingEntry(entry *types.TunnelEntry) (*zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress, error) {
	rule, err := unmarshalIngressRule(entry.Config.RuleJSON)
	if err != nil {
		return nil, fmt.Errorf("retaining entry %s:%s: %w", entry.ContainerID, entry.ServiceName, err)
	}
	return rule, nil
}
