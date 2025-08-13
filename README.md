# DockTunnel

[![Go Report Card](https://goreportcard.com/badge/github.com/yourusername/docktunnel)](https://goreportcard.com/report/github.com/yourusername/docktunnel)
[![License](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

## 简介

DockTunnel 是一个自动化工具，用于管理 Docker 容器和 Cloudflare Tunnel 之间的连接。它能够监听 Docker 容器事件，并自动配置 Cloudflare Tunnel 和 DNS 记录，使得容器化的服务可以通过自定义域名在互联网上访问。

## 功能特性

- 自动监听 Docker 容器的启动和停止事件
- 根据容器标签自动配置 Cloudflare Tunnel
- 自动管理 Cloudflare DNS 记录
- 支持多种容器服务配置选项
- 通过配置文件进行灵活配置
- 支持日志级别和格式的自定义

## 工作原理

DockTunnel 通过监听 Docker daemon 的事件来检测容器的启动和停止。当一个带有特定标签的容器启动时，DockTunnel 会解析这些标签并创建相应的 Cloudflare Tunnel 配置和 DNS 记录。当容器停止时，相关的配置也会被清理。

## 快速开始

### 系统要求

- Docker 18.09 或更高版本
- Go 1.21 或更高版本（仅开发）
- Cloudflare 账户和 API 令牌

### 安装

#### 使用 Go 安装

```bash
go install github.com/yourusername/docktunnel@latest
```

#### 从源码构建

```bash
git clone https://github.com/yourusername/docktunnel.git
cd docktunnel
go build -o docktunnel ./cmd/docktunnel
```

#### 使用 Docker 运行

```bash
docker run -d \
  --name=docktunnel \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v ./config.yaml:/etc/docktunnel/config.yaml \
  yourusername/docktunnel:latest
```

### 配置

创建 `config.yaml` 文件：

```yaml
log:
  level: "info"
  format: "text"

cloudflare:
  accountId: "your-cloudflare-account-id"
  apiToken: "your-cloudflare-api-token"
  tunnelId: ""  # 如果为空，将自动创建隧道
```

### 容器标签

要让 DockTunnel 管理您的容器，请在运行容器时添加以下标签：

```bash
docker run -d \
  --name=web-server \
  -l docktunnel.enable=true \
  -l docktunnel.web.hostname=example.com \
  -l docktunnel.web.service=http://localhost:8080 \
  nginx:latest
```

支持的标签包括：

- `docktunnel.enable`: 设置为 `true` 以启用 DockTunnel 管理
- `docktunnel.<service-name>.hostname`: 服务的主机名
- `docktunnel.<service-name>.service`: 服务地址（例如 `http://localhost:8080`）
- `docktunnel.<service-name>.path`: 可选，服务路径
- `docktunnel.<service-name>.originRequest.noTLSVerify`: 可选，是否跳过 TLS 验证
- `docktunnel.<service-name>.originRequest.connectTimeout`: 可选，连接超时时间
- `docktunnel.<service-name>.originRequest.tlsTimeout`: 可选，TLS 超时时间
- `docktunnel.<service-name>.originRequest.keepAliveConnections`: 可选，保持连接数
- `docktunnel.<service-name>.originRequest.keepAliveTimeout`: 可选，保持连接超时时间
- `docktunnel.<service-name>.originRequest.http2Origin`: 可选，是否启用 HTTP/2

### 运行

```bash
./docktunnel
```

## 使用示例

### 简单Web服务示例

假设您有一个运行在端口8080的Web应用，想要通过 `myapp.example.com` 访问：

```bash
# 启动您的应用容器
docker run -d \
  --name=my-web-app \
  -l docktunnel.enable=true \
  -l docktunnel.web.hostname=myapp.example.com \
  -l docktunnel.web.service=http://localhost:8080 \
  my-web-app:latest
```

当容器启动后，DockTunnel 会自动：
1. 在 Cloudflare 中为您的账户创建或使用现有隧道
2. 为 `myapp.example.com` 创建 DNS 记录，指向您的隧道
3. 配置隧道规则，将 `myapp.example.com` 的请求转发到 `http://localhost:8080`

### 多服务示例

如果您有多个服务需要暴露：

```bash
# API服务
docker run -d \
  --name=api-service \
  -l docktunnel.enable=true \
  -l docktunnel.api.hostname=api.example.com \
  -l docktunnel.api.service=http://localhost:3000 \
  -l docktunnel.api.path=/api \
  api-service:latest

# Web前端服务
docker run -d \
  --name=web-service \
  -l docktunnel.enable=true \
  -l docktunnel.web.hostname=example.com \
  -l docktunnel.web.service=http://localhost:8080 \
  web-service:latest
```

### 高级配置示例

对于需要特殊配置的服务：

```bash
docker run -d \
  --name=legacy-service \
  -l docktunnel.enable=true \
  -l docktunnel.legacy.hostname=legacy.example.com \
  -l docktunnel.legacy.service=http://localhost:8080 \
  -l docktunnel.legacy.originRequest.noTLSVerify=true \
  -l docktunnel.legacy.originRequest.connectTimeout=10s \
  -l docktunnel.legacy.originRequest.keepAliveConnections=10 \
  legacy-service:latest
```

## 开发

### 依赖

- Go 1.21+
- Docker

### 构建

```bash
go build -o docktunnel ./cmd/docktunnel
```

### 测试

```bash
go test ./...
```

## 部署

### 作为系统服务

创建 systemd 服务文件 `/etc/systemd/system/docktunnel.service`：

```ini
[Unit]
Description=DockTunnel Service
After=docker.service
Requires=docker.service

[Service]
ExecStart=/usr/local/bin/docktunnel
Restart=always
RestartSec=10
Environment=CONFIG_PATH=/etc/docktunnel/config.yaml

[Install]
WantedBy=multi-user.target
```

然后启用并启动服务：

```bash
sudo systemctl enable docktunnel
sudo systemctl start docktunnel
```

## 故障排除

### 常见问题

1. **无法连接到 Docker daemon**
   确保 DockTunnel 可以访问 Docker socket 文件（通常位于 `/var/run/docker.sock`）。

2. **Cloudflare API 错误**
   检查您的 `accountId` 和 `apiToken` 是否正确，并确保 API 令牌具有足够的权限。

## 许可证

本项目采用 MIT 许可证。详情请见 [LICENSE](LICENSE) 文件。