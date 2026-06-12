package label

import (
	"log/slog"
	"regexp"
	"strconv"

	"github.com/docker/docker/api/types/container"
)

// decodeTraefikToSpecs parses traefik.* labels and converts them to IngressSpec map.
func decodeTraefikToSpecs(labels map[string]string, containerInfo *container.InspectResponse) map[string]*IngressSpec {
	specs := map[string]*IngressSpec{}

	conf := decodeTraefikLabels(labels)
	if conf == nil {
		return specs
	}

	// Adapt HTTP routers
	for name, router := range conf.HTTP.Routers {
		hostnames := extractHostsFromRule(router.Rule)
		paths := extractPathsFromRule(router.Rule)

		svcName := router.Service
		if svcName == "" {
			svcName = name
		}

		port, scheme := resolveHTTPService(conf.HTTP.Services, svcName)

		for _, hn := range hostnames {
			if !sanitizeLabelValue("traefik.http.routers."+name+".rule", hn) {
				continue
			}

			spec := &IngressSpec{
				Hostname:      hn,
				Port:          port,
				Scheme:        scheme,
				OriginRequest: &OriginRequestSpec{},
			}
			if len(paths) > 0 {
				spec.Path = paths[0]
			}

			key := name + "@" + hn
			specs[key] = spec
		}
	}

	// Adapt TCP routers
	for name, router := range conf.TCP.Routers {
		hostnames := extractHostSNIFromRule(router.Rule)

		svcName := router.Service
		if svcName == "" {
			svcName = name
		}

		port := resolveTCPPort(conf.TCP.Services, svcName)

		for _, hn := range hostnames {
			if !sanitizeLabelValue("traefik.tcp.routers."+name+".rule", hn) {
				continue
			}

			key := name + "@" + hn
			specs[key] = &IngressSpec{
				Hostname:      hn,
				Port:          port,
				Scheme:        "tcp",
				OriginRequest: &OriginRequestSpec{},
			}
		}
	}

	return specs
}

// decodeTraefikLabels parses traefik.* labels into a minimal Configuration.
func decodeTraefikLabels(labels map[string]string) *Configuration {
	node, err := DecodeToNode(labels, "traefik", "traefik.http", "traefik.tcp")
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
			if len(node.Children) > 0 {
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

func resolveHTTPService(services map[string]*Service, name string) (int, string) {
	svc, ok := services[name]
	if !ok || svc.LoadBalancer == nil || len(svc.LoadBalancer.Servers) == 0 {
		return 0, "http"
	}

	server := svc.LoadBalancer.Servers[0]
	port := 0
	if server.Port != "" {
		port, _ = strconv.Atoi(server.Port)
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

	port, _ := strconv.Atoi(svc.LoadBalancer.Servers[0].Port)
	return port
}

// --- Rule extraction helpers ---

var hostRegex = regexp.MustCompile(`Host\(\s*(` + "`" + `[^` + "`" + `]+` + "`" + `(?:\s*,\s*` + "`" + `[^` + "`" + `]+` + "`" + `)*)\s*\)`)
var backtickRegex = regexp.MustCompile("`([^`]+)`")

// extractHostsFromRule extracts hostnames from Traefik Host() rule patterns.
func extractHostsFromRule(rule string) []string {
	var hostnames []string
	matches := hostRegex.FindAllStringSubmatch(rule, -1)
	for _, match := range matches {
		if len(match) > 1 {
			hostnames = append(hostnames, extractFromBackticks(match[1])...)
		}
	}
	return hostnames
}

var pathRegex = regexp.MustCompile(`Path\(\s*` + "`" + `([^` + "`" + `]+)` + "`" + `\s*\)`)

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

var hostSNIRegex = regexp.MustCompile(`HostSNI\(\s*` + "`" + `([^` + "`" + `]+)` + "`" + `\s*\)`)

// extractHostSNIFromRule extracts hostnames from Traefik HostSNI() rule patterns.
func extractHostSNIFromRule(rule string) []string {
	var hostnames []string
	matches := hostSNIRegex.FindAllStringSubmatch(rule, -1)
	for _, match := range matches {
		if len(match) > 1 {
			hostnames = append(hostnames, match[1])
		}
	}
	return hostnames
}

// extractFromBackticks extracts strings from backtick-delimited content.
func extractFromBackticks(s string) []string {
	var results []string
	matches := backtickRegex.FindAllStringSubmatch(s, -1)
	for _, match := range matches {
		if len(match) > 1 {
			results = append(results, match[1])
		}
	}
	return results
}
