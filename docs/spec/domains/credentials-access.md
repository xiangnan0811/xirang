# 凭据与访问

本文统一规定身份验证、SSH scope、临时凭据授权、凭据审计及敏感设置的存储、API 和前端边界。通用数据访问与加密迁移机制见[数据库规范](../backend/database-guidelines.md)，通用 DTO 映射见[类型安全](../frontend/type-safety.md)。

## 认证、角色和资源权限

每条受保护路由同时检查主认证、有效角色、路由权限及所需资源归属。权限字符串必须对应实际 `middleware/rbac.go` 中至少一个预期角色；不能靠 handler 单测替代真实认证/RBAC 路由测试。缺失或过期 token 返回 401；未知、缺失或无权限角色返回 403。管理凭据、系统设置、恢复及含秘密配置默认拒绝未获授权角色，导航只提供实际可用入口。

管理员通配权限和真实的 operator/viewer 权限矩阵以 `rbac.go` 为准，不复制虚构的角色 map 示例。新增权限用完整 router 测试验证允许和拒绝路径，敏感资源还须验证已有记录和实际变更操作。

登录双验证码由 `login.captcha_enabled`、`login.second_captcha_enabled` 独立控制：仅启用的通道返回 ID/question 并要求答案，两者启用时使用不同 challenge，不能互相替代。challenge 五分钟有效，无论答对答错都一次性取走；缺 store、非法 ID/答案、过期及重放在密码认证前失败关闭。旧自由文本 `captcha`/`second_captcha` 不构成证明。challenge 和登录共用登录限流器。前端只展示/提交启用通道，失败后清空并重新获取；四种开关组合、缺 store、跨通道、重放及 429 均须回归。

两步登录、TOTP 初始化和离线恢复使用持久化的一次性状态及审计；迁移中历史未完成初始化失效，已启用 TOTP 不受影响。使用过的认证或恢复审计状态禁止破坏性降级。密码验证（包括关闭 TOTP）比较原始密码字节，不 trim。

pending login token 绑定当前账户版本和 TOTP 状态，完成后只能消费一次；恢复码用并发安全事务消费，正常登录不撤销其他合法会话。初始化返回 `enrollment_id/expires_at`，验证必须提交同一未过期标识；再次初始化使旧标识失效，已启用账户不能覆盖密钥。角色变更及删除用户必须在并发下仍保留至少一个管理员；启动不自动提升现有账户。离线 `recover-admin` 只在已迁移数据库且零管理员时提升一个已有账户，不执行迁移、不创建用户、不改密码/TOTP；明确本地确认后原子撤销旧版本会话/待登录并保存原因及哈希链审计，任一失败不提升权限。操作前必须停机并保存数据库和匹配密钥，不能删除审计绕过保护。

## 按操作绑定的 step-up

服务端标准挑战协议为 HTTP 403 且错误信封 `data.error_code="STEP_UP_REQUIRED"`，触发二次验证；`data.error_code="CREDENTIAL_GRANT_REQUIRED"` 属于另一临时凭据授权分支，两类挑战互斥。前端读取 `ApiError.detail.data.error_code`，并要求 `ApiError.status=403`。普通 403、缺失或未知机器码、非对象 `data` 均不识别为对应挑战。

客户端现有兼容边界：HTTP 失败响应使用真实 HTTP 状态作为 `ApiError.status`，所以非 403 的失败响应即使携带上述码也不识别；但 HTTP 成功响应若携带失败信封 `code=403`，`request()` 会以该信封 code 构造 `ApiError.status`，仍识别匹配的挑战。这不是仅允许真实 HTTP 403 的保证，也不授权服务端生成这种非标准响应；本兼容行为不改变服务端 HTTP 状态与 code 一致的要求。

- `asset.secret_reveal` 的签名有效期严格为 2700 秒且不滑动；其他已注册操作维持 300 秒及原有复用规则。未知/未来操作无默认 TTL，失败关闭。
- 校验签名 purpose、精确 action、proof JTI、user ID/subject、role、token version、TOTP 启用状态、iat、exp、精确操作 TTL；`asset.secret_reveal` 还校验当前登录 session JTI 及撤销状态。复用不改变 iat/exp。
- JWT 每段使用严格 Base64URL 解码；解码字节相同但文本非规范的 token 仍非法。响应 `expires_at` 等于签名 exp，响应和审计 `proof_ttl_seconds` 来自同一策略，非法操作为零。
- 响应、日志、审计不含 proof/session token、OTP、内容、locator、ticket 或原始错误。回归覆盖全操作和未来操作、跨 purpose、篡改、过期、身份/角色/TOTP/版本变更、会话不匹配及撤销、不滑动复用、确定性的非规范 Base64URL 同字节变体；相关修改运行重复与 race 测试。

## 受管 SSH Key scope

`ssh_keys` 存储 `disabled`（默认 false）、可空 `expires_at` 以及默认空串的 `allowed_purposes`、`allowed_node_ids`、`allowed_node_tags`。列表以归一化逗号文本存储，由 Go 精确匹配，不能用 SQL 子串授权。空 scope 表示该维度不受限，是兼容合同。

- 禁用或 `expires_at <= now` 的密钥在使用私钥之前拒绝，包括测试和导出。purpose 非空时必须包含当前用途；节点 ID 非空须精确匹配；标签非空须有一个精确匹配；ID 和标签同时设置时都须满足。
- 新调用点必须用 `BuildSSHAuthForPurpose`、`BuildSSHAuthWithKeyForPurpose` 或 `DialSSHForNodePurpose` 等共享 purpose helper。兼容的无用途 helper 不可复制到新边界。
- 闭合用途包括 `ssh_key_test`、`ssh_key_export`、`node_test`、`terminal`、`task_command`、`batch_command`、`drill`、`probe`、`file_browser`、`docker_volumes`、`node_logs`、`task_backup`、`task_restore`、`task_hook`、`snapshot`、`snapshot_diff`、`integrity_check`、`retention`、`node_migration`。新增值同步归一化、调用方和回归；创建/更新拒绝未知值。
- scope 只约束受管 `ssh_keys`。节点内联密码/私钥仍须凭据审计，但不伪称受 SSHKey scope 控制。拒绝信息不得泄露密钥、密码、用户名加主机、endpoint、执行配置或原始 SSH/SQL/加密错误。
- `GET /ssh-keys/export` 只导出满足 `ssh_key_export` 的密钥，被拒绝项计入安全审计数量但不进入 payload。配置导入导出始终保留 scope 元数据；私钥仅在 `include_secrets=true` 时导出，导入复用 scope 归一化工具。
- API 正常响应不返回私钥；`broad_scope` 是响应派生风险标记。更新区分缺字段与显式清空，创建、更新、批量导入均传递 scope。

前端只使用映射后的 `disabled/expiresAt/allowedPurposes/allowedNodeIds/allowedNodeTags/broadScope`；未知 key type 回退 `auto`，非法数值回退有限值，缺 broad scope 不自行推导 true。过期时间转换为安全的 `datetime-local` 值，非法写入时间序列化为 null，不产生 Invalid Date。表单解释空维度兼容含义；列表以文字和颜色区分禁用、到期、宽范围及受限状态，控件有 label，装饰图标隐藏于辅助技术。scope 卡片不补充敏感连接数据或一键变更入口。

回归覆盖空 scope 兼容、过期等于当前时间、用途/节点/标签拒绝、归一化去重与未知值、显式清空、无秘密导入导出 round trip、至少一个共享 SSH 路径和一个 executor 路径；前端覆盖 mapper、编辑及列表徽章和无障碍标签，批量示例仅用明显假密钥。

## SSH 主机密钥信任

`SSH_STRICT_HOST_KEY_CHECKING` 只由环境变量控制（默认 true）。首次连接是否自动接受未知主机密钥由动态设置 `ssh.auto_accept_new_hosts` 决定，优先级 DB > `SSH_AUTO_ACCEPT_NEW_HOSTS` > 默认 false，保存后无需重启即生效；`sshutil.ResolveSSHHostKeyCallback` 的 Go SSH 回调、rsync/受管 Rsync executor 与安全风险卡片 `ssh_host_key_trust_posture` 统一经 `sshutil.AutoAcceptNewHosts()`（服务启动时安装 settings 取值源）读取。Fleet Doctor 是只读诊断，自带严格 known_hosts 回调，从不自动接受或写入 known_hosts。非法值按拒绝处理；受管 Rsync 仍在非法值时失败。已知主机的密钥变化（含改用未记录的算法）无论设置如何都拒绝。

Xirang 对 known_hosts 的全部写入（Go 回调自动接受、人工信任、外部 OpenSSH 的首次登记）都在 `sshutil` 的同一写锁内重新读取文件后判定：已有相同密钥视为已信任，已有其他密钥按 mismatch 拒绝，因此同一主机不会并存两把不同密钥。rsync 与受管 Rsync 调用的外部 OpenSSH 一律带 `StrictHostKeyChecking=yes` 与 `UpdateHostKeys=no`（`sshutil.OpenSSHKnownHostsWriteGuard`；严格校验本身不阻止 OpenSSH 在认证后按 UpdateHostKeys 追加服务器公布的密钥），自身从不写 known_hosts。自动接受开启时，执行前由 `sshutil.RegisterNewHostKeyWithOpenSSH` 用与实际传输相同的 ssh 命令前缀（同一二进制、`-F` 隔离与端口；因此沿用 ProxyJump/ProxyCommand/HostName/HostKeyAlias/CA 及已记录密钥类型偏好）对 known_hosts 私有副本以 `accept-new` 探测，`PreferredAuthentications=none`、`BatchMode`、禁用复用与转发，确保在向目标主机提供任何密钥、密码或 agent 凭据前结束（跳板主机的认证与实际传输一致）；探测在独立进程组中运行（Linux），取消、30 秒超时或正常结束时整组回收，ProxyCommand/ProxyJump 辅助进程不会遗留；OpenSSH 追加到副本的条目在写锁内重新校验后并入 known_hosts（HashKnownHosts=no，以其解析后的主机形式记录）。副本无新增（不可达、已知或 OpenSSH 拒绝了变化的密钥）时不登记，由严格传输报告结果。Go 侧拨号与探测（`DialSSH`、节点探测、测试连接、信任探测）经 `sshutil.HostKeyAlgorithmsForAddress` 优先协商 known_hosts 中该主机已记录的密钥类型，其余算法仍在后备列表中；因此多密钥服务器上已记录的 Ed25519/RSA 密钥不会因 Go 默认偏好 ECDSA 而被误判为 mismatch，而已记录类型的密钥真的变化时仍报 mismatch。命中 `@cert-authority` 记录时优先协商证书算法（CA 自身的密钥类型不代表服务器原始密钥类型）。剩余风险：该锁只协调 Xirang 进程内的写入，管理员手工编辑或其他进程并发改写 known_hosts 不在保证范围内。

校验失败返回 `sshutil.HostKeyError`（`unknown|mismatch`），消息固定且不含主机名、地址或 known_hosts 路径，指向页面操作而非环境变量；以 `%w` 透传的任务 last_error 与 Docker 卷发现 502 消息沿用该文本，终端 WebSocket 关闭原因使用不超过 123 字节的对应短文本，Doctor 建议指向节点页「测试连接」。`POST /nodes/:id/test-connection` 在拨号阶段命中该错误时仍返回 200 `ok=false`，附加 `error_code`（`ssh_host_key_unknown|ssh_host_key_mismatch`）与 `host_key.{algorithm,fingerprint_sha256}`，其他失败保持泛化消息；凭据审计 metadata 增加 `host_key_issue`。

`POST /nodes/:id/trust-host-key` 仅 admin（`RequireRole("admin")` + 节点 ownership），body `{fingerprint_sha256:"SHA256:..."}`，空或非 `SHA256:` 前缀 400。服务端重新连接节点，只在 host key 回调内捕获密钥后立即中止握手，**不向未信任主机发送任何凭据**；先比对当前指纹与提交值，不一致即 409 `ssh_host_key_changed`（即使当前密钥已受信任，也不以幂等成功掩盖确认对象的变化），一致后才在 known_hosts 写锁内重新读取文件：已记录则 `already_trusted`，无冲突则追加。结果：成功 200 `{trusted, already_trusted, algorithm, fingerprint_sha256}`；指纹已变化 409 `ssh_host_key_changed`；已有冲突记录 409 `ssh_host_key_mismatch`（不提供覆盖）；strict 关闭 409 `ssh_host_key_checking_disabled`；无法连接 502。每次请求写 `node.host_key.trust` 凭据审计（success/blocked/failure，metadata 含算法、指纹、`already_trusted`、`stage=host_key_trust`）。

前端节点页三个测试入口（行/卡片、编辑器、保存后自动测试）遇结构化主机密钥失败时弹窗展示算法与指纹，不再 toast。unknown：admin 可「信任并重试」或前往 系统设置 → 安全；非 admin 仅提示联系管理员。mismatch：仅安全警告，任何角色均无信任按钮。信任请求进行中弹窗不可关闭，也不会被其他节点并发返回的主机密钥结果替换（该结果改为 toast），失败信息留在原弹窗；信任成功后关闭弹窗再重测，普通测试结果只 toast、不改动弹窗。mapper 只接受上述两个 code 且指纹非空，其余视为普通失败。回归覆盖未知/信任/重连、重复信任幂等、已信任其他密钥时提交旧指纹仍 409、错误指纹与冲突不改文件、并发/过期快照不写入第二把密钥、rsync 与受管 Rsync 始终严格并经 Go 路径登记、多密钥主机保留已记录的非默认类型密钥、零认证尝试、真实路由权限、DB 覆盖 env 即时生效，以及前端 mapper、三种弹窗状态、进行中关闭与并发探测。

## 临时凭据授权与终端

`credential_access_grants` 是后端行记录授权，不是 bearer token。仅保存请求者/审批者安全 ID 与标签、action、purpose、资源 ID、status、UTC expiry、TTL 和有界脱敏 reason；不得保存 token、step-up、OTP、命令、流、文件、导出内容、原始 SQL、endpoint/proxy 或敏感主机信息。

终端授权通过 `POST /api/v1/credential-access-grants/terminal` 创建，主认证 + admin + step-up 后可按当前单管理员部署流程自批准。匹配元组是 `(requester_user_id, requester_role, action=terminal.open, purpose=terminal, node_id)`，同时要求活动状态和未来 expiry。

终端 WebSocket 首消息按主 token、admin、step-up、node ID、grant 的顺序检查，随后才加载节点、解析凭据和 SSH dial。grant 是附加控制，不能替代 token purpose、角色、资源权限、step-up 或 SSH scope；因此同样保护内联节点凭据。缺失、过期、撤销、拒绝、用户/角色/操作/用途/节点不符均失败关闭，使用机器码 `CREDENTIAL_GRANT_REQUIRED` 等安全拒绝信号。

已打开终端不晚于 JWT 或 session 时限关闭，并定期及每次转发输入前重新核对持久化撤销和当前用户权限；无法确认即关闭。关闭使用有界 WebSocket control write，先关闭 SSH transport 再等待 worker；终端关闭审计共用有界预算，写入失败不无限延长 handler。

前端通过 typed API 将 `nodeId/reason/requestedTtlSeconds` 映射为 `node_id/reason/requested_ttl_seconds`，step-up 走既有请求 header/option；默认 TTL 为 10 分钟，可请求 1–30 分钟，reason 上限为 240 个字符。DTO 全部映射 camelCase，未知 status 回退不授权状态，非法 ID/TTL 不得成为 NaN，缺审批者不得虚构。grant、reason、拒绝状态均不进入 localStorage/sessionStorage。

终端 reason dialog 状态仅保存在组件；复用 `ensureStepUpProof()`，一次性 grant/action 请求使用不持久化 proof，不复用缓存。grant-required close 不视为登录过期，不卸载父终端；脱敏且有界地展示 detail。空/超长 reason 阻止提交并有可访问提示。成功清理瞬态草稿并通过原连接流程重试一次；失败保留 dialog，取消清理状态且不重试，不改 auth session。

当前其他已实现的 grant 通过 `/credential-access-grants/` 子资源创建，并在真实操作前匹配，不能复用 terminal grant：

| 创建子资源 | action / purpose | 资源与边界 |
|---|---|---|
| `config-import` | `config.import / config_import` | admin 系统级，任何导入写入前 |
| `config-export` | `config.export / config_export` | admin 系统级，仅 `include_secrets=true`，读取/序列化秘密前 |
| `snapshot-restore` | `snapshot.restore / snapshot` | admin，当前 Task ID，远端恢复前；列表/浏览/搜索/diff 无此要求 |
| `task-restore` | `task.restore_trigger / task_restore` | admin，当前 Task ID；创建仅验证存在性和当前恢复资格，不保存目标路径/payload |
| `task-manual-trigger` | `task.manual_trigger / task_command` | 当前 Task ID，任务触发权限及 ownership |
| `task-batch-trigger` | `task.batch_trigger / task_command` | 每个去重后的 Task ID，任务写权限及 ownership |
| `batch-command` | `batch_command.create / batch_command` | 每个去重后的 Node ID，任务写权限及 ownership |

以上均叠加对应 action step-up 和主认证。管理员 grant 列表是只读筛选/查看入口，不提供批准、拒绝或撤销按钮。回归覆盖 TTL/reason/DTO、真实 RBAC 与 step-up、全部元组不匹配及到期边界、gate 早于秘密读取或执行、授权创建/使用/拒绝审计、终端撤销与关闭、grant-required dialog 重连和无浏览器持久化。

已知实现差异：`web-terminal.tsx` 当前两次调用 `ensureStepUpProof(terminalOpen)` 未显式禁用持久化和缓存，而 auth provider 默认 `persist=true` 并存储 proof，未满足上面的单次操作要求。该要求继续有效；本次文档整合不修改产品实现，也不将该路径记为验收通过。

## 凭据使用审计

`credential_audit_events` 是领域事件表，补充 hash-chain HTTP `audit_logs`，覆盖 GET、WebSocket 和后台执行。SQLite/PostgreSQL 的 metadata 都是 text JSON；需要检索的事实加明确列和索引，不依赖专属 JSON 查询。

事件记录安全 actor/resource ID、action/purpose、credential kind/source、安全结果 `success|failure|blocked`、脱敏 error 和小型 metadata。source 用 `ssh_key_id=<id>`、`node.password` 等标签，不含值。维护测试、导出、节点测试、terminal open/failure/close、任务 manual/restore/batch/运行时用凭据、drill、文件浏览、卷发现、配置导出、Doctor、迁移预检、probe、metrics、节点日志等调用点的现有动作身份。

禁止密码、私钥、TOTP/JWT/recovery code、解密执行配置、终端流、SFTP/file/export payload、Docker 输出/卷名、诊断输出及完整命令。metadata 只保存数量、阶段、格式/scope、安全 hash、run ID、延迟和布尔值；含 `private/password/token/secret/credential/config/output/stream/command/content/payload` 的 key/value 丢弃。错误使用共享 sanitizer，输出标记 `输出:`、`output:`、`stdout:`、`stderr:` 后替换为 `[REDACTED_OUTPUT]`。字段必须有界；JSON marshal 失败保存 `{}`。

一般调用点尽力写审计：nil DB 或无 runtime context 为 no-op，写失败仅由 `logger.Module("credential_audit")` 发安全警告，不改变主操作结果；有明确原子审计要求的操作（如 [Legacy Rclone reconciliation](task-execution-recovery.md#旧版-rclone-可变代次)）必须失败回滚，不能套用默认尽力规则。runtime 通过 `WithRuntimeContext/WriteRuntime` 保持 task/run/policy/node/actor 关联。后台只记录有意义的阻断/失败及稀疏重复 probe 失败，不逐成功 dial 写无限事件。

回归验证禁用 metadata key/value、字段长度和输出脱敏、terminal 不含输入输出、各使用点安全身份与资源关联、runtime 合并上下文、无 DB/context，以及显式原子审计边界的失败回滚。

## 敏感存储与设置风险摘要

`policies.pre_hook/post_hook` 含秘密，模型 hook 保存时加密、读取时解密。非管理员响应为空，非管理员非空输入 403，更新空/隐藏字段保留原 hook；管理员可按命令验证规则设置或清空。应用 profile 自动 hook 仅在执行时由加密 app credential 渲染，创建/更新只保存 profile 和 credential ID，不持久化生成的含密码命令。

敏感 setting（如 `smtp.password`、`metrics.remote_bearer_token`）注册 `Sensitive: true` 并加入 v1→v2 迁移 allowlist；Update/UpdateWithTx 入库前加密，空值可为空，在服务边界解密。GetEffective 数据库读取失败时保留已有过期缓存，不能用 env/default 覆盖缓存。迁移计数覆盖 policy、app credential、integration proxy 和全部敏感 setting。监控 HTTP headers 也加密且不直接 JSON 输出，API 只公开配置标记和头名；启动回填不可解密/无法保存时不就绪。环境覆盖和当前实现例外见[环境变量](../../env-vars.md)。

监控 `http_headers` 为 JSON 对象字符串且仅写入，查询不返回值或 `***`；更新省略时只有类型、完整目标（含路径/查询参数）、HTTP method 都未变才保留旧配置。用途变化必须显式替换或提交字符串 `"{}"` 清空，否则返回 409 与 `service_monitor_target_change_requires_headers`；并发变更返回 `service_monitor_concurrent_update`，客户端重新加载再编辑。Task 所有响应中的嵌套 policy 仅含 `id/name`，管理员也不能经任务接口读到解密 hook/演练脚本。

`GET /api/v1/settings/security-risk-summary` 只读且仅 admin，任何风险项都不修改节点、密钥、配置、known_hosts 或远端。类别为 `root_ssh_users`、`reused_ssh_keys`、`sudo_enabled_nodes`、`broad_scope_ssh_keys`、`disabled_ssh_keys_in_use`、`expired_ssh_keys_in_use`、`stale_ssh_keys`、`recent_credential_operations`、`weak_security_defaults`；零发现仍保留类别。examples 只含脱敏节点/密钥名字、设置标签或 action 数量，限制为实现的 `maxSecurityRiskExamples`，总 count 独立返回；不回显审计 metadata/error 或连接信息。查询失败返回标准 internal error，不包装成部分成功。

前端 `generated_at/total_risks` 映射 camelCase，未知 code 回退已知安全值，未知 severity 回退 warning，非法数量为零，缺数组为空；卡片只显示建议，不提供停用、旋转、重设 scope 等一键操作，请求失败不把旧结果标为当前确认。回归覆盖权限、全部风险类别、零项、有界脱敏例子、mapper、无变更入口，以及 policy hook 加密/可见性/保留和完整加密迁移。
