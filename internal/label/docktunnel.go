package label

import (
	"log/slog"
	"net/url"
	"regexp"
	"strings"
)

// hostnameRegex is the allow-list for hostname label values: alphanumerics
// plus '.', '-', '_' and '*' (wildcard). Anything else — whitespace, ';',
// '&', '|', backticks, quotes, shell metacharacters — is rejected.
var hostnameRegex = regexp.MustCompile(`^[A-Za-z0-9._*-]+$`)

// validHostname reports whether value is an acceptable hostname (or wildcard
// hostname like *.example.com) for a tunnel route.
func validHostname(value string) bool {
	return value != "" && hostnameRegex.MatchString(value)
}

// validServiceURL reports whether value is an acceptable service URL:
// an absolute http/https/tcp URL with a non-empty host.
func validServiceURL(value string) bool {
	u, err := url.ParseRequestURI(value)
	if err != nil {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "tcp":
	default:
		return false
	}
	return u.Host != ""
}

// validPathValue rejects control characters and newlines in path values.
func validPathValue(value string) bool {
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return false
		}
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

		// A1: docktunnel.traefik.enable is the Traefik opt-in flag consumed by
		// Parse; it must never become a service named "traefik".
		if serviceName == "traefik" && attr == "enable" {
			continue
		}

		// A12: "enable" is a reserved service-name namespace (docktunnel.enable
		// itself is the global switch); anything nested under it is a typo.
		if serviceName == "enable" {
			slog.Warn("Skipping reserved docktunnel.enable.* label",
				"label", label, "value", value)
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
		if !validHostname(value) {
			slog.Warn("Rejected invalid hostname label value",
				"attribute", attr, "value", value,
				"hint", "hostname may only contain letters, digits, '.', '-', '_', '*'")
			return
		}
		sc.Hostname = value
	case "service":
		if !validServiceURL(value) {
			slog.Warn("Rejected invalid service URL label value",
				"attribute", attr, "value", value,
				"hint", "service must be an absolute http/https/tcp URL with a non-empty host")
			return
		}
		sc.Service = value
	case "path":
		if !validPathValue(value) {
			slog.Warn("Rejected invalid path label value",
				"attribute", attr, "value", value,
				"hint", "path must not contain control characters or newlines")
			return
		}
		sc.Path = value
	case "port":
		sc.Port = value
	case "scheme":
		sc.Scheme = value
	case "proto":
		sc.Proto = value
	case "retention":
		sc.Retention = value
	case "delete_retention":
		sc.Retention = value
	case "network":
		sc.Network = value
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
	case "originRequest.matchSNItoHost", "matchSniToHost":
		// F3: matchSNItoHost has no corresponding field in the Cloudflare v5
		// SDK (zero_trust has no MatchSNI), so it was never written to the
		// tunnel config. Warn instead of silently storing a value that will
		// never be applied.
		slog.Warn("matchSNItoHost is not supported by the Cloudflare API, ignoring",
			"attribute", attr, "value", value)
	case "originRequest.access.required", "access.required":
		sc.AccessRequired = value
	case "originRequest.access.teamName", "originRequest.access.team_name",
		"access.teamName", "access.team_name":
		sc.AccessTeamName = value
	case "originRequest.access.audTag", "originRequest.access.aud_tag",
		"access.audTag", "access.aud_tag":
		sc.AccessAUDTag = value
	default:
		// Warn on unknown attributes so users learn about typos and
		// unsupported keys instead of silently dropping the value.
		slog.Warn("Ignoring unknown docktunnel label attribute",
			"attribute", attr, "hint", "see docs for supported docktunnel.* keys")
	}
}
