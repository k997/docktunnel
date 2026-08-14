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

	err := validator.Validate(rules, nil)
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}

	// 测试重复主机名情况
	rules["service3"] = &zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{
		Hostname: cloudflare.F("example1.com"), // 与service1重复
		Service:  cloudflare.F("http://localhost:8082"),
	}

	err = validator.Validate(rules, nil)
	if err == nil {
		t.Error("Expected error for duplicate hostname, got none")
	}

	// 测试与现有规则的重复
	allRules := map[string]zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{
		"example3.com": {
			Hostname: cloudflare.F("example3.com"),
			Service:  cloudflare.F("http://localhost:8083"),
		},
	}

	rules2 := map[string]*zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{
		"service4": {
			Hostname: cloudflare.F("example3.com"), // 与现有规则重复
			Service:  cloudflare.F("http://localhost:8084"),
		},
	}

	err = validator.Validate(rules2, allRules)
	if err == nil {
		t.Error("Expected error for duplicate hostname with existing rules, got none")
	}
}

// TestHostnameUniquenessValidator_CaseInsensitive verifies that case
// variants of the same DNS hostname are still treated as a collision.
// DNS is case-insensitive: App.example.com and app.example.com resolve to
// the same record, so both must not pass validation.
func TestHostnameUniquenessValidator_CaseInsensitive(t *testing.T) {
	validator := &HostnameUniquenessValidator{}

	rules := map[string]*zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{
		"service1": {
			Hostname: cloudflare.F("App.Example.COM"),
			Service:  cloudflare.F("http://localhost:8080"),
		},
		"service2": {
			Hostname: cloudflare.F("app.example.com"), // case variant
			Service:  cloudflare.F("http://localhost:8081"),
		},
	}

	err := validator.Validate(rules, nil)
	if err == nil {
		t.Error("Expected error for case-variant duplicate hostname, got none")
	}

	// Also verify against existing rules map.
	existing := map[string]zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{
		"app.example.com": {
			Hostname: cloudflare.F("app.example.com"),
			Service:  cloudflare.F("http://localhost:9000"),
		},
	}
	newRules := map[string]*zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{
		"service2": {
			Hostname: cloudflare.F("APP.EXAMPLE.COM"),
			Service:  cloudflare.F("http://localhost:9001"),
		},
	}
	if err := validator.Validate(newRules, existing); err == nil {
		t.Error("Expected error for case-variant collision with existing rules, got none")
	}
}

// TestHostnameUniquenessValidator_SameHostnameDifferentPaths verifies the B9/B10
// semantics: one hostname may expose multiple paths, so only identical
// (hostname, path) pairs collide.
func TestHostnameUniquenessValidator_SameHostnameDifferentPaths(t *testing.T) {
	validator := &HostnameUniquenessValidator{}

	rules := map[string]*zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{
		"service1": {
			Hostname: cloudflare.F("api.example.com"),
			Path:     cloudflare.F("/v1"),
			Service:  cloudflare.F("http://localhost:8080"),
		},
		"service2": {
			Hostname: cloudflare.F("api.example.com"),
			Path:     cloudflare.F("/v2"),
			Service:  cloudflare.F("http://localhost:8081"),
		},
		"service3": {
			Hostname: cloudflare.F("api.example.com"), // no path
			Service:  cloudflare.F("http://localhost:8082"),
		},
	}

	if err := validator.Validate(rules, nil); err != nil {
		t.Errorf("same hostname with different paths must be allowed, got %v", err)
	}

	// Same (hostname, path) again → collision.
	rules["service4"] = &zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{
		Hostname: cloudflare.F("api.example.com"),
		Path:     cloudflare.F("/v1"),
		Service:  cloudflare.F("http://localhost:8083"),
	}
	if err := validator.Validate(rules, nil); err == nil {
		t.Error("identical (hostname, path) must be rejected")
	}
}

// TestHostnameUniquenessValidator_ExistingRulesWithPaths verifies the
// validator checks new rules against existing (hostname, path) keys,
// allowing a different path on an existing hostname.
func TestHostnameUniquenessValidator_ExistingRulesWithPaths(t *testing.T) {
	validator := &HostnameUniquenessValidator{}

	existing := map[string]zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{
		ingressKey("api.example.com", "/v1"): {
			Hostname: cloudflare.F("api.example.com"),
			Path:     cloudflare.F("/v1"),
			Service:  cloudflare.F("http://localhost:8080"),
		},
	}

	newRules := map[string]*zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{
		"service-new": {
			Hostname: cloudflare.F("api.example.com"),
			Path:     cloudflare.F("/v2"),
			Service:  cloudflare.F("http://localhost:8081"),
		},
	}
	if err := validator.Validate(newRules, existing); err != nil {
		t.Errorf("different path on existing hostname must be allowed, got %v", err)
	}

	// Same (hostname, path) as existing → rejected.
	newRules["service-new2"] = &zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{
		Hostname: cloudflare.F("API.Example.com"), // case-insensitive
		Path:     cloudflare.F("/v1"),
		Service:  cloudflare.F("http://localhost:8082"),
	}
	if err := validator.Validate(newRules, existing); err == nil {
		t.Error("case-variant duplicate (hostname, path) must be rejected")
	}
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

	err := validator.Validate(rules, nil)
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}

	// 测试缺少hostname的情况
	rules["service2"] = &zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{
		Service: cloudflare.F("http://localhost:8081"),
		// 缺少Hostname
	}

	err = validator.Validate(rules, nil)
	if err == nil {
		t.Error("Expected error for missing hostname, got none")
	}

	// 测试缺少service的情况
	rules["service3"] = &zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{
		Hostname: cloudflare.F("example3.com"),
		// 缺少Service
	}

	err = validator.Validate(rules, nil)
	if err == nil {
		t.Error("Expected error for missing service, got none")
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

	err := validator.Validate(rules, nil)
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}

	// 测试验证失败情况
	rules["service3"] = &zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{
		Hostname: cloudflare.F("example1.com"), // 重复主机名
		Service:  cloudflare.F("http://localhost:8082"),
	}

	err = validator.Validate(rules, nil)
	if err == nil {
		t.Error("Expected error for duplicate hostname, got nil")
	}
}

func TestServiceURLValidator(t *testing.T) {
	validator := &ServiceURLValidator{}

	// Test valid URLs
	rules := map[string]*zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{
		"service1": {
			Hostname: cloudflare.F("example1.com"),
			Service:  cloudflare.F("http://localhost:8080"),
		},
		"service2": {
			Hostname: cloudflare.F("example2.com"),
			Service:  cloudflare.F("https://example.com:443"),
		},
		"service3": {
			Hostname: cloudflare.F("example3.com"),
			Service:  cloudflare.F("tcp://192.168.1.1:22"),
		},
	}

	err := validator.Validate(rules, nil)
	if err != nil {
		t.Errorf("Expected no error for valid URLs, got %v", err)
	}

	// Test malformed URL
	rules["service4"] = &zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{
		Hostname: cloudflare.F("example4.com"),
		Service:  cloudflare.F("://invalid-url"),
	}

	err = validator.Validate(rules, nil)
	if err == nil {
		t.Error("Expected error for malformed URL, got none")
	}

	// Test missing scheme
	rules["service5"] = &zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{
		Hostname: cloudflare.F("example5.com"),
		Service:  cloudflare.F("localhost:8080"), // Missing scheme
	}

	err = validator.Validate(rules, nil)
	if err == nil {
		t.Error("Expected error for missing scheme, got none")
	}

	// Test unsupported scheme
	rules["service6"] = &zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{
		Hostname: cloudflare.F("example6.com"),
		Service:  cloudflare.F("ftp://example.com:21"),
	}

	err = validator.Validate(rules, nil)
	if err == nil {
		t.Error("Expected error for unsupported scheme, got none")
	}

	// Test missing host
	rules["service7"] = &zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{
		Hostname: cloudflare.F("example7.com"),
		Service:  cloudflare.F("http://"),
	}

	err = validator.Validate(rules, nil)
	if err == nil {
		t.Error("Expected error for missing host, got none")
	}

	// Test invalid port
	rules["service8"] = &zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{
		Hostname: cloudflare.F("example8.com"),
		Service:  cloudflare.F("http://localhost:99999"),
	}

	err = validator.Validate(rules, nil)
	if err == nil {
		t.Error("Expected error for invalid port, got none")
	}

	// Test special service URL (http_status)
	rules["service9"] = &zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{
		Hostname: cloudflare.F("example9.com"),
		Service:  cloudflare.F("http_status:404"),
	}

	// Clear previous errors and test with only valid + special URL
	rules = map[string]*zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{
		"service9": {
			Hostname: cloudflare.F("example9.com"),
			Service:  cloudflare.F("http_status:404"),
		},
	}
	err = validator.Validate(rules, nil)
	if err != nil {
		t.Errorf("Expected no error for special service URL, got %v", err)
	}
}

func TestExposedPortValidator_Removed(t *testing.T) {
	// ExposedPortValidator was removed (it only logged Debug and never
	// returned an error). If real container-port validation is needed,
	// build it where container info is available (label_parser.go) — not
	// in the rule validator pipeline, which doesn't have access to
	// container.NetworkSettings.Ports.
	t.Log("ExposedPortValidator removed; this test is a placeholder")
}
