package label

import (
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/docker/docker/api/types/container"
)

// decodeTraefikToSpecs parses traefik.* labels and converts them to IngressSpec map.
func decodeTraefikToSpecs(labels map[string]string, containerInfo *container.InspectResponse) map[string]*IngressSpec {
	specs := map[string]*IngressSpec{}

	conf := decodeTraefikLabels(labels)
	if conf == nil {
		return specs
	}

	// A11: traefik.docker.network selects the Docker network used to reach the
	// container (mirrors docktunnel.<service>.network).
	network := labels["traefik.docker.network"]
	containerIP := GetContainerIPOnNetwork(containerInfo, network)

	// Adapt HTTP routers
	for name, router := range conf.HTTP.Routers {
		// A2: reject unsafe/unsupported rules before extracting anything,
		// so a rule like !Host(x) can never be inverted into "route x only".
		if ok, reason := validateTraefikRule(router.Rule); !ok {
			slog.Warn("Rejected Traefik HTTP router rule",
				"router", name, "rule", router.Rule, "reason", reason)
			continue
		}

		// Review R2: a router that references middlewares (e.g. basicAuth)
		// must not be exposed without them — that would publish an
		// unauthenticated copy of an auth-protected route.
		if router.Middlewares != "" {
			slog.Warn("Rejected Traefik HTTP router: middlewares are not supported, refusing to expose without them",
				"router", name, "middlewares", router.Middlewares)
			continue
		}

		hostnames := extractHostsFromRule(router.Rule)
		paths := extractPathsFromRule(router.Rule)

		// Review R3: a rule without any Host(...) clause (Path-only) would
		// previously be dropped silently. F5: also covers Host(a.com) without
		// quotes, which yields no quoted hostname.
		if len(hostnames) == 0 {
			slog.Warn("Rejected Traefik HTTP router: no quoted hostname found (Host(...) requires quotes)",
				"router", name, "rule", router.Rule)
			continue
		}

		svcName := router.Service
		if svcName == "" {
			svcName = name
		}

		// F4: a router pointing at a service without any usable server (missing
		// service, weighted/other non-loadbalancer structure, empty server
		// list) must not silently degrade into a port-0 route. Warn and skip.
		if !httpServiceHasServers(conf.HTTP.Services, svcName) {
			slog.Warn("Rejected Traefik HTTP router: service has no usable loadbalancer.server",
				"router", name, "service", svcName)
			continue
		}

		port, scheme := resolveHTTPService(conf.HTTP.Services, svcName)

		for _, hn := range hostnames {
			hn = strings.ToLower(hn)

			if !validHostname(hn) {
				slog.Warn("Rejected Traefik HTTP router hostname",
					"router", name, "hostname", hn)
				continue
			}

			spec := &IngressSpec{
				Hostname: hn,
				Port:     port,
				Scheme:   scheme,
				Network:  network,
			}
			if len(paths) > 0 {
				spec.Path = paths[0]
			}
			if containerIP != "" && port > 0 {
				spec.ServiceURL = scheme + "://" + containerIP + ":" + strconv.Itoa(port)
			}

			key := "http:" + name + "@" + hn
			specs[key] = spec
		}
	}

	// Adapt TCP routers
	for name, router := range conf.TCP.Routers {
		if ok, reason := validateTraefikRule(router.Rule); !ok {
			slog.Warn("Rejected Traefik TCP router rule",
				"router", name, "rule", router.Rule, "reason", reason)
			continue
		}

		hostnames := extractHostSNIFromRule(router.Rule)
		if len(hostnames) == 0 {
			slog.Warn("Rejected Traefik TCP router rule without HostSNI(...)",
				"router", name, "rule", router.Rule)
			continue
		}

		svcName := router.Service
		if svcName == "" {
			svcName = name
		}

		// F4: same guard as the HTTP loop — a TCP router with no usable
		// server would otherwise route to tcp://IP with no port.
		if !tcpServiceHasServers(conf.TCP.Services, svcName) {
			slog.Warn("Rejected Traefik TCP router: service has no usable loadbalancer.server",
				"router", name, "service", svcName)
			continue
		}

		port := resolveTCPPort(conf.TCP.Services, svcName)

		for _, hn := range hostnames {
			hn = strings.ToLower(hn)

			if !validHostname(hn) {
				slog.Warn("Rejected Traefik TCP router hostname",
					"router", name, "hostname", hn)
				continue
			}

			spec := &IngressSpec{
				Hostname: hn,
				Port:     port,
				Scheme:   "tcp",
				Network:  network,
			}
			if containerIP != "" && port > 0 {
				spec.ServiceURL = "tcp://" + containerIP + ":" + strconv.Itoa(port)
			}

			key := "tcp:" + name + "@" + hn
			specs[key] = spec
		}
	}

	return specs
}

// validateTraefikRule checks that a Traefik rule is safe to translate into a
// Cloudflare Tunnel ingress rule. HTTP rules may only be composed of Host(...)
// and Path(...) clauses; TCP rules may only contain HostSNI(...). Negations,
// boolean operators and any other matcher (HostRegexp, PathPrefix, Method,
// Header, Query, ClientIP, ...) are rejected, because they can produce
// effectively "everything else" routing that would expose internal services.
// Returns false plus a reason when the rule must be rejected.
func validateTraefikRule(rule string) (bool, string) {
	trimmed := strings.TrimSpace(rule)
	if trimmed == "" {
		return false, "empty rule"
	}

	lower := strings.ToLower(trimmed)

	// Boolean operators and negation change the routing semantics in ways we
	// cannot map onto a Cloudflare ingress rule. These are checked as raw
	// substrings (conservative: a quoted path containing "&&" is rejected
	// rather than silently misrouted).
	for _, bad := range []string{"!", "&&", "||"} {
		if strings.Contains(lower, bad) {
			return false, fmt.Sprintf("unsupported or unsafe rule clause %q", bad)
		}
	}

	// Unsupported matchers, matched as matcher-name followed by "(" so that
	// legitimate hostnames/paths containing these words (e.g. Path('/query'))
	// are not falsely rejected (review R2).
	if unsafeMatcherRegex.MatchString(trimmed) {
		return false, "rule contains an unsupported matcher (HostRegexp/PathPrefix/PathRegexp/Method/Header/Query/ClientIP)"
	}

	if strings.Contains(lower, "hostsni(") {
		// TCP-style rule: only HostSNI(...) clauses are allowed.
		if strings.Contains(lower, "host(") || strings.Contains(lower, "path(") {
			return false, "TCP rule may only contain HostSNI(...)"
		}
		if rest := tcpRuleClauseRegex.ReplaceAllString(trimmed, ""); strings.TrimSpace(rest) != "" {
			return false, "TCP rule contains unsupported content outside HostSNI(...)"
		}
		return true, ""
	}

	if !strings.Contains(lower, "host(") && !strings.Contains(lower, "path(") {
		return false, "rule contains no Host(...), Path(...) or HostSNI(...) clause"
	}

	if rest := httpRuleClauseRegex.ReplaceAllString(trimmed, ""); strings.TrimSpace(rest) != "" {
		return false, "rule contains unsupported content outside Host(...)/Path(...)"
	}
	return true, ""
}

// httpRuleClauseRegex matches a single Host(...) or Path(...) clause.
var httpRuleClauseRegex = regexp.MustCompile(`(?i)(?:Host|Path)\([^)]*\)`)

// tcpRuleClauseRegex matches a single HostSNI(...) clause.
var tcpRuleClauseRegex = regexp.MustCompile(`(?i)HostSNI\([^)]*\)`)

// unsafeMatcherRegex matches unsupported Traefik matcher invocations
// (matcher name followed by "("). Matching this shape avoids falsely
// rejecting legitimate hostnames/paths that merely contain the word,
// e.g. Path('/query').
var unsafeMatcherRegex = regexp.MustCompile(`(?i)(hostregexp|pathprefix|pathregexp|method|header|query|clientip)\s*\(`)

// decodeTraefikLabels parses traefik.* labels into a minimal Configuration.
func decodeTraefikLabels(labels map[string]string) *Configuration {
	// A6: pre-validate every key so a single malformed label (bad root casing,
	// empty segment, bracket segment) is dropped with a warning instead of
	// making DecodeToNode fail and destroying the whole traefik tree.
	filtered := map[string]string{}
	for key, value := range labels {
		if !isTraefikLabelKey(key) {
			// Not a traefik.* label; DecodeToNode filters these out anyway.
			continue
		}
		if !validTraefikLabelKey(key) {
			slog.Warn("Dropping invalid traefik label key", "label", key)
			continue
		}
		// P3-2: traefik.udp.* is not supported. The DecodeToNode filters below
		// only keep the traefik.http / traefik.tcp prefixes, so without this
		// check UDP labels would be dropped silently by sortKeys — contradicting
		// the documented "benignly ignored (Info log only)" behavior. Emit one
		// Info line per UDP label and skip it.
		if strings.HasPrefix(strings.ToLower(key), "traefik.udp") {
			slog.Info("Traefik UDP labels are not supported, ignoring", "label", key)
			continue
		}
		filtered[key] = value
	}
	if len(filtered) == 0 {
		return nil
	}

	node, err := DecodeToNode(filtered, "traefik", "traefik.http", "traefik.tcp")
	if err != nil || node == nil {
		return nil
	}

	conf := &Configuration{
		HTTP: &HTTPConfiguration{Routers: map[string]*Router{}, Services: map[string]*Service{}},
		TCP:  &TCPConfiguration{Routers: map[string]*TCPRouter{}, Services: map[string]*TCPService{}},
	}

	for _, rootChild := range node.Children {
		switch rootChild.Name {
		case "http":
			populateHTTP(rootChild, conf.HTTP)
		case "tcp":
			populateTCP(rootChild, conf.TCP)
		}
	}

	return conf
}

// isTraefikLabelKey reports whether a label key belongs to the traefik
// namespace (case-insensitive "traefik." prefix), i.e. it is a candidate for
// the traefik DecodeToNode filters.
func isTraefikLabelKey(key string) bool {
	return strings.HasPrefix(strings.ToLower(key), "traefik.")
}

// validTraefikLabelKey reports whether a traefik label key survives the
// DecodeToNode rules: root must be exactly "traefik" (case-sensitive), no
// empty segments, no segment may start with '[', and no segment may contain
// ']' without a matching '['.
func validTraefikLabelKey(key string) bool {
	split := strings.Split(key, ".")
	if split[0] != "traefik" {
		return false
	}
	for _, part := range split {
		if part == "" {
			return false
		}
		if strings.HasPrefix(part, "[") {
			return false
		}
		// F1: DecodeToNode treats ']' as a slice-delimiter terminator and
		// slices the segment at its first '[' (parser.go). When a segment
		// contains ']' but no '[' (e.g. "routers.foo]" or "routers.a]b"),
		// strings.Index returns -1 and v[:indexLeft] panics — the initial Sync
		// path has no recover. Drop such keys up front so one malformed label
		// cannot crash the process. Legitimate slice indices always contain
		// '[', so this only rejects garbage keys.
		if strings.Contains(part, "]") && !strings.Contains(part, "[") {
			return false
		}
	}
	return true
}

func populateHTTP(node *Node, http *HTTPConfiguration) {
	for _, child := range node.Children {
		switch child.Name {
		case "routers":
			for _, routerNode := range child.Children {
				router := &Router{}
				for _, field := range routerNode.Children {
					switch field.Name {
					case "rule":
						router.Rule = field.Value
					case "service":
						router.Service = field.Value
					case "middlewares":
						router.Middlewares = field.Value
					default:
						slog.Info("Ignoring unsupported Traefik HTTP router field",
							"router", routerNode.Name, "field", field.Name)
					}
				}
				http.Routers[routerNode.Name] = router
			}
		case "services":
			for _, svcNode := range child.Children {
				svc := &Service{}
				for _, field := range svcNode.Children {
					switch field.Name {
					case "loadbalancer":
						lb := &ServersLoadBalancer{}
						populateLoadBalancer(field, lb)
						svc.LoadBalancer = lb
					default:
						slog.Info("Ignoring unsupported Traefik HTTP service field",
							"service", svcNode.Name, "field", field.Name)
					}
				}
				http.Services[svcNode.Name] = svc
			}
		default:
			// middlewares etc — ignored
		}
	}
}

func populateLoadBalancer(node *Node, lb *ServersLoadBalancer) {
	for _, child := range node.Children {
		switch child.Name {
		case "server":
			// A7: only add a server when it actually carries fields; checking
			// node.Children here was always true (it contains the servers).
			if len(child.Children) > 0 {
				server := Server{}
				for _, field := range child.Children {
					switch field.Name {
					case "port":
						server.Port = field.Value
					case "scheme":
						server.Scheme = field.Value
					case "url":
						server.URL = field.Value
					}
				}
				lb.Servers = append(lb.Servers, server)
			}
		default:
			// sticky, healthCheck etc — ignored
		}
	}
}

func populateTCP(node *Node, tcp *TCPConfiguration) {
	for _, child := range node.Children {
		switch child.Name {
		case "routers":
			for _, routerNode := range child.Children {
				router := &TCPRouter{}
				for _, field := range routerNode.Children {
					switch field.Name {
					case "rule":
						router.Rule = field.Value
					case "service":
						router.Service = field.Value
					default:
						slog.Info("Ignoring unsupported Traefik TCP router field",
							"router", routerNode.Name, "field", field.Name)
					}
				}
				tcp.Routers[routerNode.Name] = router
			}
		case "services":
			for _, svcNode := range child.Children {
				svc := &TCPService{}
				for _, field := range svcNode.Children {
					switch field.Name {
					case "loadbalancer":
						lb := &TCPServersLoadBalancer{}
						for _, lbChild := range field.Children {
							if lbChild.Name == "server" {
								server := TCPServer{}
								for _, sf := range lbChild.Children {
									switch sf.Name {
									case "port":
										server.Port = sf.Value
									case "scheme":
										server.Scheme = sf.Value
									}
								}
								lb.Servers = append(lb.Servers, server)
							}
						}
						svc.LoadBalancer = lb
					}
				}
				tcp.Services[svcNode.Name] = svc
			}
		}
	}
}

// httpServiceHasServers reports whether the named HTTP service resolves to at
// least one usable loadbalancer server. Services defined via a weighted or
// other non-loadbalancer structure have a nil LoadBalancer and must be treated
// as unusable (F4), instead of silently producing a port-0 route.
func httpServiceHasServers(services map[string]*Service, name string) bool {
	svc, ok := services[name]
	if !ok || svc.LoadBalancer == nil {
		return false
	}
	return len(svc.LoadBalancer.Servers) > 0
}

// tcpServiceHasServers is the TCP counterpart of httpServiceHasServers.
func tcpServiceHasServers(services map[string]*TCPService, name string) bool {
	svc, ok := services[name]
	if !ok || svc.LoadBalancer == nil {
		return false
	}
	return len(svc.LoadBalancer.Servers) > 0
}

func resolveHTTPService(services map[string]*Service, name string) (int, string) {
	svc, ok := services[name]
	if !ok || svc.LoadBalancer == nil || len(svc.LoadBalancer.Servers) == 0 {
		return 0, "http"
	}

	server := svc.LoadBalancer.Servers[0]

	// A5: server.url takes priority when present.
	if server.URL != "" {
		u, err := url.Parse(server.URL)
		if err != nil {
			slog.Error("Invalid Traefik service server.url, falling back to scheme/port",
				"service", name, "url", server.URL, "error", err)
		} else {
			port := 0
			if u.Port() != "" {
				p, err := strconv.Atoi(u.Port())
				if err != nil {
					slog.Error("Invalid port in Traefik service server.url, using 0",
						"service", name, "url", server.URL, "port", u.Port(), "error", err)
				} else {
					port = p
				}
			}
			scheme := u.Scheme
			if scheme == "" {
				scheme = server.Scheme
			}
			if scheme == "" {
				scheme = "http"
			}
			return port, scheme
		}
	}

	port := 0
	if server.Port != "" {
		p, err := strconv.Atoi(server.Port)
		if err != nil {
			slog.Error("Invalid Traefik service server.port, using 0",
				"service", name, "value", server.Port, "error", err)
		} else {
			port = p
		}
	}

	scheme := server.Scheme
	if scheme == "" {
		scheme = "http"
	}

	return port, scheme
}

func resolveTCPPort(services map[string]*TCPService, name string) int {
	svc, ok := services[name]
	if !ok || svc.LoadBalancer == nil || len(svc.LoadBalancer.Servers) == 0 {
		return 0
	}

	port, err := strconv.Atoi(svc.LoadBalancer.Servers[0].Port)
	if err != nil {
		slog.Error("Invalid Traefik TCP service server.port, using 0",
			"service", name, "value", svc.LoadBalancer.Servers[0].Port, "error", err)
		return 0
	}
	return port
}

// --- Rule extraction helpers ---
//
// Traefik accepts Host(`x`), Host("x"), and Host('x') delimiter styles in
// practice; the official docs and most migrations use double quotes. The
// regexes below accept all three so users copying a Traefik rule into a
// docktunnel.* label don't silently lose routing.

var hostRegex = regexp.MustCompile(`(?i)Host\(\s*([^)]+)\)`)

// extractHostsFromRule extracts hostnames from Traefik Host() rule patterns.
func extractHostsFromRule(rule string) []string {
	var hostnames []string
	matches := hostRegex.FindAllStringSubmatch(rule, -1)
	for _, match := range matches {
		if len(match) > 1 {
			hostnames = append(hostnames, extractQuoted(match[1])...)
		}
	}
	return hostnames
}

// F2: Path() and HostSNI() clauses accept the same three delimiter styles as
// Host() (backtick, double quote, single quote), matching extractQuoted. A
// single-quoted Path('/api') that passes validateTraefikRule must not be
// silently dropped — that would widen the rule to the whole host.
var pathRegex = regexp.MustCompile(`(?i)Path\(\s*[` + "`" + `"']([^` + "`" + `"']+)[` + "`" + `"']\s*\)`)

// extractPathsFromRule extracts paths from Traefik Path() rule patterns.
func extractPathsFromRule(rule string) []string {
	var paths []string
	matches := pathRegex.FindAllStringSubmatch(rule, -1)
	for _, match := range matches {
		if len(match) > 1 {
			paths = append(paths, match[1])
		}
	}
	return paths
}

// hostSNIRegex captures everything between HostSNI( and ) without restricting
// the quote style, mirroring hostRegex. Multi-value rules like
// HostSNI('a.com','b.com') are then split by extractQuoted; a quoted-only
// single-value match is untouched (P3-1).
var hostSNIRegex = regexp.MustCompile(`(?i)HostSNI\(\s*([^)]+)\)`)

// extractHostSNIFromRule extracts hostnames from Traefik HostSNI() rule patterns.
func extractHostSNIFromRule(rule string) []string {
	var hostnames []string
	matches := hostSNIRegex.FindAllStringSubmatch(rule, -1)
	for _, match := range matches {
		if len(match) > 1 {
			hostnames = append(hostnames, extractQuoted(match[1])...)
		}
	}
	return hostnames
}

// extractQuoted extracts the contents of backtick-, double-quote- or
// single-quote-delimited tokens (e.g. `Host(`a`, `b`)` → ["a","b"]). Used for
// Host() and HostSNI() rules where multiple hosts can be listed comma-separated
// inside one pair of delimiters.
func extractQuoted(s string) []string {
	var results []string
	re := regexp.MustCompile("[`\"']([^`\"']+)[`\"']")
	matches := re.FindAllStringSubmatch(s, -1)
	for _, match := range matches {
		if len(match) > 1 {
			results = append(results, match[1])
		}
	}
	return results
}
