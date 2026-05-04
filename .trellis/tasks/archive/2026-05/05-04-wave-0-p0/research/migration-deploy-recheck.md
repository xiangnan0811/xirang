# 迁移/部署 finding 复核

- 复核日期：2026-05-03
- 复核范围：D-1 ~ D-5（迁移与部署 P1）
- 方法：实读 migration SQL、模型、加密实现、Dockerfile、compose、entrypoint

---

## D-1 Integration.Endpoint 容量

- **状态**：⚠️部分真实（PostgreSQL 真实风险，SQLite 无实际风险）

### 证据

- baseline 字段类型（**未被后续迁移修改**）：
  - SQLite: `endpoint VARCHAR(1024) NOT NULL`
    - `backend/internal/database/migrations/sqlite/000001_baseline.up.sql:73`
  - PostgreSQL: `endpoint VARCHAR(1024) NOT NULL`
    - `backend/internal/database/migrations/postgres/000001_baseline.up.sql:73`
- 后续迁移仅新增 `secret`(TEXT)/`proxy_url`(TEXT)，未 ALTER endpoint：
  - `migrations/sqlite/000013_integration_secret.up.sql:4`：`ADD COLUMN secret TEXT`
  - `migrations/sqlite/000029_integration_proxy.up.sql:1`：`ADD COLUMN proxy_url TEXT`
  - postgres 同（grep 已验证，无 endpoint ALTER）
- 模型层定义（`backend/internal/model/models.go:121-134`）：
  - `Endpoint string \`gorm:"size:1024;not null"\`` —— 与迁移一致
  - `Secret string \`gorm:"size:512"\`` —— **与迁移 TEXT 不一致**（GORM tag 仅在 AutoMigrate 影响新表，迁移 SQL 为权威；现有部署 secret 为 TEXT，不受 size:512 限制）
  - `ProxyURL string \`gorm:"size:512;not null;default:''"\`` —— 同上
- 加密实现（`backend/internal/secure/crypto.go:117-180`）：
  - 格式：`enc:v2:` (7B) + base64(nonce 12B || ciphertext N B || GCM tag 16B)
  - 总密文长度公式：`7 + ceil((N+28)/3)*4`

### 加密后预估长度（明文 → 密文）

| 明文 N | 二进制 N+28 | base64 长度 | 总长 |
|---|---|---|---|
| 256 | 284 | 380 | 387 |
| 512 | 540 | 720 | 727 |
| 734 | 762 | 1016 | **1023** |
| 740 | 768 | 1024 | **1031**（溢出） |
| 1024 | 1052 | 1404 | 1411 |

→ **明文 endpoint > 734 字节即超 VARCHAR(1024)**。

### 是否真有溢出风险

- **PostgreSQL**：硬约束。Webhook URL 通常 < 200 字节，但飞书/钉钉/企业微信带 access_token 的 webhook URL 实测 200~400 字节，加密后 ≤ 600，**正常使用安全**。极端长 URL（带签名/key/sign 多参）可能逼近 700 → **风险存在但很低**。
- **SQLite**：`VARCHAR(n)` 是软约束，等价于 TEXT，**不会真的截断或报错**。子代理在 SQLite 路径上的判断不准。

### 修复

- 将 PostgreSQL `integrations.endpoint` ALTER 至 TEXT，与 secret/proxy_url 对齐
- 将模型 tag `size:512` 调成 `size:0` 或显式 TEXT，避免新部署 AutoMigrate 走小列
- 工作量：**S**（一条迁移 + 模型 tag 改两行 + down 迁移）

---

## D-2 日志无文件落盘

- **状态**：✅真实

### 证据

- `backend/internal/logger/logger.go:22`：`Log = zerolog.New(os.Stdout)`
  - 唯一输出目的地为 `os.Stdout`，无文件 sink，无 multi-writer
- `backend/cmd/server/main.go:51`：`logger.Init(os.Getenv("LOG_LEVEL"))`
  - 仅传 LOG_LEVEL，无 LOG_FILE / LOG_PATH 等参数
- 全代码仓库 grep `LOG_FILE\|log.Out\|Output\|os.Stderr` 在 logger 目录内只命中 `os.Stdout` 一处
- `deploy/allinone/Dockerfile:1-83`：无 `>` 重定向，无 logrotate，无 syslog 转发；`entrypoint.sh:32-39` 直接 `/usr/local/bin/xirang &` 启动后端，stdout 流向容器
- `docker-compose.prod.yml:1-22`：无 `logging:` 段（未配 driver/options），使用 Docker 默认 `json-file` driver
- Docker 默认 `json-file` driver 无 max-size/max-file 限制 → 持续增长，仅 `docker logs` 可读，容器删除即丢

### 是否真有问题

- 真实。无任何"重启不丢"的本地回看路径。`docker logs` 在容器重建时清零；当前未启用 logrotate / journald 持久化。
- 影响：事故复盘时若已重建容器，日志归零；磁盘耗尽风险（未限大小）

### 修复

- 短：`docker-compose.prod.yml` 加 `logging.driver: json-file` + `options.max-size/max-file`
- 长：logger 支持 LOG_FILE 双写（lumberjack 滚动），并把日志卷挂出
- 工作量：**S**（短期方案，2 行 yaml）/ **M**（长期 logger 改造）

---

## D-3 开发 docker-compose 无数据库卷挂载

- **状态**：⚠️部分真实（设计上挂的是源码目录，包含 db；但未独立 data 卷）

### 证据

- `docker-compose.yml:6-7`：`volumes: - ./:/workspace`（**整个项目根目录挂入容器**）
- `docker-compose.yml:12`：`SQLITE_PATH: /workspace/backend/xirang.db`
  - SQLite 文件实际写到宿主 `./backend/xirang.db`，**不会因容器重启丢失**
  - 但会污染源码目录（git ignore 已排除）

### 是否真有"重启丢 SQLite"

- **否**。子代理结论"重启丢 SQLite"是误报。db 文件持久化在宿主 `./backend/xirang.db`。
- 真问题是：
  - SQLite 在源码树里，不利于 reset；
  - dev compose 没有显式 data 卷（不致命，dev only）

### 修复

- 可选：把 SQLITE_PATH 改到独立 `./data/dev.db` + `./data:/data` 卷，避免污染
- 工作量：**S**（非必需）

---

## D-4 缺 TZ 环境变量

- **状态**：✅真实

### 证据

- `docker-compose.yml`：无 TZ
- `docker-compose.prod.yml:1-22`：仅 `env_file: .env`，未显式 TZ
- `.env.deploy`、`backend/.env.example`、`backend/.env.production.example`、`web/.env.example`：grep `TZ` 全部无匹配
- `deploy/allinone/Dockerfile`：未 `apk add tzdata`，未设 ENV TZ
- 后端日志使用 RFC3339（`backend/internal/logger/logger.go:29`），含时区偏移
  - 但 Go `time.Local` 在 alpine 无 tzdata 时退化为 UTC
  - 业务侧若依赖 cron 本地时间（如 supercronic 的 `xirang-backup.cron`）会按 UTC 解释 → 与运维预期错位

### 是否真有影响

- **是**。
  - Go runtime 在 alpine 无 tzdata 时无法解析 `Asia/Shanghai`，`time.LoadLocation` 返回错误
  - supercronic / cron 表达式按 UTC 触发
  - 用户界面显示时间通常前端转换，问题相对小，但日志/cron/数据库时间戳行为不一致

### 修复

- Dockerfile：`RUN apk add --no-cache tzdata` + `ENV TZ=UTC`（或允许 .env 注入）
- `.env.deploy.example` 加 `TZ=Asia/Shanghai` 注释项
- 工作量：**S**

---

## D-5 deploy/allinone/Dockerfile 缺 HEALTHCHECK

- **状态**：⚠️部分真实（Dockerfile 确实缺，但 compose 已补）

### 证据

- `deploy/allinone/Dockerfile:1-83`：grep `HEALTHCHECK` 无任何匹配
- `docker-compose.prod.yml:15-20`：**有** healthcheck：
  ```
  test: ["CMD", "curl", "-fsS", "http://127.0.0.1:8080/healthz"]
  interval: 30s; timeout: 5s; retries: 3; start_period: 15s
  ```
- entrypoint 内部已有自检（`entrypoint.sh:73-91`）：启动期最多 30s 等后端 `/healthz`，未就绪则退出

### 是否真有影响

- **轻微**。
  - 仅 `docker run`（不经 compose）启动镜像时无 HEALTHCHECK，K8s/裸 docker 用户拿不到健康状态
  - compose 用户已被 prod compose 兜底，问题受限
- entrypoint 启动期把关已部分覆盖"启动失败立即退出"，但运行期降级（如 backend 死掉但 nginx 还在、healthz 502）依赖 compose 的 healthcheck

### 修复

- Dockerfile 加 `HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 CMD curl -fsS http://127.0.0.1:8080/healthz || exit 1`
- 与 compose 端的健康检查保持一致
- 工作量：**S**（一行）

---

## 总体结论

| Finding | 真实/误报 | 优先级 | 工作量 |
|---|---|---|---|
| D-1 Endpoint VARCHAR(1024) | ⚠️部分真实（PG 才有） | P2 | S |
| D-2 日志无文件落盘 | ✅真实 | **P1** | S/M |
| D-3 dev compose 无 data 卷 | ❌误报（dev 不丢） | P3 可选 | S |
| D-4 缺 TZ | ✅真实 | **P1** | S |
| D-5 Dockerfile 缺 HEALTHCHECK | ⚠️部分真实（compose 已兜底） | P2 | S |

### 真实/误报统计
- 完全真实：2（D-2、D-4）
- 部分真实：2（D-1、D-5）
- 误报：1（D-3 关于"重启丢 db"）

### 优先修复建议
1. **P1 D-2 日志持久化**：先在 prod compose 加 `logging.driver: json-file` 限大小（2 行 yaml，零回归风险）。中期在 logger 加 LOG_FILE。
2. **P1 D-4 TZ + tzdata**：Dockerfile 装 tzdata + ENV TZ；env.example 给出注释项。
3. **P2 D-1 Endpoint**：单独迁移把 PG 的 `integrations.endpoint` ALTER 为 TEXT，并对齐模型 tag（消除 secret/proxy_url 的 size:512 与 TEXT 不一致）。
4. **P2 D-5 Dockerfile HEALTHCHECK**：补一行，统一 docker run 与 compose 行为。
5. **D-3 跳过**或改造为"独立 data 卷"清理项。

### 关于子代理误报
- 子代理对 SQLite VARCHAR 软约束的认知不准（D-1）
- 子代理把"无独立 data 卷"等同于"重启丢库"（D-3），实际 dev compose 挂的是项目根 `./:/workspace`，db 已持久化
- D-2/D-4 判断准确，D-5 没看 compose 兜底

## 关键文件路径
- `/Users/weibo/Code/xirang/backend/internal/database/migrations/sqlite/000001_baseline.up.sql:73`
- `/Users/weibo/Code/xirang/backend/internal/database/migrations/postgres/000001_baseline.up.sql:73`
- `/Users/weibo/Code/xirang/backend/internal/model/models.go:121-185`
- `/Users/weibo/Code/xirang/backend/internal/secure/crypto.go:117-180`
- `/Users/weibo/Code/xirang/backend/internal/logger/logger.go:22`
- `/Users/weibo/Code/xirang/backend/cmd/server/main.go:51`
- `/Users/weibo/Code/xirang/docker-compose.yml:6-13`
- `/Users/weibo/Code/xirang/docker-compose.prod.yml:1-22`
- `/Users/weibo/Code/xirang/deploy/allinone/Dockerfile:1-83`
- `/Users/weibo/Code/xirang/deploy/allinone/entrypoint.sh:73-91`
