# DockTunnel 生产就绪度修复完成情况

**日期**: 2026-08-13
**范围**: 上线前评估发现的 P0/P1/P2 问题修复
**提交**: `2c0e1c9` chore: production readiness fixes（基于 `bc8ebce`）

---

## 背景

上线前评估发现项目代码质量扎实（构建/`go vet`/常规测试全通过，2026-06-19 代码审查的严重 bug 基本已修），但存在 6 项 P0 阻塞、若干 P1/P2 缺口。本文档记录各项的完成情况与验证结果。

---

## 一、P0 阻塞项（全部修复）

### 1. 环境变量配置完全失效 ✅

**问题**：`internal/config/config.go` 依赖 viper `AutomaticEnv()` + `Unmarshal()`，但 viper v1.20.1 的 `Unmarshal` **不读取 AutomaticEnv 键**（实测：`v.Get` 能读到 env，`v.Unmarshal` 返回空值）。导致 README、`.env.example`、docker-compose 的 `environment:` 全部不生效，运行时报 `cloudflare API token is required`。三处文档的变量名还互不一致（README 用 `DOCKTUNNEL_` 前缀、`.env.example` 无前缀、代码实际读 `CLOUDFLARE_ACCOUNTID`）。

**修复**（`internal/config/config.go`）：
- 设置 `SetEnvPrefix("DOCKTUNNEL")`
- 对全部 57 个配置 key 显式 `BindEnv`，统一为规范的 `DOCKTUNNEL_*` 命名（如 `DOCKTUNNEL_CLOUDFLARE_ACCOUNT_ID`）
- 保留旧的无前缀 `CLOUDFLARE_ACCOUNT_ID` / `CLOUDFLARE_API_TOKEN` 别名，存量部署不受影响

**测试**：新增 3 个回归测试 —— `TestEnvVarsAreReadByUnmarshal`（env 生效、int/duration 解析）、`TestEnvVarsOverrideConfigFile`（env 优先级高于配置文件）、`TestEnvVarsLegacyUnprefixedAlias`（旧别名兼容）。

### 2. `go test -race` 失败 ✅

**问题**：`internal/state` 的 `TestRunCompensation_SuccessfulRetry` 存在数据竞争——补偿 goroutine 向 `executed` slice 追加，测试主 goroutine 直接读取（裸 append + `time.Sleep`）。

**修复**（`internal/state/compensation_test.go`）：互斥锁保护 `executed`，用 `eventually` 有界轮询替代固定 sleep（同时覆盖"executor 返回后记录才被移除"的时序）。

**验证**：`go test -race ./...` 13 个包全部通过。

### 3. Dockerfile 过时 / 镜像构建失败风险 ✅

**问题**：
- `golang:1.21-alpine` 与 go.mod 的 `go 1.24.6` 不匹配
- `FROM scratch` 无 shell/工具，compose 的 `pgrep` 健康检查必然失败
- `upx --best --lzma` 压缩 Go 二进制可能引发反病毒误报
- 无版本信息注入（`-X main.Version` 对缺失符号静默丢弃）

**修复**（`Dockerfile` 重写）：
- 构建阶段 `golang:1.24-alpine`，`CGO_ENABLED=0` + `-trimpath` + ldflags 版本注入
- 最终阶段 `alpine:3.20`（提供 CA 证书、busybox wget、shell），非 root 用户（uid 10001）
- 创建并归属 `/var/lib/docktunnel` 状态目录，`VOLUME` 声明
- 支持 `--build-arg GOPROXY=...` 覆盖模块代理
- `EXPOSE 9100`（指标/健康检查端口）

**验证**：`docker build` 成功；`docker run` 以 uid 10001 运行、`/var/lib/docktunnel` 可写、缺凭据时优雅报错退出。

**配套**（`cmd/docktunnel/main.go`）：新增 `Version/BuildDate/GitCommit` 变量（此前 `-X` ldflags 静默丢弃），启动日志输出版本信息。

### 4. docker-compose 配置损坏 ✅

**问题**：healthcheck 用 `pgrep`（scratch 无此工具）；状态卷被注释（容器重建丢失 retention/补偿队列状态）；env 变量名无 `DOCKTUNNEL_` 前缀；`x-production`/`x-development` 锚点写法无效。

**修复**（`docker-compose.yml` 重写）：`wget http://127.0.0.1:9100/healthz` 健康检查、命名卷 `docktunnel-state:/var/lib/docktunnel`、规范的 `DOCKTUNNEL_*` env 命名。

**验证**：`docker compose config` 通过。

### 5. 无 CI/CD ✅

**新增**：
- `.github/workflows/ci.yml`：gofmt 检查 → `go vet` → `go test -race ./...` → 构建二进制 → Docker 镜像构建
- `.github/workflows/release.yml`：打 `v*` tag 自动发布 —— 跨平台二进制（linux/darwin × amd64/arm64）+ SHA256SUMS + Docker 镜像（需配置 `DOCKERHUB_USERNAME/PASSWORD` secrets）

### 6. traefik-source 损坏 gitlink ✅

**问题**：`traefik-source`（49MB Traefik 源码副本）在 git 索引中是 mode 160000 的 gitlink，但无 `.gitmodules` 映射，新 clone 会得到空目录；`git submodule status` 报错。不参与构建（go.mod 无引用）。

**修复**：`git rm --cached traefik-source` 从索引移除（本地保留作参考），加入 `.gitignore`。仓库现在可干净 clone。

---

## 二、P1 高优先级

### 7. cloudflareManager 测试覆盖 14.5% → 53.5% ✅

**修复**：
- 抽出纯函数 `matchExistingCNAME`（大小写不敏感 + trailing dot 的 DNS 记录匹配，即 6-19 审查 #8/#6 的核心决策逻辑）和 `collectDeleteIDs`
- 新增 `internal/cloudflareManager/httptest_test.go`：基于 `option.WithBaseURL` 指向 httptest 假 API 服务的端到端测试，覆盖：
  - `UpsertDNSRecords`：已存在记录（大小写/点差异）→ Patch；不存在 → Post；zone 逐级解析
  - `DeleteDNSRecords`：按 hostname 收集删除 ID
  - `UpdateConfiguration` / `GetConfiguration`：隧道配置更新与读取
- 顺带修正了 SDK 查询参数格式的认知（`name.exact` 嵌套点格式）

**覆盖率对比**：

| 包 | 修复前 | 修复后 |
|---|---|---|
| cloudflareManager | 14.5% | **53.5%** |
| config | 55.8% | 57.4% |

### 8. 文档漂移 ✅

- `README.md`：Go 版本 1.21 → 1.24；项目结构更新为实际文件（dispatcher/syncer/reconciler/handlers/health/gc/compensation/sync_worker、label/、state/ 等）；环境变量章节说明前缀规则；compose 示例修正
- `CLAUDE.md`：Label Parser 路径更新、Go 版本更新
- `.env.example`：全量重写为规范的 `DOCKTUNNEL_*` 命名并补充补偿/持久化/server 等新配置段

### 9. 安全与贡献文档 ✅

- 新增 `SECURITY.md`：漏洞上报流程（Security Advisories）、本项目的安全相关区域（token 处理、docker socket、诊断端口、cleanup 默认值）
- 新增 `CONTRIBUTING.md`：开发环境、编码标准、PR checklist、bug 上报模板

---

## 三、P2 打磨

### 10. 版本注入修复 ✅

`main.go` 新增 `Version/BuildDate/GitCommit` 变量，`-X` ldflags 现在真正生效；启动日志输出 `version/git_commit/build_date`。Dockerfile 与 release workflow 均注入版本。

---

## 四、验证结果（全绿）

```
go build ./...                  ✅
go vet ./...                    ✅
gofmt -s -d cmd internal pkg    ✅
go test ./...                   ✅（13 包）
go test -race ./...             ✅（13 包，无数据竞争）
go test -cover ./...            ✅（cloudflareManager 53.5%）
docker build                    ✅（镜像实测运行）
docker compose config           ✅
```

---

## 五、未完成 / 后续工作

| 项 | 说明 | 建议优先级 |
|---|---|---|
| controller / docker 覆盖率（53% / 51.6%） | 需较大测试投入 | 上线后第一批技术债 |
| `VerifyAPIToken` 接入启动路径 | 启动时强制调 Cloudflare API 有可用性风险，保持现状（首次 sync 时报错） | 低 |
| 集成测试（`tests/integration/`） | 需真实 Docker + Cloudflare 凭据，无 CI harness | 部署前在预发环境手工执行 |
| 发布镜像 | `kongque/docktunnel` 镜像推送到 Docker Hub | 上线动作 |

## 六、上线前建议动作

1. 打 tag（如 `v0.1.0`）触发 release workflow，验证二进制与镜像产物
2. 将镜像推送 Docker Hub，更新 README 中的镜像引用
3. 预发环境手工跑 `tests/integration/`（docker_events_test.go、end_to_end_test.go）
4. 用规范环境变量（`DOCKTUNNEL_*`）配置部署，验证 env 生效
5. 验证健康检查（`/healthz`）与状态卷持久化（容器重建后 retention 状态保留）
