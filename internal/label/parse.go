package label

import (
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"docktunnel/pkg/types"

	"github.com/cloudflare/cloudflare-go/v5/zero_trust"
	"github.com/docker/docker/api/types/container"
)

// Parse parses container labels into Cloudflare Tunnel ingress rules.
// Supports both docktunnel.* and traefik.* labels.
// On hostname conflict, docktunnel labels take priority.
func Parse(containerInfo *container.InspectResponse) (map[string]*zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress, error) {
	if containerInfo == nil || containerInfo.Config == nil || containerInfo.Config.Labels == nil {
		return nil, fmt.Errorf("no valid container info")
	}

	labels := containerInfo.Config.Labels

	// Stage 1: Decode both label namespaces
	dtServices := decodeDockTunnel(labels)
	tfSpecs := decodeTraefikToSpecs(labels, containerInfo)

	// Stage 2: Adapt docktunnel ServiceConfigs -> IngressSpecs
	dtSpecs := adaptDockTunnelToSpecs(dtServices, containerInfo)

	// Stage 3: Merge (docktunnel wins on hostname conflict)
	mergedSpecs := mergeSpecs(tfSpecs, dtSpecs)

	if len(mergedSpecs) == 0 {
		return nil, fmt.Errorf("no valid ingress rules found")
	}

	// Stage 4: Build CF Ingress rules
	containerIP := GetContainerIP(containerInfo)
	rules := buildIngressRules(mergedSpecs, containerIP)

	for name, rule := range rules {
		if rule.OriginRequest.Present {
			slog.Debug("Applied originRequest settings", "service", name)
		}
	}

	return rules, nil
}

// ParseRetentionPolicy parses a retention policy label value.
func ParseRetentionPolicy(value string) (types.RetentionPolicy, error) {
	policy := types.RetentionPolicy{}
	trimmed := strings.ToLower(strings.TrimSpace(value))

	switch trimmed {
	case "0", "immediate":
		policy.Type = types.Immediate
		return policy, nil
	case "forever", "keep":
		policy.Type = types.Forever
		return policy, nil
	}

	if strings.HasSuffix(trimmed, "d") {
		daysStr := strings.TrimSuffix(trimmed, "d")
		days, err := strconv.Atoi(daysStr)
		if err != nil || days <= 0 {
			return policy, fmt.Errorf("invalid retention policy format: %s", value)
		}
		policy.Type = types.Timed
		policy.Duration = time.Duration(days) * 24 * time.Hour
		return policy, nil
	}

	duration, err := time.ParseDuration(value)
	if err != nil {
		return policy, fmt.Errorf("invalid retention policy format: %s", value)
	}
	if duration <= 0 {
		return policy, fmt.Errorf("retention duration must be positive, got: %s", value)
	}

	policy.Type = types.Timed
	policy.Duration = duration
	return policy, nil
}
