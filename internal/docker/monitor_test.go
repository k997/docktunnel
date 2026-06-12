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
