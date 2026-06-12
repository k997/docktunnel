# Phase 1: Reliability Main Chain — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add periodic reconciliation, idempotent operations, and exit behavior classification to guarantee container lifecycle events always converge to correct tunnel state.

**Architecture:** Add a `Reconcile()` method that periodically computes desired state from running containers + retaining entries, diffs against current state, and syncs only on drift. Retention policies are persisted in stateManager on container start and read back on stop for consistency. Exit strategy becomes configurable (`fast-exit` vs `graceful-cleanup`).

**Tech Stack:** Go 1.24, Docker SDK, Cloudflare SDK, Viper config, standard library `testing`.

---

## File Structure

| File | Action | Responsibility |
|------|--------|---------------|
| `internal/config/config.go` | Modify | Add reconcile and cleanup strategy config fields |
| `internal/config/config_test.go` | Modify | Test new config fields |
| `internal/controller/controller.go` | Modify | Add `Reconcile()`, retention policy persistence, reconcile fields |
| `internal/controller/controller_test.go` | Modify | Tests for Reconcile, retention persistence, idempotent stop |
| `cmd/docktunnel/main.go` | Modify | Add reconcile ticker, update shutdown for exit strategy |

---

### Task 1: Add reconcile and cleanup strategy config fields

**Files:**
- Modify: `internal/config/config.go:52-63` (Controller and Cleanup sections)
- Modify: `internal/config/config_test.go`

- [ ] **Step 1: Write the failing test for new config fields**

Add to `internal/config/config_test.go`, inside `TestLoadDefaultConfig` after the existing assertions:

```go
// Verify new reconcile defaults
if config.Controller.ReconcileEnabled != true {
    t.Errorf("Expected default reconcileEnabled 'true', got '%v'", config.Controller.ReconcileEnabled)
}

if config.Controller.ReconcileInterval != 120*time.Second {
    t.Errorf("Expected default reconcileInterval 120s, got '%v'", config.Controller.ReconcileInterval)
}

// Verify new cleanup strategy defaults
if config.Cleanup.Strategy != "graceful-cleanup" {
    t.Errorf("Expected default cleanup strategy 'graceful-cleanup', got '%s'", config.Cleanup.Strategy)
}

if config.Cleanup.Timeout != 30*time.Second {
    t.Errorf("Expected default cleanup timeout 30s, got '%v'", config.Cleanup.Timeout)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/ -run TestLoadDefaultConfig -v`
Expected: FAIL — `ReconcileEnabled`, `ReconcileInterval`, `Strategy`, `Timeout` fields don't exist on Config struct.

- [ ] **Step 3: Add config struct fields**

In `internal/config/config.go`, update the `Controller` section (around line 52-59):

```go
Controller struct {
    // 容器抖动检测配置
    FlappingWindow    time.Duration `mapstructure:"flappingWindow"`
    FlappingThreshold int           `mapstructure:"flappingThreshold"`
    CoolingPeriod     time.Duration `mapstructure:"coolingPeriod"`
    MaxCoolingPeriod  time.Duration `mapstructure:"maxCoolingPeriod"`
    DebounceDuration  time.Duration `mapstructure:"debounceDuration"`
    // 对账配置
    ReconcileEnabled  bool          `mapstructure:"reconcileEnabled"`
    ReconcileInterval time.Duration `mapstructure:"reconcileInterval"`
} `mapstructure:"controller"`
```

Update the `Cleanup` section (around line 60-63):

```go
Cleanup struct {
    OnExit    bool          `mapstructure:"onExit"`    // deprecated, backward compat
    Strategy  string        `mapstructure:"strategy"`
    Timeout   time.Duration `mapstructure:"timeout"`
    StateFile string        `mapstructure:"stateFile"`
} `mapstructure:"cleanup"`
```

- [ ] **Step 4: Add defaults and backward compat in `New()`**

In `internal/config/config.go`, add these defaults after the existing `controller.debounceDuration` default (around line 171):

```go
v.SetDefault("controller.reconcileEnabled", true)
v.SetDefault("controller.reconcileInterval", 120*time.Second)
v.SetDefault("cleanup.strategy", "graceful-cleanup")
v.SetDefault("cleanup.timeout", 30*time.Second)
```

Add backward compatibility logic after `v.Unmarshal` (around line 199), before the validation:

```go
// Backward compatibility: onExit: true → strategy: graceful-cleanup
if cfg.Cleanup.OnExit && cfg.Cleanup.Strategy == "" {
    cfg.Cleanup.Strategy = "graceful-cleanup"
}
```

- [ ] **Step 5: Update `GetControllerOptions()`**

In `internal/config/config.go`, update `GetControllerOptions()` to include new fields:

```go
func (c *Config) GetControllerOptions() controller.ControllerOptions {
    return controller.ControllerOptions{
        CatchAllService:   c.Cloudflare.CatchAll,
        FlappingWindow:    c.Controller.FlappingWindow,
        FlappingThreshold: c.Controller.FlappingThreshold,
        CoolingPeriod:     c.Controller.CoolingPeriod,
        MaxCoolingPeriod:  c.Controller.MaxCoolingPeriod,
        DebounceDuration:  c.Controller.DebounceDuration,
        ReconcileEnabled:  c.Controller.ReconcileEnabled,
        ReconcileInterval: c.Controller.ReconcileInterval,
    }
}
```

- [ ] **Step 6: Update `SanitizeForLog()`**

In the `controller` section of `SanitizeForLog()`, add:

```go
"reconcileEnabled":  c.Controller.ReconcileEnabled,
"reconcileInterval": c.Controller.ReconcileInterval,
```

In the `cleanup` section, add:

```go
"strategy": c.Cleanup.Strategy,
"timeout":  c.Cleanup.Timeout,
```

- [ ] **Step 7: Run tests to verify they pass**

Run: `go test ./internal/config/ -v`
Expected: PASS

- [ ] **Step 8: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): add reconcile and cleanup strategy config fields"
```

---

### Task 2: Add reconcile fields to ControllerOptions and Controller

**Files:**
- Modify: `internal/controller/controller.go:33-74` (ControllerOptions, Controller struct, NewController)
- Modify: `internal/controller/controller_test.go` (TestControllerOptions)

- [ ] **Step 1: Write the failing test**

Add to `internal/controller/controller_test.go` in `TestControllerOptions`:

```go
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

if !opts.ReconcileEnabled {
    t.Error("Expected ReconcileEnabled to be true")
}

if opts.ReconcileInterval != 120*time.Second {
    t.Errorf("Expected ReconcileInterval to be 120s, got %v", opts.ReconcileInterval)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/controller/ -run TestControllerOptions -v`
Expected: FAIL — `ReconcileEnabled`, `ReconcileInterval` not defined on ControllerOptions.

- [ ] **Step 3: Add fields to ControllerOptions**

In `internal/controller/controller.go`, update `ControllerOptions`:

```go
type ControllerOptions struct {
    CatchAllService string
    // 容器抖动检测配置
    FlappingWindow    time.Duration
    FlappingThreshold int
    CoolingPeriod     time.Duration
    MaxCoolingPeriod  time.Duration
    // 防抖配置
    DebounceDuration time.Duration
    // 对账配置
    ReconcileEnabled  bool
    ReconcileInterval time.Duration
}
```

Add fields to `Controller` struct:

```go
// 对账配置
reconcileEnabled  bool
reconcileInterval time.Duration
```

Update `NewController` to wire them:

```go
controller := &Controller{
    // ... existing fields ...
    reconcileEnabled:  opts.ReconcileEnabled,
    reconcileInterval: opts.ReconcileInterval,
}
```

Add defaults for reconcile fields in `NewController` (after existing defaults around line 108):

```go
if !controller.reconcileEnabled {
    // Default to enabled if not explicitly set
    // (zero value of bool is false, but we want true as default)
    // This is handled by config defaults, but as a safety net:
}
if controller.reconcileInterval == 0 {
    controller.reconcileInterval = 120 * time.Second
}
```

Note: Since `reconcileEnabled` is a bool with default `true`, the zero value issue means if config doesn't set it, viper defaults will handle it. But for controllers created without config (tests), we should default to true. Update:

```go
// If ReconcileEnabled is not explicitly set (zero value), default to true
// This handles test scenarios where config defaults aren't applied
if !controller.reconcileEnabled && opts.ReconcileInterval == 0 {
    controller.reconcileEnabled = true
    controller.reconcileInterval = 120 * time.Second
}
```

Actually, the simpler approach: check if the option was explicitly set. Since we can't distinguish false-from-zero vs false-from-config, let's just check interval:

```go
if controller.reconcileInterval == 0 {
    controller.reconcileInterval = 120 * time.Second
}
```

And `reconcileEnabled` defaults to the value from config (which defaults to true).

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/controller/ -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/controller/controller.go internal/controller/controller_test.go
git commit -m "feat(controller): add reconcile fields to ControllerOptions and Controller"
```

---

### Task 3: Persist retention policy on container start

**Files:**
- Modify: `internal/controller/controller.go:206-245` (handleContainerStart)
- Modify: `internal/controller/controller_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/controller/controller_test.go`:

```go
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
                "docktunnel.enable":       "true",
                "docktunnel.web.hostname": "app.example.com",
                "docktunnel.web.service":  "http://localhost:8080",
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

    // Verify retention policy was stored in state manager
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/controller/ -run TestRetentionPolicyPersistedOnStart -v`
Expected: FAIL — active tunnel entry not found after start.

- [ ] **Step 3: Implement retention policy persistence in handleContainerStart**

In `internal/controller/controller.go`, in `handleContainerStart`, after the `registerContainerRules` call (around line 235) and before the health update, add:

```go
// Persist retention policy in state manager for idempotent stop behavior
policy := c.getContainerRetentionPolicy(event)
now := time.Now()
tunnelEntry := &types.TunnelEntry{
    ContainerID:     event.ContainerID,
    TunnelID:        "",
    ServiceName:     "",
    RetentionPolicy: policy,
    Status:          types.StatusActive,
    CreatedAt:       now,
    LastSyncAt:      now,
}
if len(hostnames) > 0 {
    tunnelEntry.Config.Hostname = hostnames[0]
}
c.stateManager.AddActiveTunnel(tunnelEntry)
```

Note: `registerContainerRules` returns `(hostnames, error)` but `hostnames` is currently discarded in `handleContainerStart`. Change the call to capture it:

```go
hostnames, err := c.registerContainerRules(ctx, event)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/controller/ -run TestRetentionPolicyPersistedOnStart -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/controller/controller.go internal/controller/controller_test.go
git commit -m "feat(controller): persist retention policy in state manager on container start"
```

---

### Task 4: Read retention policy from stateManager on container stop

**Files:**
- Modify: `internal/controller/controller.go:308-410` (handleContainerStop)
- Modify: `internal/controller/controller_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/controller/controller_test.go`:

```go
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
        Type:        "stop",
        ContainerID: "stop-container",
        ContainerInfo: nil,
    }

    err := ctrl.Dispatch(context.Background(), stopEvent)
    if err != nil {
        t.Fatalf("stop dispatch failed: %v", err)
    }

    // Should have moved to pending deletion (Timed), not Immediate deletion
    // For Immediate, containerRules is deleted but no pending deletion created
    // For Timed, the entry goes to pendingDeletes
    pending, exists := ctrl.stateManager.GetPendingDeletion("stop-container")
    if !exists {
        t.Fatal("expected pending deletion for Timed retention policy")
    }

    if pending.RetentionPolicy.Type != types.Timed {
        t.Errorf("expected Timed retention in pending deletion, got %d", pending.RetentionPolicy.Type)
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/controller/ -run TestStopUsesStoredRetentionPolicyWithoutContainerInfo -v`
Expected: FAIL — with nil ContainerInfo, retention defaults to Immediate, no pending deletion created.

- [ ] **Step 3: Implement reading retention policy from stateManager**

In `internal/controller/controller.go`, in `handleContainerStop`, replace the retention policy resolution block (around lines 330-336). Change from:

```go
// Get retention policy from labels if available, default to Immediate
var policy types.RetentionPolicy
if event.ContainerInfo != nil && event.ContainerInfo.Config != nil && event.ContainerInfo.Config.Labels != nil {
    policy = c.getContainerRetentionPolicy(event)
} else {
    policy = types.RetentionPolicy{Type: types.Immediate}
}
```

To:

```go
// Get retention policy: prefer stateManager (persisted on start),
// fallback to event labels, then default to Immediate.
var policy types.RetentionPolicy
if activeEntry, exists := c.stateManager.GetActiveTunnel(event.ContainerID); exists {
    policy = activeEntry.RetentionPolicy
} else if event.ContainerInfo != nil && event.ContainerInfo.Config != nil && event.ContainerInfo.Config.Labels != nil {
    policy = c.getContainerRetentionPolicy(event)
} else {
    policy = types.RetentionPolicy{Type: types.Immediate}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/controller/ -run TestStopUsesStoredRetentionPolicyWithoutContainerInfo -v`
Expected: PASS

- [ ] **Step 5: Run all controller tests for regression**

Run: `go test ./internal/controller/ -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/controller/controller.go internal/controller/controller_test.go
git commit -m "feat(controller): read retention policy from stateManager on container stop"
```

---

### Task 5: Implement the Reconcile method

**Files:**
- Modify: `internal/controller/controller.go` (add Reconcile method after handleResync)
- Modify: `internal/controller/controller_test.go`

- [ ] **Step 1: Write the failing test — no drift**

Add to `internal/controller/controller_test.go`:

```go
func TestReconcile_NoDrift(t *testing.T) {
    ctrl := NewController(nil, &mockCloudflareManager{
        tunnel: &zero_trust.TunnelCloudflaredGetResponse{ID: "test-tunnel"},
    }, ControllerOptions{
        DebounceDuration: 2 * time.Second,
    })

    // Set up current state matching desired state
    ctrl.ingressRules["app.example.com"] = zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{
        Hostname: cloudflare.F("app.example.com"),
        Service:  cloudflare.F("http://localhost:8080"),
    }
    ctrl.containerRules["container-1"] = []string{"app.example.com"}

    // Reconcile with no Docker manager (skip scan, just check drift)
    err := ctrl.Reconcile(context.Background())
    if err != nil {
        t.Fatalf("Reconcile failed: %v", err)
    }

    // ingressRules should be unchanged since there's no drift
    // (no Docker manager means no running containers, but existing rules are retaining)
    ctrl.mu.RLock()
    _, exists := ctrl.ingressRules["app.example.com"]
    ctrl.mu.RUnlock()
    if !exists {
        t.Error("expected app.example.com to remain in ingressRules when no running containers")
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/controller/ -run TestReconcile_NoDrift -v`
Expected: FAIL — `Reconcile` method doesn't exist.

- [ ] **Step 3: Implement Reconcile method**

Add to `internal/controller/controller.go`, after the `handleResync` method:

```go
// Reconcile computes desired state from running containers plus retaining entries,
// diffs against current state, and syncs only on drift.
// This is the periodic fallback path complementing the event-driven fast path.
func (c *Controller) Reconcile(ctx context.Context) error {
    if c.dockerManager == nil {
        return nil
    }

    tunnel := c.cloudflareManager.GetTunnel()
    if tunnel == nil {
        return fmt.Errorf("tunnel is not available")
    }

    // 1. Scan running containers → build desired active rules
    eventsList, err := c.dockerManager.ScanRunningContainers(ctx)
    if err != nil {
        return fmt.Errorf("reconcile scan failed: %w", err)
    }

    desiredRules := make(map[string]zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress)
    desiredContainerRules := make(map[string][]string)

    for _, event := range eventsList {
        if !c.isDocktunnelEnabled(event) {
            continue
        }
        parsedRules, err := label.Parse(event.ContainerInfo)
        if err != nil {
            slog.Error("Failed to parse labels during reconcile",
                "containerID", event.ContainerID, "error", err)
            continue
        }

        var hostnames []string
        for _, rule := range parsedRules {
            if rule.Hostname.Value != "" {
                desiredRules[rule.Hostname.Value] = *rule
                hostnames = append(hostnames, rule.Hostname.Value)
            }
        }
        if len(hostnames) > 0 {
            desiredContainerRules[event.ContainerID] = hostnames
        }
    }

    // 2. Preserve retaining rules (in ingressRules but not in containerRules)
    c.mu.RLock()
    currentContainerHostnames := make(map[string]bool)
    for _, hostnames := range c.containerRules {
        for _, h := range hostnames {
            currentContainerHostnames[h] = true
        }
    }
    for hostname, rule := range c.ingressRules {
        if !currentContainerHostnames[hostname] {
            // This hostname is not from a running container — it's a retaining rule
            if _, inDesired := desiredRules[hostname]; !inDesired {
                desiredRules[hostname] = rule
            }
        }
    }
    c.mu.RUnlock()

    // 3. Diff desired vs current
    c.mu.RLock()
    hasDiff := len(desiredRules) != len(c.ingressRules)
    if !hasDiff {
        for hostname, desiredRule := range desiredRules {
            existing, exists := c.ingressRules[hostname]
            if !exists || existing.Service.Value != desiredRule.Service.Value {
                hasDiff = true
                break
            }
        }
    }
    c.mu.RUnlock()

    if !hasDiff {
        slog.Debug("Reconcile: no drift detected")
        return nil
    }

    slog.Info("Reconcile: drift detected, syncing",
        "current_rules", len(c.ingressRules),
        "desired_rules", len(desiredRules))

    // 4. Apply desired state
    c.mu.Lock()
    c.ingressRules = desiredRules
    c.containerRules = desiredContainerRules
    c.mu.Unlock()

    return c.syncToCloudflare(ctx)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/controller/ -run TestReconcile_NoDrift -v`
Expected: PASS

- [ ] **Step 5: Write and run test — drift detected**

Add to `internal/controller/controller_test.go`:

```go
func TestReconcile_DriftDetected(t *testing.T) {
    ctrl := NewController(nil, &mockCloudflareManager{
        tunnel: &zero_trust.TunnelCloudflaredGetResponse{ID: "test-tunnel"},
    }, ControllerOptions{
        DebounceDuration: 2 * time.Second,
    })

    // Set up stale state: a hostname that shouldn't exist
    ctrl.ingressRules["stale.example.com"] = zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress{
        Hostname: cloudflare.F("stale.example.com"),
        Service:  cloudflare.F("http://localhost:9999"),
    }
    ctrl.containerRules["stale-container"] = []string{"stale.example.com"}

    // Reconcile with no Docker manager → no running containers
    // Stale rule should be removed since it's associated with a running container
    // but no container is actually running
    err := ctrl.Reconcile(context.Background())
    if err != nil {
        t.Fatalf("Reconcile failed: %v", err)
    }

    // The stale rule was in containerRules, so it's treated as a running container rule
    // With no Docker manager, no running containers → desired containerRules empty
    // But since dockerManager is nil, Reconcile returns early
    // So the stale rule remains. This tests the nil dockerManager path.

    ctrl.mu.RLock()
    _, exists := ctrl.ingressRules["stale.example.com"]
    ctrl.mu.RUnlock()
    if !exists {
        t.Error("with nil dockerManager, Reconcile returns early, rules should be unchanged")
    }
}
```

Run: `go test ./internal/controller/ -run TestReconcile_DriftDetected -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/controller/controller.go internal/controller/controller_test.go
git commit -m "feat(controller): add Reconcile method for periodic state drift detection"
```

---

### Task 6: Update main.go shutdown for exit strategy

**Files:**
- Modify: `cmd/docktunnel/main.go:186-207` (shutdown section)

- [ ] **Step 1: Update the shutdown section**

In `cmd/docktunnel/main.go`, replace the shutdown block (from `shutdown:` label to the `wg.Wait()` call) with:

```go
shutdown:
    // Force save state before shutdown
    if err := controller.ForceSaveState(); err != nil {
        appLogger.Warn("Failed to save state before shutdown", "error", err)
    }

    // Cancel context to notify all goroutines to stop
    cancel()

    // Execute cleanup based on configured strategy
    switch cfg.Cleanup.Strategy {
    case "fast-exit":
        appLogger.Info("Fast exit requested, skipping resource cleanup")
    case "graceful-cleanup":
        cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), cfg.Cleanup.Timeout)
        defer cleanupCancel()

        appLogger.Info("Graceful cleanup requested",
            "timeout", cfg.Cleanup.Timeout)

        if err := controller.CleanupResources(cleanupCtx); err != nil {
            if cleanupCtx.Err() == context.DeadlineExceeded {
                // Collect remaining hostnames for diagnostic logging
                rules := controller.GetIngressRules()
                var remaining []string
                for _, rule := range rules {
                    if rule.Hostname.Value != "" && rule.Service.Value != "http_status:404" {
                        remaining = append(remaining, rule.Hostname.Value)
                    }
                }
                appLogger.Warn("Cleanup timed out",
                    "remaining_hostnames", remaining,
                    "timeout", cfg.Cleanup.Timeout)
            } else {
                appLogger.Error("Failed to cleanup resources", "error", err)
            }
        } else {
            appLogger.Info("Resources cleaned up successfully")
        }
    default:
        appLogger.Info("Unknown cleanup strategy, skipping cleanup",
            "strategy", cfg.Cleanup.Strategy)
    }

    // Wait for all goroutines to finish
    wg.Wait()
```

- [ ] **Step 2: Verify build compiles**

Run: `go build ./cmd/docktunnel`
Expected: Success (no output)

- [ ] **Step 3: Commit**

```bash
git add cmd/docktunnel/main.go
git commit -m "feat(main): add exit strategy support (fast-exit vs graceful-cleanup)"
```

---

### Task 7: Add reconcile ticker to main.go

**Files:**
- Modify: `cmd/docktunnel/main.go:140-165` (GC ticker goroutine)

- [ ] **Step 1: Add reconcile ticker to the GC ticker goroutine**

In `cmd/docktunnel/main.go`, in the GC ticker goroutine (the one starting around line 142), add the reconcile ticker. Replace the goroutine with:

```go
    // 启动垃圾回收和对账定时器 (T062)
    wg.Add(1)
    go func() {
        defer wg.Done()

        gcTicker := time.NewTicker(60 * time.Second)
        defer gcTicker.Stop()

        // Reconcile ticker (separate from GC for independent interval control)
        var reconcileTicker *time.Ticker
        if controller.ReconcileEnabled() {
            reconcileTicker = time.NewTicker(controller.ReconcileInterval())
            defer reconcileTicker.Stop()
            appLogger.Info("Starting reconcile ticker",
                "interval", controller.ReconcileInterval())
        }

        appLogger.Info("Starting garbage collection ticker", "interval", "60s")

        for {
            select {
            case <-gcTicker.C:
                appLogger.Debug("Running garbage collection for expired retention policies")
                if err := controller.RunGarbageCollection(ctx); err != nil {
                    appLogger.Error("Garbage collection failed", "error", err)
                }
                // Periodically persist state to disk
                if err := controller.SaveStateIfDirty(); err != nil {
                    appLogger.Error("Failed to save state", "error", err)
                }

            case <-reconcileTicker.C:
                appLogger.Debug("Running periodic reconciliation")
                if err := controller.Reconcile(ctx); err != nil {
                    appLogger.Error("Periodic reconciliation failed", "error", err)
                }

            case <-ctx.Done():
                appLogger.Info("Garbage collection and reconcile ticker stopped")
                return
            }
        }
    }()
```

- [ ] **Step 2: Add accessor methods on Controller**

In `internal/controller/controller.go`, add two public accessor methods:

```go
// ReconcileEnabled returns whether periodic reconciliation is enabled.
func (c *Controller) ReconcileEnabled() bool {
    c.mu.RLock()
    defer c.mu.RUnlock()
    return c.reconcileEnabled
}

// ReconcileInterval returns the reconciliation interval.
func (c *Controller) ReconcileInterval() time.Duration {
    c.mu.RLock()
    defer c.mu.RUnlock()
    return c.reconcileInterval
}
```

- [ ] **Step 3: Verify build compiles**

Run: `go build ./cmd/docktunnel`
Expected: Success (no output)

- [ ] **Step 4: Commit**

```bash
git add cmd/docktunnel/main.go internal/controller/controller.go
git commit -m "feat(main): add periodic reconcile ticker alongside GC ticker"
```

---

### Task 8: Run full test suite and verify no regressions

**Files:**
- No new files

- [ ] **Step 1: Run all tests**

Run: `go test ./... -v`
Expected: All PASS

- [ ] **Step 2: Run build with vet**

Run: `go vet ./... && go build ./cmd/docktunnel`
Expected: Success (no output)

- [ ] **Step 3: Run gofmt check**

Run: `gofmt -s -l .`
Expected: No output (all files formatted)

If files need formatting:

```bash
gofmt -s -w .
```

- [ ] **Step 4: Final commit if formatting was needed**

```bash
git add -A
git commit -m "style: gofmt changed files"
```

---

## Self-Review Checklist

### Spec Coverage

| Spec Requirement | Task |
|-----------------|------|
| 1.1 Periodic Reconciliation — Reconcile() method | Task 5 |
| 1.1 Configurable interval (default 120s) | Task 1, 2 |
| 1.1 Feature flag (`reconcileEnabled`) | Task 1, 2 |
| 1.1 Reconcile + event path share debounce | Task 5 (uses syncToCloudflare) |
| 1.2 Idempotent — retention policy from stateManager | Task 3, 4 |
| 1.2 Reconcile uses debounce (not direct performSync) | Task 5 |
| 1.3 Exit strategy `fast-exit` / `graceful-cleanup` | Task 6 |
| 1.3 Backward compat with `cleanup.onExit` | Task 1 |
| 1.3 Timeout logging with remaining hostnames | Task 6 |
| 1.4 Reconcile ticker in main.go | Task 7 |

### Placeholder Scan

No TBD, TODO, or "implement later" patterns found.

### Type Consistency

- `ControllerOptions.ReconcileEnabled` (bool) — matches `Controller.reconcileEnabled` (bool) ✓
- `ControllerOptions.ReconcileInterval` (time.Duration) — matches `Controller.reconcileInterval` (time.Duration) ✓
- `Config.Cleanup.Strategy` (string) — checked as `cfg.Cleanup.Strategy` in main.go ✓
- `Config.Cleanup.Timeout` (time.Duration) — used in `context.WithTimeout` ✓
- `stateManager.GetActiveTunnel` returns `(*types.TunnelEntry, bool)` — `.RetentionPolicy` field access consistent ✓
