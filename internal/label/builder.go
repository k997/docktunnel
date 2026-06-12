package label

import (
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/cloudflare/cloudflare-go/v5"
	"github.com/cloudflare/cloudflare-go/v5/zero_trust"
	"github.com/docker/docker/api/types/container"
)

// GetContainerIP detects the container IP address.
func GetContainerIP(containerInfo *container.InspectResponse) string {
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

	for _, network := range containerInfo.NetworkSettings.Networks {
		if network.IPAddress != "" {
			return network.IPAddress
		}
	}

	return ""
}

// adaptDockTunnelToSpecs converts decoded docktunnel ServiceConfigs into IngressSpecs.
func adaptDockTunnelToSpecs(services map[string]*ServiceConfig, containerInfo *container.InspectResponse) map[string]*IngressSpec {
	specs := map[string]*IngressSpec{}
	containerIP := GetContainerIP(containerInfo)

	for name, sc := range services {
		spec := &IngressSpec{
			Hostname:      sc.Hostname,
			Path:          sc.Path,
			Retention:     sc.Retention,
			OriginRequest: convertOriginRequest(sc),
		}

		if sc.Service != "" {
			spec.ServiceURL = sc.Service
		} else if sc.Port != "" {
			scheme := resolveScheme(sc)
			port, _ := strconv.Atoi(sc.Port)
			spec.Port = port
			spec.Scheme = scheme
			if containerIP != "" {
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
		}
	}
	if v := sc.TLSTimeout; v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			spec.TLSTimeout = &d
			hasAny = true
		}
	}
	if v := sc.TCPKeepAlive; v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			spec.TCPKeepAlive = &d
			hasAny = true
		}
	}
	if v := sc.KeepAliveTimeout; v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			spec.KeepAliveTimeout = &d
			hasAny = true
		}
	}
	if v := sc.KeepAliveConnections; v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			spec.KeepAliveConnections = &n
			hasAny = true
		}
	}
	if v := sc.NoHappyEyeballs; v != "" {
		b := v == "true"
		spec.NoHappyEyeballs = &b
		hasAny = true
	}
	if v := sc.NoTLSVerify; v != "" {
		b := v == "true"
		spec.NoTLSVerify = &b
		hasAny = true
	}
	if v := sc.HTTP2Origin; v != "" {
		b := v == "true"
		spec.HTTP2Origin = &b
		hasAny = true
	}
	if v := sc.DisableChunkedEncoding; v != "" {
		b := v == "true"
		spec.DisableChunkedEncoding = &b
		hasAny = true
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
		}
	}
	if v := sc.MatchSNItoHost; v != "" {
		b := v == "true"
		spec.MatchSNItoHost = &b
		hasAny = true
	}

	// Access
	if sc.AccessRequired != "" || sc.AccessTeamName != "" || sc.AccessAUDTag != "" {
		access := &AccessSpec{}
		access.Required = sc.AccessRequired == "true"
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

// mergeSpecs merges traefik and docktunnel specs. On hostname conflict, docktunnel wins.
func mergeSpecs(traefikSpecs, docktunnelSpecs map[string]*IngressSpec) map[string]*IngressSpec {
	merged := map[string]*IngressSpec{}

	traefikByHostname := map[string]string{}
	for key, spec := range traefikSpecs {
		merged[key] = spec
		if spec.Hostname != "" {
			traefikByHostname[spec.Hostname] = key
		}
	}

	for key, dtSpec := range docktunnelSpecs {
		if dtSpec.Hostname != "" {
			if tfKey, exists := traefikByHostname[dtSpec.Hostname]; exists {
				delete(merged, tfKey)
				slog.Info("DockTunnel label overrides Traefik for hostname",
					"hostname", dtSpec.Hostname, "docktunnel_key", key, "traefik_key", tfKey)
			}
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
