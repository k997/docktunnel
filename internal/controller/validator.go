package controller

import (
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strings"

	"github.com/cloudflare/cloudflare-go/v5/zero_trust"
)

// RuleValidator 定义规则验证器接口，适配cloudflare-go/v5
type RuleValidator interface {
	Validate(rules map[string]*zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress, existingRules map[string]zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress) error
}

// HostnameUniquenessValidator 验证 (hostname, path) 键的唯一性
//
// Since B10 the route identity is (hostname, path): one hostname may expose
// multiple paths (README microservice scenario), so only identical
// (hostname, path) pairs collide. DNS is case-insensitive, so hostnames are
// lowercased before keying — App.example.com and app.example.com resolve to
// the same record.
type HostnameUniquenessValidator struct{}

func (v *HostnameUniquenessValidator) Validate(rules map[string]*zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress, existingRules map[string]zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress) error {
	// (lowercased hostname, path) -> service name, for error messages
	routeMap := make(map[string]string)

	// 从现有规则中填充 (hostname, path) 键。existingRules 的 map 键是
	// ingressKey(hostname, path)（B10），这里直接读规则字段更可靠。
	if existingRules != nil {
		for _, rule := range existingRules {
			key := strings.ToLower(rule.Hostname.Value) + "\x00" + rule.Path.Value
			routeMap[key] = "an existing service"
		}
	}

	// 检查新规则中的 (hostname, path) 键
	for serviceName, rule := range rules {
		key := strings.ToLower(rule.Hostname.Value) + "\x00" + rule.Path.Value
		if existingService, exists := routeMap[key]; exists {
			return fmt.Errorf("duplicate route %s%s found. It is already used by %s, and new service %s also tries to use it",
				rule.Hostname.Value, rule.Path.Value, existingService, serviceName)
		}
		routeMap[key] = serviceName
	}
	return nil
}

// RequiredFieldsValidator 验证必需字段
type RequiredFieldsValidator struct{}

func (v *RequiredFieldsValidator) Validate(rules map[string]*zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress, _ map[string]zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress) error {
	for serviceName, rule := range rules {
		if rule.Hostname.Value == "" {
			return fmt.Errorf("service %s missing required hostname", serviceName)
		}

		if rule.Service.Value == "" {
			return fmt.Errorf("service %s missing required service", serviceName)
		}
	}
	return nil
}

// ServiceURLValidator 验证服务URL格式 (T094)
type ServiceURLValidator struct{}

func (v *ServiceURLValidator) Validate(rules map[string]*zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress, _ map[string]zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress) error {
	for serviceName, rule := range rules {
		serviceURL := rule.Service.Value

		// Check if it's a special service URL (http_status, etc.)
		if strings.HasPrefix(serviceURL, "http_status:") || strings.HasPrefix(serviceURL, "ssh:") {
			continue
		}

		// Parse URL to validate format
		parsedURL, err := url.Parse(serviceURL)
		if err != nil {
			slog.Warn("Service URL is malformed",
				"service", serviceName,
				"url", serviceURL,
				"error", err)
			return fmt.Errorf("service %s has malformed service URL: %s: %w", serviceName, serviceURL, err)
		}

		// Validate scheme
		if parsedURL.Scheme == "" {
			return fmt.Errorf("service %s missing URL scheme: %s", serviceName, serviceURL)
		}

		// Validate scheme is supported
		supportedSchemes := map[string]bool{
			"http":  true,
			"https": true,
			"tcp":   true,
			"ssh":   true,
			"ws":    true,
			"wss":   true,
		}
		if !supportedSchemes[parsedURL.Scheme] {
			return fmt.Errorf("service %s has unsupported URL scheme: %s (supported: http, https, tcp, ssh, ws, wss)", serviceName, parsedURL.Scheme)
		}

		// Validate host is present
		if parsedURL.Host == "" {
			return fmt.Errorf("service %s missing host in service URL: %s", serviceName, serviceURL)
		}

		// Validate host format (host:port)
		host, port, err := net.SplitHostPort(parsedURL.Host)
		if err != nil {
			// If there's no port, check if it's just a host
			if strings.Contains(err.Error(), "missing port") {
				// URLs without ports might be valid for some schemes
				slog.Debug("Service URL has no explicit port",
					"service", serviceName,
					"url", serviceURL)
				continue
			}
			return fmt.Errorf("service %s has invalid host format: %s: %w", serviceName, parsedURL.Host, err)
		}

		// Validate host is not empty
		if host == "" {
			return fmt.Errorf("service %s has empty host in service URL: %s", serviceName, serviceURL)
		}

		// Validate port is in valid range
		if port != "" {
			portNum, err := net.LookupPort("tcp", port)
			if err != nil {
				return fmt.Errorf("service %s has invalid port in service URL: %s: %w", serviceName, port, err)
			}
			if portNum < 1 || portNum > 65535 {
				return fmt.Errorf("service %s has port out of range in service URL: %d", serviceName, portNum)
			}
		}
	}
	return nil
}

// CompositeValidator 组合多个验证器
type CompositeValidator struct {
	validators []RuleValidator
}

func NewCompositeValidator() *CompositeValidator {
	return &CompositeValidator{
		validators: []RuleValidator{
			&RequiredFieldsValidator{},
			&ServiceURLValidator{},
			&HostnameUniquenessValidator{},
		},
	}
}

func (v *CompositeValidator) Validate(rules map[string]*zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress, existingRules map[string]zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress) error {
	for _, validator := range v.validators {
		if err := validator.Validate(rules, existingRules); err != nil {
			return err
		}
	}
	return nil
}
