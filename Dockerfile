# 构建阶段
FROM golang:1.21-alpine AS builder

# 安装构建依赖
RUN apk add --no-cache upx ca-certificates

# 设置工作目录
WORKDIR /app

# 复制go模块文件
COPY go.mod go.sum ./

# 下载依赖
RUN go mod download

# 复制源代码
COPY . .

# 构建应用
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o docktunnel ./cmd/docktunnel

# 使用UPX压缩二进制文件
RUN upx --best --lzma docktunnel

# 创建非root用户
RUN adduser -D -s /bin/sh docktunnel

# 最终阶段
FROM scratch

# 从中间阶段复制ca证书和用户信息
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /etc/passwd /etc/passwd
COPY --from=builder /etc/group /etc/group

# 从构建阶段复制压缩后的二进制文件
COPY --from=builder /app/docktunnel /docktunnel

# 使用非root用户运行
USER docktunnel

# 暴露端口（虽然本应用不直接监听端口，但保留作为参考）
EXPOSE 8080

# 设置配置文件挂载点
VOLUME ["/app"]

# 设置入口点
ENTRYPOINT ["/docktunnel"]