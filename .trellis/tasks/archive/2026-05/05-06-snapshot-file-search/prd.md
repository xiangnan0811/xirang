# 跨快照内容搜索 (Snapshot File Search)

## Goal

用户可通过文件名/路径模糊搜索，在历史快照中快速定位某个文件存在于哪些快照中，支持一键跳转浏览/恢复。

## Requirements

### 数据模型
- 新增 `snapshot_file_index` 表（迁移 000054）：
  - `id`, `task_id` (index), `snapshot_id` (string, index), `path` (text, index), `size` (int64), `mtime` (string)
  - 联合唯一索引：`(task_id, snapshot_id, path)` — 防止重复索引
- 无 FTS5，用 SQLite `LIKE` + B-tree index 搜索路径

### 索引构建（lazy）
- 首次搜索时，检查该 task 是否已建索引
- 未索引：后台 goroutine 跑 `restic ls --json <latest-snapshot>` 获取文件清单，批量 INSERT
- 已索引：跑 `restic ls --json` 获取新快照的文件清单，INSERT IGNORE 增量更新
- 构建中返回 `{"status": "indexing", "progress": "X/Y snapshots"}`

### 搜索 API
- `GET /api/v1/tasks/:id/snapshots/search?q=<keyword>`
- q 为空 → 400
- 搜索范围：该 task 下所有已索引快照
- SQL：`SELECT DISTINCT snapshot_id, path, size FROM snapshot_file_index WHERE task_id=? AND path LIKE '%<q>%' LIMIT 200`
- 返回：`[{snapshot_id, path, size, mtime}]` + 快照时间（join snapshots）
- 关键词转义防 SQL 注入

### 前端
- Task 详情页新增搜索框 + 搜索结果列表
- 结果列表每行：文件名、快照时间、文件大小
- 点击行 → 跳转到该快照的文件浏览页面

### 安全
- 搜索关键词参数化绑定，防 SQL 注入
- 仅 restic 类型 task 支持

## Acceptance Criteria

- [ ] 首次搜索 task → 后台构建索引 → 前端显示"索引构建中"
- [ ] 索引完成后搜索结果即时返回
- [ ] 再次搜索（有新快照）→ 增量更新 → 搜索结果含新快照
- [ ] 搜索 `nginx.conf` → 返回所有含该文件名的快照记录
- [ ] 空关键词 → 400
- [ ] 非 restic task → 400
- [ ] `go test ./...` + `npm run check` 全绿

## Definition of Done

- 单测：索引构建/去重/搜索/注入防护
- 迁移 000054（SQLite + Postgres 双轨）
- 前端搜索框组件 + i18n
- 用户文档更新

## Out of Scope

- 文件内容全文搜索
- 跨 Task 全局搜索
- 正则搜索
- 搜索结果导出

## Decision (ADR-lite)

### D1: 索引构建 — Approach A (按需构建 lazy)
**Decision**: 首次搜索时建索引，后续增量更新。
### D2: 搜索方式 — SQLite LIKE（非 FTS5）
**Decision**: 路径 LIKE + B-tree 索引满足 MVP 需求，FTS5 后期升级。

## Technical Notes

- 迁移 000054: CREATE TABLE snapshot_file_index
- 索引逻辑: `backend/internal/snapshot/indexer.go`
- 搜索 handler: `backend/internal/api/handlers/snapshot_search_handler.go`
- 前端: Task 详情页嵌入搜索组件

## Implementation Plan

| PR | 内容 | 预估 |
|---|---|---|
| **PR1** | 模型+迁移+indexer（索引构建/增量）+ 搜索 handler + 单测 | 2.5 天 |
| **PR2** | 前端搜索框 + i18n + 集成测试 + 文档 | 1.5 天 |

总计：**4 天**
