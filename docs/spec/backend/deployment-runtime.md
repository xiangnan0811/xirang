# 部署运行时合同

本篇约束官方 Compose、All-in-One 镜像、Nginx 和入口进程。运维操作见[部署指南](../../deployment.md)，配置清单见[环境变量](../../env-vars.md)，发布与自动化见[维护者入口](../../maintainers/README.md)。

## 官方入口与配置

根 `docker-compose.yml` 是唯一官方生产 Compose。Core 镜像为 `linnea7171/xirang:${IMAGE_TAG:-latest}`，`IMAGE_TAG` 是 Core 镜像选择变量；不增加 registry、namespace 或 image-name 分拆变量。可选本地 Worker 的构建标识不属于 Core 的公共镜像选择。

公开 HTTP 端口映射 `10761:10761`。TLS 由外部反向代理/负载均衡器管理，镜像不提供证书挂载、HTTPS 监听或内置重定向。容器后端 `SERVER_ADDR=:3000`，Nginx 上游 `127.0.0.1:3000`；源码运行的默认值为 `:8080`，不承诺仅绑定回环地址。

Compose 必须有 `.env`；部署从 `.env.deploy` 复制并填写。生产秘密为空由配置/初始化验证拒绝，首次管理员密码只在数据库尚无 admin 时需要。具体必填值、优先级和生效条件引用环境变量主文。

官方镜像同时保留 `/usr/local/bin/xirang-recover-admin`，并提供独立
`/usr/local/bin/xirang-recover-account`。前者仅在零管理员时提升现有角色，后者只在全部
Core/worker 停止后选择重置原账户密码和/或第二因子；均不执行迁移或创建账户。
新工具无常驻进程或网络监听，`-h` 不读取配置或数据库；控制终端、状态查证、密钥保全
与受限重新绑定流程见[安全加固](../../admin/security.md#原账户密码或第二因子丢失)。

## 健康检查与进程生命周期

`/healthz` 只表示进程路由存活。`/readyz` 在 2 秒请求期限内检查数据库连接/Ping，数据库缺失、取连接失败或 Ping 失败返回 503，成功返回 200。它不证明所有外部节点、Provider、备份资产能力或可选 Worker 已可用。

Compose 与 Dockerfile 的健康检查均访问 `http://127.0.0.1:10761/readyz`，间隔 30 秒、timeout 5 秒、start period 15 秒、3 次重试。入口脚本先启动后端和 supercronic，最多尝试约 30 次、每次间隔 1 秒等待内部 `:3000/readyz`，成功后启动 Nginx。curl 自身耗时可使总等待超过 30 秒，不把提示文字当严格总 deadline。

Core 的 curl/libcurl `8.23.0-r0` 替换已不可安装的旧锁定后，仍须验证上述内部启动
等待和公开 readiness 路径，以及可选 Worker 组合启动。升级通过重建并替换整个
Core 镜像生效，不改变探针参数、Worker inventory 或数据库；回退旧镜像恢复旧依赖，
旧锁定源码可能无法重建，不能用修改探针或跳过扫描代替依赖修复。

后端、supercronic、Nginx 任一关键子进程退出，容器退出并向其他子进程发 TERM 后等待，避免只剩 Nginx 对外提供 502。默认运行用户为 UID/GID 10000；entrypoint 可先以 root 修复 bind mount 权限再切换后端与定时器用户，挂载权限仍应在部署验证中实际检查。

回归入口为 `scripts/test-core-compose.sh` 及其自测、入口脚本相关测试、`internal/api/router_test.go` 中 readyz 的健康/数据库关闭/nil DB 案例。Compose 渲染在临时环境使用示例 env，不能覆盖操作者现有 `.env`。

数据库迁移和恢复不是普通 Core 重启：先停止并排空旧版 Core、scheduler、executor、collector、notification worker 及独立 writer，确认没有旧连接后再由新版本迁移、最后启动新 worker；旧/新二进制不得混写同一数据库或投递状态。SQLite 数据库恢复必须离线，PostgreSQL 恢复必须由 DBA/运维手动 stop/drain 应用连接。不可逆退役迁移失败时按[部署指南](../../deployment.md#回滚与灾难恢复)恢复升级前数据库、匹配旧版二进制和原密钥，不执行 down 或手改迁移元数据。

## 持久化与日志

| 存储 | 用途及约束 |
| --- | --- |
| `./data:/data` | SQLite 及应用数据，包括默认 known_hosts |
| `./backups:/backup` | 数据库备份；不等价于全部远程备份源或恢复点 |
| `./logs:/logs` | 应用与 Nginx 文件日志 |
| Core 的 derived/export named volumes | 备份资产衍生与导出密文，容器替换时保留；归领域生命周期管理 |
| updater/worker runtime named volumes | 分隔的私有 socket 运行时，不能当业务备份目录 |
| `/var/cache/xirang/asset-content` | 独立、非持久化的认证内容缓存，不声明为 volume |

备份资产 named volume 的挂载者、权限、排他用途及 parser/updater 隔离见[处理与导出合同](../domains/backup-processing-export.md)，缓存根身份校验见[内容交付合同](../domains/backup-content-delivery.md)。不得以“只有三个 bind mount”为由删除 named volume 或丢失密文。

镜像默认 `LOG_FILE=/logs/xirang.log`，Nginx 文件日志也写 `/logs`。Compose 的 Docker `json-file` 当前按 `max-size=10m`、`max-file=3` 轮转 stdout/stderr；该设置不会轮转 `/logs` 文件。镜像没有安装/调度 logrotate，应用也没有内置文件轮转。运维方必须单独控制文件日志增长；不能声称 Docker 日志选项已经保护挂载日志。文件轮转方式及打开句柄影响见[日志合同](logging-guidelines.md)。

## 数据库快照与 cron 产物观测

Web `POST /api/v1/system/backup-db` 仅以 SQLite `VACUUM INTO` 生成数据库快照，
按 `DB_BACKUP_MAX_COUNT` 保留数量；列表失败、空列表和 PostgreSQL 的 501 不得
混为同一状态。创建成功后的列表刷新失败不撤销已创建事实。外部配置、当前与历史
加密密钥、known_hosts 及远端数据必须另行保全，恢复只走现有离线运维流程。

### 数据库文件来源与下载列表

`GET /api/v1/system/backups?source=web|cron` 保持 admin-only，省略 `source`
仍为 `web`；显式空值、重复或未知来源返回 400 `BACKUP_REQUEST_INVALID`。
Web 来源沿用 `DB_BACKUP_DIR`，未配置时取 `SQLITE_PATH`（默认
`./xirang.db`）同目录的 `backups`，同时要求现有 SQLite 运行时开关和实际
GORM dialect 都为 SQLite。cron 来源只取 trim 后的 `CRON_DB_BACKUP_DIR`，
以实际 dialect 选择 SQLite 或 PostgreSQL，不搜索备用目录、不接受客户端路径或引擎。
同一可信目录可以容纳两个命名空间，但二者的列表不混用。

Web 下载准入只接受 `xirang-YYYYMMDD-HHMMSS.db` 或带 1–64 位 ASCII
字母、数字、下划线、短横线后缀的同名格式，时间戳必须可解析；原 Web 保留清理
规则不变。普通非空 `.db` 的旧可见性保留，非受管名称只显示为 `unmanaged`，
不能据此读取内容。cron 只列当前引擎严格受管命名的普通文件，不依赖 job receipt，
不混入另一引擎、临时文件、符号链接或特殊文件。

响应仍为 `data` 数组，保留 `filename,size,created_at,sha256`，增加
`source,engine,downloadable,unavailable_reason,packaged_size`。
`created_at` 仍是文件 mtime，并非创建时间。不可用原因闭合为
`""|unmanaged|incomplete|unsafe|too_large`；缺少或非法 sidecar、空受管数据
为 `incomplete`。`packaged_size` 是可计算的 tar 字节数，否则为 0。
`downloadable=true` 仅表示可尝试下载，不证明内容校验、文件保留或恢复成功；
数据上限为 4 GiB，超限行仍可见但不可下载。两来源按文件名降序排列。

配置根固定为规范绝对路径，拒绝最终根符号链接并核对打开前、打开后及当前身份。
受信任配置的祖先目录沿用 cron 语义，不承诺抵御能任意修改部署目录的本机主体。
文件对通过 rooted、非阻塞、禁止跟随链接的打开及描述符身份/大小/mtime 核对固定；
成功固定后保留原数据描述符，不按路径寻找替代物，清理 unlink 不改变所读对象。
每批扫描 256 项，最多 4096 个原始目录项、每个 sidecar 4096 字节、累计 sidecar
读取 4 MiB（非法候选也计预算）。全局预算耗尽返回 503
`BACKUP_SCAN_LIMIT_EXCEEDED`，不返回伪完整部分列表；消失项可略过，读取目录失败
不能当空列表。已配置目录不存在时返回空数组，其他目录错误为 503
`BACKUP_DIRECTORY_UNAVAILABLE`。Web 不支持或未知 dialect 为 501
`BACKUP_SOURCE_UNSUPPORTED`，cron 未配置为 409 `BACKUP_SOURCE_NOT_CONFIGURED`。
安全错误码位于 `data.error_code`，消息不包含路径或原始异常。

新 `backup-db.sh` checksum 记录使用产物 basename，数据库文件和
`BACKUP_FILE`/receipt 路径保持原义。下载专用 parser 对 cron 旧记录容忍相对、
非规范或其他挂载位置标签，但要求单记录、无 NUL/反斜线/尾随分隔符且清理后的
basename 精确相等；标签只用于比较，绝不作为访问路径。Web 只接受 basename
或固定根下规范绝对路径。下载必须另做真实内容 hash；既有 cron 观察器不扩大
旧格式准入，下载校验不回写 job、新鲜度或 `content_verified` 事实。

### 经校验的有界 tar 下载

`GET /api/v1/system/backups/:source/:filename/download` 先通过主认证、admin、
专用 step-up 和系统 grant，再打开备份内容。授权合同见
[凭据与访问](../domains/credentials-access.md#临时凭据授权与终端)。
请求必须携带单个强 `If-Match: "<64 位小写 hex digest>"`，缺少为 428
`BACKUP_PRECONDITION_REQUIRED`，非法为 400 `BACKUP_REQUEST_INVALID`，
与重新固定的 sidecar 不同为 412 `BACKUP_CHANGED`。路径段不二次解码，
不接受路径分隔符、点段、NUL、百分号或编码残留；不接受 Range/续传
（400 `BACKUP_RANGE_UNSUPPORTED`）。令牌、proof、grant 不进入 URL/query。

单 Core 的两个来源共享两个非排队下载许可，容量满立即 429
`BACKUP_DOWNLOAD_BUSY`、`Retry-After: 5`。数据最多 4 GiB，超限为 413
`BACKUP_TOO_LARGE`。总预算从认证前开始计十分钟，准备阶段最多 120 秒，
最终期限取总预算、原主会话及最初所选 grant 到期中的最早值。
准备超时但总预算有余额时以最多五秒错误写窗口返回 504
`BACKUP_PREPARE_TIMEOUT`；总期限已尽或响应已提交只能中止，不补 JSON。
原客户端取消不再写错误响应。无法设置底层 transport deadline 时返回 503，
不进入认证或文件处理；保持 server 全局 30 秒 WriteTimeout 不变。

准备阶段按初始大小，以最多 32 KiB 的取消感知读取做第一遍 SHA256、额外尾字节及
描述符 Stat 校验，再 rewind 同一 FD。不存在为 404 `BACKUP_NOT_FOUND`；
不完整/不安全为 409 `BACKUP_NOT_READY`，内容不符为 409
`BACKUP_INTEGRITY_FAILED`，存储或无法确认授权为安全 503。
发头前、传输中距离上次校验至少五秒及 tar 结束前重新核对原主会话和固定 grant ID，
每次最多 500ms；不以后来新 grant 替换，不写回过期状态。proof 新鲜度是准入条件，
不是传输租约，不在发送前重新要求它仍剩 300 秒。

响应为不压缩 `application/x-tar`，attachment 名为 `<source>-<原文件名>.tar`，
携带精确 `Content-Length`、强 digest `ETag`、`Cache-Control: private, no-store`、
`X-Content-Type-Options: nosniff`、`X-Accel-Buffering: no`。
CORS 允许 `If-Match`，暴露 `Content-Disposition` 和 `ETag`。
USTAR 只含原 basename 数据与 `<basename>.sha256`，普通文件 mode 0600、
uid/gid 0、空 uname/gname、Unix epoch mtime，无路径和扩展元数据。
checksum 文本为 `<lowercase digest>  <basename>\n`；
tar 总长度为 `2560 + ceil(dataSize/512)*512`。

第二遍仍从原 FD 读取固定大小并 hash，每次至多 32 KiB 写刷新
`min(now+30秒,绝对期限)`。只有流出 hash、Stat、context、session 和原 grant
均有效才发送 checksum 与 tar 结尾；不 defer tar.Close。结束后显式剥离 Gin
writer，再执行可观察错误的底层 Flush，全部描述符关闭及 flush 完成后才释放许可。
失败或取消记录一次结果，metadata 仅为来源、引擎、受管 basename、size、
sent_bytes、固定 stage/reason_code 与 grant_id，不含原始错误、路径、请求头或草稿。
服务端成功仅证明写入及可观察 flush 成功，不证明客户端收到、落盘或恢复成功；
已发字节不能撤回，结尾后的 flush 失败也不承诺撤回。

准入真正拒绝记录 `blocked`；基础设施不可用记录 `failure/admission`：
proof 存储故障与缺 session binding 使用 `authorization_unavailable`，
缺下载预算使用 `budget_unavailable`。准备预算耗尽仍由预算层返回可写的
504 `BACKUP_PREPARE_TIMEOUT` 并记录 failure，不误分类成 proof 无效或成功。

R14 备份列表/子路径及精确 grant 创建路径使用局部 recovery：入口即禁止缓存，
`http.ErrAbortHandler` 原样传至 net/http，其他已提交响应的 panic 也转换为流中止；
不调用 Gin request dump、不记录 panic 内容或 proof。普通路由仍使用原 Gin recovery。
本路径不套 ETag/gzip 或整包缓冲中间件。

官方 Nginx 仅为 `^/api/v1/system/backups/(web|cron)/[^/]+/download$`
配置独立 location，沿用普通 API 的 Host/真实 IP/转发 IP/协议策略，
使用 HTTP/1.1 上游，禁用 response/request buffering、proxy cache、临时文件和 gzip；
`proxy_read_timeout 660s`、`proxy_send_timeout 30s`、`send_timeout 30s`。
660 秒只容纳准备期无响应字节，不延长后端总预算；直连 Core 成功不能替代
真实代理长下载与取消验收。

### cron 产物观察

独立 admin-only `GET /api/v1/system/cron-backup-status` 不继承 Web 的 SQLite 限制。
以实际 DB dialect 选择 `xirang-sqlite-*.db` 或 `xirang-postgres-*.dump`；
`CRON_DB_BACKUP_DIR` 默认空，All-in-One 显式 `/backup/db`，目录是容器内路径。
`CRON_DB_BACKUP_MAX_AGE_HOURS` 默认 26、正整数 1–8760；非法配置作为响应状态呈现，
不令启动失败。接口不创建目录、不执行调度、不写状态、不校验整个数据库内容。

响应状态为 `not_configured`、`invalid_configuration`、`directory_unreadable`、
`no_complete_backup`、`scan_limit_exceeded`、`clock_anomaly`、`stale`、`fresh`。
`engine` 正常为 `sqlite` 或 `postgres`；仅无法识别运行时引擎的
`invalid_configuration` 响应使用空值，不能伪称受支持引擎。
固定证据字段为 `evidence=artifact_pair`、`time_source=mtime`、
`content_verified=false`；时间是 RFC3339 UTC。只在有完整产物对时提供最新时间与
产物名，以数据库/校验文件较晚的 mtime 作为发布时间。任一合格对未来 mtime
返回时钟异常；否则 age ≤ 阈值才为 fresh。目录存在不证明 cron 已启用，
这些顶层产物字段不提供最近尝试、作业成功、调度时区或恢复成功声明。

扫描以 `os.OpenRoot` 约束，配置根符号链接无效，产物/校验文件必须非空普通文件；
Lstat、打开后 SameFile 及读取后身份/大小/mtime 核对拒绝替换或变化。
目录最多 4096 项、每个校验文件最多 4096 字节、总校验读取最多 4 MiB；
超限不得从部分结果宣称 fresh。校验文本恰好一条 SHA256 记录，名称仅接受对应
basename 或配置目录下规范绝对路径，只作相等性验证，绝不据此打开文件。
cron 每日备份、mtime 30 天清理与 Web 数量限制保持独立，详见[部署指南](../../deployment.md#手动备份与恢复)。

### cron 作业执行记录

同一响应必含独立 `job` 对象，固定 `evidence=job_record`，包含 `status`、
`checked_at`、`max_age_seconds`，以及可选的安全 `latest_attempt`、`last_success`。
作业与产物证据不能相互覆盖：成功记录可以与产物缺失同时存在，旧的完整产物也不能
掩盖最近失败。管理面不提供执行或重试 cron 的动作，读取不创建告警。

`CRON_DB_BACKUP_STATE_DIR` 默认空（未接入），官方镜像显式设为
`/backup/.cron-db-state`；它不改写产物目录或官方 cron 的 `/backup/db` 输出位置。
以实际引擎划分 `sqlite/`、`postgres/`，目录 0700、永久锁文件和状态文件 0600。
只允许具有可靠本机 Linux flock、rename、fsync 语义的持久文件系统，不承诺 NFS
或多个独立本地卷的分布式互斥。Core 以实际 DB dialect 观察，runner 使用 `DB_TYPE`。

`xirang-cron-db-backup init` 固定首次初始化时间与源身份；容器重启不刷新宽限。
entrypoint 在 supercronic 启动前以相同非 root 身份初始化，失败仅输出固定警告，
继续启动 Core；`run` 无法提交 running 记录时不启动脚本。每日 cron 调用 runner，
30 天产物清理独立进行；手工 `backup-db.sh` 不产生 cron 作业记录。

运行器持有整个执行期的 `run.lock`，子进程继承锁描述符。重复运行退出 75，
不制造尝试；TERM/INT 转发到进程组，5 秒后升级 KILL。发送组信号期间保留未回收的
直接子进程身份，避免 PID/进程组编号复用；僵尸不算活跃执行者。取消后只有确认组已
停止并重新取得独立打开的 run.lock，才发布终态；继承锁仍被占用或无法确认时保留
running，由后续观察推导状态。取消与完成竞态仍以合法 receipt 加零退出为成功条件。
`state.lock` 保护状态提交及后台一致性决策，API 获取至多等待 100ms，后台观察至多
1s；运行器最终发布等待至多 10s，覆盖后台锁内数据库操作的 5s 期限。底层 I/O 仍依赖
本机文件系统，正常备份不新增总执行期限。观察时钟在锁内读完状态后采样，避免把
等待期间刚提交的记录误报为未来时间。
状态版本 1，单对象 UTF-8 JSON 上限 16KiB，拒绝重复键、未知字段、尾随对象、
符号链接、特殊文件、错误引擎、非法关联及路径注入。

`job.status` 为 `not_configured`、`invalid_configuration`、`not_initialized`、
`state_unavailable`、`state_invalid`、`clock_anomaly`、`never_run`、`running`、
`overdue_running`、`interrupted`、`failed`、`success` 或 `stale`。
未来/倒序时间不视为成功；running 且锁仍被占用才表示执行中，超期为
overdue_running；锁已释放但没有完成记录则只读推导 interrupted，不伪造终止时间。
持久 interrupted 只记检测时间和 `process_interrupted`，不承诺备份没有发生。
失败码闭合为 `backup_failed`、`backup_start_failed`、`result_invalid`、
`process_interrupted`，不记录命令、DSN、密码、原始输出或错误路径。

脚本在原有最终 SHA256 复核及输出后，仅在内部环境
`XIRANG_BACKUP_RECEIPT_FD=3` 时写 basename receipt；未设置时离线操作保持原义。
只有合法 receipt 与子进程零退出同时成立才写 success，并更新 last_success；
新的 running、失败或中断保留旧 last_success。成功记录表示脚本完成证据，
不等于 PostgreSQL 恢复验收、产物当前仍存在或完整灾备恢复。

状态以同目录临时文件、文件 Sync、原子 Rename 和目录 Sync 发布。运行器只有全部
成功才确认成功；失败输出 `state_publish_failed` 并非零退出，不删除已验证产物。
**Rename 已成功而目录 Sync 失败时，发布结果不确定**：随后仍可能读到完整成功记录，
并据此恢复告警；该记录不能证明原 runner 最后一次持久化确认成功。不能承诺在故障
介质上保存失败事实，也不以回滚文件或重建状态掩盖不确定结果。
损坏状态不自动覆盖或重置身份；运维须保全现场并从匹配的私有状态备份恢复。

## 内容网关与可选 Worker

资产内容精确路由与形状兜底路由的顺序、Range、buffering、75 秒网关上限、独立脱敏日志和转发头安全全部归[内容交付合同](../domains/backup-content-delivery.md)，改动 Nginx 时运行 `check-asset-content-nginx.sh` 及变异自测，并以实际渲染模板验证。应用授权和更短 deadline 仍是最终边界。

Worker 保持可选、本地构建且不发布公共镜像。启用能力与安装就绪不能混为一谈；Core-only 运行保留 `10761` 入口。Compose 改动须通过 `check-compose-config.sh` 及自测；涉及 Worker 的静态检查使用 `ASSET_WORKER_STATIC_ONLY=1 scripts/test-asset-worker.sh`，静态通过不代表实际沙箱或容器验收完成。

依赖固定、镜像发布、版本来源与部署文档同步归维护者主文。[Go 工具链升级](../../maintainers/automation.md#go-工具链升级)须同时覆盖模块声明、Core/Worker/supercronic 构建和 Worker 指纹，保持 CGO 与运行镜像的 libc 兼容。Alpine 软件源移除已锁定版本时，按[镜像构建依赖](../../maintainers/automation.md#镜像构建依赖)核对两个架构的可安装版本；包解析错误中出现的基础镜像已安装旧版本不是降级依据，安全修复库仍须保留精确锁定。修改镜像/配置后按贡献指南执行对应 backend、frontend、YAML、Nginx、文档及 CI 门禁；本篇不以文档检查代替真实容器运行证据。

共享加密库补丁不改变 BuilderBase 或运行接口，但必须同步两个镜像与生产工具链 inventory；工具链和 pipeline 指纹按新 inventory 派生。两架构运行时闭包及签名运行包须按新源码重新生成，并保持 Core/Worker 一致，不能手改摘要或把旧闭包声明为新包版本。

Core 的 `tzdata` 锁定更新仅影响 All-in-One 时区数据库；Worker 未安装独立
tzdata 包，不因此改变工具链 inventory。更新后须重新构建 Core，并复核受规则
变化地区的本地调度时间；既有 UTC 时刻不做数据迁移。

Core 的 Expat 直接锁定为 `libexpat=2.9.0-r0`，替代软件源已移除的
`2.8.5-r0`；基础镜像 digest 和其他包锁定保持不变。此包不在 Worker 的显式
包清单或生产工具链 inventory 中，不因此新增 Worker pin 或修改指纹合同。
更新须重新构建并验证 Core 的双架构镜像与实际就绪路径；不新增配置或数据迁移。

Core 的 c-ares 直接锁定从软件源已不可安装的 `1.34.8-r0` 更新为 `1.34.9-r0`，
保留基础镜像 digest 和其他包锁定。新版本包含上游 DNS 应答校验与内存安全修复；
不以基础镜像已安装的旧版本替代安全锁定。此修复不新增 Worker 显式依赖或修改
其生产工具链 inventory；须验证 Core 双架构构建、扫描和就绪路径，以及 Worker
配置下的配套 Core。修复从重建并替换 Core 镜像后生效，不新增配置或 schema migration。

Core 与 Worker 的 CGO 构建阶段将 `musl-dev` 从已不可安装的 `1.2.6-r2`
锁定到 `1.2.6-r3`，共享生产 inventory 同步更新；Go 与基础镜像 digest 不变，
不扩大为运行镜像 libc 升级。构建包也参与工具链及 pipeline 指纹，须按同一源码
重建 Core/Worker 并重新生成各目标架构的闭包、签名运行包及 proof，保留 CGO
产物在实际运行镜像中的加载与就绪验收。无新增配置或 schema migration；
切换前排空旧 Worker 在途处理，回退使用配套旧镜像及制品，不手改指纹或沿用旧证明。
