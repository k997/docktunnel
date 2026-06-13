# Phase 6: Observability & Operations

**Date:** 2026-06-13
**Status:** Approved
**Depends on:** Phases 1-5 (state machine, retention, compensation queue, persistence)

## Problem

DockTunnel has no observability surface. When something goes wrong — a hostname isn't reachable, a retention policy didn't fire, the compensation queue is stuck — the operator has only `log/slog` text to diagnose. There is no metrics endpoint to scrape, no way to compare desired vs actual state without running `cloudflared` CLI by hand, and the log fields are inconsistent (some calls use `containerID`, others `container_id`; outcomes are embedded in the message string, not structured).

Phase 6 closes the "discover → locate → fix" loop by adding three things:

1. **Consistent structured log fields** so log queries can answer "what happened to container X's service?" without grepping message strings.
2. **Prometheus metrics** at `/metrics` so dashboards and alerts can be built on real signal, not log volume.
3. **A `/debug/state` endpoint** that returns desired vs actual state side-by-side with a computed diff, so drift is visible without making live Cloudflare API calls.

## Approach

Add three new packages (`metrics`, `diagnostics`, `server`) and a small amount of instrumentation across the controller and cloudflareManager. The new HTTP server is read-only with respect to controller state — it only reads snapshots, never triggers Cloudflare API calls. This means `/debug/state` latency is microseconds, not seconds, and the server can be scraped aggressively without affecting the controller's API rate budget.

The single new external dependency is `github.com/prometheus/client_golang`. No HTTP framework, no OpenTelemetry extensions.

---

## 1. Architecture Overview

**New packages:**

| Package | Responsibility |
|---------|----------------|
| `internal/metrics/` | Five Prometheus metric definitions + helper functions |
| `internal/diagnostics/` | Debug state types, pure diff function, JSON handler |
| `internal/server/` | `http.ServeMux` wrapper with graceful shutdown |

**Modified packages:**

| Package | Changes |
|---------|---------|
| `internal/controller/` | Add `GetDebugState()`; cache last-known actual state after Sync/Reconcile; instrument Dispatch, Reconcile, GC, compensation |
| `internal/cloudflareManager/` | Add `result=success/failure` to DNS sync log calls; call `metrics.RecordDNSSync()` |
| `internal/config/` | Add `server.bindAddr`, `server.port` |
| `cmd/docktunnel/` | Start/stop HTTP server; wire snapshot function |

**Data flow:**

```
                     ┌──────────────────┐
                     │  internal/server │ ← http.ServeMux on 127.0.0.1:9100
                     │                  │
   /metrics ─────────┤→ promhttp.Handler│
                     │                  │
   /debug/state ─────┤→ diagnostics.H() │── reads controller.GetDebugState()
                     │                  │
   /healthz ─────────┤→ static "ok"     │
                     └────────┬─────────┘
                              │
              ┌───────────────┴────────────────┐
              │                                │
   ┌──────────▼─────────┐         ┌────────────▼───────────┐
   │ internal/metrics   │         │ internal/diagnostics   │
   │ (5 metric defs)    │         │ (Diff struct + JSON)   │
   └──────────▲─────────┘         └────────────▲───────────┘
              │                                │
   controller.instrumentX()         controller.GetDebugState()
   at Dispatch/Reconcile/            returns desired + cached actual
   GC/compensation entry points
```

---

## 2. Structured Logging Enhancements

**Goal:** Every meaningful operation logs `action` and `result` so log queries can answer "what happened to this container's service?" without grepping message strings.

**Approach:** Audit existing log calls in controller and cloudflareManager paths. Keep existing field names — only add `action` and `result` where missing. No renames.

**Standard fields to add:**

| Field | Values | Used in |
|-------|--------|---------|
| `action` | `dispatch_start`, `dispatch_end`, `reconcile`, `gc`, `compensation_tick`, `cleanup` | Every meaningful operation |
| `result` | `success`, `failure`, `skipped`, `debounced`, `cooling` | Outcome of the action |

**Scope rules:**

- Do NOT rename existing fields. The codebase already mixes `containerID` (camelCase) and `container_id` (snake_case). Renaming is a breaking change for any log aggregator queries — out of scope.
- Do NOT touch debug-level logs that are pure tracing (e.g., "Processing Docker event"). Focus on operational logs (action outcomes).
- If a log call already has equivalent context in a different shape, leave it alone — no churn.

**Files to touch:**

- `internal/controller/controller.go` — `Dispatch`, `Sync`, `Reconcile`, `RunGarbageCollection`, `handleContainerStart`, `handleContainerStop`
- `internal/controller/compensation.go` — compensation loop iterations
- `internal/cloudflareManager/tunnel.go` — DNS sync call outcomes

**Example transformation** (`Dispatch` entry/exit):

```go
// Entry
slog.Info("Handling container start event",
    "action", "dispatch_start",
    "containerID", event.ContainerID,
    "type", event.Type)

// Exit (success)
slog.Info("Handled container start event",
    "action", "dispatch_end",
    "containerID", event.ContainerID,
    "result", "success")

// Exit (failure)
slog.Error("Failed to dispatch event",
    "action", "dispatch",
    "containerID", event.ContainerID,
    "result", "failure",
    "error", err)
```

---

## 3. Metrics Package

**File:** `internal/metrics/metrics.go`

Uses `github.com/prometheus/client_golang/prometheus/promauto` for registration at package init time.

**Five metric definitions:**

```go
package metrics

import (
    "github.com/prometheus/client_golang/prometheus"
    "github.com/prometheus/client_golang/prometheus/promauto"
)

var (
    // Counter — Docker events processed.
    EventsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
        Namespace: "docktunnel",
        Name:      "events_total",
        Help:      "Total Docker events processed by type and result.",
    }, []string{"type", "result"})

    // Histogram — Reconcile cycle duration.
    ReconcileDuration = promauto.NewHistogram(prometheus.HistogramOpts{
        Namespace: "docktunnel",
        Name:      "reconcile_duration_seconds",
        Help:      "Wall-clock time spent in Reconcile().",
        Buckets:   prometheus.DefBuckets,
    })

    // Counter — DNS sync API calls.
    DNSSyncOperations = promauto.NewCounterVec(prometheus.CounterOpts{
        Namespace: "docktunnel",
        Name:      "dns_sync_operations_total",
        Help:      "DNS sync operations against Cloudflare by operation and result.",
    }, []string{"operation", "result"})

    // Gauge — Retention entries by lifecycle status.
    RetentionEntries = promauto.NewGaugeVec(prometheus.GaugeOpts{
        Namespace: "docktunnel",
        Name:      "retention_entries",
        Help:      "Number of retention entries by status (Active, Retaining, PendingDelete).",
    }, []string{"status"})

    // Gauge — Compensation queue depth.
    CompensationQueueLength = promauto.NewGauge(prometheus.GaugeOpts{
        Namespace: "docktunnel",
        Name:      "compensation_queue_length",
        Help:      "Number of items currently in the compensation queue.",
    })
)
```

**Helper functions** (same file — centralizes label discipline so call sites cannot misspell labels):

```go
func RecordEvent(eventType, result string) {
    EventsTotal.WithLabelValues(eventType, result).Inc()
}

func ObserveReconcile(seconds float64) {
    ReconcileDuration.Observe(seconds)
}

func RecordDNSSync(operation, result string) {
    DNSSyncOperations.WithLabelValues(operation, result).Inc()
}

func SetRetentionEntries(status string, n int) {
    RetentionEntries.WithLabelValues(status).Set(float64(n))
}

func SetCompensationQueueLength(n int) {
    CompensationQueueLength.Set(float64(n))
}
```

**Naming note:** `dns_sync_operations` in the roadmap is exposed as `docktunnel_dns_sync_operations_total` because Prometheus convention requires counters to end in `_total`. The client library appends this anyway; making it explicit in the source prevents confusion.

**No init function needed.** `promauto` registers with the default registry at package init time. Importing the package anywhere wires up `/metrics`.

---

## 4. Diagnostics Package

**File:** `internal/diagnostics/diagnostics.go`

Computes the response for `/debug/state` and produces JSON. Lives in its own package so it can be unit-tested without spinning up an HTTP server.

**Types:**

```go
package diagnostics

import "time"

// RuleView is a single ingress rule as exposed in the debug response.
type RuleView struct {
    Hostname string `json:"hostname"`
    Service  string `json:"service"`
    Path     string `json:"path,omitempty"`
}

// StateDiff classifies rules by where they appear.
type StateDiff struct {
    DesiredOnly []string `json:"desired_only"` // hostnames only in desired
    ActualOnly  []string `json:"actual_only"`  // hostnames only in actual
    Matching    int      `json:"matching"`     // count in both
}

// DebugStateResponse is the JSON returned by /debug/state.
type DebugStateResponse struct {
    Timestamp    time.Time   `json:"timestamp"`
    DesiredState []RuleView  `json:"desired_state"`
    ActualState  []RuleView  `json:"actual_state"`
    Source       string      `json:"source"` // "live_cache" or "empty"
    Diff         StateDiff   `json:"diff"`
}
```

**Pure diff function:**

```go
// ComputeDiff returns the set difference of desired vs actual by hostname.
func ComputeDiff(desired, actual []RuleView) StateDiff {
    desiredSet := make(map[string]struct{}, len(desired))
    actualSet := make(map[string]struct{}, len(actual))

    for _, r := range desired {
        desiredSet[r.Hostname] = struct{}{}
    }
    for _, r := range actual {
        actualSet[r.Hostname] = struct{}{}
    }

    var diff StateDiff
    for h := range desiredSet {
        if _, ok := actualSet[h]; !ok {
            diff.DesiredOnly = append(diff.DesiredOnly, h)
        } else {
            diff.Matching++
        }
    }
    for h := range actualSet {
        if _, ok := desiredSet[h]; !ok {
            diff.ActualOnly = append(diff.ActualOnly, h)
        }
    }
    return diff
}
```

**HTTP handler** (dependency-injected snapshot func):

```go
// Handler returns an http.HandlerFunc that serves debug state.
func Handler(snapshot func() DebugStateResponse) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        resp := snapshot()
        w.Header().Set("Content-Type", "application/json")
        json.NewEncoder(w).Encode(resp)
    }
}
```

**Why injection:** the handler takes a `snapshot` function so the package has zero coupling to the controller. Tests pass a fake snapshot func; production wires it to `controller.GetDebugState()`.

**Controller integration:**

The controller does NOT currently read the live tunnel config from Cloudflare — it only pushes via `UpdateConfiguration`. Phase 6 adds a `GetConfiguration(ctx)` method to `cloudflareManager.Manager` (and the `CloudflareManager` interface) that performs a single GET against `client.ZeroTrust.Tunnels.Cloudflared.Configurations.Get`. The controller calls this at the end of successful `Sync()` and `Reconcile()` cycles and caches the result. Failure to fetch is non-fatal — the cache simply stays stale, and the `Source` field will continue to read `"live_cache"` since the previous successful fetch (or `"empty"` if no fetch has ever succeeded).

```go
// In Controller struct:
actualStateMu        sync.RWMutex
lastKnownActualRules []diagnostics.RuleView

// refreshActualState is called at end of Sync/Reconcile.
func (c *Controller) refreshActualState(ctx context.Context) {
    ingress, err := c.cloudflareManager.GetConfiguration(ctx)
    if err != nil {
        slog.Warn("Failed to fetch live tunnel config for diagnostics", "error", err)
        return
    }
    views := make([]diagnostics.RuleView, 0, len(ingress))
    for _, rule := range ingress {
        if rule.Hostname == "" {
            continue // skip catch-all rules like http_status:404
        }
        views = append(views, diagnostics.RuleView{
            Hostname: rule.Hostname,
            Service:  rule.Service,
            Path:     rule.Path,
        })
    }
    c.setLastKnownActualRules(views)
}
```

New public method:

```go
// GetDebugState returns the current desired vs actual state for diagnostics.
// Safe to call from any goroutine.
func (c *Controller) GetDebugState() diagnostics.DebugStateResponse {
    c.actualStateMu.RLock()
    defer c.actualStateMu.RUnlock()

    desired := c.snapshotRuleViews()
    actual := c.lastKnownActualRules
    source := "live_cache"
    if actual == nil {
        source = "empty"
    }

    return diagnostics.DebugStateResponse{
        Timestamp:    time.Now(),
        DesiredState: desired,
        ActualState:  actual,
        Source:       source,
        Diff:         diagnostics.ComputeDiff(desired, actual),
    }
}
```

**Staleness bound:** worst case the data is up to 120s old (one reconcile interval). The `Source` field surfaces this so the operator can tell.

---

## 5. HTTP Server Package

**File:** `internal/server/server.go`

Wraps `http.ServeMux`, binds to a configurable address, runs in a goroutine tied to the main context.

```go
package server

import (
    "context"
    "net/http"
    "time"

    "github.com/prometheus/client_golang/prometheus/promhttp"

    "docktunnel/internal/diagnostics"
)

type Server struct {
    srv *http.Server
}

// New builds a Server with /metrics, /debug/state, and /healthz registered.
func New(addr string, snapshot func() diagnostics.DebugStateResponse) *Server {
    mux := http.NewServeMux()
    mux.Handle("/metrics", promhttp.Handler())
    mux.Handle("/debug/state", diagnostics.Handler(snapshot))
    mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
        w.Write([]byte("ok"))
    })

    return &Server{
        srv: &http.Server{
            Addr:              addr,
            Handler:           mux,
            ReadHeaderTimeout: 5 * time.Second,
        },
    }
}

// Start begins serving in a goroutine. Returns when ctx is cancelled
// or the server fails to bind.
func (s *Server) Start(ctx context.Context) error {
    errCh := make(chan error, 1)
    go func() {
        if err := s.srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
            errCh <- err
            return
        }
        errCh <- nil
    }()

    select {
    case <-ctx.Done():
        shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
        defer cancel()
        return s.srv.Shutdown(shutdownCtx)
    case err := <-errCh:
        return err
    }
}
```

**Design notes:**

- **Three routes only:** `/metrics`, `/debug/state`, `/healthz`. K8s liveness/readiness probes can hit `/healthz` without auth concerns.
- **`ReadHeaderTimeout: 5s`** — slowloris protection, stdlib recommendation.
- **Graceful shutdown** — when the main context is cancelled (SIGINT/SIGTERM), `Shutdown()` gives in-flight requests 5 seconds to finish.
- **No middleware** — auth, logging, etc. all out of scope. Adding later means wrapping `mux`, not rewriting.
- **Non-fatal:** if the port is taken, `Start()` returns the error; the caller logs at error level but the controller continues running. Losing observability is degraded, not broken.

---

## 6. Config and Main Integration

**File:** `internal/config/config.go`

Add to `Config` struct (after `Persistence`):

```go
Server struct {
    BindAddr string `mapstructure:"bindAddr"`
    Port     int    `mapstructure:"port"`
} `mapstructure:"server"`
```

Defaults in `New()`:

```go
v.SetDefault("server.bindAddr", "127.0.0.1")
v.SetDefault("server.port", 9100)
```

Getter method:

```go
// GetServerAddr returns the configured "host:port" for the diagnostics/metrics server.
func (c *Config) GetServerAddr() string {
    addr := c.Server.BindAddr
    if addr == "" {
        addr = "127.0.0.1"
    }
    port := c.Server.Port
    if port == 0 {
        port = 9100
    }
    return fmt.Sprintf("%s:%d", addr, port)
}
```

Add to `SanitizeForLog`:

```go
"server": map[string]interface{}{
    "bindAddr": c.Server.BindAddr,
    "port":     c.Server.Port,
},
```

**YAML example:**

```yaml
server:
  bindAddr: "127.0.0.1"  # default; set to 0.0.0.0 to expose
  port: 9100
```

---

**File:** `cmd/docktunnel/main.go`

Start the server after `controller.LoadState()` succeeds and before the event loop:

```go
// Start HTTP server for metrics and diagnostics
debugServer := server.New(cfg.GetServerAddr(), controller.GetDebugState)
wg.Add(1)
go func() {
    defer wg.Done()
    appLogger.Info("Starting diagnostics HTTP server", "addr", cfg.GetServerAddr())
    if err := debugServer.Start(ctx); err != nil {
        appLogger.Error("Diagnostics HTTP server stopped with error", "error", err)
    }
}()
```

**Why before the event loop:** the server must be ready to serve `/healthz` and `/metrics` even if no Docker events have arrived yet. Starting it early means Prometheus can scrape from t=0.

---

## 7. Controller Instrumentation Points

| Location | Metric | Code |
|----------|--------|------|
| `Dispatch` exit (success) | `events_total` | `metrics.RecordEvent(event.Type, "success")` |
| `Dispatch` exit (failure) | `events_total` | `metrics.RecordEvent(event.Type, "failure")` |
| `Dispatch` exit (skipped — debounced/cooling) | `events_total` | `metrics.RecordEvent(event.Type, "skipped")` |
| After `Reconcile(ctx)` returns | `reconcile_duration_seconds` | `metrics.ObserveReconcile(time.Since(start).Seconds())` |
| After `RunGarbageCollection` | `retention_entries` | `metrics.SetRetentionEntries("Active", n)` etc. |
| Each compensation loop tick | `compensation_queue_length` | `metrics.SetCompensationQueueLength(c.queue.Len())` |

**Important:** `events_total` is incremented ONCE per Dispatch call, at the exit, with the actual outcome. Calling it at entry with an empty result label would create a useless `"type=X, result=""` series — never do this.

**DNS sync counter** — instrumented in `cloudflareManager` where the API calls happen:

```go
metrics.RecordDNSSync("create_records", "success")
metrics.RecordDNSSync("create_records", "failure")
metrics.RecordDNSSync("delete_records", "success")
```

The `metrics` package import in `cloudflareManager` is the only cross-package dependency from infra code. It is safe because the helpers are pure functions over package-level vars.

---

## Out of Scope

- Authentication / TLS on the HTTP server
- Metrics on the metrics server itself (Prometheus self-instruments via `promhttp.Handler`)
- Structured logging changes in `cloudflareManager` beyond DNS sync results
- OpenTelemetry trace export (existing `otel` deps in `go.mod` are not extended)
- Historical metric retention (Prometheus scrapes and stores — we just expose)
- Per-hostname time-series (cardinality explosion risk)
- Push gateway or remote-write support
- Web UI / dashboard
