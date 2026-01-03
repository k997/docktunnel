package mocks

import (
	"context"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/events"
)

// MockDockerClient is a mock implementation of the Docker API client
type MockDockerClient struct {
	Containers map[string]*types.ContainerJSON
	EventChan  chan events.Message
	ErrorChan  chan error
	ShouldDelay bool
	Delay      time.Duration
}

// NewMockDockerClient creates a new mock Docker client
func NewMockDockerClient() *MockDockerClient {
	return &MockDockerClient{
		Containers: make(map[string]*types.ContainerJSON),
		EventChan:  make(chan events.Message, 100),
		ErrorChan:  make(chan error, 1),
		ShouldDelay: false,
	}
}

// Events returns the Docker event stream
func (m *MockDockerClient) Events(ctx context.Context, options events.ListOptions) (<-chan events.Message, <-chan error) {
	if m.ShouldDelay {
		time.Sleep(m.Delay)
	}
	return m.EventChan, m.ErrorChan
}

// ContainerInspect retrieves container details
func (m *MockDockerClient) ContainerInspect(ctx context.Context, containerID string) (types.ContainerJSON, error) {
	if m.ShouldDelay {
		time.Sleep(m.Delay)
	}

	if containerJSON, exists := m.Containers[containerID]; exists {
		return *containerJSON, nil
	}

	return types.ContainerJSON{}, &MockDockerError{Message: "container not found"}
}

// ContainerList lists all containers
func (m *MockDockerClient) ContainerList(ctx context.Context, options container.ListOptions) ([]types.Container, error) {
	containers := make([]types.Container, 0, len(m.Containers))

	for _, c := range m.Containers {
		containers = append(containers, types.Container{
			ID:    c.ID,
			Names: []string{c.Name},
			Image: c.Config.Image,
		})
	}

	return containers, nil
}

// Close closes the mock client
func (m *MockDockerClient) Close() error {
	close(m.EventChan)
	close(m.ErrorChan)
	return nil
}

// EmitEvent sends a mock Docker event
func (m *MockDockerClient) EmitEvent(event events.Message) {
	m.EventChan <- event
}

// AddContainer adds a container to the mock
func (m *MockDockerClient) AddContainer(containerJSON *types.ContainerJSON) {
	m.Containers[containerJSON.ID] = containerJSON
}

// MockDockerError represents a mock Docker error
type MockDockerError struct {
	Message string
}

func (e *MockDockerError) Error() string {
	return e.Message
}
