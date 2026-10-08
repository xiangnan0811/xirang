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
SSH_AUTO_ACCEPT_NEW_HOSTS=false
SSH_KNOWN_HOSTS_PATH=/data/.ssh/known_hosts
```

含义：

- 首次连接未知主机时拒绝连接。节点页「测试连接」（含保存后的自动测试）会弹窗显示服务器算法与 SHA256 指纹；通过独立可信渠道（如在服务器控制台执行 `ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub`）核对一致后，管理员可点击「信任并重试」写入 known_hosts。服务端确认时只读取主机密钥，不向未信任主机发送凭据；指纹与弹窗时不一致会拒绝写入。非管理员只能看到指纹并联系管理员。也可以继续手工预置 known_hosts。
- 已知主机指纹变化时拒绝连接，避免中间人攻击。页面只显示安全警告，不提供一键覆盖；核实服务器确已重装后，由管理员手工更新 known_hosts。
- All-in-One 镜像默认把 known_hosts 放在 `/data` 下，随数据卷持久化。

只有明确接受首次连接信任风险时，才开启「自动接受新主机密钥」：在 系统设置 → 安全 修改 `ssh.auto_accept_new_hosts`，保存后立即生效，无需重启；未在页面设置时回退 `SSH_AUTO_ACCEPT_NEW_HOSTS`（默认 `false`）。此选项会自动记录未知主机密钥，但不会允许已知主机密钥变化；`strict=true` 本身不代表首连身份已经人工核验。`SSH_STRICT_HOST_KEY_CHECKING` 仍只能通过环境变量设置。rsync 类任务调用的 OpenSSH 始终使用 `StrictHostKeyChecking=yes` 与 `UpdateHostKeys=no`，自身不写 known_hosts；开启自动接受时，Xirang 在执行前沿用同一 ssh 路由（包括 `~/.ssh/config` 中的 ProxyJump/ProxyCommand）做一次不提交任何凭据的探测并统一登记，以保证同一主机不会记录两把不同密钥。该保证只覆盖 Xirang 自身的写入，请避免在服务运行、备份执行期间手工改写 known_hosts。

## SSH 密钥轮换

管理员在 SSH Key 页选择密钥后，输入新私钥并点击「检查候选密钥」。
检查只解析公钥，不连接节点、不保存密钥；确认页提供可复制公钥、标准公钥指纹
及受影响节点数。请由管理员先在目标主机部署新公钥，再确认保存。
禁用键不能通过轮换启用：应先编辑补私钥，复核用途、目标节点/标签和到期时间，
再手动启用并验证关联节点。

「密钥已更新」不代表所有节点可连接。轮换会验证全部关联节点，部分失败或
结果未知单独提示；修复远端公钥部署后可「重新验证失败或未知节点」，不会再次
保存密钥。保存请求结果未知时，先刷新实际密钥状态，再由用户重新开始，不自动重试。
列表和 CSV/JSON 的历史 `fingerprint` 是私钥摘要，不是 SSH 公钥指纹；
请用确认页的标准公钥指纹与 `ssh-keygen -lf <公钥文件> -E sha256` 核对。

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

用户可在个人设置 → 账户安全启用 TOTP 两步验证，兼容 Google Authenticator 等应用。
敏感操作尚未启用 TOTP 时会提供账户安全入口。验证成功后当前标签页自动换发会话，
无需正常重新登录；请先保存本次显示的恢复码并勾选确认，再点击完成。
「返回操作」只返回业务页，必须由用户重新确认操作，不会自动执行先前提交。
其他已登录标签页的旧会话会失效。若激活响应丢失或无法安装新会话，请重新登录；
页面会提示可能已启用，未收到的恢复码无法从本次响应恢复。验证码明确错误时可修改后重试。

## 高风险临时授权

Web SSH 终端在打开会话前需要同时满足：有效的 admin 主认证、TOTP 二次验证 proof，以及绑定当前用户、`terminal.open` 操作、`terminal` 用途和目标节点的短时授权。授权由管理员在终端弹窗中填写原因并通过二次验证后自助创建，默认有效期很短，到期、撤销、拒绝或资源不匹配都不会放行。

终端断开后保留输出和可复制的安全状态，不自动重连或关闭弹窗。点击「重新连接」
会重新二次验证并建立新的 SSH 会话，不恢复原会话的进程或 PTY；请先确认远端操作
是否已经执行，避免重复输入有副作用的命令。正常结束代码也不代表命令成功，
应核对实际命令输出。关闭弹窗才清除本次显示。

配置导入在执行前同样需要有效的 admin 主认证、TOTP 二次验证 proof，以及绑定当前用户、`config.import` 操作和 `config_import` 用途的短时系统级授权；缺少、过期、撤销、拒绝或不匹配的授权会在导入写入前被拒绝。
配置导入中的系统设置逐项按当前 Settings 注册表进行正常校验；已退役或未知的设置键会拒绝整个导入，不会被静默丢弃或忽略。

含敏感字段的配置导出（`include_secrets=true`）需要有效的 admin 主认证、TOTP 二次验证 proof，以及绑定当前用户、`config.export` 操作和 `config_export` 用途的短时系统级授权；缺少、过期、撤销、拒绝或不匹配的授权会在读取或序列化敏感配置前被拒绝。普通配置导出不包含敏感字段，不需要临时授权。

快照恢复是远端文件恢复/覆盖类高风险操作。兼容端点 `POST /api/v1/tasks/:id/snapshots/:sid/restore` 还要求备份资产有效开启，并需要 admin 主认证、TOTP 二次验证和绑定当前用户、`snapshot.restore` 操作、`snapshot` 用途及当前任务 ID 的短时任务级授权。旧快照列表、文件浏览、搜索和差异 HTTP 端点已退役；当前读取使用 Catalog 与资产搜索，详见[备份资产搜索](backup-recovery.md#备份资产搜索)。

任务恢复触发同样会写入恢复目标。`POST /tasks/:id/restore` 需要有效的 admin 主认证、TOTP 二次验证 proof，以及绑定当前用户、`task.restore_trigger` 操作、`task_restore` 用途和当前任务 ID 的短时任务级授权；缺少、过期、撤销、拒绝、用户/角色变化或任务不匹配的授权会在进入恢复执行前被拒绝。授权创建只校验任务存在性和当前恢复资格，不记录恢复目标路径或恢复 payload。

授权记录和凭据审计只保存用户、角色、操作、用途、资源 ID、状态、TTL 等安全字段。管理员可在“临时授权”页面只读查看授权状态、筛选条件和生命周期时间；该入口不提供批准、拒绝或撤销操作。Xirang 不记录导入文件内容、导出文件内容、终端输入/输出、命令文本、文件内容、恢复目标路径、快照文件列表、私钥、密码、令牌、主机地址或代理端点；终端录屏/回放不属于当前内置能力。

### 导入结果与修复顺序

默认配置导出不含秘密，不是完整恢复包。导入采用同名跳过策略时，系统设置仍会
更新；覆盖节点也不会替换现有密码或内嵌私钥。结果分别列出新建、更新、跳过、
拒绝及导入后禁用数量；“已导入”不代表凭据可用。网络或服务端结果不确定时，
先读取实际记录，不自动重放导入。

来源数字节点范围不能证明当前数据库中的对应关系。新密钥缺私钥或范围无法验证
时保持禁用；不安全的覆盖项整条拒绝，原记录不变。来源私钥摘要不被信任。
请依次编辑密钥补私钥、复核用途/本地节点或标签/到期时间、手动启用，再编辑节点
重新绑定并测试连接。轮换不是启用禁用密钥的替代入口。

## 备份资产 Worker 与 updater 信任边界

备份资产 Worker 当前是默认关闭、非 GA 的可选本地 profile，没有稳定公共 Worker 镜像或 Docker Hub/GitHub Release 发布合同。官方 All-in-One Core 和公开端口 `10761` 不变。未部署 Worker 时，仍获准的 Catalog、原生预览和下载可继续使用；受管 Recovery 另依赖 Processing 就绪与恶意软件证据，不能承诺无 Worker 可恢复，完整要求见[处理与导出合同](../spec/domains/backup-processing-export.md)。

按[部署指南](../deployment.md)保留 Compose 的固定用户、独立 socket volume、只读文件系统、无网络、资源限制和敏感 tmpfs 配置。Parser 不能挂载数据库、源备份、Docker socket、updater 凭据或可写 bundle。容器禁止 swap 仍不能替代宿主的禁用或受审计加密 swap 配置。

Worker 只能通过一次性授权处理受限输入，不能任意访问路径或执行调用方提供的命令。运行环境缺少沙箱能力时，增强处理不可用，不能通过放宽隔离来强制启用。

Updater 与 parser 的身份、socket、PID namespace 和权限必须隔离。只有 updater 可更新经过签名验证的 bundle，parser 只读；派生与导出存储只对 Core 可见，不能放入数据库卷或 Provider 源。离线导入只使用固定 inbox 和受保护的 Ed25519 公钥集合，浏览器不能上传 bundle 字节或任意服务器路径。

仓库 Compose 对 updater 同样使用 `network_mode: none`，只支持 offline-only。Online updater 默认关闭；若未来单独部署，必须同时具备 exact HTTPS origin allowlist、独立 allowlist proxy/firewall、隔离网络和 updater-only credential secret。应用层 allowlist 不能代替 egress firewall，parser Worker 永远不得继承 updater 网络或凭据。

恶意软件结果区分 `not_scanned`、`no_finding`、`finding`、`stale`；“未扫描”或“过期”不表示安全。检测到风险不会因重试变成无风险，也不会改变源备份可信事实。有限秘密分类默认关闭，不能放宽 Core 权限。

排障资料不能包含源内容、原始工具输出、凭据或运行授权。完整实现与回归约束见[处理与导出合同](../spec/domains/backup-processing-export.md)。回退时关闭设置及可选 profile，保留源备份、恢复点、Catalog 和加密数据。

## 敏感字段保护

Xirang 会加密存储 SSH 密码、SSH 私钥、TOTP 密钥、通知端点、代理地址等敏感字段。请妥善备份 `DATA_ENCRYPTION_KEY`；数据库备份没有对应密钥时无法恢复敏感字段明文。若部署仍处于密钥轮替或含历史 v1 字段/旧 v2 domain-key envelope，还必须原样保留适用的 `DATA_ENCRYPTION_LEGACY_KEY` 及其它历史 key-ring/decryption key；不能用新密钥代替旧密钥完成数据库恢复。

备份资产控制面同样依赖该密钥。仓库访问绑定、冻结原因和 wrapped domain key 只有在恢复原数据库 **并且** 使用匹配的 `DATA_ENCRYPTION_KEY` 与适用历史密钥时才可读。仅保留 Provider 仓库只能在 Admin 有效重连/导入后重建可验证的 RecoveryPoint/Catalog 事实，不能重建 overlays、审计、策略、冻结或 Task 关系。错误或缺失密钥必须失败关闭，不得静默换绑或把 rebuild 报成成功。数据库退役迁移的升级前保全、旧版隔离恢复和不可逆回退见[备份、恢复与快照](./backup-recovery.md#升级与灾难恢复)。

任务中的策略摘要不返回 hook 或演练脚本；有权限的管理员从策略编辑入口管理。TOTP 初始化具有有效期，重新初始化会使上次二维码失效；已启用账户不能直接覆盖密钥。敏感字段、登录会话和临时授权的完整合同见[凭据与访问](../spec/domains/credentials-access.md)。

## 最后管理员与离线恢复

修改角色和删除用户不能移除最后一个管理员，并发请求也必须保留至少一个管理员。服务启动不会自动提升现有账户。若经过离线核验确认为零管理员状态，可在停机维护窗口使用镜像内 `/usr/local/bin/xirang-recover-admin`（源码入口 `backend/cmd/recover-admin`）：

1. 备份数据库及对应加密密钥，确认数据库已迁移到本版本；恢复工具不执行迁移，也不创建用户。
2. 使用受控运维会话设置临时 `XIRANG_BREAK_GLASS_CONFIRMATION`，并执行 `xirang-recover-admin -username <现有账户> -reason <8–512 字节操作原因> -confirmation <同一确认值>`。沿用该部署的数据库及配置环境。不要将真实凭据用作确认值，避免将其写入 shell 历史或长期部署配置；这只是明确的本地操作确认，不是远程认证或密码替代。
3. 工具仅在没有管理员时提升现有账户，原密码和 TOTP 保持不变；同时撤销旧版本会话/待完成登录，并原子保存操作原因与哈希链审计。确认失败、账户不存在、已有管理员或审计写入失败均不会提升权限。
4. 核验审计和正常登录后清除临时确认环境变量，再恢复服务。不得通过删除安全审计或修改迁移版本来绕过保护。
