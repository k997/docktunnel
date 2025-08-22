package controller

import (
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/cloudflare/cloudflare-go/v5"
	"github.com/cloudflare/cloudflare-go/v5/zero_trust"
	"github.com/docker/docker/api/types/container"
)

// parseLabelsToIngress 解析容器标签并生成Ingress规则，适配cloudflare-go/v5
func parseLabelsToIngress(containerInfo *container.InspectResponse, ruleValidator RuleValidator) ([]zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress, error) {
	// 检查containerInfo和Config是否存在
	if containerInfo == nil || containerInfo.Config == nil || containerInfo.Config.Labels == nil {
		// 返回空规则列表而不是错误，因为没有标签是有效的情况
		return []zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{
			{
				Service: cloudflare.F("http_status:404"),
			},
		}, nil
	}

	// 获取容器标签
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
					// 默认协议为http
					proto := "http"
					// 检查是否已设置proto标签
					protoLabel := "docktunnel." + serviceName + ".proto"
					if protoValue, exists := labels[protoLabel]; exists {
						proto = protoValue
					}

					// 获取容器IP地址
					containerIP := getContainerIP(containerInfo)
					if containerIP != "" {
						rule.Service = cloudflare.F(proto + "://" + containerIP + ":" + value)
					}
				}
			}
		case "proto":
			// proto标签，仅当没有设置service且有port时使用
			// 不单独处理proto，只在处理port时检查proto的值
			// 这里不需要做任何事情，因为proto的处理已经在port中完成了
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

	// 验证规则
	if err := ruleValidator.Validate(rawRules); err != nil {
		return nil, err
	}

	// 转换为Cloudflare Ingress规则列表
	var ingressRules []zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress
	for _, rule := range rawRules {
		ingressRules = append(ingressRules, *rule)
	}

	// 添加默认的catch-all规则
	ingressRules = append(ingressRules, zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{
		Service: cloudflare.F("http_status:404"),
	})

	return ingressRules, nil
}

// getContainerIP 获取容器的IP地址
func getContainerIP(containerInfo *container.InspectResponse) string {
	if containerInfo.NetworkSettings == nil {
		return ""
	}

	// 检查是否使用host网络模式
	if containerInfo.HostConfig != nil && containerInfo.HostConfig.NetworkMode == "host" {
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
