# 部署、升级与运维

本文档面向自托管 Xirang 的用户，说明生产部署、外部反向代理、升级回滚、数据备份、健康检查和常见故障处理。维护者发布链路请看 [维护者发布手册](maintainers/release.md)。

## 官方交付方式

- GitHub Release 是权威公开版本源和变更说明源。
- 官方公开镜像为 `docker.io/linnea7171/xirang`。
- `latest` 表示最新稳定版；生产环境建议固定到显式 `vX.Y.Z` 标签。
- 当前公开发布仅使用稳定版 semver，不提供 nightly/prerelease 镜像通道。

## 运行架构

生产部署默认使用 All-in-One 单容器镜像：

```text
                  ┌────────────────────────────────┐
HTTP :10761  ───> │ Nginx                          │
                  │   ├── /api/v1/*  ──> Backend   │
                  │   ├── /healthz   ──> Backend   │  (liveness)
                  │   ├── /readyz    ──> Backend   │  (数据库连通性)
                  │   └── /*         ──> Web UI    │
                  │                                │
                  │ Backend (:3000)                │
                  │ SQLite(/data) 或 PostgreSQL    │
                  │ Cron 每日备份数据库              │
                  └────────────────────────────────┘
```

容器内入口端口固定为 `10761`，后端默认监听 `:3000`。入口脚本和 Nginx 都使用后端 3000 端口，不应只通过 `SERVER_ADDR` 改变镜像内部端口。项目不在容器内处理 HTTPS；公网 HTTPS 由外部代理终止 TLS，再反代到 `http://127.0.0.1:10761`。宿主机端口默认发布到所有接口，若只允许同机代理访问，将 Compose 的 `ports` 改为 `127.0.0.1:10761:10761`。

### Rsync 路径隔离部署

配置任一 `RSYNC_ALLOWED_SOURCE_PREFIXES` / `RSYNC_ALLOWED_TARGET_PREFIXES` 后，Rsync 使用一次性 `xirang-rsync-confined` helper 固定文件描述符并应用 Linux Landlock 文件系统权限；本地与 SSH 远端都必须具备所需隔离能力（Landlock ABI 至少 3，包含文件截断保护）。缺 helper、内核不支持或策略不可安全应用时拒绝执行，不回退到普通 Rsync。两项均留空保留原有不限制模式。允许列表是文件系统根边界，包含根本身及真实后代，不包含名称相似的兄弟目录或越界符号链接。

All-in-One 镜像内置 `/usr/local/bin/xirang-rsync-confined`。SSH 备份节点须由管理员部署与 Core 同版本、匹配节点架构的 helper；可从同版本源码在 `backend` 目录执行 `go build -o xirang-rsync-confined ./cmd/rsync-confined`，再安装为节点 SSH 用户可执行且不可篡改的程序。`RSYNC_CONFINEMENT_HELPER` 选择本地程序，`RSYNC_CONFINEMENT_REMOTE_HELPER` 选择远端程序；默认均从执行环境查找 `xirang-rsync-confined`。不得用删除白名单作为缺少 helper 的自动补救。

保留源目录根名或固定普通文件操作数的隔离路径还需要可用的非特权 user/mount namespace、`mount_setattr`，以及 util-linux 的 `unshare` 和 `mount`（位于 `/usr/bin` 或 `/bin`）。这些能力须在实际执行用户及容器安全策略下可用。Docker 安全策略可能以 `EPERM` 拒绝创建命名空间；镜像包含 helper 不等于环境支持所有隔离传输，遇到限制会拒绝执行。不要自动启用 privileged、授予 SYS_ADMIN、关闭 seccomp 或删除白名单；先评估运行环境并用一次性数据验收。隔离实现约束见[凭据与访问合同](spec/domains/credentials-access.md)。

部署后先使用一次性目录验证正常传输、内部链接、越界链接拒绝及缺失 helper 拒绝，再恢复备份调度。配置隔离时 SSH 不读取用户自定义配置文件，只使用 Core 生成的连接参数与精确凭据文件；需要的连接配置应在节点配置中显式提供。

## Docker Compose 部署（推荐）

### 1. 获取部署文件

```bash
git clone https://github.com/xiangnan0811/xirang.git
cd xirang
```

如果你不需要完整源码，也可以只复制发布包或仓库中的以下文件到服务器同一目录：

- `docker-compose.yml`
- `.env.deploy`

### 2. 准备 `.env`

```bash
cp .env.deploy .env
```

`docker-compose.yml` 通过 `env_file: .env` 注入配置；**缺少 `.env` 时 `docker compose config` / `up` 会失败**，不会静默回退到空密钥默认值。

生产环境启动前至少填写以下密钥（未声明 `APP_ENV=development` 时，缺 `JWT_SECRET` / `DATA_ENCRYPTION_KEY` / `METRICS_TOKEN` 会拒绝启动）：

```env
# 仅首次启动且库中尚无 admin 用户时需要；已有 admin 后可留空或删除
ADMIN_INITIAL_PASSWORD=<首次登录 admin 的强密码>
JWT_SECRET=<至少 32 字符的强随机字符串>
DATA_ENCRYPTION_KEY=<至少 16 字符的强随机加密密钥>
METRICS_TOKEN=<至少 16 字符的强随机 token，禁止文档占位符；可用 openssl rand -hex 32 生成>
```

生产环境建议同时固定镜像版本：

```env
IMAGE_TAG=vX.Y.Z
```

常用部署项：

```env
APP_ENV=production
TZ=Asia/Shanghai
DB_TYPE=sqlite
SQLITE_PATH=/data/xirang.db
# 若前端构建时使用了外部 VITE_WS_URL，需在 Nginx CSP 中追加允许的 connect-src：
# CSP_CONNECT_SRC_EXTRA=wss://ws.example.com
```

完整变量说明见 [环境变量参考](env-vars.md)。

### 3. 启动服务

```bash
docker compose pull
docker compose up -d
```

检查状态：

```bash
docker compose ps
docker compose logs --tail=200 xirang
curl -fsS http://127.0.0.1:10761/readyz
curl -fsS http://127.0.0.1:10761/healthz
```

首次登录：

- 地址：`http://<server>:10761`
- 用户名：`admin`
- 密码：`.env` 中的 `ADMIN_INITIAL_PASSWORD`

### 4. 可选：本地备份资产 Worker profile（非 GA）

仓库根 Compose 提供 `asset-worker` 可选 profile，用于在本机从源码构建和验证 parser Worker 与独立 updater。该能力当前**不是 GA**，没有稳定公共 Worker 镜像，也不会发布到 Docker Hub 或 GitHub Release。它使用本地镜像名 `xirang-asset-worker:${ASSET_WORKER_IMAGE_TAG:-local}`；官方 Core 仍是 `linnea7171/xirang:${IMAGE_TAG:-latest}`，公开端口仍只有 `10761`。

普通 `docker compose up -d` 不会启动 profile 服务；在备份资产启用门禁和各自授权满足时，Catalog、元数据搜索、Content Broker、workspace、原生预览和下载可继续工作。未部署 Worker、没有 active verified bundle 或 capability 不匹配时，增强处理显示 `not_deployed`/`unsupported`，不会仅因此制造备份失败或告警。

共享运行时包补丁会改变 Core/Worker 的生产工具链指纹。升级此类补丁时同时重建同源 Core 与可选 Worker，并重新生成匹配当前源码和架构的运行时闭包及签名运行包；不得复用旧指纹的运行包或绕过就绪检查。包版本不通过 `.env` 覆盖，维护步骤见[镜像构建依赖](maintainers/automation.md#镜像构建依赖)。

受管 Recovery 不属于上述无 Worker 可用承诺：当前实现启用它时要求 Processing 已就绪且具备恶意软件证据服务，本机与远程 Worker 均关闭时不满足准入。旧版普通任务恢复仍按各执行器的证据和授权要求处理，不能代替受管恢复的安全检查。部署前须按[处理与导出合同](spec/domains/backup-processing-export.md)核验实际需要的恢复路径。

Profile 固定使用以下本地身份和权限合同：

| 对象 | 身份/模式 | 说明 |
|---|---|---|
| parser Worker | UID/GID `10000:10000` | non-root、read-only rootfs、无网络/DNS、只读 bundle mount |
| updater | UID/GID `10002:10002` | 独立 UDS 与 PID namespace；只有它可写 bundle store |
| parser socket volume | `asset-worker-worker-runtime` | parser 只读挂载到 `/run/xirang/worker`；不包含 updater socket |
| updater socket volume | `asset-worker-updater-runtime` | updater 只读挂载到 `/run/xirang`；不包含 parser socket volume |
| bundle root | `10002:10000`, mode `2750` | updater owner 可写；Worker group 可读，但 parser mount 强制只读 |
| Derived Store volume | `asset-worker-derived-store`, `10000:10000`, mode `0700` | 仅 Core 与 initializer 挂载到 `/var/lib/xirang-asset-runtime/derived`；parser/updater 不可见 |
| Export Store volume | `asset-worker-export-store`, `10000:10000`, mode `0700` | 仅 Core 与 initializer 挂载到 `/var/lib/xirang-asset-runtime/export`；parser/updater 不可见 |
| inbox 目录 | `10002:10002`, mode `0555` | updater-only、只读、不得是符号链接 |
| trust 文件 | `10002:10002`, mode `0440` | updater-only Ed25519 公钥集合，不得是符号链接 |

Core 同时挂载 updater runtime 和嵌套的 Worker runtime，以分别创建 mode `0660`（`10000:10002`）与 `0600`（`10000:10000`）的 UDS。`/run/xirang` 使用 setgid mode `2770`，让 Core 创建的 updater socket 继承 GID `10002`；parser 不加入该组，也完全不挂载 updater runtime。反向同样成立：updater 不挂载 `asset-worker-worker-runtime`，因此两个进程都不能观察或连接对方的 socket。

Core、parser 和 updater 保持各自独立的 PID namespace。Linux 跨 PID namespace 的 `SO_PEERCRED` 可能返回 peer PID `0`；PID 只作为诊断元数据，不参与授权。Core 的 updater listener 依赖受保护的 UDS，并在解码 receipt 前校验精确 UID/GID 与 socket owner/mode。

`asset-worker-init` 还会把独立 Derived Store 与 Export Store volume 初始化为 `0700:10000:10000`。这两个 volume 持久保留 Core 加密产物并与 `/data`、`/backup`、`/logs` 以及所有 Provider 源隔离；parser 和 updater 都不挂载它们。

先准备固定 inbox 和 trust 文件。Trust 文档只包含公钥与 UTC 生效/退役时间；不要把私钥或在线凭据放入该文件：

```json
{
  "schema_version": 1,
  "keys": [
    {
      "id": "operator-key-1",
      "public_key": "<base64-ed25519-public-key>",
      "active_from": "<RFC3339-UTC>",
      "retire_after": "<RFC3339-UTC>"
    }
  ]
}
```

```bash
mkdir -p asset-worker-inbox
sudo chown 10002:10002 asset-worker-inbox asset-worker-updater-trust.json
sudo chmod 0555 asset-worker-inbox
sudo chmod 0440 asset-worker-updater-trust.json
```

在 `.env` 中显式启用全局 feature、本机 UDS 和独立 updater，并让加密 Derived / Export Store 使用 Compose 提供的专用 volume 与默认路径。`BACKUP_ASSETS_ENABLED=true` 仍须通过就绪门禁：全新安装在库存盘点、导出根和密钥域就绪后即可启用；已有安装还须管理员确认当前库存摘要。CodeDefault 与官方 `.env.deploy` 仍是 `false`。`/data`、`/backup` 和 `/logs` 及其子路径会被 private-runtime guard 拒绝，不能用作 Derived 或 Export Store。Settings 数据库覆盖优先于环境变量；若已有同名 DB override，必须在设置界面同步更新或删除旧覆盖值：

```env
BACKUP_ASSETS_ENABLED=true
BACKUP_ASSETS_WORKER_LOCAL_ENABLED=true
BACKUP_ASSETS_WORKER_LOCAL_SOCKET=/run/xirang/worker/asset-worker.sock
BACKUP_ASSETS_WORKER_UPDATER_ENABLED=true
BACKUP_ASSETS_WORKER_UPDATER_ONLINE_ENABLED=false
BACKUP_ASSETS_PROCESSING_SECRET_CLASSIFY=false
BACKUP_ASSETS_PROCESSING_BACKFILL_PAUSED=true
BACKUP_ASSETS_DERIVED_STORE_ROOT=/var/lib/xirang-asset-runtime/derived
BACKUP_ASSETS_EXPORT_ROOT=/var/lib/xirang-asset-runtime/export

ASSET_WORKER_IMAGE_TAG=local
ASSET_WORKER_INBOX_DIR=./asset-worker-inbox
ASSET_WORKER_UPDATER_TRUST_FILE=./asset-worker-updater-trust.json
```

仓库 profile 固定为 offline-only，并给 Worker 与 updater 都设置 `network_mode: none`。不要在该 profile 中启用 online updater；在线模式需要独立的 allowlist proxy/firewall、隔离网络和凭据 secret 合同，当前仓库 Compose 不提供这条部署路径。

Worker 的 job workspace 是敏感 `tmpfs`。仓库 Compose 已把 parser/updater 的 `memswap_limit` 设为与各自 `mem_limit` 相同，使这两个容器不能使用 swap；部署前仍应确认宿主 swap 已关闭，或只使用经过审计的全盘加密 swap。若改用 `docker run` 或外部编排，必须保留同等的 no-swap、memory、PID、read-only、no-network、seccomp 与 `noexec,nosuid,nodev` tmpfs 限制。

本地构建并启动：

```bash
docker compose --profile asset-worker build asset-worker
docker compose --profile asset-worker up -d
docker compose --profile asset-worker ps
docker compose --profile asset-worker logs --tail=200 asset-worker asset-worker-updater
```

以下变更需要重启 Core/profile：本机/远程 Worker enablement 与 socket/certificate/trust、updater enablement/online origins、Derived Store root/chunk、Export Store root，以及 inbox、trust 文件或 bundle mount。Backfill pause/quota 与有限秘密分类是动态设置；默认分别为 paused 与 disabled。

回退时先暂停 backfill，再关闭本机 Worker 与 updater 设置并重启 Core，然后停止可选服务：

```bash
docker compose --profile asset-worker stop asset-worker asset-worker-updater
docker compose up -d xirang
```

不要使用 `down -v` 作为功能回退，也不要删除 Provider bytes、RecoveryPoint、Catalog 或源备份。加密 Derived 数据和已验签 bundle 可保留给受控调和或后续重新启用。移除 profile 后仍获准的原生预览和下载可继续使用；若没有其他就绪的 Worker，受管 Recovery 的安全准入会受影响，回退前须核验。

### 5. 可选：外部反向代理与 HTTPS

Xirang 容器只提供 HTTP 单入口。生产公网访问建议在同机或前置网关部署外部反向代理：

```text
https://xirang.example.com ──> 外部反向代理 ──> http://127.0.0.1:10761
```

反代需要保留 `Host`、`X-Forwarded-For`、`X-Forwarded-Proto`，并支持 WebSocket Upgrade。使用外部域名时，在 `.env` 中设置：

```env
CORS_ALLOWED_ORIGINS=https://xirang.example.com
```

### 6. 可选：私网 HTTP 备份内容

备份内容默认只允许 HTTPS 交付。若受控内网暂时无法终止 TLS，Admin 可在“备份 > 概览 > 私网 HTTP 内容传输”确认风险后动态启用；该开关同时覆盖原生预览/原始下载、导出产物、归档成员和恢复结果，且不需要重启。HTTP 会以明文传输内容和非 `Secure` 的短期票据 Cookie，链路上的设备或用户可能读取内容或重放票据，因此优先部署 HTTPS，并只在隔离且可信的网络中短期启用。

允许的客户端地址严格限定为 IPv4 RFC 1918（`10.0.0.0/8`、`172.16.0.0/12`、`192.168.0.0/16`）、IPv6 ULA（`fc00::/7`）和回环地址。CGNAT（`100.64.0.0/10`）、链路本地、未指定、多播及公网地址不会被视为私网。经反向代理访问时，将 `TRUSTED_PROXIES` 只配置为实际代理 IP/CIDR，并确保每一层覆盖 `X-Forwarded-Proto`、正确追加 `X-Forwarded-For`；不要信任全网段。Xirang 会从右向左剥离可信代理跳点，以最近的非可信地址作客户端判断，证据缺失、越界或格式异常都会失败关闭。

回滚时，在同一面板关闭开关即可立即拒绝新的 HTTP 签票和已有票据的后续 HTTP Serve。环境变量 `BACKUP_ASSETS_CONTENT_ALLOW_INSECURE_PRIVATE_NETWORK=true` 只在没有数据库覆盖时生效；若要恢复安全默认，可关闭面板，或删除 DB override 并将环境变量设为 `false`/移除后重新加载配置。此功能不提供内置 TLS，也不改变公网应由外部反向代理终止 HTTPS 的部署边界。

## Docker Run 部署

```bash
cp .env.deploy .env
mkdir -p data backups logs

docker run -d \
  --name xirang \
  --restart unless-stopped \
  -p 10761:10761 \
  -v "$(pwd)/data:/data" \
  -v "$(pwd)/backups:/backup" \
  -v "$(pwd)/logs:/logs" \
  --env-file .env \
  docker.io/linnea7171/xirang:vX.Y.Z
```

## PostgreSQL 部署

默认使用 SQLite，适合小规模单机部署。如需 PostgreSQL，在 `.env` 中设置：

```env
DB_TYPE=postgres
DB_DSN=postgresql://user:pass@host:5432/xirang?sslmode=require
```

后端会在 PostgreSQL DSN 未显式设置 `timezone` / `TimeZone` 时追加 `timezone=UTC`，确保时间戳按 UTC 读写。

## 升级与回滚

### 升级到稳定版

1. 阅读目标版本的 GitHub Release 和 `CHANGELOG.md`，并确认该版本的 `Publish Docker Images` 工作流已成功、官方 Docker Hub 稳定标签已发布。仅有 GitHub Release 不代表镜像可部署；若发行说明标记镜像发布受阻，应继续使用此前已验证版本。

   Nginx／PCRE2 等镜像内依赖的安全修复要求拉取修复后的完整镜像并重建容器；只更新应用文件或宿主机软件包不会替换容器中的漏洞版本。回退旧镜像也会恢复其中的旧依赖，须重新评估对应漏洞风险。

   Go 1.27.2 安全补丁须同源重建 Core、Worker 与 supercronic，并重新生成匹配架构的 Worker 闭包、签名运行包及证明；旧工具链指纹产物不能复用。Core 同时将镜像中 TIFF 4.7.1-r0 替换为直接锁定的 4.7.2-r0，修复 CVE-2026-4775。

   > 若目标版本包含备份职责收敛迁移，不能直接执行下面的 `pull`/`up -d`；必须先完成[备份职责收敛升级](#备份职责收敛升级)的 STOP、保全、旧版隔离恢复演练、迁移和新 worker 启动顺序。
2. 备份数据库、`.env` 及匹配的加密密钥，验证数据库产物和同名 `.sha256` 文件。容器已停止不代表没有数据，也不能跳过升级前备份。使用仓库手动部署工作流时，按[维护者发布手册](maintainers/release.md)准备当前部署目录与备份门禁。
3. 修改 `.env`：

```env
IMAGE_TAG=vX.Y.Z
```

4. 拉取并重启：

```bash
docker compose pull
docker compose up -d
```
5. 检查就绪状态、进程存活状态和日志：

```bash
curl -fsS http://127.0.0.1:10761/readyz
curl -fsS http://127.0.0.1:10761/healthz
docker compose logs --tail=200 xirang
```

### 搜索派生索引协议升级

若目标版本改变备份资产搜索的规范化输出，`NormalizerVersion` 会推进以使旧 token postings 失效。这不是源 Catalog 或数据库 schema 迁移：Core 与 Indexer 必须使用同一版本整体升级；旧代在重建完成前由 Search 报告 `unavailable` 且不报告 authoritative empty，不得回退旧代继续提供命中。

现有 Search worker 会通过候选列表和 bounded `Indexer.Build` 异步重建 metadata index，完成后再原子切换 active generation。该重建不启动平行 scheduler、不在启动时批量改写或删除旧 postings；metadata 重建也不代表 content/OCR projection 已完成，二者仍须各自恢复到合同要求的 coverage。

需要回退时，旧版 Core/Indexer 不得读取或复用新版 postings。应恢复与旧版匹配的完整数据库、镜像和密钥，再按旧协议重建；不能手工改写 normalizer version、删除 postings 或把替换镜像当作安全回滚。

<a id="备份职责收敛升级"></a>
### 备份职责收敛升级（不可逆）

这是一次不可逆的数据退役，不是普通的镜像替换。升级前必须先安排维护窗口；任何前置条件无法确认时都应 **STOP**，不要让迁移“先跑起来再观察”。

1. **STOP 并排空旧写入者。** 先暂停调度和新的任务、备份、恢复、快照异常处理、保留清理、告警投递及升级动作，等待正在运行的工作收敛。随后停止并确认已经退出所有旧版 Core、scheduler、executor、collector、notification worker 以及独立部署在 Compose 之外的同类进程。必须确认没有旧进程仍会写数据库、备份资产元数据、任务运行、告警、投递或审计；仅停止 HTTP 入口、关闭 SSH 或等待租约过期都不算排空，也不能把容器“已停止”当成远端写入已结束。
2. **制作一致的升级前保全副本。** 在同一维护窗口保存经校验的数据库备份、备份资产/Provider 所需的独立保全副本、旧版二进制或镜像标识及完整旧部署配置。密钥必须与这份数据库一致：至少保存 `DATA_ENCRYPTION_KEY`；若部署设置了 `DATA_ENCRYPTION_LEGACY_KEY` 或其他历史 key-ring/decryption key，也必须原样保存全部适用的旧密钥。不要在这一步轮换密钥、用新密钥覆盖旧密钥，或只备份 `.env` 而漏掉外部 secret store。备份产物和 `.sha256` 均须非空并通过校验。
3. **用旧版匹配二进制做隔离的实际数据库恢复演练。** 只能在临时、与生产隔离且可销毁的数据库、存储和网络环境恢复副本；使用与该副本 schema/写入合同匹配的旧版 Core/worker 二进制和同一组 `DATA_ENCRYPTION_KEY`/适用历史密钥启动，实际登录并读取任务、审计和备份资产，再用匹配的旧版数据库/备份工具把选定副本恢复到临时目标并核对恢复结果。只执行 `PRAGMA integrity_check`、`pg_restore --list` 或检查文件存在不算恢复演练。演练应验证敏感字段可解密、任务/运行历史和审计可读；演练副本绝不能连接生产数据库、Provider 写入端或通知渠道。
4. **新版本先迁移，后启动新 worker。** 旧进程确认全部退出且恢复演练通过后，才部署新匹配二进制。先由新版本执行成对迁移并确认 `schema_migrations` 为 clean，再启动新的 Core、scheduler、executor、collector、notification worker；不得把“新 Core 正在迁移”与旧 worker 并行，也不得在迁移完成前启动新 worker。旧版和新版不得混写同一数据库、同一投递状态或同一备份资产控制面。
5. **核对保留和围栏事实。** 迁移后检查备份资产、任务/TaskRun 历史、任务日志、审计、告警行、升级历史和已发送投递事实仍在；检查退役告警已 resolved/不可重试，未发送投递为 `unknown` 且原因 `feature_retired`、无 lease/next-retry，不能被自动或手动 claim。旧配置导入若包含退役设置键必须整份正常拒绝，不能静默丢弃。只有这些核对完成后才解除维护窗口。

#### SQLite 与 PostgreSQL 恢复边界

- **SQLite：** 数据库恢复必须离线执行。停止整个 Compose 栈以及任何独立旧 worker，确认没有运行中的 Xirang 进程后，使用 `XIRANG_RESTORE_OFFLINE=1` 调用 `scripts/restore-db.sh`；保留并校验升级前的数据库副本，按脚本处理 `-wal`/`-shm`，再运行 `PRAGMA integrity_check`。不要覆盖仍被进程打开的数据库文件，也不要只替换主库而留下旧 sidecar。
- **PostgreSQL：** 恢复和迁移前必须由 DBA/运维人员手动停止并排空所有 Core、scheduler、executor、collector、notification worker 及其连接；核对数据库活动会话和写入者均已退出后，才使用匹配的 `pg_restore`/SQL 恢复。仅执行 `docker compose stop` 或停止一个 HTTP 容器不足以满足 PostgreSQL 的手动 stop/drain 要求；恢复、迁移期间不得让旧版或新版应用连接同一目标库。

若隔离恢复演练、备份校验、排空确认或迁移后的保留/围栏核对任一失败，**STOP**：不要重试到生产库，不要手工修 `schema_migrations`，恢复升级前数据库并使用匹配旧版二进制和原密钥处理。该迁移的 down SQL 明确失败，不能作为回滚路径。

### 跨数据合同升级

涉及恢复捕获、投递认领、定时意图或备份完成事实的升级，须先按[备份职责收敛升级](#备份职责收敛升级)停止并排空全部旧 Core、scheduler、executor、collector、notification worker，备份数据库、加密密钥和备份数据，完成隔离恢复演练，再让新 Core 执行迁移。不要混用不理解当前数据合同的旧进程写同一数据库。

- 历史 Rsync 成功记录不会自动成为可信捕获证据。**先隔离保全唯一剩余备份**，再决定是否重新备份；不要为了满足恢复准入而覆盖最后一份数据。详见[旧版 Rsync 恢复准入](admin/backup-recovery.md#旧版-rsync-恢复准入)。
- 告警投递意图会在重启后恢复，已发送但回执未提交的外部结果仍可能重复投递；历史未知投递决策不会被盲目重发。升级后检查通知状态与接收通道，不要把未知状态视为已发送。
- 到期定时意图会持久化并等待容量；升级后检查积压及共享可变目标的未决占用，不要以删除运行记录解除阻塞。
- 健康数据只使用可证明的备份完成事实。普通命令成功、导入基线或无法证明的旧时间戳不会自动成为可信备份，升级后可能显露此前未被正确标记的证据不足。
- 新捕获使用可保全路径字节的 v2 manifest 及配套根字段编码；旧 v1 证据仍可读取，但不得让旧 Core 消费新 v2 证据。恢复前需要在 Core 上为选中内容的私有暂存副本预留磁盘空间；暂存不能替代原始备份保全。
- 通知冷却期按真实发送成功时间计算；历史空时间戳不会被补造成成功。飞书、钉钉和企业微信需要业务成功回执，通用 webhook 保持 HTTP 2xx 语义。
- 已使用的捕获、代次、投递或健康证据会阻止不安全 schema 降级。不要删除证据、修改迁移版本或强行混用旧二进制。优先前向修复；回退必须同时保全数据库、可变备份树和通知回执状态，不能只替换镜像。

具体版本变化查阅 GitHub Release 与 CHANGELOG；当前迁移位置和受检查版本号见[后端入口](../backend/README.md)，配对迁移与降级要求见[数据库合同](spec/backend/database-guidelines.md)。

### 回滚与灾难恢复

本次备份职责收敛迁移执行后不提供日常版本回滚：down SQL 明确失败，迁移驱动写入旧版本号的动作受 `schema_migrations` guard 拦截。不得手工修改版本号或 dirty 标记、删除 guard、直接执行 down、删数据来“凑”旧 schema，或让旧二进制绕过保护器继续写新库。

发生不可接受的迁移结果或需要回到旧版时，走显式灾难恢复，而不是替换镜像：

1. **STOP** 新版 Core、scheduler、executor、collector、notification worker，并等待所有新写入和投递 lease 收敛；按 SQLite 离线或 PostgreSQL 手动 stop/drain 要求确认没有连接和写入者。
2. 从升级前已验证的数据库备份恢复**整个升级前数据库**，不要用迁移后的库拼接或只恢复某几张表；同时恢复与它匹配的旧版二进制/镜像、完整旧配置、`DATA_ENCRYPTION_KEY` 及所有适用的 `DATA_ENCRYPTION_LEGACY_KEY`/历史解密密钥。
3. 仅启动匹配旧版的 Core 和旧 worker，先核对 schema、任务/审计/投递状态及备份资产控制面，再恢复对外服务。新版产生的控制面写入不能假定存在于旧库；需要保留的升级后资产和外部备份必须另行保全，不得把 Provider 数据清理或数据库降级当作资产恢复。
4. 记录灾难恢复审计和丢失窗口；完成新的备份与恢复验证后，才能重新规划前向升级。显式灾难恢复是唯一解除不可逆退役影响的路径，不能作为日常发布回滚或 `schema_migrations` bypass。

## 数据目录与备份

Docker Compose 默认持久化目录：

| 宿主机路径 | 容器路径 | 用途 |
|---|---|---|
| `./data` | `/data` | SQLite 数据库及应用数据 |
| `./backups` | `/backup` | 自动/手动备份文件 |
| `./logs` | `/logs` | 应用日志与 Nginx 访问/错误日志 |

根 Compose 还为 Core 挂载两个运行时 socket named volume，以及 Derived Store 和 Export Store 的独立持久 volume；这些挂载不以启用 Worker profile 为前提。启用备份资产前必须满足私有存储权限和路径就绪条件，详见上文可选 profile。数据库和 Provider 仓库以外的加密派生/导出数据也应按保留需求纳入运维盘点；不要把 `docker compose down -v` 当成升级或功能回退。

容器内置 cron：

| 时间 | 操作 |
|---|---|
| 每日 02:00 | 执行 `backup-db.sh` 备份数据库到 `/backup/db/` |
| 每日 02:30 | 清理 30 天前的旧备份文件，并一并删除 30 天前残留的 `*.tmp.*` 临时文件 |

cron 只按文件 `mtime` 清理（30 天），不读取 `DB_BACKUP_MAX_COUNT`；`DB_BACKUP_MAX_COUNT`（默认 20）只约束管理 API `POST /system/backup-db` 的保留数量。备份脚本 `backup-db.sh` 同时承担两层兜底：备份开始前清理 `output_dir` 中 pid 已消失或超过 24h 的孤儿 `*.tmp.*`（含 SQLite 的 `-journal`/`-wal`/`-shm` 边车文件），并且本次失败不会留下临时文件；SQLite 备份前还会检查目标文件系统可用空间至少为「源库大小 + 64MiB」，不足则直接拒绝并返回非零，不会开始 `.backup`。

系统维护页将 **Web SQLite 快照** 和 **cron 产物观测** 分开展示。Web 快照仅含
SQLite 数据库，不包含外部配置、当前/历史加密密钥、known_hosts 或远端数据；
这些材料必须另行保全。Web 列表读取失败可重试，不能把失败当“暂无备份”。
PostgreSQL 的 Web 自备份不支持提示也不代表没有 cron 数据库备份。

All-in-One 设置 `CRON_DB_BACKUP_DIR=/backup/db`；源码/其他部署默认不观测，
需要明确配置该进程可读的目录。页面仅显示容器内配置目录，不推断宿主机路径。
`CRON_DB_BACKUP_MAX_AGE_HOURS` 默认 26，允许正整数 1–8760；无效配置、目录不可读、
无完整对、扫描超限、未来时间和过期均有独立提示。
“最近完整备份（由产物推断）”只检查本引擎受管产物及同名 `.sha256` 文件，
以两者较晚 mtime 判断新鲜度，**未重新校验内容**。
目录存在或状态 fresh 都不能证明 cron 已启用、作业成功或数据库可恢复；
仍须按下述步骤执行真实离线恢复核对。

### 手动备份与恢复

`backup-db.sh` 是 cron 和升级前保全共同使用的唯一数据库备份契约。成功必须同时生成非空数据库产物和同名 `.sha256` 校验文件；任一工具缺失、备份为空或校验文件生成失败都会返回非零。All-in-One 镜像已内置 `sqlite3` 和 PostgreSQL `pg_dump`/`pg_restore` 客户端。

SQLite 备份（在线 `.backup`，不会直接复制 WAL 主文件）：

```bash
DB_TYPE=sqlite SQLITE_PATH=./data/xirang.db \
  bash scripts/backup-db.sh ./backups
backup_file="$(find ./backups -maxdepth 1 -type f -name 'xirang-sqlite-*.db' -printf '%T@ %p\n' | sort -nr | sed -n '1s/^[^ ]* //p')"
test -n "${backup_file}" && test -s "${backup_file}" && test -s "${backup_file}.sha256"
```

SQLite 恢复**必须离线执行**。停止整个 Compose 栈（不要只覆盖正在运行的数据库文件），确认没有运行中的 Xirang 后，再使用显式维护窗口变量；脚本会校验 SHA-256、执行 `PRAGMA integrity_check`，将文件写入同目录临时文件后原子替换，并仅在该离线路径删除旧的 `-wal`/`-shm` sidecar：

```bash
backup_file="$(find ./backups -maxdepth 1 -type f -name 'xirang-sqlite-*.db' -printf '%T@ %p\n' | sort -nr | sed -n '1s/^[^ ]* //p')"
test -n "${backup_file}" && test -s "${backup_file}" && test -s "${backup_file}.sha256"
docker compose stop
if docker compose ps --status running --services | grep -q .; then
  echo "仍有 Compose 服务运行，拒绝恢复" >&2
  exit 1
fi

XIRANG_RESTORE_OFFLINE=1 DB_TYPE=sqlite SQLITE_PATH=./data/xirang.db \
  bash scripts/restore-db.sh "${backup_file}"
test "$(sqlite3 ./data/xirang.db 'PRAGMA integrity_check;')" = ok

docker compose start
until curl -fsS http://127.0.0.1:10761/readyz >/dev/null; do sleep 1; done
curl -fsS http://127.0.0.1:10761/healthz
```
恢复后只启动与该备份 schema 匹配的二进制和原配置/密钥；若这是职责收敛迁移的灾难恢复，必须启动升级前旧版而不是当前新版。上面的 `docker compose start` 仅适用于镜像、schema 与备份匹配且已完成隔离核对的普通恢复。

PostgreSQL 备份可使用在线 `pg_dump`，但升级前仍必须先停止并排空会写该库的旧 worker；恢复不是在线操作。先由 DBA/运维手动停止并 drain 全部 Core、scheduler、executor、collector、notification worker，核对 `pg_stat_activity` 等活动会话已无旧/新应用写入，再执行恢复；`docker compose stop` 不能替代 PostgreSQL 的连接排空：

```bash
DB_TYPE=postgres DB_DSN='postgresql://user:pass@host:5432/xirang' \
  bash scripts/backup-db.sh ./backups

# 在人工 stop/drain、确认无应用连接后执行；恢复期间不得启动任何旧/新版 worker
DB_TYPE=postgres DB_DSN='postgresql://user:pass@host:5432/xirang' \
  bash scripts/restore-db.sh ./backups/xirang-postgres-20260301-020000.dump
```

恢复完成后仍须使用与备份匹配的二进制和密钥启动，并在隔离环境完成实际恢复核对；不得把 PG 恢复命令当作允许旧/新 writer 并行的信号。

## 健康检查与日志

`/healthz` 仅表示后端能处理请求；`/readyz` 用 2 秒超时 Ping 数据库，不验证每个业务表、备份仓库、Worker 或第三方通道。入口脚本最多等待后端就绪约 30 秒再启动 Nginx，后端、定时任务进程或 Nginx 任一退出都会终止容器。容器显示 healthy 不能代替一次真实登录和所需业务路径验证。

Compose 的 Docker `json-file` 日志限制为每文件 `10m`、最多 3 个，只约束容器标准输出。`/logs/xirang.log` 和 Nginx 日志没有内置轮转；需在宿主机另行安排轮转和空间监控。后端长期持有日志文件句柄，不会自动重新打开被重命名的文件，轮转方案须适配这一行为。日志文件打开失败时后端回退为仅标准输出并记录错误，不会因此拒绝启动。

```bash
# 容器状态
docker compose ps

# 实时 stdout 日志
docker compose logs -f xirang

# 最近 200 行 stdout 日志
docker compose logs --tail=200 xirang

# 持久化日志文件（Nginx 访问日志请求行会省略查询字符串）
ls -lah ./logs
tail -f ./logs/xirang.log
# 宿主机就绪检查（Compose healthcheck 使用此端点，会 Ping 数据库）
curl -fsS http://127.0.0.1:10761/readyz
# 宿主机进程存活检查（不访问数据库）
curl -fsS http://127.0.0.1:10761/healthz
```

进入容器排查：

```bash
docker exec -it xirang bash
```

查看 SQLite 表数据示例：

```bash
docker exec -it xirang sh -lc \
  "sqlite3 /data/xirang.db 'SELECT count(*) FROM tasks;'"
```

## Prometheus `/metrics`

All-in-One 入口不代理 `/metrics`，外层代理也必须能直接连到后端才可转发此路径。抓取地址、Token、Prometheus 示例与备份资产指标统一见[监控指南](admin/monitoring-alerting.md#prometheus-指标)。

## 迁移 dirty 状态排障

后端使用 golang-migrate 维护 `schema_migrations`。如果上一次迁移异常中断，启动日志可能出现：

```text
schema_migrations.dirty=1
```

服务会无条件拒绝启动，避免基于半完成 schema 继续写入数据。

处理步骤：

1. **先保全现场并制作可验证备份**：SQLite 同时保留数据库、WAL/SHM 和启动日志；PostgreSQL 保留 dump、迁移日志与对应镜像版本。不要直接修改或删除 `schema_migrations`。
2. 查看 dirty 版本：
   ```bash
   # SQLite
   sqlite3 ./data/xirang.db "SELECT version, dirty FROM schema_migrations;"

   # PostgreSQL
   psql "$DB_DSN" -c "SELECT version, dirty FROM schema_migrations;"
   ```
3. 根据只读诊断结果选择恢复方式：
   - `dirty=1`：停止服务，优先恢复已验证的迁移前备份，再重新升级。确需保留当前数据时，只能由数据库/迁移负责人执行有审计记录的离线修复，并在完整性校验后再启动服务。
   - 版本记录为 clean、但日志报告 schema drift：不要提高版本号或补写 clean 标记；恢复已验证备份，或按发布方给出的精确对象清单执行有审计记录的离线修复。

服务启动路径不会自动 `force`、重试 dirty 迁移或在 schema 不完整时继续写入。不要用手工版本号变更掩盖失败迁移。

<a id="utc-时间戳约定"></a>
## 数据库时间与迁移开发

数据库时间统一按 UTC 写入；容器 `TZ` 影响定时任务与运维展示，不改变数据库存储合同。新增迁移和 UTC 检查步骤统一见[数据库合同](spec/backend/database-guidelines.md)与[后端入口](../backend/README.md)。

## 本地构建镜像（高级用户）

普通部署应优先使用官方预构建镜像。需要自行构建时：

```bash
docker build -f deploy/allinone/Dockerfile \
  -t docker.io/linnea7171/xirang:vX.Y.Z-local .
```

多架构构建：

```bash
docker buildx create --use

docker buildx build \
  --platform linux/amd64,linux/arm64 \
  -f deploy/allinone/Dockerfile \
  -t docker.io/linnea7171/xirang:vX.Y.Z-local \
  --push .
```

本地构建默认不要推送或覆盖 `latest`；`latest` 只应由正式 GitHub Release 触发的发布 workflow 更新。
