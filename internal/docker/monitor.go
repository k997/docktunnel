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

// Manager 封装了所有Docker相关的操作
type Manager struct {
	client dockerClient
}

// NewManager 创建一个新的Docker Manager实例
func NewManager() (*Manager, error) {
	// 使用环境变量创建Docker客户端
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("failed to create docker client: %w", err)
	}

	return &Manager{
		client: cli,
	}, nil
}

// ScanRunningContainers 扫描所有正在运行且启用了DockTunnel的容器
func (m *Manager) ScanRunningContainers(ctx context.Context) ([]events.Event, error) {
	// 创建过滤器，只获取正在运行的容器
	filter := filters.NewArgs()
	filter.Add("status", "running")
	filter.Add("label", "docktunnel.enable=true")

	// 列出符合条件的容器
	containers, err := m.client.ContainerList(ctx, containerTypes.ListOptions{
		Filters: filter,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list containers: %w", err)
	}

	// 转换为事件结构
	var result []events.Event
	for _, c := range containers {
		event := events.Event{
			Type:        eventTypes.ActionStart,
			ContainerID: c.ID,
		}
		containerInfo, err := m.client.ContainerInspect(ctx, c.ID)
		if err != nil {
			// 即使无法获取容器信息，也发送事件，但不包含详细信息
			// 上层处理逻辑需要处理ContainerInfo为nil的情况
		} else {
			event.ContainerInfo = &containerInfo
		}
		result = append(result, event)
	}

	return result, nil
}

// ListenForEvents listens to Docker events with automatic reconnection.
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

		// If connection lasted >30s, reset backoff
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

		// Emit resync event after reconnect
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

			// Translate Docker actions to internal actions
			switch {
			case message.Action == eventTypes.ActionStart:
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
				containerInfo, err := m.client.ContainerInspect(ctx, event.ContainerID)
				if err != nil {
					slog.Warn("Failed to inspect container on stop event",
						"containerID", event.ContainerID, "error", err)
				} else {
					event.ContainerInfo = &containerInfo
				}
			case message.Action == eventTypes.ActionDie:
				event.Type = eventTypes.ActionDie
				containerInfo, err := m.client.ContainerInspect(ctx, event.ContainerID)
				if err != nil {
					slog.Warn("Failed to inspect container on die event",
						"containerID", event.ContainerID, "error", err)
				} else {
					event.ContainerInfo = &containerInfo
				}
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

// Close 关闭Docker客户端连接
func (m *Manager) Close() error {
	if m.client != nil {
		return m.client.Close()
	}
	return nil
}
