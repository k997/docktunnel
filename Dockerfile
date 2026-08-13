# syntax=docker/dockerfile:1

# 构建阶段
FROM golang:1.24-alpine AS builder

# 安装构建依赖（ca-certificates 用于最终阶段的证书；构建期不需要，保持精简）
RUN apk add --no-cache git

# 设置工作目录
WORKDIR /app

# 复制 go 模块文件
COPY go.mod go.sum ./

# 允许通过构建参数覆盖 Go 模块代理（默认走官方 proxy.golang.org）
ARG GOPROXY=https://proxy.golang.org,direct

# 下载依赖
RUN GOPROXY=${GOPROXY} go mod download

# 复制源代码
COPY . .

# 构建应用。
# - CGO_ENABLED=0: 静态链接，可在任意 scratch/alpine 镜像运行
# - -trimpath: 可复现构建，不泄漏构建机路径
# - 版本信息通过 ldflags 注入（与 Makefile 一致）
ARG VERSION=dev
ARG BUILD_DATE
ARG GIT_COMMIT
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath \
    -ldflags "-s -w -X main.Version=${VERSION} -X main.BuildDate=${BUILD_DATE} -X main.GitCommit=${GIT_COMMIT}" \
    -o docktunnel ./cmd/docktunnel

# 最终阶段：alpine 提供 ca-certificates、busybox wget（用于健康检查）和 shell，
# 同时保留与 scratch 相当的小体积。scratch 无 shell/工具，无法支撑
# docker healthcheck（pgrep/wget 均不存在）。
FROM alpine:3.20

# ca-certificates: 与 Cloudflare API 的 TLS 通信必需
# adduser: 非 root 运行
# /var/lib/docktunnel: 状态持久化目录（保留 retention/补偿队列/flapping 状态）
RUN apk add --no-cache ca-certificates \
    && adduser -D -H -u 10001 docktunnel \
    && mkdir -p /var/lib/docktunnel \
    && chown -R docktunnel:docktunnel /var/lib/docktunnel

COPY --from=builder /app/docktunnel /usr/local/bin/docktunnel

# 使用非 root 用户运行
USER docktunnel

# 诊断/指标端口（/metrics /healthz /debug/state，默认绑定 127.0.0.1）
EXPOSE 9100

# 状态目录：挂载 volume 以在容器重建后保留 retention/补偿队列状态
VOLUME ["/var/lib/docktunnel"]

ENTRYPOINT ["/usr/local/bin/docktunnel"]
