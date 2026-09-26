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

## 内容网关与可选 Worker

资产内容精确路由与形状兜底路由的顺序、Range、buffering、75 秒网关上限、独立脱敏日志和转发头安全全部归[内容交付合同](../domains/backup-content-delivery.md)，改动 Nginx 时运行 `check-asset-content-nginx.sh` 及变异自测，并以实际渲染模板验证。应用授权和更短 deadline 仍是最终边界。

Worker 保持可选、本地构建且不发布公共镜像。启用能力与安装就绪不能混为一谈；Core-only 运行保留 `10761` 入口。Compose 改动须通过 `check-compose-config.sh` 及自测；涉及 Worker 的静态检查使用 `ASSET_WORKER_STATIC_ONLY=1 scripts/test-asset-worker.sh`，静态通过不代表实际沙箱或容器验收完成。

依赖固定、镜像发布、版本来源与部署文档同步归维护者主文。[Go 工具链升级](../../maintainers/automation.md#go-工具链升级)须同时覆盖模块声明、Core/Worker/supercronic 构建和 Worker 指纹，保持 CGO 与运行镜像的 libc 兼容。Alpine 软件源移除已锁定版本时，按[镜像构建依赖](../../maintainers/automation.md#镜像构建依赖)核对两个架构的可安装版本；包解析错误中出现的基础镜像已安装旧版本不是降级依据，安全修复库仍须保留精确锁定。修改镜像/配置后按贡献指南执行对应 backend、frontend、YAML、Nginx、文档及 CI 门禁；本篇不以文档检查代替真实容器运行证据。
