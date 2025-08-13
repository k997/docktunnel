package docker

import (
	"context"
	"fmt"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/events"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
)

// Manager 封装了所有Docker相关的操作
type Manager struct {
	client *client.Client
}

// Container 表示一个Docker容器的信息
type Container struct {
	ID     string
	Names  []string
	Labels map[string]string
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
func (m *Manager) ScanRunningContainers(ctx context.Context) ([]Container, error) {
	// 创建过滤器，只获取正在运行的容器
	filter := filters.NewArgs()
	filter.Add("status", "running")
	filter.Add("label", "docktunnel.enable=true")

	// 列出符合条件的容器
	containers, err := m.client.ContainerList(ctx, container.ListOptions{
		Filters: filter,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list containers: %w", err)
	}

	// 转换为内部Container结构
	var result []Container
	for _, c := range containers {
		result = append(result, Container{
			ID:     c.ID,
			Names:  c.Names,
			Labels: c.Labels,
		})
	}

	return result, nil
}

// ListenForEvents 监听Docker事件并在相关事件发生时通过channel发送通知
func (m *Manager) ListenForEvents(ctx context.Context, updateChan chan<- struct{}) error {
	// 创建过滤器，只监听容器相关的事件
	filter := filters.NewArgs()
	filter.Add("type", string(events.ContainerEventType))
	filter.Add("event", string(events.ActionStart))
	filter.Add("event", string(events.ActionDie))
	filter.Add("event", string(events.ActionStop))
	filter.Add("label", "docktunnel.enable=true")

	// 监听事件
	messages, errs := m.client.Events(ctx, events.ListOptions{
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
		case <-messages:
			// 当监听到相关事件时，发送通知
			select {
			case updateChan <- struct{}{}:
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
