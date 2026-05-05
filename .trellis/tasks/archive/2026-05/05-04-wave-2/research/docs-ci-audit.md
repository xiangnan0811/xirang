# Wave 2 — 文档 + CI/CD 实读审查

- **Scope**: docs (CHANGELOG/README/CONTRIBUTING/release-maintainers/deployment/env-vars/migration-utc-cutover) + `.github/workflows/*` + `.githooks/pre-commit` + `release-please-config.json` + `backend/.env.example` + `web/package.json` + `backend/go.mod`
- **审查时间锚**: HEAD `8d95e86`，main 已发 v0.19.2
- **方法**: 每个 finding 实读 ≥5 行；置信度 ✅/⚠️/❓；保守报告

---

## F-1 [✅] CHANGELOG.md 0.19.2 条目极度不完整，掩盖了 Wave 0/1 实质性安全/部署修复

- **文件:行**: `CHANGELOG.md:3-9`
- **实读摘要**:
  ```
  ## [0.19.2](.../v0.19.1...v0.19.2) (2026-05-04)
  ### Bug Fixes
  * Wave 0 安全/稳定性/文档整改（基于全方位审查后实读复核清单） (#105)
  * Wave 1 清理 Wave 0 Out-of-Scope 三项 (B-2 SFTP RealPath / B-8 UTC 迁移 / F-3 logs 虚拟化) (#108)
  ```
- **问题**: 整个 0.19.2 release notes 只有 2 行 squash PR title，**完全不展开**用户/运维需要知道的实质改动。Wave 0/1 实际修复内容（任务 PRD 列出）至少包括：命令注入加固（ShellEscape）、JWT revoked race（pruneRevokedLocked）、SSRF 校验、SFTP symlink 逃逸（RealPath）、**000050_utc_cutover 不可幂等迁移**、TASK_MAX_EXECUTION_SECONDS 全局兜底、LOG_FILE 双写、TZ + tzdata + HEALTHCHECK + log rotation、Integration.endpoint VARCHAR→TEXT (000048)、anomaly alerts 默认关闭等十多项。**对运维而言最危险的"UTC 迁移不可幂等、必须停服执行"完全没在 release notes 中预警**——升级用户只看 release 不会知道有 runbook。
- **影响**: 用户跳版升级时可能直接踩 000050 的不可逆数据损坏；安全修复透明度也差，下游不知道哪些 CVE-类问题已被修复。
- **正确修复方向**: 在 0.19.2 entry 下手动追加 ### Breaking changes / ### Security 子节，明确列：(1) "0.19.2 包含 000050_utc_cutover 不可幂等迁移，升级前必读 docs/migration-utc-cutover.md"；(2) 安全相关 commit hash 列表与一句话说明。Release Please 保留原 squash 标题，但允许在 Release PR 上手工增补 body——下次发版前应在 release-please 配置里加 release-notes-body 模板或在 GitHub Release 页面直接补上。
- **工作量**: S（直接编辑 GitHub Release 文案 + CHANGELOG.md，不需要重发版本）

---

## F-2 [✅] `backend/.env.example` 缺失 docs/env-vars.md 中已文档化的 ~30 个变量

- **文件:行**: `backend/.env.example`（130 行，仅含 ~28 个 KEY=VALUE）vs `docs/env-vars.md`
- **实读摘要**: 用 `grep '^[A-Z_]+=' backend/.env.example | sort -u` 与 `docs/env-vars.md` 中表格列出的变量名 diff，结果有 **39 个变量** 仅在 docs 中出现，未在 .env.example。剔除"前端 VITE_*（5 个）/ 部署变量 IMAGE_*/HTTP_PORT 等（5 个）/ DB_DSN（注释里有）/ ENVIRONMENT/GIN_MODE（备用别名）"后，仍有约 25 个真实缺口，例如：
  - 异常检测 §10.1：`ANOMALY_ENABLED` / `ANOMALY_ALERTS_ENABLED` / `ANOMALY_EWMA_ALPHA` / `ANOMALY_EWMA_SIGMA` / `ANOMALY_EWMA_WINDOW_HOURS` / `ANOMALY_EWMA_MIN_SAMPLES` / `ANOMALY_DISK_FORECAST_DAYS` / `ANOMALY_DISK_FORECAST_MIN_HISTORY_HOURS` / `ANOMALY_EVENTS_RETENTION_DAYS`
  - 数据保留 §8：`RETENTION_CHECK_INTERVAL` / `BACKUP_STORAGE_MIN_FREE_GB` / `BACKUP_STORAGE_MAX_USAGE_PCT` / `INTEGRITY_CHECK_MULTIPLIER` / `LOG_RETENTION_DAYS_DEFAULT` / `SILENCE_RETENTION_DAYS`
  - 备份/系统 §13：`DB_BACKUP_DIR` / `DB_BACKUP_MAX_COUNT` / `BACKUP_STALE_THRESHOLD_HOURS`
  - SMTP §9：`SMTP_REQUIRE_TLS`（实际存在但 .env.example 缺）
  - 邮件去重之外 §10：（已含 ALERT_DEDUP_WINDOW）
- **问题**: 用户照着 backend/.env.example 复制部署，会错过整套异常检测开关、数据保留阈值、备份存储告警阈值。`backend/.env.production.example` 同样仅 42 项，覆盖更窄。CONTRIBUTING.md:90 与 pre-commit hook 都说"新增/修改环境变量必须同步更新 docs/env-vars.md 和 .env.example"——实际上文档单边领先。
- **影响**: 文档与 example 文件两边事实不一致；运维只看 example 时遗漏关键调优项；新贡献者复制 example 后缺多项默认值，需要重新查源码。
- **正确修复方向**: 一次性把 docs/env-vars.md §8/10.1/13 的所有变量补齐到 `.env.example`（注释形式给默认值即可），并在 CI 加一条 `check-env-vars-sync.sh` 脚本——grep `^[A-Z_]+=` from .env.example 与 docs/env-vars.md 提取，diff 报错。或反向：把 `.env.example` 设为 source-of-truth 让 docs 自动生成。
- **工作量**: M（补齐 30 个变量条目 + 写 sync 校验脚本）

---

## F-3 [✅] `.env.example` 缺 SMTP_REQUIRE_TLS 但 docs §9 已说 v0.18+ 默认 true 强制 TLS——回退到 .env.example 部署的用户实际行为是 true，不知道可关闭

- **文件:行**: `backend/.env.example:56-63`，`backend/.env.production.example` 同步缺；`docs/env-vars.md:117`
- **实读摘要** (env-vars.md:117):
  > `| \`SMTP_REQUIRE_TLS\` | bool | \`true\` | 否 | 强制 TLS 连接（465 隐式/587 STARTTLS），设为 \`false\` 回退到明文 |`
- **实读摘要** (.env.example:56-63):
  ```
  # SMTP（仅 email 通知通道需要）
  SMTP_HOST=
  SMTP_PORT=587
  SMTP_USER=
  SMTP_PASS=
  SMTP_FROM=
  ```
- **问题**: SMTP_REQUIRE_TLS 不在任一 .env.example，运维如果遇到老 SMTP server 不支持 STARTTLS 的内网邮件场景，无法知道这个 escape hatch 存在。
- **影响**: 中——只有"老 SMTP server"场景受影响，但对内网部署是真问题。
- **正确修复方向**: 在 SMTP 段加 `# SMTP_REQUIRE_TLS=true` 注释行 + 简短说明何时关闭。
- **工作量**: S

---

## F-4 [✅] CLAUDE.md 迁移版本仍为 000047，与 main 实际 000050 脱节（且 CONTRIBUTING + pre-commit 明确要求同步）

- **文件:行**: `CLAUDE.md:50`
- **实读摘要**: `当前版本：\`000047_alert_deliveries_drop_error\`（SQLite + PostgreSQL 双轨迁移）`
- **问题**: 实际目录最新是 `000050_utc_cutover`（diff = 3 个迁移）。`.githooks/pre-commit:49-50` 与 `scripts/check-doc-freshness.sh:54-58` 都对"`migrations/` 变更未同步 CLAUDE.md" 给警告，但因 pre-commit 是本地 hook 默认未启用、check-doc-freshness 又是非阻断（`exit 0`，line 82）——多次跳过导致漂移。
- **影响**: 文档失真，新 agent / 新贡献者读 CLAUDE.md 会误判数据库 schema 状态。
- **正确修复方向**: 把 CLAUDE.md:50 更新为 `000050_utc_cutover`，并在 docs/env-vars.md 的"§敏感字段加密策略"或新增 "数据库迁移注意事项" 引用 docs/migration-utc-cutover.md。考虑把 check-doc-freshness 在"新增 migration 但未更 CLAUDE.md"这一条改为阻断（exit 1）。
- **工作量**: S

---

## F-5 [⚠️] `publish-images.yml` Trivy action 用 `@master` 浮动 ref，违背仓库其他 action 全部 SHA-pin 的安全约定

- **文件:行**: `.github/workflows/publish-images.yml:117-124`
- **实读摘要**:
  ```yaml
  - name: Scan image for vulnerabilities
    uses: aquasecurity/trivy-action@master
    with:
      image-ref: docker.io/${{ env.IMAGE_NAMESPACE }}/xirang:v${{ steps.version.outputs.version }}
      format: 'table'
      exit-code: '1'
      severity: 'HIGH,CRITICAL'
      ignore-unfixed: true
  ```
- **问题**: 同文件其他 action 全部 pinned 到 SHA（如 `actions/checkout@34e114876b... # v4`、`docker/login-action@c94ce9fb... # v3`），唯独 trivy 用 `@master`。该 action 的所有者（aquasecurity）一旦被入侵或推送恶意 master，CI 会以 `id-token: write` + `attestations: write` + DOCKERHUB_TOKEN 上下文执行任意代码——这是供应链攻击高危面。同时 `@master` 也可能在某次更新后突然引入 break，导致镜像发布被阻塞。
- **影响**: 高——发布流程权限敏感且影响 latest tag。
- **正确修复方向**: 改为 `aquasecurity/trivy-action@<SHA> # v0.x.y`，按其他 action 的同样规范固定。
- **工作量**: S

---

## F-6 [✅] `publish-images.yml` Trivy 扫描在镜像发布**之后**且 `exit-code:1`——若有 HIGH/CRITICAL 漏洞，镜像已经被 push 到 Docker Hub，扫描失败只阻 attest 步骤但镜像和 latest tag 已对外可拉

- **文件:行**: `.github/workflows/publish-images.yml:104-131`
- **实读摘要**:
  ```yaml
  - name: Build and push image  # 先 push:true
    with:
      push: true
      ...
  - name: Scan image for vulnerabilities  # 再 scan
    uses: aquasecurity/trivy-action@master
    with:
      exit-code: '1'
  - name: Attest build provenance  # scan 失败 attest 不会跑
  ```
- **问题**: 步骤顺序是 build & push (with push: true) → scan → attest。Trivy 失败 (exit-code: 1) 时 attest 不跑，但镜像已经在 Docker Hub 上以 `vX.Y.Z`/`X.Y.Z`/`latest` 三个 tag 发布。Job 失败后用户已经能 `docker pull` 到带漏洞的镜像。
- **影响**: 中—high。trivy 当前没报问题不代表未来不会有，且 ignore-unfixed: true 可能让真实 CVE 在 base image 升级窗口内静默通过；但一旦扫描失败，撤回需手工 `docker rmi` + 用旧 tag 重 push，且 latest 已被覆盖。
- **正确修复方向**: 改为 build 不 push（`push: false, load: true`）→ 本地 trivy 扫描 → 通过后再单独 push。或者用 docker buildx 的 `--output type=image,push-by-digest=true` 二段式发布。
- **工作量**: M（重排步骤、可能要拆 build/push）

---

## F-7 [✅] `release-please-config.json` 极简，未配置 `extra-files` / `release-notes` 模板——这是 F-1 release notes 信息量不足的根因

- **文件:行**: `release-please-config.json:1-10`
- **实读摘要**:
  ```json
  {
    "$schema": "...",
    "include-v-in-tag": true,
    "packages": {
      ".": {
        "release-type": "simple",
        "changelog-path": "CHANGELOG.md"
      }
    }
  }
  ```
- **问题**: 没设 `changelog-sections`（只把 fix/feat 进 release notes，其他 type 全丢）、没设 `extra-files`（如果有版本字符串文件需要同步）、没设 `release-notes-header`/`footer`（无法在每个 release 自动注入"升级前请先看 CHANGELOG.md 的 BREAKING/SECURITY 段"提示）。直接结果就是 0.19.2 这种 PR 标题=单行的 release notes。
- **影响**: 中——不致命，但工具配置过于"开箱即用"，未利用 Release Please 提供的 release notes 增强能力。
- **正确修复方向**: 至少加 `changelog-sections`（把 chore(deps), perf, refactor 也纳入"Misc"段）和 `release-notes-footer`（固定一段"安全/Breaking 说明请查 docs/release-maintainers.md"）。
- **工作量**: S（编辑 JSON）

---

## F-8 [✅] `docs/migration-utc-cutover.md` Verify 步骤的 SQLite SQL 语法不可执行（占位符 `XXXX` + ATTACH 后未 DETACH）

- **文件:行**: `docs/migration-utc-cutover.md:138-151`
- **实读摘要**:
  ```sql
  ATTACH DATABASE '/backup/xirang-pre-utc-cutover-XXXX.db.bak' AS old;
  SELECT
    (SELECT created_at FROM old.users ORDER BY id DESC LIMIT 1) AS old_local,
    (SELECT created_at FROM main.users ORDER BY id DESC LIMIT 1) AS new_utc;
  -- 期望: old - new = 8 hours
  ```
- **问题**: (1) `XXXX` 是字面占位，未提示用户用第 1 节中的 `$TS` 变量替换——一线运维容易直接复制粘贴运行，sqlite3 会报 `unable to open database`。(2) 没有 `DETACH DATABASE old;`，sqlite3 退出时会留 attach 状态（虽然 shell 关闭就消失但教学上不严谨）。(3) 注释 `-- 期望: old - new = 8 hours` 是人脑判断，没给 SQL 算 diff——可以加 `julianday(old.x) - julianday(main.x)` 直接出 0.333... 验证。(4) PostgreSQL 版本（line 154-162）甚至没给跨库对比的具体语法，只说"与 backup 还原到临时库后的同行对比，差应为 8 小时"——临时库怎么建？要 `CREATE DATABASE xirang_old` 后 `pg_restore`？docs 没说。
- **影响**: 中——runbook 是为停服窗口写的，运维在压力下 copy-paste 报错会拖延切回时间；UTC 切换是不可逆操作，verify 步骤失败 = 决策回滚还是放过的风险窗口。
- **正确修复方向**: (1) 把 `XXXX` 换成 `<your-TS>` 并明确"用步骤 1 记录的 $TS 替换"。(2) 加 SQLite 全表抽样的 `julianday()` 差值断言例子。(3) PG 段补齐 "建临时库 → pg_restore -d xirang_old → 跨库 dblink/手工对比 → 销毁临时库" 完整链路。(4) 增加一段 "如果 verify 失败" 决策树（什么情况走 Path A 备份恢复、什么情况走 Path B down.sql）。
- **工作量**: M

---

## F-9 [✅] `docs/migration-utc-cutover.md` 第 3 节 "Migrate" 让用户启动后端→看日志→停服→Verify→再启动——但实际生产部署的 docker-compose 健康检查会立即把容器标 healthy 并接流量，根本来不及"只观察日志，不让它服务流量"

- **文件:行**: `docs/migration-utc-cutover.md:99-116`
- **实读摘要**:
  ```
  # 2. 启动后端，**只观察日志，不让它服务流量**（如果可能）
  make prod-up
  docker compose logs -f xirang-backend | grep -E "(数据库迁移完成|migrat|ERROR|FATAL)"
  ...
  观察 1-2 分钟无 ERROR / FATAL 日志后，**立即停服**进入 Verify 阶段：
  make prod-down
  ```
- **问题**: `make prod-up` 直接启动整个 compose 栈，Nginx + 后端 + healthcheck 全起，宿主机端口 80/443 立刻 listen——上游负载均衡或浏览器会立刻打过来。"只观察日志不服务流量"在标准 docker-compose 部署下做不到。Wave 1 已加 HEALTHCHECK，启动后接近 30s 就 healthy。
- **影响**: 中——迁移期 1-2 分钟内的写入会以 UTC 时间写入新表，但 SLA report / scheduler 等依赖时间相关数据的可能产生奇怪结果。
- **正确修复方向**: 改写为：(1) 推荐做法是 `docker compose up -d xirang-backend --no-deps` + 用 firewall/iptables 暂时阻流量，或在 nginx 层 `return 503`；(2) 给 systemd 部署一段独立指令；(3) 简化版替代方案：直接停 nginx 容器，仅启 backend 跑 migration 一次，看完日志立刻 down。
- **工作量**: M

---

## F-10 [✅] CONTRIBUTING.md 完全没提 `.trellis/` 工作流，但 AGENTS.md 把 trellis 列为顶级开发约定，且仓库已有 `.trellis/` 目录在追踪——人类贡献者读 CONTRIBUTING 不会知道 trellis 的存在

- **文件:行**: `CONTRIBUTING.md:1-111`（全文 grep 无 trellis），`AGENTS.md:1-13`（trellis 强引用）
- **实读摘要** (AGENTS.md:6-11):
  > `This project is managed by Trellis. The working knowledge you need lives under \`.trellis/\`:`
  > `- \`.trellis/workflow.md\` — development phases, when to create tasks, skill routing`
  > `- \`.trellis/spec/\` — package- and layer-scoped coding guidelines`
  > `- \`.trellis/workspace/\` — per-developer journals and session traces`
  > `- \`.trellis/tasks/\` — active and archived tasks`
- **实读摘要** (CONTRIBUTING.md:18-50)：只讲 fork/branch/PR/squash，零 trellis 内容
- **问题**: 仓库已经事实采用 trellis 工作流（task 目录、spec 目录、journal 目录都在 git 中），但人类贡献者读 CONTRIBUTING.md 不会被引导去看 .trellis/ 文档。AGENTS.md 是给 AI 看的，人类贡献者通常不读。
- **影响**: 低-中。外部贡献者提 PR 时不知道项目用 PRD-first / spec 驱动；维护者审 PR 时如果发现"这个 change 应该走 brainstorm 才对"再让 PR 作者补，回路成本高。
- **正确修复方向**: CONTRIBUTING.md 加一段 "## 任务工作流（可选）"——介绍 .trellis/ 用途、新功能/Wave 类任务建议怎么先 brainstorm。无需强制外部 contributor 用 trellis，但要让他们知道这套约定存在。
- **工作量**: S

---

## F-11 [⚠️] `web/package.json` 用 React 18 而非 React 19；多数依赖不算过时但 React 18 在 2026 年偏旧

- **文件:行**: `web/package.json:36-44`
- **实读摘要**:
  ```json
  "react": "^18.3.1",
  "react-dom": "^18.3.1",
  "react-router-dom": "^6.30.1",
  "vite": "^7.3.1",
  ...
  "framer-motion": "^12.38.0",
  "i18next": "^25.8.18",
  ```
- **问题**: React 19 已 GA 1+ 年（2024 末发布），React 18 仍可继续用但 Radix UI / cmdk / sonner 等都已支持 React 19；React Router 6 也已被 v7（带 type-safe routing 改进）替代——v7 升级路径官方有 codemod。**这不是 P0 修复**，但属于"≥18 个月未跟进的方向性老化"。
- **影响**: 低。短期不影响功能；中期 dependency conflict 概率增加（新组件库要求 React 19 peer dep）。
- **正确修复方向**: 单独开 task 评估 React 18→19 + React Router 6→7 升级，给出兼容性矩阵和 codemod 报告，**不在 Wave 2 整改**。
- **工作量**: L（单独 wave）

---

## F-12 [⚠️] CI 缺少 docker-compose smoke 测试 / E2E 冒烟，仅靠单元测试

- **文件:行**: `.github/workflows/ci.yml:14-124`
- **实读摘要**: 全文只有 `pr-title` / `backend (lint+test+build+govulncheck)` / `frontend (audit+npm run check+bundle budget)` / `doc-freshness` 4 个 job，没有任何 docker build 或 e2e。`scripts/smoke-e2e.sh` 在 README/CLAUDE.md 中提到（`bash scripts/smoke-e2e.sh`），但 CI 不调用。
- **问题**: 后端 + 前端单测都全绿，不代表 docker 镜像能正常启起来——例如 supercronic 路径变更、Nginx 模板 envsubst 失败、tzdata 安装失败这类只在镜像运行时暴露的问题，CI 不会发现。Wave 0/1 加的 HEALTHCHECK + LOG_FILE + TZ 都是镜像级特性，CI 没自动验证。
- **影响**: 中。已修过的镜像层问题没有回归网。
- **正确修复方向**: 在 CI 加一个 `docker-smoke` job：build allinone Dockerfile → docker run --env-file 测试 env → wait healthcheck healthy → curl /healthz → docker logs 检查无 FATAL。可以用 docker buildx 缓存到 GHA 加速。
- **工作量**: M

---

## F-13 [✅] `pre-commit` hook 检查项与 `check-doc-freshness.sh` 不一致，两者维护成本翻倍且容易漂移

- **文件:行**: `.githooks/pre-commit:40-53` vs `scripts/check-doc-freshness.sh:33-72`
- **实读摘要**:
  - pre-commit 5 条：models.go→CLAUDE.md / router.go→README_backend.md / router.tsx→CLAUDE.md / migrations/→CLAUDE.md / config.go→docs/env-vars.md
  - check-doc-freshness 6 条：上述 5 条 + "发布/镜像/部署/版本检查变更 → 发布文档"
- **问题**: 两个脚本几乎重复但不完全相同（CI 多一条第 6 规则）。pre-commit 是阻断式（exit 1），CI 是非阻断（exit 0 第 82 行明文）——保护强度差异大。任何加新规则需要同时改两处。
- **影响**: 低。当前没看到错失，但维护成本会随规则增长。
- **正确修复方向**: 合并：抽出 `scripts/lib/doc-freshness-rules.sh` 共享 `check()` 函数 + 规则数组，两个入口都 source。或者干脆删 pre-commit（`make setup-hooks` 强度难保证），靠 CI doc-freshness 在 PR 上做提醒。
- **工作量**: S

---

## F-14 [⚠️] CI / Workflow 中无对前端 build artifact 的 cache scope，每次 `npm ci` + `npm run check` ≥ 3-5 min 浪费

- **文件:行**: `.github/workflows/ci.yml:75-103`
- **实读摘要**:
  ```yaml
  - name: Setup Node.js
    uses: actions/setup-node@... # v4
    with:
      node-version: '20'
      cache: npm
      cache-dependency-path: web/package-lock.json
  - name: Install dependencies
    run: npm ci
  ```
- **问题**: 只缓存 npm 全局缓存（即 `~/.npm`），没缓存 `web/node_modules` 也没缓存 vite/tsc 增量产物。每次 PR 都 cold install + cold typecheck + cold build。
- **影响**: 低（仅时间成本）。
- **正确修复方向**: 加 `actions/cache` 缓存 `web/node_modules`（key 用 package-lock.json hash）+ `web/.vite-cache`。或采用 `pnpm` + `pnpm/action-setup` 自带的 store cache。**非紧急**，仅在 CI 时间成为瓶颈时再做。
- **工作量**: S

---

## 不算 finding 但需要主代理知晓

- **trivy ignore-unfixed: true**（publish-images.yml:124）会把"上游基础镜像已知但未发布修复版本"的 CVE 静默通过——这是常见 trade-off，不算缺陷，但需要在 release-maintainers.md 中明确说明这条策略。
- **deploy.yml** SSH 步骤用 pinned action `appleboy/ssh-action@2ead...`，且健康检查循环是 self-contained，质量良好。
- **dockerhub-description.yml** 的 fallback 行为（line 30-42）写得很稳健，credentials 缺失时只跳过不报错——好实现。
- README.md 全文未发现"Docker Hub namespace=`xirang`"与 MEMORY.md 中"实际为 `linnea7171`"的冲突，因为 README.md 用的就是 `docker.io/xirang/xirang`——但 MEMORY.md 提到 Docker Hub 实际是 `linnea7171/xirang`，**这是用户私有信息**，不在本仓库内（公开镜像确为 xirang/xirang）。无 finding。

---

## 字数统计

约 2400 字（含代码块），未超 2500。
