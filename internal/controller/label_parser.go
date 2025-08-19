package controller

import (
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/cloudflare/cloudflare-go"
)

// parseLabelsToIngress 解析容器标签并生成Ingress规则
func parseLabelsToIngress(labels map[string]string, ruleValidator RuleValidator) ([]cloudflare.UnvalidatedIngressRule, error) {
	// 创建临时存储，键是服务名称，值是该服务对应的Ingress规则
	rawRules := make(map[string]*cloudflare.UnvalidatedIngressRule)
	
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
			rule = &cloudflare.UnvalidatedIngressRule{}
		}
		
		// 根据属性设置规则字段
		switch attribute {
		case "hostname":
			rule.Hostname = value
		case "service":
			rule.Service = value
		case "path":
			rule.Path = value
		// 源站请求配置
		case "originRequest.connectTimeout":
			if rule.OriginRequest == nil {
				rule.OriginRequest = &cloudflare.OriginRequestConfig{}
			}
			
			// 解析时间值
			if duration, err := time.ParseDuration(value); err == nil {
				tunnelDuration := &cloudflare.TunnelDuration{Duration: duration}
				rule.OriginRequest.ConnectTimeout = tunnelDuration
			} else {
				slog.Warn("Invalid connect timeout value, skipping", "value", value)
			}
		case "originRequest.tlsTimeout":
			if rule.OriginRequest == nil {
				rule.OriginRequest = &cloudflare.OriginRequestConfig{}
			}
			
			// 解析时间值
			if duration, err := time.ParseDuration(value); err == nil {
				tunnelDuration := &cloudflare.TunnelDuration{Duration: duration}
				rule.OriginRequest.TLSTimeout = tunnelDuration
			} else {
				slog.Warn("Invalid TLS timeout value, skipping", "value", value)
			}
		case "originRequest.tcpKeepAlive":
			if rule.OriginRequest == nil {
				rule.OriginRequest = &cloudflare.OriginRequestConfig{}
			}
			
			// 解析时间值
			if duration, err := time.ParseDuration(value); err == nil {
				tunnelDuration := &cloudflare.TunnelDuration{Duration: duration}
				rule.OriginRequest.TCPKeepAlive = tunnelDuration
			} else {
				slog.Warn("Invalid TCP keep alive value, skipping", "value", value)
			}
		case "originRequest.noHappyEyeballs":
			if rule.OriginRequest == nil {
				rule.OriginRequest = &cloudflare.OriginRequestConfig{}
			}
			
			if value == "true" {
				noHappyEyeballs := true
				rule.OriginRequest.NoHappyEyeballs = &noHappyEyeballs
			} else if value == "false" {
				noHappyEyeballs := false
				rule.OriginRequest.NoHappyEyeballs = &noHappyEyeballs
			}
		case "originRequest.keepAliveConnections":
			if rule.OriginRequest == nil {
				rule.OriginRequest = &cloudflare.OriginRequestConfig{}
			}
			
			if num, err := strconv.Atoi(value); err == nil {
				rule.OriginRequest.KeepAliveConnections = &num
			} else {
				slog.Warn("Invalid keep alive connections value, skipping", "value", value)
			}
		case "originRequest.keepAliveTimeout":
			if rule.OriginRequest == nil {
				rule.OriginRequest = &cloudflare.OriginRequestConfig{}
			}
			
			// 解析时间值
			if duration, err := time.ParseDuration(value); err == nil {
				tunnelDuration := &cloudflare.TunnelDuration{Duration: duration}
				rule.OriginRequest.KeepAliveTimeout = tunnelDuration
			} else {
				slog.Warn("Invalid keep alive timeout value, skipping", "value", value)
			}
		case "originRequest.httpHostHeader":
			if rule.OriginRequest == nil {
				rule.OriginRequest = &cloudflare.OriginRequestConfig{}
			}
			
			httpHostHeader := value
			rule.OriginRequest.HTTPHostHeader = &httpHostHeader
		case "originRequest.originServerName":
			if rule.OriginRequest == nil {
				rule.OriginRequest = &cloudflare.OriginRequestConfig{}
			}
			
			originServerName := value
			rule.OriginRequest.OriginServerName = &originServerName
		case "originRequest.caPool":
			if rule.OriginRequest == nil {
				rule.OriginRequest = &cloudflare.OriginRequestConfig{}
			}
			
			caPool := value
			rule.OriginRequest.CAPool = &caPool
		case "originRequest.noTLSVerify":
			if rule.OriginRequest == nil {
				rule.OriginRequest = &cloudflare.OriginRequestConfig{}
			}
			
			if value == "true" {
				noTLSVerify := true
				rule.OriginRequest.NoTLSVerify = &noTLSVerify
			} else if value == "false" {
				noTLSVerify := false
				rule.OriginRequest.NoTLSVerify = &noTLSVerify
			}
		case "originRequest.disableChunkedEncoding":
			if rule.OriginRequest == nil {
				rule.OriginRequest = &cloudflare.OriginRequestConfig{}
			}
			
			if value == "true" {
				disableChunkedEncoding := true
				rule.OriginRequest.DisableChunkedEncoding = &disableChunkedEncoding
			} else if value == "false" {
				disableChunkedEncoding := false
				rule.OriginRequest.DisableChunkedEncoding = &disableChunkedEncoding
			}
		case "originRequest.bastionMode":
			if rule.OriginRequest == nil {
				rule.OriginRequest = &cloudflare.OriginRequestConfig{}
			}
			
			if value == "true" {
				bastionMode := true
				rule.OriginRequest.BastionMode = &bastionMode
			} else if value == "false" {
				bastionMode := false
				rule.OriginRequest.BastionMode = &bastionMode
			}
		case "originRequest.proxyAddress":
			if rule.OriginRequest == nil {
				rule.OriginRequest = &cloudflare.OriginRequestConfig{}
			}
			
			proxyAddress := value
			rule.OriginRequest.ProxyAddress = &proxyAddress
		case "originRequest.proxyPort":
			if rule.OriginRequest == nil {
				rule.OriginRequest = &cloudflare.OriginRequestConfig{}
			}
			
			if port, err := strconv.ParseUint(value, 10, 32); err == nil {
				port32 := uint(port)
				rule.OriginRequest.ProxyPort = &port32
			} else {
				slog.Warn("Invalid proxy port value, skipping", "value", value)
			}
		case "originRequest.proxyType":
			if rule.OriginRequest == nil {
				rule.OriginRequest = &cloudflare.OriginRequestConfig{}
			}
			
			proxyType := value
			rule.OriginRequest.ProxyType = &proxyType
		case "originRequest.http2Origin":
			if rule.OriginRequest == nil {
				rule.OriginRequest = &cloudflare.OriginRequestConfig{}
			}
			
			if value == "true" {
				http2Origin := true
				rule.OriginRequest.Http2Origin = &http2Origin
			} else if value == "false" {
				http2Origin := false
				rule.OriginRequest.Http2Origin = &http2Origin
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
	var ingressRules []cloudflare.UnvalidatedIngressRule
	for _, rule := range rawRules {
		ingressRules = append(ingressRules, *rule)
	}
	
	// 添加默认的catch-all规则
	ingressRules = append(ingressRules, cloudflare.UnvalidatedIngressRule{
		Service: "http_status:404",
	})
	
	return ingressRules, nil
}