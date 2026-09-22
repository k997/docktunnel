# Cloudflare Tunnel 完整配置参考手册

> 基于Cloudflare官方文档整理，涵盖cloudflared所有可配置字段。
>
> 官方文档：https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/

---

## 目录

- [1. 概述](#1-概述)
- [2. 隧道运行参数](#2-隧道运行参数)
- [3. 入口规则 (Ingress Rules)](#3-入口规则-ingress-rules)
- [4. OriginRequest 配置](#4-originrequest-配置)
  - [4.1 TLS 设置](#41-tls-设置)
  - [4.2 连接设置](#42-连接设置)
  - [4.3 HTTP 设置](#43-http-设置)
  - [4.4 代理设置](#44-代理设置)
  - [4.5 Access 设置](#45-access-设置)
- [5. 协议与传输](#5-协议与传输)
- [6. DNS 配置](#6-dns-配置)
- [7. 可观测性](#7-可观测性)
- [8. 服务管理](#8-服务管理)
- [9. 高级配置](#9-高级配置)
- [10. CLI 子命令参考](#10-cli-子命令参考)
- [11. 环境变量映射](#11-环境变量映射)
- [12. 完整配置示例](#12-完整配置示例)
- [13. 快速参考表](#13-快速参考表)

---

## 1. 概述

Cloudflare Tunnel（通过 `cloudflared` 运行）在基础设施和Cloudflare全球网络之间创建加密连接。配置方式有三种：

| 方式 | 说明 |
|------|------|
| **配置文件 (YAML)** | 本地管理隧道的推荐方式，通常位于 `~/.cloudflared/config.yml` |
| **CLI 标志** | 命令行参数，优先级高于配置文件 |
| **环境变量** | 大多数CLI标志有对应的环境变量 |
| **Tunnel Token** | 远程管理隧道使用，通过Dashboard创建 |

### 配置文件基本结构

```yaml
tunnel: <TUNNEL_UUID>
credentials-file: /path/to/credentials.json

protocol: auto
edge-ip-version: 4
grace-period: 30s
retries: 5
loglevel: info

originRequest:
  connectTimeout: 30s
  # ... 全局默认originRequest配置

ingress:
  - hostname: app.example.com
    service: http://localhost:8080
    originRequest:
      # ... 每条规则的覆盖配置
  - service: http_status:404  # 必须以catch-all结尾
```

---

## 2. 隧道运行参数

以下参数为 `cloudflared tunnel run` 的配置项，可在配置文件顶层或通过CLI标志设置。

### `tunnel`

| 属性 | 值 |
|------|-----|
| **类型** | string |
| **格式** | UUID 或隧道名称 |
| **必需** | 是（本地管理隧道） |
| **说明** | 要运行的隧道标识符 |

```yaml
tunnel: 6ff42ae2-765d-4adf-9791-63af9f60a2dd
# 或
tunnel: my-tunnel-name
```

### `credentials-file`

| 属性 | 值 |
|------|-----|
| **类型** | string (文件路径) |
| **必需** | 是（本地管理隧道） |
| **说明** | 隧道凭证JSON文件路径，在执行 `cloudflared tunnel create` 时生成 |

```yaml
credentials-file: /home/user/.cloudflared/6ff42ae2-765d-4adf-9791-63af9f60a2dd.json
```

### `token`

| 属性 | 值 |
|------|-----|
| **类型** | string |
| **环境变量** | `TUNNEL_TOKEN` |
| **说明** | 远程管理隧道使用的Token（替代 credentials-file） |

```yaml
token: <tunnel-token-from-dashboard>
```

### `token-file`

| 属性 | 值 |
|------|-----|
| **类型** | string (文件路径) |
| **环境变量** | `TUNNEL_TOKEN_FILE` |
| **最低版本** | 2025.4.0 |
| **说明** | 包含Tunnel Token的文件路径 |

```yaml
token-file: /path/to/token.txt
```

### `protocol`

| 属性 | 值 |
|------|-----|
| **类型** | string (枚举) |
| **有效值** | `auto`, `quic`, `http2` |
| **默认** | `auto` |
| **CLI** | `--protocol` |
| **环境变量** | `TUNNEL_TRANSPORT_PROTOCOL` |
| **说明** | cloudflared与Cloudflare边缘之间的传输协议 |

- `auto` — 自动选择QUIC，如果UDP不可用则回退到HTTP/2
- `quic` — 使用QUIC协议，消除HTTP/2 over TCP的队头阻塞
- `http2` — 使用HTTP/2 over TCP，兼容性更好

```yaml
protocol: quic
```

### `edge-ip-version`

| 属性 | 值 |
|------|-----|
| **类型** | string (枚举) |
| **有效值** | `auto`, `4`, `6` |
| **默认** | `4` |
| **CLI** | `--edge-ip-version` |
| **环境变量** | `TUNNEL_EDGE_IP_VERSION` |
| **说明** | 连接Cloudflare边缘时使用的IP版本 |

- `4` — 仅IPv4
- `6` — 仅IPv6
- `auto` — 由操作系统决定，双栈环境下会同时使用两种地址集进行回退

```yaml
edge-ip-version: auto
```

### `edge-bind-address`

| 属性 | 值 |
|------|-----|
| **类型** | string (IP地址) |
| **默认** | 无（由操作系统决定） |
| **CLI** | `--edge-bind-address` |
| **环境变量** | `TUNNEL_EDGE_BIND_ADDRESS` |
| **说明** | 指定cloudflared与Cloudflare边缘连接的源IP地址。在有多个网络接口时很有用。此设置的IP版本会覆盖 `edge-ip-version` |

```yaml
edge-bind-address: "203.0.113.50"
```

### `grace-period`

| 属性 | 值 |
|------|-----|
| **类型** | duration |
| **默认** | `30s` |
| **CLI** | `--grace-period` |
| **环境变量** | `TUNNEL_GRACE_PERIOD` |
| **说明** | 优雅关闭的等待时间。收到SIGINT/SIGTERM后，停止接受新请求，等待进行中的请求完成，超时后强制关闭。收到第二个SIGTERM/SIGINT也会立即关闭 |

```yaml
grace-period: 45s
```

### `retries`

| 属性 | 值 |
|------|-----|
| **类型** | integer |
| **默认** | `5` |
| **CLI** | `--retries` |
| **环境变量** | `TUNNEL_RETRIES` |
| **说明** | 与边缘连接失败时的最大重试次数，使用指数退避（1s, 2s, 4s, 8s, 16s） |

```yaml
retries: 5
```

### `autoupdate-freq`

| 属性 | 值 |
|------|-----|
| **类型** | duration |
| **默认** | `24h` |
| **CLI** | `--autoupdate-freq` |
| **说明** | 检查cloudflared更新的频率。发现更新时，启动新进程连接Cloudflare，连接成功后旧进程优雅关闭 |

```yaml
autoupdate-freq: 24h
```

### `no-autoupdate`

| 属性 | 值 |
|------|-----|
| **类型** | boolean |
| **默认** | `false` |
| **CLI** | `--no-autoupdate` |
| **环境变量** | `NO_AUTOUPDATE` |
| **说明** | 完全禁用自动更新 |

```yaml
no-autoupdate: true
```

### `post-quantum`

| 属性 | 值 |
|------|-----|
| **类型** | boolean |
| **默认** | `false` |
| **CLI** | `--post-quantum` |
| **环境变量** | `TUNNEL_POST_QUANTUM` |
| **说明** | 启用后量子密码学。需要Go 1.22+，**不兼容** `--protocol http2` |

```yaml
post-quantum: true
```

### `region`

| 属性 | 值 |
|------|-----|
| **类型** | string |
| **有效值** | `""` (全球), `"us"` |
| **默认** | `""` |
| **CLI** | `--region` |
| **环境变量** | `TUNNEL_REGION` |
| **说明** | 限制连接到特定区域的边缘节点。`us` 用于FedRAMP High合规 |

```yaml
region: us
```

### `tag`

| 属性 | 值 |
|------|-----|
| **类型** | map (key=value) |
| **CLI** | `--tag` (可重复) |
| **环境变量** | `TUNNEL_TAG` |
| **说明** | 为隧道连接添加自定义标签，用于识别和管理 |

```yaml
tag:
  env: production
  team: platform
```

### `origincert`

| 属性 | 值 |
|------|-----|
| **类型** | string (文件路径) |
| **默认** | `~/.cloudflared/cert.pem` |
| **CLI** | `--origincert` |
| **环境变量** | `TUNNEL_ORIGIN_CERT` |
| **说明** | 账户证书路径（仅本地管理隧道使用） |

```yaml
origincert: /home/user/.cloudflared/cert.pem
```

### `pidfile`

| 属性 | 值 |
|------|-----|
| **类型** | string (文件路径) |
| **CLI** | `--pidfile` |
| **环境变量** | `TUNNEL_PIDFILE` |
| **说明** | 首次成功连接后将PID写入指定文件 |

```yaml
pidfile: /run/cloudflared.pid
```

### `dns-resolver-addrs`

| 属性 | 值 |
|------|-----|
| **类型** | string (IP:PORT，可重复) |
| **CLI** | `--dns-resolver-addrs` |
| **环境变量** | `TUNNEL_DNS_RESOLVER_ADDRS` |
| **最低版本** | 2025.7.0 |
| **说明** | 自定义DNS解析器地址 |

```yaml
dns-resolver-addrs:
  - 8.8.8.8:53
  - 1.1.1.1:53
```

---

## 3. 入口规则 (Ingress Rules)

入口规则定义了外部请求如何路由到本地服务。**配置文件必须包含至少一条ingress规则，且最后一条必须是catch-all规则**（没有hostname的规则）。

### 规则字段

| 字段 | 类型 | 必需 | 说明 |
|------|------|------|------|
| `hostname` | string | 否 | 匹配的域名，支持通配符如 `*.example.com` |
| `service` | string | 是 | 本地服务的URL |
| `path` | string | 否 | 匹配的路径，支持正则表达式如 `/api/.*` |
| `originRequest` | object | 否 | 该规则的originRequest覆盖配置 |

### service 支持的协议

| 格式 | 说明 |
|------|------|
| `http://host:port` | HTTP协议 |
| `https://host:port` | HTTPS协议 |
| `tcp://host:port` | TCP透传 |
| `ssh://host:port` | SSH协议 |
| `rdp://host:port` | RDP协议 |
| `unix:/path/to/socket` | Unix域套接字 |
| `unix+tls:/path/to/socket` | TLS加密的Unix域套接字 |
| `hello` | Cloudflare内置测试页面 |
| `http_status:NNN` | 直接返回HTTP状态码（用于catch-all） |

### 配置示例

```yaml
ingress:
  # 精确域名匹配
  - hostname: app.example.com
    service: http://localhost:8080

  # 通配符域名匹配
  - hostname: "*.example.com"
    service: http://localhost:8080

  # 带路径匹配
  - hostname: api.example.com
    path: /v1/(.*)
    service: http://localhost:3000

  # 带originRequest覆盖
  - hostname: secure.example.com
    service: https://localhost:443
    originRequest:
      noTLSVerify: true
      http2Origin: true

  # Catch-all规则（必须放在最后）
  - service: http_status:404
```

### originRequest 作用域

`originRequest` 可以配置在两个层级：

1. **全局默认** — 配置文件顶层，所有ingress规则继承
2. **每条规则** — ingress规则内部，覆盖全局默认值

```yaml
# 全局默认
originRequest:
  connectTimeout: 30s
  noTLSVerify: false

ingress:
  - hostname: app.example.com
    service: http://localhost:8080
    # 使用全局默认

  - hostname: slow-api.example.com
    service: http://localhost:3000
    originRequest:
      connectTimeout: 120s  # 覆盖全局默认
```

---

## 4. OriginRequest 配置

### 4.1 TLS 设置

#### `originServerName`

| 属性 | 值 |
|------|-----|
| **类型** | string |
| **默认** | `""`（使用service URL中的hostname） |
| **说明** | TLS证书验证时使用的预期主机名。为空时使用service URL的hostname |

```yaml
originRequest:
  originServerName: api.internal
```

#### `caPool`

| 属性 | 值 |
|------|-----|
| **类型** | string (文件路径) |
| **默认** | `""` |
| **说明** | CA证书池文件路径（.pem/.crt），用于验证源站证书。仅在证书不由公共CA签发时需要 |

```yaml
originRequest:
  caPool: /etc/ssl/certs/my-ca.pem
```

#### `noTLSVerify`

| 属性 | 值 |
|------|-----|
| **类型** | boolean |
| **默认** | `false` |
| **说明** | 禁用TLS证书验证。为true时接受源站的任何证书。适用于自签名证书环境，但会降低安全性 |

```yaml
originRequest:
  noTLSVerify: true
```

#### `tlsTimeout`

| 属性 | 值 |
|------|-----|
| **类型** | duration |
| **默认** | `10s` |
| **说明** | TLS握手超时时间 |

```yaml
originRequest:
  tlsTimeout: 15s
```

#### `http2Origin`

| 属性 | 值 |
|------|-----|
| **类型** | boolean |
| **默认** | `false` |
| **说明** | 启用HTTP/2连接到源站（需要源站启用SSL）。与 `noTLSVerify` 配合可用于自签名证书场景 |

```yaml
originRequest:
  http2Origin: true
```

#### `matchSNItoHost`

| 属性 | 值 |
|------|-----|
| **类型** | boolean |
| **默认** | `false` |
| **说明** | 自动将SNI设置为匹配请求的Host头 |

```yaml
originRequest:
  matchSNItoHost: true
```

### 4.2 连接设置

#### `connectTimeout`

| 属性 | 值 |
|------|-----|
| **类型** | duration |
| **默认** | `30s` |
| **说明** | 建立到源站的新TCP连接的超时时间（不包括TLS握手） |

```yaml
originRequest:
  connectTimeout: 60s
```

#### `noHappyEyeballs`

| 属性 | 值 |
|------|-----|
| **类型** | boolean |
| **默认** | `false` |
| **说明** | 禁用Happy Eyeballs算法（RFC 6555），该算法用于IPv4/IPv6双栈回退 |

```yaml
originRequest:
  noHappyEyeballs: true
```

#### `keepAliveConnections`

| 属性 | 值 |
|------|-----|
| **类型** | integer |
| **默认** | `100` |
| **说明** | 到源站的最大空闲keep-alive连接数。不限制总并发连接数 |

```yaml
originRequest:
  keepAliveConnections: 200
```

#### `keepAliveTimeout`

| 属性 | 值 |
|------|-----|
| **类型** | duration |
| **默认** | `1m30s` |
| **说明** | 空闲keep-alive连接的超时时间，超时后连接被丢弃 |

```yaml
originRequest:
  keepAliveTimeout: 2m
```

#### `tcpKeepAlive`

| 属性 | 值 |
|------|-----|
| **类型** | duration |
| **默认** | `30s` |
| **说明** | Cloudflare与源站之间TCP keep-alive数据包的发送间隔 |

```yaml
originRequest:
  tcpKeepAlive: 30s
```

### 4.3 HTTP 设置

#### `httpHostHeader`

| 属性 | 值 |
|------|-----|
| **类型** | string |
| **默认** | `""` |
| **说明** | 覆盖发送到源站的HTTP Host头 |

```yaml
originRequest:
  httpHostHeader: internal-app.local
```

#### `disableChunkedEncoding`

| 属性 | 值 |
|------|-----|
| **类型** | boolean |
| **默认** | `false` |
| **说明** | 禁用分块传输编码。适用于WSGI服务器等不支持分块编码的源站 |

```yaml
originRequest:
  disableChunkedEncoding: true
```

### 4.4 代理设置

#### `proxyType`

| 属性 | 值 |
|------|-----|
| **类型** | string (枚举) |
| **有效值** | `""` (无代理), `"socks"` (SOCKS5) |
| **默认** | `""` |
| **说明** | 配置cloudflared启动的代理类型，用于将HTTP流量转换为TCP（用于SSH、RDP等协议） |

```yaml
originRequest:
  proxyType: socks
```

#### `proxyAddress`

| 属性 | 值 |
|------|-----|
| **类型** | string (IP地址) |
| **默认** | `127.0.0.1` |
| **说明** | 代理地址。仅适用于本地管理隧道 |

```yaml
originRequest:
  proxyAddress: 127.0.0.1
```

#### `proxyPort`

| 属性 | 值 |
|------|-----|
| **类型** | integer |
| **默认** | `0`（随机未使用端口） |
| **说明** | 代理端口。仅适用于本地管理隧道 |

```yaml
originRequest:
  proxyPort: 9050
```

### 4.5 Access 设置

#### `access`

| 属性 | 值 |
|------|-----|
| **类型** | object |
| **说明** | 配置cloudflared在将流量代理到源站之前验证Cloudflare Access JWT。为所有受保护hostname的L7请求添加 `Cf-Access-Jwt-Assertion` 请求头 |

```yaml
originRequest:
  access:
    required: true
    teamName: my-team-name
    audTag:
      - abc123def456
      - xyz789ghi012
```

##### `access.required`

| 属性 | 值 |
|------|-----|
| **类型** | boolean |
| **必需** | 是 |
| **说明** | 启用Access JWT验证 |

##### `access.teamName`

| 属性 | 值 |
|------|-----|
| **类型** | string |
| **必需** | 是 |
| **说明** | Cloudflare Zero Trust团队名称 |

##### `access.audTag`

| 属性 | 值 |
|------|-----|
| **类型** | list of strings |
| **必需** | 是 |
| **说明** | Access应用程序的受众标签（AUD Tag），可配置多个 |

---

## 5. 协议与传输

### QUIC vs HTTP/2

| 特性 | QUIC | HTTP/2 |
|------|------|--------|
| **传输层** | UDP | TCP |
| **队头阻塞** | 无 | 有 |
| **延迟** | 更低 | 较高 |
| **网络兼容性** | 需要UDP出站 | 仅需TCP出站 |
| **后量子密码** | 支持 (`--post-quantum`) | 不支持 |

### WARP 路由（私有网络）

通过 `warp-routing` 配置将私有IP/CIDR范围路由到隧道：

```yaml
warp-routing:
  enabled: true
```

管理命令：

```bash
# 添加IP路由
cloudflared tunnel route ip add 10.0.0.0/8 <tunnel-name>

# 查看路由表
cloudflared tunnel route ip show

# 删除路由
cloudflared tunnel route ip delete 10.0.0.0/8
```

虚拟网络支持（用于重叠IP空间隔离）：

```bash
# 创建虚拟网络
cloudflared tunnel vnet add -d production

# 列出虚拟网络
cloudflared tunnel vnet list

# 删除虚拟网络
cloudflared tunnel vnet delete <name|uuid>
```

---

## 6. DNS 配置

### 通过CLI管理DNS

```bash
# 为隧道创建DNS CNAME记录
cloudflared tunnel route dns <tunnel-name> app.example.com

# 覆盖已有DNS记录
cloudflared tunnel route dns -f <tunnel-name> app.example.com

# 添加到负载均衡器池
cloudflared tunnel route lb <tunnel-name> lb.example.com my-pool
```

DNS记录格式：`<TUNNEL_UUID>.cfargotunnel.com`（CNAME目标）

### DNS解析器自定义

```bash
cloudflared tunnel run --dns-resolver-addrs 8.8.8.8:53 --dns-resolver-addrs 1.1.1.1:53 <tunnel>
```

---

## 7. 可观测性

### `metrics`

| 属性 | 值 |
|------|-----|
| **类型** | string (IP:PORT) |
| **CLI** | `--metrics` |
| **环境变量** | `TUNNEL_METRICS` |
| **说明** | Prometheus指标端点地址 |

```yaml
metrics: 0.0.0.0:9100
```

### `loglevel`

| 属性 | 值 |
|------|-----|
| **类型** | string (枚举) |
| **有效值** | `debug`, `info`, `warn`, `error`, `fatal` |
| **默认** | `info` |
| **CLI** | `--loglevel` |
| **环境变量** | `TUNNEL_LOGLEVEL` |

```yaml
loglevel: debug
```

### `logfile`

| 属性 | 值 |
|------|-----|
| **类型** | string (文件路径) |
| **CLI** | `--logfile` |
| **环境变量** | `TUNNEL_LOGFILE` |

```yaml
logfile: /var/log/cloudflared.log
```

### 日志流

```bash
# 实时查看隧道日志
cloudflared tunnel tail <tunnel-uuid>
```

### 验证配置

```bash
# 验证配置文件中的ingress规则
cloudflared tunnel ingress validate

# 测试URL匹配哪条ingress规则
cloudflared tunnel ingress rule https://app.example.com/api
```

---

## 8. 服务管理

### 安装为系统服务

```bash
# 安装
cloudflared --config /path/to/config.yml service install

# 卸载
cloudflared service uninstall
```

平台行为：

| 平台 | 安装位置 |
|------|---------|
| Linux | `/etc/systemd/system/cloudflared.service` |
| macOS | `~/Library/LaunchAgents/com.cloudflare.cloudflared.plist` |
| Windows | Windows注册表服务项 |

### 高可用与副本

- 每个cloudflared实例建立4个到不同Cloudflare数据中心的连接
- 同一隧道可在多台主机上运行（副本），最多100个连接（25个副本）
- 流量路由到地理位置最近的副本
- 适用于：高可用、故障转移、零停机配置更新

### 负载均衡

对于需要智能流量分发的场景，使用Cloudflare Load Balancer：

1. 创建多个隧道（每个指向不同的源站）
2. 为每个隧道创建DNS记录
3. 创建负载均衡器，将隧道DNS记录作为源站
4. 支持：延迟路由、地理路由、健康检查、故障转移、随机/哈希/轮询分发

---

## 9. 高级配置

### FedRAMP High 合规

```yaml
region: us
```

限制所有连接仅通过美国FedRAMP合规的边缘节点。

### 防火墙配置

Cloudflare边缘端点：
- `region1.v2.argotunnel.com`
- `region2.v2.argotunnel.com`

两个端点都解析到多个IPv4和IPv6地址。QUIC使用UDP端口，HTTP/2使用TCP端口。

### 上游代理

`proxyType`、`proxyAddress`、`proxyPort` 控制的是cloudflared启动的**本地代理**（用于协议转换），不是cloudflared自身出站连接的上游代理。`HTTPS_PROXY` / `NO_PROXY` 环境变量对cloudflared到边缘的连接**不被官方支持**。

### 响应头

cloudflared本身**不提供**响应头配置。响应头通过Cloudflare边缘管理：

- **Transform Rules**（Rules > Transform Rules > Modify Response Header）
- **Page Rules / Configuration Rules**
- 源站自行设置的响应头会透传

---

## 10. CLI 子命令参考

| 命令 | 说明 |
|------|------|
| `cloudflared tunnel login` | 通过浏览器认证，保存origin证书 |
| `cloudflared tunnel list [-d]` | 列出隧道；`-d` 包含已删除的 |
| `cloudflared tunnel create <NAME>` | 创建新隧道 |
| `cloudflared tunnel run [flags] <UUID\|NAME>` | 运行隧道 |
| `cloudflared tunnel info <UUID\|NAME>` | 显示连接器详情 |
| `cloudflared tunnel cleanup [--connector-id ID] <UUID\|NAME>` | 删除指定或全部连接 |
| `cloudflared tunnel delete [-f] <UUID\|NAME>` | 删除隧道；`-f` 强制删除 |
| `cloudflared tunnel tail <UUID>` | 实时流式传输隧道日志 |
| `cloudflared tunnel route dns [-f] <UUID\|NAME> <hostname>` | 创建DNS CNAME |
| `cloudflared tunnel route lb <UUID\|NAME> <hostname> <pool>` | 添加到负载均衡器池 |
| `cloudflared tunnel route ip add [--vnet NAME] <IP/CIDR> <UUID\|NAME>` | 添加IP路由 |
| `cloudflared tunnel route ip show` | 显示IP路由表 |
| `cloudflared tunnel route ip delete <CIDR>` | 删除IP路由 |
| `cloudflared tunnel route ip get <IP/CIDR>` | 查询路由归属 |
| `cloudflared tunnel vnet add [-d] <NAME>` | 创建虚拟网络 |
| `cloudflared tunnel vnet delete <NAME\|UUID>` | 删除虚拟网络 |
| `cloudflared tunnel vnet list` | 列出虚拟网络 |
| `cloudflared tunnel ingress validate` | 验证配置文件 |
| `cloudflared tunnel ingress rule <URL>` | 测试URL匹配 |

---

## 11. 环境变量映射

| CLI 标志 | 环境变量 |
|----------|---------|
| `--autoupdate-freq` | （无直接映射） |
| `--config` | （无直接映射） |
| `--dns-resolver-addrs` | `TUNNEL_DNS_RESOLVER_ADDRS` |
| `--edge-bind-address` | `TUNNEL_EDGE_BIND_ADDRESS` |
| `--edge-ip-version` | `TUNNEL_EDGE_IP_VERSION` |
| `--grace-period` | `TUNNEL_GRACE_PERIOD` |
| `--logfile` | `TUNNEL_LOGFILE` |
| `--loglevel` | `TUNNEL_LOGLEVEL` |
| `--metrics` | `TUNNEL_METRICS` |
| `--no-autoupdate` | `NO_AUTOUPDATE` |
| `--origincert` | `TUNNEL_ORIGIN_CERT` |
| `--pidfile` | `TUNNEL_PIDFILE` |
| `--post-quantum` | `TUNNEL_POST_QUANTUM` |
| `--protocol` | `TUNNEL_TRANSPORT_PROTOCOL` |
| `--region` | `TUNNEL_REGION` |
| `--retries` | `TUNNEL_RETRIES` |
| `--tag` | `TUNNEL_TAG` |
| `--token` | `TUNNEL_TOKEN` |
| `--token-file` | `TUNNEL_TOKEN_FILE` |

---

## 12. 完整配置示例

```yaml
# 隧道标识
tunnel: 6ff42ae2-765d-4adf-9791-63af9f60a2dd
credentials-file: /home/user/.cloudflared/6ff42ae2-765d-4adf-9791-63af9f60a2dd.json

# 传输协议
protocol: auto
edge-ip-version: auto
edge-bind-address: ""

# 连接参数
grace-period: 30s
retries: 5

# 更新
autoupdate-freq: 24h
no-autoupdate: false

# 可观测性
loglevel: info
logfile: /var/log/cloudflared.log
metrics: 0.0.0.0:9100

# WARP路由
warp-routing:
  enabled: false

# 全局originRequest默认值
originRequest:
  connectTimeout: 30s
  tlsTimeout: 10s
  tcpKeepAlive: 30s
  keepAliveConnections: 100
  keepAliveTimeout: 1m30s
  noHappyEyeballs: false
  noTLSVerify: false
  http2Origin: false
  disableChunkedEncoding: false
  originServerName: ""
  caPool: ""
  httpHostHeader: ""
  matchSNItoHost: false
  proxyType: ""
  proxyAddress: 127.0.0.1
  proxyPort: 0

# 入口规则
ingress:
  - hostname: app.example.com
    service: http://localhost:8080
    originRequest:
      connectTimeout: 60s

  - hostname: api.example.com
    service: https://localhost:443
    path: /v1/(.*)
    originRequest:
      http2Origin: true
      originServerName: api.internal
      caPool: /etc/ssl/certs/internal-ca.pem

  - hostname: ssh.example.com
    service: ssh://localhost:22

  - hostname: secure.example.com
    service: https://localhost:8443
    originRequest:
      noTLSVerify: true
      access:
        required: true
        teamName: my-team
        audTag:
          - abc123def456

  - hostname: "*.example.com"
    service: http://localhost:8080

  # Catch-all（必需）
  - service: http_status:404
```

---

## 13. 快速参考表

### 隧道运行参数

| 参数 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `tunnel` | string | — | 隧道UUID或名称 |
| `credentials-file` | string | — | 凭证文件路径 |
| `token` | string | — | 远程管理Token |
| `token-file` | string | — | Token文件路径 |
| `protocol` | enum | `auto` | 传输协议 (`auto`/`quic`/`http2`) |
| `edge-ip-version` | enum | `4` | 边缘IP版本 (`auto`/`4`/`6`) |
| `edge-bind-address` | string | — | 边缘连接源IP |
| `grace-period` | duration | `30s` | 优雅关闭等待时间 |
| `retries` | int | `5` | 连接重试次数 |
| `autoupdate-freq` | duration | `24h` | 更新检查频率 |
| `no-autoupdate` | bool | `false` | 禁用自动更新 |
| `post-quantum` | bool | `false` | 启用后量子密码 |
| `region` | string | `""` | 边缘区域 (`us` for FedRAMP) |
| `tag` | map | — | 自定义标签 |
| `origincert` | string | `~/.cloudflared/cert.pem` | 账户证书路径 |
| `pidfile` | string | — | PID文件路径 |
| `dns-resolver-addrs` | list | — | 自定义DNS解析器 |
| `loglevel` | enum | `info` | 日志级别 |
| `logfile` | string | — | 日志文件路径 |
| `metrics` | string | — | Prometheus指标端点 |
| `warp-routing.enabled` | bool | `false` | WARP路由 |

### Ingress 规则字段

| 字段 | 类型 | 必需 | 说明 |
|------|------|------|------|
| `hostname` | string | 否 | 域名（支持通配符） |
| `service` | string | 是 | 源站服务URL |
| `path` | string | 否 | 路径匹配（支持正则） |
| `originRequest` | object | 否 | OriginRequest覆盖配置 |

### OriginRequest 字段

| 字段 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `connectTimeout` | duration | `30s` | TCP连接超时 |
| `tlsTimeout` | duration | `10s` | TLS握手超时 |
| `tcpKeepAlive` | duration | `30s` | TCP keep-alive间隔 |
| `keepAliveConnections` | int | `100` | 最大keep-alive连接数 |
| `keepAliveTimeout` | duration | `1m30s` | keep-alive超时 |
| `noHappyEyeballs` | bool | `false` | 禁用Happy Eyeballs |
| `noTLSVerify` | bool | `false` | 禁用TLS验证 |
| `originServerName` | string | `""` | TLS验证主机名 |
| `matchSNItoHost` | bool | `false` | SNI匹配Host头 |
| `caPool` | string | `""` | CA证书池路径 |
| `http2Origin` | bool | `false` | 启用HTTP/2到源站 |
| `httpHostHeader` | string | `""` | 覆盖Host头 |
| `disableChunkedEncoding` | bool | `false` | 禁用分块编码 |
| `proxyType` | string | `""` | 代理类型 (`socks`) |
| `proxyAddress` | string | `127.0.0.1` | 代理地址 |
| `proxyPort` | int | `0` | 代理端口 |
| `access.required` | bool | — | 启用Access验证 |
| `access.teamName` | string | — | Zero Trust团队名 |
| `access.audTag` | list | — | AUD标签列表 |
