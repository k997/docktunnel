# DockTunnel v0.1.0 发布前验收报告

**日期**: 2026-08-14
**范围**: 提交 `a9b9427`（feat: production readiness overhaul，57 文件，+6079/−1320）按领域拆分后的验收归档
**基线**: `2c0e1c9` chore: production readiness fixes
**结论**: **可发布 v0.1.0**

---

## 一、背景与范围

本次发布前对 DockTunnel 做了三轮深度审查，覆盖三大领域：

1. **label / Traefik 兼容层**：标签解析安全边界、Traefik 规则子集映射、注入与误暴露风险
2. **Docker / 控制器**：事件流、状态机、对账逻辑、补偿队列、持久化一致性
3. **Cloudflare / 工程化**：API 契约、分页/重试/限流、发布流水线、文档对齐

审查共发现 **3 个 P0** 与 **约 15 项 P1/P2** 问题。P0 三件套：

- **cleanup 门控缺失**：退出清理无条件执行（含默认配置），可能在生产退出时误删 DNS 记录
- **Traefik 标签无 opt-in**：任何容器上的 `traefik.*` 标签（含受 basicAuth 等中间件保护的内部路由）都会被直接发布为公网隧道规则
- **保留规则无法跨重启恢复**：retention 条目只存 hostname，进程重启后无法重建完整 ingress 规则，保留语义失效

## 二、修复执行

修复由 v4-flash 模型分波执行，主代理全程负责修正与收口：

1. **第一波（并行）**：label 领域与 cloudflare 领域并行修复
2. **第二波**：controller 领域（期望态/对账/补偿/持久化）
3. **第三波**：docs/engineering 对齐（README/CLAUDE/SECURITY/CI/Docker/多架构发布）
4. **第四波**：收尾验证（构建/静态检查/竞态测试/集成测试）
5. **审查发现修复**：主代理根据复验审查修正 **R1–R6**（如 R1：`delete_retention` 主拼写与按服务隔离校验；R2：middlewares 拒绝暴露；R3：无 Host 的 Path-only 路由拒绝；R5：冷却指数 shift 钳位；R6：单容器校验失败不毁整树同步）
6. **P3 修复**：增量修复 **P3-1~P3-4**（多值 HostSNI 拆分、traefik.udp.* Info 忽略、`]` 无 `[` 恶意 key 防 panic、全部 zone 失败聚合错误触发重试）

## 三、关键修复清单（按五领域）

### 1. Cloudflare（internal/cloudflareManager/）

- 隧道 secret 由 hex 改为标准 base64 编码，落实 32 字节 `tunnel_secret` 契约
- Tunnels/Zones/DNS 记录列表全部改为 ListAutoPaging 全量分页，消除大账户/大 zone 漏项
- 创建隧道后按名复查，防止 5xx 重试产生重复隧道
- Upsert/Delete 增加 content 所有权校验：同名记录指向其它目标时跳过，拒绝覆盖/误删
- 专用 `http.Client`（连接/TLS/响应头/整体超时）替代无超时的 `http.DefaultClient`
- 重试退避改 full jitter，429 尊重 Retry-After（delta-seconds 与 HTTP-date 两种格式）
- zone 缓存带 10 分钟 TTL，避免 zone 删除/转移后命中过期缓存
- DNS 批量操作按每批最多 200 条分块提交
- 全部 zone 列表失败返回聚合错误，触发外层重试；部分失败靠下轮 reconcile 收敛
- 覆盖率 **53.5% → 78.8%**（httptest 假 API 端到端测试）

### 2. Label / Traefik（internal/label/）

- **Traefik 显式 opt-in**（P0）：`docktunnel.traefik.enable=true` 才解析 `traefik.*`，消除内部路由无认证公网暴露
- 安全规则子集：`!`、`&&`、`||`、`HostRegexp`/`PathPrefix`/`PathRegexp`/`Method`/`Header`/`Query`/`ClientIP` 一律拒绝 + WARN；middlewares 路由拒绝暴露；weighted/无 loadbalancer.server 的 service 拒绝
- 单引号 Path/HostSNI 支持；多值 `HostSNI('a.com','b.com')` 拆分；无引号 Host(a.com) 无 hostname 则跳过 + WARN
- `server.url` 优先于 `server.port`/`server.scheme`；无效 port/url 记录 Error 而非静默置 0
- 恶意 key（段含 `]` 无 `[`）不再 panic，DecodeToNode 自带防御；单条坏标签不毁整树
- `delete_retention` 主拼写与 `retention` 别名、`access.*` 别名族、`matchSNItoHost` 明确不支持 + WARN
- `docktunnel.<svc>.network` 与 `traefik.docker.network` 选网（host→localhost，未知网络回退默认探测）
- hostname 白名单校验（允许 `*` 通配）；service URL 强制 http/https/tcp 绝对地址

### 3. Controller / State / Docker（internal/controller、internal/state、internal/docker、pkg/types、cmd/docktunnel）

- **保留规则跨重启恢复**（P0）：`buildDesiredRules` 统一期望态 = 运行容器 ∪ 未过期 Retaining，RuleJSON 持久化完整 ingress 规则，运行中优先
- Reconcile 对期望态 vs 内存态 vs Cloudflare 实际配置做三向 diff，仅漂移时触发同步
- syncWorker 新增 onError 回调：同步失败经补偿队列入队单条 ActionSync（去重 + Dead 清理）
- **cleanup 门控**（P0）：`cleanup.onExit`（默认 false）总开关，策略仅 `graceful-cleanup`/`fast-exit` 生效
- catchAll 接线；`(hostname,path)` 复合键替代 hostname 单键；规则确定性排序
- 启动竞态重排（监听器 → 初始 Sync → dispatch 循环）；全 goroutine panic 恢复
- 断连差集按 stop 语义逐服务转换；冷却容器被 Reconcile 跳过（含指数 shift 钳位）
- destroy→die 语义统一；`health_status:starting` 补 inspect；事件通道 ok 检查触发重连
- 状态键改 `containerID:serviceName` 复合键；持久化 0600 + saveMu 串行；`GetAllPendingActions` 返回拷贝修复数据竞争
- validator 按（小写 hostname, path）判重；删除 flapping 死代码；`delete_retention` 按服务隔离校验
- 覆盖率 **53% → 69.4%**

### 4. 配置（internal/config/）

- `CONFIG_PATH` 环境变量指定配置文件完整路径（如 systemd 部署），非空时优先于默认查找路径
- `cloudflare.rateLimit` 默认 10 → 4，对齐 Cloudflare 官方约 1200 req/5min 限额
- `cleanup.strategy` 只接受 `""`/`graceful-cleanup`/`fast-exit`；旧值 `force-cleanup`/`none` 报错并给出迁移提示
- 覆盖率 **57.4% → 58.2%**

### 5. 工程化与文档（README/CLAUDE/SECURITY/Dockerfile/Makefile/.github/）

- README/CLAUDE/SECURITY 与修复后行为全对齐（traefik 三类矩阵、cleanup 语义、cloudflared 部署章节、systemd 单元修正、示例 2 注释移出、占位说明）
- `.env` 与 `.env.*` 入 `.gitignore`（保留 `!.env.example`）；新增 `.dockerignore`
- Dockerfile 固定 `golang:1.24.6-alpine`、`GOTOOLCHAIN=local`、HEALTHCHECK 探活 `/healthz`
- Makefile `-trimpath`，build 不再依赖 deps
- release.yml：buildx + qemu 多架构 amd64/arm64，GHCR 总是推送 + Docker Hub 可选（secrets 存在时）；版本号去 v 前缀
- ci.yml 新增 govulncheck 漏洞扫描

## 四、验收过程

1. **v4-pro 四区审查**：对拆分后的五个领域（label、cloudflare、controller/state、config、engineering）分四区交叉复核
2. **F1–F6 复验全部到位**：对审查发现逐项复验（F1：恶意 key 防 panic；F2：单引号 Path/HostSNI；F3：matchSNItoHost 明确不支持；F4：weighted/无 server service 拒绝；F5：无引号 Host 无 hostname 跳过；F6：集成测试对齐 traefik opt-in 与 delete_retention 新行为）
3. **P3 增量复验全部到位**：P3-1~P3-4 逐项验证（多值 HostSNI、UDP Info 忽略、`]` 无 `[` 防 panic、全部 zone 失败聚合错误）

## 五、验证矩阵（全绿）

| 验证项 | 命令 | 结果 |
|---|---|---|
| 代码格式 | `gofmt -s -d cmd internal pkg tests` | ✅ 无输出 |
| 构建 | `go build ./...` | ✅ 通过 |
| 静态检查 | `go vet ./...` | ✅ 通过 |
| 静态检查（集成标签） | `go vet -tags integration ./...` | ✅ 通过 |
| 竞态测试 | `go test -race -count=1 ./...` | ✅ 全部通过，无数据竞争 |
| 集成测试 | `go test -tags integration -count=1 ./tests/integration/` | ✅ 7 PASS 1 SKIP（无 CF 凭据时 TestEndToEndLifecycle 跳过） |
| 单元覆盖率 | `go test -count=1 -cover ./...` | ✅ 见下表 |
| Compose 校验 | `docker compose config` | ✅ 通过 |
| 镜像构建检查 | `docker build --check .` | ✅ 无警告 |

### 覆盖率对比（本次验收实测，`-count=1`）

| 包 | 修复前 | 修复后 |
|---|---|---|
| cloudflareManager | 53.5% | **78.8%** |
| controller | 53% | **69.4%** |
| config | 57.4% | **58.2%** |
| docker | 51.6% | **53.5%** |
| label | 81.4% | **82.6%** |
| state | — | **81.9%** |

> 注：cloudflareManager/config 起点取自 `docs/production-readiness-2026-08-13.md`；controller/docker 起点取自该文档"未完成/后续工作"节的 53%/51.6%；label 起点为审查基线 81.4%。

## 六、已知权衡与遗留

- **旧状态文件无 RuleJSON 的保留条目一次性降级跳过**：升级前生成的 retention 条目没有持久化完整规则，`ruleFromRetainingEntry` 解析失败时 Warn 并跳过该条目（只影响升级瞬间仍处于保留期的旧条目，不阻塞启动）
- **冷却期容器被对账下线路由的语义**：Reconcile 以 `skipCooling=true` 跳过冷却中的容器，冷却期内其路由可能不参与期望态重建；这是冷却逻辑的预期行为，冷却结束后的下一轮对账收敛
- **单 zone 部分失败靠下轮 reconcile 收敛**：DNS 列表多 zone 部分失败时仅记录 Error 日志并返回成功，由周期性 Reconcile 在下一轮重试收敛
- **UDP 不支持属 Cloudflare 模型限制**：Cloudflare Tunnel 无 UDP 入口模型，`traefik.udp.*` 按文档 Info 忽略
- 集成测试无 CI harness，需在预发环境手工执行（见下方行动清单）

## 七、发布前行动清单

1. **打 tag**：`git tag v0.1.0 && git push origin v0.1.0`（触发 release.yml：多架构镜像 + 跨平台二进制）
2. **替换占位**：README 中的仓库地址、镜像名（`kongque/docktunnel`）与联系方式占位
3. **Docker Hub secrets（可选）**：配置 `DOCKERHUB_USERNAME`/`DOCKERHUB_PASSWORD` secrets 后 Docker Hub 才推送镜像；未配置时仅推 GHCR
4. **预发环境集成测试**：在预发环境配置 `CLOUDFLARE_ACCOUNT_ID`/`CLOUDFLARE_API_TOKEN` 后手工跑 `go test -tags integration ./tests/integration/`（当前环境无凭据，端到端用例跳过）
5. **规范配置部署**：用 `DOCKTUNNEL_*` 环境变量或 `CONFIG_PATH` 指向的配置文件部署，验证 `/healthz` 健康检查与状态卷持久化（容器重建后 retention 状态保留）

## 八、结论

**DockTunnel 可发布 v0.1.0。** 三个 P0 阻塞项（cleanup 门控、Traefik opt-in 防暴露、保留规则跨重启恢复）全部修复并经复验；P1/P2 与 P3 项增量修复到位；构建、静态检查、竞态测试、集成测试、compose 校验、镜像构建检查全绿；关键包测试覆盖率显著提升。
