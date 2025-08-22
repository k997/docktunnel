package controller

import (
	"testing"
	
	"github.com/cloudflare/cloudflare-go/v5"
	"github.com/cloudflare/cloudflare-go/v5/zero_trust"
)

func TestHostnameUniquenessValidator(t *testing.T) {
	validator := &HostnameUniquenessValidator{}
	
	// 测试正常情况 - 没有重复主机名
	rules := map[string]*zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{
		"service1": {
			Hostname: cloudflare.F("example1.com"),
			Service:  cloudflare.F("http://localhost:8080"),
		},
		"service2": {
			Hostname: cloudflare.F("example2.com"),
			Service:  cloudflare.F("http://localhost:8081"),
		},
	}
	
	err := validator.Validate(rules)
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}
	
	// 测试重复主机名情况
	rules["service3"] = &zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{
		Hostname: cloudflare.F("example1.com"), // 与service1重复
		Service:  cloudflare.F("http://localhost:8082"),
	}
	
	err = validator.Validate(rules)
	if err == nil {
		t.Error("Expected error for duplicate hostname, got nil")
	}
}

func TestServiceNameUniquenessValidator(t *testing.T) {
	validator := &ServiceNameUniquenessValidator{}
	
	// 测试正常情况 - 服务名唯一（这个测试实际上不会失败，因为map的key本身就是唯一的）
	rules := map[string]*zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{
		"service1": {
			Hostname: cloudflare.F("example1.com"),
			Service:  cloudflare.F("http://localhost:8080"),
		},
		"service2": {
			Hostname: cloudflare.F("example2.com"),
			Service:  cloudflare.F("http://localhost:8081"),
		},
	}
	
	err := validator.Validate(rules)
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}
	
	// 注意：由于Go map的特性，我们无法真正测试重复的服务名
	// 因为map的key本身就是唯一的
}

func TestRequiredFieldsValidator(t *testing.T) {
	validator := &RequiredFieldsValidator{}
	
	// 测试正常情况 - 所有必需字段都存在
	rules := map[string]*zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{
		"service1": {
			Hostname: cloudflare.F("example1.com"),
			Service:  cloudflare.F("http://localhost:8080"),
		},
	}
	
	err := validator.Validate(rules)
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}
	
	// 测试缺少主机名
	rules["service2"] = &zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{
		Service: cloudflare.F("http://localhost:8081"),
		// 缺少Hostname
	}
	
	err = validator.Validate(rules)
	if err == nil {
		t.Error("Expected error for missing hostname, got nil")
	}
	
	// 测试缺少服务地址
	rules["service3"] = &zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{
		Hostname: cloudflare.F("example3.com"),
		// 缺少Service
	}
	
	err = validator.Validate(rules)
	if err == nil {
		t.Error("Expected error for missing service, got nil")
	}
}

func TestCompositeValidator(t *testing.T) {
	validator := NewCompositeValidator()
	
	// 测试正常情况 - 所有验证都通过
	rules := map[string]*zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{
		"service1": {
			Hostname: cloudflare.F("example1.com"),
			Service:  cloudflare.F("http://localhost:8080"),
		},
		"service2": {
			Hostname: cloudflare.F("example2.com"),
			Service:  cloudflare.F("http://localhost:8081"),
		},
	}
	
	err := validator.Validate(rules)
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}
	
	// 测试验证失败情况
	rules["service3"] = &zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{
		Hostname: cloudflare.F("example1.com"), // 重复主机名
		Service:  cloudflare.F("http://localhost:8082"),
	}
	
	err = validator.Validate(rules)
	if err == nil {
		t.Error("Expected error for duplicate hostname, got nil")
	}
}