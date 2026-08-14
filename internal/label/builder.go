package label

import (
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cloudflare/cloudflare-go/v5"
	"github.com/cloudflare/cloudflare-go/v5/zero_trust"
	"github.com/docker/docker/api/types/container"
)

// GetContainerIP detects the container IP address.
// It is a convenience wrapper around GetContainerIPOnNetwork with no
// specific network requested.
func GetContainerIP(containerInfo *container.InspectResponse) string {
	return GetContainerIPOnNetwork(containerInfo, "")
}

// GetContainerIPOnNetwork detects the container IP address, preferring the
// given Docker network when one is specified.
//   - network "" behaves exactly like the historical GetContainerIP: host
//     mode -> "localhost", bridge first, otherwise the first non-empty IP in
//     deterministic (sorted) network-name order.
//   - network "host" -> "localhost".
//   - network names an attached network -> that network's IP.
//   - network names an unknown network -> warns and falls back to the default
//     detection.
func GetContainerIPOnNetwork(containerInfo *container.InspectResponse, network string) string {
	if network != "" {
		if containerInfo == nil || containerInfo.NetworkSettings == nil {
			return ""
		}
		if network == "host" {
			return "localhost"
		}
		if ep, exists := containerInfo.NetworkSettings.Networks[network]; exists {
			return ep.IPAddress
		}
		slog.Warn("Container network not found, falling back to default IP detection",
			"network", network)
	}
	return defaultContainerIP(containerInfo)
}

func defaultContainerIP(containerInfo *container.InspectResponse) string {
	if containerInfo == nil {
		return ""
	}
	if containerInfo.NetworkSettings == nil {
		return ""
	}
	if containerInfo.HostConfig == nil {
		return ""
	}

	if containerInfo.HostConfig.NetworkMode == "host" {
		return "localhost"
	}

	if network, exists := containerInfo.NetworkSettings.Networks["bridge"]; exists && network.IPAddress != "" {
		return network.IPAddress
	}

	// Iterate networks in deterministic (sorted) name order so a container
	// attached to multiple non-bridge networks always picks the same IP.
	// Map iteration order is random in Go — without sorting, the same labels
	// could resolve to different IPs on different runs.
	names := make([]string, 0, len(containerInfo.NetworkSettings.Networks))
	for name := range containerInfo.NetworkSettings.Networks {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if network := containerInfo.NetworkSettings.Networks[name]; network.IPAddress != "" {
			return network.IPAddress
		}
	}

	return ""
}

// adaptDockTunnelToSpecs converts decoded docktunnel ServiceConfigs into IngressSpecs.
func adaptDockTunnelToSpecs(services map[string]*ServiceConfig, containerInfo *container.InspectResponse) map[string]*IngressSpec {
	specs := map[string]*IngressSpec{}

	for name, sc := range services {
		spec := &IngressSpec{
			Hostname:      sc.Hostname,
			Path:          sc.Path,
			Retention:     sc.Retention,
			Network:       sc.Network,
			OriginRequest: convertOriginRequest(sc),
		}

		// A11: resolve the container IP on the service's requested network
		// (empty network falls back to the default detection).
		containerIP := GetContainerIPOnNetwork(containerInfo, sc.Network)

		if sc.Service != "" {
			spec.ServiceURL = sc.Service
		} else if sc.Port != "" {
			scheme := resolveScheme(sc)
			port, err := strconv.Atoi(sc.Port)
			if err != nil {
				slog.Error("Invalid docktunnel service port, using 0",
					"service", name, "value", sc.Port, "error", err)
				port = 0
			}
			spec.Port = port
			spec.Scheme = scheme
			if err == nil && containerIP != "" {
				spec.ServiceURL = scheme + "://" + containerIP + ":" + sc.Port
			}
		}

		specs[name] = spec
	}

	return specs
}

func resolveScheme(sc *ServiceConfig) string {
	if sc.Scheme != "" {
		return sc.Scheme
	}
	if sc.Proto != "" {
		slog.Info("Using proto label (deprecated, use scheme)", "proto", sc.Proto)
		return sc.Proto
	}
	return "http"
}

func convertOriginRequest(sc *ServiceConfig) *OriginRequestSpec {
	spec := &OriginRequestSpec{}
	hasAny := false

	if v := sc.ConnectTimeout; v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			spec.ConnectTimeout = &d
			hasAny = true
		} else {
			slog.Warn("Ignoring originRequest.connectTimeout: invalid duration",
				"value", v, "error", err)
		}
	}
	if v := sc.TLSTimeout; v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			spec.TLSTimeout = &d
			hasAny = true
		} else {
			slog.Warn("Ignoring originRequest.tlsTimeout: invalid duration",
				"value", v, "error", err)
		}
	}
	if v := sc.TCPKeepAlive; v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			spec.TCPKeepAlive = &d
			hasAny = true
		} else {
			slog.Warn("Ignoring originRequest.tcpKeepAlive: invalid duration",
				"value", v, "error", err)
		}
	}
	if v := sc.KeepAliveTimeout; v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			spec.KeepAliveTimeout = &d
			hasAny = true
		} else {
			slog.Warn("Ignoring originRequest.keepAliveTimeout: invalid duration",
				"value", v, "error", err)
		}
	}
	if v := sc.KeepAliveConnections; v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			spec.KeepAliveConnections = &n
			hasAny = true
		} else {
			slog.Warn("Ignoring originRequest.keepAliveConnections: invalid integer",
				"value", v, "error", err)
		}
	}
	if v := sc.NoHappyEyeballs; v != "" {
		if b, err := parseBoolLabel("originRequest.noHappyEyeballs", v); err == nil {
			spec.NoHappyEyeballs = &b
			hasAny = true
		}
	}
	if v := sc.NoTLSVerify; v != "" {
		if b, err := parseBoolLabel("originRequest.noTLSVerify", v); err == nil {
			spec.NoTLSVerify = &b
			hasAny = true
		}
	}
	if v := sc.HTTP2Origin; v != "" {
		if b, err := parseBoolLabel("originRequest.http2Origin", v); err == nil {
			spec.HTTP2Origin = &b
			hasAny = true
		}
	}
	if v := sc.DisableChunkedEncoding; v != "" {
		if b, err := parseBoolLabel("originRequest.disableChunkedEncoding", v); err == nil {
			spec.DisableChunkedEncoding = &b
			hasAny = true
		}
	}
	if v := sc.HTTPHostHeader; v != "" {
		spec.HTTPHostHeader = v
		hasAny = true
	}
	if v := sc.OriginServerName; v != "" {
		spec.OriginServerName = v
		hasAny = true
	}
	if v := sc.CAPool; v != "" {
		spec.CAPool = v
		hasAny = true
	}
	if v := sc.ProxyType; v != "" {
		spec.ProxyType = v
		hasAny = true
	}
	if v := sc.ProxyAddress; v != "" {
		spec.ProxyAddress = v
		hasAny = true
	}
	if v := sc.ProxyPort; v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			spec.ProxyPort = &n
			hasAny = true
		} else {
			slog.Warn("Ignoring originRequest.proxyPort: invalid integer",
				"value", v, "error", err)
		}
	}
	// F3: matchSNItoHost is not supported by the Cloudflare v5 SDK (zero_trust
	// has no MatchSNI field); the decoder now ignores the label with a warning,
	// so nothing is converted here. OriginRequestSpec.MatchSNItoHost is
	// retained (deprecated) only to keep gob/tests stable and is never
	// populated.

	// Access
	if sc.AccessRequired != "" || sc.AccessTeamName != "" || sc.AccessAUDTag != "" {
		access := &AccessSpec{}
		// accessRequired 也走 ParseBool
		if sc.AccessRequired != "" {
			if b, err := parseBoolLabel("originRequest.access.required", sc.AccessRequired); err == nil {
				access.Required = b
			}
		}
		access.TeamName = sc.AccessTeamName
		if sc.AccessAUDTag != "" {
			tags := strings.Split(sc.AccessAUDTag, ",")
			for i, tag := range tags {
				tags[i] = strings.TrimSpace(tag)
			}
			access.AUDTag = tags
		}
		spec.Access = access
		hasAny = true
	}

	if !hasAny {
		return nil
	}
	return spec
}

// parseBoolLabel parses a boolean label value using Go's strconv.ParseBool,
// which accepts 1, t, T, TRUE, true, True, 0, f, F, FALSE, false, False.
// This is much more forgiving than the previous `v == "true"` check, which
// silently treated True / TRUE / 1 / yes as false — most dangerously for
// noTLSVerify, where a user setting "True" against a self-signed origin
// would still get TLS verification and fail handshakes without any warning.
func parseBoolLabel(label, value string) (bool, error) {
	b, err := strconv.ParseBool(value)
	if err != nil {
		slog.Warn("Ignoring boolean label: invalid value",
			"label", label, "value", value,
			"hint", "accepted: 1, t, true, TRUE, True, 0, f, false, FALSE, False")
		return false, err
	}
	return b, nil
}

// mergeSpecs merges traefik and docktunnel specs. On hostname conflict, docktunnel wins.
// Hostname comparison is case-insensitive and written hostnames are normalized
// to lower case so Host(`Example.COM`) and Host(`example.com`) collide.
func mergeSpecs(traefikSpecs, docktunnelSpecs map[string]*IngressSpec) map[string]*IngressSpec {
	merged := map[string]*IngressSpec{}

	traefikByHostname := map[string]string{}
	for key, spec := range traefikSpecs {
		merged[key] = spec
		if spec.Hostname != "" {
			traefikByHostname[strings.ToLower(spec.Hostname)] = key
		}
	}

	for key, dtSpec := range docktunnelSpecs {
		if dtSpec.Hostname != "" {
			if tfKey, exists := traefikByHostname[strings.ToLower(dtSpec.Hostname)]; exists {
				delete(merged, tfKey)
				slog.Info("DockTunnel label overrides Traefik for hostname",
					"hostname", dtSpec.Hostname, "docktunnel_key", key, "traefik_key", tfKey)
			}
			dtSpec.Hostname = strings.ToLower(dtSpec.Hostname)
		}
		merged[key] = dtSpec
	}

	return merged
}

// buildIngressRules converts merged IngressSpecs to CF Ingress rules.
func buildIngressRules(specs map[string]*IngressSpec, containerIP string) map[string]*zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress {
	rules := map[string]*zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{}

	for name, spec := range specs {
		rule := &zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{}

		if spec.Hostname != "" {
			rule.Hostname = cloudflare.F(spec.Hostname)
		}

		if spec.Path != "" {
			rule.Path = cloudflare.F(spec.Path)
		}

		rule.Service = cloudflare.F(buildServiceURL(spec, containerIP))

		if spec.OriginRequest != nil {
			rule.OriginRequest = cloudflare.F(buildOriginRequestCF(spec.OriginRequest))
		}

		rules[name] = rule
	}

	return rules
}

func buildServiceURL(spec *IngressSpec, containerIP string) string {
	if spec.ServiceURL != "" {
		return spec.ServiceURL
	}

	scheme := spec.Scheme
	if scheme == "" {
		scheme = "http"
	}

	if spec.Port == 0 {
		return scheme + "://" + containerIP
	}
	return scheme + "://" + containerIP + ":" + strconv.Itoa(spec.Port)
}

func buildOriginRequestCF(spec *OriginRequestSpec) zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngressOriginRequest {
	cf := zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngressOriginRequest{}

	if spec.ConnectTimeout != nil {
		cf.ConnectTimeout = cloudflare.F(spec.ConnectTimeout.Nanoseconds())
	}
	if spec.TLSTimeout != nil {
		cf.TLSTimeout = cloudflare.F(spec.TLSTimeout.Nanoseconds())
	}
	if spec.TCPKeepAlive != nil {
		cf.TCPKeepAlive = cloudflare.F(spec.TCPKeepAlive.Nanoseconds())
	}
	if spec.KeepAliveConnections != nil {
		cf.KeepAliveConnections = cloudflare.F(*spec.KeepAliveConnections)
	}
	if spec.KeepAliveTimeout != nil {
		cf.KeepAliveTimeout = cloudflare.F(spec.KeepAliveTimeout.Nanoseconds())
	}
	if spec.NoHappyEyeballs != nil {
		cf.NoHappyEyeballs = cloudflare.F(*spec.NoHappyEyeballs)
	}
	if spec.NoTLSVerify != nil {
		cf.NoTLSVerify = cloudflare.F(*spec.NoTLSVerify)
	}
	if spec.OriginServerName != "" {
		cf.OriginServerName = cloudflare.F(spec.OriginServerName)
	}
	if spec.CAPool != "" {
		cf.CAPool = cloudflare.F(spec.CAPool)
	}
	if spec.HTTP2Origin != nil {
		cf.HTTP2Origin = cloudflare.F(*spec.HTTP2Origin)
	}
	if spec.HTTPHostHeader != "" {
		cf.HTTPHostHeader = cloudflare.F(spec.HTTPHostHeader)
	}
	if spec.DisableChunkedEncoding != nil {
		cf.DisableChunkedEncoding = cloudflare.F(*spec.DisableChunkedEncoding)
	}
	if spec.ProxyType != "" {
		cf.ProxyType = cloudflare.F(spec.ProxyType)
	}

	// Access
	if spec.Access != nil {
		access := zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngressOriginRequestAccess{}
		access.Required = cloudflare.F(spec.Access.Required)
		access.TeamName = cloudflare.F(spec.Access.TeamName)
		if len(spec.Access.AUDTag) > 0 {
			access.AUDTag = cloudflare.F(spec.Access.AUDTag)
		}
		cf.Access = cloudflare.F(access)
	}

	return cf
}
