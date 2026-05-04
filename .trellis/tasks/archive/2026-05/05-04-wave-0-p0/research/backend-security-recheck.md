# 后端安全 finding 复核

- **Date**: 2026-05-03
- **Scope**: B-4 ~ B-8 五项后端安全 finding 实读核验
- **方法**: 全部基于实际 Read/Grep 当前代码，未参考子代理报告

---

## B-4 命令注入

- **状态**：⚠️ 部分真实（机制安全，但缺少深度防御与回归测试）
- **当前代码状态**：
  - `ShellEscape` 实现在 `backend/internal/task/executor/executor.go:362-365`：用单引号包裹并转义 `'` → `'\''`。这是 POSIX shell 的标准安全转义，单引号内 `$(...)`、反引号、`;`、换行符等**全部被字面化**，不会被 shell 解释。
  - 用户输入路径在 executor 中的封装：
    - `restic_executor.go:65,86,122,126,277,309,350,352,386,397` — 全部经 `ShellEscape`
    - `rclone_executor.go:207,210` — `source/dest/bwlimit` 全部经 `ShellEscape`
    - `executor.go:286-287` — rsync 远程 restore 的 source/target 经 `ShellEscape`
    - `executor.go:557` — `EnsureRemoteTargetReady` 的 targetPath 经 `ShellEscape`
  - rsync 本地执行（`executor.go:200`）使用 `exec.CommandContext(e.binary, args...)` + `--` 分隔符（line 199），**不经过 shell**，不存在注入。
  - `command` 执行器 (`command_executor.go:64`) 把用户 `task.Command` 直接 `session.Start`，由远程 shell 解释——这是**设计意图**（让用户在远端跑任意命令），权限边界是任务创建/编辑的 RBAC（见 `task_handler.go:860 validateTaskRequest`），不属于注入漏洞。
  - 输入校验（`task_handler.go:894-906` + `helpers.go:217-237`）：仅在 `RSYNC_ALLOWED_SOURCE_PREFIXES` / `RSYNC_ALLOWED_TARGET_PREFIXES` 环境变量配置时才生效；**默认无路径白名单**。`ExcludeRules` 仅 `TrimSpace`（`policy_handler.go:198,360`），无任何字符校验。
  - 测试：`retention_test.go:13-35 TestShellEscape` 仅覆盖 4 个良性输入（空字符串/简单字符串/单引号/空格），**未覆盖**对抗性输入：`$(...)`、反引号、`;`、换行符、Unicode 控制字符、`'\''` 嵌套绕过尝试等。
- **子代理判断的偏差**：
  - 子代理称"可能被 `$(...)`、反引号、换行符绕过"——**实测错误**。`ShellEscape` 单引号包裹后这些字符全部失效。
  - 但子代理对"测试覆盖不足"的担忧是合理的。
- **正确修复方向**：
  1. 增强 `TestShellEscape` 用例，加入对抗性输入（`$(rm -rf /)`、`` `id` ``、`a'; rm -rf /; '`、`$IFS`、换行注入），并实际通过 `sh -c "echo ..."` 回放验证字面化。`tests/` 目录新增 `executor/shell_escape_adversarial_test.go`。
  2. 在 `validateTaskRequest` 中对 `RsyncSource/RsyncTarget` 增加默认黑名单字符校验（控制字符、换行），即便 ShellEscape 已经安全也作为深度防御。
  3. 在 `policy_handler.go` 的 `ExcludeRules`/exclude patterns 写入前增加单行限制（拒绝换行）；并对每条 pattern 做长度上限。
  4. **不要**改变 `ShellEscape` 实现——当前实现已是最佳实践。
- **实施工作量**：S（仅新增测试 + 简单字符校验）

---

## B-5 WebSocket 推送缺 per-message ACL

- **状态**：❌ 误报
- **当前代码状态**：
  - `ws/hub.go:107-122` 的 `Run()` broadcast 循环对每条事件调用 `h.clientCanAccessTask(c, event.TaskID)`（line 112），未通过则 `continue`，不会下发。
  - `clientCanAccessTask` (`hub.go:386-408`) 带 30 秒 TTL 的内存缓存（`taskAccessCacheTTL`，line 65），实际查询委托 `canAccessTask` (`hub.go:335-364`)：admin 全开、operator 按 `AllowedNodeIDs` 校验、viewer 仅校验任务存在。
  - `loadBackfillEvents` (`hub.go:283-333`) 在返回前调用 `filterEventsByAccess` (line 332) 做同等过滤，覆盖断线重连时的历史回放。
  - `ws_handler.go:40-59` 在认证后通过 `middleware.OwnedNodeIDs(db, claims.UserID)` 加载 operator 的 ownership 集合，存入 `AccessScope.AllowedNodeIDs`。
  - 测试：`ws/hub_test.go:174-241` 已覆盖 operator 仅看到所属节点日志的场景。
- **子代理判断的偏差**：完全错判。子代理可能只看了 `ServeWS` 入口而未读 `Run()` 主循环。
- **正确修复方向**：无需修复。
  - 可选优化：30 秒缓存 TTL 让 ownership 变更最长延迟 30 秒生效；如需即时撤权，可在 `node_owner_handler.go` 调用 `Hub.InvalidateAccessCache(userID)`（需新增方法）。但这是产品体验问题，不是安全漏洞。
- **实施工作量**：N/A

---

## B-6 JWT revoked map 竞态

- **状态**：❌ 误报
- **当前代码状态**：
  - `auth/jwt.go:172-183` 的 `pruneRevokedLocked` 命名带 `Locked` 后缀，是 Go 社区惯例，意即"调用者必须持锁"。
  - 调用点全部在 `m.mu.Lock()` 范围内：
    - `RevokeToken` (line 125-128)：`m.mu.Lock(); m.revoked[key]=...; m.pruneRevokedLocked(...); m.mu.Unlock()`
    - `parseToken` (line 158-164)：`m.mu.Lock(); ... m.pruneRevokedLocked(now); ... m.mu.Unlock()`
  - `loadRevokedFromDB` (line 50-61) 启动时调用，写入 `m.revoked` 时同样持锁（line 56-60）。
  - 唯一的 goroutine 异步操作是 `pruneRevokedLocked:181`：`go db.Where(...).Delete(...)`，操作的是数据库而非内存 map，不构成对 `m.revoked` 的并发访问。
- **子代理判断的偏差**：误判。可能被 `pruneRevokedLocked` 内的 `delete(m.revoked, key)` 调用 + 异步 goroutine 同时出现的视觉关联误导。
- **正确修复方向**：无需修复。
  - 可选改进：异步 goroutine 多次触发时可能并发执行同一条 SQL `DELETE`，但 SQL 层是幂等的，无副作用。如需更严格，可在 JWTManager 上加 `pruneInProgress atomic.Bool` 防止重入。非安全问题。
- **实施工作量**：N/A

---

## B-7 任务 goroutine 无 context 超时

- **状态**：✅ 真实
- **当前代码状态**：
  - `runner.go:147 runTask` 内 `execCtx, cancel := context.WithCancel(context.Background())`（line 270）——**只 Cancel，没有 Timeout**。`runRestoreTask` (line 525, 530) 同样如此。
  - `cancel` 仅在显式 `Manager.Cancel()`（manager.go:340-344）或进程关闭（manager.go:473-478 `Shutdown`）时触发。如果 executor 的 `session.Wait()` 永久阻塞（远端 hang、半连接），ctx 永不超时。
  - 已存在的局部超时：
    - `sshutil/ssh_auth.go:182` SSH `Dial` 5 秒超时（仅握手）
    - `runner.go:281,344` Pre/Post hook 超时（默认 5 分钟，可配置 `Policy.HookTimeoutSeconds`）
    - `retention.go:122,172` 保留策略 30 分钟超时
    - `integrity_checker.go:54,101` 完整性检查 30 分钟超时
  - 主备份/恢复执行（`executor.Run` 调用链）**没有任何挂钟超时**。executor 内部的 `session.Wait()` (restic_executor.go:187, command_executor.go:110, rclone_executor.go:162, executor.go:236/349) 仅在 ctx 取消时通过 `session.Signal(SIGTERM)` 终止，但 ctx 本身永不超时。
- **子代理判断**：基本准确。定位的行号 285 是 `m.taskWG.Add(1)` 后的 goroutine 启动，471 一带是 retry 流程；真正的根因在 `runner.go:270` 和 `runner.go:530`。
- **正确修复方向**：
  1. 在 `Manager` 增加 `taskMaxDuration time.Duration` 字段（默认 24 小时，可由环境变量 `TASK_MAX_DURATION` 或 `system_settings.task.max_duration` 覆盖）。
  2. 在 `runner.go:270` 改为：
     ```go
     execCtx, cancel := context.WithTimeout(context.Background(), m.taskMaxDuration)
     ```
     `runRestoreTask:530` 同样处理。
  3. 在 `Policy` 模型增加可选 `MaxRuntimeSeconds int`（迁移 000031），允许策略级覆盖；优先级：Policy > 全局 > 默认 24h。
  4. 超时触发时区分日志：在 `runTask` 末尾检查 `errors.Is(execCtx.Err(), context.DeadlineExceeded)`，写入 `last_error="任务超时（超过 N 分钟）"` 并发告警。
  5. 测试：在 `runner_test.go` 用一个 sleep 超过超时时长的 stub executor 验证状态切到 failed。
- **实施工作量**：M（涉及迁移 + 设置 + 单元测试）

---

## B-8 时区混用

- **状态**：⚠️ 部分真实（命名/可读性问题，非功能漏洞）
- **当前代码状态**：
  - 全局统计：`backend/internal/` 下非测试代码 `time.Now()` 出现 **139** 次，`time.Now().UTC()` 出现 **30** 次。
  - 关键位置：
    - `auth/login_lock.go:132` 后台 cleanup 用 `time.Now()`（local），存入对比的 `LockedUntil` 字段也由调用方传入（auth/service.go:81 `time.Now()` 也是 local），**自洽**。
    - `alerting/silence_retention.go:68` `cutoff := time.Now().UTC().Add(-...)` 用 UTC，与 DB 中存储的 `created_at`（GORM 默认 `time.Time` 字段）比较。
    - `task/runner.go:247` 唯一显式 `.UTC()` 的地方（`last_run_at`），其余 17 处 `time.Now()` 都是 local。
  - DB 层（`database/database.go`）：SQLite DSN（line 32-39）**未指定** `_loc` 参数，go-sqlite3 默认为 `auto`（按 `time.Local` 存取）；Postgres 用 GORM 默认（驱动按 TIMESTAMPTZ 自动 UTC 化，读出来转 local）。
  - **关键事实**：`time.Now()` 与 `time.Now().UTC()` 表示**同一个绝对时刻**，只有 `Location()` 字段不同。`now.Add(-x).After(other)` 等比较不会因 zone 改变结果。`.UTC()` 影响的只是 `time.Format()` 显示和 string serialization。
  - 因此：login lock 不会因 zone 漂移；silence retention cutoff 不会因 UTC 而错算。**功能上无 bug**。
  - 真正可能踩坑的场景：
    1. 容器宿主机 TZ ≠ DB 期望 TZ 时，**SQLite 写入显示** 与日志显示混乱（用户排错困难）。
    2. JSON 序列化给前端时，部分时间带 `Z` 后缀、部分带 `+08:00`，前端 Date 解析虽然正确，但展示一致性差。
    3. 跨进程/跨实例对比（如分布式部署）容易引入认知偏差。
- **子代理判断的偏差**：把"风格不一致"夸大成"时区漂移导致锁定/过期判断错误"。功能层面无 bug。
- **正确修复方向**：
  1. **不必**紧急修复（无功能性 bug）。
  2. 中期统一：新增 `internal/util/clock.go` 暴露 `Now() time.Time { return time.Now().UTC() }`，用 lint 规则禁止业务代码直接调 `time.Now()`（CI 加 `gochecknoglobals` 或 `forbidigo`）。逐步替换 139 处。
  3. 数据库层强制：SQLite DSN 加 `&_loc=UTC`；Postgres `gorm.Config{NowFunc: func() time.Time { return time.Now().UTC() }}`。这样 `CreatedAt/UpdatedAt` 自动 UTC。
  4. 前端展示在 `web/src/lib/format.ts` 统一 `toLocaleString` 格式化。
- **实施工作量**：M（机械替换 + 增加 lint 规则；可拆分为多个 PR）

---

## 总体结论

| Finding | 状态 | 优先级 | 工作量 |
|---|---|---|---|
| B-4 命令注入 | ⚠️ 部分（机制安全，缺测试） | 中 | S |
| B-5 WS per-message ACL | ❌ 误报 | — | N/A |
| B-6 JWT 竞态 | ❌ 误报 | — | N/A |
| B-7 任务无超时 | ✅ 真实 | **高** | M |
| B-8 时区混用 | ⚠️ 部分（风格问题，非 bug） | 低 | M |

- 真实问题数：**1**（B-7）
- 误报数：**2**（B-5, B-6）
- 部分真实：**2**（B-4 缺深度防御与对抗性测试；B-8 是代码风格而非功能 bug）

**建议优先修复**：
1. **B-7（任务超时）** — 唯一可能引发生产事故的真实问题，executor 卡死会泄漏 goroutine 和数据库连接，长期运行会撑爆 worker pool。
2. **B-4 测试增强** — 新增对抗性 `ShellEscape` 测试，加默认黑名单字符校验。机制本身无 bug，但测试覆盖不足让回归保护薄弱。
3. **B-8** 可放入下一波重构，与全站时间规范化一起处理。
4. **B-5/B-6** 不修复，但应在审查报告里**显式撤回**误报，避免误导后续维护者。

---

## 子代理审查报告整体可信度评估

5 项 finding 中：
- **2 项纯误报**（B-5, B-6）— 错报率 40%
- **2 项夸大严重性**（B-4 称"可能被绕过"实测安全；B-8 称"导致漂移"实测仅风格问题）
- **1 项准确定位**（B-7）

子代理倾向于"看到模式就下结论"而非完整阅读控制流（如 B-5 漏看了 `Run()` 主循环、B-6 漏看了 `Locked` 后缀的调用约定）。在没有人工复核前，**不应直接基于该报告改代码**。
