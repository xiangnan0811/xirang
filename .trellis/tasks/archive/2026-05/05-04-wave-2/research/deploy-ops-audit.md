# Wave 2 — 迁移 / 部署 / 运维 实读审查

- 范围：仓库 `/Users/weibo/Code/xirang/` migration 000050、Dockerfile、entrypoint、scripts、Makefile、prod compose、env example、bundle 预算、metrics endpoint
- 实读 18 个文件，含 4 段 SQL（up/down × sqlite/postgres）+ supercronic CVE 在 NVD 上线核验
- 已剔除 Wave 0/1 加固项；不重复 echo

---

## ⚠️ 高优先级 — 设计/正确性风险

### F-1 [✅] migration 000050 整体未事务包裹，部分失败留下 dirty + 时间偏移翻倍风险
- **文件:行**：`backend/internal/database/migrations/sqlite/000050_utc_cutover.up.sql:46-158`、对应 postgres 1-119、`backend/internal/database/migrator.go:79-87`
- **实读片段**：
  ```sql
  -- 文件全 113 条 UPDATE（sqlite 版）从 users 一路扫到 anomaly_events，
  -- 整个文件没有 BEGIN/COMMIT。golang-migrate 默认会用单事务执行整个 .sql,
  -- 但 sqlite3 driver 里这意味着 IMMEDIATE 锁直至最后一条 UPDATE 完成。
  ```
  ```go
  // migrator.go
  if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
      return fmt.Errorf("执行迁移失败: %w", err)
  }
  version, dirty, _ := m.Version()
  log.Printf("数据库迁移完成，当前版本: %d, dirty: %v", version, dirty)
  ```
- **问题**：
  1. golang-migrate 对 sqlite3 / pgx 默认逐 statement 执行，但 SQLite 驱动会"自动事务包裹"——**仅当上层未显式 BEGIN**。验证：当前文件无显式 BEGIN，依赖驱动隐式事务行为，对单一长 .sql 是事务一致的；但如果 SQL 文件含 113 条 UPDATE，任一中段失败（磁盘满 / FK 漂移 / 一个未来 ALTER 把列删了）会让整个 .sql 回滚但 `schema_migrations.dirty=1` 仍被置位。
  2. `migrator.go:83` 读 `dirty` 但只 log，**未执行任何修复或拒绝启动**。生产首发时若 000050 首跑失败，下次启动会因 `dirty=1` 直接 `Up()` 报 "database is dirty"，运维必须手工 `migrate force` 才能继续。
  3. 注释明确"不可幂等"——但如果 dirty 后操作员手工 `force` 跳过 000050，新写入即按 UTC，但历史数据仍是 +8h；产生**永久双时区污染**，下游聚合（reports.period_start/period_end、dashboard time_range）会错位。
- **影响**：单次 cutover 失败 → 永久数据偏移 / 启动死锁，无任何自动救护
- **正确修复方向**：
  - 在 SQL 顶部加显式 `BEGIN; ... COMMIT;`（PostgreSQL 自动事务，SQLite 必须）
  - migrator.go 在读 `dirty=true` 时 panic 或显式拒绝启动，强制人工介入
  - 文档化 dirty=1 后的 force-rollback 标准流程（先把所有时间列 +8h 还原再 force）
- **工作量**：M（SQL 改 + migrator 加 dirty 拒启动 + runbook）

### F-2 [⚠️] migration 000050 假设"集群只有一台后端在写"——未文档化锁机制
- **文件:行**：`sqlite/000050_utc_cutover.up.sql:6-9`
- **实读片段**：
  ```sql
  -- WARNING: 本迁移**不可幂等执行** —— 重复运行会让时间偏移翻倍。
  -- 必须在 maintenance window 执行；不能 in-flight 写入时迁移（否则 race condition）。
  -- 详细 runbook 见 docs/migration-utc-cutover.md
  ```
- **问题**：注释说"不能 in-flight 写入"，但应用启动时自动 `RunMigrations`，没有任何前置检查阻止后端在 cutover 进行中接受请求。如果 docker compose 多副本（虽然当前模型偏单实例），任意 1 个副本启动时 race 触发 000050，其他副本可能正在写入；新写入用旧本地时间字符串 → cutover 后这些 row 被减 8h 而其实它们已经是 UTC。
- **影响**：理论存在 race window；当前单容器部署影响小但若运维水平扩展会爆雷
- **正确修复方向**：在 000050 前加单条事务里的 `SELECT ... FOR UPDATE` (postgres) / `BEGIN IMMEDIATE` (sqlite) + 文档明确"必须先 docker compose down 再 up"，或加 advisory lock
- **工作量**：S（runbook 强化为 hard requirement + 启动日志大字提示）

### F-3 [✅] 后续新增 migration 缺少"Wave 2 后只准 UTC 写"的强制守卫
- **文件:行**：`backend/internal/database/migrator.go:106-134`、`migrations/sqlite/000041 ~ 000049/*.up.sql`
- **实读片段**：现 `preMigrationFixups` 只挂了 `fixupLegacyPolicyBwlimit`；000050 之后的 migration 完全靠 reviewer 记得"不要 INSERT 用 datetime('now')、用 UTC"。
- **问题**：000050 之后任何新 migration 若 INSERT/UPDATE 时间默认值用 `datetime('now', 'localtime')`、PG `NOW() AT TIME ZONE 'Asia/Shanghai'` 或裸 `CURRENT_TIMESTAMP`（在 SQLite 中是 UTC，在 Local-tz 容器里 PG 是 Local）会立即破坏 UTC 不变量。当前没有 lint / pre-commit 检测此模式。
- **影响**：长期回归风险，下次有人手写 SQL 默认值会悄无声息引入新偏移
- **正确修复方向**：加一个简单 grep 在 CI（`backend/internal/database/migrations/**/*.sql`）阻断 `localtime`、`AT TIME ZONE`、`CURRENT_TIMESTAMP` 单独使用（除了首次 baseline），或写一个 `*_test.go` 把所有 .sql 文件 string-scan
- **工作量**：S（10 行 CI 脚本 + 1 个 test）

### F-4 [✅] `/metrics` 端点完全公开（无鉴权 / 无 IP 白名单）
- **文件:行**：`backend/internal/api/router.go:325`
- **实读片段**：
  ```go
  router.GET("/metrics", gin.WrapH(promhttp.Handler()))
  ```
  注意：挂在 `router` 而非 `secured` 组上，且没有任何 middleware。
- **问题**：Prometheus default registry 包含 Go runtime + 自定义 `http_requests_total{method, path, status}`、`http_request_duration_seconds`。`path` 维度是 `c.FullPath()`，会暴露**所有 secured 路由名**（`/api/v1/admin/metrics/rollup-status`、`/api/v1/system/backup-db` 等管理端 path、节点 ID 路径模板等），泄露应用拓扑/管理面给任意外部抓取者；同时 `http_requests_in_flight`、`http_response_size_bytes` 可被监控者推断速率/特征。
- **影响**：信息泄露（路由清单 + 流量画像），且无法限速 → DDoS 放大目标
- **正确修复方向**：把 `/metrics` 移到 `secured` 或单独挂 `middleware.RequireRole("admin")`；或加 `IP allowlist` middleware（仅 127.0.0.1 / Docker 网段）
- **工作量**：S（移到 admin 组 + 文档加 Prom scraper 配 token）

---

## ⚠️ 中优先级 — 部署/运维健壮性

### F-5 [✅] `.env.deploy` 默认 `JWT_SECRET=CHANGE-ME-use-a-strong-jwt-secret`：恰好在弱密钥黑名单里 → 启动报错
- **文件:行**：`.env.deploy:25-28`、`backend/internal/config/config.go:216-244`
- **实读片段**：
  ```ini
  ADMIN_INITIAL_PASSWORD=CHANGE-ME-use-a-strong-password
  JWT_SECRET=CHANGE-ME-use-a-strong-jwt-secret
  DATA_ENCRYPTION_KEY=CHANGE-ME-use-a-strong-encryption-key
  ```
  config.go 的 weakSet 含 `"CHANGE-ME-use-a-strong-jwt-secret"` 与 `"CHANGE-ME-use-a-strong-encryption-key"`；ADMIN 密码缺数字会被 `ValidatePasswordStrength` 拒绝（必须含大写+小写+数字+特殊符号）。
- **问题**：这是**正向防御**——三个值留原默认会立即 panic；但用户首次跑 `make prod-up` 会在 logs 里看到 fatal，然后才会去改 .env，体验上"一键启动"承诺破灭。
- **影响**：第一次部署的用户必摔倒一次；不是 bug 但首跑体验需运维手册兜底
- **正确修复方向**：`make prod-up` 前加一个 prerequisite 检查 shell（grep `.env` 是否还含 `CHANGE-ME-` 三字串），失败明确提示
- **工作量**：S（Makefile 加 5 行 grep 校验）

### F-6 [✅] `make prod-up` 缺 `.env` 时仅依赖 docker compose 抛 warning，不会优雅终止
- **文件:行**：`Makefile:37-38`
- **实读片段**：
  ```make
  prod-up:
  	docker compose -f docker-compose.prod.yml up -d
  ```
- **问题**：`docker-compose.prod.yml:5-6` 用 `env_file: - .env`；docker compose 在 .env 不存在时会输出 `WARN[0000] The "JWT_SECRET" variable is not set` 然后启动容器，容器内 backend panic 退出循环（restart unless-stopped），表象是"容器一直崩溃"。新手很可能不知道是 .env 缺失。
- **影响**：误诊路径长（"明明 image pull 成功了为什么 healthcheck 一直红"）
- **正确修复方向**：Makefile 在 prod-up 前 `@test -f .env || (echo "❌ .env 不存在，请先 cp .env.deploy .env 并修改"; exit 1)`
- **工作量**：S（1 行 Makefile）

### F-7 [⚠️] `scripts/backup-db.sh` 仅 SQLite 路径写 sha256，PostgreSQL 路径不校验
- **文件:行**：`scripts/backup-db.sh:23-27, 44-46`
- **实读片段**：
  ```bash
  sqlite3 "${sqlite_path}" ".timeout 5000" ".backup '${backup_file}'"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "${backup_file}" > "${backup_file}.sha256"
  ...
  pg_dump "${dsn}" --format=custom --file "${backup_file}" --no-owner --no-privileges
  echo "✅ PostgreSQL 备份完成：${backup_file}"
  exit 0
  ```
- **问题**：PG 备份后无 sha256 / 无大小校验 / 无 `pg_restore --list` 干跑验证。容器层 retention cron（`xirang-backup.cron:4`）按 `-mtime +30 -delete` 删除——如果 pg_dump 中途失败（DSN 临时不可达、磁盘短暂满），磁盘上会留下 0 字节 .dump 文件，30 天后被静默删，但当事人以为有完整备份。
- **影响**：PG 用户备份**沉默失败**风险；恢复时才发现文件损坏
- **正确修复方向**：PG 路径加 `pg_dump | tee >(sha256sum > $f.sha256)`；备份后 `pg_restore --list "$file" >/dev/null` 验证可读；非 0 大小 + 非 0 退出码联合检查
- **工作量**：S

### F-8 [✅] `scripts/restore-db.sh` 不校验 .sha256
- **文件:行**：`scripts/restore-db.sh:36-49`
- **实读片段**：
  ```bash
  if [[ -f "${sqlite_path}" ]]; then
    rollback_file="${sqlite_path}.before-restore.$(date +%Y%m%d-%H%M%S).bak"
    cp "${sqlite_path}" "${rollback_file}"
    echo "🔁 已备份当前 SQLite 文件：${rollback_file}"
  fi
  cp "${backup_file}" "${sqlite_path}"
  ```
- **问题**：backup-db.sh 写了 .sha256 兄弟文件，但 restore-db.sh 完全不读、不校验。备份在传输/存储中损坏（rsync 中断、磁盘 bit-rot、人为 truncate）→ 恢复出"成功"但拿到一个坏库，等到首次 INSERT 才报 `database disk image is malformed`。
- **影响**：恢复路径无完整性保证，是真正的"灾难恢复演练"漏点
- **正确修复方向**：restore-db.sh 在 `cp` 前若 `${backup_file}.sha256` 存在则 `sha256sum -c` 校验失败立即 exit；--no-verify 选项允许跳过
- **工作量**：S

### F-9 [⚠️] `xirang-backup.cron` 容量保护只看时间（30 天）不看磁盘水位
- **文件:行**：`deploy/allinone/xirang-backup.cron:3-4`
- **实读片段**：
  ```cron
  0 2 * * * /usr/local/bin/backup-db.sh /backup/db
  30 2 * * * find /backup/db -maxdepth 1 \( -name "*.db" -o -name "*.dump" -o -name "*.sha256" \) -mtime +30 -delete
  ```
  挂载点 `./backups:/backup`（compose:11）。
- **问题**：30 天 retention 假设"30 天内所有备份文件总和 < 卷容量"。某些场景：用户 db 涨到 5GB / day → 150GB 占用；宿主 `./backups` 目录满后 cron 当晚 `sqlite3 .backup` 写半截失败 → 02:30 cron find 仍会成功删旧的（包括今天那个不完整文件），形成 "fail-then-purge" 的不可恢复链路。
- **影响**：磁盘满后**当晚备份+原始 30 天前备份双双不可用**
- **正确修复方向**：cron 增加 `df --output=avail /backup | tail -1` < 阈值时跳过备份并 alert；或保留至少 N 份最新（不论时间）
- **工作量**：S（cron 改成 wrapper script）

### F-10 [✅] `entrypoint.sh` 信号转发不完整：`trap cleanup EXIT` 在 wait loop 中不触发
- **文件:行**：`deploy/allinone/entrypoint.sh:70-114`
- **实读片段**：
  ```sh
  trap cleanup EXIT
  trap 'exit 143' TERM INT
  ...
  while :; do
    if ! is_running "${XIRANG_PID}"; then ...
    if ! is_running "${NGINX_PID}"; then
      wait "${NGINX_PID}"
      exit $?
    fi
    sleep 1
  done
  ```
- **问题**：`sleep 1` 是阻塞 syscall，shell 接到 SIGTERM 时要等 sleep 返回才能跑 trap。docker stop 默认 10s grace，绝大多数场景没问题，但极端慢启动时（first-run go mod download）容器可能被 SIGKILL。另外 `trap 'exit 143' TERM INT` 直接 exit 143 而不是先 cleanup（EXIT 触发但已经被 SIGTERM 路径绕过 cleanup 函数中 `wait`），nginx/backend/cron 子进程依赖容器 namespace 销毁而非显式 SIGTERM。
- **影响**：优雅停机不彻底；ssh 会话 / 长任务可能丢失最后日志写入
- **正确修复方向**：`trap 'cleanup; exit 143' TERM INT`，并把 sleep 替换为 `wait -n` 或 inotify
- **工作量**：S

### F-11 [⚠️] `entrypoint.sh` 启动期 env 校验缺失
- **文件:行**：`deploy/allinone/entrypoint.sh:1-50`
- **实读片段**：脚本顶部仅 `set -eu`，未对 `JWT_SECRET / ADMIN_INITIAL_PASSWORD / DATA_ENCRYPTION_KEY` 做存在性预检即 `start_backend`。
- **问题**：依赖 backend 二进制内部校验（config.go:176-184 + bootstrap.go:30-36）panic。这没问题但日志路径绕：用户先看到容器 restart loop → 进 `docker logs xirang` → 才看到 fatal。如果 entrypoint.sh 在 start_backend 前 grep 这三个变量不为空且不在 weakSet，可以在 entrypoint 输出更具操作性的提示（"请编辑 .env"）。
- **影响**：UX，不影响安全
- **正确修复方向**：entrypoint.sh 在 start_backend 前简单 `[ -n "${JWT_SECRET:-}" ] || { echo "==> JWT_SECRET 未设置..."; exit 78; }`
- **工作量**：S

---

## ❓ 较低优先级 / 信息项

### F-12 [⚠️] dev `docker-compose.yml` 用 `golang:1.26.1`，与 prod Dockerfile / go.mod (`1.26.2`) 漂移
- **文件:行**：`docker-compose.yml:3`、`backend/go.mod:3`、`deploy/allinone/Dockerfile:10,29`
- **实读片段**：dev compose `image: golang:1.26.1`；prod 用 `golang:1.26.2-alpine`；go.mod `go 1.26.2`
- **问题**：dev image 1.26.1 含 Wave 2 自己 Dockerfile 注释里说要回避的 CVE-2026-32280/32282/33810。开发者本地编译/测试用的是有漏洞的工具链。go.mod 的 `go 1.26.2` 只是最低版本声明，1.26.1 也能编。
- **影响**：开发者机器（容器内）暴露 stdlib HIGH/MEDIUM CVE；不影响 prod 镜像
- **正确修复方向**：把 dev compose 升到 `golang:1.26.2`，并在 docs 里写"两个 Dockerfile 的 Go 版本必须同步"
- **工作量**：S

### F-13 [✅] CVE 注释里的 "HIGH CVE" 略夸大；CVE-2026-32282 是 MEDIUM (6.4)
- **文件:行**：`deploy/allinone/Dockerfile:26-28`
- **NVD 实查**：
  - CVE-2026-32280：HIGH (7.5) crypto/x509 chain DoS，fix 1.25.9 / 1.26.2 ✓
  - CVE-2026-32282：**MEDIUM (6.4)** os.Root.Chmod symlink TOCTOU (Linux only, AC:H, PR:H), fix 1.25.9 / 1.26.2
  - CVE-2026-33810：HIGH (8.2) DNS constraint case-mismatch wildcard bypass, fix only 1.26.2
- **问题**：注释说三个全是 HIGH；实际一个是 MEDIUM。修复版本号正确。重编 supercronic 的决策正确（v0.2.44 用 1.26.1 编译会带前两个 CVE）。
- **影响**：注释夸大，无功能影响
- **正确修复方向**：注释微调："HIGH/MEDIUM CVE"
- **工作量**：S

### F-14 [⚠️] `scripts/smoke-e2e.sh` 未覆盖 v0.19.2 关键新功能（虚拟化日志、UTC 时间显示、SFTP RealPath）
- **文件:行**：`scripts/smoke-e2e.sh` 全文 454 行
- **实读片段**：grep `react-virtual|UTC|RealPath` 在 smoke-e2e.sh 全无命中。覆盖范围：login / node / sshkey / policy / task / silence / SLO / dashboard / escalation / anomaly。
- **问题**：v0.19.2 的 logs-viewer 虚拟化（`web/src/pages/logs/logs-viewer.tsx:3` 使用 `useVirtualizer`）、UTC cutover 后端时间响应、SFTP RealPath 修复都属于"用户不报但回归会爆"类型，smoke-e2e 仅打 API 不验证响应里的时间格式（如 `created_at` 是否带 Z 后缀 / Location 是否 UTC）。
- **影响**：000050 cutover 后新写入的时间格式发生变化，前端如果有"假定 +8h"的解析逻辑会显示错时间，smoke 不会发现
- **正确修复方向**：smoke 加 `created_at | grep -E "Z$|\\+00:00"` 断言；前端可以加 `tests/integration/logs-virtualization.spec.ts`
- **工作量**：S（smoke 加 1 段）

### F-15 [✅] `bundle-budget.mjs` 546 KiB 预算 + 仅监控 main JS：lazy chunk 无预算
- **文件:行**：`web/scripts/check-bundle-budget.mjs:31-53`
- **实读片段**：
  ```js
  const indexFiles = candidateFiles.filter((file) => file.startsWith('index-'));
  const pool = indexFiles.length > 0 ? indexFiles : candidateFiles;
  // ...选 pool 内最大那个文件
  ```
- **问题**：只看 `index-*.js` 主入口最大文件。React Router lazy-loaded pages 各自打成 chunk（如 `pages-logs-*.js` 含 `react-virtual` + `xterm`），chunk 不在 budget 内。下一波若有人在 `Logs` 页面引入新大依赖 → 打到 lazy chunk 不报警，但用户首次进入 Logs 页面感受加载慢。
- **影响**：性能预算盲区；对"主页打开慢"无效，对"二级页加载慢"无防护
- **正确修复方向**：脚本加 "总 JS 体积" 预算（所有 .js 求和），或对 `pages-*` chunks 单独设上限
- **工作量**：S

### F-16 [⚠️] `docker-compose.prod.yml` 未声明 named volume，纯 bind mount → 卷生命周期外置但无统一管理
- **文件:行**：`docker-compose.prod.yml:10-13`
- **实读片段**：
  ```yaml
  volumes:
    - ./data:/data
    - ./backups:/backup
  ```
- **问题**：`./data` 与 `./backups` 是 host bind mount。Pros: 用户 ls 可见；Cons: docker compose down -v 不会清理它们（用户可能误以为彻底卸载）；备份保留 30 天 + 数据库可能 GB 级 → `./backups` 目录无独立挂盘的部署里和应用 root fs 共享 inode/磁盘。`/etc/nginx/certs` 注释默认禁用，证书路径若用户改成挂某 acme 共享卷会改这里。
- **影响**：磁盘隔离差；备份目录撑爆主盘 → 应用一起死
- **正确修复方向**：docs 里强调"建议把 ./backups 挂到独立块设备"；或 compose 里加 `driver_opts: o: bind,size=...`
- **工作量**：S（仅文档）

---

## 总结

- **必须修**（F-1 / F-2 / F-3 / F-4）：000050 事务 + dirty 拒启动 + 后续 migration 时间默认值 lint + `/metrics` 鉴权
- **应该修**（F-6 / F-7 / F-8 / F-10 / F-14）：Makefile prereq、PG 备份/恢复 sha256、entrypoint 信号、smoke 断言时间格式
- **可以观望**（F-5 / F-9 / F-11 / F-12 / F-13 / F-15 / F-16）：UX、文档、轻度版本漂移
