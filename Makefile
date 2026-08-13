# DockTunnel Makefile

# 变量定义
NAME=docktunnel
BINARY=${NAME}
MAIN_DIR=cmd/${NAME}
VERSION ?= dev
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
GIT_COMMIT ?= $(shell git rev-parse HEAD)
GIT_BRANCH ?= $(shell git rev-parse --abbrev-ref HEAD)

# Go相关变量
GO_BUILD=go build
GO_TEST=go test
GO_CLEAN=go clean
GO_DEPS=go mod tidy
GOFMT=gofmt
GOFLAGS ?= -v

# 构建标签和链接标志
LDFLAGS=-ldflags "-X main.Version=${VERSION} -X main.BuildDate=${BUILD_DATE} -X main.GitCommit=${GIT_COMMIT}"

# 默认目标
all: fmt build

# 格式化代码
fmt:
	${GOFMT} -s -w .

# 检查代码格式
fmt-check:
	@diff=$$(${GOFMT} -s -d .); \
	if [ -n "$$diff" ]; then \
		echo "Please run 'make fmt' to format the code."; \
		echo "$${diff}"; \
		exit 1; \
	fi;

# 安装依赖（显式目标：仅在需要时运行）
# 注意：go mod tidy 可能改写 go.mod/go.sum，build 不再自动依赖它，
# 以避免无意间改动依赖锁定文件。
deps:
	${GO_DEPS}

# 构建二进制文件（-trimpath 与 CI/Dockerfile 保持一致：可复现构建）
build:
	${GO_BUILD} ${GOFLAGS} -trimpath ${LDFLAGS} -o ${BINARY} ./${MAIN_DIR}

# 安装二进制文件到GOPATH
install:
	${GO_BUILD} ${GOFLAGS} -trimpath ${LDFLAGS} -o ${GOPATH}/bin/${BINARY} ./${MAIN_DIR}

# 运行测试
test:
	${GO_TEST} ./...

# 运行测试并显示覆盖率
test-coverage:
	${GO_TEST} -cover ./...

# 清理构建产物
clean:
	${GO_CLEAN}
	rm -f ${BINARY}

# 运行程序
run: build
	./${BINARY}

# 构建Docker镜像
docker-build:
	docker build -t ${NAME}:${VERSION} .

# 构建Docker镜像并标记为latest
docker-build-latest:
	docker build -t ${NAME}:${VERSION} -t ${NAME}:latest .

# 帮助信息
help:
	@echo "DockTunnel Makefile targets:"
	@echo "  all               - 格式化代码并构建 (default)"
	@echo "  fmt               - 格式化代码"
	@echo "  fmt-check         - 检查代码格式"
	@echo "  deps              - 下载/整理依赖 (go mod tidy; 显式目标，build 不再自动执行)"
	@echo "  build             - 构建二进制文件 (-trimpath)"
	@echo "  install           - 安装二进制文件到GOPATH (-trimpath)"
	@echo "  test              - 运行测试"
	@echo "  test-coverage     - 运行测试并显示覆盖率"
	@echo "  clean             - 清理构建产物"
	@echo "  run               - 构建并运行程序"
	@echo "  docker-build      - 构建Docker镜像"
	@echo "  docker-build-latest - 构建Docker镜像并标记为latest"
	@echo "  help              - 显示此帮助信息"

# 设置默认目标
.DEFAULT_GOAL := all