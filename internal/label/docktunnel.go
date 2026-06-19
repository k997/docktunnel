package label

import (
	"log/slog"
	"regexp"
	"strings"
)

var dangerousChars = regexp.MustCompile("[;&|`$']")

func sanitizeLabelValue(key, value string) bool {
	if dangerousChars.MatchString(value) {
		slog.Warn("Rejected label value with dangerous characters",
			"label", key, "value", value, "reason", "potential injection attack")
		return false
	}
	return true
}

// decodeDockTunnel parses docktunnel.* labels into ServiceConfig map.
func decodeDockTunnel(labels map[string]string) map[string]*ServiceConfig {
	services := map[string]*ServiceConfig{}

	for label, value := range labels {
		if !strings.HasPrefix(label, "docktunnel.") {
			continue
		}

		parts := strings.Split(label, ".")
		if len(parts) < 3 {
			continue
		}

		serviceName := parts[1]
		attr := strings.Join(parts[2:], ".")

		if !sanitizeLabelValue(label, value) {
			slog.Warn("Skipping label with dangerous characters", "label", label, "value", value)
			continue
		}

		sc, ok := services[serviceName]
		if !ok {
			sc = &ServiceConfig{}
			services[serviceName] = sc
		}

		setServiceConfigField(sc, attr, value)
	}

	return services
}

func setServiceConfigField(sc *ServiceConfig, attr, value string) {
	switch attr {
	case "hostname":
		sc.Hostname = value
	case "service":
		sc.Service = value
	case "port":
		sc.Port = value
	case "scheme":
		sc.Scheme = value
	case "proto":
		sc.Proto = value
	case "path":
		sc.Path = value
	case "retention":
		sc.Retention = value
	case "originRequest.connectTimeout":
		sc.ConnectTimeout = value
	case "originRequest.tlsTimeout":
		sc.TLSTimeout = value
	case "originRequest.tcpKeepAlive":
		sc.TCPKeepAlive = value
	case "originRequest.keepAliveConnections":
		sc.KeepAliveConnections = value
	case "originRequest.keepAliveTimeout":
		sc.KeepAliveTimeout = value
	case "originRequest.noHappyEyeballs":
		sc.NoHappyEyeballs = value
	case "originRequest.noTLSVerify":
		sc.NoTLSVerify = value
	case "originRequest.http2Origin":
		sc.HTTP2Origin = value
	case "originRequest.disableChunkedEncoding":
		sc.DisableChunkedEncoding = value
	case "originRequest.httpHostHeader":
		sc.HTTPHostHeader = value
	case "originRequest.originServerName":
		sc.OriginServerName = value
	case "originRequest.caPool":
		sc.CAPool = value
	case "originRequest.proxyType":
		sc.ProxyType = value
	case "originRequest.proxyAddress":
		sc.ProxyAddress = value
	case "originRequest.proxyPort":
		sc.ProxyPort = value
	case "originRequest.matchSNItoHost":
		sc.MatchSNItoHost = value
	case "originRequest.access.required":
		sc.AccessRequired = value
	case "originRequest.access.teamName":
		sc.AccessTeamName = value
	case "originRequest.access.audTag":
		sc.AccessAUDTag = value
	default:
		// Warn on unknown attributes so users learn about typos and
		// unsupported keys (e.g. originRequest.fallbackDelay) instead of
		// silently dropping the value.
		if strings.HasPrefix(attr, "originRequest.") {
			slog.Warn("Ignoring unknown originRequest subkey",
				"attribute", attr, "hint", "see docs for supported originRequest.* keys")
		}
	}
}
