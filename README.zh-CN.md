# DockTunnel

[English](README.md) | 简体中文

[![Go Report Card](https://goreportcard.com/badge/k997/docktunnel)](https://goreportcard.com/report/k997/docktunnel)
[![License](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

## 简介

**DockTunnel** 是一个智能化的 Cloudflare Tunnel Docker Controller，通过监听 Docker 容器事件自动管理 Cloudflare Tunnel 配置和 DNS 记录。它弥合了 Docker 动态环境与 Cloudflare Tunnel 静态配置之间的鸿沟，让容器化服务通过自定义域名轻松暴露到互联网。

### 核心特性

- **事件驱动架构**: 实时监听 Docker 容器启动/停止事件，自动同步配置
- **智能标签解析**: 支持多层级配置优先级（自定义标签 → Traefik 兼容 → 自动检测 → 全局默认）
- **Traefik 兼容**: 可选的最小兼容子集，需 `docktunnel.traefik.enable=true` 显式开启（见 [Traefik 兼容标签](#traefik-兼容标签)）
- **高级网络支持**: 自动检测容器 IP，支持 Bridge/Host 网络模式
- **强大的容错机制**:
  - 容器抖动检测（Flapping Detection）
  - 智能退避和冷却期管理
  - 事件防抖（Debouncing）
  - 三层容错设计（配置校验、状态同步、资源清理）
- **灵活的清理策略**: 支持立即删除、延时保留、永久保留等多种策略
- **DNS 所有权账本**：只删除本控制器写入过（或收养过）的记录。指向同一隧道但由手工/其他工具维护的记录会被跳过并告警——对账不再可能吞掉不属于它的域名（账本随状态持久化，账本为空或丢失时 fail-safe 为「不删除」）
- **批量 DNS 管理**: 高效的批量 DNS 记录操作，减少 API 调用
- **速率限制与重试**: 令牌桶算法 + 指数退避，保护 API 资源

## 工作原理

DockTunnel 采用 **事件驱动 + 状态协调** 的架构模式：

```
┌─────────────────┐    ┌─────────────────┐    ┌─────────────────┐
│   Docker        │    │   Controller    │    │  Cloudflare     │
│   Events        │    │   & Logic       │    │   Manager       │
│   Listener      │◄──►│                 │◄──►│                 │
└─────────────────┘    └─────────────────┘    └─────────────────┘
         │                       │                       │
         │              ┌─────────────────┐              │
         │              │     Config      │              │
         │              │     Manager     │              │
         │              └─────────────────┘              │
         │                       │                       │
         └───────────────────────────────────────────────┘
```

### 数据流程

1. **事件源**: Docker Daemon 发送 `START`、`DIE`、`DESTROY` 事件
2. **事件摄入**: Watcher 模块接收事件，过滤掉无 `docktunnel.enable=true` 标签的无关容器
3. **配置处理**:
   - **检查器**: 调用 Docker API 查询容器详细信息（Labels, Ports, Networks）
   - **解析器**: 应用 4 层优先级策略，解析出标准化的 `IngressRule` 对象
   - **策略引擎**: 处理 `retention`（保留策略）和 GC 逻辑
4. **状态管理**: 维护一份 "期望状态"（Desired State）
5. **执行同步**: Syncer 模块计算 Diff，通过 Cloudflare API 更新远程配置

## 快速开始

### 系统要求

- **Docker**: 18.09+ （用于容器部署）
- **Go**: 1.24+ （仅开发环境，见 [go.mod](go.mod)）
- **Cloudflare 账户**和 API Token，需要以下权限：
  - Account: Read/Write
  - Zone: Read/Write
  - Tunnel: Read/Write

### 安装

#### 方式 1: 使用 Docker（推荐）

```bash
docker run -d \
  --name=docktunnel \
  --restart=unless-stopped \
  -v /var/run/docker.sock:/var/run/docker.sock:ro \
  -v ./config.yaml:/etc/docktunnel/config.yaml:ro \
  -v docktunnel-state:/var/lib/docktunnel \
  ghcr.io/k997/docktunnel:latest
```

#### 方式 2: 从源码构建

```bash
git clone https://github.com/k997/docktunnel.git
cd docktunnel

# 构建二进制文件
go build -o docktunnel ./cmd/docktunnel

# 运行
./docktunnel
```

#### 方式 3: 使用 Make

```bash
# 下载依赖
make deps

# 构建并运行
make build
make run
```

### 运行 cloudflared 连接器（必需）

> **重要**：DockTunnel **只管理隧道配置与 DNS 记录**（通过 Cloudflare API 写入
> ingress 规则），它**本身不承载流量**。要让域名真正生效，你必须在**能够访问
> 容器网络**的机器上运行 **cloudflared 连接器**（即 Cloudflare Tunnel 的
> `cloudflared tunnel run` 守护进程），并在 Cloudflare 中把该隧道注册到
> DockTunnel 管理的同一个 Tunnel ID。

- 安装与连接 cloudflared 请参考 Cloudflare 官方文档：
  - 安装：<https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/downloads/>
  - 创建并运行隧道：<https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/get-started/create-remote-tunnel/>
- **可达性前提**：DockTunnel 默认使用容器的**桥接网络 IP**（如
  `172.17.0.2`）作为 ingress 的源站地址。该 IP 只有**与容器位于同一 Docker
  网络**（或同主机）的进程才能访问，因此 cloudflared 连接器必须运行在
  **能 ping 通容器 IP 的机器**上——常见做法是把连接器作为容器放进与业务容器
  相同的 Docker 网络，或使用 `docktunnel.<svc>.network` / `traefik.docker.network`
  标签指定正确的网络（`host` 表示 `localhost`）。
- 若未运行连接器，DNS 与隧道配置虽然正确，但访问域名会超时/报错。

### 配置

DockTunnel 支持多种配置方式，按优先级从高到低：

1. 环境变量（前缀 `DOCKTUNNEL_`）
2. 配置文件 `./config.yaml`、`/etc/docktunnel/config.yaml`，或由
   `CONFIG_PATH` 环境变量指定的完整路径（常用于 systemd 部署）
3. 默认值

#### 配置文件示例

创建 `config.yaml`：

```yaml
log:
  level: info          # 日志级别: debug, info, warn, error
  format: text         # 日志格式: text 或 json

cloudflare:
  accountId: "your-cloudflare-account-id"
  apiToken: "your-cloudflare-api-token"
  tunnelName: "DockTunnel"      # 隧道名称，为空则自动创建
  tunnelId: ""                   # 可选：指定现有隧道 ID
  catchAll: "http_status:404"   # 默认 catch-all 规则
  # API 速率限制（请求/秒）。Cloudflare 官方限额约 1200 请求/5 分钟（≈4 RPS），
  # 默认 4，超过会触发 429 限流。
  rateLimit: 4
  maxRetries: 3                  # 最大重试次数
  retryDelay: 1s                 # 初始重试延迟
  maxRetryDelay: 30s             # 最大重试延迟

controller:
  flappingWindow: 60s            # 容器抖动检测时间窗口
  flappingThreshold: 5           # 触发抖动的重启次数阈值
  coolingPeriod: 300s            # 冷却期（5分钟）
  maxCoolingPeriod: 1800s        # 最大冷却期（30分钟）
  debounceDuration: 2s           # 事件防抖延迟

cleanup:
  # 退出清理总开关（默认 false）。false 时任何策略都不会清理；
  # true 时策略才生效：graceful-cleanup（清理）| fast-exit（不清理直接退出）。
  onExit: false
  strategy: "graceful-cleanup"

server:
  bindAddr: "127.0.0.1"  # 诊断/指标服务绑定地址（默认仅回环）
  port: 9100             # 端口（/metrics /healthz /debug/state）
  # debugToken: "..."    # 非回环绑定（如 0.0.0.0）时必须设置，
                         # 否则 /debug/state 会泄露全部 hostname/service URL
```

#### 环境变量配置

所有环境变量使用 `DOCKTUNNEL_` 前缀 + 配置路径（点转下划线），例如 `cloudflare.accountId` → `DOCKTUNNEL_CLOUDFLARE_ACCOUNT_ID`。环境变量优先级高于配置文件。

```bash
export DOCKTUNNEL_LOG_LEVEL=debug
export DOCKTUNNEL_CLOUDFLARE_ACCOUNT_ID="your-account-id"
export DOCKTUNNEL_CLOUDFLARE_API_TOKEN="your-api-token"
export DOCKTUNNEL_CLOUDFLARE_TUNNEL_NAME="DockTunnel"
```

完整变量清单见 [.env.example](.env.example)。

## 容器标签系统

### 标签架构

DockTunnel 使用双层标签架构：
- **全局规则**: 应用于所有服务的默认配置
- **局部规则**: 针对特定服务的配置（优先级更高）

### 标签格式

```
docktunnel.<service-name>.<attribute>
```

例如：`docktunnel.web.hostname` 中，`web` 是服务名，`hostname` 是属性名。

### 核心标签

#### 必需标签

| 标签 | 类型 | 说明 | 示例 |
|------|------|------|------|
| `docktunnel.enable` | boolean | 启用开关（必填） | `true` |

#### 全局规则标签

| 标签 | 类型 | 说明 | 默认值 |
|------|------|------|--------|
| `docktunnel.traefik.enable` | boolean | 显式开启 `traefik.*` 标签解析（默认关闭） | `false` |
| `docktunnel.delete_retention` | string | 全局保留策略（对所有服务生效） | `immediate` |

> `docktunnel.retention` 是 `delete_retention` 的**旧别名**，两者等价；
> 文档主推 `delete_retention`。

**清理策略说明**：
- `0` / `immediate`: 立即删除（默认，容器停止即删除）
- `forever` / `keep`: 永久保留
- `30m` / `1h` / `7d`: 延时删除（支持时间单位：`s`, `m`, `h`, `d`）

#### 局部规则标签

| 标签 | 类型 | 说明 | 示例 |
|------|------|------|------|
| `docktunnel.<name>.hostname` | string | 外部域名 | `example.com` |
| `docktunnel.<name>.service` | string | 服务地址 | `http://172.17.0.2:8080` |
| `docktunnel.<name>.path` | string | 路径前缀 | `/api` |
| `docktunnel.<name>.scheme` | string | 内部协议 | `https` |
| `docktunnel.<name>.port` | int | 内部端口 | `8080` |
| `docktunnel.<name>.network` | string | 指定取 IP 的 Docker 网络（`host` 表示 `localhost`） | `my-net` |
| `docktunnel.<name>.delete_retention` | string | 该服务的保留策略（覆盖全局） | `30m` |

**优先级回退**（端口检测）：
1. `docktunnel.<name>.port` 标签
2. Traefik `http.services.<name>.loadbalancer.server.port` 标签
3. 容器首个 ExposedPort
4. 默认端口 `80`

### Origin Request 配置

#### TLS 设置

| 标签 | 类型 | 说明 |
|------|------|------|
| `docktunnel.<name>.originRequest.noTLSVerify` | boolean | 跳过 TLS 验证（允许自签名证书） |
| `docktunnel.<name>.originRequest.originServerName` | string | TLS 握手的 SNI 域名 |
| `docktunnel.<name>.originRequest.caPool` | string | CA 证书路径（需挂载到容器） |

> 注：`matchSNItoHost`（含旧拼写 `matchSniToHost`）已移除——Cloudflare API
> 无此字段，设置该标签会被忽略并记录 WARN。

#### 超时设置

| 标签 | 类型 | 说明 | 默认值 |
|------|------|------|--------|
| `docktunnel.<name>.originRequest.connectTimeout` | duration | TCP 连接超时 | `30s` |
| `docktunnel.<name>.originRequest.tlsTimeout` | duration | TLS 握手超时 | `10s` |
| `docktunnel.<name>.originRequest.tcpKeepAlive` | duration | TCP 保活探测间隔 | `30s` |

#### 连接池设置

| 标签 | 类型 | 说明 | 默认值 |
|------|------|------|--------|
| `docktunnel.<name>.originRequest.keepAliveConnections` | int | 最大空闲连接数 | `100` |
| `docktunnel.<name>.originRequest.keepAliveTimeout` | duration | 空闲连接保持时间 | `1m30s` |

#### HTTP 设置

| 标签 | 类型 | 说明 |
|------|------|------|
| `docktunnel.<name>.originRequest.httpHostHeader` | string | 强制重写 Host Header |
| `docktunnel.<name>.originRequest.http2Origin` | boolean | 启用 HTTP/2（gRPC 服务必须） |
| `docktunnel.<name>.originRequest.disableChunkedEncoding` | boolean | 禁用分块传输编码 |

#### 代理设置

| 标签 | 类型 | 说明 |
|------|------|------|
| `docktunnel.<name>.originRequest.proxyType` | string | 代理类型（通常留空或 `socks`） |
| `docktunnel.<name>.originRequest.noHappyEyeballs` | boolean | 禁用 Happy Eyeballs 算法 |

#### Cloudflare Access（Zero Trust）

| 标签 | 类型 | 说明 |
|------|------|------|
| `docktunnel.<name>.access.required` | boolean | 强制鉴权（未验证则拒绝） |
| `docktunnel.<name>.access.team_name` | string | Zero Trust 团队名称 |
| `docktunnel.<name>.access.aud_tag` | string | JWT Application Audience Tag |

> 代码同时兼容以下别名（取值相同）：`docktunnel.<name>.originRequest.access.*`
> 前缀，以及驼峰拼写 `access.teamName` / `access.audTag`。

### Traefik 兼容标签

DockTunnel 支持解析 Traefik 标签，但**只实现一个可选的最小兼容子集**，
且必须用 `docktunnel.traefik.enable=true` **显式开启**（默认不解析任何
`traefik.*` 标签，避免把 Traefik 内部/受中间件保护的路由意外发布为公网规则）。

**支持**：

| Traefik 标签 | 解析逻辑 |
|--------------|----------|
| `traefik.http.routers.<name>.rule` | 提取 `Host(...)` 与 `Path(...)` 子句 → hostname / path |
| `traefik.http.services.<name>.loadbalancer.server.port` | 源站端口 |
| `traefik.http.services.<name>.loadbalancer.server.scheme` | 源站协议 |
| `traefik.http.services.<name>.loadbalancer.server.url` | 源站 URL（**优先于** port/scheme） |
| `traefik.tcp.routers.<name>.rule` | 提取 `HostSNI(...)` → TCP 规则 |
| `traefik.tcp.services.<name>.loadbalancer.server.port` | TCP 源站端口 |
| `traefik.docker.network` | 指定取 IP 的 Docker 网络（同 `docktunnel.<svc>.network`） |

**拒绝 + WARN**（无法安全映射，相关 router 被跳过并记录 WARN，绝不暴露）：

- `!`（取反）、`&&`、`||`（布尔运算）
- `HostRegexp`、`PathPrefix`、`PathRegexp`
- `Method`、`Header`、`Query`、`ClientIP`
- `middlewares`（引用中间件的 router 会被拒绝——避免不带认证直接暴露）
- `weighted` services，以及引用了不存在或没有 `loadbalancer.server` 的
  service 的 router（否则会静默退化成无端口路由）

**良性忽略**（不影响安全，仅 Info 日志，不产生路由）：

- `entryPoints`、`priority`、`tls`、UDP

**其它行为**：
- 只有 `Path(...)` 而没有 `Host(...)` 子句的 router 会被跳过并 WARN；
  `Host(a.com)` 这类无引号写法同样视为无 hostname 并跳过并 WARN；
- TCP rule 只允许 `HostSNI(...)`，混入 `Host(...)`/`Path(...)` 会被拒绝；
- HTTP rule 中 `Host(...)`/`Path(...)` 之外的任何内容都会被拒绝。

```bash
# 开启 Traefik 兼容解析的最小示例
docker run -d \
  --name=legacy-app \
  -l docktunnel.enable=true \
  -l docktunnel.traefik.enable=true \
  -l traefik.http.routers.app.rule=Host\('legacy.example.com'\) \
  -l traefik.http.services.app.loadbalancer.server.port=8080 \
  legacy-app:latest
```

### 容器 IP 检测

DockTunnel 自动检测容器 IP 地址：

- **Host 网络模式**: 使用 `localhost`
- **Bridge 网络**: 使用容器的 Bridge IP 地址（如 `172.17.0.2`）
- **其他网络**: 使用第一个可用网络的 IP 地址（按网络名排序，结果确定）

可通过 `docktunnel.<svc>.network`（或 Traefik 的 `traefik.docker.network`）
显式指定取 IP 的网络；值为 `host` 时使用 `localhost`，网络不存在时告警并回退
到默认检测。

### 完整标签示例

```bash
docker run -d \
  --name=web-app \
  -l docktunnel.enable=true \
  -l docktunnel.web.hostname=app.example.com \
  -l docktunnel.web.service=http://172.17.0.2:8080 \
  -l docktunnel.web.path=/api \
  -l docktunnel.web.originRequest.noTLSVerify=true \
  -l docktunnel.web.originRequest.connectTimeout=30s \
  -l docktunnel.web.originRequest.keepAliveConnections=50 \
  -l docktunnel.web.access.required=true \
  -l docktunnel.web.access.team_name=myteam \
  nginx:latest
```

## 使用示例

### 示例 1: 基础 Web 服务

```bash
docker run -d \
  --name=my-web-app \
  -p 8080:80 \
  -l docktunnel.enable=true \
  -l docktunnel.web.hostname=myapp.example.com \
  -l docktunnel.web.service=http://localhost:8080 \
  nginx:alpine
```

**自动执行流程**：
1. 检测到容器启动事件
2. 解析标签，生成 Ingress 规则
3. 在 Cloudflare 创建/更新隧道配置
4. 创建 DNS 记录 `myapp.example.com` → 指向隧道
5. 配置生效，外部可访问

### 示例 2: 多服务容器

一个容器内暴露多个服务：

```bash
docker run -d \
  --name=fullstack-app \
  -l docktunnel.enable=true \
  -l docktunnel.frontend.hostname=app.example.com \
  -l docktunnel.frontend.service=http://localhost:3000 \
  -l docktunnel.api.hostname=api.example.com \
  -l docktunnel.api.service=http://localhost:8080 \
  -l docktunnel.api.path=/api \
  -l docktunnel.api.originRequest.http2Origin=true \
  myapp:latest
```

> 其中 `frontend` 为前端服务、`api` 为 API 服务（带 `/api` 路径与 HTTP/2）。

### 示例 3: Traefik 兼容模式

> 必须显式设置 `docktunnel.traefik.enable=true` 才会解析 `traefik.*` 标签。

```bash
docker run -d \
  --name=legacy-app \
  -l docktunnel.enable=true \
  -l docktunnel.traefik.enable=true \
  -l traefik.http.routers.app.rule=Host\('legacy.example.com'\) \
  -l traefik.http.services.app.loadbalancer.server.port=8080 \
  -l traefik.http.services.app.loadbalancer.server.scheme=http \
  legacy-app:latest
```

### 示例 4: 自签名证书 + gRPC 服务

```bash
docker run -d \
  --name=grpc-service \
  -l docktunnel.enable=true \
  -l docktunnel.grpc.hostname=grpc.example.com \
  -l docktunnel.grpc.service=https://localhost:9090 \
  -l docktunnel.grpc.originRequest.noTLSVerify=true \
  -l docktunnel.grpc.originRequest.http2Origin=true \
  -l docktunnel.grpc.originRequest.originServerName=grpc.example.com \
  grpc-service:latest
```

### 示例 5: 延时清理策略

```bash
docker run -d \
  --name=temp-service \
  -l docktunnel.enable=true \
  -l docktunnel.temp.hostname=temp.example.com \
  -l docktunnel.temp.service=http://localhost:8080 \
  -l docktunnel.temp.delete_retention=30m \
  temp-service:latest
```

**清理流程**：
1. 容器停止后，配置保留 30 分钟
2. 全局 GC 任务每分钟扫描一次
3. 超过 30 分钟后，自动删除配置和 DNS 记录

### 示例 6: 永久保留策略

```bash
docker run -d \
  --name=prod-service \
  -l docktunnel.enable=true \
  -l docktunnel.prod.hostname=prod.example.com \
  -l docktunnel.prod.service=http://localhost:8080 \
  -l docktunnel.prod.delete_retention=forever \
  prod-service:latest
```

**行为**：容器停止后，配置永久保留（GC 扫描时会忽略）

### 示例 7: Cloudflare Access 零信任保护

```bash
docker run -d \
  --name=internal-tool \
  -l docktunnel.enable=true \
  -l docktunnel.tool.hostname=internal.example.com \
  -l docktunnel.tool.service=http://localhost:3000 \
  -l docktunnel.tool.access.required=true \
  -l docktunnel.tool.access.team_name=engineering \
  -l docktunnel.tool.access.aud_tag=a4b3c2d1 \
  internal-tool:latest
```

**行为**：访问时必须通过 Cloudflare Access 验证

### 示例 8: 完整配置（所有选项）

覆盖服务、TLS、超时、连接池、HTTP、代理、Access 与清理策略的全部选项：

```bash
docker run -d \
  --name=full-config \
  -l docktunnel.enable=true \
  -l docktunnel.full.hostname=full.example.com \
  -l docktunnel.full.service=http://172.17.0.2:8080 \
  -l docktunnel.full.path=/api \
  -l docktunnel.full.originRequest.noTLSVerify=true \
  -l docktunnel.full.originRequest.originServerName=origin.example.com \
  -l docktunnel.full.originRequest.caPool=/etc/ssl/certs/ca.pem \
  -l docktunnel.full.originRequest.connectTimeout=30s \
  -l docktunnel.full.originRequest.tlsTimeout=10s \
  -l docktunnel.full.originRequest.tcpKeepAlive=30s \
  -l docktunnel.full.originRequest.keepAliveConnections=100 \
  -l docktunnel.full.originRequest.keepAliveTimeout=90s \
  -l docktunnel.full.originRequest.httpHostHeader=full.example.com \
  -l docktunnel.full.originRequest.http2Origin=false \
  -l docktunnel.full.originRequest.disableChunkedEncoding=false \
  -l docktunnel.full.originRequest.proxyType=socks \
  -l docktunnel.full.originRequest.noHappyEyeballs=false \
  -l docktunnel.full.access.required=true \
  -l docktunnel.full.access.team_name=myteam \
  -l docktunnel.full.access.aud_tag=abc123 \
  -l docktunnel.full.delete_retention=1h \
  full-config:latest
```

## 高级功能

### 容器抖动检测

当容器在短时间内频繁重启时，DockTunnel 会：

1. **检测抖动**: 在 `flappingWindow`（默认 60s）内重启次数超过 `flappingThreshold`（默认 5 次）
2. **触发冷却**: 标记容器为不稳定，进入冷却期
3. **指数退避**: 冷却期从 `coolingPeriod`（默认 300s）开始，最大 `maxCoolingPeriod`（默认 1800s）
4. **恢复同步**: 冷却期结束后，恢复正常同步

### 事件防抖

- **防抖延迟**: `debounceDuration`（默认 2s）
- **行为**: 在 2 秒内的多个容器事件会被合并为一次同步操作
- **好处**: 减少 API 调用，避免配置抖动

### 状态同步流程

```
容器启动 → 解析标签 → 校验规则 → 更新内存状态 → 批量同步 Cloudflare
    ↓
检测到变化 → 计算配置差异 → 应用更新 → 记录日志
```

### 规则校验

- **hostname 唯一性**: 全局检查，防止域名冲突
- **service name 唯一性**: 容器内服务名唯一性检查
- **必填字段**: 确保 `hostname` 和 `service` 配置完整

## 配置优先级

### 端口检测优先级

1. `docktunnel.<name>.port` 标签
2. `traefik.http.services.<name>.loadbalancer.server.port` 标签
3. 容器首个 ExposedPort
4. 默认端口 `80`

### 协议检测优先级

1. `docktunnel.<name>.service` 标签中包含的协议（如 `https://`）
2. `docktunnel.<name>.scheme` 标签
3. `traefik.http.services.<name>.loadbalancer.server.scheme` 标签
4. 默认协议 `http`

### 配置合并优先级

1. **局部规则**: `docktunnel.<name>.*` 标签（最高优先级）
2. **Traefik 兼容**: `traefik.http.*` 标签
3. **全局规则**: `docktunnel.*` 标签（不含服务名）
4. **系统默认**: 配置文件中的默认值

## 故障排除

### 调试模式

```bash
# 启用 Debug 日志
export DOCKTUNNEL_LOG_LEVEL=debug
./docktunnel

# 或在配置文件中
log:
  level: debug
  format: json
```

### 常见问题

#### 1. 无法连接到 Docker daemon

**症状**: `Error: Cannot connect to the Docker daemon`

**解决方案**:
- 确保 Docker socket 已挂载：`-v /var/run/docker.sock:/var/run/docker.sock`
- 检查文件权限：`ls -l /var/run/docker.sock`
- 确保 DockTunnel 运行在 Docker 容器外或正确挂载 socket

#### 2. Cloudflare API 错误

**症状**: `Error: Cloudflare API request failed`

**解决方案**:
- 验证 `accountId` 和 `apiToken` 正确性
- 确认 API Token 权限：
  - Account: Read/Write
  - Zone: Read/Write
  - Tunnel: Read/Write
- 检查网络连接和防火墙设置

#### 3. 容器标签未生效

**症状**: 容器启动后无反应

**解决方案**:
- 确认设置了 `docktunnel.enable=true`
- 检查日志：`docker logs docktunnel`
- 验证标签格式：`docker inspect <container> --format='{{json .Config.Labels}}'`
- 确保 hostname 和 service 配置正确

#### 4. DNS 记录未创建

**症状**: 隧道配置成功但无法访问域名

**解决方案**:
- 检查域名 DNS 记录是否在 Cloudflare 控制台显示
- 验证 Zone ID 正确性
- 确认域名已添加到 Cloudflare 账户
- 检查 DNS 传播：`dig example.com`

#### 5. 服务无法访问

**症状**: DNS 解析正确但服务无法访问

**解决方案**:
- 验证容器服务正常运行：`docker exec <container> curl localhost:8080`
- 检查容器 IP 地址：`docker inspect <container> --format='{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}'`
- 确认网络模式：
  - Bridge 模式：使用容器 IP
  - Host 模式：使用 `localhost`
- 检查防火墙规则

#### 6. 配置频繁更新/回退

**症状**: Cloudflare 配置频繁变化

**解决方案**:
- 检查容器是否在频繁重启（容器抖动）
- 调整 `flappingThreshold` 和 `coolingPeriod`
- 检查事件防抖设置 `debounceDuration`
- 查看日志中的冷却期提示

### 日志分析

#### Debug 日志示例

```json
{
  "level": "DEBUG",
  "msg": "Container started",
  "container_id": "abc123",
  "container_name": "web-app",
  "labels": {
    "docktunnel.enable": "true",
    "docktunnel.web.hostname": "app.example.com"
  }
}
```

#### 错误日志示例

```json
{
  "level": "ERROR",
  "msg": "Failed to update tunnel configuration",
  "error": "rate limit exceeded",
  "retry_after": "60s"
}
```

## 开发指南

### 项目结构

```
DockTunnel/
├── cmd/
│   └── docktunnel/          # 主程序入口
│       ├── main.go
│       └── main_test.go
├── internal/
│   ├── cloudflareManager/   # Cloudflare API 管理（隧道/DNS/zone）
│   │   ├── tunnel.go
│   │   └── tunnel_test.go
│   ├── config/              # 配置管理（文件 + 环境变量 + 默认值）
│   │   ├── config.go
│   │   └── config_test.go
│   ├── controller/          # 核心业务逻辑（状态机/对账/GC/补偿）
│   │   ├── controller.go    # 编排器
│   │   ├── dispatcher.go    # 事件分发
│   │   ├── syncer.go        # 状态同步
│   │   ├── reconciler.go    # 周期对账
│   │   ├── handlers.go      # 容器生命周期处理
│   │   ├── health.go        # 容器抖动检测
│   │   ├── gc.go            # 过期清理
│   │   ├── compensation.go  # 失败补偿
│   │   ├── diagnostics.go   # 诊断数据
│   │   ├── sync_worker.go   # 串行化 Cloudflare 写入
│   │   └── validator.go     # 规则校验
│   ├── docker/              # Docker 监控（事件监听/重连/扫描）
│   │   ├── monitor.go
│   │   └── monitor_test.go
│   ├── events/              # 事件定义
│   │   └── event.go
│   ├── label/               # 标签解析（docktunnel.* + Traefik 兼容）
│   │   ├── parser.go
│   │   ├── builder.go
│   │   ├── docktunnel.go
│   │   ├── traefik.go
│   │   └── types.go
│   ├── state/               # 状态管理（持久化/状态机/补偿队列）
│   │   ├── manager.go
│   │   ├── persistence.go
│   │   ├── transition.go
│   │   └── compensation.go
│   ├── instance/            # 单实例锁
│   ├── metrics/             # Prometheus 指标
│   ├── diagnostics/         # /debug/state 诊断
│   ├── server/              # HTTP 服务（/metrics /healthz /debug/state）
│   └── logger/              # 结构化日志
├── pkg/types/               # 公共类型（错误分类/Tunnel 类型）
├── tests/                   # 集成测试 + mock
├── config.yaml              # 配置文件示例
├── go.mod
├── go.sum
├── Makefile
├── Dockerfile
├── docker-compose.yml
└── README.md
```

### 构建

```bash
# 格式化代码
make fmt

# 下载依赖
make deps

# 构建
make build

# 运行测试
make test

# 测试覆盖率
make test-coverage

# 清理
make clean
```

### 测试

```bash
# 运行所有测试
go test ./...

# 运行特定包测试
go test ./internal/controller

# 显示详细输出
go test -v ./internal/controller

# 测试覆盖率
go test -cover ./...
go test -coverprofile=coverage.out ./...
go tool cover -html=coverage.out
```

### 代码风格

- 遵循 [Effective Go](https://golang.org/doc/effective_go) 指南
- 使用 `gofmt` 格式化代码
- 编写单元测试（覆盖率目标：80%+）
- 添加文档注释到导出的类型、函数、常量

### 提交代码

1. Fork 项目
2. 创建功能分支：`git checkout -b feature/amazing-feature`
3. 提交更改：`git commit -m 'Add amazing feature'`
4. 推送分支：`git push origin feature/amazing-feature`
5. 提交 Pull Request

## 部署

### 作为系统服务（systemd）

创建 `/etc/systemd/system/docktunnel.service`：

```ini
[Unit]
Description=DockTunnel - Cloudflare Tunnel Docker Controller
After=docker.service
Requires=docker.service

[Service]
Type=simple
# 建议使用非 root 用户运行（最小权限原则）
User=docktunnel
ExecStart=/usr/local/bin/docktunnel
Restart=always
RestartSec=10
# CONFIG_PATH 指定配置文件的完整路径（已获 config 加载器支持），
# 适用于 systemd 部署（不会去当前目录找 config.yaml）
Environment=CONFIG_PATH=/etc/docktunnel/config.yaml

# 安全加固
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
# 状态持久化目录：ProtectSystem=strict 下 /var/lib 只读，必须显式放行
# StateDirectory 会创建 /var/lib/docktunnel 并把属主设为 User
StateDirectory=docktunnel
ReadWritePaths=/var/lib/docktunnel

[Install]
WantedBy=multi-user.target
```

启用并启动服务：

```bash
sudo systemctl daemon-reload
sudo systemctl enable docktunnel
sudo systemctl start docktunnel
sudo systemctl status docktunnel
```

### Docker Compose 部署

```yaml
services:
  docktunnel:
    image: ghcr.io/k997/docktunnel:latest
    container_name: docktunnel
    restart: unless-stopped
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock:ro
      - docktunnel-state:/var/lib/docktunnel   # 状态持久化，容器重建不丢失
      - ./config.yaml:/etc/docktunnel/config.yaml:ro
    environment:
      - DOCKTUNNEL_LOG_LEVEL=info
    healthcheck:
      test: ["CMD", "wget", "-q", "-O", "-", "http://127.0.0.1:9100/healthz"]
      interval: 30s
      timeout: 5s
      retries: 3
      start_period: 10s

volumes:
  docktunnel-state:
```

仓库内附一份可直接使用的 [docker-compose.yml](docker-compose.yml)。

### Kubernetes 部署（DaemonSet）

> **示例未验证**，生产使用前建议先在测试集群验证（标签解析依赖 Docker
> socket，DaemonSet 模式需确认 `hostPath` 挂载与节点权限）。

```yaml
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: docktunnel
  namespace: kube-system
spec:
  selector:
    matchLabels:
      app: docktunnel
  template:
    metadata:
      labels:
        app: docktunnel
    spec:
      containers:
      - name: docktunnel
        image: ghcr.io/k997/docktunnel:latest
        resources:
          limits:
            memory: "128Mi"
            cpu: "500m"
        volumeMounts:
        - name: docker-socket
          mountPath: /var/run/docker.sock
          readOnly: true
        - name: config
          mountPath: /etc/docktunnel
          readOnly: true
        env:
        - name: DOCKTUNNEL_LOG_LEVEL
          value: "info"
      volumes:
      - name: docker-socket
        hostPath:
          path: /var/run/docker.sock
      - name: config
        configMap:
          name: docktunnel-config
```

## 性能优化

### API 速率限制

默认配置：
- 速率限制：4 请求/秒（Cloudflare 官方 API 限额约 1200 请求/5 分钟，≈4 RPS；
  默认值即贴近该限额，超过会触发 429）
- 最大重试：3 次
- 重试延迟：1s（指数增长，最大 30s）

调整建议：
- 大规模部署（100+ 容器）：可提高到 10-20 请求/秒（注意仍可能触发
  Cloudflare 官方 429 限流，需结合实际用量）
- 小规模部署（< 20 容器）：保持默认 4 请求/秒即可

### 事件防抖优化

- **默认值**: 2 秒
- **高动态环境**: 降低到 500ms - 1s
- **稳定环境**: 提高到 5s - 10s

### 冷却期调整

- **默认值**: 300s（5 分钟）
- **生产环境**: 提高到 600s - 900s
- **开发环境**: 降低到 60s - 120s

## 安全建议

### API Token 安全

- 使用最小权限原则
- 定期轮换 API Token
- 不要将 Token 提交到版本控制
- 使用环境变量或密钥管理工具（如 HashiCorp Vault）

### Docker Socket 安全

- 以只读方式挂载：`/var/run/docker.sock:ro`
- 限制容器权限（不使用 `--privileged`）
- 使用专用用户运行

### 网络隔离

- 在专用网络中运行 DockTunnel
- 限制出站连接（仅允许 Cloudflare API）
- 使用防火墙规则限制访问

## 常见应用场景

### 场景 1: 本地开发环境

将本地开发服务暴露到互联网：

```bash
docker run -d \
  --name=dev-app \
  -p 3000:3000 \
  -l docktunnel.enable=true \
  -l docktunnel.dev.hostname=dev.example.com \
  -l docktunnel.dev.service=http://localhost:3000 \
  my-dev-app:latest
```

### 场景 2: 微服务架构

管理多个微服务的外部访问：

```bash
# 用户服务
docker run -d --name=user-service \
  -l docktunnel.enable=true \
  -l docktunnel.users.hostname=api.example.com \
  -l docktunnel.users.path=/users \
  -l docktunnel.users.service=http://user-service:8001 \
  user-service:latest

# 订单服务
docker run -d --name=order-service \
  -l docktunnel.enable=true \
  -l docktunnel.orders.hostname=api.example.com \
  -l docktunnel.orders.path=/orders \
  -l docktunnel.orders.service=http://order-service:8002 \
  order-service:latest
```

### 场景 3: CI/CD 流水线

临时暴露测试环境：

```bash
docker run -d \
  --name=staging-$BUILD_NUMBER \
  -l docktunnel.enable=true \
  -l docktunnel.staging.hostname=staging-$BUILD_NUMBER.example.com \
  -l docktunnel.staging.service=http://localhost:8080 \
  -l docktunnel.staging.delete_retention=1h \
  staging-app:latest
```

### 场景 4: Traefik 迁移

从 Traefik 迁移到 Cloudflare Tunnel（需显式开启 Traefik 标签解析）：

```bash
docker run -d \
  --name=legacy-app \
  -l docktunnel.enable=true \
  -l docktunnel.traefik.enable=true \
  -l traefik.http.routers.app.rule=Host\('app.example.com'\) \
  -l traefik.http.services.app.loadbalancer.server.port=8080 \
  legacy-app:latest
```

## 监控与日志

### 诊断与监控端点

诊断/指标 HTTP 服务默认绑定 `127.0.0.1:9100`，提供三个端点：

| 端点 | 说明 |
|------|------|
| `/healthz` | 存活探针（返回 200；Docker HEALTHCHECK 与 compose healthcheck 都打这里） |
| `/metrics` | Prometheus 指标 |
| `/debug/state` | 当前期望状态快照（**泄露全部 hostname 与 service URL**） |

安全说明：
- 默认只监听回环地址（`127.0.0.1`），无需额外保护；
- 若将 `server.bindAddr` 改为非回环地址（如 `0.0.0.0`），**必须**配置
  `server.debugToken`（对应环境变量 `DOCKTUNNEL_SERVER_DEBUG_TOKEN`），
  否则启动校验会拒绝，`/debug/state` 也需要 Bearer token 才能访问。

### 日志输出示例

```json
{"level":"INFO","msg":"DockTunnel starting","version":"1.0.0"}
{"level":"INFO","msg":"Connected to Docker daemon"}
{"level":"INFO","msg":"Connected to Cloudflare API","account_id":"xxx"}
{"level":"DEBUG","msg":"Container event received","action":"start","container_id":"abc123"}
{"level":"INFO","msg":"Processing container","container_name":"web-app","services":["web"]}
{"level":"INFO","msg":"Updating tunnel configuration","tunnel_id":"xxx","rules_count":5}
{"level":"INFO","msg":"DNS records updated","created":1,"deleted":0}
{"level":"INFO","msg":"Sync completed","duration":1.234s}
```

### 日志聚合

推荐使用以下工具聚合日志：
- **ELK Stack**: Elasticsearch + Logstash + Kibana
- **Loki**: Grafana Loki（轻量级）
- **Fluentd**: Fluentd + Elasticsearch
- **Cloud Logging**: 云服务商日志服务

## 贡献指南

欢迎贡献！请遵循以下步骤：

1. Fork 项目
2. 创建功能分支：`git checkout -b feature/amazing-feature`
3. 编写测试：`go test ./...`
4. 提交代码：`git commit -m 'Add amazing feature'`
5. 推送分支：`git push origin feature/amazing-feature`
6. 提交 Pull Request

### 代码审查标准

- 遵循 Go 代码规范
- 测试覆盖率 > 80%
- 添加文档注释
- 通过所有测试

## 许可证

本项目采用 **MIT 许可证**。详情请见 [LICENSE](LICENSE) 文件。

第三方代码的许可声明见 [THIRD-PARTY-NOTICES.md](THIRD-PARTY-NOTICES.md)。

## 致谢

- [Cloudflare](https://www.cloudflare.com/) - 提供 Tunnel 服务和 API
- [Cloudflare Go SDK](https://github.com/cloudflare/cloudflare-go) - 官方 Go SDK
- [Docker](https://www.docker.com/) - 容器技术
- [Traefik](https://traefik.io/) - 灵感的来源（标签兼容性设计）

## 联系方式

- **问题反馈**: [GitHub Issues](https://github.com/k997/docktunnel/issues)
- **功能建议**: [GitHub Discussions](https://github.com/k997/docktunnel/discussions)

---

**注意**: 本项目仍在活跃开发中，API 可能发生变化。建议在生产环境使用前进行充分测试。

**Star 🌟 这个项目**: 如果你觉得 DockTunnel 有帮助，请给我们一个 Star！
