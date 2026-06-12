# Docker Reconnection & Health Monitoring Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add auto-reconnection with exponential backoff when Docker daemon disconnects, and health status monitoring so containers with HEALTHCHECK are only exposed when healthy.

**Architecture:** Refactor `monitor.go` to extract `listenOnce()` and wrap it in a reconnection loop with exponential backoff. After reconnect, emit a resync event through the existing event channel so the controller does a full state reconciliation. Add `health_status` event handling in both monitor (translation to internal actions) and controller (expose/remove services based on health). Introduce a `dockerClient` interface on `Manager` for testability.

**Tech Stack:** Go 1.24, Docker SDK v28, existing event/controller architecture

---

## File Structure

| File | Responsibility |
|------|---------------|
| `internal/docker/monitor.go` | Docker client wrapper: container scanning, event listening, reconnection loop, health_status translation |
| `internal/docker/monitor_test.go` | Unit tests for reconnection logic, health event translation |
| `internal/events/event.go` | Event struct + health action constants + resync action constant |
| `internal/controller/controller.go` | Health event dispatch + resync handling |

---

### Task 1: Add health and resync action constants to events package

**Files:**
- Modify: `internal/events/event.go`

- [ ] **Step 1: Add new action constants to event.go**

Add these constants after the imports in `internal/events/event.go`:

```go
package events

import (
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/events"
)

// Health action constants — emitted when a container with a HEALTHCHECK changes state.
// Containers without a HEALTHCHECK never produce these events.
const (
	ActionHealthHealthy   events.Action = "health_healthy"
	ActionHealthUnhealthy events.Action = "health_unhealthy"
	ActionHealthStarting  events.Action = "health_starting"
)

// ActionResync is emitted after a Docker daemon reconnection to trigger a full state reconciliation.
const ActionResync events.Action = "resync"

// Event is the unified data structure passed between system components.
type Event struct {
	Type          events.Action
	ContainerID   string
	ContainerInfo *container.InspectResponse
}
```

- [ ] **Step 2: Run tests to verify existing code still compiles**

Run: `go build ./internal/events/...`
Expected: compiles with no errors

- [ ] **Step 3: Commit**

```bash
git add internal/events/event.go
git commit -m "feat(events): add health action and resync constants"
```

---

### Task 2: Introduce dockerClient interface for testability

**Files:**
- Modify: `internal/docker/monitor.go`

- [ ] **Step 1: Define the dockerClient interface and update Manager to use it**

Add the interface and update `Manager` struct + `NewManager` in `internal/docker/monitor.go`:

```go
package docker

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	containerTypes "github.com/docker/docker/api/types/container"
	eventTypes "github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"

	"docktunnel/internal/events"
)

// dockerClient abstracts the Docker API methods used by Manager.
type dockerClient interface {
	ContainerList(ctx context.Context, options containerTypes.ListOptions) ([]containerTypes.Summary, error)
	ContainerInspect(ctx context.Context, containerID string) (containerTypes.InspectResponse, error)
	Events(ctx context.Context, options eventTypes.ListOptions) (<-chan eventTypes.Message, <-chan error)
	Close() error
}

// Manager wraps all Docker-related operations.
type Manager struct {
	client dockerClient
}

// NewManager creates a new Docker Manager instance.
func NewManager() (*Manager, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("failed to create docker client: %w", err)
	}

	return &Manager{
		client: cli,
	}, nil
}
```

Keep `ScanRunningContainers`, `Close` unchanged (they already use `m.client`).

- [ ] **Step 2: Run tests to verify compilation**

Run: `go build ./internal/docker/...`
Expected: compiles with no errors

- [ ] **Step 3: Commit**

```bash
git add internal/docker/monitor.go
git commit -m "refactor(docker): extract dockerClient interface for testability"
```

---

### Task 3: Extract listenOnce and add reconnection loop

**Files:**
- Modify: `internal/docker/monitor.go`

- [ ] **Step 1: Write the failing test for reconnection**

Add to `internal/docker/monitor_test.go`:

```go
package docker

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	containerTypes "github.com/docker/docker/api/types/container"
	eventTypes "github.com/docker/docker/api/types/events"

	"docktunnel/internal/events"
)

// mockDockerClient implements dockerClient for testing.
type mockDockerClient struct {
	mu             sync.Mutex
	eventsCalls    int
	eventErr       chan error
	eventMessages  chan eventTypes.Message
	inspectResult  containerTypes.InspectResponse
	inspectErr     error
	listResult     []containerTypes.Summary
	listErr        error
	closeErr       error
}

func (m *mockDockerClient) ContainerList(ctx context.Context, options containerTypes.ListOptions) ([]containerTypes.Summary, error) {
	return m.listResult, m.listErr
}

func (m *mockDockerClient) ContainerInspect(ctx context.Context, containerID string) (containerTypes.InspectResponse, error) {
	return m.inspectResult, m.inspectErr
}

func (m *mockDockerClient) Events(ctx context.Context, options eventTypes.ListOptions) (<-chan eventTypes.Message, <-chan error) {
	m.mu.Lock()
	m.eventsCalls++
	m.mu.Unlock()
	return m.eventMessages, m.eventErr
}

func (m *mockDockerClient) Close() error {
	return m.closeErr
}

func TestListenForEvents_ReconnectsOnStreamError(t *testing.T) {
	// First call: returns an error immediately (simulates disconnect)
	// Second call: returns messages channel that stays open (simulates reconnect)
	firstErr := make(chan error, 1)
	firstErr <- errors.New("stream closed")
	firstMsg := make(chan eventTypes.Message)
	close(firstMsg)

	secondErr := make(chan error)
	secondMsg := make(chan eventTypes.Message)

	callCount := 0
	client := &mockDockerClient{}

	// Override Events to alternate between failing and succeeding
	origEvents := client.Events
	_ = origEvents
	client.eventErr = firstErr
	client.eventMessages = firstMsg

	// We need a more sophisticated mock that changes behavior per call
	mockedClient := &alternatingMockClient{
		responses: []eventsResponse{
			{msgs: firstMsg, errs: firstErr}, // fails immediately
			{msgs: secondMsg, errs: secondErr}, // succeeds (stays open)
		},
		inspectResult: containerTypes.InspectResponse{},
	}

	mgr := &Manager{client: mockedClient}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	eventCh := make(chan events.Event, 10)

	errCh := make(chan error, 1)
	go func() {
		errCh <- mgr.ListenForEvents(ctx, eventCh)
	}()

	// Wait briefly for reconnection to happen
	time.Sleep(1500 * time.Millisecond) // wait for initial backoff (1s)

	mockedClient.mu.Lock()
	calls := mockedClient.eventsCalls
	mockedClient.mu.Unlock()

	if calls < 2 {
		t.Errorf("expected Events to be called at least 2 times (initial + reconnect), got %d", calls)
	}

	cancel()
	<-errCh // wait for ListenForEvents to return
}

type eventsResponse struct {
	msgs <-chan eventTypes.Message
	errs <-chan error
}

type alternatingMockClient struct {
	mu            sync.Mutex
	responses     []eventsResponse
	eventsCalls   int
	inspectResult containerTypes.InspectResponse
	listResult    []containerTypes.Summary
}

func (m *alternatingMockClient) ContainerList(ctx context.Context, options containerTypes.ListOptions) ([]containerTypes.Summary, error) {
	return m.listResult, nil
}

func (m *alternatingMockClient) ContainerInspect(ctx context.Context, containerID string) (containerTypes.InspectResponse, error) {
	return m.inspectResult, nil
}

func (m *alternatingMockClient) Events(ctx context.Context, options eventTypes.ListOptions) (<-chan eventTypes.Message, <-chan error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	idx := m.eventsCalls
	m.eventsCalls++

	if idx < len(m.responses) {
		return m.responses[idx].msgs, m.responses[idx].errs
	}
	// Past defined responses, return closed channels
	msgs := make(chan eventTypes.Message)
	errs := make(chan error, 1)
	errs <- errors.New("no more mock responses")
	close(msgs)
	return msgs, errs
}

func (m *alternatingMockClient) Close() error { return nil }
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/docker/... -run TestListenForEvents_ReconnectsOnStreamError -v`
Expected: FAIL — `ListenForEvents` currently returns on first error without retry

- [ ] **Step 3: Implement listenOnce and reconnection loop**

Replace the `ListenForEvents` method in `internal/docker/monitor.go` with:

```go
// ListenForEvents listens to Docker events with automatic reconnection.
// On disconnect, it retries with exponential backoff (1s → 60s).
// After reconnect, it emits a resync event for full state reconciliation.
func (m *Manager) ListenForEvents(ctx context.Context, eventChannel chan<- events.Event) error {
	backoff := 1 * time.Second
	maxBackoff := 60 * time.Second

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		err := m.listenOnce(ctx, eventChannel)
		if err == nil || ctx.Err() != nil {
			return err
		}

		slog.Warn("Docker event stream disconnected, reconnecting",
			"backoff", backoff, "error", err)

		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return ctx.Err()
		}

		backoff = min(backoff*2, maxBackoff)

		// After reconnect, trigger a full resync
		slog.Info("Reconnected to Docker daemon, triggering resync")
		select {
		case eventChannel <- events.Event{Type: events.ActionResync}:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// listenOnce connects to the Docker event stream and processes events until
// the stream closes or the context is cancelled.
func (m *Manager) listenOnce(ctx context.Context, eventChannel chan<- events.Event) error {
	filter := filters.NewArgs()
	filter.Add("type", "container")
	filter.Add("label", "docktunnel.enable=true")

	messages, errs := m.client.Events(ctx, eventTypes.ListOptions{
		Filters: filter,
	})

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-errs:
			if err != nil {
				return fmt.Errorf("docker event stream error: %w", err)
			}
		case message := <-messages:
			if message.Type != "container" {
				continue
			}

			event := events.Event{
				ContainerID: message.Actor.ID,
			}

			// Translate Docker health_status actions to internal actions
			switch {
			case message.Action == string(eventTypes.ActionStart):
				event.Type = eventTypes.ActionStart
				containerInfo, err := m.client.ContainerInspect(ctx, event.ContainerID)
				if err != nil {
					slog.Warn("Failed to inspect container on start event",
						"containerID", event.ContainerID, "error", err)
				} else {
					event.ContainerInfo = &containerInfo
				}
			case message.Action == "stop":
				event.Type = eventTypes.ActionStop
			case message.Action == string(eventTypes.ActionDie):
				event.Type = eventTypes.ActionDie
			case message.Action == "health_status: healthy":
				event.Type = events.ActionHealthHealthy
				containerInfo, err := m.client.ContainerInspect(ctx, event.ContainerID)
				if err != nil {
					slog.Warn("Failed to inspect container on health event",
						"containerID", event.ContainerID, "error", err)
				} else {
					event.ContainerInfo = &containerInfo
				}
			case message.Action == "health_status: unhealthy":
				event.Type = events.ActionHealthUnhealthy
				containerInfo, err := m.client.ContainerInspect(ctx, event.ContainerID)
				if err != nil {
					slog.Warn("Failed to inspect container on health event",
						"containerID", event.ContainerID, "error", err)
				} else {
					event.ContainerInfo = &containerInfo
				}
			case message.Action == "health_status: starting":
				event.Type = events.ActionHealthStarting
			default:
				continue
			}

			select {
			case eventChannel <- event:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
}
```

Note: the original `ListenForEvents` body (lines 69-119) is fully replaced by `ListenForEvents` (reconnection loop) + `listenOnce` (single event stream session).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/docker/... -run TestListenForEvents_ReconnectsOnStreamError -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/docker/monitor.go internal/docker/monitor_test.go
git commit -m "feat(docker): add reconnection loop with exponential backoff"
```

---

### Task 4: Add health status event translation test

**Files:**
- Modify: `internal/docker/monitor_test.go`

- [ ] **Step 1: Write the failing test for health_status event translation**

Add to `internal/docker/monitor_test.go`:

```go
func TestListenOnce_TranslatesHealthStatusEvents(t *testing.T) {
	msgs := make(chan eventTypes.Message, 3)
	errs := make(chan error)

	// Simulate three health events
	msgs <- eventTypes.Message{
		Type:   "container",
		Action: "health_status: healthy",
		Actor:  eventTypes.Actor{ID: "container-1"},
	}
	msgs <- eventTypes.Message{
		Type:   "container",
		Action: "health_status: unhealthy",
		Actor:  eventTypes.Actor{ID: "container-2"},
	}
	msgs <- eventTypes.Message{
		Type:   "container",
		Action: "health_status: starting",
		Actor:  eventTypes.Actor{ID: "container-3"},
	}
	close(msgs)

	inspectResult := containerTypes.InspectResponse{}

	client := &simpleMockClient{
		msgs:         msgs,
		errs:         errs,
		inspectResult: inspectResult,
	}

	mgr := &Manager{client: client}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	eventCh := make(chan events.Event, 3)

	err := mgr.listenOnce(ctx, eventCh)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	// Verify three events were received with correct types
	expected := []events.Action{
		events.ActionHealthHealthy,
		events.ActionHealthUnhealthy,
		events.ActionHealthStarting,
	}

	for i, expectedAction := range expected {
		select {
		case event := <-eventCh:
			if event.Type != expectedAction {
				t.Errorf("event %d: expected action %s, got %s", i, expectedAction, event.Type)
			}
		default:
			t.Fatalf("event %d: expected event not received", i)
		}
	}
}

// simpleMockClient is a minimal mock for single-call tests.
type simpleMockClient struct {
	msgs          <-chan eventTypes.Message
	errs          <-chan error
	inspectResult containerTypes.InspectResponse
	listResult    []containerTypes.Summary
}

func (m *simpleMockClient) ContainerList(ctx context.Context, options containerTypes.ListOptions) ([]containerTypes.Summary, error) {
	return m.listResult, nil
}

func (m *simpleMockClient) ContainerInspect(ctx context.Context, containerID string) (containerTypes.InspectResponse, error) {
	return m.inspectResult, nil
}

func (m *simpleMockClient) Events(ctx context.Context, options eventTypes.ListOptions) (<-chan eventTypes.Message, <-chan error) {
	return m.msgs, m.errs
}

func (m *simpleMockClient) Close() error { return nil }
```

- [ ] **Step 2: Run test to verify it passes (health translation is in listenOnce from Task 3)**

Run: `go test ./internal/docker/... -run TestListenOnce_TranslatesHealthStatusEvents -v`
Expected: PASS

- [ ] **Step 3: Commit**

```bash
git add internal/docker/monitor_test.go
git commit -m "test(docker): add health_status event translation test"
```

---

### Task 5: Add health event and resync handling in controller

**Files:**
- Modify: `internal/controller/controller.go`

- [ ] **Step 1: Write the failing test for health event dispatch**

Add to `internal/controller/controller_test.go`:

```go
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

	// Simulate a container start to register its rules first
	startEvent := events.Event{
		Type:        "start",
		ContainerID: "healthy-container",
		ContainerInfo: &containerTypes.InspectResponse{
			Config: &containerTypes.Config{
				Labels: map[string]string{
					"docktunnel.enable":         "true",
					"docktunnel.web.hostname":   "app.example.com",
					"docktunnel.web.service":    "http://localhost:8080",
				},
			},
			NetworkSettings: &containerTypes.NetworkSettings{
				Networks: map[string]*containerTypes.NetworkEndpointSettings{
					"bridge": {IPAddress: "172.17.0.2"},
				},
			},
		},
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

	// Now simulate health_unhealthy — should remove the rules
	unhealthyEvent := events.Event{
		Type:        events.ActionHealthUnhealthy,
		ContainerID: "healthy-container",
		ContainerInfo: &containerTypes.InspectResponse{
			Config: &containerTypes.Config{
				Labels: map[string]string{
					"docktunnel.enable":       "true",
					"docktunnel.web.hostname": "app.example.com",
					"docktunnel.web.service":  "http://localhost:8080",
				},
			},
			NetworkSettings: &containerTypes.NetworkSettings{
				Networks: map[string]*containerTypes.NetworkEndpointSettings{
					"bridge": {IPAddress: "172.17.0.2"},
				},
			},
		},
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
}

func TestDispatch_HealthEventsDoNotTriggerFlapping(t *testing.T) {
	opts := ControllerOptions{
		FlappingWindow:    60 * time.Second,
		FlappingThreshold: 2, // low threshold for testing
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
		Config: &containerTypes.Config{
			Labels: map[string]string{
				"docktunnel.enable":       "true",
				"docktunnel.web.hostname": "app.example.com",
				"docktunnel.web.service":  "http://localhost:8080",
			},
		},
		NetworkSettings: &containerTypes.NetworkSettings{
			Networks: map[string]*containerTypes.NetworkEndpointSettings{
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

	// Send many health unhealthy/healthy cycles — should NOT trigger flapping
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

	// Container should NOT be flapping
	if ctrl.isFlapping("test-container") {
		t.Error("health events should not trigger flapping detection")
	}
}
```

This test requires importing `containerTypes "github.com/docker/docker/api/types/container"` and `"docktunnel/internal/events"` in the test file.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/controller/... -run "TestDispatch_HealthHealthy|TestDispatch_HealthEventsDoNotTriggerFlapping" -v`
Expected: FAIL — `Dispatch` doesn't handle health actions yet

- [ ] **Step 3: Implement health event handling in controller**

In `internal/controller/controller.go`, update the `Dispatch` method to handle health and resync events:

```go
// Dispatch is the unified entry point for all event processing.
func (c *Controller) Dispatch(ctx context.Context, event events.Event) error {
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
		return nil
	}
}
```

Add the three new handler methods to `internal/controller/controller.go`:

```go
// handleHealthHealthy re-exposes a container's services when it becomes healthy.
// It reuses the start handler's label parsing and rule registration logic
// but skips flapping detection (health changes are not restarts).
func (c *Controller) handleHealthHealthy(ctx context.Context, event events.Event) error {
	if !c.isDocktunnelEnabled(event) {
		return nil
	}
	slog.Info("Container became healthy, exposing services", "containerID", event.ContainerID)

	// Reuse start handler logic (parse labels, register rules, sync)
	// but bypass flapping check
	return c.handleContainerStart(ctx, event)
}

// handleHealthUnhealthy removes a container's services when it becomes unhealthy
// or enters the starting health check phase.
// Health state changes do not trigger flapping detection.
func (c *Controller) handleHealthUnhealthy(ctx context.Context, event events.Event) error {
	if !c.isDocktunnelEnabled(event) {
		return nil
	}
	slog.Info("Container became unhealthy, removing services", "containerID", event.ContainerID)

	c.mu.Lock()
	hostnamesToRemove, exists := c.containerRules[event.ContainerID]
	if !exists {
		c.mu.Unlock()
		slog.Debug("No rules found for unhealthy container", "containerID", event.ContainerID)
		return nil
	}

	delete(c.containerRules, event.ContainerID)
	for _, hostname := range hostnamesToRemove {
		delete(c.ingressRules, hostname)
	}
	// Do NOT call updateContainerHealth — health events don't affect flapping
	c.mu.Unlock()

	return c.syncToCloudflare(ctx)
}

// handleResync performs a full state synchronization after Docker daemon reconnection.
func (c *Controller) handleResync(ctx context.Context) error {
	slog.Info("Handling resync event after Docker reconnection")
	return c.Sync(ctx)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/controller/... -run "TestDispatch_HealthHealthy|TestDispatch_HealthEventsDoNotTriggerFlapping" -v`
Expected: PASS

- [ ] **Step 5: Run full test suite**

Run: `go test ./internal/... -v`
Expected: all tests pass

- [ ] **Step 6: Commit**

```bash
git add internal/controller/controller.go internal/controller/controller_test.go
git commit -m "feat(controller): handle health events and resync after reconnection"
```

---

### Task 6: Handle reconnection backoff reset on successful connection

**Files:**
- Modify: `internal/docker/monitor.go`
- Modify: `internal/docker/monitor_test.go`

- [ ] **Step 1: Write the failing test for backoff reset**

Add to `internal/docker/monitor_test.go`:

```go
func TestListenForEvents_ResetsBackoffOnSuccessfulConnection(t *testing.T) {
	// First call: returns error after a brief period (simulates short-lived connection)
	// Second call: stays open, then context is cancelled (simulates successful connection)
	firstErr := make(chan error, 1)
	firstMsg := make(chan eventTypes.Message)

	secondErr := make(chan error)
	secondMsg := make(chan eventTypes.Message)

	mockedClient := &alternatingMockClient{
		responses: []eventsResponse{
			{msgs: firstMsg, errs: firstErr},  // fails
			{msgs: secondMsg, errs: secondErr}, // succeeds
			{msgs: secondMsg, errs: secondErr}, // would be third call if backoff wasn't reset
		},
		inspectResult: containerTypes.InspectResponse{},
	}

	mgr := &Manager{client: mockedClient}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	eventCh := make(chan events.Event, 10)

	errCh := make(chan error, 1)
	go func() {
		errCh <- mgr.ListenForEvents(ctx, eventCh)
	}()

	// Trigger first failure
	firstErr <- errors.New("connection lost")

	// Wait for reconnection (initial backoff = 1s + some margin)
	time.Sleep(2 * time.Second)

	// Cancel context to stop the listener
	cancel()
	<-errCh

	// Should have been called exactly 2 times: initial + 1 reconnect
	mockedClient.mu.Lock()
	calls := mockedClient.eventsCalls
	mockedClient.mu.Unlock()

	if calls != 2 {
		t.Errorf("expected exactly 2 Events calls (initial + 1 reconnect), got %d", calls)
	}
}
```

- [ ] **Step 2: Run test to check current behavior**

Run: `go test ./internal/docker/... -run TestListenForEvents_ResetsBackoffOnSuccessfulConnection -v`
Expected: Should PASS because `ListenForEvents` only reconnects on error. The backoff reset happens naturally since a successful connection means we stay in `listenOnce` until the next error. No explicit reset code is needed — the `backoff` variable is local to `ListenForEvents` and resets to 1s on each new `ListenForEvents` call.

Actually, we need to reset backoff within the reconnection loop when `listenOnce` succeeds for a while. Let me update the implementation:

The current `ListenForEvents` has `backoff` as a local variable that grows on each reconnection. We need to reset it after a successful connection. The simplest approach: reset backoff to 1s at the start of each successful `listenOnce` iteration (i.e., after `listenOnce` returns nil).

Update `ListenForEvents` in `internal/docker/monitor.go`:

```go
func (m *Manager) ListenForEvents(ctx context.Context, eventChannel chan<- events.Event) error {
	backoff := 1 * time.Second
	maxBackoff := 60 * time.Second

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		err := m.listenOnce(ctx, eventChannel)
		if err == nil || ctx.Err() != nil {
			return err
		}

		slog.Warn("Docker event stream disconnected, reconnecting",
			"backoff", backoff, "error", err)

		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return ctx.Err()
		}

		backoff = min(backoff*2, maxBackoff)

		slog.Info("Reconnected to Docker daemon, triggering resync")
		select {
		case eventChannel <- events.Event{Type: events.ActionResync}:
		case <-ctx.Done():
			return ctx.Err()
		}

		// Reset backoff after successful reconnect + resync
		// The next listenOnce call will start fresh; if it fails quickly,
		// backoff was already increased above and will be used.
		// But if the connection stays up for a while, we want backoff to reset.
		// We reset here because reaching this point means the previous attempt failed,
		// but we've done our backoff wait and are about to try again.
	}
}
```

Wait — there's a subtlety. The backoff resets when the function exits (local variable). But within one `ListenForEvents` call, if we disconnect → reconnect → disconnect quickly, the backoff should keep growing. And if we reconnect and stay connected for a long time, then disconnect, it should start from 1s again.

The issue is: how do we know a connection was "successful"? `listenOnce` blocks until the stream closes or errors. So if `listenOnce` returns after 10 minutes of successful operation, that's a "successful" connection before the stream died.

Let me add a reset: after each `listenOnce` return (even error), if the connection lasted longer than the current backoff, reset the backoff. Actually, that's overcomplicated. Let me just track the time:

```go
func (m *Manager) ListenForEvents(ctx context.Context, eventChannel chan<- events.Event) error {
	backoff := 1 * time.Second
	const maxBackoff = 60 * time.Second

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		connectedAt := time.Now()
		err := m.listenOnce(ctx, eventChannel)
		if err == nil || ctx.Err() != nil {
			return err
		}

		// If the connection lasted longer than 30 seconds, reset backoff.
		// This prevents a long-lived connection's failure from using accumulated backoff.
		if time.Since(connectedAt) > 30*time.Second {
			backoff = 1 * time.Second
		}

		slog.Warn("Docker event stream disconnected, reconnecting",
			"backoff", backoff, "error", err)

		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return ctx.Err()
		}

		backoff = min(backoff*2, maxBackoff)

		slog.Info("Reconnected to Docker daemon, triggering resync")
		select {
		case eventChannel <- events.Event{Type: events.ActionResync}:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
```

- [ ] **Step 3: Run all monitor tests**

Run: `go test ./internal/docker/... -v`
Expected: all tests pass

- [ ] **Step 4: Commit**

```bash
git add internal/docker/monitor.go internal/docker/monitor_test.go
git commit -m "feat(docker): reset reconnection backoff after stable connection"
```

---

### Task 7: Update handleContainerStart to skip flapping for health events

**Files:**
- Modify: `internal/controller/controller.go`

The current `handleContainerStart` calls `c.isFlapping()` and `c.updateContainerHealth()`. When called from `handleHealthHealthy`, we said health events should skip flapping. The simplest approach: `handleHealthHealthy` calls `handleContainerStart` directly, which checks flapping. This is actually fine — if a container IS flapping (due to restarts), we should still respect that during health events. The spec says "health events do not trigger flapping," meaning they don't INCREMENT the restart counter, not that they ignore existing flapping state.

The `updateContainerHealth` call in `handleContainerStart` with `isStartEvent=true` WILL increment the restart counter. This is a problem when called from `handleHealthHealthy`.

Solution: extract the rule registration logic from `handleContainerStart` into a shared method.

- [ ] **Step 1: Extract registerContainerRules from handleContainerStart**

In `internal/controller/controller.go`, add a new method:

```go
// registerContainerRules parses labels, validates, and registers ingress rules for a container.
// Returns the parsed hostnames. The caller must handle locking.
func (c *Controller) registerContainerRules(ctx context.Context, event events.Event) ([]string, error) {
	parsedRules, err := label.Parse(event.ContainerInfo)
	if err != nil {
		return nil, fmt.Errorf("failed to parse container labels for container %s: %w", event.ContainerID, err)
	}

	c.mu.RLock()
	if err := c.ruleValidator.Validate(parsedRules, c.ingressRules); err != nil {
		c.mu.RUnlock()
		return nil, fmt.Errorf("invalid ingress rules for container %s: %w", event.ContainerID, err)
	}
	c.mu.RUnlock()

	hostnames := make([]string, 0)
	for _, rule := range parsedRules {
		if rule.Hostname.Value != "" {
			hostnames = append(hostnames, rule.Hostname.Value)
		}
	}

	c.mu.Lock()
	for _, rule := range parsedRules {
		if rule.Hostname.Value != "" {
			c.ingressRules[rule.Hostname.Value] = *rule
		}
	}
	c.containerRules[event.ContainerID] = hostnames
	c.mu.Unlock()

	return hostnames, nil
}
```

- [ ] **Step 2: Refactor handleContainerStart to use registerContainerRules**

Replace the body of `handleContainerStart` in `internal/controller/controller.go`:

```go
// handleContainerStart handles container start events.
func (c *Controller) handleContainerStart(ctx context.Context, event events.Event) error {
	if !c.isDocktunnelEnabled(event) {
		return nil
	}
	slog.Info("Handling container start event", "containerID", event.ContainerID)

	// Check if container is restarting from pending deletion
	if _, pending := c.stateManager.GetPendingDeletion(event.ContainerID); pending {
		slog.Info("Container restarting during retention period, canceling retention timer",
			"containerID", event.ContainerID)
		if err := c.stateManager.RestoreActiveTunnel(event.ContainerID); err != nil {
			slog.Warn("Failed to restore active tunnel for container",
				"containerID", event.ContainerID, "error", err)
		}
	}

	// Check if container is flapping
	if c.isFlapping(event.ContainerID) {
		slog.Warn("Container is flapping, ignoring start event", "containerID", event.ContainerID)
		return nil
	}

	if _, err := c.registerContainerRules(ctx, event); err != nil {
		slog.Error("Failed to register container rules", "error", err, "containerID", event.ContainerID)
		return err
	}

	// Update health tracking (increments restart counter)
	c.mu.Lock()
	c.updateContainerHealth(event.ContainerID, true)
	c.mu.Unlock()

	return c.syncToCloudflare(ctx)
}
```

- [ ] **Step 3: Update handleHealthHealthy to use registerContainerRules directly**

```go
func (c *Controller) handleHealthHealthy(ctx context.Context, event events.Event) error {
	if !c.isDocktunnelEnabled(event) {
		return nil
	}
	slog.Info("Container became healthy, exposing services", "containerID", event.ContainerID)

	if _, err := c.registerContainerRules(ctx, event); err != nil {
		slog.Error("Failed to register container rules on health event",
			"error", err, "containerID", event.ContainerID)
		return err
	}

	// Do NOT call updateContainerHealth — health events don't affect flapping counter
	return c.syncToCloudflare(ctx)
}
```

- [ ] **Step 4: Run full test suite**

Run: `go test ./internal/... -v`
Expected: all tests pass

- [ ] **Step 5: Commit**

```bash
git add internal/controller/controller.go
git commit -m "refactor(controller): extract registerContainerRules, skip flapping on health events"
```

---

### Task 8: Full integration verification

**Files:** None (verification only)

- [ ] **Step 1: Build the full project**

Run: `go build ./...`
Expected: compiles with no errors

- [ ] **Step 2: Run full test suite**

Run: `go test ./... -v`
Expected: all tests pass

- [ ] **Step 3: Run go vet**

Run: `go vet ./...`
Expected: no issues

- [ ] **Step 4: Run format check**

Run: `gofmt -s -l .`
Expected: no output (all files formatted)
