# 应用感知备份 (DB-aware backup profiles)

## Goal

让 Xirang 的 Policy 能"识别业务应用"——选择 MySQL / Postgres / MongoDB / Redis / Docker 等 profile 后，自动注入正确的 dump / quiesce 钩子，使备份**保证业务数据一致性**，从"备份文件"升级到"备份业务数据"。这是把 Xirang 从"运维玩具"推向"敢用在生产"的关键一步。

## Requirements

### 数据模型
- 新增 `AppCredential` 资源：first-class 凭据资产
  - 字段：`id`, `name` (uniqueIndex), `type` (enum), `description`, 加密 JSON `config` (含 host/port/user/password/container_name 等)
  - GORM `BeforeSave/AfterFind` 走 `secure.EncryptIfNeeded` 加解密 password 字段
  - API 响应不返回 password 明文，仅返回 `has_password` bool（参照 `Integration.HasSecret` 模式 `models.go:188`）
- `Policy` 加字段：
  - `app_profile` (string enum, nullable) — 留空 = 当前行为，向后兼容
  - `app_credential_id` (uint, nullable, FK) — 引用 AppCredential
- 数据库迁移：`000048_app_credentials_and_policy_profile`（SQLite + Postgres 双轨）

### 内置 8 个 Profile
| Profile ID | 类型 | 说明 |
|---|---|---|
| `mysql` | 主机 | `mysqldump --single-transaction --all-databases` |
| `postgres` | 主机 | `pg_dumpall` (su to postgres) |
| `mongodb` | 主机 | `mongodump` |
| `redis` | 主机 | `BGSAVE` + cp RDB |
| `docker-mysql` | 容器 | `docker exec -i {container} mysqldump ...` |
| `docker-postgres` | 容器 | `docker exec -i {container} pg_dumpall ...` |
| `docker-mongodb` | 容器 | `docker exec {container} mongodump ...` |
| `docker-redis` | 容器 | `docker exec {container} redis-cli BGSAVE ...` |

每个 profile 携带：
- 期望 AppCredential.type 名（强匹配）
- pre-hook / post-hook 模板（含变量占位）
- 配置项 schema（前端用于动态渲染表单）

### Hook 渲染机制
- Policy 保存时根据 `app_profile` + 关联 `AppCredential.config` 渲染 hook 模板，写入 `Policy.PreHook` / `PostHook`（**用户可见、可手动 override**）
- AppCredential **更新后自动重渲染所有引用此 credential 的 Policy 的 hook 字段**（防止密码不同步，#5）
- 渲染时若 profile=`docker-*`，先注入"容器存在性预校验"片段（`docker inspect {container} >/dev/null`）（#9）

### 引用完整性
- 删除 AppCredential 时若存在 Policy 引用 → 阻止删除并返回引用列表（#10）

### 后端 API
- `GET /api/v1/app-credentials` — 列表
- `POST /api/v1/app-credentials` — 创建
- `GET /api/v1/app-credentials/:id` — 详情（不含 password）
- `PUT /api/v1/app-credentials/:id` — 更新（password 字段空 = 不变）
- `DELETE /api/v1/app-credentials/:id` — 删除（引用校验）
- `GET /api/v1/app-credentials/profiles` — 返回 8 个 profile 的 schema（替代 `GET /hook-templates`，旧端点标 deprecated 但保留）
- Policy CRUD：增加 `app_profile` / `app_credential_id` 字段处理

### 前端
- 新增页面 `/app/credentials`（资源列表 + 创建/编辑/删除）
  - 按 type 过滤
  - 编辑时密码字段空 = 不修改；显示 "has_password" 状态
- Policy 编辑页扩展：
  - "应用类型" 下拉（无 / mysql / postgres / .../docker-redis）
  - 选定后联动 "Credential" 选择器（仅显示匹配 type 的）
  - 显示渲染后的 pre-hook / post-hook 预览（只读，附"展开手动编辑"按钮 → 与现有字段融合）
- 弃用现有"内置 Hook 模板列表 + 复制按钮"UI

### 兼容性
- 现有所有 Policy（无 profile）保持完全向后兼容
- 现有 hook 测试（`backend/internal/task/hook_test.go`）必须全部 GREEN
- `GET /hook-templates` 端点保留，响应不变（标 deprecated header）

## Acceptance Criteria

- [ ] 创建 `mysql` 类型 AppCredential（host/user/password）→ 创建 Policy 选 mysql profile + 该 credential → 触发备份能拿到有效 .sql dump 文件并备走
- [ ] 同上但用 `docker-mysql`，能 `docker exec` 进容器跑 dump
- [ ] 4 主机 profile + 4 容器 profile 都有 e2e 集成测试 GREEN（可用 testcontainers 或脚本化 fixture）
- [ ] 修改 AppCredential 密码后，所有引用此 credential 的 Policy 的 `pre_hook` 字段自动同步更新
- [ ] 删除被引用的 AppCredential 返回 409 + 引用列表，前端友好提示
- [ ] 容器化 profile 的 hook 在容器不存在时返回明确错误（不是 cryptic mysqldump 错）
- [ ] AppCredential password 在数据库中加密；API 响应仅返回 `has_password`
- [ ] 现有所有 Policy（无 app_profile）保持原行为，相关测试 GREEN
- [ ] 数据库迁移可双向（up + down 都成功）
- [ ] `go test ./...` + `npm run check` 全绿
- [ ] Swagger 文档更新；用户文档新增"应用感知备份"章节

## Definition of Done

- 单测覆盖 profile 渲染 / 凭据加解密 / credential 更新触发重渲染 / 删除引用校验
- 集成测试至少 8 个 profile 各一个 e2e（可分批跑）
- 数据库迁移脚本（SQLite + Postgres 双轨），含 down 迁移
- `go test ./...` + `npm run check` 全绿
- API Swagger 注解完整
- 前端：AppCredential 管理页 + Policy form profile 选择器
- 升级 / 回滚说明写到 PR description
- 用户文档 + changelog 条目

## Out of Scope

- **#1** 自定义 profile（用户自己写 dump 模板）→ 留作后续 PR
- **#2** profile 与 anomaly detection 联动（dump size 骤变）→ 跑题到 anomaly 任务
- **#3** profile 与 recovery drill 联动 → 等 drill 任务实现后再联动
- **#4** Restore 端 profile hook（自动 import）→ 独立任务
- **#6** 自动检测节点上跑哪些 DB → 另一个产品方向
- **#8** 临时目录磁盘容量预校验 → 与磁盘监控重叠
- 数据库集群备份（主从切换、binlog PITR）
- 自动凭据轮换 / Vault 集成
- AppCredential 细粒度 RBAC（MVP: admin 管理，普通用户只能引用）
- 应用层备份（GitHub repos / 邮箱等 SaaS）

## Technical Approach

### 关键设计
1. **AppCredential 是 first-class 资源**，独立 CRUD，有 `type` 字段强约束 profile 匹配
2. **Hook 渲染在保存时一次性发生**，写入 `pre_hook/post_hook` 字段（用户可见可改），保持现有 runner 路径零改动
3. **Credential 更新触发重渲染**：在 AppCredential `AfterSave` hook 中查询所有引用此 credential 的 Policy 并重新渲染 hook（事务内）
4. **容器化预校验**：所有 `docker-*` profile 的 pre-hook 模板第一行注入 `docker inspect {container} >/dev/null 2>&1 || { echo "容器 {container} 不存在或未运行"; exit 1; }`
5. **删除保护**：AppCredential delete handler 先 `SELECT count(*) FROM policies WHERE app_credential_id = ?`，>0 则返回 409
6. **Profile 定义集中**：在 `backend/internal/profile/` 新包 集中 8 个 profile 的 hook 模板 + schema，方便后续添加和测试

### 受影响文件
- `backend/internal/model/models.go`（Policy 加字段，新增 AppCredential）
- `backend/internal/profile/`（新包）
- `backend/internal/api/handlers/app_credential_handler.go`（新增）
- `backend/internal/api/handlers/policy_handler.go`（保存逻辑加 profile 渲染）
- `backend/internal/api/handlers/hook_templates_handler.go`（标 deprecated）
- `backend/internal/database/migrations/{sqlite,postgres}/000048_*.sql`
- `backend/internal/api/router.go`（注册新路由）
- `web/src/pages/credentials-page.tsx`（新增）
- `web/src/components/credential-editor.tsx`（新增）
- `web/src/components/policy-editor*.tsx`（扩展 profile 选择器）
- `web/src/lib/api/credentials.ts`（新增）
- `web/src/router.tsx`（注册新路由）

### 复用现有能力
- 加密：`backend/internal/secure/secure.go`（`EncryptIfNeeded` / `DecryptIfNeeded`）+ GORM hooks，参照 `Integration.BeforeSave/AfterFind` 模式（`models.go:141-190`）
- Hook 执行：`backend/internal/task/hook.go` + `runner.go:308-396` 完全不动
- API 响应：参照 `Integration.HasSecret` 字段（`models.go:132-188`）

### 风险与回滚
- **数据迁移失败**：新字段全部 nullable，回滚 = drop 新列 + drop app_credentials 表，零数据损失
- **渲染逻辑 bug**：保留"用户手写 hook"通道作为 escape hatch（profile 留空 = 等同当前行为）
- **AppCredential.AfterSave 触发的级联渲染失败**：用 transaction，失败回滚整个 update
- **容器化预校验阻塞用户**：明确错误信息 + 文档说明如何排查

## Decision (ADR-lite)

### D1: 架构方向 — Approach B (半结构化 profile)
**Context**: 模板复制（A）/ 半结构化（B）/ 完全动态（C）/ 专用 executor（D）。
**Decision**: **Approach B**。Policy 加 `app_profile` 枚举 + `app_credential_id` 外键；保存时渲染 hook 文本（用户仍可见可 override）。
**Consequences**: profile 是 first-class 数据；调试可见；完全向后兼容；需要 SQLite + Postgres 双轨迁移。

### D2: 凭据存储位置 — Approach D (独立 AppCredential)
**Context**: Policy 嵌入 / Node 字段 / 独立资源 / 全新 Vault。
**Decision**: 新建 **AppCredential** 资源。
**Consequences**: 凭据可跨 Policy 复用；轮换只需改一处；可治理；MVP +1.5 天；删除需校验引用。

### D3: Profile 覆盖范围 — 8 profile（4 主机 + 4 容器化）
**Context**: 1 / 4 / 8 三档。
**Decision**: 一次到位 8 个，覆盖现代部署主流场景。
**Consequences**: 首发即覆盖容器化；AppCredential 必须有 container_name 字段；MVP 10-14 天；PR 必须拆分。

### D4: 多 Profile 同节点策略 — 1 Policy : 1 Profile
**Context**: 同节点 MySQL + Redis 怎么处理。
**Decision**: 保持 Policy : Credential = 1 : 1。多 DB 同节点 = 多 Policy。
**Consequences**: 数据模型最简；不同 DB 独立 cron / retention / 告警。

### D5: MVP 边界 — Core + #5 + #9 + #10
**Context**: 10 个候选边界场景的取舍。
**Decision**: MVP 包含核心 + #5 (credential 更新触发重渲染) + #9 (容器化预校验) + #10 (悬挂引用保护)。其余出范围。
**Consequences**: ~12 天工作量；密码不会同步不一致；容器化错误友好；无数据库孤儿。

## Implementation Plan (PR 拆分)

| PR | 内容 | 预估 |
|---|---|---|
| **PR1** | AppCredential 模型 + GORM 加密 hook + 迁移 000048 + 基础 CRUD handler + 单测 | 2-3 天 |
| **PR2** | `profile/` 包：8 profile 定义 + hook 渲染 + Policy 保存集成 + credential AfterSave 触发重渲染（#5）+ 删除引用校验（#10）+ 单测 | 3 天 |
| **PR3** | 容器化场景：4 个 docker-* profile 的 hook 模板 + 容器存在性预校验注入（#9）+ 集成测试 | 1-2 天 |
| **PR4** | 前端 AppCredential 管理页（列表 / 编辑 / 删除拦截）+ API client | 2 天 |
| **PR5** | 前端 Policy 编辑页 profile 选择器 + 联动 credential + hook 预览 + 弃用旧"复制 hook 模板"UI | 1-2 天 |
| **PR6** | 文档（用户向 + Swagger）+ changelog + `GET /hook-templates` deprecation header | 0.5-1 天 |

总计：**9.5-13 天**

## Technical Notes

### 受影响的关键代码位置（已审计）
- `backend/internal/model/models.go:89-117` — Policy 模型，加 `app_profile` + `app_credential_id`
- `backend/internal/model/models.go:126-190` — Integration 模型 = AppCredential 加密模式参照
- `backend/internal/api/handlers/hook_templates_handler.go:16-52` — 现有静态模板，重构为 profile 包的初始数据
- `backend/internal/api/handlers/policy_handler.go` — 保存逻辑加 hook 渲染
- `backend/internal/task/runner.go:308-396` — hook 调用点（不改）
- `backend/internal/task/hook.go:12-27` — SSH hook 执行（不改）
- `backend/internal/task/hook_test.go` — 回归基线（不能 RED）

### 测试策略
- **单元测试**：profile 渲染（每个 profile 一个 case）/ 加密解密 / credential 更新触发 / 删除引用校验
- **集成测试**：使用 testcontainers 或 docker compose fixture，跑真实 mysql/postgres/mongo/redis 容器，验证 e2e 备份能产出有效 dump
- **回归**：现有 `hook_test.go` 必须全 GREEN，证明无 profile 的 Policy 行为不变

### 升级与回滚
- **升级**：迁移 000048 创建 app_credentials 表 + Policy 加新列；现有数据零变更
- **回滚**：先回滚代码，然后 down 迁移（drop 新列 + drop 表）；现有 Policy 完全不受影响
- 文档需明确："已使用 app_profile 的 Policy 在回滚后会回到手写 hook 模式（pre_hook 字段保留渲染后内容，可继续工作）"
