# 部署运行时合同

本篇约束官方 Compose、All-in-One 镜像、Nginx 和入口进程。运维操作见[部署指南](../../deployment.md)，配置清单见[环境变量](../../env-vars.md)，发布与自动化见[维护者入口](../../maintainers/README.md)。

## 官方入口与配置

根 `docker-compose.yml` 是唯一官方生产 Compose。Core 镜像为 `linnea7171/xirang:${IMAGE_TAG:-latest}`，`IMAGE_TAG` 是 Core 镜像选择变量；不增加 registry、namespace 或 image-name 分拆变量。可选本地 Worker 的构建标识不属于 Core 的公共镜像选择。

公开 HTTP 端口映射 `10761:10761`。TLS 由外部反向代理/负载均衡器管理，镜像不提供证书挂载、HTTPS 监听或内置重定向。容器后端 `SERVER_ADDR=:3000`，Nginx 上游 `127.0.0.1:3000`；源码运行的默认值为 `:8080`，不承诺仅绑定回环地址。

Compose 必须有 `.env`；部署从 `.env.deploy` 复制并填写。生产秘密为空由配置/初始化验证拒绝，首次管理员密码只在数据库尚无 admin 时需要。具体必填值、优先级和生效条件引用环境变量主文。

## 健康检查与进程生命周期

`/healthz` 只表示进程路由存活。`/readyz` 在 2 秒请求期限内检查数据库连接/Ping，数据库缺失、取连接失败或 Ping 失败返回 503，成功返回 200。它不证明所有外部节点、Provider、备份资产能力或可选 Worker 已可用。

Compose 与 Dockerfile 的健康检查均访问 `http://127.0.0.1:10761/readyz`，间隔 30 秒、timeout 5 秒、start period 15 秒、3 次重试。入口脚本先启动后端和 supercronic，最多尝试约 30 次、每次间隔 1 秒等待内部 `:3000/readyz`，成功后启动 Nginx。curl 自身耗时可使总等待超过 30 秒，不把提示文字当严格总 deadline。

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
不提供最近尝试、作业成功、调度时区或恢复成功声明。

扫描以 `os.OpenRoot` 约束，配置根符号链接无效，产物/校验文件必须非空普通文件；
Lstat、打开后 SameFile 及读取后身份/大小/mtime 核对拒绝替换或变化。
目录最多 4096 项、每个校验文件最多 4096 字节、总校验读取最多 4 MiB；
超限不得从部分结果宣称 fresh。校验文本恰好一条 SHA256 记录，名称仅接受对应
basename 或配置目录下规范绝对路径，只作相等性验证，绝不据此打开文件。
cron 每日备份、mtime 30 天清理与 Web 数量限制保持独立，详见[部署指南](../../deployment.md#手动备份与恢复)。

## 内容网关与可选 Worker

资产内容精确路由与形状兜底路由的顺序、Range、buffering、75 秒网关上限、独立脱敏日志和转发头安全全部归[内容交付合同](../domains/backup-content-delivery.md)，改动 Nginx 时运行 `check-asset-content-nginx.sh` 及变异自测，并以实际渲染模板验证。应用授权和更短 deadline 仍是最终边界。

Worker 保持可选、本地构建且不发布公共镜像。启用能力与安装就绪不能混为一谈；Core-only 运行保留 `10761` 入口。Compose 改动须通过 `check-compose-config.sh` 及自测；涉及 Worker 的静态检查使用 `ASSET_WORKER_STATIC_ONLY=1 scripts/test-asset-worker.sh`，静态通过不代表实际沙箱或容器验收完成。

依赖固定、镜像发布、版本来源与部署文档同步归维护者主文。[Go 工具链升级](../../maintainers/automation.md#go-工具链升级)须同时覆盖模块声明、Core/Worker/supercronic 构建和 Worker 指纹，保持 CGO 与运行镜像的 libc 兼容。Alpine 软件源移除已锁定版本时，按[镜像构建依赖](../../maintainers/automation.md#镜像构建依赖)核对两个架构的可安装版本；包解析错误中出现的基础镜像已安装旧版本不是降级依据，安全修复库仍须保留精确锁定。修改镜像/配置后按贡献指南执行对应 backend、frontend、YAML、Nginx、文档及 CI 门禁；本篇不以文档检查代替真实容器运行证据。

共享加密库补丁不改变 BuilderBase 或运行接口，但必须同步两个镜像与生产工具链 inventory；工具链和 pipeline 指纹按新 inventory 派生。两架构运行时闭包及签名运行包须按新源码重新生成，并保持 Core/Worker 一致，不能手改摘要或把旧闭包声明为新包版本。

Core 的 `tzdata` 锁定更新仅影响 All-in-One 时区数据库；Worker 未安装独立
tzdata 包，不因此改变工具链 inventory。更新后须重新构建 Core，并复核受规则
变化地区的本地调度时间；既有 UTC 时刻不做数据迁移。
