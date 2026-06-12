package controller

import (
	"context"
	"log/slog"
	"testing"
	"time"

	containerTypes "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"

	"docktunnel/internal/events"
	"docktunnel/internal/state"
	"docktunnel/pkg/types"

	"github.com/cloudflare/cloudflare-go/v5"
	"github.com/cloudflare/cloudflare-go/v5/dns"
	"github.com/cloudflare/cloudflare-go/v5/zero_trust"
)

// mockCloudflareManager 是一个模拟的Cloudflare管理器，用于测试
type mockCloudflareManager struct {
	tunnel *zero_trust.TunnelCloudflaredGetResponse
}

// GetTunnel 返回模拟的隧道信息
func (m *mockCloudflareManager) GetTunnel() *zero_trust.TunnelCloudflaredGetResponse {
	return m.tunnel
}

// UpdateConfiguration 模拟更新配置
func (m *mockCloudflareManager) UpdateConfiguration(ctx context.Context, ingressRules []zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress) error {
	return nil
}

// ListDNSRecords 模拟列出DNS记录
func (m *mockCloudflareManager) ListDNSRecords(ctx context.Context) ([]dns.RecordResponse, error) {
	return []dns.RecordResponse{}, nil
}

// DeleteDNSRecords 模拟批量删除DNS记录
func (m *mockCloudflareManager) DeleteDNSRecords(ctx context.Context, hostnames []string) error {
	return nil
}

// UpsertDNSRecords 模拟批量创建或更新DNS记录
func (m *mockCloudflareManager) UpsertDNSRecords(ctx context.Context, hostnames []string) error {
	return nil
}

func TestNewController(t *testing.T) {
	// 测试创建控制器实例
	controller := &Controller{
		ingressRules:    make(map[string]zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress),
		containerRules:  make(map[string][]string),
		containerHealth: make(map[string]*ContainerHealth),
		ruleValidator:   NewCompositeValidator(),
	}

	if controller == nil {
		t.Error("Controller should not be nil")
	}

	// 检查初始状态（不再存储catch-all规则）
	if len(controller.ingressRules) != 0 {
		t.Errorf("Expected no rules initially, got %d", len(controller.ingressRules))
	}

	// 检查通过GetIngressRules方法可以获取到catch-all规则
	rules := controller.GetIngressRules()
	if len(rules) == 0 {
		t.Error("Expected to get rules from GetIngressRules, got none")
		return
	}

	// 检查最后一个规则是否为catch-all规则
	lastRule := rules[len(rules)-1]
	if lastRule.Service.Value != "http_status:404" {
		t.Errorf("Expected last rule to be catch-all with service 'http_status:404', got '%s'", lastRule.Service.Value)
	}
}

func TestNewControllerWithOptions(t *testing.T) {
	// 测试使用ControllerOptions创建控制器实例
	opts := ControllerOptions{
		CatchAllService:   "http_status:404",
		FlappingWindow:    60 * time.Second,
		FlappingThreshold: 5,
		CoolingPeriod:     300 * time.Second,
		MaxCoolingPeriod:  1800 * time.Second,
		DebounceDuration:  2 * time.Second,
		ReconcileEnabled:  true,
		ReconcileInterval: 120 * time.Second,
	}

	controller := NewController(nil, nil, opts)

	if controller == nil {
		t.Error("Controller should not be nil")
	}

	// 检查配置选项是否正确应用
	if controller.flappingWindow != 60*time.Second {
		t.Errorf("Expected flappingWindow to be 60s, got %v", controller.flappingWindow)
	}

	if controller.flappingThreshold != 5 {
		t.Errorf("Expected flappingThreshold to be 5, got %d", controller.flappingThreshold)
	}

	if controller.coolingPeriod != 300*time.Second {
		t.Errorf("Expected coolingPeriod to be 300s, got %v", controller.coolingPeriod)
	}

	if controller.maxCoolingPeriod != 1800*time.Second {
		t.Errorf("Expected maxCoolingPeriod to be 1800s, got %v", controller.maxCoolingPeriod)
	}

	if controller.debounceDuration != 2*time.Second {
		t.Errorf("Expected debounceDuration to be 2s, got %v", controller.debounceDuration)
	}

	// Test new reconcile configuration defaults
	if controller.reconcileEnabled != true {
		t.Errorf("Expected reconcile enabled 'true', got %v", controller.reconcileEnabled)
	}

	if controller.reconcileInterval != 120*time.Second {
		t.Errorf("Expected reconcile interval '120s', got %v", controller.reconcileInterval)
	}

	// 检查初始状态（不再存储catch-all规则）
	if len(controller.ingressRules) != 0 {
		t.Errorf("Expected no rules initially, got %d", len(controller.ingressRules))
	}

	// 检查通过GetIngressRules方法可以获取到catch-all规则
	rules := controller.GetIngressRules()
	if len(rules) == 0 {
		t.Error("Expected to get rules from GetIngressRules, got none")
		return
	}

	// 检查最后一个规则是否为catch-all规则
	lastRule := rules[len(rules)-1]
	if lastRule.Service.Value != "http_status:404" {
		t.Errorf("Expected last rule to be catch-all with service 'http_status:404', got '%s'", lastRule.Service.Value)
	}
}

func TestControllerOptions(t *testing.T) {
	// 测试ControllerOptions结构体
	opts := ControllerOptions{
		CatchAllService:   "http_status:404",
		FlappingWindow:    60 * time.Second,
		FlappingThreshold: 5,
		CoolingPeriod:     300 * time.Second,
		MaxCoolingPeriod:  1800 * time.Second,
		DebounceDuration:  2 * time.Second,
		ReconcileEnabled:  true,
		ReconcileInterval: 120 * time.Second,
	}

	if opts.CatchAllService != "http_status:404" {
		t.Errorf("Expected CatchAllService to be 'http_status:404', got %s", opts.CatchAllService)
	}

	if opts.FlappingWindow != 60*time.Second {
		t.Errorf("Expected FlappingWindow to be 60s, got %v", opts.FlappingWindow)
	}

	if opts.FlappingThreshold != 5 {
		t.Errorf("Expected FlappingThreshold to be 5, got %d", opts.FlappingThreshold)
	}

	if opts.CoolingPeriod != 300*time.Second {
		t.Errorf("Expected CoolingPeriod to be 300s, got %v", opts.CoolingPeriod)
	}

	if opts.MaxCoolingPeriod != 1800*time.Second {
		t.Errorf("Expected MaxCoolingPeriod to be 1800s, got %v", opts.MaxCoolingPeriod)
	}

	if opts.DebounceDuration != 2*time.Second {
		t.Errorf("Expected DebounceDuration to be 2s, got %v", opts.DebounceDuration)
	}

	if opts.ReconcileEnabled != true {
		t.Errorf("Expected ReconcileEnabled to be 'true', got %v", opts.ReconcileEnabled)
	}

	if opts.ReconcileInterval != 120*time.Second {
		t.Errorf("Expected ReconcileInterval to be 120s, got %v", opts.ReconcileInterval)
	}
}

func TestCleanupResourcesLogic(t *testing.T) {
	// 创建控制器实例
	opts := ControllerOptions{
		CatchAllService: "http_status:410",
	}
	controller := NewController(nil, nil, opts)

	// 添加一些测试规则
	controller.ingressRules["example.com"] = zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{
		Hostname: cloudflare.F("example.com"),
		Service:  cloudflare.F("http://localhost:8080"),
	}

	controller.ingressRules["test.com"] = zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{
		Hostname: cloudflare.F("test.com"),
		Service:  cloudflare.F("http://localhost:3000"),
	}

	controller.containerRules["container1"] = []string{"example.com", "test.com"}
	controller.containerRules["container2"] = []string{"test.com"}

	// 检查规则是否正确添加
	rules := controller.GetIngressRules()
	if len(rules) != 3 { // 2个普通规则 + 1个catch-all规则
		t.Errorf("Expected 3 ingress rules, got %d", len(rules))
	}

	if len(controller.containerRules) != 2 {
		t.Errorf("Expected 2 container rules, got %d", len(controller.containerRules))
	}

	// 手动测试CleanupResources的逻辑部分（不调用实际的方法）
	// 清空ingressRules和containerRules
	controller.ingressRules = make(map[string]zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress)
	controller.containerRules = make(map[string][]string)

	// 检查规则是否被正确清理（不保留任何规则）
	rules = controller.GetIngressRules()
	if len(rules) != 1 { // 只应该保留动态添加的catch-all规则
		t.Errorf("Expected 1 ingress rule after cleanup (catch-all), got %d", len(rules))
	}

	// 检查containerRules是否被清空
	if len(controller.containerRules) != 0 {
		t.Errorf("Expected 0 container rules after cleanup, got %d", len(controller.containerRules))
	}
}

func TestContainerHealthStruct(t *testing.T) {
	// 测试ContainerHealth结构体
	health := &ContainerHealth{
		RestartCount: 3,
		IsFlapping:   true,
	}

	if health.RestartCount != 3 {
		t.Errorf("Expected RestartCount to be 3, got %d", health.RestartCount)
	}

	if !health.IsFlapping {
		t.Error("Expected IsFlapping to be true")
	}
}

// TestStartupReconciliation tests that containers restarted during downtime
// are properly restored from pending deletion state (T068, T074)
func TestStartupReconciliation(t *testing.T) {
	// This is a simplified test that verifies the reconciliation logic path
	// In a real scenario, this would test the full integration with state persistence

	// Create a controller with state manager
	controller := &Controller{
		ingressRules:      make(map[string]zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress),
		containerRules:    make(map[string][]string),
		containerHealth:   make(map[string]*ContainerHealth),
		ruleValidator:     NewCompositeValidator(),
		stateManager:      nil, // Will be set below
		flappingWindow:    60 * time.Second,
		flappingThreshold: 5,
		coolingPeriod:     5 * time.Minute,
		maxCoolingPeriod:  30 * time.Minute,
		debounceDuration:  2 * time.Second,
	}

	// Create state manager
	controller.stateManager = state.NewManager(slog.Default())

	// Simulate a container that was stopped and moved to pending deletion
	containerID := "test-container-123"
	now := time.Now().UTC()
	pastTime := now.Add(-1 * time.Hour)

	pendingEntry := &types.TunnelEntry{
		ContainerID: containerID,
		TunnelID:    "tunnel-123",
		ServiceName: "web",
		Status:      types.StatusPendingDelete,
		CreatedAt:   now.Add(-2 * time.Hour),
		DeletedAt:   &pastTime,
		LastSyncAt:  pastTime,
		Config: types.TunnelConfiguration{
			Hostname:   "test.example.com",
			ServiceURL: "http://localhost:8080",
		},
		RetentionPolicy: types.RetentionPolicy{
			Type:     types.Timed,
			Duration: 30 * time.Minute,
		},
	}

	// Add to pending deletions (simulating persisted state)
	controller.stateManager.AddPendingDeletion(pendingEntry)

	// Verify it's in pending deletions
	_, exists := controller.stateManager.GetPendingDeletion(containerID)
	if !exists {
		t.Fatal("Container should be in pending deletions before reconciliation")
	}

	// Now simulate the container being restarted
	// This would happen during the Sync() reconciliation loop
	// For this test, we directly call the restoration logic

	err := controller.stateManager.RestoreActiveTunnel(containerID)
	if err != nil {
		t.Fatalf("Failed to restore active tunnel: %v", err)
	}

	// Verify it's no longer in pending deletions
	_, exists = controller.stateManager.GetPendingDeletion(containerID)
	if exists {
		t.Error("Container should no longer be in pending deletions after restoration")
	}

	// Verify it's now in active tunnels
	restoredEntry, exists := controller.stateManager.GetActiveTunnel(containerID)
	if !exists {
		t.Fatal("Container should be in active tunnels after restoration")
	}

	// Verify the entry details
	if restoredEntry.ContainerID != containerID {
		t.Errorf("Expected container ID %s, got %s", containerID, restoredEntry.ContainerID)
	}

	if restoredEntry.Status != types.StatusActive {
		t.Errorf("Expected status Active, got %d", restoredEntry.Status)
	}

	if restoredEntry.ServiceName != "web" {
		t.Errorf("Expected service name 'web', got %s", restoredEntry.ServiceName)
	}

	if restoredEntry.Config.Hostname != "test.example.com" {
		t.Errorf("Expected hostname 'test.example.com', got %s", restoredEntry.Config.Hostname)
	}

	// Verify DeletedAt was cleared
	if restoredEntry.DeletedAt != nil {
		t.Error("DeletedAt should be nil after restoration")
	}
}
func TestDispatch_HealthHealthy(t *testing.T) {
	opts := ControllerOptions{
		FlappingWindow:    60 * time.Second,
		FlappingThreshold: 5,
		CoolingPeriod:     5 * time.Minute,
		MaxCoolingPeriod:  30 * time.Minute,
		DebounceDuration:  2 * time.Second,
	}

	ctrl := NewController(nil, &mockCloudflareManager{
		tunnel: &zero_trust.TunnelCloudflaredGetResponse{
			ID: "test-tunnel",
		},
	}, opts)

	containerInfo := &containerTypes.InspectResponse{
		ContainerJSONBase: &containerTypes.ContainerJSONBase{
			HostConfig: &containerTypes.HostConfig{
				NetworkMode: "bridge",
			},
		},
		Config: &containerTypes.Config{
			Labels: map[string]string{
				"docktunnel.enable":       "true",
				"docktunnel.web.hostname": "app.example.com",
				"docktunnel.web.service":  "http://localhost:8080",
			},
		},
		NetworkSettings: &containerTypes.NetworkSettings{
			Networks: map[string]*network.EndpointSettings{
				"bridge": {IPAddress: "172.17.0.2"},
			},
		},
	}

	// Start the container
	startEvent := events.Event{
		Type:          "start",
		ContainerID:   "healthy-container",
		ContainerInfo: containerInfo,
	}

	err := ctrl.Dispatch(context.Background(), startEvent)
	if err != nil {
		t.Fatalf("start dispatch failed: %v", err)
	}

	// Verify rules were registered
	ctrl.mu.RLock()
	hostnames, exists := ctrl.containerRules["healthy-container"]
	ctrl.mu.RUnlock()
	if !exists {
		t.Fatal("expected container rules after start event")
	}
	if len(hostnames) != 1 || hostnames[0] != "app.example.com" {
		t.Errorf("expected hostname app.example.com, got %v", hostnames)
	}

	// Simulate health_unhealthy — should remove the rules
	unhealthyEvent := events.Event{
		Type:          events.ActionHealthUnhealthy,
		ContainerID:   "healthy-container",
		ContainerInfo: containerInfo,
	}

	err = ctrl.Dispatch(context.Background(), unhealthyEvent)
	if err != nil {
		t.Fatalf("unhealthy dispatch failed: %v", err)
	}

	ctrl.mu.RLock()
	_, existsAfter := ctrl.containerRules["healthy-container"]
	ctrl.mu.RUnlock()
	if existsAfter {
		t.Error("expected container rules to be removed after unhealthy event")
	}

	// Simulate health_healthy — should re-add the rules
	healthyEvent := events.Event{
		Type:          events.ActionHealthHealthy,
		ContainerID:   "healthy-container",
		ContainerInfo: containerInfo,
	}

	err = ctrl.Dispatch(context.Background(), healthyEvent)
	if err != nil {
		t.Fatalf("healthy dispatch failed: %v", err)
	}

	ctrl.mu.RLock()
	hostnames2, existsAfter2 := ctrl.containerRules["healthy-container"]
	ctrl.mu.RUnlock()
	if !existsAfter2 {
		t.Fatal("expected container rules after healthy event")
	}
	if len(hostnames2) != 1 || hostnames2[0] != "app.example.com" {
		t.Errorf("expected hostname app.example.com after healthy, got %v", hostnames2)
	}
}

func TestDispatch_HealthEventsDoNotTriggerFlapping(t *testing.T) {
	opts := ControllerOptions{
		FlappingWindow:    60 * time.Second,
		FlappingThreshold: 2,
		CoolingPeriod:     5 * time.Minute,
		MaxCoolingPeriod:  30 * time.Minute,
		DebounceDuration:  2 * time.Second,
	}

	ctrl := NewController(nil, &mockCloudflareManager{
		tunnel: &zero_trust.TunnelCloudflaredGetResponse{
			ID: "test-tunnel",
		},
	}, opts)

	containerInfo := &containerTypes.InspectResponse{
		ContainerJSONBase: &containerTypes.ContainerJSONBase{
			HostConfig: &containerTypes.HostConfig{
				NetworkMode: "bridge",
			},
		},
		Config: &containerTypes.Config{
			Labels: map[string]string{
				"docktunnel.enable":       "true",
				"docktunnel.web.hostname": "app.example.com",
				"docktunnel.web.service":  "http://localhost:8080",
			},
		},
		NetworkSettings: &containerTypes.NetworkSettings{
			Networks: map[string]*network.EndpointSettings{
				"bridge": {IPAddress: "172.17.0.2"},
			},
		},
	}

	// Start the container
	startEvent := events.Event{
		Type:          "start",
		ContainerID:   "test-container",
		ContainerInfo: containerInfo,
	}
	_ = ctrl.Dispatch(context.Background(), startEvent)

	// Send many health unhealthy/healthy cycles
	for i := 0; i < 10; i++ {
		unhealthyEvent := events.Event{
			Type:          events.ActionHealthUnhealthy,
			ContainerID:   "test-container",
			ContainerInfo: containerInfo,
		}
		_ = ctrl.Dispatch(context.Background(), unhealthyEvent)

		healthyEvent := events.Event{
			Type:          events.ActionHealthHealthy,
			ContainerID:   "test-container",
			ContainerInfo: containerInfo,
		}
		_ = ctrl.Dispatch(context.Background(), healthyEvent)
	}

	// Container should still have rules (not flapping)
	ctrl.mu.RLock()
	_, exists := ctrl.containerRules["test-container"]
	ctrl.mu.RUnlock()
	if !exists {
		t.Error("health events should not trigger flapping - container should still have rules")
	}
}

func TestRetentionPolicyPersistedOnStart(t *testing.T) {
	ctrl := NewController(nil, &mockCloudflareManager{
		tunnel: &zero_trust.TunnelCloudflaredGetResponse{ID: "test-tunnel"},
	}, ControllerOptions{
		FlappingWindow:    60 * time.Second,
		FlappingThreshold: 5,
		CoolingPeriod:     5 * time.Minute,
		MaxCoolingPeriod:  30 * time.Minute,
		DebounceDuration:  2 * time.Second,
	})

	containerInfo := &containerTypes.InspectResponse{
		ContainerJSONBase: &containerTypes.ContainerJSONBase{
			HostConfig: &containerTypes.HostConfig{NetworkMode: "bridge"},
		},
		Config: &containerTypes.Config{
			Labels: map[string]string{
				"docktunnel.enable":        "true",
				"docktunnel.web.hostname":  "app.example.com",
				"docktunnel.web.service":   "http://localhost:8080",
				"docktunnel.web.retention": "1h",
			},
		},
		NetworkSettings: &containerTypes.NetworkSettings{
			Networks: map[string]*network.EndpointSettings{
				"bridge": {IPAddress: "172.17.0.2"},
			},
		},
	}

	startEvent := events.Event{
		Type:          "start",
		ContainerID:   "retain-container",
		ContainerInfo: containerInfo,
	}

	err := ctrl.Dispatch(context.Background(), startEvent)
	if err != nil {
		t.Fatalf("start dispatch failed: %v", err)
	}

	entry, exists := ctrl.stateManager.GetActiveTunnel("retain-container")
	if !exists {
		t.Fatal("expected active tunnel entry after start with retention label")
	}

	if entry.RetentionPolicy.Type != types.Timed {
		t.Errorf("expected Timed retention, got %d", entry.RetentionPolicy.Type)
	}

	if entry.RetentionPolicy.Duration != 1*time.Hour {
		t.Errorf("expected 1h retention duration, got %v", entry.RetentionPolicy.Duration)
	}
}

func TestStopUsesStoredRetentionPolicyWithoutContainerInfo(t *testing.T) {
	ctrl := NewController(nil, &mockCloudflareManager{
		tunnel: &zero_trust.TunnelCloudflaredGetResponse{ID: "test-tunnel"},
	}, ControllerOptions{
		FlappingWindow:    60 * time.Second,
		FlappingThreshold: 5,
		CoolingPeriod:     5 * time.Minute,
		MaxCoolingPeriod:  30 * time.Minute,
		DebounceDuration:  2 * time.Second,
	})

	// Manually set up state: container with rules and a Timed retention policy
	ctrl.ingressRules["app.example.com"] = zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{
		Hostname: cloudflare.F("app.example.com"),
		Service:  cloudflare.F("http://localhost:8080"),
	}
	ctrl.containerRules["stop-container"] = []string{"app.example.com"}

	// Store retention policy in state manager (simulating what handleContainerStart does)
	now := time.Now()
	ctrl.stateManager.AddActiveTunnel(&types.TunnelEntry{
		ContainerID: "stop-container",
		RetentionPolicy: types.RetentionPolicy{
			Type:     types.Timed,
			Duration: 1 * time.Hour,
		},
		Status:    types.StatusActive,
		CreatedAt: now,
	})

	// Stop event WITHOUT ContainerInfo (simulates stop/die with nil info)
	stopEvent := events.Event{
		Type:          "stop",
		ContainerID:   "stop-container",
		ContainerInfo: nil,
	}

	err := ctrl.Dispatch(context.Background(), stopEvent)
	if err != nil {
		t.Fatalf("stop dispatch failed: %v", err)
	}

	// Should have moved to pending deletion (Timed), not Immediate deletion
	pending, exists := ctrl.stateManager.GetPendingDeletion("stop-container")
	if !exists {
		t.Fatal("expected pending deletion for Timed retention policy")
	}

	if pending.RetentionPolicy.Type != types.Timed {
		t.Errorf("expected Timed retention in pending deletion, got %d", pending.RetentionPolicy.Type)
	}
}

func TestReconcile_NoDriftWithNilDockerManager(t *testing.T) {
	ctrl := NewController(nil, &mockCloudflareManager{
		tunnel: &zero_trust.TunnelCloudflaredGetResponse{ID: "test-tunnel"},
	}, ControllerOptions{
		DebounceDuration: 2 * time.Second,
	})

	// Set up current state with a retaining rule
	ctrl.ingressRules["app.example.com"] = zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{
		Hostname: cloudflare.F("app.example.com"),
		Service:  cloudflare.F("http://localhost:8080"),
	}
	ctrl.containerRules["container-1"] = []string{"app.example.com"}

	// Reconcile with no Docker manager → returns nil early
	err := ctrl.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	// Rules should be unchanged
	ctrl.mu.RLock()
	_, exists := ctrl.ingressRules["app.example.com"]
	ctrl.mu.RUnlock()
	if !exists {
		t.Error("expected app.example.com to remain in ingressRules")
	}
}

func TestReconcile_RetainingRulesPreserved(t *testing.T) {
	ctrl := NewController(nil, &mockCloudflareManager{
		tunnel: &zero_trust.TunnelCloudflaredGetResponse{ID: "test-tunnel"},
	}, ControllerOptions{
		DebounceDuration: 2 * time.Second,
	})

	// Set up a retaining rule: in ingressRules but NOT in containerRules
	ctrl.ingressRules["retained.example.com"] = zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{
		Hostname: cloudflare.F("retained.example.com"),
		Service:  cloudflare.F("http://localhost:8080"),
	}
	// No containerRules entry for this hostname — it's a retaining rule

	// Reconcile with nil dockerManager returns early, no changes
	err := ctrl.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	ctrl.mu.RLock()
	_, exists := ctrl.ingressRules["retained.example.com"]
	ctrl.mu.RUnlock()
	if !exists {
		t.Error("retaining rule should be preserved")
	}
}

func TestReconcileAccessors(t *testing.T) {
	ctrl := NewController(nil, nil, ControllerOptions{
		ReconcileEnabled:  true,
		ReconcileInterval: 60 * time.Second,
	})

	if !ctrl.ReconcileEnabled() {
		t.Error("expected ReconcileEnabled to be true")
	}
	if ctrl.ReconcileInterval() != 60*time.Second {
		t.Errorf("expected ReconcileInterval 60s, got %v", ctrl.ReconcileInterval())
	}
}

func TestStop_Immediate_DeletesRouteViaTransition(t *testing.T) {
	ctrl := NewController(nil, &mockCloudflareManager{
		tunnel: &zero_trust.TunnelCloudflaredGetResponse{ID: "test-tunnel"},
	}, ControllerOptions{
		DebounceDuration: 2 * time.Second,
	})

	// Set up: container has rules and active tunnel with Immediate policy
	ctrl.ingressRules["app.example.com"] = zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{
		Hostname: cloudflare.F("app.example.com"),
		Service:  cloudflare.F("http://localhost:8080"),
	}
	ctrl.containerRules["c1"] = []string{"app.example.com"}
	ctrl.stateManager.AddActiveTunnel(&types.TunnelEntry{
		ContainerID:     "c1",
		RetentionPolicy: types.RetentionPolicy{Type: types.Immediate},
		Status:          types.StatusActive,
		Config:          types.TunnelConfiguration{Hostname: "app.example.com"},
	})

	stopEvent := events.Event{Type: "stop", ContainerID: "c1", ContainerInfo: nil}
	err := ctrl.Dispatch(context.Background(), stopEvent)
	if err != nil {
		t.Fatalf("stop dispatch failed: %v", err)
	}

	// Ingress should be deleted
	ctrl.mu.RLock()
	_, ingressExists := ctrl.ingressRules["app.example.com"]
	ctrl.mu.RUnlock()
	if ingressExists {
		t.Error("ingress should be deleted for Immediate retention")
	}

	// Container rules should be cleared
	ctrl.mu.RLock()
	_, rulesExist := ctrl.containerRules["c1"]
	ctrl.mu.RUnlock()
	if rulesExist {
		t.Error("containerRules should be cleared")
	}

	// State should be PendingDelete
	pending, _ := ctrl.stateManager.GetPendingDeletion("c1")
	if pending != nil && pending.Status != types.StatusPendingDelete {
		t.Errorf("expected StatusPendingDelete, got %d", pending.Status)
	}
}

func TestStop_Timed_KeepsRouteViaTransition(t *testing.T) {
	ctrl := NewController(nil, &mockCloudflareManager{
		tunnel: &zero_trust.TunnelCloudflaredGetResponse{ID: "test-tunnel"},
	}, ControllerOptions{
		DebounceDuration: 2 * time.Second,
	})

	ctrl.ingressRules["app.example.com"] = zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{
		Hostname: cloudflare.F("app.example.com"),
		Service:  cloudflare.F("http://localhost:8080"),
	}
	ctrl.containerRules["c1"] = []string{"app.example.com"}
	ctrl.stateManager.AddActiveTunnel(&types.TunnelEntry{
		ContainerID:     "c1",
		RetentionPolicy: types.RetentionPolicy{Type: types.Timed, Duration: 1 * time.Hour},
		Status:          types.StatusActive,
		Config:          types.TunnelConfiguration{Hostname: "app.example.com"},
	})

	stopEvent := events.Event{Type: "stop", ContainerID: "c1", ContainerInfo: nil}
	err := ctrl.Dispatch(context.Background(), stopEvent)
	if err != nil {
		t.Fatalf("stop dispatch failed: %v", err)
	}

	// Ingress should be KEPT (Timed retention)
	ctrl.mu.RLock()
	_, ingressExists := ctrl.ingressRules["app.example.com"]
	ctrl.mu.RUnlock()
	if !ingressExists {
		t.Error("ingress should be kept for Timed retention")
	}

	// Container rules should be cleared
	ctrl.mu.RLock()
	_, rulesExist := ctrl.containerRules["c1"]
	ctrl.mu.RUnlock()
	if rulesExist {
		t.Error("containerRules should be cleared for Timed retention")
	}

	// State should be Retaining
	pending, _ := ctrl.stateManager.GetPendingDeletion("c1")
	if pending != nil && pending.Status != types.StatusRetaining {
		t.Errorf("expected StatusRetaining, got %d", pending.Status)
	}
}

func TestStart_RestoresFromRetainingViaTransition(t *testing.T) {
	ctrl := NewController(nil, &mockCloudflareManager{
		tunnel: &zero_trust.TunnelCloudflaredGetResponse{ID: "test-tunnel"},
	}, ControllerOptions{
		DebounceDuration: 2 * time.Second,
	})

	// Set up: container in Retaining state (stopped with Timed retention)
	past := time.Now().Add(-30 * time.Minute)
	ctrl.stateManager.AddPendingDeletion(&types.TunnelEntry{
		ContainerID:     "c1",
		ServiceName:     "web",
		Config:          types.TunnelConfiguration{Hostname: "app.example.com"},
		Status:          types.StatusRetaining,
		RetentionPolicy: types.RetentionPolicy{Type: types.Timed, Duration: 1 * time.Hour},
		DeletedAt:       &past,
	})

	containerInfo := &containerTypes.InspectResponse{
		ContainerJSONBase: &containerTypes.ContainerJSONBase{
			HostConfig: &containerTypes.HostConfig{NetworkMode: "bridge"},
		},
		Config: &containerTypes.Config{
			Labels: map[string]string{
				"docktunnel.enable":       "true",
				"docktunnel.web.hostname": "app.example.com",
				"docktunnel.web.service":  "http://localhost:8080",
			},
		},
		NetworkSettings: &containerTypes.NetworkSettings{
			Networks: map[string]*network.EndpointSettings{
				"bridge": {IPAddress: "172.17.0.2"},
			},
		},
	}

	startEvent := events.Event{Type: "start", ContainerID: "c1", ContainerInfo: containerInfo}
	err := ctrl.Dispatch(context.Background(), startEvent)
	if err != nil {
		t.Fatalf("start dispatch failed: %v", err)
	}

	// Should be in active tunnels now
	entry, exists := ctrl.stateManager.GetActiveTunnel("c1")
	if !exists {
		t.Fatal("expected entry in active tunnels after restart")
	}
	if entry.Status != types.StatusActive {
		t.Errorf("expected StatusActive, got %d", entry.Status)
	}
	if entry.DeletedAt != nil {
		t.Error("DeletedAt should be nil after restoration")
	}

	// Should NOT be in pending deletes
	_, pendingExists := ctrl.stateManager.GetPendingDeletion("c1")
	if pendingExists {
		t.Error("entry should not be in pending deletes after restart")
	}
}
