package docker

import (
	"context"
	"fmt"

	containerTypes "github.com/docker/docker/api/types/container"
	eventTypes "github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"

	"docktunnel/internal/events"
)

// Manager 封装了所有Docker相关的操作
type Manager struct {
	client *client.Client
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

// ListenForEvents 监听Docker事件并在相关事件发生时通过channel发送通知
func (m *Manager) ListenForEvents(ctx context.Context, eventChannel chan<- events.Event) error {
	// 创建过滤器，只监听容器相关的事件
	filter := filters.NewArgs()
	filter.Add("type", "container")
	filter.Add("label", "docktunnel.enable=true")

	// 监听事件
	messages, errs := m.client.Events(ctx, eventTypes.ListOptions{
		Filters: filter,
	})

	// 处理事件和错误
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-errs:
			if err != nil {
				return fmt.Errorf("docker event error: %w", err)
			}
		case message := <-messages:
			// 只处理我们关心的事件类型
			if message.Type != "container" {
				continue
			}

			event := events.Event{
				Type:        eventTypes.Action(message.Action),
				ContainerID: message.Actor.ID,
			}

			// 如果是启动事件，获取容器详细信息
			if event.Type == eventTypes.ActionStart {
				containerInfo, err := m.client.ContainerInspect(ctx, event.ContainerID)
				if err != nil {
					// 即使无法获取容器信息，也发送事件，但不包含详细信息
					// 上层处理逻辑需要处理ContainerInfo为nil的情况
				} else {
					event.ContainerInfo = &containerInfo
				}
			}

			// 当监听到相关事件时，发送通知
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
