package controller

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"

	containerTypes "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"

	"docktunnel/internal/events"
	"docktunnel/internal/state"
	"docktunnel/pkg/types"

	"github.com/cloudflare/cloudflare-go/v5"
	"github.com/cloudflare/cloudflare-go/v5/zero_trust"
)

// mockDockerScanner implements DockerScanner for controller tests.
type mockDockerScanner struct {
	events []events.Event
	err    error
}

func (m *mockDockerScanner) ScanRunningContainers(ctx context.Context) ([]events.Event, error) {
	return m.events, m.err
}

// testContainerInfo builds a minimal InspectResponse with the given labels.
func testContainerInfo(labels map[string]string) *containerTypes.InspectResponse {
	if labels == nil {
		labels = map[string]string{}
	}
	labels["docktunnel.enable"] = "true"
	return &containerTypes.InspectResponse{
		ContainerJSONBase: &containerTypes.ContainerJSONBase{
			HostConfig: &containerTypes.HostConfig{NetworkMode: "bridge"},
		},
		Config: &containerTypes.Config{Labels: labels},
		NetworkSettings: &containerTypes.NetworkSettings{
			Networks: map[string]*network.EndpointSettings{
				"bridge": {IPAddress: "172.17.0.2"},
			},
		},
	}
}

func TestIngressKeyHelpers(t *testing.T) {
	key := ingressKey("app.example.com", "/api")
	if key != "app.example.com\x00/api" {
		t.Errorf("unexpected ingressKey: %q", key)
	}
	if got := hostnameFromKey(key); got != "app.example.com" {
		t.Errorf("hostnameFromKey = %q, want app.example.com", got)
	}
	if got := hostnameFromKey("bare-hostname"); got != "bare-hostname" {
		t.Errorf("hostnameFromKey on key without separator = %q, want bare-hostname", got)
	}
}

func TestBuildDesiredRules_PreservesRetainingEntries(t *testing.T) {
	ctrl := NewController(&mockDockerScanner{}, &mockCloudflareManager{
		tunnel: &zero_trust.TunnelCloudflaredGetResponse{ID: "t1"},
	}, ControllerOptions{})

	// A retaining entry with a persisted rule (B1).
	rule := zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{
		Hostname: cloudflare.F("retained.example.com"),
		Service:  cloudflare.F("http://origin:8080"),
	}
	ruleJSON, err := json.Marshal(rule)
	if err != nil {
		t.Fatalf("marshal rule: %v", err)
	}
	past := time.Now().Add(-time.Minute)
	ctrl.stateManager.AddPendingDeletion(&types.TunnelEntry{
		ContainerID: "c-retained",
		ServiceName: "web",
		Status:      types.StatusRetaining,
		RetentionPolicy: types.RetentionPolicy{
			Type:     types.Timed,
			Duration: time.Hour,
		},
		DeletedAt: &past,
		Config: types.TunnelConfiguration{
			Hostname: "retained.example.com",
			RuleJSON: ruleJSON,
		},
	})

	desired, err := ctrl.buildDesiredRules(context.Background(), false)
	if err != nil {
		t.Fatalf("buildDesiredRules failed: %v", err)
	}

	key := ingressKey("retained.example.com", "")
	got, exists := desired.rules[key]
	if !exists {
		t.Fatal("retaining entry rule must be part of desired state")
	}
	if got.Service.Value != "http://origin:8080" {
		t.Errorf("expected retained service, got %q", got.Service.Value)
	}
}

func TestBuildDesiredRules_RunningContainerWinsOnKeyConflict(t *testing.T) {
	// Retaining entry for hostname X, but a running container also serves X —
	// the running container must win (B1).
	rule := zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{
		Hostname: cloudflare.F("app.example.com"),
		Service:  cloudflare.F("http://old-origin:8080"),
	}
	ruleJSON, _ := json.Marshal(rule)
	past := time.Now().Add(-time.Minute)
	sm := state.NewManager(slog.Default())
	sm.AddPendingDeletion(&types.TunnelEntry{
		ContainerID: "c-old",
		ServiceName: "web",
		Status:      types.StatusRetaining,
		RetentionPolicy: types.RetentionPolicy{
			Type:     types.Timed,
			Duration: time.Hour,
		},
		DeletedAt: &past,
		Config: types.TunnelConfiguration{
			Hostname: "app.example.com",
			RuleJSON: ruleJSON,
		},
	})

	scanner := &mockDockerScanner{events: []events.Event{{
		Type:        "start",
		ContainerID: "c-running",
		ContainerInfo: testContainerInfo(map[string]string{
			"docktunnel.web.hostname": "app.example.com",
			"docktunnel.web.service":  "http://new-origin:9090",
		}),
	}}}

	ctrl := &Controller{
		dockerManager:     scanner,
		cloudflareManager: &mockCloudflareManager{tunnel: &zero_trust.TunnelCloudflaredGetResponse{ID: "t1"}},
		stateManager:      sm,
		ingressRules:      map[string]zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{},
		containerRules:    map[string][]string{},
		containerHealth:   map[string]*ContainerHealth{},
		ruleValidator:     NewCompositeValidator(),
	}
	ctrl.healthTracker = newHealthTracker(ctrl, slog.Default())

	desired, err := ctrl.buildDesiredRules(context.Background(), false)
	if err != nil {
		t.Fatalf("buildDesiredRules failed: %v", err)
	}

	key := ingressKey("app.example.com", "")
	got := desired.rules[key]
	if got.Service.Value != "http://new-origin:9090" {
		t.Errorf("running container should win on key conflict, got service %q", got.Service.Value)
	}
}

func TestBuildDesiredRules_TransitionsStoppedDuringDisconnect(t *testing.T) {
	sm := state.NewManager(slog.Default())
	ruleJSON, _ := json.Marshal(zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{
		Hostname: cloudflare.F("gone.example.com"),
		Service:  cloudflare.F("http://origin:8080"),
	})
	sm.AddActiveTunnel(&types.TunnelEntry{
		ContainerID: "c-gone",
		ServiceName: "web",
		Status:      types.StatusActive,
		RetentionPolicy: types.RetentionPolicy{
			Type:     types.Timed,
			Duration: time.Hour,
		},
		Config: types.TunnelConfiguration{
			Hostname: "gone.example.com",
			RuleJSON: ruleJSON,
		},
	})

	// Scanner returns no containers: c-gone stopped during the disconnect
	// window (B8).
	ctrl := &Controller{
		dockerManager:     &mockDockerScanner{},
		cloudflareManager: &mockCloudflareManager{tunnel: &zero_trust.TunnelCloudflaredGetResponse{ID: "t1"}},
		stateManager:      sm,
		ingressRules:      map[string]zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{},
		containerRules:    map[string][]string{},
		containerHealth:   map[string]*ContainerHealth{},
		ruleValidator:     NewCompositeValidator(),
	}
	ctrl.healthTracker = newHealthTracker(ctrl, slog.Default())

	desired, err := ctrl.buildDesiredRules(context.Background(), false)
	if err != nil {
		t.Fatalf("buildDesiredRules failed: %v", err)
	}

	// The entry must have transitioned to retaining…
	pending, exists := sm.GetPendingDeletion("c-gone")
	if !exists {
		t.Fatal("entry for container that stopped during disconnect should be in pending deletions")
	}
	if pending.Status != types.StatusRetaining {
		t.Errorf("expected StatusRetaining, got %d", pending.Status)
	}
	if _, active := sm.GetActiveTunnel("c-gone", "web"); active {
		t.Error("entry should not remain active")
	}

	// …and its retained rule must survive in desired state (Timed retention).
	if _, exists := desired.rules[ingressKey("gone.example.com", "")]; !exists {
		t.Error("retained rule should be part of desired state after disconnect transition")
	}
}

func TestBuildDesiredRules_CrossContainerConflictKeepsFirst(t *testing.T) {
	// Two containers serve the same (hostname, path). Sorted by containerID,
	// "a-aaa" is processed first and must win (B17).
	scanner := &mockDockerScanner{events: []events.Event{
		{
			Type:        "start",
			ContainerID: "b-zzz",
			ContainerInfo: testContainerInfo(map[string]string{
				"docktunnel.web.hostname": "dup.example.com",
				"docktunnel.web.service":  "http://second:8080",
			}),
		},
		{
			Type:        "start",
			ContainerID: "a-aaa",
			ContainerInfo: testContainerInfo(map[string]string{
				"docktunnel.web.hostname": "dup.example.com",
				"docktunnel.web.service":  "http://first:8080",
			}),
		},
	}}

	ctrl := &Controller{
		dockerManager:     scanner,
		cloudflareManager: &mockCloudflareManager{tunnel: &zero_trust.TunnelCloudflaredGetResponse{ID: "t1"}},
		stateManager:      state.NewManager(slog.Default()),
		ingressRules:      map[string]zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{},
		containerRules:    map[string][]string{},
		containerHealth:   map[string]*ContainerHealth{},
		ruleValidator:     NewCompositeValidator(),
	}
	ctrl.healthTracker = newHealthTracker(ctrl, slog.Default())

	desired, err := ctrl.buildDesiredRules(context.Background(), false)
	if err != nil {
		t.Fatalf("buildDesiredRules failed: %v", err)
	}

	got := desired.rules[ingressKey("dup.example.com", "")]
	if got.Service.Value != "http://first:8080" {
		t.Errorf("first-sorted container should win, got service %q", got.Service.Value)
	}
}

func TestBuildDesiredRules_SkipCoolingContainer(t *testing.T) {
	scanner := &mockDockerScanner{events: []events.Event{{
		Type:        "start",
		ContainerID: "c-cooling",
		ContainerInfo: testContainerInfo(map[string]string{
			"docktunnel.web.hostname": "cooling.example.com",
			"docktunnel.web.service":  "http://origin:8080",
		}),
	}}}

	ctrl := &Controller{
		dockerManager:     scanner,
		cloudflareManager: &mockCloudflareManager{tunnel: &zero_trust.TunnelCloudflaredGetResponse{ID: "t1"}},
		stateManager:      state.NewManager(slog.Default()),
		ingressRules:      map[string]zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{},
		containerRules:    map[string][]string{},
		containerHealth: map[string]*ContainerHealth{
			"c-cooling": {IsFlapping: true, CoolingUntil: time.Now().Add(time.Hour)},
		},
		ruleValidator: NewCompositeValidator(),
	}
	ctrl.healthTracker = newHealthTracker(ctrl, slog.Default())

	// skipCooling=true (Reconcile): the cooling container's rules must be
	// excluded so periodic reconciliation cannot hollow out cooling (B11).
	desired, err := ctrl.buildDesiredRules(context.Background(), true)
	if err != nil {
		t.Fatalf("buildDesiredRules failed: %v", err)
	}
	if _, exists := desired.rules[ingressKey("cooling.example.com", "")]; exists {
		t.Error("cooling container must be skipped when skipCooling=true")
	}

	// skipCooling=false (Sync): the rule is included.
	desired2, err := ctrl.buildDesiredRules(context.Background(), false)
	if err != nil {
		t.Fatalf("buildDesiredRules failed: %v", err)
	}
	if _, exists := desired2.rules[ingressKey("cooling.example.com", "")]; !exists {
		t.Error("cooling container should be included when skipCooling=false")
	}
}

func TestBuildDesiredRules_ScanFailure(t *testing.T) {
	ctrl := &Controller{
		dockerManager:     &mockDockerScanner{err: errors.New("docker down")},
		cloudflareManager: &mockCloudflareManager{},
		stateManager:      state.NewManager(slog.Default()),
	}
	_, err := ctrl.buildDesiredRules(context.Background(), false)
	if err == nil {
		t.Fatal("expected error when scan fails")
	}
}

func TestGetIngressRules_DeterministicSortCatchAllLast(t *testing.T) {
	ctrl := NewController(nil, nil, ControllerOptions{CatchAllService: "http_status:410"})

	ctrl.ingressRules[ingressKey("b.example.com", "")] = zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{
		Hostname: cloudflare.F("b.example.com"),
		Service:  cloudflare.F("http://b:8080"),
	}
	ctrl.ingressRules[ingressKey("a.example.com", "/api")] = zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{
		Hostname: cloudflare.F("a.example.com"),
		Path:     cloudflare.F("/api"),
		Service:  cloudflare.F("http://api:8080"),
	}
	ctrl.ingressRules[ingressKey("a.example.com", "")] = zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{
		Hostname: cloudflare.F("a.example.com"),
		Service:  cloudflare.F("http://a:8080"),
	}

	rules := ctrl.GetIngressRules()
	if len(rules) != 4 {
		t.Fatalf("expected 4 rules (3 + catch-all), got %d", len(rules))
	}

	// Deterministic order: a.example.com (no path) → a.example.com /api →
	// b.example.com → catch-all last (B10).
	got := []string{
		rules[0].Hostname.Value + rules[0].Path.Value,
		rules[1].Hostname.Value + rules[1].Path.Value,
		rules[2].Hostname.Value + rules[2].Path.Value,
	}
	want := []string{"a.example.com", "a.example.com/api", "b.example.com"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("rule %d = %q, want %q (got %v)", i, got[i], want[i], got)
		}
	}

	// Catch-all last and uses the configured service (B4).
	last := rules[len(rules)-1]
	if last.Service.Value != "http_status:410" {
		t.Errorf("catch-all service = %q, want http_status:410 (configured)", last.Service.Value)
	}
	if last.Hostname.Value != "" {
		t.Errorf("catch-all should have no hostname, got %q", last.Hostname.Value)
	}
}

func TestGetIngressRules_CatchAllDefaultsTo404(t *testing.T) {
	ctrl := NewController(nil, nil, ControllerOptions{})
	rules := ctrl.GetIngressRules()
	if len(rules) != 1 {
		t.Fatalf("expected only catch-all, got %d rules", len(rules))
	}
	if rules[0].Service.Value != "http_status:404" {
		t.Errorf("default catch-all = %q, want http_status:404", rules[0].Service.Value)
	}
}

// TestHandleContainerStop_FallbackFillsHostname verifies that when active
// state was lost, the fallback branch of handleContainerStop fills
// Config.Hostname (and RuleJSON) so downstream GC / buildDesiredRules act on
// the correct route (B12).
func TestHandleContainerStop_FallbackFillsHostname(t *testing.T) {
	ctrl := NewController(nil, &mockCloudflareManager{
		tunnel: &zero_trust.TunnelCloudflaredGetResponse{ID: "t1"},
	}, ControllerOptions{})

	ctrl.containerRules["c1"] = []string{"app.example.com"}

	stopEvent := events.Event{
		Type:        "stop",
		ContainerID: "c1",
		ContainerInfo: testContainerInfo(map[string]string{
			"docktunnel.web.hostname": "app.example.com",
			"docktunnel.web.service":  "http://origin:8080",
		}),
	}
	if err := ctrl.Dispatch(context.Background(), stopEvent); err != nil {
		t.Fatalf("stop dispatch failed: %v", err)
	}

	pending, exists := ctrl.stateManager.GetPendingDeletion("c1")
	if !exists {
		t.Fatal("expected pending deletion entry after stop with Immediate retention")
	}
	if pending.Config.Hostname != "app.example.com" {
		t.Errorf("fallback entry Config.Hostname = %q, want app.example.com (B12)", pending.Config.Hostname)
	}
	if len(pending.Config.RuleJSON) == 0 {
		t.Error("fallback entry should persist RuleJSON for retention recovery")
	}
}

// Review R6: a container with one incomplete service (no hostname) must not
// block its own good services or abort the sync.
func TestBuildDesiredRules_IsolatesServiceWithoutHostname(t *testing.T) {
	scanner := &mockDockerScanner{events: []events.Event{{
		Type:        "start",
		ContainerID: "c-mixed",
		ContainerInfo: testContainerInfo(map[string]string{
			// Incomplete: originRequest only, no hostname (gap-filling scenario).
			"docktunnel.incomplete.originRequest.noTLSVerify": "true",
			"docktunnel.good.hostname":                        "good.example.com",
			"docktunnel.good.service":                         "http://172.17.0.2:8080",
		}),
	}}}
	ctrl := &Controller{
		dockerManager:     scanner,
		cloudflareManager: &mockCloudflareManager{tunnel: &zero_trust.TunnelCloudflaredGetResponse{ID: "t1"}},
		stateManager:      state.NewManager(slog.Default()),
		ingressRules:      map[string]zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{},
		containerRules:    map[string][]string{},
		containerHealth:   map[string]*ContainerHealth{},
		ruleValidator:     NewCompositeValidator(),
	}
	ctrl.healthTracker = newHealthTracker(ctrl, slog.Default())

	desired, err := ctrl.buildDesiredRules(context.Background(), false)
	if err != nil {
		t.Fatalf("buildDesiredRules failed: %v", err)
	}
	if _, ok := desired.rules[ingressKey("good.example.com", "")]; !ok {
		t.Error("good service should be routed despite the incomplete sibling service")
	}
	if len(desired.rules) != 1 {
		t.Errorf("expected exactly 1 rule, got %d", len(desired.rules))
	}
}

// Review R1: the README-documented delete_retention spelling must drive the
// retention policy just like the legacy retention spelling.
func TestGetServiceRetentionPolicy_DeleteRetentionKeys(t *testing.T) {
	ctrl := &Controller{}
	labels := map[string]string{
		"docktunnel.web.delete_retention": "30m",
	}
	policy := ctrl.getServiceRetentionPolicy(labels, "web")
	if policy.Type != types.Timed || policy.Duration != 30*time.Minute {
		t.Errorf("per-service delete_retention = %+v, want Timed/30m", policy)
	}

	labels = map[string]string{
		"docktunnel.web.retention":        "1h",
		"docktunnel.web.delete_retention": "30m",
	}
	policy = ctrl.getServiceRetentionPolicy(labels, "web")
	if policy.Duration != 30*time.Minute {
		t.Errorf("delete_retention should win over legacy retention, got %+v", policy)
	}

	labels = map[string]string{"docktunnel.delete_retention": "forever"}
	policy = ctrl.getServiceRetentionPolicy(labels, "other")
	if policy.Type != types.Forever {
		t.Errorf("global delete_retention = %+v, want Forever", policy)
	}

	labels = map[string]string{"docktunnel.retention": "keep"}
	policy = ctrl.getServiceRetentionPolicy(labels, "other")
	if policy.Type != types.Forever {
		t.Errorf("legacy global retention = %+v, want Forever", policy)
	}
}
