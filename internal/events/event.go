package events

import (
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/events"
)

// Event 是一个统一的数据结构，用于在系统各部分之间传递事件信息
type Event struct {
	Type          events.Action           // 直接复用 Docker SDK 的事件类型
	ContainerID   string
	ContainerInfo *container.InspectResponse // 复用 SDK 的容器详情类型，仅在 Start 事件中填充
}