package controller

import (
	"fmt"
	
	"github.com/cloudflare/cloudflare-go"
)

// RuleValidator 定义规则验证器接口
type RuleValidator interface {
	Validate(rules map[string]*cloudflare.UnvalidatedIngressRule) error
}

// HostnameUniquenessValidator 验证主机名唯一性
type HostnameUniquenessValidator struct{}

func (v *HostnameUniquenessValidator) Validate(rules map[string]*cloudflare.UnvalidatedIngressRule) error {
	hostnameMap := make(map[string]string) // hostname -> service name
	for serviceName, rule := range rules {
		if rule.Hostname == "" {
			return fmt.Errorf("service %s missing required hostname", serviceName)
		}
		
		if existingService, exists := hostnameMap[rule.Hostname]; exists {
			return fmt.Errorf("duplicate hostname %s found in services %s and %s", rule.Hostname, existingService, serviceName)
		}
		
		hostnameMap[rule.Hostname] = serviceName
	}
	return nil
}

// ServiceNameUniquenessValidator 验证服务名唯一性
type ServiceNameUniquenessValidator struct{}

func (v *ServiceNameUniquenessValidator) Validate(rules map[string]*cloudflare.UnvalidatedIngressRule) error {
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

func (v *RequiredFieldsValidator) Validate(rules map[string]*cloudflare.UnvalidatedIngressRule) error {
	for serviceName, rule := range rules {
		if rule.Hostname == "" {
			return fmt.Errorf("service %s missing required hostname", serviceName)
		}
		
		if rule.Service == "" {
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

func (v *CompositeValidator) Validate(rules map[string]*cloudflare.UnvalidatedIngressRule) error {
	for _, validator := range v.validators {
		if err := validator.Validate(rules); err != nil {
			return err
		}
	}
	return nil
}