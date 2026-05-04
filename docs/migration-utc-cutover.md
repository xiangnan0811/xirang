# UTC Cutover Migration Runbook (000050_utc_cutover)

本 runbook 指导生产 / staging 环境如何安全执行 `000050_utc_cutover` 迁移。该迁移把所有
历史 timestamp 列从本地时区 (Asia/Shanghai, UTC+8) 平移到 UTC，配合 GORM `NowFunc=time.Now().UTC()`
与数据库连接 `_loc=UTC` / `timezone=UTC` 形成端到端 UTC 存储。

> **关键风险**：迁移**不可幂等**——重复运行会让所有时间戳被减第二次 8h，无法自动检测。
> 必须在停服窗口内一次性完成；任何中断都要走 Rollback 重来。

---

## 0. 前置检查（迁移前必读）

1. **当前部署确实在 UTC+8（Asia/Shanghai）**
   - 容器宿主或裸机 `date +%z` 输出 `+0800`
   - 老 GORM 写入时使用 `time.Now()`（本地时区），所以 DB 内 timestamp 字面值反映了 +8 小时
   - 如果你的部署在其他时区（如 +9/+0/-5），**不能直接套用本迁移**——必须 fork
     `migrations/sqlite/000050_utc_cutover.up.sql`、`migrations/postgres/000050_utc_cutover.up.sql`
     与对应 down 文件，把所有 `'-8 hours'` / `INTERVAL '8 hours'` 改成与你的本地时区偏移相符的值
2. **当前数据库版本必须是 `49`**（`000049_policy_max_execution_seconds`）
   - SQLite: `sqlite3 /data/xirang.db "SELECT version, dirty FROM schema_migrations;"`
   - PostgreSQL: `SELECT version, dirty FROM schema_migrations;`
   - 应返回 `49 | 0`（dirty=0）。如果 dirty=1 必须先修复历史问题再上车
3. **代码版本对齐**：要升到的镜像 / 二进制必须包含本 commit（GORM `NowFunc=UTC`、SQLite DSN
   `_loc=UTC`、PostgreSQL DSN `timezone=UTC` 三处改动）。否则平移完成后，业务还在写 Local
   时间，会立刻出现新数据偏离 UTC 的问题
4. **应用层已停**：迁移前必须 `make prod-down` 或等价操作；任何 in-flight 写入都会让 race
   出现（部分行被减 8h，部分行已经是 UTC，无法事后区分）

---

## 1. Backup（必做，无回头路）

### SQLite

```bash
# 假设数据库默认路径是 /data/xirang.db；按实际部署调整
TS=$(date +%Y%m%d-%H%M%S)
sudo cp /data/xirang.db /backup/xirang-pre-utc-cutover-$TS.db.bak
sudo sha256sum /backup/xirang-pre-utc-cutover-$TS.db.bak | tee /backup/xirang-pre-utc-cutover-$TS.sha256
```

确认备份大小与原文件一致：

```bash
ls -lh /data/xirang.db /backup/xirang-pre-utc-cutover-$TS.db.bak
```

### PostgreSQL

```bash
TS=$(date +%Y%m%d-%H%M%S)
pg_dump -F c -f /backup/xirang-pre-utc-cutover-$TS.pgdump \
  -h <host> -U <user> <dbname>
sha256sum /backup/xirang-pre-utc-cutover-$TS.pgdump | tee /backup/xirang-pre-utc-cutover-$TS.sha256
```

> **绝不要跳过 backup**。down.sql 假设你只跑过一次 up.sql；如果因任何原因数据进入不一致
> 状态，恢复 backup 是最快的回滚路径。

---

## 2. 停服

```bash
# Docker Compose 部署
make prod-down

# 或裸机 systemd
sudo systemctl stop xirang-backend
```

确认无 task_runs 还在 running：

```bash
# SQLite
sqlite3 /data/xirang.db "SELECT COUNT(*) FROM task_runs WHERE status = 'running';"
# PostgreSQL
psql -c "SELECT COUNT(*) FROM task_runs WHERE status = 'running';"
```

预期返回 `0`。如果非 0，说明有任务在停服时仍处于 running（可能是调度器异常或上次未清理）；
此时手动把它们置为 `failed`：

```sql
UPDATE task_runs SET status='failed', last_error='killed by utc-cutover maintenance', finished_at = started_at WHERE status='running';
```

注意：上面这条 SQL 写的 `finished_at` 仍是老 Local 时间，**会被本迁移随后一起 -8h 平移**——
正常路径，不需要额外处理。

---

## 3. Migrate

> 迁移由后端启动时自动执行（`bootstrap` 调用 `database.RunMigrations`）。**不需要手动
> 运行 SQL**——只要新镜像启动就会触发 `migrate.Up()` 把 49 → 50。

`docker compose up -d` 一启动就会被反向代理 / 健康检查接流量，**做不到「只观察日志
不接流量」**。推荐用下列两种方式之一确保 cutover 期间没有用户请求与迁移并发：

### 推荐方式 A — 用 `docker compose run` 单跑迁移容器（最干净）

`docker compose run` 不会启动 `depends_on` 的服务，也不映射 compose 文件里的端口；
适合用一次性容器只执行 `migrate.Up()` 然后退出，再正常启动整套服务。

```bash
# 1. 升级镜像。假设镜像 tag 已切到含本 commit 的版本。
docker compose pull

# 2. 单跑后端容器执行迁移，前台直接看日志；进程在等迁移完成 → 启动 HTTP 服务。
#    我们用 ALLOW_DIRTY_STARTUP=false（默认）让 dirty=1 立即拒绝启动。
#    确认看到 "数据库迁移完成，当前版本: 50, dirty: false" 后用 Ctrl-C 退出。
docker compose run --rm \
  --service-ports="" \
  -e ALLOW_DIRTY_STARTUP=false \
  xirang-backend 2>&1 | tee /tmp/utc-cutover-migrate.log

# 3. Verify（见下一节）通过后再正常启动整套服务：
make prod-up
```

> Compose 版本若不支持 `--service-ports=""`，可用 `docker compose run --rm
> --no-deps -p '' xirang-backend ...` 替代；目标是让该容器**不映射任何宿主端口**。

### 推荐方式 B — 临时阻断流量后启动整套服务

如果反向代理在 compose 文件里且不能拆分启动顺序，可在 nginx/HAProxy 层暂时
`return 503` 或在主机 firewall 阻断对外端口，让 backend 正常启动跑完迁移：

```bash
# 1. 升级镜像
docker compose pull

# 2. 在反向代理层加 return 503（或 firewall block 80/443），然后启动
make prod-up
docker compose logs -f xirang-backend | grep -E "(数据库迁移完成|migrat|ERROR|FATAL)"
```

期望看到：

```
数据库迁移完成，当前版本: 50, dirty: false
```

观察 1-2 分钟无 ERROR / FATAL 日志后停服进入 Verify：

```bash
make prod-down
```

### 失败处理

如果日志出现 `dirty: true` 或迁移报错，**不要重启**——直接进入 Rollback。
后续启动时本服务的 dirty 守卫（`migrator.go:checkMigrationDirty`）会拒绝启动并
提示设置 `ALLOW_DIRTY_STARTUP=true` 才能跳过；切勿在没有完整恢复备份前直接
设置该 env，否则可能导致永久双时区污染。

---

## 3.5. Dirty 状态恢复

启动日志显示 `FATAL: schema_migrations.dirty=1, version=50` 时表示前次迁移在
执行过程中失败但 schema_migrations 没有被驱动自动标记 clean。**继续启动会基于
半完成的 schema 写入数据**，最坏情况是双时区污染。

修复决策树：

1. **首选：恢复备份**（参考第 5 节 Path A）——切回上个版本镜像 + 还原 backup +
   重启。这是数据最安全的回滚方式。
2. **次选：跑 down.sql 回滚**（参考第 5 节 Path B）——仅当确认 up.sql 完整跑完
   再失败（例如发生在 COMMIT 之后），用 down.sql 把所有时间戳加回 +8h。
3. **特殊情况：手工 clean 标记**——如果你已经确认数据完全没受到中间态影响
   （例如失败发生在 ALTER 之类 DDL 上而不是 UPDATE 中段），可以：
   ```bash
   # 用 golang-migrate CLI 强制标记 clean
   migrate -path backend/internal/database/migrations/sqlite \
           -database "sqlite3:///data/xirang.db" force 50
   # 然后正常启动；服务会通过 dirty 检查
   make prod-up
   ```
4. **紧急绕过（仅 rescue）**：环境变量 `ALLOW_DIRTY_STARTUP=true` 让服务跳过
   dirty 检查直接启动，用于短暂上线运行手工修复 SQL。**修复完后必须 unset 重启**。
   ```bash
   ALLOW_DIRTY_STARTUP=true make prod-up
   # ...修复...
   unset ALLOW_DIRTY_STARTUP
   make prod-down && make prod-up
   ```

---

## 4. Verify

启动一个一次性 SQLite/PSQL shell，跑下面的 sanity 检查：

### SQLite

```sql
-- 验证迁移已应用且 clean
SELECT version, dirty FROM schema_migrations;
-- 期望: 50 | 0
```

跨库对比验证（用第 1 节中记录的 $TS 值替换占位符 `<your-TS>`）。把 ATTACH
路径整体替换成第 1 节命名的真实备份文件，例如：
`/backup/xirang-pre-utc-cutover-20260504-123045.db.bak`。

```sql
ATTACH DATABASE '/backup/xirang-pre-utc-cutover-<your-TS>.db.bak' AS old;

-- 抽样断言：应当返回 0.333..."天"（即 8 小时），用 julianday() 直接算 diff
SELECT
  julianday(
    (SELECT created_at FROM old.users ORDER BY id DESC LIMIT 1)
  ) -
  julianday(
    (SELECT created_at FROM main.users ORDER BY id DESC LIMIT 1)
  ) AS diff_days;
-- 期望: 0.3333... (即 8 小时；任何其他值意味着 cutover 不正确)

-- 多表抽样：tasks / alerts / audit_logs 都应当返回相同的 ~0.333
SELECT 'tasks' AS tbl,
       julianday((SELECT MAX(created_at) FROM old.tasks)) -
       julianday((SELECT MAX(created_at) FROM main.tasks)) AS diff_days
UNION ALL
SELECT 'alerts',
       julianday((SELECT MAX(created_at) FROM old.alerts)) -
       julianday((SELECT MAX(created_at) FROM main.alerts))
UNION ALL
SELECT 'audit_logs',
       julianday((SELECT MAX(created_at) FROM old.audit_logs)) -
       julianday((SELECT MAX(created_at) FROM main.audit_logs));

DETACH DATABASE old;
```

### PostgreSQL

```sql
SELECT version, dirty FROM schema_migrations;
-- 期望: 50 | f
```

PostgreSQL 跨库对比需要先把 backup 还原到一个临时库，再用 `dblink` 或两次
连接对比：

```bash
# 1. 创建临时库（专用于 verify，不影响生产）
psql -h <host> -U <admin-user> -c "CREATE DATABASE xirang_verify_<your-TS>;"

# 2. 还原 backup 到临时库
pg_restore -h <host> -U <admin-user> -d xirang_verify_<your-TS> \
           -O -x /backup/xirang-pre-utc-cutover-<your-TS>.pgdump

# 3. 在生产库执行跨库对比（需要安装 dblink 扩展）
psql -h <host> -U <user> -d <prod-dbname> <<'SQL'
CREATE EXTENSION IF NOT EXISTS dblink;
SELECT
  EXTRACT(EPOCH FROM (old_ts - new_ts)) / 3600.0 AS diff_hours
FROM (
  SELECT
    (SELECT created_at FROM users ORDER BY id DESC LIMIT 1) AS new_ts,
    (SELECT created_at FROM dblink('dbname=xirang_verify_<your-TS>',
                                   'SELECT created_at FROM users ORDER BY id DESC LIMIT 1')
                       AS t(created_at TIMESTAMP) LIMIT 1) AS old_ts
) q;
-- 期望: 8.0
SQL

# 4. 验证完毕，销毁临时库
psql -h <host> -U <admin-user> -c "DROP DATABASE xirang_verify_<your-TS>;"
```

如果环境没有 dblink 权限，最简退路：分别 `psql` 进 prod 与临时库，
`SELECT created_at FROM users ORDER BY id DESC LIMIT 1`，肉眼对比两个值差
应是 8 小时。

### Verify 失败决策树

| 现象 | 处置 |
|---|---|
| `version != 50` 或 `dirty=true` | 直接 Rollback Path A（恢复备份） |
| 时间差 ≠ 8 小时（例如 16 小时） | 平移已重复执行（双倍偏移），Rollback Path A 立即恢复 |
| 时间差 = 0 | 平移未生效（可能数据已被新写入覆盖），Rollback Path A |
| 时间差 = 8 小时 + 启动后第一次写入是 UTC | Verify 通过，正常启动服务 |

### 启动后第一次写入验证

短暂启动后端（**只让它跑一次 task scheduler 写入**就停），然后查最新一条 task_run：

```sql
SELECT id, created_at, started_at FROM task_runs ORDER BY id DESC LIMIT 1;
```

`created_at` 应该贴近 `datetime('now')` (SQLite) / `now() AT TIME ZONE 'UTC'` (PG)
即当前 UTC 时刻。如果它写成本地时间（比 UTC now 多 8h），说明代码版本没对齐——见前置检查 #3。

通过后即可 `make prod-up` 正常服务流量。

---

## 5. Rollback

> 仅在 Migrate 或 Verify 阶段发现问题时使用。**永远先恢复备份再考虑跑 down.sql**——
> backup 路径更可靠，down.sql 只在备份不可用时作为后备。

### Path A: 恢复备份（首选）

替换 `<your-TS>` 为第 1 节命名备份时记录的时间戳。

```bash
# SQLite
make prod-down
sudo cp /backup/xirang-pre-utc-cutover-<your-TS>.db.bak /data/xirang.db
# 切回上一个不含 NowFunc=UTC / DSN _loc=UTC 的镜像 tag
docker compose pull <old-tag>
make prod-up

# PostgreSQL
make prod-down
pg_restore -c -d <dbname> /backup/xirang-pre-utc-cutover-<your-TS>.pgdump
docker compose pull <old-tag>
make prod-up
```

### Path B: 跑 down.sql（当备份损坏时的后备）

> 假设迁移已成功跑到 version=50；如果 dirty=1，先手动把 schema_migrations 标记为 50 clean，
> 再 force 跑 down：

```bash
# golang-migrate CLI 安装：https://github.com/golang-migrate/migrate/tree/master/cmd/migrate
# SQLite
migrate -path backend/internal/database/migrations/sqlite \
        -database "sqlite3:///data/xirang.db" down 1

# PostgreSQL
migrate -path backend/internal/database/migrations/postgres \
        -database "pgx5://user:pass@host/dbname?sslmode=disable" down 1
```

down.sql 会把所有时间戳加回 +8h。完成后切回上一个老镜像 tag。

> **绝对不要**在 down 之后又重新 up——会双倍偏移。要么备份恢复，要么 down.sql 一次。

---

## 已知影响范围

迁移完成后会出现以下短期可见的"异常"（都不是 bug，是 UTC 切换的固有现象）：

1. **SLA 报告 / 趋势图的时间窗口前后 8h 内显示错位**
   - 旧数据已被减 8h，时刻轴上向左平移
   - 新数据是 UTC 写入
   - 前端按浏览器本地时区呈现，所以视觉上的"今天 14:00"会涵盖跨越平移点的两段
   - 等待 24-72h 后，旧数据从大多数滚动窗口移出，趋势图回归正常
2. **审计日志 `created_at` 字段**：迁移点之前与之后的查询条件 `created_at >= 'YYYY-MM-DD'`
   语义稍变（从 Local 比较变 UTC 比较）。如果有自动化分析脚本依赖 audit log 时间，需要
   review 它们是否假设 Local 时区
3. **node maintenance window** (`maintenance_start`/`maintenance_end`)：维护窗口被一并平移
   8h，所以仍然指向同一绝对时刻，前端按本地时区呈现，所以维护窗口在 UI 上**仍然显示原来的时刻**——
   无需修改任何配置
4. **Cron 任务的下一次执行时间**：`tasks.next_run_at` 也被 -8h，但 cron scheduler 重新计算
   时仍按 cron 表达式 + UTC 解析（如果 cron 表达式是 `0 8 * * *`，UTC 模式下表示 UTC 8:00 即
   北京 16:00）。**这是 schedule 行为变更**——如果业务依赖 cron 表达式按本地时间执行，
   需要把 cron 表达式整体后移 8h（或在迁移前评估每条 cron 是否需要调整）

> 推荐做法：迁移前先把所有 `tasks.cron_spec` / `policies.cron_spec` 列出来，标记哪些是
> "希望按本地时间触发" 的（如"每天早 8 点跑备份"），迁移后改写为 UTC 等价表达式
> （`0 0 * * *` → 本地 UTC+8 的 8:00）。

---

## 容器 TZ 与 DB UTC 的关系

**不需要**修改 docker-compose 中的 `TZ=Asia/Shanghai`。容器 TZ 仅影响：

- 容器内 shell `date` 输出
- Go 的 `log.Print` 默认输出
- 文件 mtime（备份目录命名等）

而 GORM 写入 / 读取 timestamp 列已经强制 UTC（`NowFunc` + `_loc=UTC` / `timezone=UTC`）。
两者解耦：运维看日志依旧是本地时间，DB 内部全部 UTC。

---

## 测试参考

- 单元测试：`backend/internal/database/utc_cutover_test.go`
  - `TestNowFuncReturnsUTC`：验证 GORM 自动写入的 `CreatedAt` 是 UTC
  - `TestSQLiteLocUTCRoundTrip`：验证 `_loc=UTC` 后写入读出无时区漂移
  - `TestUTCCutoverSQLEquivalence`：模拟"老 Local 数据 → 跑 -8h SQL → 读出"，断言绝对时刻不变
  - `TestBuildPostgresDSN`：URL/keyword/已含 timezone 三种 DSN 输入
- 本地 dry-run：用临时 SQLite + 30 天历史种子，跑 up.sql + down.sql 各一次，验证回到原值
