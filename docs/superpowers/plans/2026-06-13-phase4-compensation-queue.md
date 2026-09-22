# Phase 4: Failure Compensation & Retry Governance — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a durable compensation queue that persists failed Cloudflare API actions and retries them with exponential backoff, surviving process restarts.

**Architecture:** Compensation records live inside the state manager's `pendingActions` map, persisted via the existing gob state file. A background goroutine drains the queue by calling an executor closure. Errors are classified as retryable or permanent via custom error types propagated from the Cloudflare manager.

**Tech Stack:** Go 1.24, testify assertions, gob encoding, existing state persistence infrastructure.

---

## File Structure

| File | Action | Responsibility |
|------|--------|---------------|
| `pkg/types/errors.go` | Create | `RetryableError` and `PermanentError` types |
| `pkg/types/tunnel.go` | Modify | Add `CompensationRecord` type, add `PendingActions` to `StateSnapshot` |
| `internal/state/manager.go` | Modify | Add `pendingActions` field, `EnqueueAction`, `RunCompensation`, `GetDeadActions` methods; update `NewManager`, `GetSnapshot`, `LoadFromSnapshot` |
| `internal/state/persistence.go` | Modify | Bump `StateVersion` to 3, register new gob types |
| `internal/cloudflareManager/tunnel.go` | Modify | Wrap final error in `callWithRetry` with typed errors |
| `internal/config/config.go` | Modify | Add `Compensation` config section with defaults |
| `internal/controller/controller.go` | Modify | Add `ExecuteAction` method, wire compensation into `handleContainerStop`, start `RunCompensation` goroutine |
| `cmd/docktunnel/main.go` | Modify | Start compensation goroutine alongside GC goroutine |
| `pkg/types/errors_test.go` | Create | Tests for error type classification |
| `internal/state/compensation_test.go` | Create | Tests for `EnqueueAction`, `RunCompensation`, `GetDeadActions`, snapshot round-trip |

---

### Task 1: Error Types

**Files:**
- Create: `pkg/types/errors.go`
- Create: `pkg/types/errors_test.go`

- [ ] **Step 1: Write the failing tests**

Create `pkg/types/errors_test.go`:

```go
package types

import (
	"errors"
	"testing"
)

func TestRetryableError(t *testing.T) {
	inner := errors.New("connection timeout")
	err := &RetryableError{Err: inner}

	if err.Error() != "retryable: connection timeout" {
		t.Errorf("expected 'retryable: connection timeout', got %q", err.Error())
	}
	if !errors.Is(err, inner) {
		t.Error("expected errors.Is to match inner error")
	}
}

func TestPermanentError(t *testing.T) {
	inner := errors.New("403 forbidden")
	err := &PermanentError{Err: inner}

	if err.Error() != "permanent: 403 forbidden" {
		t.Errorf("expected 'permanent: 403 forbidden', got %q", err.Error())
	}
	if !errors.Is(err, inner) {
		t.Error("expected errors.Is to match inner error")
	}
}

func TestAsRetryableError(t *testing.T) {
	err := &RetryableError{Err: errors.New("timeout")}
	var re *RetryableError
	if !errors.As(err, &re) {
		t.Error("expected errors.As to match RetryableError")
	}
}

func TestAsPermanentError(t *testing.T) {
	err := &PermanentError{Err: errors.New("bad request")}
	var pe *PermanentError
	if !errors.As(err, &pe) {
		t.Error("expected errors.As to match PermanentError")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/types/ -run "TestRetryableError|TestPermanentError|TestAsRetryableError|TestAsPermanentError" -v`
Expected: FAIL — `RetryableError` and `PermanentError` types not defined.

- [ ] **Step 3: Write the implementation**

Create `pkg/types/errors.go`:

```go
package types

import "fmt"

// RetryableError wraps an error that should be retried (e.g., 429, 5xx, timeout).
type RetryableError struct {
	Err error
}

func (e *RetryableError) Error() string { return fmt.Sprintf("retryable: %v", e.Err) }
func (e *RetryableError) Unwrap() error { return e.Err }

// PermanentError wraps an error that should not be retried (e.g., 401, 403, bad params).
type PermanentError struct {
	Err error
}

func (e *PermanentError) Error() string { return fmt.Sprintf("permanent: %v", e.Err) }
func (e *PermanentError) Unwrap() error { return e.Err }
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/types/ -run "TestRetryableError|TestPermanentError|TestAsRetryableError|TestAsPermanentError" -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/types/errors.go pkg/types/errors_test.go
git commit -m "feat(types): add RetryableError and PermanentError types"
```

---

### Task 2: CompensationRecord Type and StateSnapshot Update

**Files:**
- Modify: `pkg/types/tunnel.go:129-144`

- [ ] **Step 1: Write the failing test**

Add to `pkg/types/errors_test.go` (or create `pkg/types/compensation_test.go` if preferred):

```go
package types

import (
	"testing"
	"time"
)

func TestCompensationRecordFields(t *testing.T) {
	now := time.Now().UTC()
	rec := CompensationRecord{
		ID:          "rec-1",
		Action:      Action{Kind: ActionDeleteRoute, Hostname: "app.example.com"},
		RetryCount:  0,
		MaxRetries:  10,
		NextRetryAt: now.Add(30 * time.Second),
		LastError:   "",
		CreatedAt:   now,
		Dead:        false,
	}
	if rec.ID != "rec-1" {
		t.Errorf("expected ID rec-1, got %s", rec.ID)
	}
	if rec.Action.Kind != ActionDeleteRoute {
		t.Errorf("expected ActionDeleteRoute, got %d", rec.Action.Kind)
	}
	if rec.Dead {
		t.Error("expected Dead to be false")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/types/ -run TestCompensationRecordFields -v`
Expected: FAIL — `CompensationRecord` not defined.

- [ ] **Step 3: Add CompensationRecord to tunnel.go**

In `pkg/types/tunnel.go`, add after the `Action` struct (after line 135):

```go
// CompensationRecord represents a failed action awaiting retry.
type CompensationRecord struct {
	ID          string        // unique record ID
	Action      Action        // the action to retry
	RetryCount  int
	MaxRetries  int
	NextRetryAt time.Time
	LastError   string
	CreatedAt   time.Time
	Dead        bool // true if permanently failed or retries exhausted
}
```

Update `StateSnapshot` (replace lines 137-144) to add `PendingActions`:

```go
// StateSnapshot represents the persisted state of the controller for recovery after restart
type StateSnapshot struct {
	Version            int
	Timestamp          time.Time
	ActiveTunnels      map[string]*TunnelEntry
	PendingDeletions   map[string]*TunnelEntry
	FlappingContainers map[string]FlappingState
	PendingActions     map[string]*CompensationRecord
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/types/ -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/types/tunnel.go pkg/types/errors_test.go
git commit -m "feat(types): add CompensationRecord and PendingActions to StateSnapshot"
```

---

### Task 3: Cloudflare Manager — Typed Error Propagation

**Files:**
- Modify: `internal/cloudflareManager/tunnel.go:125-181`

- [ ] **Step 1: Write the failing test**

Add a test in `internal/cloudflareManager/tunnel_test.go` (create if needed):

```go
package cloudflareManager

import (
	"context"
	"errors"
	"testing"

	"docktunnel/pkg/types"
)

func TestCallWithRetry_ReturnsRetryableError(t *testing.T) {
	m := &Manager{
		maxRetries:    1,
		retryDelay:    1 * time.Millisecond,
		maxRetryDelay: 1 * time.Millisecond,
	}

	err := m.callWithRetry(context.Background(), func() error {
		return errors.New("server error: 503 service unavailable")
	})

	var re *types.RetryableError
	if !errors.As(err, &re) {
		t.Errorf("expected RetryableError, got %T: %v", err, err)
	}
}

func TestCallWithRetry_ReturnsPermanentError(t *testing.T) {
	m := &Manager{
		maxRetries:    1,
		retryDelay:    1 * time.Millisecond,
		maxRetryDelay: 1 * time.Millisecond,
	}

	err := m.callWithRetry(context.Background(), func() error {
		return errors.New("authentication error: 401 unauthorized")
	})

	var pe *types.PermanentError
	if !errors.As(err, &pe) {
		t.Errorf("expected PermanentError, got %T: %v", err, err)
	}
}

func TestCallWithRetry_ReturnsNilOnSuccess(t *testing.T) {
	m := &Manager{
		maxRetries:    1,
		retryDelay:    1 * time.Millisecond,
		maxRetryDelay: 1 * time.Millisecond,
	}

	err := m.callWithRetry(context.Background(), func() error {
		return nil
	})

	if err != nil {
		t.Errorf("expected nil, got %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/cloudflareManager/ -run "TestCallWithRetry_" -v`
Expected: FAIL — `callWithRetry` returns a plain wrapped error, not a typed error.

- [ ] **Step 3: Modify `callWithRetry` to wrap the final error**

Replace the return statement at line 180 of `internal/cloudflareManager/tunnel.go`:

Old code (line 180):
```go
	return fmt.Errorf("operation failed after %d retries: %w", m.maxRetries, lastErr)
```

New code:
```go
	return m.classifyError(lastErr)
```

Add a new helper method after `isRetriableError`:

```go
// classifyError wraps an error as RetryableError or PermanentError based on
// whether it would normally be retried.
func (m *Manager) classifyError(err error) error {
	if m.isRetriableError(err) {
		return &types.RetryableError{Err: err}
	}
	return &types.PermanentError{Err: err}
}
```

Also add the import for `docktunnel/pkg/types` to the import block.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/cloudflareManager/ -run "TestCallWithRetry_" -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/cloudflareManager/tunnel.go internal/cloudflareManager/tunnel_test.go
git commit -m "feat(cf): propagate typed errors from callWithRetry"
```

---

### Task 4: Compensation Config

**Files:**
- Modify: `internal/config/config.go:14-74` (Config struct)
- Modify: `internal/config/config.go:161-228` (New function — add defaults)

- [ ] **Step 1: Add Compensation section to Config struct**

In `internal/config/config.go`, add a new nested struct after the `Defaults` struct (after line 73):

```go
		Compensation struct {
			InitialDelay  time.Duration `mapstructure:"initialDelay"`
			MaxDelay      time.Duration `mapstructure:"maxDelay"`
			MaxRetries    int           `mapstructure:"maxRetries"`
			PollInterval  time.Duration `mapstructure:"pollInterval"`
		} `mapstructure:"compensation"`
```

- [ ] **Step 2: Add viper defaults in New() function**

After the existing defaults (after line 187, `v.SetDefault("defaults.path", "")`), add:

```go
		// Compensation defaults
		v.SetDefault("compensation.initialDelay", 30*time.Second)
		v.SetDefault("compensation.maxDelay", 30*time.Minute)
		v.SetDefault("compensation.maxRetries", 10)
		v.SetDefault("compensation.pollInterval", 30*time.Second)
```

- [ ] **Step 3: Add a getter method**

After `GetControllerOptions` (after line 102), add:

```go
// GetCompensationConfig returns compensation configuration with defaults applied
func (c *Config) GetCompensationConfig() (initialDelay, maxDelay time.Duration, maxRetries int, pollInterval time.Duration) {
	initialDelay = c.Compensation.InitialDelay
	if initialDelay == 0 {
		initialDelay = 30 * time.Second
	}
	maxDelay = c.Compensation.MaxDelay
	if maxDelay == 0 {
		maxDelay = 30 * time.Minute
	}
	maxRetries = c.Compensation.MaxRetries
	if maxRetries == 0 {
		maxRetries = 10
	}
	pollInterval = c.Compensation.PollInterval
	if pollInterval == 0 {
		pollInterval = 30 * time.Second
	}
	return
}
```

- [ ] **Step 4: Add compensation to SanitizeForLog**

In `SanitizeForLog`, add after the `"defaults"` entry (after line 280):

```go
			"compensation": map[string]interface{}{
				"initialDelay": c.Compensation.InitialDelay,
				"maxDelay":     c.Compensation.MaxDelay,
				"maxRetries":   c.Compensation.MaxRetries,
				"pollInterval": c.Compensation.PollInterval,
			},
```

- [ ] **Step 5: Verify existing tests still pass**

Run: `go test ./internal/config/ -v`
Expected: PASS (existing tests should still pass since new fields have defaults)

- [ ] **Step 6: Commit**

```bash
git add internal/config/config.go
git commit -m "feat(config): add compensation config section with defaults"
```

---

### Task 5: State Manager — EnqueueAction

**Files:**
- Modify: `internal/state/manager.go:26-37` (Manager struct)
- Modify: `internal/state/manager.go:60-71` (NewManager)
- Create: `internal/state/compensation_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/state/compensation_test.go`:

```go
package state

import (
	"errors"
	"log/slog"
	"os"
	"testing"
	"time"

	"docktunnel/pkg/types"
)

func TestEnqueueAction_RetryableError(t *testing.T) {
	sm := NewManager(slog.New(slog.NewTextHandler(os.Stdout, nil)))
	action := types.Action{
		Kind:        types.ActionDeleteRoute,
		ContainerID: "c1",
		ServiceName: "web",
		Hostname:    "app.example.com",
	}

	sm.EnqueueAction(action, &types.RetryableError{Err: errors.New("503")})

	records := sm.GetAllPendingActions()
	if len(records) != 1 {
		t.Fatalf("expected 1 pending action, got %d", len(records))
	}
	for _, rec := range records {
		if rec.Dead {
			t.Error("record should not be dead")
		}
		if rec.RetryCount != 0 {
			t.Errorf("expected RetryCount 0, got %d", rec.RetryCount)
		}
		if rec.NextRetryAt.IsZero() {
			t.Error("expected NextRetryAt to be set")
		}
		if rec.Action.Hostname != "app.example.com" {
			t.Errorf("expected hostname app.example.com, got %s", rec.Action.Hostname)
		}
	}
}

func TestEnqueueAction_PermanentError(t *testing.T) {
	sm := NewManager(slog.New(slog.NewTextHandler(os.Stdout, nil)))
	action := types.Action{
		Kind:     types.ActionDeleteRoute,
		Hostname: "app.example.com",
	}

	sm.EnqueueAction(action, &types.PermanentError{Err: errors.New("403")})

	records := sm.GetAllPendingActions()
	if len(records) != 1 {
		t.Fatalf("expected 1 pending action, got %d", len(records))
	}
	for _, rec := range records {
		if !rec.Dead {
			t.Error("record should be dead")
		}
	}
}

func TestEnqueueAction_PlainError_TreatedAsRetryable(t *testing.T) {
	sm := NewManager(slog.New(slog.NewTextHandler(os.Stdout, nil)))
	action := types.Action{
		Kind:     types.ActionDeleteRoute,
		Hostname: "app.example.com",
	}

	sm.EnqueueAction(action, errors.New("unknown failure"))

	records := sm.GetAllPendingActions()
	if len(records) != 1 {
		t.Fatalf("expected 1 pending action, got %d", len(records))
	}
	for _, rec := range records {
		if rec.Dead {
			t.Error("plain error should be treated as retryable")
		}
	}
}

func TestGetDeadActions(t *testing.T) {
	sm := NewManager(slog.New(slog.NewTextHandler(os.Stdout, nil)))

	// Enqueue a permanent error (dead) and a retryable (alive)
	sm.EnqueueAction(types.Action{Kind: types.ActionDeleteRoute, Hostname: "dead.example.com"},
		&types.PermanentError{Err: errors.New("403")})
	sm.EnqueueAction(types.Action{Kind: types.ActionDeleteRoute, Hostname: "alive.example.com"},
		&types.RetryableError{Err: errors.New("503")})

	dead := sm.GetDeadActions()
	if len(dead) != 1 {
		t.Fatalf("expected 1 dead action, got %d", len(dead))
	}
	if dead[0].Action.Hostname != "dead.example.com" {
		t.Errorf("expected dead.example.com, got %s", dead[0].Action.Hostname)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/state/ -run "TestEnqueueAction|TestGetDeadActions" -v`
Expected: FAIL — `EnqueueAction`, `GetAllPendingActions`, `GetDeadActions` not defined.

- [ ] **Step 3: Update Manager struct**

In `internal/state/manager.go`, add fields to the Manager struct (after the `dirty` field at line 37):

```go
	pendingActions     map[string]*types.CompensationRecord
	compInitialDelay   time.Duration
	compMaxDelay       time.Duration
	compMaxRetries     int
	compPollInterval   time.Duration
```

- [ ] **Step 4: Update NewManager**

Update `NewManager` to initialize the new fields:

```go
func NewManager(logger *slog.Logger) *Manager {
	return &Manager{
		activeTunnels:    make(map[string]*types.TunnelEntry),
		pendingDeletes:   make(map[string]*types.TunnelEntry),
		flappingContainers: make(map[string]types.FlappingState),
		pendingActions:   make(map[string]*types.CompensationRecord),
		logger:           logger,
		statePath:        "",
		lastSaved:        time.Time{},
		dirty:            false,
		compInitialDelay: 30 * time.Second,
		compMaxDelay:     30 * time.Minute,
		compMaxRetries:   10,
		compPollInterval: 30 * time.Second,
	}
}
```

- [ ] **Step 5: Add EnqueueAction, GetAllPendingActions, GetDeadActions**

Add these methods to `internal/state/manager.go`:

```go
// SetCompensationConfig configures retry parameters for the compensation queue.
func (sm *Manager) SetCompensationConfig(initialDelay, maxDelay time.Duration, maxRetries int, pollInterval time.Duration) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.compInitialDelay = initialDelay
	sm.compMaxDelay = maxDelay
	sm.compMaxRetries = maxRetries
	sm.compPollInterval = pollInterval
}

// EnqueueAction records a failed action for retry. It classifies the error
// to decide whether the action is retryable or permanently dead.
func (sm *Manager) EnqueueAction(action types.Action, err error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	now := time.Now().UTC()
	id := action.ContainerID + ":" + action.ServiceName + ":" + now.Format("20060102150405")

	rec := &types.CompensationRecord{
		ID:          id,
		Action:      action,
		RetryCount:  0,
		MaxRetries:  sm.compMaxRetries,
		NextRetryAt: now.Add(sm.compInitialDelay),
		LastError:   err.Error(),
		CreatedAt:   now,
		Dead:        false,
	}

	var permanent *types.PermanentError
	if errors.As(err, &permanent) {
		rec.Dead = true
		sm.logger.Error("Action permanently failed, marking dead",
			"action", action.Kind,
			"hostname", action.Hostname,
			"error", err)
	}

	sm.pendingActions[id] = rec
	sm.markDirty()
}

// GetAllPendingActions returns a copy of all compensation records.
func (sm *Manager) GetAllPendingActions() []*types.CompensationRecord {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	result := make([]*types.CompensationRecord, 0, len(sm.pendingActions))
	for _, rec := range sm.pendingActions {
		result = append(result, rec)
	}
	return result
}

// GetDeadActions returns compensation records that are permanently failed.
func (sm *Manager) GetDeadActions() []types.CompensationRecord {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	var dead []types.CompensationRecord
	for _, rec := range sm.pendingActions {
		if rec.Dead {
			dead = append(dead, *rec)
		}
	}
	return dead
}
```

Add `"errors"` to the import block of `manager.go`.

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/state/ -run "TestEnqueueAction|TestGetDeadActions" -v`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add internal/state/manager.go internal/state/compensation_test.go
git commit -m "feat(state): add EnqueueAction, GetDeadActions for compensation queue"
```

---

### Task 6: State Manager — RunCompensation

**Files:**
- Modify: `internal/state/manager.go`

- [ ] **Step 1: Write the failing tests**

Add to `internal/state/compensation_test.go`:

```go
func TestRunCompensation_SuccessfulRetry(t *testing.T) {
	sm := NewManager(slog.New(slog.NewTextHandler(os.Stdout, nil)))
	sm.compInitialDelay = 1 * time.Millisecond

	action := types.Action{
		Kind:     types.ActionDeleteRoute,
		Hostname: "app.example.com",
	}
	sm.EnqueueAction(action, &types.RetryableError{Err: errors.New("503")})

	// Force NextRetryAt to past so it's immediately due
	for _, rec := range sm.pendingActions {
		rec.NextRetryAt = time.Now().Add(-1 * time.Second)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var executed []types.Action
	executor := func(a types.Action) error {
		executed = append(executed, a)
		return nil
	}

	go sm.RunCompensation(ctx, executor)

	// Wait for execution
	time.Sleep(500 * time.Millisecond)

	if len(executed) != 1 {
		t.Fatalf("expected 1 execution, got %d", len(executed))
	}
	if executed[0].Hostname != "app.example.com" {
		t.Errorf("expected hostname app.example.com, got %s", executed[0].Hostname)
	}

	// Record should be removed after successful execution
	records := sm.GetAllPendingActions()
	if len(records) != 0 {
		t.Errorf("expected 0 pending actions after success, got %d", len(records))
	}
}

func TestRunCompensation_RetryableFailureBacksOff(t *testing.T) {
	sm := NewManager(slog.New(slog.NewTextHandler(os.Stdout, nil)))
	sm.compInitialDelay = 1 * time.Millisecond
	sm.compMaxDelay = 10 * time.Millisecond
	sm.compMaxRetries = 3

	action := types.Action{
		Kind:     types.ActionDeleteRoute,
		Hostname: "app.example.com",
	}
	sm.EnqueueAction(action, &types.RetryableError{Err: errors.New("503")})

	// Force NextRetryAt to past
	for _, rec := range sm.pendingActions {
		rec.NextRetryAt = time.Now().Add(-1 * time.Second)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	callCount := 0
	executor := func(a types.Action) error {
		callCount++
		return &types.RetryableError{Err: errors.New("still failing")}
	}

	go sm.RunCompensation(ctx, executor)

	// Wait for at least one cycle
	time.Sleep(500 * time.Millisecond)

	records := sm.GetAllPendingActions()
	if len(records) != 1 {
		t.Fatalf("expected 1 pending action, got %d", len(records))
	}
	for _, rec := range records {
		if rec.Dead {
			t.Error("record should not be dead yet")
		}
		if rec.RetryCount == 0 {
			t.Error("expected RetryCount > 0 after failed retry")
		}
	}
}

func TestRunCompensation_ExhaustedRetriesMarksDead(t *testing.T) {
	sm := NewManager(slog.New(slog.NewTextHandler(os.Stdout, nil)))
	sm.compInitialDelay = 1 * time.Millisecond
	sm.compMaxDelay = 1 * time.Millisecond
	sm.compMaxRetries = 2

	action := types.Action{
		Kind:     types.ActionDeleteRoute,
		Hostname: "app.example.com",
	}
	sm.EnqueueAction(action, &types.RetryableError{Err: errors.New("503")})

	// Force NextRetryAt to past and set retry count near limit
	for _, rec := range sm.pendingActions {
		rec.NextRetryAt = time.Now().Add(-1 * time.Second)
		rec.RetryCount = 2 // at max already
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	executor := func(a types.Action) error {
		return &types.RetryableError{Err: errors.New("still failing")}
	}

	go sm.RunCompensation(ctx, executor)
	time.Sleep(500 * time.Millisecond)

	dead := sm.GetDeadActions()
	if len(dead) != 1 {
		t.Fatalf("expected 1 dead action, got %d", len(dead))
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/state/ -run "TestRunCompensation_" -v`
Expected: FAIL — `RunCompensation` not defined.

- [ ] **Step 3: Implement RunCompensation**

Add to `internal/state/manager.go`:

```go
// RunCompensation runs a background loop that retries failed actions.
// Blocks until ctx is cancelled.
func (sm *Manager) RunCompensation(ctx context.Context, executor func(types.Action) error) {
	for {
		sm.mu.RLock()
		interval := sm.compPollInterval
		sm.mu.RUnlock()

		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}

		sm.processCompensationQueue(executor)
	}
}

// processCompensationQueue processes due compensation records.
func (sm *Manager) processCompensationQueue(executor func(types.Action) error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	now := time.Now()
	for id, rec := range sm.pendingActions {
		if rec.Dead {
			continue
		}
		if now.Before(rec.NextRetryAt) {
			continue
		}

		// Execute outside the lock — release, execute, re-acquire
		action := rec.Action
		sm.mu.Unlock()

		err := executor(action)

		sm.mu.Lock()

		if err == nil {
			delete(sm.pendingActions, id)
			sm.markDirty()
			sm.logger.Info("Compensation action succeeded",
				"action", action.Kind,
				"hostname", action.Hostname)
			continue
		}

		rec.LastError = err.Error()

		var permanent *types.PermanentError
		if errors.As(err, &permanent) {
			rec.Dead = true
			sm.markDirty()
			sm.logger.Error("Compensation action permanently failed",
				"action", action.Kind,
				"hostname", action.Hostname,
				"error", err)
			continue
		}

		rec.RetryCount++
		if rec.RetryCount >= rec.MaxRetries {
			rec.Dead = true
			sm.markDirty()
			sm.logger.Error("Compensation action exhausted retries",
				"action", action.Kind,
				"hostname", action.Hostname,
				"retries", rec.RetryCount)
			continue
		}

		// Exponential backoff
		backoff := sm.compInitialDelay * time.Duration(1<<uint(rec.RetryCount))
		if backoff > sm.compMaxDelay {
			backoff = sm.compMaxDelay
		}
		rec.NextRetryAt = now.Add(backoff)
		sm.markDirty()
		sm.logger.Warn("Compensation action failed, will retry",
			"action", action.Kind,
			"hostname", action.Hostname,
			"retry_count", rec.RetryCount,
			"next_retry_at", rec.NextRetryAt,
			"error", err)
	}
}
```

Add `"context"` to the import block if not already present.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/state/ -run "TestRunCompensation_" -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/state/manager.go internal/state/compensation_test.go
git commit -m "feat(state): add RunCompensation background retry loop"
```

---

### Task 7: State Persistence — Snapshot and Migration

**Files:**
- Modify: `internal/state/manager.go:420-506` (GetSnapshot, LoadFromSnapshot)
- Modify: `internal/state/persistence.go:21` (StateVersion)
- Modify: `internal/state/persistence.go:242-260` (RegisterGobTypes)

- [ ] **Step 1: Write the failing test**

Add to `internal/state/compensation_test.go`:

```go
func TestSnapshotIncludesPendingActions(t *testing.T) {
	sm := NewManager(slog.New(slog.NewTextHandler(os.Stdout, nil)))
	sm.EnqueueAction(types.Action{
		Kind:     types.ActionDeleteRoute,
		Hostname: "app.example.com",
	}, &types.RetryableError{Err: errors.New("503")})

	snap := sm.GetSnapshot()
	if len(snap.PendingActions) != 1 {
		t.Fatalf("expected 1 pending action in snapshot, got %d", len(snap.PendingActions))
	}
}

func TestLoadFromSnapshot_RestoresPendingActions(t *testing.T) {
	sm := NewManager(slog.New(slog.NewTextHandler(os.Stdout, nil)))
	sm.EnqueueAction(types.Action{
		Kind:        types.ActionDeleteRoute,
		ContainerID: "c1",
		ServiceName: "web",
		Hostname:    "app.example.com",
	}, &types.RetryableError{Err: errors.New("503")})

	snap := sm.GetSnapshot()

	sm2 := NewManager(slog.New(slog.NewTextHandler(os.Stdout, nil)))
	sm2.LoadFromSnapshot(snap)

	records := sm2.GetAllPendingActions()
	if len(records) != 1 {
		t.Fatalf("expected 1 pending action after load, got %d", len(records))
	}
}

func TestLoadFromSnapshot_MigratesV2ToV3(t *testing.T) {
	sm := NewManager(slog.New(slog.NewTextHandler(os.Stdout, nil)))

	// Simulate a v2 snapshot (no PendingActions)
	snap := &types.StateSnapshot{
		Version:            2,
		Timestamp:          time.Now().UTC(),
		ActiveTunnels:      map[string]*types.TunnelEntry{},
		PendingDeletions:   map[string]*types.TunnelEntry{},
		FlappingContainers: map[string]types.FlappingState{},
		PendingActions:     nil, // v2 won't have this
	}

	sm.LoadFromSnapshot(snap)

	records := sm.GetAllPendingActions()
	if len(records) != 0 {
		t.Fatalf("expected 0 pending actions for v2 migration, got %d", len(records))
	}
	if sm.pendingActions == nil {
		t.Error("pendingActions should be initialized, not nil")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/state/ -run "TestSnapshotIncludesPendingActions|TestLoadFromSnapshot_RestoresPendingActions|TestLoadFromSnapshot_MigratesV2ToV3" -v`
Expected: FAIL — `PendingActions` not populated in snapshot.

- [ ] **Step 3: Update GetSnapshot**

In `internal/state/manager.go`, update `GetSnapshot` to include `PendingActions`. Add after the `flappingContainers` copy block (around line 438):

```go
	pendingActions := make(map[string]*types.CompensationRecord, len(sm.pendingActions))
	for k, v := range sm.pendingActions {
		pendingActions[k] = v
	}
```

And add to the returned snapshot struct:

```go
		PendingActions:     pendingActions,
```

- [ ] **Step 4: Update LoadFromSnapshot**

In `internal/state/manager.go`, update `LoadFromSnapshot`. After the `sm.flappingContainers = snapshot.FlappingContainers` line (around line 497), add:

```go
	// Restore pending actions (migrate v2 → v3)
	if snapshot.PendingActions != nil {
		sm.pendingActions = snapshot.PendingActions
	} else {
		sm.pendingActions = make(map[string]*types.CompensationRecord)
	}
```

- [ ] **Step 5: Bump StateVersion**

In `internal/state/persistence.go`, change line 21:

```go
	StateVersion = 3
```

- [ ] **Step 6: Register new gob types**

In `internal/state/persistence.go`, add to `RegisterGobTypes`:

```go
	gob.Register(types.CompensationRecord{})
	gob.Register(map[string]*types.CompensationRecord{})
```

- [ ] **Step 7: Run tests to verify they pass**

Run: `go test ./internal/state/ -v`
Expected: PASS (all tests including existing ones)

- [ ] **Step 8: Run full test suite**

Run: `go test ./...`
Expected: PASS

- [ ] **Step 9: Commit**

```bash
git add internal/state/manager.go internal/state/persistence.go internal/state/compensation_test.go
git commit -m "feat(state): persist compensation records in state file, bump version to 3"
```

---

### Task 8: Controller — ExecuteAction and Integration

**Files:**
- Modify: `internal/controller/controller.go:313-404` (handleContainerStop)
- Modify: `internal/controller/controller.go:56-80` (Controller struct, NewController)

- [ ] **Step 1: Add ExecuteAction method**

Add a new method to the Controller in `internal/controller/controller.go`:

```go
// ExecuteAction executes a single action (e.g., delete route and sync).
// Used by the compensation queue to retry failed actions.
func (c *Controller) ExecuteAction(ctx context.Context, action types.Action) error {
	if action.Kind == types.ActionDeleteRoute && action.Hostname != "" {
		c.mu.Lock()
		delete(c.ingressRules, action.Hostname)
		c.mu.Unlock()
	}
	return c.syncToCloudflare(ctx)
}
```

- [ ] **Step 2: Wire compensation into handleContainerStop**

In `handleContainerStop`, replace the action execution block (lines 392-403):

Old:
```go
	// Execute actions: clear containerRules, handle ingress based on actions
	c.mu.Lock()
	delete(c.containerRules, event.ContainerID)
	for _, action := range allActions {
		if action.Kind == types.ActionDeleteRoute && action.Hostname != "" {
			delete(c.ingressRules, action.Hostname)
		}
	}
	c.updateContainerHealth(event.ContainerID, false)
	c.mu.Unlock()

	return c.syncToCloudflare(ctx)
```

New:
```go
	// Execute actions: clear containerRules, handle ingress based on actions
	c.mu.Lock()
	delete(c.containerRules, event.ContainerID)
	for _, action := range allActions {
		if action.Kind == types.ActionDeleteRoute && action.Hostname != "" {
			delete(c.ingressRules, action.Hostname)
		}
	}
	c.updateContainerHealth(event.ContainerID, false)
	c.mu.Unlock()

	if err := c.syncToCloudflare(ctx); err != nil {
		// Sync failed — enqueue actions for retry
		for _, action := range allActions {
			if action.Kind == types.ActionDeleteRoute && action.Hostname != "" {
				c.stateManager.EnqueueAction(action, err)
			}
		}
		return err
	}
	return nil
```

- [ ] **Step 3: Verify existing tests still pass**

Run: `go test ./internal/controller/ -v`
Expected: PASS (existing tests may need syncToCloudflare mock to return nil, which it already does in test setups)

- [ ] **Step 4: Commit**

```bash
git add internal/controller/controller.go
git commit -m "feat(controller): wire compensation into handleContainerStop, add ExecuteAction"
```

---

### Task 9: Main — Start Compensation Goroutine

**Files:**
- Modify: `cmd/docktunnel/main.go:80-81` (controller creation)
- Modify: `cmd/docktunnel/main.go:140-182` (background goroutine block)

- [ ] **Step 1: Configure compensation on state manager**

After the controller creation and state path setup (after line 91 `controller.SetStatePath(statePath)`), add:

```go
	// Configure compensation queue parameters
	initialDelay, maxDelay, maxRetries, pollInterval := cfg.GetCompensationConfig()
	controller.SetCompensationConfig(initialDelay, maxDelay, maxRetries, pollInterval)
```

- [ ] **Step 2: Add SetCompensationConfig to Controller**

In `internal/controller/controller.go`, add a passthrough method:

```go
// SetCompensationConfig configures the compensation queue parameters.
func (c *Controller) SetCompensationConfig(initialDelay, maxDelay time.Duration, maxRetries int, pollInterval time.Duration) {
	c.stateManager.SetCompensationConfig(initialDelay, maxDelay, maxRetries, pollInterval)
}
```

- [ ] **Step 3: Start compensation goroutine in main.go**

In the background goroutine block (around line 140, in the `wg.Add(1)` goroutine), add the compensation loop alongside the GC and reconcile tickers. Inside the `for { select {} }` loop, add the compensation trigger. Alternatively, start a separate goroutine right after the existing GC goroutine (after line 182):

```go
		// Start compensation queue background loop
		wg.Add(1)
		go func() {
			defer wg.Done()
			appLogger.Info("Starting compensation queue background loop")
			controller.RunCompensationLoop(ctx)
		}()
```

- [ ] **Step 4: Add RunCompensationLoop to Controller**

In `internal/controller/controller.go`, add:

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

- [ ] **Step 5: Verify build and tests**

Run: `go build ./... && go test ./...`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add cmd/docktunnel/main.go internal/controller/controller.go
git commit -m "feat(main): start compensation queue goroutine with config"
```

---

### Task 10: Full Integration Verification

**Files:** None (verification only)

- [ ] **Step 1: Run full test suite**

Run: `go test ./... -v`
Expected: All tests PASS

- [ ] **Step 2: Run go vet**

Run: `go vet ./...`
Expected: No issues

- [ ] **Step 3: Run gofmt check**

Run: `gofmt -s -l .`
Expected: No output (all files formatted)

- [ ] **Step 4: Verify state file backward compatibility**

Run: `go test ./internal/state/ -run "TestLoadFromSnapshot_MigratesV2ToV3|TestSaveAndLoadGob" -v`
Expected: PASS

- [ ] **Step 5: Final commit (if any formatting fixes needed)**

```bash
gofmt -s -w .
git add -A
git commit -m "style: gofmt all files"
```
