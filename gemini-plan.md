你好！我是 Gemini。作为一个拥有丰富系统编程经验的 Go 语言高级工程师，我非常乐意协助你设计这个 Cloudflare Tunnel Docker Controller（以下简称 **CFT-Controller**）。

这是一个非常实用的 DevOps 工具，它弥合了 Docker 动态环境与 Cloudflare Tunnel 静态配置之间的鸿沟。你的需求非常详尽，特别是关于 **优先级回退** 和 **Traefik 兼容性** 的设计，这增加了系统的鲁棒性。

以下是针对该项目的架构设计方案。

------

### 1. 架构概览 (High-Level Architecture)

**CFT-Controller** 将作为一个独立的 Docker 容器（Sidecar 或 Global Controller）运行。它通过挂载 `/var/run/docker.sock` 与 Docker 守护进程通信，并利用 Cloudflare API (Remote Config) 实时更新 Tunnel 的 Ingress 规则。

核心架构采用 **事件驱动 (Event-Driven)** + **状态协调 (Reconciliation)** 模式。

#### 系统数据流图

1. **Event Source:** Docker Daemon 发送 `START`, `DIE`, `DESTROY` 事件。
2. **Ingestion:** Watcher 模块接收事件，过滤掉无 `cftunnel.enable=true` 标签的无关容器。
3. **Processing:**
   - **Inspector:** 调用 Docker API 查询容器详细信息（Labels, Ports, Networks）。
   - **Parser:** 应用 4 层优先级策略，解析出标准化的 `IngressRule` 对象。
   - **Policy Engine:** 处理 `retention`（保留策略）和 GC 逻辑。
4. **State Management:** 维护一份 "Desired State"（期望状态）。
5. **Actuation:** Syncer 模块计算 Diff，通过 Cloudflare API 更新远程配置。

------

### 2. 技术选型 (Tech Stack)

- **编程语言:** Go (Golang) 1.22+
- **Docker SDK:** `github.com/docker/docker/client` (官方 SDK)
- **Cloudflare SDK:** `github.com/cloudflare/cloudflare-go` (官方 SDK，用于操作 Cloudflare Zero Trust API)
- **配置管理:** `github.com/spf13/viper` (处理全局默认配置、环境变量)
- **日志系统:** `log/slog` (Go 1.21+ 标准库，结构化日志)
- **缓存/状态:** 内存 Map + `encoding/gob` (本地文件持久化，用于重启后恢复“保留期”内的容器状态)。
- **解析库:** `github.com/traefik/paerser` (可选，或手写正则解析 Traefik 的 `Host('...')` 规则)。

------

### 3. 核心模块设计

我们需要将系统解耦为以下五个核心模块：

#### 3.1. Watcher (事件监听器)

- **功能**: 长期运行的协程，监听 Docker Event Stream。
- **核心逻辑**:
  - 监听 `container` 类型的 `start` (创建/启动), `die` (停止), `destroy` (删除) 事件。
  - **初筛**: 检查事件中的 `Actor.Attributes` 是否包含 `cftunnel.enable=true`。如果包含，丢入处理通道；否则直接丢弃。
  - **全量同步**: 启动时执行一次 `ContainerList`，以防漏掉在 Controller 停机期间启动的容器。

#### 3.2. ConfigResolver (配置解析引擎 - **最核心**)

- **功能**: 实现你定义的“4层优先级”策略，将杂乱的 Labels 转换为统一的配置对象。
- **核心逻辑**:
  - **Step 1 (Check)**: 再次确认 `cftunnel.enable`。
  - **Step 2 (Local Rules)**: 遍历 `cftunnel.*` 标签。
  - **Step 3 (Traefik Compat)**: 如果 Local Rules 缺少关键信息（如 Hostname），正则提取 `traefik.http.routers.*` 和 `traefik.http.services.*`。
    - *难点*: Traefik 将 Router（域名）和 Service（端口）分开，需要通过 Router 关联的 Service Name 进行匹配。
  - **Step 4 (Auto Detect)**: 如果端口仍未定义，读取 `ContainerJSON.NetworkSettings.Ports` 获取第一个暴露端口。
  - **Step 5 (Global Defaults)**: 填充缺失的 Timeouts、TLS 设置等。

#### 3.3. StateManager (状态与生命周期管理)

- **功能**: 维护当前内存中的“隧道规则表”，并处理删除保留策略（Retention Policy）。
- **核心数据结构**: `map[ContainerID]TunnelConfigWrapper`。
- **保留逻辑 (GC)**:
  - 当收到 `DIE` 事件，读取 `cftunnel.delete_retention`。
  - 如果是 `immediate`: 立即从 State 中移除，触发同步。
  - 如果是 `30m`: 标记该 Entry 为 `PendingDelete`，记录 `DeadTime`。
  - 如果是 `forever`: 标记为 `Ignored`，不从配置中删除。
  - **Ticker**: 启动一个后台 Ticker（如每分钟），扫描 `PendingDelete` 的对象，若超时则物理删除。

#### 3.4. CloudflareProvider (云端同步器)

- **功能**: 与 Cloudflare API 交互。
- **核心逻辑**:
  - 获取当前的 Remote Config (Ingress Rules)。
  - 将本地 State 转换为 Cloudflare Ingress 格式。
  - **去重与排序**: Cloudflare Ingress 是有序数组，必须保证 `Catch-all` 规则在最后。
  - 调用 API 更新配置。

------

### 4. 核心数据结构设计 (Go Structs)

这是系统的骨架，决定了代码的清晰度。

Go

```
// ContainerInfo 封装容器的原始信息，用于传递给解析器
type ContainerInfo struct {
    ID          string
    Name        string
    Labels      map[string]string
    IPAddress   string // 容器内网IP
    ExposedPort int    // 自动探测的端口
    Status      string // Running, Exited
    StoppedAt   time.Time
}

// IngressRule 标准化的单条隧道规则 (对应 Cloudflare API 结构)
type IngressRule struct {
    Hostname string `json:"hostname,omitempty"`
    Path     string `json:"path,omitempty"`
    Service  string `json:"service"` // http://10.0.0.1:8080
    OriginRequest OriginRequestConfig `json:"originRequest,omitempty"`
}

// OriginRequestConfig 对应你表格中的 Origin/Access 设置
type OriginRequestConfig struct {
    NoTLSVerify          *bool          `json:"noTLSVerify,omitempty"`
    ConnectTimeout       *time.Duration `json:"connectTimeout,omitempty"`
    Access               *AccessConfig  `json:"access,omitempty"`
    // ... 其他字段 (KeepAlive, HTTP2, etc)
}

// AccessConfig Zero Trust 相关配置
type AccessConfig struct {
    Required  bool   `json:"required"`
    TeamName  string `json:"teamName"`
    AudTag    string `json:"audTag"`
}

// TunnelEntry 状态管理器中的单元
type TunnelEntry struct {
    ContainerID string
    TunnelID    string // 支持多 Tunnel 管理
    Rule        IngressRule
    
    // GC 控制
    RetentionPolicy string // "immediate", "forever", "1h"
    DeletedAt       *time.Time
}
```

------

### 5. 关键逻辑流程详解

#### A. 配置解析优先级 (Config Strategy Implementation)

我们需要一个 Builder 模式的方法 `BuildIngressRule(c *ContainerInfo, global DefaultConfig)`.

1. **确定 Service URL**:
   - *尝试 1*: 读取 `cftunnel.<name>.service` (最高级，直接 URL)。
   - *尝试 2*: 组合 `scheme` + `ip` + `port`。
     - `scheme`: `cftunnel.<name>.scheme` -> `traefik...scheme` -> "http"。
     - `port`: `cftunnel.<name>.port` -> `traefik...port` -> `c.ExposedPort` -> 80。
     - `ip`: 使用容器 Bridge 网络 IP 或 Service Name (如果在同一网络)。
2. **确定 Hostname**:
   - *尝试 1*: `cftunnel.<name>.hostname`。
   - *尝试 2*: `traefik.http.routers.<name>.rule` (解析 regex `Host\((.+)\)` )。
   - *尝试 3*: 如果失败，报错或跳过（Hostname 通常是必填项，除非是 Catch-all）。
3. **应用全局 Origin 设置**:
   - 使用反射或深拷贝，将 Global Origin Config 复制到对象中。
   - 遍历容器 Labels，覆盖特定字段。

#### B. 垃圾回收 (Garbage Collection)

Go

```
func (sm *StateManager) RunGC() {
    for id, entry := range sm.entries {
        if entry.DeletedAt == nil {
            continue
        }
        
        // 解析时长，例如 "1h"
        duration, _ := time.ParseDuration(entry.RetentionPolicy)
        
        if time.Since(*entry.DeletedAt) > duration {
            // 真正删除
            delete(sm.entries, id)
            sm.requestSync = true // 标记需要同步 Cloudflare
        }
    }
}
```

------

### 6. 依赖管理 (Go Modules)

你需要初始化的 `go.mod` 核心依赖：

Bash

```
go mod init cft-controller
go get github.com/docker/docker/client
go get github.com/cloudflare/cloudflare-go
go get github.com/spf13/viper
go get github.com/robfig/cron/v3  # 可选，用于精确的 GC 定时任务
```

------

### 7. 开发步骤规划

建议分为三个阶段进行开发：

#### Phase 1: 核心连接与监听 (MVP)

- **目标**: 能跑通流程，打印日志，不实际修改 Cloudflare。
- **任务**:
  1. 初始化 Docker Client。
  2. 编写 Watcher，监听 Event。
  3. 实现简单的 Label Parser (仅支持 `cftunnel.*`)。
  4. 在控制台打印出：“检测到容器 X，生成规则：Hostname: abc.com -> Service: 172.17.0.2:80”。

#### Phase 2: Traefik 兼容与 Cloudflare 集成

- **目标**: 实际操作 Tunnel，支持 Traefik 标签。
- **任务**:
  1. 引入 Cloudflare Go SDK，配置 API Token。
  2. 编写 `Sync()` 方法：获取 Remote Config -> 修改 -> Put Remote Config。
  3. 完善 Parser，增加 Regex 解析 Traefik Label。
  4. 处理 `cftunnel.service` 等 catch-all 逻辑。

#### Phase 3: 高级策略与健壮性

- **目标**: 生产环境可用。
- **任务**:
  1. 实现 Retention Policy (GC)。
  2. 实现 State 持久化 (保存到本地文件，防止重启丢失 PendingDelete 状态)。
  3. 增加 OriginRequest 的所有详细参数映射 (Timeout, TLS 等)。
  4. 构建 Docker 镜像，编写 Docker Compose 示例。
