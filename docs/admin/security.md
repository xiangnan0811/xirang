# 安全加固

本文档汇总生产部署 Xirang 时应关注的安全配置。漏洞报告方式见 [安全政策](../../SECURITY.md)。

## 必填密钥

生产环境必须设置强随机值：

| 变量 | 用途 | 建议 |
|---|---|---|
| `ADMIN_INITIAL_PASSWORD` | 首次启动且库中尚无 `admin` 时创建该用户 | 一次性强密码，登录后尽快修改；**已有 admin 后不再需要**，也不会因留空而拒绝启动。 |
| `JWT_SECRET` | JWT 签名密钥 | 至少 32 字符强随机字符串；可用 `openssl rand -hex 32`。 |
| `DATA_ENCRYPTION_KEY` | 敏感字段加密密钥 | 至少 16 字符；建议 32 字节 base64。 |
| `METRICS_TOKEN` | 保护 `/metrics` 的 Bearer token | 至少 16 字符强随机值，禁止文档占位符；可用 `openssl rand -hex 32`。 |

除显式 `APP_ENV`/`ENVIRONMENT=development` 外（含未声明环境），弱密钥或缺失 `JWT_SECRET` / `DATA_ENCRYPTION_KEY` / `METRICS_TOKEN` 会导致服务拒绝启动。`GIN_MODE` 不会放宽该策略。`ADMIN_INITIAL_PASSWORD` 仅在库中不存在 admin 用户时由 bootstrap 校验。

## HTTPS

All-in-One 容器只提供 HTTP 单入口 `10761`。公网 HTTPS 应由外部反向代理或负载均衡终止 TLS，例如 Caddy、Nginx Proxy Manager、Nginx 或云厂商网关。

反向代理建议：

- 只将外部代理暴露到公网，按需限制宿主机 `10761` 端口的访问来源。
- 保留 `Host`、`X-Forwarded-For`、`X-Forwarded-Proto` 请求头。
- 支持 WebSocket Upgrade，确保实时日志和终端连接可用。
- 使用外部域名访问时，在 `.env` 中配置 `CORS_ALLOWED_ORIGINS=https://xirang.example.com`。

## SSH 主机校验

生产环境建议保持：

```env
SSH_STRICT_HOST_KEY_CHECKING=true
SSH_AUTO_ACCEPT_NEW_HOSTS=true
SSH_KNOWN_HOSTS_PATH=/data/.ssh/known_hosts
```

含义：

- 首次连接新主机时自动接受并持久化指纹。
- 已知主机指纹变化时拒绝连接，避免中间人攻击。
- All-in-One 镜像默认把 known_hosts 放在 `/data` 下，随数据卷持久化。

如需禁用首次自动接受，设置：

```env
SSH_AUTO_ACCEPT_NEW_HOSTS=false
```

## Webhook / 通知 SSRF 防护

生产环境建议保持：

```env
INTEGRATION_BLOCK_PRIVATE_ENDPOINTS=true
```

该配置会阻断 Webhook、Slack、Telegram 等通知端点指向私网或回环地址，降低 SSRF 风险。

## 登录防护

相关配置：

| 变量 | 默认值 | 说明 |
|---|---|---|
| `LOGIN_RATE_LIMIT` | `10` | 登录接口速率限制次数。 |
| `LOGIN_RATE_WINDOW` | `1m` | 登录速率限制窗口。 |
| `LOGIN_FAIL_LOCK_THRESHOLD` | `5` | 连续失败多少次后锁定账号。 |
| `LOGIN_FAIL_LOCK_DURATION` | `15m` | 锁定持续时间。 |
| `LOGIN_CAPTCHA_ENABLED` | `false` | 登录验证码默认值，可在系统设置中调整。 |
| `LOGIN_SECOND_CAPTCHA_ENABLED` | `false` | 二次验证码默认值，可在系统设置中调整。 |

用户可在个人设置中启用 TOTP 两步验证，兼容 Google Authenticator 等应用。

## 高风险临时授权

Web SSH 终端在打开会话前需要同时满足：有效的 admin 主认证、TOTP 二次验证 proof，以及绑定当前用户、`terminal.open` 操作、`terminal` 用途和目标节点的短时授权。授权由管理员在终端弹窗中填写原因并通过二次验证后自助创建，默认有效期很短，到期、撤销、拒绝或资源不匹配都不会放行。

配置导入在执行前同样需要有效的 admin 主认证、TOTP 二次验证 proof，以及绑定当前用户、`config.import` 操作和 `config_import` 用途的短时系统级授权；缺少、过期、撤销、拒绝或不匹配的授权会在导入写入前被拒绝。

含敏感字段的配置导出（`include_secrets=true`）需要有效的 admin 主认证、TOTP 二次验证 proof，以及绑定当前用户、`config.export` 操作和 `config_export` 用途的短时系统级授权；缺少、过期、撤销、拒绝或不匹配的授权会在读取或序列化敏感配置前被拒绝。普通配置导出不包含敏感字段，不需要临时授权。

快照恢复是远端文件恢复/覆盖类高风险操作。`POST /tasks/:id/snapshots/:sid/restore` 需要有效的 admin 主认证、TOTP 二次验证 proof，以及绑定当前用户、`snapshot.restore` 操作、`snapshot` 用途和当前任务 ID 的短时任务级授权；缺少、过期、撤销、拒绝或任务不匹配的授权会在恢复执行前被拒绝。快照列表、文件浏览、搜索和差异比较不需要该恢复授权。

任务恢复触发同样会写入恢复目标。`POST /tasks/:id/restore` 需要有效的 admin 主认证、TOTP 二次验证 proof，以及绑定当前用户、`task.restore_trigger` 操作、`task_restore` 用途和当前任务 ID 的短时任务级授权；缺少、过期、撤销、拒绝、用户/角色变化或任务不匹配的授权会在进入恢复执行前被拒绝。授权创建只校验任务存在性和当前恢复资格，不记录恢复目标路径或恢复 payload。

授权记录和凭据审计只保存用户、角色、操作、用途、资源 ID、状态、TTL 等安全字段。管理员可在“临时授权”页面只读查看授权状态、筛选条件和生命周期时间；该入口不提供批准、拒绝或撤销操作。Xirang 不记录导入文件内容、导出文件内容、终端输入/输出、命令文本、文件内容、恢复目标路径、快照文件列表、私钥、密码、令牌、主机地址或代理端点；终端录屏/回放不属于当前内置能力。

## 备份资产 Worker 与 updater 信任边界

备份资产 Worker 当前是默认关闭、非 GA 的可选本地 profile，没有稳定公共 Worker 镜像或 Docker Hub/GitHub Release 发布合同。官方 All-in-One Core 和公开端口 `10761` 不变。未部署 Worker 时，Catalog、原生预览、下载和 recovery 继续可用。

Parser Worker 使用固定 non-root UID/GID `10000:10000`，read-only rootfs、drop-all capabilities、`no-new-privileges`、reviewed seccomp、PID/CPU/memory 上限和 `noexec,nosuid,nodev` job tmpfs。仓库 Compose 把 `memswap_limit` 固定为与 `mem_limit` 相同，禁止 parser/updater 容器使用 swap；运维侧仍应关闭宿主 swap，或只使用经过审计的全盘加密 swap，避免其他运行方式让敏感 tmpfs 页面落到明文交换空间。仓库 Compose 为 parser Worker 设置 `network_mode: none`，不配置 DNS；它只读挂载 `asset-worker-worker-runtime` 到 `/run/xirang/worker`，连接 mode `0600`、owner `10000:10000` 的 Core UDS，并只读挂载 active bundle。Worker 不加入 updater GID，也不挂载 `asset-worker-updater-runtime`、`/data`、`/backup`、`/logs`、Docker socket、updater inbox/credential 或任何 Provider 源路径。

所有 parser/tool 调用来自服务端闭合 capability/profile：不经过 shell，不接受调用方 executable、argv、环境变量、codec、字体、模型、路径、URL 或工具配置。输入只来自一次性 attempt-bound grant，输出在 Core 再次检查 MIME、数量、大小、digest、coverage 和安全策略后才能发布。取消、超时或 fence 丢失会终止整个进程组并清理私有 workspace；缺少可验证 tmpfs/Landlock/seccomp 合同时 capability 不会被 advertised。

Updater 与 parser 使用不同进程、UID/GID `10002:10002`、PID namespace、socket 和可写 bundle volume。Updater 只读挂载独立的 `asset-worker-updater-runtime` 到 `/run/xirang`，不挂载 `asset-worker-worker-runtime`，因此不能观察 parser socket；parser 的隔离方向与之对称。Core 使用 setgid mode `2770`、owner/group `10000:10002` 的 updater runtime 创建 mode `0660`、owner/group `10000:10002` 的 `/run/xirang/asset-worker-updater.sock`，并在解码 receipt 前校验 socket 与 Linux peer credential。跨 PID namespace 时 `SO_PEERCRED` 的 peer PID 可以是 `0`；PID 只作为诊断元数据，不是授权主体，认证仍由受保护 UDS、精确 UID/GID 与 socket owner/mode 共同完成。

Content-addressed bundle volume 是唯一的共享数据 mount：根目录为 updater owner、Worker reader group `10002:10000` 与 setgid mode `2750`。Updater mount 可写，parser mount 强制只读，因此 parser 可验证 active bundle 但不能修改 store。Inbox 与 Ed25519 trust secret 仍只挂载到 updater。Socket volumes、bundle volume 与 secret mounts 不能互相替代或合并。

Core 加密 Derived Store 使用单独的 `asset-worker-derived-store` named volume，由 initializer 固定为 `0700:10000:10000` 并挂载到 `/var/lib/xirang-asset-runtime/derived`。只有 Core 与 initializer 能看到该 volume；parser/updater 不挂载它，且 private-runtime guard 会拒绝把 Derived root 放到 `/data`、`/backup`、`/logs` 或已知 Provider 源路径下。

默认更新路径是 signed offline import：运维人员把候选目录放入固定 updater-only inbox，目录要求 `10002:10002`、mode `0555`；Ed25519 trust 文件要求 `10002:10002`、mode `0440`。Updater no-follow 扫描并验证 canonical manifest、Ed25519 signature、精确 tar/file SHA-256、大小/时间/路径/类型限制，fsync content-addressed store 后通过 journal 与原子 pointer rename 激活。浏览器和 Core HTTP API 不接收 bundle bytes、multipart、URL、服务器路径、inbox 文件名或原始 manifest；Admin API 只使用脱敏 candidate ID 和 expected fingerprint 的小型 JSON 控制请求。

仓库 Compose 对 updater 同样使用 `network_mode: none`，只支持 offline-only。Online updater 默认关闭；若未来单独部署，必须同时具备 exact HTTPS origin allowlist、独立 allowlist proxy/firewall、隔离网络和 updater-only credential secret。应用层 allowlist 不能代替 egress firewall，parser Worker 永远不得继承 updater 网络或凭据。

Malware 结果区分 `not_scanned`、`no_finding`、`finding`、`stale`；positive finding 是成功扫描结果，不会被当成失败重试或篡改 RecoveryPoint 信任状态。Preview job、Derived ticket/read 和 Search release 均由服务端重新执行 malware/sensitivity gate。有限秘密分类默认关闭，并且只能加强 Core 结论；`unknown`/`secret` 在缺少精确 proof 时继续失败关闭。

处理 API、日志、指标、审计和管理聚合只记录闭合 capability/profile/state/error category、opaque 资源引用及有界计数。禁止记录 Provider locator、宿主/tmp/bundle/inbox 路径、Worker UID/PID/证书、credential、grant/session/attempt/fence/activation secret、原始 argv/stdout/stderr/tool diagnostic、manifest/body 或源内容。回退只关闭 settings/可选 profile并保留数据；不得删除 Provider bytes、RecoveryPoint、Catalog 或源备份。

## 敏感字段保护

Xirang 会加密存储 SSH 密码、SSH 私钥、TOTP 密钥、通知端点、代理地址等敏感字段。请妥善备份 `DATA_ENCRYPTION_KEY`；数据库备份没有对应密钥时无法恢复敏感字段明文。

备份资产控制面同样依赖该密钥。仓库访问绑定、冻结原因和 wrapped domain key 只有在恢复原数据库 **并且** 使用匹配的 `DATA_ENCRYPTION_KEY` 时才可读。仅保留 Provider 仓库只能在 Admin 有效重连/导入后重建可验证的 RecoveryPoint/Catalog 事实，不能重建 overlays、审计、策略、冻结或 Task 关系。错误或缺失密钥必须失败关闭，不得静默换绑或把 rebuild 报成成功。详见 [备份、恢复与快照](./backup-recovery.md#控制面灾难恢复)。
