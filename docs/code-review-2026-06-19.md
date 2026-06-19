# DockTunnel 代码审查报告 (2026-06-19)

审查范围：`cmd/`、`internal/`、`pkg/types/`（约 18 个文件，`traefik-source/` 是 vendored Traefik 源码，跳过）。
所有问题均已对照源码二次确认。

---

## 一、严重 Bug（直接影响线上行为）

### 1. `UpsertDNSRecords` 只 create 不 update
- 位置：`internal/cloudflareManager/tunnel.go:482-510`
- batch 只填 `Posts`，没有 `Patches`。当 hostname 已存在 CNAME（状态丢失后重启、或运维手动建过记录），`Records.Batch` 会因为重复记录失败，整个 zone 的 upsert 中止；已成功的 zone 与失败的 zone 各自留下 —— 非原子。
- 与 CLAUDE.md 承诺的 "create/update DNS records" 语义不符。
- 修复：先 list 现有 CNAME，按存在性拆分 `Posts` / `Patches`。

### 2. `DeleteDNSRecords` 在重试时重复 append record ID
- 位置：`internal/cloudflareManager/tunnel.go:552-578`
- `deletes` 声明在 `callWithRetry` 闭包外，闭包内向其 append。5xx 触发重试时，同一 hostname 的记录会被 append 多次，最终 batch 携带重复 ID，Cloudflare 要么报错要么静默处理。
- 修复：在闭包内构造本地 slice，成功后再赋给外层 `deletes`。

### 3. `ExecuteAction` 删除路由前不检查容器是否已恢复
- 位置：`internal/controller/controller.go:449-458`
- 补偿队列重试 `ActionDeleteRoute` 时无条件 `delete(c.ingressRules, action.Hostname)` 再 sync。如果容器在 enqueue 到 retry 之间已经重启并通过 `handleContainerStart` 重新注册了同一 hostname（controller.go:224），这里会把**正在运行的容器**的路由删掉，导致线上服务不可达。
- 修复：删除前检查 `stateManager` 是否仍持有该 hostname 的 active tunnel，或 `RestoreActiveTunnel` 时取消对应的 pending delete。

### 4. Zone cache key 用了完整 hostname，缓存形同虚设
- 位置：`internal/cloudflareManager/tunnel.go:349-396`
- 缓存键是 `hostname`（如 `app.example.com`），但实际 zone 查询用的是推导出的 `domain`（`example.com`）。结果：同一 zone 下每个子域都触发新的 `Zones.List` 调用并写入新缓存项，缓存命中率几乎为 0，API 调用量大幅放大，极易触发 rate limit。
- 修复：缓存键用推导后的 `domain`。

### 5. Public suffix 处理错误
- 位置：`internal/cloudflareManager/tunnel.go:360-364`
- `strings.Join(parts[len(parts)-2:], ".")` 简单取最后两段。`app.example.co.uk` → `co.uk`，zone 查不到 → DNS 操作静默失败。英国/澳洲/日本等域名全军覆没。
- 修复：从右向左逐步尝试更长的后缀，命中第一个 zone 即返回。

### 6. CNAME 内容比较未归一化
- 位置：`internal/cloudflareManager/tunnel.go:407, 434-437`
- 按 `Content == "<id>.cfargotunnel.com"` 精确匹配。Cloudflare 返回的 CNAME content 可能有 trailing `.`、大小写不同。清理时漏匹配 → 孤儿 CNAME 残留。
- 修复：`strings.TrimSuffix(strings.ToLower(content), ".")`。

### 7. Traefik 规则 regex 只认反引号
- 位置：`internal/label/traefik.go:251, 266, 280`
- 三个 regex 都硬编码反引号分隔符。Traefik 官方文档大量示例用 `Host("app.com")` 双引号。从 Traefik 迁移过来的 label 全部无法提取 hostname → 路由静默丢失。
- 修复：扩展字符类同时支持 `"` 和 `'`。

### 8. Hostname 唯一性校验大小写敏感
- 位置：`internal/controller/validator.go:33, 37`
- `hostnameMap[rule.Hostname.Value]` 直接用 hostname 做 key。`App.example.com` 与 `app.example.com` 都能过校验，但 DNS 是大小写不敏感的，sync 时会互相覆盖或被 Cloudflare 拒绝。
- 修复：所有 map 操作前 `strings.ToLower`。

### 9. `cleanup.onExit` 默认 `true` —— 崩溃即清空 DNS
- 位置：`internal/config/config.go:251`
- 开箱默认进程退出就拆 DNS / ingress。任何崩溃重启（OOM、panic、宿主重启）都会让外部域名短暂不可达，直到 reconcile 把记录加回来。
- 修复：默认改为 `false`，由运维主动开启。

### 10. bool 标签只接受小写 `"true"`
- 位置：`internal/label/builder.go:120, 124-130, 165-169`
- `b := v == "true"`。`True` / `TRUE` / `1` / `yes` 全部被当作 `false`。最危险的是 `noTLSVerify=True`：用户以为关掉了 TLS 校验（自签证书场景），实际仍然开着 → 容器握手失败，且没有任何告警。
- 修复：`strconv.ParseBool` + 解析失败时 `slog.Warn`。

---

## 二、中等 Bug（特定场景触发）

### 11. `GetSnapshot` 浅拷贝指针字段，序列化时数据竞争
- 位置：`internal/state/manager.go:469-502`
- 新 map 复制了 header，但 `*TunnelEntry` / `*CompensationRecord` 是指针，仍指向同一对象。`Save`/`SaveJSON` 在锁外用 gob 编码这些指针（Save 不持锁），与 `transitionStoppedLocked` 等并发写入竞争 → 持久化数据可能撕裂。
- 修复：在 RLock 内对每个 entry 做 deep copy。

### 12. `processCompensationQueue` 用了陈旧的 `now` 时间戳
- 位置：`internal/state/manager.go:797, 859`
- `now := time.Now()` 在循环开始时捕获一次。executor 耗时长时，`NextRetryAt = now.Add(backoff)` 偏早，多次慢重试后 backoff 失真叠加。
- 修复：executor 返回后重新取 `time.Now()`。

### 13. 跨 unlock/relock 期间补偿记录未被取消
- 位置：`internal/state/manager.go:794-871`
- 补偿循环释放 `sm.mu` 调 executor，重新加锁后只重新 fetch 记录。但 `RestoreActiveTunnel` 不清理 `pendingActions`，所以隧道在 unlock 期间恢复后，对应的 `ActionDeleteRoute` 仍会被 retry —— 配合 #3 形成完整链路。
- 修复：`RestoreActiveTunnel` 时取消该容器的 pending delete。

### 14. `RunGC` 清理过期 flapping 状态未调用 `markDirty`
- 位置：`internal/state/manager.go:620-628`
- 改动不会触发 `SaveIfDirty`，重启后过期 flapping 又回来。
- 修复：循环内 `sm.markDirty()`。

### 15. Rate limiter 只在一次逻辑操作入口 wait 一次
- 位置：`internal/cloudflareManager/tunnel.go:131-134`
- `callWithRetry` 闭包内可能发多次 HTTP（`ListDNSRecords` 先列 zone 再逐 zone 列 DNS），但 `rateLimiter.Wait` 只在闭包外调用一次。`RateLimit: 10` 实际 RPS 远超 10 → 429 → 整个闭包重试 → 雪崩。
- 修复：每个 HTTP 调用都过 `rateLimiter.Wait`。

### 16. Retry 计数 off-by-one
- 位置：`internal/cloudflareManager/tunnel.go:136`
- `for i := 0; i <= m.maxRetries; i++` —— `MaxRetries: 3` 实际尝试 4 次。
- 修复：`< m.maxRetries`。

### 17. 错误分类靠字符串匹配 status code
- 位置：`internal/cloudflareManager/tunnel.go:182-204`
- `strings.Contains(err.Error(), "429")` 这种判断。cloudflare-go v5 返回 typed error 带 `StatusCode`，字符串匹配既会误判（错误 body 里出现 "500" 字样）也会漏判（401 没数字）。
- 修复：type-assert `*cloudflare.Error` 直接读 `StatusCode`。

### 18. HTTP server 缺少 ReadTimeout/WriteTimeout/IdleTimeout
- 位置：`internal/server/server.go:33-38`
- 只设了 `ReadHeaderTimeout: 5s`。slowloris 攻击或 `/debug/state` 的 `snapshot()` 阻塞（它可能要去抢 controller 锁）可耗尽 FD，把 `/metrics` 一起拖死。
- 修复：补齐三个 timeout，并用 `http.TimeoutHandler` 包 `/debug/state`。

### 19. `/debug/state` 无认证、无方法检查
- 位置：`internal/server/server.go:23-30` 配合 `diagnostics.Handler`
- 默认 bind 127.0.0.1 还好，但 `bindAddr` 可配置成 `0.0.0.0`，此时任何人都能拉到全部 hostname 和 service URL。
- 修复：非 loopback bind 时强制鉴权，并 reject 非 GET 方法。

### 20. 配置数值/时长无范围校验
- 位置：`internal/config/config.go:302-308`
- `New()` 只校验 APIToken 和 AccountID 非空。负的 `coolingPeriod`、0 的 `debounceDuration`、`DOCKTUNNEL_CONTROLLER_DEBOUNCE_DURATION=2`（缺单位）等全部静默通过 —— Viper 解析失败会落到 zero value，debounce 直接被绕过。
- 修复：在 `New()` 末尾加 `validate()` 检查所有 duration/count 为正。

### 21. duration/int parse 错误被静默吞掉
- 位置：`internal/label/builder.go:89-118, 159-164`
- `if d, err := time.ParseDuration(v); err == nil { ... }` 错误分支完全没日志。用户写 `connectTimeout=30 sec`（多了空格）或 `keepAliveConnections=ten`，标签看起来设了实际没生效。
- 修复：err 分支 `slog.Warn`。

### 22. 多网络容器 IP 选择不确定
- 位置：`internal/label/builder.go:34-38`
- bridge 检查完后，fallback `for _, network := range containerInfo.NetworkSettings.Networks` 遍历 map。多 custom network 时顺序随机，每次 reconcile 可能选到不同 IP → 配置抖动。
- 修复：按 network name 排序后再选，或支持 `docktunnel.network` 标签显式指定。

### 23. `originRequest.*` 子键未识别时静默丢弃
- 位置：`internal/label/docktunnel.go:35, 54-108`
- `switch` 的 default 分支是空的。`originRequest.connectTimeout`（驼峰拼错）或大小写不一致的标签会被静默忽略。
- 修复：default 分支 `slog.Warn` 报告未知子键。

---

## 三、代码质量问题

### 24. `ServiceNameUniquenessValidator` 是死代码
- 位置：`internal/controller/validator.go:43-54`
- Go map 的 key 天然唯一，`if serviceNameMap[serviceName]` 永远是 false。看似有保护实则无效，误导审查者。
- 修复：删除，或重写为跨容器 service 名冲突检测。

### 25. `ExposedPortValidator` 是 no-op
- 位置：`internal/controller/validator.go:154-183`
- 只打 Debug 日志，无条件 `return nil`。要么实现要么删除。

### 26. `ValidateAPIToken` 是空壳
- 位置：`internal/config/config.go:313-319`
- 注释里有 `TODO: Implement actual Cloudflare API validation call`，目前只检查 token 长度 ≥ 20。生产部署时 token 权限错也得到运行时第一次 sync 才暴露。
- 修复：调用 `/user/tokens/verify` 真正验证。

### 27. 事件 channel 缓冲只有 10
- 位置：`cmd/docktunnel/main.go:112`
- `make(chan events.Event, 10)`。容器风暴（批量重启）时会丢事件或卡住 Docker 事件监听协程。配合 `metrics.EventsDropped` 缺失，丢了也不知道。
- 修复：256+ 并补 drop 计数。

### 28. CF 初始化失败用 `slog.Error` 而非 `appLogger`
- 位置：`cmd/docktunnel/main.go:75-78`
- JSON 日志部署下这一行破坏日志管道（其他全部走 `appLogger`）。
- 修复：统一用 `appLogger.Error`。

### 29. `EventsTotal` 的 `result` label 是开放集
- 位置：`internal/metrics/metrics.go:13-17, 72`
- 调用方若把 `err.Error()` 当 result 传，Prometheus cardinality 会爆炸（CF 错误信息里带 request ID）。
- 修复：枚举 `success/error/skipped/dropped`，禁止 err 字符串作为 label。

---

## 四、汇总与修复优先级

| 严重度 | 数量 |
|---|---|
| 严重（直接影响线上） | 10 |
| 中等（特定场景触发） | 13 |
| 代码质量 | 6 |

### 第一优先级（最小改动消除最大风险）
- #9：`cleanup.onExit` 默认 `false`（1 行）
- #1 + #4 + #6：Cloudflare DNS 集成三件套
- #3 + #13：补偿队列与恢复路径的状态竞争

### 第二优先级（数据正确性）
- #2 DeleteDNSRecords 重试重复
- #7 Traefik regex
- #8 Hostname 大小写
- #10 bool 解析
- #11 GetSnapshot 深拷贝
- #20 配置范围校验

### 第三优先级（健壮性 / 可观测性）
- 其余各项
