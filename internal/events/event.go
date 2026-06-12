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

// Event 是一个统一的数据结构，用于在系统各部分之间传递事件信息
type Event struct {
	Type          events.Action           // 直接复用 Docker SDK 的事件类型
	ContainerID   string
	ContainerInfo *container.InspectResponse // 复用 SDK 的容器详情类型，仅在 Start 事件中填充
}