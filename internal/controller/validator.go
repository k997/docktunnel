package controller

import (
	"fmt"
	
	"github.com/cloudflare/cloudflare-go/v5/zero_trust"
)

// RuleValidator 定义规则验证器接口，适配cloudflare-go/v5
type RuleValidator interface {
	Validate(rules map[string]*zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress, existingRules map[string]zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress) error
}

// HostnameUniquenessValidator 验证主机名唯一性
type HostnameUniquenessValidator struct{}

func (v *HostnameUniquenessValidator) Validate(rules map[string]*zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress, existingRules map[string]zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress) error {
	hostnameMap := make(map[string]string) // hostname -> service name

	// 从现有规则中填充主机名
	if existingRules != nil {
		for hostname := range existingRules {
			hostnameMap[hostname] = "an existing service"
		}
	}

	// 检查新规则中的主机名
	for serviceName, rule := range rules {
		if existingService, exists := hostnameMap[rule.Hostname.Value]; exists {
			return fmt.Errorf("duplicate hostname %s found. It is already used by %s, and new service %s also tries to use it", rule.Hostname.Value, existingService, serviceName)
		}

		hostnameMap[rule.Hostname.Value] = serviceName
	}
	return nil
}

// ServiceNameUniquenessValidator 验证服务名唯一性
type ServiceNameUniquenessValidator struct{}

func (v *ServiceNameUniquenessValidator) Validate(rules map[string]*zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress, _ map[string]zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress) error {
	serviceNameMap := make(map[string]bool)
	for serviceName := range rules {
		if serviceNameMap[serviceName] {
			return fmt.Errorf("duplicate service name found: %s", serviceName)
		}
		serviceNameMap[serviceName] = true
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

// CompositeValidator 组合多个验证器
type CompositeValidator struct {
	validators []RuleValidator
}

func NewCompositeValidator() *CompositeValidator {
	return &CompositeValidator{
		validators: []RuleValidator{
			&ServiceNameUniquenessValidator{},
			&RequiredFieldsValidator{},
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