# 文档治理 finding 复核

- **日期**：2026-05-03
- **范围**：仓库根目录、`backend/`、`web/`、`docs/`、`.githooks/`、`~/.claude/projects/-Users-weibo-Code-xirang/memory/`
- **方法**：直接 Read / ls 实读核验，未做主观判断

---

## D1 CLAUDE.md migration 版本
- **状态**：真实
- **当前**：CLAUDE.md:50 写 `000033_node_metric_samples_extend`
- **应为**：`000047_alert_deliveries_drop_error`（实际目录最新文件 `000047_alert_deliveries_drop_error.up.sql`）
- **修复**：直接 Edit `/Users/weibo/Code/xirang/CLAUDE.md` 第 50 行
- **工作量**：S

## D2 MEMORY.md migration 版本
- **状态**：真实
- **当前**：`/Users/weibo/.claude/projects/-Users-weibo-Code-xirang/memory/MEMORY.md` 第 33-40 行写 `000030_task_run_progress (latest)`，列出 000025–000030
- **应为**：000047（实际已新增 17 个迁移：000031–000047）
- **修复**：Edit MEMORY.md，更新 "Current Migration Version" 与列表；该文件在 `~/.claude` 下，不在仓库内，修改不会被 git 追踪
- **工作量**：S
- **备注**：MEMORY.md 文件头有 14 天点位提示，里面 PRD 路径 `.omc/prd.json` 等内容也可能过期，但不在本次 finding 范围内

## D3 CLAUDE.md "30+ handler" 计数
- **状态**：部分真实（描述与现状偏差，但子代理 "42 个" 数字也不精确）
- **实测**：`backend/internal/api/handlers/` 下非 `_test.go` 的 `.go` 文件共 **47** 个，其中真正的 handler 文件约 **42** 个；剩余 5 个为辅助文件：`helpers.go`、`response.go`、`realtime_auth.go`、`storage_guide_darwin.go`、`storage_guide_linux.go`
- **当前**：CLAUDE.md 写 "含 30+ handler"
- **建议**：写为 "含 40+ handler" 即可保持模糊正确，无需精确到 42
- **工作量**：S

## D4 .githooks/pre-commit 引用 backend/README_backend.md
- **状态**：误报
- **核实**：
  - `.githooks/pre-commit` 第 43-44 行确实引用 `backend/README_backend.md`
  - 但 `/Users/weibo/Code/xirang/backend/README_backend.md` **存在**（与 go.mod 同目录）
- **结论**：pre-commit 钩子工作正常，无需修复
- **工作量**：N/A

## D5 缺 CODE_OF_CONDUCT.md
- **状态**：真实
- **核实**：仓库根目录 ls 结果无 `CODE_OF_CONDUCT.md`，相关治理文件仅 `LICENSE`、`SECURITY.md`、`CONTRIBUTING.md`
- **修复**：新建 `/Users/weibo/Code/xirang/CODE_OF_CONDUCT.md`，可采用 Contributor Covenant 2.1 模板
- **工作量**：S
- **优先级**：低（开源项目治理建议项，非阻塞）

## D6 缺 web/.env.example
- **状态**：误报
- **核实**：`/Users/weibo/Code/xirang/web/.env.example` **存在**
- **结论**：CLAUDE.md 第 60 行的提示与 `docs/env-vars.md` 第 3 行的引用都指向真实存在的文件
- **工作量**：N/A

## D7 docs/env-vars.md 与代码不同步
- **状态**：部分真实（描述模糊，但有可观察的结构缺陷）
- **核实**：
  - `docs/env-vars.md` 存在，14 章节结构完整（服务器/数据库/认证/CORS/SSH/备份/节点探测/数据保留/邮件/告警/前端/部署/版本检查/指标推送）
  - 每节都标注了"读取位置"，与 `backend/internal/config/config.go` 等代码路径关联
  - **缺失**：grep 未匹配到 "敏感字段处理统一指南" 或类似章节；CLAUDE.md 提到的"敏感字段（密码、私钥、TOTP、端点、代理地址）通过 GORM hooks 自动加解密"在 env-vars.md 中无对应说明
- **修复**：在 `docs/env-vars.md` 第 3 节（认证与安全）后追加"敏感字段加密处理"小节，引用 `backend/internal/secure/crypto.go` 与 GORM hooks
- **工作量**：M（需要梳理代码中真实的加密字段清单）

---

## 总体结论

**统计**（7 项）：
- 真实：4 项（D1、D2、D5、D7）
- 误报：2 项（D4、D6）
- 描述失真但有真问题：1 项（D3，数字偏差不大）

**误报率**：2/7 ≈ 29%。子代理报告确实存在虚报，但核心主线 finding（D1/D2/D7）真实有效。

**建议优先修复顺序**（按工作量与价值）：
1. **D1（S）**：CLAUDE.md migration 版本号 — 单行 Edit，最小成本
2. **D2（S）**：MEMORY.md migration 版本号 — 单文件更新，影响后续 AI 会话准确性
3. **D3（S）**：CLAUDE.md handler 计数 — 改为 "40+" 即可
4. **D7（M）**：docs/env-vars.md 增补敏感字段加密章节 — 需阅读 `secure/crypto.go` 与各 model hooks
5. **D5（S）**：补 CODE_OF_CONDUCT.md — 用 Contributor Covenant 模板，低优先级

**无需处理**：D4（pre-commit 引用文件存在）、D6（web/.env.example 存在）

---

## 附录：实测命令与输出摘要

- migration 目录最新 5 个：`000043_escalation` → `000044_anomaly` → `000045_alert_slo_id` → `000046_node_logs_node_created_index` → `000047_alert_deliveries_drop_error`
- handler 目录非测试 `.go` 文件：47 个
- pre-commit 引用关系：`backend/internal/api/router.go` → `backend/README_backend.md`（文件均存在）
- docs/env-vars.md 章节：1 服务器/2 数据库/3 认证安全/4 CORS+WS/5 SSH/6 备份执行/7 节点探测/8 数据保留/9 邮件/10 告警/11 前端/12 部署/13 版本检查/14 指标推送（无敏感字段加密专章）
