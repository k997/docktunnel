package controller

import (
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/cloudflare/cloudflare-go/v5"
	"github.com/cloudflare/cloudflare-go/v5/zero_trust"
	"github.com/docker/docker/api/types/container"
)

// parseLabelsToIngress 解析容器标签并生成Ingress规则，适配cloudflare-go/v5
func parseLabelsToIngress(containerInfo *container.InspectResponse) (map[string]*zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress, error) {
	// 检查containerInfo是否为nil
	if containerInfo == nil || containerInfo.Config == nil || containerInfo.Config.Labels == nil {
		// 返回错误而不是默认规则，因为没有容器信息是无效配置
		return nil, fmt.Errorf("no valid ingress rules found")
	}

	labels := containerInfo.Config.Labels

	// 创建临时存储，键是服务名称，值是该服务对应的Ingress规则
	rawRules := make(map[string]*zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress)

	// 遍历所有标签，解析其内容
	for label, value := range labels {
		// 检查是否是docktunnel标签
		if !strings.HasPrefix(label, "docktunnel.") {
			continue
		}

		// 解析标签格式: docktunnel.<service-name>.<attribute>
		parts := strings.Split(label, ".")
		if len(parts) < 3 {
			// 特殊处理docktunnel.enable标签，它只有两部分
			if len(parts) == 2 && parts[1] == "enable" {
				// 这是启用标签，不需要特殊处理，继续解析其他标签
				continue
			}
			slog.Warn("Invalid label format, skipping", "label", label)
			continue
		}

		serviceName := parts[1]
		attribute := strings.Join(parts[2:], ".") // 处理多级属性如 originRequest.noTLSVerify

		// 获取或创建该服务的规则
		rule, exists := rawRules[serviceName]
		if !exists {
			rule = &zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{}
		}

		// 根据属性设置规则字段
		switch attribute {
		case "hostname":
			rule.Hostname = cloudflare.F(value)
		case "service":
			rule.Service = cloudflare.F(value)
		case "port":
			// port标签，仅当没有设置service时使用
			if rule.Service.Value == "" {
				// 如果有容器信息，我们可以生成服务地址
				if containerInfo.NetworkSettings != nil {
					// 默认scheme为http
					scheme := "http"
					// 优先检查scheme标签（Traefik兼容）
					schemeLabel := "docktunnel." + serviceName + ".scheme"
					if schemeValue, exists := labels[schemeLabel]; exists {
						slog.Info("Using scheme label", "service", serviceName, "scheme", schemeValue)
						scheme = schemeValue
					} else {
						// 回退到proto标签（向后兼容）
						protoLabel := "docktunnel." + serviceName + ".proto"
						if protoValue, exists := labels[protoLabel]; exists {
							slog.Info("Using proto label (deprecated, use scheme)", "service", serviceName, "proto", protoValue)
							scheme = protoValue
						}
					}

					// 获取容器IP地址
					containerIP := getContainerIP(containerInfo)
					if containerIP != "" {
						rule.Service = cloudflare.F(scheme + "://" + containerIP + ":" + value)
						slog.Info("Auto-detected service URL from port label", "service", serviceName, "url", rule.Service.Value, "source", "port+auto-detect")
					}
				}
			}
		case "scheme":
			// scheme标签，仅当没有设置service且有port时使用
			// 不单独处理scheme，只在处理port时检查scheme的值
			// 这里不需要做任何事情，因为scheme的处理已经在port中完成了
		case "proto":
			// proto标签已弃用，仅用于向后兼容
			// 不单独处理，已在case "port"中处理
		case "path":
			rule.Path = cloudflare.F(value)
		// 源站请求配置
		case "originRequest.connectTimeout":
			// 初始化OriginRequest字段
			if !rule.OriginRequest.Present {
				originRequest := zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngressOriginRequest{}
				rule.OriginRequest = cloudflare.F(originRequest)
			}

			// 解析时间值
			if duration, err := time.ParseDuration(value); err == nil {
				seconds := int64(duration.Seconds())
				rule.OriginRequest.Value.ConnectTimeout = cloudflare.F(seconds)
			} else {
				slog.Warn("Invalid connect timeout value, skipping", "value", value)
			}
		case "originRequest.tlsTimeout":
			// 初始化OriginRequest字段
			if !rule.OriginRequest.Present {
				originRequest := zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngressOriginRequest{}
				rule.OriginRequest = cloudflare.F(originRequest)
			}

			// 解析时间值
			if duration, err := time.ParseDuration(value); err == nil {
				seconds := int64(duration.Seconds())
				rule.OriginRequest.Value.TLSTimeout = cloudflare.F(seconds)
			} else {
				slog.Warn("Invalid TLS timeout value, skipping", "value", value)
			}
		case "originRequest.tcpKeepAlive":
			// 初始化OriginRequest字段
			if !rule.OriginRequest.Present {
				originRequest := zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngressOriginRequest{}
				rule.OriginRequest = cloudflare.F(originRequest)
			}

			// 解析时间值
			if duration, err := time.ParseDuration(value); err == nil {
				seconds := int64(duration.Seconds())
				rule.OriginRequest.Value.TCPKeepAlive = cloudflare.F(seconds)
			} else {
				slog.Warn("Invalid TCP keep alive value, skipping", "value", value)
			}
		case "originRequest.noHappyEyeballs":
			// 初始化OriginRequest字段
			if !rule.OriginRequest.Present {
				originRequest := zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngressOriginRequest{}
				rule.OriginRequest = cloudflare.F(originRequest)
			}

			if value == "true" {
				rule.OriginRequest.Value.NoHappyEyeballs = cloudflare.F(true)
			} else if value == "false" {
				rule.OriginRequest.Value.NoHappyEyeballs = cloudflare.F(false)
			}
		case "originRequest.keepAliveConnections":
			// 初始化OriginRequest字段
			if !rule.OriginRequest.Present {
				originRequest := zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngressOriginRequest{}
				rule.OriginRequest = cloudflare.F(originRequest)
			}

			if num, err := strconv.Atoi(value); err == nil {
				rule.OriginRequest.Value.KeepAliveConnections = cloudflare.F(int64(num))
			} else {
				slog.Warn("Invalid keep alive connections value, skipping", "value", value)
			}
		case "originRequest.keepAliveTimeout":
			// 初始化OriginRequest字段
			if !rule.OriginRequest.Present {
				originRequest := zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngressOriginRequest{}
				rule.OriginRequest = cloudflare.F(originRequest)
			}

			// 解析时间值
			if duration, err := time.ParseDuration(value); err == nil {
				seconds := int64(duration.Seconds())
				rule.OriginRequest.Value.KeepAliveTimeout = cloudflare.F(seconds)
			} else {
				slog.Warn("Invalid keep alive timeout value, skipping", "value", value)
			}
		case "originRequest.httpHostHeader":
			// 初始化OriginRequest字段
			if !rule.OriginRequest.Present {
				originRequest := zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngressOriginRequest{}
				rule.OriginRequest = cloudflare.F(originRequest)
			}

			rule.OriginRequest.Value.HTTPHostHeader = cloudflare.F(value)
		case "originRequest.originServerName":
			// 初始化OriginRequest字段
			if !rule.OriginRequest.Present {
				originRequest := zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngressOriginRequest{}
				rule.OriginRequest = cloudflare.F(originRequest)
			}

			rule.OriginRequest.Value.OriginServerName = cloudflare.F(value)
		case "originRequest.caPool":
			// 初始化OriginRequest字段
			if !rule.OriginRequest.Present {
				originRequest := zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngressOriginRequest{}
				rule.OriginRequest = cloudflare.F(originRequest)
			}

			rule.OriginRequest.Value.CAPool = cloudflare.F(value)
		case "originRequest.noTLSVerify":
			// 初始化OriginRequest字段
			if !rule.OriginRequest.Present {
				originRequest := zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngressOriginRequest{}
				rule.OriginRequest = cloudflare.F(originRequest)
			}

			if value == "true" {
				rule.OriginRequest.Value.NoTLSVerify = cloudflare.F(true)
			} else if value == "false" {
				rule.OriginRequest.Value.NoTLSVerify = cloudflare.F(false)
			}
		case "originRequest.disableChunkedEncoding":
			// 初始化OriginRequest字段
			if !rule.OriginRequest.Present {
				originRequest := zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngressOriginRequest{}
				rule.OriginRequest = cloudflare.F(originRequest)
			}

			if value == "true" {
				rule.OriginRequest.Value.DisableChunkedEncoding = cloudflare.F(true)
			} else if value == "false" {
				rule.OriginRequest.Value.DisableChunkedEncoding = cloudflare.F(false)
			}
		case "originRequest.proxyType":
			// 初始化OriginRequest字段
			if !rule.OriginRequest.Present {
				originRequest := zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngressOriginRequest{}
				rule.OriginRequest = cloudflare.F(originRequest)
			}

			rule.OriginRequest.Value.ProxyType = cloudflare.F(value)
		case "originRequest.http2Origin":
			// 初始化OriginRequest字段
			if !rule.OriginRequest.Present {
				originRequest := zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngressOriginRequest{}
				rule.OriginRequest = cloudflare.F(originRequest)
			}

			if value == "true" {
				rule.OriginRequest.Value.HTTP2Origin = cloudflare.F(true)
			} else if value == "false" {
				rule.OriginRequest.Value.HTTP2Origin = cloudflare.F(false)
			}
		}

		// 更新规则
		rawRules[serviceName] = rule
	}

	// Step 2: If no DockTunnel rules found, try Traefik labels as fallback (4-layer priority: DockTunnel → Traefik → Auto-detect → Defaults)
	if len(rawRules) == 0 {
		slog.Info("No DockTunnel labels found, checking for Traefik labels as fallback")
		traefikRules := parseTraefikLabels(labels, containerInfo)
		if len(traefikRules) > 0 {
			slog.Info("Found Traefik labels, using them as fallback", "ruleCount", len(traefikRules))
			return traefikRules, nil
		}
		slog.Info("No Traefik labels found either")
	}

	// 如果没有规则，返回错误
	if len(rawRules) == 0 {
		return nil, fmt.Errorf("no valid ingress rules found")
	}

	return rawRules, nil
}

// getContainerIP 获取容器的IP地址
func getContainerIP(containerInfo *container.InspectResponse) string {
	if containerInfo.NetworkSettings == nil {
		return ""
	}

	if containerInfo.HostConfig == nil {
		return ""
	}

	// 检查是否使用host网络模式
	if containerInfo.HostConfig.NetworkMode == "host" {
		// 对于host网络模式，使用localhost
		return "localhost"
	}

	// 优先使用bridge网络模式的IP
	if network, exists := containerInfo.NetworkSettings.Networks["bridge"]; exists && network.IPAddress != "" {
		return network.IPAddress
	}

	// 如果没有bridge网络，使用第一个找到的网络IP
	for _, network := range containerInfo.NetworkSettings.Networks {
		if network.IPAddress != "" {
			return network.IPAddress
		}
	}

	return ""
}

// parseTraefikLabels parses Traefik v2 labels as fallback
// Supports: traefik.http.routers.<name>.rule with Host() patterns
//          traefik.http.services.<name>.loadbalancer.server.port
func parseTraefikLabels(labels map[string]string, containerInfo *container.InspectResponse) map[string]*zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress {
	rules := make(map[string]*zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress)

	// Parse router rules to get hostname → service name mapping
	routerServices := make(map[string]string) // routerName → serviceName
	routerHostnames := make(map[string][]string) // routerName → hostnames

	for label, value := range labels {
		if !strings.HasPrefix(label, "traefik.http.routers.") {
			continue
		}

		// Parse traefik.http.routers.<name>.rule
		if strings.HasSuffix(label, ".rule") {
			// Extract router name: traefik.http.routers.<name>.rule
			parts := strings.Split(label, ".")
			if len(parts) >= 4 {
				routerName := parts[3]
				hostnames := extractHostnamesFromRule(value)
				if len(hostnames) > 0 {
					routerHostnames[routerName] = hostnames
					slog.Info("Extracted hostnames from Traefik rule", "router", routerName, "hostnames", hostnames)
				}
			}
		}

		// Parse traefik.http.routers.<name>.service
		if strings.HasSuffix(label, ".service") {
			parts := strings.Split(label, ".")
			if len(parts) >= 4 {
				routerName := parts[3]
				routerServices[routerName] = value
			}
		}
	}

	// Parse service configurations
	servicePorts := make(map[string]int)
	for label, value := range labels {
		if !strings.HasPrefix(label, "traefik.http.services.") {
			continue
		}

		// Parse traefik.http.services.<name>.loadbalancer.server.port
		if strings.HasSuffix(label, ".loadbalancer.server.port") {
			// Extract service name: traefik.http.services.<name>.loadbalancer.server.port
			parts := strings.Split(label, ".")
			if len(parts) >= 4 {
				serviceName := parts[3]
				if port, err := strconv.Atoi(value); err == nil {
					servicePorts[serviceName] = port
					slog.Info("Extracted port from Traefik service", "service", serviceName, "port", port)
				}
			}
		}
	}

	// Build rules from Traefik configuration
	for routerName, hostnames := range routerHostnames {
		serviceName, hasService := routerServices[routerName]
		if !hasService {
			// Use router name as service name if not specified
			serviceName = routerName
		}

		for _, hostname := range hostnames {
			// Skip if we already have a rule for this hostname
			if _, exists := rules[hostname]; exists {
				continue
			}

			rule := &zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{
				Hostname: cloudflare.F(hostname),
			}

			// Try to build service URL from Traefik service configuration
			if port, hasPort := servicePorts[serviceName]; hasPort {
				containerIP := getContainerIP(containerInfo)
				if containerIP != "" {
					rule.Service = cloudflare.F("http://" + containerIP + ":" + strconv.Itoa(port))
					slog.Info("Built service URL from Traefik config", "hostname", hostname, "url", rule.Service.Value)
				}
			}

			rules[hostname] = rule
		}
	}

	return rules
}

// extractHostnamesFromRule extracts hostnames from Traefik router rule
// Supports: Host(`example.com`), Host(`a.com`, `b.com`), Host(`example.com`) && Path(`/api`)
func extractHostnamesFromRule(rule string) []string {
	var hostnames []string

	// Look for Host(`pattern`) patterns with flexible spacing
	// This regex captures everything between Host( and the closing )
	hostRegex := regexp.MustCompile(`Host\(\s*(` + "`[^`]+`" + `(?:\s*,\s*` + "`[^`]+`" + `)*)\s*\)`)
	matches := hostRegex.FindAllStringSubmatch(rule, -1)

	for _, match := range matches {
		if len(match) > 1 {
			// match[1] contains the backtick-delimited hostnames like `a.com`, `b.com`, `c.com`
			hostnamesList := extractFromBackticks(match[1])
			hostnames = append(hostnames, hostnamesList...)
		}
	}

	return hostnames
}

// extractFromBackticks extracts all strings from backtick-delimited content
// Input: "`a.com`, `b.com`, `c.com`"
// Output: ["a.com", "b.com", "c.com"]
func extractFromBackticks(s string) []string {
	var results []string
	backtickRegex := regexp.MustCompile("`([^`]+)`")
	matches := backtickRegex.FindAllStringSubmatch(s, -1)

	for _, match := range matches {
		if len(match) > 1 {
			results = append(results, match[1])
		}
	}

	return results
}
