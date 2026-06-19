package docker

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	containerTypes "github.com/docker/docker/api/types/container"
	eventTypes "github.com/docker/docker/api/types/events"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"docktunnel/internal/events"
	"docktunnel/internal/metrics"
)

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
	msgs := make(chan eventTypes.Message)
	errs := make(chan error, 1)
	errs <- errors.New("no more mock responses")
	close(msgs)
	return msgs, errs
}

func (m *alternatingMockClient) Close() error { return nil }

func TestListenForEvents_ReconnectsOnStreamError(t *testing.T) {
	firstErr := make(chan error, 1)
	firstErr <- errors.New("stream closed")
	firstMsg := make(chan eventTypes.Message)
	close(firstMsg)

	secondErr := make(chan error)
	secondMsg := make(chan eventTypes.Message)

	mockedClient := &alternatingMockClient{
		responses: []eventsResponse{
			{msgs: firstMsg, errs: firstErr},
			{msgs: secondMsg, errs: secondErr},
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

	// Wait for reconnection (initial backoff 1s + margin)
	time.Sleep(1500 * time.Millisecond)

	mockedClient.mu.Lock()
	calls := mockedClient.eventsCalls
	mockedClient.mu.Unlock()

	if calls < 2 {
		t.Errorf("expected Events to be called at least 2 times (initial + reconnect), got %d", calls)
	}

	cancel()
	<-errCh
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

func TestListenOnce_TranslatesHealthStatusEvents(t *testing.T) {
	msgs := make(chan eventTypes.Message, 3)
	errs := make(chan error)

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

	// Signal end of stream by sending EOF error after messages are read
	go func() {
		time.Sleep(50 * time.Millisecond)
		errs <- errors.New("stream closed")
	}()

	client := &simpleMockClient{
		msgs:          msgs,
		errs:          errs,
		inspectResult: containerTypes.InspectResponse{},
	}

	mgr := &Manager{client: client}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	eventCh := make(chan events.Event, 3)

	// listenOnce will return an error when the stream ends
	err := mgr.listenOnce(ctx, eventCh)
	if err == nil {
		t.Fatalf("expected error when stream ends, got nil")
	}

	expected := []eventTypes.Action{
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

// TestTrySendEvent_DropsAndCountsWhenChannelFull verifies that a non-resync
// event is dropped (and counted) when the channel is full, rather than
// blocking the caller.
func TestTrySendEvent_DropsAndCountsWhenChannelFull(t *testing.T) {
	before := testutil.ToFloat64(metrics.EventsDropped)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Single-slot channel, already full.
	ch := make(chan events.Event, 1)
	ch <- events.Event{Type: "start", ContainerID: "filler"}

	sent := trySendEvent(ctx, ch, events.Event{Type: "start", ContainerID: "dropped"})
	if sent {
		t.Error("expected send to be dropped when channel is full")
	}

	after := testutil.ToFloat64(metrics.EventsDropped)
	if after-before != 1 {
		t.Errorf("expected EventsDropped delta=1, got %v", after-before)
	}

	// Confirm only the filler event is still in the channel.
	if len(ch) != 1 {
		t.Errorf("expected channel len=1, got %d", len(ch))
	}
}

// TestTrySendEvent_NeverDropsResync verifies resync events bypass the drop
// path — they're how we recover from previous drops.
func TestTrySendEvent_NeverDropsResync(t *testing.T) {
	before := testutil.ToFloat64(metrics.EventsDropped)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch := make(chan events.Event, 1)
	ch <- events.Event{Type: "start"} // fill the channel

	// Drain in a goroutine so the resync send can complete.
	go func() {
		<-ch
	}()

	sent := trySendEvent(ctx, ch, events.Event{Type: events.ActionResync})
	if !sent {
		t.Error("resync event should always be sent, even on a full channel")
	}

	after := testutil.ToFloat64(metrics.EventsDropped)
	if after != before {
		t.Errorf("resync must not increment EventsDropped, got delta=%v", after-before)
	}
}

// TestTrySendEvent_ReturnsFalseOnContextCancel verifies the helper honors
// context cancellation even when blocking on a resync send.
func TestTrySendEvent_ReturnsFalseOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	ch := make(chan events.Event, 1)
	ch <- events.Event{Type: "start"}

	cancel() // cancel before calling

	sent := trySendEvent(ctx, ch, events.Event{Type: events.ActionResync})
	if sent {
		t.Error("expected send to be false after ctx cancel")
	}
}
