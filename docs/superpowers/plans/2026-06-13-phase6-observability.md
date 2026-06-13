# Phase 6: Observability & Operations Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add structured log fields, a Prometheus `/metrics` endpoint, and a `/debug/state` diagnostics endpoint to DockTunnel.

**Architecture:** Three new packages (`internal/metrics`, `internal/diagnostics`, `internal/server`) wrapped by a single `http.ServeMux` on `127.0.0.1:9100`. The HTTP server is read-only with respect to controller state — it reads snapshots only, never triggers Cloudflare API calls. Controller is instrumented at Dispatch/Reconcile/GC/compensation entry points.

**Tech Stack:** Go 1.24, `github.com/prometheus/client_golang` (new dep), stdlib `net/http`, existing `log/slog`.

---

## File Structure

**New files:**

| File | Responsibility |
|------|----------------|
| `internal/metrics/metrics.go` | 5 Prometheus metric vars + helper funcs (RecordEvent, ObserveReconcile, RecordDNSSync, SetRetentionEntries, SetCompensationQueueLength) |
| `internal/metrics/metrics_test.go` | Unit tests for helpers (no panic, label set correct) |
| `internal/diagnostics/diagnostics.go` | `RuleView`, `StateDiff`, `DebugStateResponse` types; `ComputeDiff` pure function; `Handler` factory |
| `internal/diagnostics/diagnostics_test.go` | Tests for `ComputeDiff` (cases: identical, disjoint, partial overlap, empty sides) and `Handler` (JSON shape) |
| `internal/server/server.go` | `Server` struct, `New(addr, snapshot)`, `Start(ctx)` with graceful shutdown |
| `internal/server/server_test.go` | Route registration, ctx cancel, bind error propagation |

**Modified files:**

| File | Changes |
|------|---------|
| `go.mod` / `go.sum` | Add `github.com/prometheus/client_golang` |
| `pkg/types/tunnel.go` | Add `String()` method on `EntryStatus` for metric labels |
| `internal/config/config.go` | Add `Server` struct, defaults, `GetServerAddr()`, `SanitizeForLog` entry |
| `internal/config/config_test.go` | Test `GetServerAddr` defaults + override |
| `internal/controller/controller.go` | Add `lastKnownActualRules` field + mutex; `GetDebugState()` method; `refreshActualState()` helper called from Sync/Reconcile; instrument Dispatch, Reconcile caller, GC, compensation loop |
| `internal/cloudflareManager/tunnel.go` | Add `GetConfiguration()` method (live tunnel config fetch); add `metrics.RecordDNSSync()` calls in `UpsertDNSRecords` / `DeleteDNSRecords` |
| `cmd/docktunnel/main.go` | Construct + start HTTP server in goroutine |

---

### Task 1: Add Prometheus client_golang dependency

**Files:**
- Modify: `go.mod`, `go.sum`

- [ ] **Step 1: Add the dependency**

Run:
```bash
cd /workspace && go get github.com/prometheus/client_golang/prometheus
cd /workspace && go get github.com/prometheus/client_golang/prometheus/promauto
cd /workspace && go get github.com/prometheus/client_golang/prometheus/promhttp
cd /workspace && go mod tidy
```

- [ ] **Step 2: Verify the dependency was added**

Run: `cd /workspace && grep "prometheus/client_golang" go.mod`
Expected: A single line like `github.com/prometheus/client_golang v1.20.5` (version may differ).

- [ ] **Step 3: Verify the project still builds**

Run: `cd /workspace && go build ./...`
Expected: no errors.

- [ ] **Step 4: Verify existing tests still pass**

Run: `cd /workspace && go test ./...`
Expected: all tests pass (no behavioral changes from a dep add).

- [ ] **Step 5: Commit**

```bash
cd /workspace && git add go.mod go.sum
git commit -m "chore: add prometheus/client_golang dependency for Phase 6"
```

---

### Task 2: Add String() method on EntryStatus

The retention gauge needs string label values. `EntryStatus` is currently an int enum without a String method.

**Files:**
- Modify: `pkg/types/tunnel.go` (add String method after the const block at line 23)
- Test: `pkg/types/tunnel_test.go` (new file)

- [ ] **Step 1: Write the failing test**

Create `pkg/types/tunnel_test.go`:

```go
package types

import "testing"

func TestEntryStatus_String(t *testing.T) {
    tests := []struct {
        status EntryStatus
        want   string
    }{
        {StatusActive, "Active"},
        {StatusPendingDelete, "PendingDelete"},
        {StatusRetaining, "Retaining"},
        {StatusDeleted, "Deleted"},
        {StatusFlapping, "Flapping"},
        {EntryStatus(99), "Unknown"},
    }
    for _, tt := range tests {
        got := tt.status.String()
        if got != tt.want {
            t.Errorf("EntryStatus(%d).String() = %q, want %q", tt.status, got, tt.want)
        }
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /workspace && go test ./pkg/types/ -run TestEntryStatus_String -v`
Expected: FAIL / compile error (`tt.status.String undefined`).

- [ ] **Step 3: Implement the String method**

Add to `pkg/types/tunnel.go` immediately after the `EntryStatus` const block (after line 23):

```go
// String returns the human-readable name of the status.
func (s EntryStatus) String() string {
    switch s {
    case StatusActive:
        return "Active"
    case StatusPendingDelete:
        return "PendingDelete"
    case StatusRetaining:
        return "Retaining"
    case StatusDeleted:
        return "Deleted"
    case StatusFlapping:
        return "Flapping"
    default:
        return "Unknown"
    }
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /workspace && go test ./pkg/types/ -run TestEntryStatus_String -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /workspace && git add pkg/types/tunnel.go pkg/types/tunnel_test.go
git commit -m "feat(types): add String method to EntryStatus for metric labels"
```

---

### Task 3: Create metrics package with 5 metrics + helpers

**Files:**
- Create: `internal/metrics/metrics.go`
- Test: `internal/metrics/metrics_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/metrics/metrics_test.go`:

```go
package metrics

import (
    "testing"

    "github.com/prometheus/client_golang/prometheus/testutil"
)

func TestRecordEvent_IncrementsCounter(t *testing.T) {
    // Reset by checking delta — counter starts at 0 for fresh label set
    before := testutil.ToFloat64(EventsTotal.WithLabelValues("start", "success"))
    RecordEvent("start", "success")
    after := testutil.ToFloat64(EventsTotal.WithLabelValues("start", "success"))
    if after-before != 1 {
        t.Errorf("expected delta=1, got %v", after-before)
    }
}

func TestObserveReconcile_RecordsDuration(t *testing.T) {
    // Histograms don't expose count via ToFloat64 directly; verify no panic.
    ObserveReconcile(0.123)
}

func TestRecordDNSSync_IncrementsCounter(t *testing.T) {
    before := testutil.ToFloat64(DNSSyncOperations.WithLabelValues("upsert", "success"))
    RecordDNSSync("upsert", "success")
    after := testutil.ToFloat64(DNSSyncOperations.WithLabelValues("upsert", "success"))
    if after-before != 1 {
        t.Errorf("expected delta=1, got %v", after-before)
    }
}

func TestSetRetentionEntries_SetsGauge(t *testing.T) {
    SetRetentionEntries("Active", 5)
    if got := testutil.ToFloat64(RetentionEntries.WithLabelValues("Active")); got != 5 {
        t.Errorf("expected 5, got %v", got)
    }
}

func TestSetCompensationQueueLength_SetsGauge(t *testing.T) {
    SetCompensationQueueLength(7)
    if got := testutil.ToFloat64(CompensationQueueLength); got != 7 {
        t.Errorf("expected 7, got %v", got)
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /workspace && go test ./internal/metrics/ -v`
Expected: FAIL / compile error (package doesn't exist).

- [ ] **Step 3: Implement the metrics package**

Create `internal/metrics/metrics.go`:

```go
// Package metrics defines DockTunnel's Prometheus metrics and helper
// functions for instrumented call sites. Importing this package registers
// all metrics with the default Prometheus registry.
package metrics

import (
    "github.com/prometheus/client_golang/prometheus"
    "github.com/prometheus/client_golang/prometheus/promauto"
)

var (
    // EventsTotal counts Docker events processed by Dispatch.
    EventsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
        Namespace: "docktunnel",
        Name:      "events_total",
        Help:      "Total Docker events processed by type and result.",
    }, []string{"type", "result"})

    // ReconcileDuration observes wall-clock time of Reconcile cycles.
    ReconcileDuration = promauto.NewHistogram(prometheus.HistogramOpts{
        Namespace: "docktunnel",
        Name:      "reconcile_duration_seconds",
        Help:      "Wall-clock time spent in Reconcile().",
        Buckets:   prometheus.DefBuckets,
    })

    // DNSSyncOperations counts Cloudflare DNS API calls.
    DNSSyncOperations = promauto.NewCounterVec(prometheus.CounterOpts{
        Namespace: "docktunnel",
        Name:      "dns_sync_operations_total",
        Help:      "DNS sync operations against Cloudflare by operation and result.",
    }, []string{"operation", "result"})

    // RetentionEntries tracks tunnel entries grouped by lifecycle status.
    RetentionEntries = promauto.NewGaugeVec(prometheus.GaugeOpts{
        Namespace: "docktunnel",
        Name:      "retention_entries",
        Help:      "Number of retention entries by status (Active, Retaining, PendingDelete).",
    }, []string{"status"})

    // CompensationQueueLength tracks the compensation queue depth.
    CompensationQueueLength = promauto.NewGauge(prometheus.GaugeOpts{
        Namespace: "docktunnel",
        Name:      "compensation_queue_length",
        Help:      "Number of items currently in the compensation queue.",
    })
)

// RecordEvent increments EventsTotal with the given labels.
func RecordEvent(eventType, result string) {
    EventsTotal.WithLabelValues(eventType, result).Inc()
}

// ObserveReconcile records a Reconcile cycle's duration in seconds.
func ObserveReconcile(seconds float64) {
    ReconcileDuration.Observe(seconds)
}

// RecordDNSSync increments DNSSyncOperations with the given labels.
func RecordDNSSync(operation, result string) {
    DNSSyncOperations.WithLabelValues(operation, result).Inc()
}

// SetRetentionEntries sets the gauge for the given status.
func SetRetentionEntries(status string, n int) {
    RetentionEntries.WithLabelValues(status).Set(float64(n))
}

// SetCompensationQueueLength sets the compensation queue gauge.
func SetCompensationQueueLength(n int) {
    CompensationQueueLength.Set(float64(n))
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /workspace && go test ./internal/metrics/ -v`
Expected: all 5 tests PASS.

- [ ] **Step 5: Commit**

```bash
cd /workspace && git add internal/metrics/metrics.go internal/metrics/metrics_test.go
git commit -m "feat(metrics): add Prometheus metrics package with 5 metrics and helpers"
```

---

### Task 4: Create diagnostics types and ComputeDiff

**Files:**
- Create: `internal/diagnostics/diagnostics.go`
- Test: `internal/diagnostics/diagnostics_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/diagnostics/diagnostics_test.go`:

```go
package diagnostics

import (
    "reflect"
    "sort"
    "testing"
)

func TestComputeDiff_Identical(t *testing.T) {
    desired := []RuleView{{Hostname: "a.com"}, {Hostname: "b.com"}}
    actual := []RuleView{{Hostname: "a.com"}, {Hostname: "b.com"}}
    diff := ComputeDiff(desired, actual)
    if len(diff.DesiredOnly) != 0 || len(diff.ActualOnly) != 0 {
        t.Errorf("expected no differences, got %+v", diff)
    }
    if diff.Matching != 2 {
        t.Errorf("expected Matching=2, got %d", diff.Matching)
    }
}

func TestComputeDiff_Disjoint(t *testing.T) {
    desired := []RuleView{{Hostname: "a.com"}}
    actual := []RuleView{{Hostname: "b.com"}}
    diff := ComputeDiff(desired, actual)
    if !reflect.DeepEqual(sorted(diff.DesiredOnly), []string{"a.com"}) {
        t.Errorf("DesiredOnly = %v, want [a.com]", diff.DesiredOnly)
    }
    if !reflect.DeepEqual(sorted(diff.ActualOnly), []string{"b.com"}) {
        t.Errorf("ActualOnly = %v, want [b.com]", diff.ActualOnly)
    }
    if diff.Matching != 0 {
        t.Errorf("expected Matching=0, got %d", diff.Matching)
    }
}

func TestComputeDiff_PartialOverlap(t *testing.T) {
    desired := []RuleView{{Hostname: "a.com"}, {Hostname: "b.com"}, {Hostname: "c.com"}}
    actual := []RuleView{{Hostname: "b.com"}, {Hostname: "d.com"}}
    diff := ComputeDiff(desired, actual)
    if !reflect.DeepEqual(sorted(diff.DesiredOnly), []string{"a.com", "c.com"}) {
        t.Errorf("DesiredOnly = %v, want [a.com c.com]", diff.DesiredOnly)
    }
    if !reflect.DeepEqual(sorted(diff.ActualOnly), []string{"d.com"}) {
        t.Errorf("ActualOnly = %v, want [d.com]", diff.ActualOnly)
    }
    if diff.Matching != 1 {
        t.Errorf("expected Matching=1, got %d", diff.Matching)
    }
}

func TestComputeDiff_EmptySides(t *testing.T) {
    diff := ComputeDiff(nil, nil)
    if len(diff.DesiredOnly) != 0 || len(diff.ActualOnly) != 0 || diff.Matching != 0 {
        t.Errorf("expected zero diff, got %+v", diff)
    }
}

func TestComputeDiff_DuplicatesInInput(t *testing.T) {
    // Duplicate hostnames in input should be deduplicated by the set logic.
    desired := []RuleView{{Hostname: "a.com"}, {Hostname: "a.com"}}
    actual := []RuleView{{Hostname: "a.com"}}
    diff := ComputeDiff(desired, actual)
    if diff.Matching != 1 {
        t.Errorf("expected Matching=1, got %d", diff.Matching)
    }
}

func sorted(s []string) []string {
    out := append([]string(nil), s...)
    sort.Strings(out)
    return out
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /workspace && go test ./internal/diagnostics/ -v`
Expected: FAIL / compile error (package doesn't exist).

- [ ] **Step 3: Implement types and ComputeDiff**

Create `internal/diagnostics/diagnostics.go`:

```go
// Package diagnostics provides types and helpers for the /debug/state endpoint.
package diagnostics

import "time"

// RuleView is a single ingress rule as exposed in the debug response.
type RuleView struct {
    Hostname string `json:"hostname"`
    Service  string `json:"service"`
    Path     string `json:"path,omitempty"`
}

// StateDiff classifies rules by where they appear relative to desired/actual.
type StateDiff struct {
    DesiredOnly []string `json:"desired_only"`
    ActualOnly  []string `json:"actual_only"`
    Matching    int      `json:"matching"`
}

// DebugStateResponse is the JSON returned by /debug/state.
type DebugStateResponse struct {
    Timestamp    time.Time  `json:"timestamp"`
    DesiredState []RuleView `json:"desired_state"`
    ActualState  []RuleView `json:"actual_state"`
    Source       string     `json:"source"`
    Diff         StateDiff  `json:"diff"`
}

// ComputeDiff returns the set difference of desired vs actual by hostname.
// Duplicate hostnames within a single slice are deduplicated.
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

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /workspace && go test ./internal/diagnostics/ -v`
Expected: all 5 tests PASS.

- [ ] **Step 5: Commit**

```bash
cd /workspace && git add internal/diagnostics/diagnostics.go internal/diagnostics/diagnostics_test.go
git commit -m "feat(diagnostics): add types and ComputeDiff for /debug/state endpoint"
```

---

### Task 5: Add Handler to diagnostics package

**Files:**
- Modify: `internal/diagnostics/diagnostics.go` (append Handler func)
- Test: `internal/diagnostics/diagnostics_test.go` (append Handler test)

- [ ] **Step 1: Write the failing test**

Append to `internal/diagnostics/diagnostics_test.go`:

```go
func TestHandler_ReturnsJSON(t *testing.T) {
    snapshot := func() DebugStateResponse {
        return DebugStateResponse{
            Timestamp:    time.Date(2026, 6, 13, 14, 30, 0, 0, time.UTC),
            DesiredState: []RuleView{{Hostname: "a.com", Service: "http://localhost:80"}},
            ActualState:  []RuleView{{Hostname: "a.com", Service: "http://localhost:80"}},
            Source:       "live_cache",
            Diff:         StateDiff{Matching: 1},
        }
    }

    req := httptest.NewRequest(http.MethodGet, "/debug/state", nil)
    rec := httptest.NewRecorder()

    Handler(snapshot).ServeHTTP(rec, req)

    if rec.Code != http.StatusOK {
        t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
    }
    if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
        t.Errorf("Content-Type = %q, want application/json", ct)
    }

    var resp DebugStateResponse
    if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
        t.Fatalf("failed to unmarshal response: %v", err)
    }
    if resp.Source != "live_cache" {
        t.Errorf("Source = %q, want live_cache", resp.Source)
    }
    if len(resp.DesiredState) != 1 || resp.DesiredState[0].Hostname != "a.com" {
        t.Errorf("DesiredState = %+v, want single a.com entry", resp.DesiredState)
    }
}
```

Also add to the imports at the top of the test file:

```go
import (
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "reflect"
    "sort"
    "testing"
    "time"
)
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /workspace && go test ./internal/diagnostics/ -run TestHandler -v`
Expected: FAIL / compile error (`Handler undefined`).

- [ ] **Step 3: Implement Handler**

Append to `internal/diagnostics/diagnostics.go`. Also add `encoding/json`, `net/http` to the imports:

```go
import (
    "encoding/json"
    "net/http"
    "time"
)
```

Append the Handler function at the end of the file:

```go
// Handler returns an http.HandlerFunc that serves the current debug state.
// The snapshot function is called on every request; it must be safe to call
// from any goroutine.
func Handler(snapshot func() DebugStateResponse) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        resp := snapshot()
        w.Header().Set("Content-Type", "application/json")
        if err := json.NewEncoder(w).Encode(resp); err != nil {
            http.Error(w, "failed to encode response", http.StatusInternalServerError)
        }
    }
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /workspace && go test ./internal/diagnostics/ -v`
Expected: all tests PASS (including the new Handler test).

- [ ] **Step 5: Commit**

```bash
cd /workspace && git add internal/diagnostics/diagnostics.go internal/diagnostics/diagnostics_test.go
git commit -m "feat(diagnostics): add Handler for HTTP serving of debug state"
```

---

### Task 6: Create server package

**Files:**
- Create: `internal/server/server.go`
- Test: `internal/server/server_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/server/server_test.go`:

```go
package server

import (
    "context"
    "encoding/json"
    "net"
    "net/http"
    "net/http/httptest"
    "testing"
    "time"

    "docktunnel/internal/diagnostics"
)

func TestNew_RegistersRoutes(t *testing.T) {
    snapshot := func() diagnostics.DebugStateResponse {
        return diagnostics.DebugStateResponse{Source: "test"}
    }
    srv := New("127.0.0.1:0", snapshot)

    // /healthz
    req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
    rec := httptest.NewRecorder()
    srv.srv.Handler.ServeHTTP(rec, req)
    if rec.Code != http.StatusOK {
        t.Errorf("/healthz status = %d, want 200", rec.Code)
    }
    if rec.Body.String() != "ok" {
        t.Errorf("/healthz body = %q, want ok", rec.Body.String())
    }

    // /debug/state
    req = httptest.NewRequest(http.MethodGet, "/debug/state", nil)
    rec = httptest.NewRecorder()
    srv.srv.Handler.ServeHTTP(rec, req)
    if rec.Code != http.StatusOK {
        t.Errorf("/debug/state status = %d, want 200", rec.Code)
    }
    var resp diagnostics.DebugStateResponse
    if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
        t.Errorf("/debug/state invalid JSON: %v", err)
    }

    // /metrics
    req = httptest.NewRequest(http.MethodGet, "/metrics", nil)
    rec = httptest.NewRecorder()
    srv.srv.Handler.ServeHTTP(rec, req)
    if rec.Code != http.StatusOK {
        t.Errorf("/metrics status = %d, want 200", rec.Code)
    }
}

func TestStart_ReturnsOnContextCancel(t *testing.T) {
    // Find a free port by opening a listener and closing it.
    l, err := net.Listen("tcp", "127.0.0.1:0")
    if err != nil {
        t.Fatalf("failed to find free port: %v", err)
    }
    addr := l.Addr().String()
    l.Close()

    snapshot := func() diagnostics.DebugStateResponse {
        return diagnostics.DebugStateResponse{}
    }
    srv := New(addr, snapshot)

    ctx, cancel := context.WithCancel(context.Background())
    done := make(chan error, 1)
    go func() {
        done <- srv.Start(ctx)
    }()

    // Give server a moment to bind.
    time.Sleep(50 * time.Millisecond)
    cancel()

    select {
    case err := <-done:
        if err != nil {
            t.Errorf("Start returned error on ctx cancel: %v", err)
        }
    case <-time.After(2 * time.Second):
        t.Fatal("Start did not return within 2s of ctx cancel")
    }
}

func TestStart_PropagatesBindError(t *testing.T) {
    // Bind something on the port first.
    l, err := net.Listen("tcp", "127.0.0.1:0")
    if err != nil {
        t.Fatalf("failed to find free port: %v", err)
    }
    defer l.Close()
    addr := l.Addr().String()

    snapshot := func() diagnostics.DebugStateResponse {
        return diagnostics.DebugStateResponse{}
    }
    srv := New(addr, snapshot)

    ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
    defer cancel()
    err = srv.Start(ctx)
    if err == nil {
        t.Fatal("expected bind error, got nil")
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /workspace && go test ./internal/server/ -v`
Expected: FAIL / compile error (package doesn't exist).

- [ ] **Step 3: Implement the server**

Create `internal/server/server.go`:

```go
// Package server provides the HTTP server exposing /metrics, /debug/state,
// and /healthz for observability tooling.
package server

import (
    "context"
    "net/http"
    "time"

    "github.com/prometheus/client_golang/prometheus/promhttp"

    "docktunnel/internal/diagnostics"
)

// Server wraps an *http.Server with the standard DockTunnel routes registered.
type Server struct {
    srv *http.Server
}

// New builds a Server with /metrics, /debug/state, and /healthz registered.
// addr is host:port (e.g., "127.0.0.1:9100"). The snapshot function is called
// on every /debug/state request and must be goroutine-safe.
func New(addr string, snapshot func() diagnostics.DebugStateResponse) *Server {
    mux := http.NewServeMux()
    mux.Handle("/metrics", promhttp.Handler())
    mux.Handle("/debug/state", diagnostics.Handler(snapshot))
    mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
        _, _ = w.Write([]byte("ok"))
    })

    return &Server{
        srv: &http.Server{
            Addr:              addr,
            Handler:           mux,
            ReadHeaderTimeout: 5 * time.Second,
        },
    }
}

// Start begins serving in a goroutine. It blocks until ctx is cancelled
// (graceful shutdown) or the server fails to bind. The returned error is
// non-nil only on bind failure.
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

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /workspace && go test ./internal/server/ -v`
Expected: all 3 tests PASS.

- [ ] **Step 5: Commit**

```bash
cd /workspace && git add internal/server/server.go internal/server/server_test.go
git commit -m "feat(server): add HTTP server with /metrics, /debug/state, /healthz"
```

---

### Task 7: Add Server config

**Files:**
- Modify: `internal/config/config.go` (add Server struct at line 84 after Persistence; defaults at line 240; getter + SanitizeForLog)
- Test: `internal/config/config_test.go` (append test)

- [ ] **Step 1: Write the failing test**

Append to `internal/config/config_test.go`:

```go
func TestServerDefaults(t *testing.T) {
    cfg := &Config{}
    addr := cfg.GetServerAddr()
    if addr != "127.0.0.1:9100" {
        t.Errorf("default addr = %q, want 127.0.0.1:9100", addr)
    }
}

func TestServerOverride(t *testing.T) {
    cfg := &Config{}
    cfg.Server.BindAddr = "0.0.0.0"
    cfg.Server.Port = 8080
    addr := cfg.GetServerAddr()
    if addr != "0.0.0.0:8080" {
        t.Errorf("override addr = %q, want 0.0.0.0:8080", addr)
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /workspace && go test ./internal/config/ -run TestServer -v`
Expected: FAIL / compile error (`GetServerAddr undefined`).

- [ ] **Step 3: Add Server struct to Config**

In `internal/config/config.go`, find the Persistence block (around line 80-83):

```go
    Persistence struct {
        BackupCount    int  `mapstructure:"backupCount"`
        ValidateOnLoad bool `mapstructure:"validateOnLoad"`
    } `mapstructure:"persistence"`
```

Add immediately after it (still inside the outer Config struct):

```go
    Server struct {
        BindAddr string `mapstructure:"bindAddr"`
        Port     int    `mapstructure:"port"`
    } `mapstructure:"server"`
```

- [ ] **Step 4: Add viper defaults**

In the `New()` function, find the persistence defaults (around line 238-239):

```go
    // Persistence defaults
    v.SetDefault("persistence.backupCount", 3)
    v.SetDefault("persistence.validateOnLoad", true)
```

Add immediately after:

```go
    // Server defaults
    v.SetDefault("server.bindAddr", "127.0.0.1")
    v.SetDefault("server.port", 9100)
```

- [ ] **Step 5: Add GetServerAddr method**

Find the `GetPersistenceConfig` method (around line 135-146) and add immediately after it:

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

- [ ] **Step 6: Add Server entry to SanitizeForLog**

Find the `"persistence"` entry in `SanitizeForLog` (around line 339-342):

```go
        "persistence": map[string]interface{}{
            "backupCount":    c.Persistence.BackupCount,
            "validateOnLoad": c.Persistence.ValidateOnLoad,
        },
```

Add immediately after:

```go
        "server": map[string]interface{}{
            "bindAddr": c.Server.BindAddr,
            "port":     c.Server.Port,
        },
```

- [ ] **Step 7: Run test to verify it passes**

Run: `cd /workspace && go test ./internal/config/ -run TestServer -v`
Expected: both tests PASS.

- [ ] **Step 8: Run full config package tests to ensure no regression**

Run: `cd /workspace && go test ./internal/config/ -v`
Expected: all tests PASS.

- [ ] **Step 9: Commit**

```bash
cd /workspace && git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): add Server section (bindAddr, port) with defaults"
```

---

### Task 8: Add controller debug-state caching + GetDebugState

This task adds the controller-side state that the `/debug/state` endpoint reads.

**Important clarification:** DockTunnel's controller does not currently fetch the live tunnel config from Cloudflare; it only pushes updates. So `actual_state` represents what the controller last successfully pushed via `UpdateConfiguration` — not a fresh read from Cloudflare. The diff catches drift between desired (`c.ingressRules`) and last-pushed state. This is documented in the `Source` field as `"live_cache"`.

**Files:**
- Modify: `internal/controller/controller.go` (add fields at line 80; add method at end of file)
- Test: `internal/controller/controller_test.go` (new file if not exists; append if exists)

- [ ] **Step 1: Write the failing test**

Create or append to `internal/controller/controller_test.go`:

```go
package controller

import (
    "testing"

    "docktunnel/internal/diagnostics"
)

func TestGetDebugState_InitialState(t *testing.T) {
    c := &Controller{}
    resp := c.GetDebugState()
    if resp.Source != "empty" {
        t.Errorf("initial Source = %q, want empty", resp.Source)
    }
    if len(resp.DesiredState) != 0 {
        t.Errorf("initial DesiredState = %v, want empty", resp.DesiredState)
    }
}

func TestSetLastKnownActualRules_UpdatesCache(t *testing.T) {
    c := &Controller{}
    rules := []diagnostics.RuleView{
        {Hostname: "a.com", Service: "http://localhost:80"},
    }
    c.setLastKnownActualRules(rules)

    resp := c.GetDebugState()
    if resp.Source != "live_cache" {
        t.Errorf("Source = %q, want live_cache", resp.Source)
    }
    if len(resp.ActualState) != 1 || resp.ActualState[0].Hostname != "a.com" {
        t.Errorf("ActualState = %v, want single a.com", resp.ActualState)
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /workspace && go test ./internal/controller/ -run TestGetDebugState -v`
Expected: FAIL / compile error (`GetDebugState undefined`).

- [ ] **Step 3: Add fields and methods to Controller**

First, add fields to the Controller struct. Find line 80 (the closing `}` of the struct) and add new fields just before it:

```go
    // Cached actual state for /debug/state diagnostics endpoint.
    // Updated after successful UpdateConfiguration calls.
    actualStateMu        sync.RWMutex
    lastKnownActualRules []diagnostics.RuleView
```

Add the `diagnostics` import to the top of the file (in the existing import block):

```go
    "docktunnel/internal/diagnostics"
```

Add at the end of the file (after the last method):

```go
// GetDebugState returns the current desired vs actual state for the /debug/state endpoint.
// Safe to call from any goroutine. Uses cached state — never makes Cloudflare API calls.
func (c *Controller) GetDebugState() diagnostics.DebugStateResponse {
    c.actualStateMu.RLock()
    defer c.actualStateMu.RUnlock()

    desired := c.snapshotRuleViewsLocked()
    actual := c.lastKnownActualRules
    source := "empty"
    if actual != nil {
        source = "live_cache"
    }

    return diagnostics.DebugStateResponse{
        Timestamp:    time.Now(),
        DesiredState: desired,
        ActualState:  actual,
        Source:       source,
        Diff:         diagnostics.ComputeDiff(desired, actual),
    }
}

// setLastKnownActualRules replaces the cached actual-state snapshot.
// Called by Sync/Reconcile after a successful UpdateConfiguration push.
func (c *Controller) setLastKnownActualRules(rules []diagnostics.RuleView) {
    c.actualStateMu.Lock()
    c.lastKnownActualRules = rules
    c.actualStateMu.Unlock()
}

// snapshotRuleViewsLocked builds a slice of RuleView from c.ingressRules.
// Caller must hold c.mu (read or write).
func (c *Controller) snapshotRuleViewsLocked() []diagnostics.RuleView {
    views := make([]diagnostics.RuleView, 0, len(c.ingressRules))
    for _, rule := range c.ingressRules {
        views = append(views, diagnostics.RuleView{
            Hostname: rule.Hostname.Value,
            Service:  rule.Service.Value,
            Path:     rule.Path.Value,
        })
    }
    return views
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /workspace && go test ./internal/controller/ -run TestGetDebugState -v`
Expected: both tests PASS.

- [ ] **Step 5: Verify the rest of the controller package still compiles**

Run: `cd /workspace && go build ./internal/controller/`
Expected: no errors.

- [ ] **Step 6: Commit**

```bash
cd /workspace && git add internal/controller/controller.go internal/controller/controller_test.go
git commit -m "feat(controller): add GetDebugState and last-known actual rules cache"
```

---

### Task 9: Add GetConfiguration to cloudflareManager (real Cloudflare state)

The controller currently has no way to read what's actually configured in Cloudflare — it only pushes via `UpdateConfiguration`. To make `/debug/state` show real drift, we add a `GetConfiguration` method that fetches the live tunnel config.

**Files:**
- Modify: `internal/cloudflareManager/tunnel.go` (add `GetConfiguration` method near `UpdateConfiguration` at line 296)
- Modify: `internal/controller/controller.go` (extend `CloudflareManager` interface at line 25)
- Test: `internal/cloudflareManager/tunnel_test.go` (new file)

- [ ] **Step 1: Add the method to cloudflareManager**

In `internal/cloudflareManager/tunnel.go`, immediately after `UpdateConfiguration` (after line 321), add:

```go
// GetConfiguration fetches the current tunnel configuration from Cloudflare.
// Returns the ingress rules currently configured on the tunnel.
func (m *Manager) GetConfiguration(ctx context.Context) ([]zero_trust.TunnelCloudflaredConfigurationGetResponseConfigIngress, error) {
    if m.tunnel == nil {
        return nil, fmt.Errorf("tunnel is not available")
    }

    params := zero_trust.TunnelCloudflaredConfigurationGetParams{
        AccountID: cloudflare.F(m.account),
    }

    var result []zero_trust.TunnelCloudflaredConfigurationGetResponseConfigIngress
    err := m.callWithRetry(ctx, func() error {
        resp, err := m.client.ZeroTrust.Tunnels.Cloudflared.Configurations.Get(ctx, m.tunnel.ID, params)
        if err != nil {
            return err
        }
        result = resp.Result.Config.Ingress
        return nil
    })
    if err != nil {
        return nil, fmt.Errorf("failed to get tunnel configuration: %w", err)
    }

    return result, nil
}
```

- [ ] **Step 2: Add the method to the CloudflareManager interface**

In `internal/controller/controller.go`, find the `CloudflareManager` interface (line 25-31) and add the method:

```go
type CloudflareManager interface {
    GetTunnel() *zero_trust.TunnelCloudflaredGetResponse
    GetConfiguration(ctx context.Context) ([]zero_trust.TunnelCloudflaredConfigurationGetResponseConfigIngress, error)
    UpdateConfiguration(ctx context.Context, ingressRules []zero_trust.TunnelCloudflaredConfigurationUpdateParamsConfigIngress) error
    ListDNSRecords(ctx context.Context) ([]dns.RecordResponse, error)
    DeleteDNSRecords(ctx context.Context, hostnames []string) error
    UpsertDNSRecords(ctx context.Context, hostnames []string) error
}
```

- [ ] **Step 3: Check for existing mock implementations of CloudflareManager**

Run: `cd /workspace && grep -rn "CloudflareManager" --include="*.go" | grep -v "_test.go" | head`

If any mock/fake implementations exist (e.g., in test files), they will now fail to satisfy the interface until `GetConfiguration` is added. Update each one to add a stub:

```go
func (m *MockManager) GetConfiguration(ctx context.Context) ([]zero_trust.TunnelCloudflaredConfigurationGetResponseConfigIngress, error) {
    return nil, nil
}
```

If no mocks exist, skip this step.

- [ ] **Step 4: Verify the project builds**

Run: `cd /workspace && go build ./...`
Expected: no errors.

- [ ] **Step 5: Run all tests**

Run: `cd /workspace && go test ./...`
Expected: all tests PASS (no behavioral changes from adding a method that isn't called yet).

- [ ] **Step 6: Commit**

```bash
cd /workspace && git add internal/cloudflareManager/tunnel.go internal/controller/controller.go
git commit -m "feat(cloudflare): add GetConfiguration to read live tunnel config"
```

---

### Task 10: Populate lastKnownActualRules from real Cloudflare state

The cache is only useful if something writes to it. This task wires `GetConfiguration` into the controller's Sync and Reconcile paths so the cache reflects what Cloudflare actually has — not just what we last pushed.

**Files:**
- Modify: `internal/controller/controller.go` (add helper; call it from Sync and Reconcile)

- [ ] **Step 1: Add a refresh helper to the controller**

Add this method anywhere in `internal/controller/controller.go` (place it near `setLastKnownActualRules`):

```go
// refreshActualState fetches the live tunnel config from Cloudflare and
// caches it for /debug/state. Safe to call from Sync/Reconcile.
func (c *Controller) refreshActualState(ctx context.Context) {
    ingress, err := c.cloudflareManager.GetConfiguration(ctx)
    if err != nil {
        slog.Warn("Failed to fetch live tunnel config for diagnostics",
            "error", err)
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

The `slog` and `diagnostics` packages must already be imported (added in Task 8). If `slog` isn't imported at the top of the file, add `"log/slog"` to the import block.

- [ ] **Step 2: Call refreshActualState at end of Sync**

In `internal/controller/controller.go`, find the `Sync` method (line 447). Locate the END of the method (just before the final `return nil`). Add:

```go
    // Refresh the actual-state cache for /debug/state (Phase 6).
    // Non-fatal: a failure here just means diagnostics show stale data.
    c.refreshActualState(ctx)

    return nil
```

- [ ] **Step 3: Call refreshActualState at end of Reconcile**

Find the `Reconcile` method (line 919). At the END of the method (just before the final `return nil`), add the same call:

```go
    c.refreshActualState(ctx)

    return nil
```

- [ ] **Step 4: Verify build**

Run: `cd /workspace && go build ./...`
Expected: no errors.

- [ ] **Step 5: Run all tests**

Run: `cd /workspace && go test ./...`
Expected: all tests PASS.

- [ ] **Step 6: Commit**

```bash
cd /workspace && git add internal/controller/controller.go
git commit -m "feat(controller): populate lastKnownActualRules from live Cloudflare state"
```

---

### Task 11: Wire HTTP server into main.go

**Files:**
- Modify: `cmd/docktunnel/main.go` (add server import; add goroutine after controller.LoadState at line 106)

- [ ] **Step 1: Add imports**

In `cmd/docktunnel/main.go`, find the import block (around lines 3-24) and add:

```go
    "docktunnel/internal/server"
```

(`diagnostics` is referenced only inside `server.New`'s signature; main.go passes `controller.GetDebugState` as a method value and does not name the `diagnostics` package directly.)

- [ ] **Step 2: Add server goroutine**

Find the existing block where LoadState is called (around line 102-106):

```go
    appLogger.Info("Loading persisted state", "path", statePath)
    if err := controller.LoadState(); err != nil {
        appLogger.Warn("Failed to load persisted state, starting with clean state",
            "error", err)
        // Continue anyway - don't fail startup (T075)
    }
```

Immediately after that block (and before the "创建事件通道" comment around line 108), add:

```go
    // Start HTTP server for metrics and diagnostics (Phase 6)
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

- [ ] **Step 3: Verify the project builds**

Run: `cd /workspace && go build ./...`
Expected: no errors.

- [ ] **Step 4: Smoke-test the server**

Run in background:
```bash
cd /workspace && go build -o /tmp/docktunnel-phase6 ./cmd/docktunnel
```

Then (this won't actually run since it needs CF creds — just verify the binary builds):
```bash
ls -la /tmp/docktunnel-phase6
```
Expected: binary exists, no build errors.

- [ ] **Step 5: Run unit tests**

Run: `cd /workspace && go test ./...`
Expected: all tests PASS.

- [ ] **Step 6: Commit**

```bash
cd /workspace && git add cmd/docktunnel/main.go
git commit -m "feat(main): start HTTP server for /metrics and /debug/state"
```

---

### Task 12: Instrument Dispatch with events_total counter

**Files:**
- Modify: `internal/controller/controller.go` (Dispatch method at line 126)

- [ ] **Step 1: Wrap Dispatch to record outcome**

Replace the existing `Dispatch` method (lines 126-146) with:

```go
// Dispatch 是所有事件处理的统一入口
func (c *Controller) Dispatch(ctx context.Context, event events.Event) error {
    err := c.dispatchInner(ctx, event)

    // Record outcome for /metrics (Phase 6)
    result := "success"
    if err != nil {
        result = "failure"
    }
    metrics.RecordEvent(event.Type, result)

    return err
}

// dispatchInner is the original dispatch switch.
func (c *Controller) dispatchInner(ctx context.Context, event events.Event) error {
    switch event.Type {
    case eventTypes.ActionStart:
        return c.handleContainerStart(ctx, event)
    case eventTypes.ActionStop:
        return c.handleContainerStop(ctx, event)
    case eventTypes.ActionDie:
        return c.handleContainerStop(ctx, event)
    case events.ActionHealthHealthy:
        return c.handleHealthHealthy(ctx, event)
    case events.ActionHealthUnhealthy:
        return c.handleHealthUnhealthy(ctx, event)
    case events.ActionHealthStarting:
        return c.handleHealthUnhealthy(ctx, event)
    case events.ActionResync:
        return c.handleResync(ctx)
    default:
        // 忽略不关心的事件
        return nil
    }
}
```

Add the `metrics` import to the top of the file (if not already present):

```go
    "docktunnel/internal/metrics"
```

- [ ] **Step 2: Verify the controller builds**

Run: `cd /workspace && go build ./internal/controller/`
Expected: no errors.

- [ ] **Step 3: Run all controller tests**

Run: `cd /workspace && go test ./internal/controller/ -v`
Expected: all tests PASS.

- [ ] **Step 4: Commit**

```bash
cd /workspace && git add internal/controller/controller.go
git commit -m "feat(controller): record Dispatch outcome as docktunnel_events_total"
```

---

### Task 13: Instrument Reconcile with duration histogram and GC with retention gauge

The Reconcile call site is in `main.go`'s reconcile ticker. RunGarbageCollection also runs in the same goroutine.

**Files:**
- Modify: `cmd/docktunnel/main.go` (reconcile ticker block around line 179-184)
- Modify: `internal/controller/controller.go` (RunGarbageCollection around line 822)

- [ ] **Step 1: Wrap Reconcile call with timing in main.go**

Find the reconcile ticker case in main.go (around line 179-184):

```go
            case <-reconcileTicker.C:
                appLogger.Debug("Running periodic reconciliation")
                if err := controller.Reconcile(ctx); err != nil {
                    appLogger.Error("Periodic reconciliation failed", "error", err)
                }
```

Replace with:

```go
            case <-reconcileTicker.C:
                appLogger.Debug("Running periodic reconciliation")
                reconcileStart := time.Now()
                if err := controller.Reconcile(ctx); err != nil {
                    appLogger.Error("Periodic reconciliation failed", "error", err)
                }
                metrics.ObserveReconcile(time.Since(reconcileStart).Seconds())
```

Add the `metrics` import to main.go:

```go
    "docktunnel/internal/metrics"
```

- [ ] **Step 2: Update retention gauge after GC**

In `internal/controller/controller.go`, find the `RunGarbageCollection` method (around line 822). At the END of the method (just before the final `return nil`), add code to count entries by status and update the gauge.

First, locate the end of RunGarbageCollection. The simplest path is to add a helper method and call it.

Add this helper method anywhere in controller.go (place it near the other state-related helpers). The state model is: `ActiveTunnels` holds entries with `Status == StatusActive`; `PendingDeletions` holds entries with `Status == StatusRetaining` OR `Status == StatusPendingDelete` (per `internal/state/transition.go:53,66`). We categorize by reading the `Status` field, not by which map the entry sits in:

```go
// updateRetentionGauge counts entries by lifecycle status and updates
// the docktunnel_retention_entries gauge.
func (c *Controller) updateRetentionGauge() {
    snapshot := c.stateManager.GetSnapshot()
    counts := map[string]int{
        "Active":        0,
        "Retaining":     0,
        "PendingDelete": 0,
    }
    for _, entry := range snapshot.ActiveTunnels {
        if entry.Status == types.StatusActive {
            counts["Active"]++
        }
    }
    for _, entry := range snapshot.PendingDeletions {
        switch entry.Status {
        case types.StatusRetaining:
            counts["Retaining"]++
        case types.StatusPendingDelete:
            counts["PendingDelete"]++
        }
    }
    for status, n := range counts {
        metrics.SetRetentionEntries(status, n)
    }
}
```

Add `types` to the imports if not already present:

```go
    "docktunnel/pkg/types"
```

Then at the end of RunGarbageCollection (just before `return nil`), call it:

```go
    // Update retention gauge for /metrics (Phase 6)
    c.updateRetentionGauge()

    return nil
```

- [ ] **Step 3: Verify everything builds**

Run: `cd /workspace && go build ./...`
Expected: no errors.

- [ ] **Step 4: Run all tests**

Run: `cd /workspace && go test ./...`
Expected: all tests PASS.

- [ ] **Step 5: Commit**

```bash
cd /workspace && git add cmd/docktunnel/main.go internal/controller/controller.go
git commit -m "feat(controller): observe reconcile duration and update retention gauge"
```

---

### Task 14: Instrument compensation queue length

**Files:**
- Modify: `internal/controller/controller.go` (RunCompensationLoop at line 428)

- [ ] **Step 1: Add metric update to RunCompensationLoop**

Find `RunCompensationLoop` (line 428):

```go
// RunCompensationLoop starts the compensation queue background loop.
// Blocks until ctx is cancelled.
func (c *Controller) RunCompensationLoop(ctx context.Context) {
    executor := func(action types.Action) error {
        return c.ExecuteAction(ctx, action)
    }
    c.stateManager.RunCompensation(ctx, executor)
}
```

Replace with:

```go
// RunCompensationLoop starts the compensation queue background loop.
// Blocks until ctx is cancelled.
func (c *Controller) RunCompensationLoop(ctx context.Context) {
    executor := func(action types.Action) error {
        return c.ExecuteAction(ctx, action)
    }

    // Set up a gauge updater that runs alongside the compensation loop.
    gaugeDone := make(chan struct{})
    go func() {
        defer close(gaugeDone)
        ticker := time.NewTicker(5 * time.Second)
        defer ticker.Stop()
        for {
            select {
            case <-ticker.C:
                pending := c.stateManager.GetAllPendingActions()
                metrics.SetCompensationQueueLength(len(pending))
            case <-ctx.Done():
                return
            }
        }
    }()

    c.stateManager.RunCompensation(ctx, executor)
    <-gaugeDone
}
```

The `time` package is already imported in controller.go.

- [ ] **Step 2: Verify build**

Run: `cd /workspace && go build ./...`
Expected: no errors.

- [ ] **Step 3: Run all tests**

Run: `cd /workspace && go test ./...`
Expected: all tests PASS.

- [ ] **Step 4: Commit**

```bash
cd /workspace && git add internal/controller/controller.go
git commit -m "feat(controller): expose compensation queue length as gauge"
```

---

### Task 15: Instrument cloudflareManager DNS sync operations

**Files:**
- Modify: `internal/cloudflareManager/tunnel.go` (UpsertDNSRecords at line 427; DeleteDNSRecords at line 482)

- [ ] **Step 1: Add metrics import**

In `internal/cloudflareManager/tunnel.go`, add to the import block:

```go
    "docktunnel/internal/metrics"
```

- [ ] **Step 2: Instrument UpsertDNSRecords**

Find `UpsertDNSRecords` (around line 427). At the start of the function (right after `if len(hostnames) == 0 { return nil }`), add a deferred metric recorder:

```go
// UpsertDNSRecords 批量创建或更新DNS记录
func (m *Manager) UpsertDNSRecords(ctx context.Context, hostnames []string) (err error) {
    if len(hostnames) == 0 {
        return nil
    }

    // Record outcome for /metrics (Phase 6)
    defer func() {
        result := "success"
        if err != nil {
            result = "failure"
        }
        metrics.RecordDNSSync("upsert", result)
    }()

    // ... (rest of existing function unchanged)
```

The key change is renaming the return type to `(err error)` so the deferred func can read it.

- [ ] **Step 3: Instrument DeleteDNSRecords**

Find `DeleteDNSRecords` (around line 482). Apply the same pattern:

```go
// DeleteDNSRecords 批量删除DNS记录
func (m *Manager) DeleteDNSRecords(ctx context.Context, hostnames []string) (err error) {
    if len(hostnames) == 0 {
        slog.Debug("No hostnames to delete")
        return nil
    }

    // Record outcome for /metrics (Phase 6)
    defer func() {
        result := "success"
        if err != nil {
            result = "failure"
        }
        metrics.RecordDNSSync("delete", result)
    }()

    slog.Info("Deleting DNS records", "hostnames", hostnames)

    // ... (rest of existing function unchanged)
```

- [ ] **Step 4: Verify build**

Run: `cd /workspace && go build ./...`
Expected: no errors.

- [ ] **Step 5: Run all tests**

Run: `cd /workspace && go test ./...`
Expected: all tests PASS.

- [ ] **Step 6: Commit**

```bash
cd /workspace && git add internal/cloudflareManager/tunnel.go
git commit -m "feat(cloudflare): record DNS sync operations as Prometheus counter"
```

---

### Task 16: Audit and enrich structured log fields

This task adds `action` and `result` fields to operational log calls. No renames of existing fields. No test changes — this is observability enrichment verified by inspection.

**Files:**
- Modify: `internal/controller/controller.go` (Dispatch handlers, Sync, Reconcile, RunGarbageCollection)
- Modify: `internal/cloudflareManager/tunnel.go` (UpsertDNSRecords, DeleteDNSRecords)

- [ ] **Step 1: Audit Dispatch entry/exit logs**

Find `slog.Info("Handling container start event", "containerID", event.ContainerID)` (around line 226). Update to:

```go
slog.Info("Handling container start event",
    "action", "dispatch_start",
    "containerID", event.ContainerID,
    "type", event.Type)
```

Find the equivalent container stop log (around line 323). Update similarly:

```go
slog.Info("Handling container stop event",
    "action", "dispatch_start",
    "containerID", event.ContainerID,
    "type", event.Type)
```

- [ ] **Step 2: Add result to error paths in handlers**

For every `slog.Error` call in `handleContainerStart`, `handleContainerStop`, `Reconcile`, `Sync`, `RunGarbageCollection`, add `"result", "failure"` if not already present. Pattern:

```go
slog.Error("Failed to ...",
    "action", "...",
    "containerID", containerID,
    "result", "failure",
    "error", err)
```

- [ ] **Step 3: Add action to Reconcile and Sync entry logs**

Find `slog.Info("Starting synchronization", ...)` (around line 454). Add `"action", "sync"`:

```go
slog.Info("Starting synchronization", "action", "sync", "tunnelID", tunnel.ID)
```

Find the corresponding Reconcile entry log. Add `"action", "reconcile"`.

Find the GC entry log in `RunGarbageCollection`. Add `"action", "gc"`.

- [ ] **Step 4: Add result to cloudflareManager DNS logs**

In `internal/cloudflareManager/tunnel.go`:

`UpsertDNSRecords` success path — find existing success log, add `"result", "success"` if a log call exists, otherwise skip.

`DeleteDNSRecords`:
```go
slog.Info("Deleting DNS records",
    "action", "dns_delete",
    "hostnames", hostnames)
```

- [ ] **Step 5: Verify build**

Run: `cd /workspace && go build ./...`
Expected: no errors.

- [ ] **Step 6: Run all tests**

Run: `cd /workspace && go test ./...`
Expected: all tests PASS.

- [ ] **Step 7: Commit**

```bash
cd /workspace && git add internal/controller/controller.go internal/cloudflareManager/tunnel.go
git commit -m "feat(logging): add action and result fields to operational log calls"
```

---

### Task 17: Final verification

**Files:**
- No modifications — verification only.

- [ ] **Step 1: Run full test suite**

Run: `cd /workspace && go test ./...`
Expected: all tests PASS.

- [ ] **Step 2: Run go vet**

Run: `cd /workspace && go vet ./...`
Expected: no issues.

- [ ] **Step 3: Run gofmt check**

Run: `cd /workspace && gofmt -l .`
Expected: empty output (no files need formatting).

If any files appear in the output, format them:
```bash
cd /workspace && gofmt -s -w .
```

Then commit the formatting fix separately:
```bash
cd /workspace && git add -A
git commit -m "style: gofmt all files"
```

- [ ] **Step 4: Run final build**

Run: `cd /workspace && go build -o /tmp/docktunnel-phase6-final ./cmd/docktunnel`
Expected: success, binary at `/tmp/docktunnel-phase6-final`.

- [ ] **Step 5: Manual smoke test (optional, requires running Docker + Cloudflare creds)**

If you have a working DockTunnel setup, start the binary and verify:

```bash
curl http://127.0.0.1:9100/healthz       # → "ok"
curl http://127.0.0.1:9100/metrics        # → Prometheus exposition
curl http://127.0.0.1:9100/debug/state    # → JSON response
```

The `/metrics` output should include lines starting with:
- `docktunnel_events_total`
- `docktunnel_reconcile_duration_seconds`
- `docktunnel_dns_sync_operations_total`
- `docktunnel_retention_entries`
- `docktunnel_compensation_queue_length`

The `/debug/state` output should be valid JSON with `desired_state`, `actual_state`, `source`, and `diff` fields.

- [ ] **Step 6: Final commit (if any cleanup)**

If formatting or vet issues were fixed in step 3, commit them. Otherwise no commit needed.

---

## Done Criteria

- All 5 Prometheus metrics appear at `/metrics` after the binary starts.
- `/debug/state` returns valid JSON with desired/actual/diff.
- `/healthz` returns 200 "ok".
- All existing tests still pass.
- No new tests fail.
- `go vet ./...` is clean.
- `gofmt -l .` is empty.
- The HTTP server starts in a goroutine and shuts down cleanly on SIGINT/SIGTERM.
