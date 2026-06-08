# Implement Plan: 仓库潜在 Bug Dynamic Workflow 审计与修复

## Ordered Checklist

1. **Preflight**
   - 确认当前 Trellis 任务。
   - 检查 Git 状态。
   - 检查 MCP 列表和 CodeGraph 可用性。
   - 检查项目验证命令和依赖状态。

2. **CodeGraph / Dependency Handling**
   - 如果 CodeGraph MCP 可用，记录可用状态并在 Workflow 提示中要求代理使用它。
   - 如果缺失，搜索项目与环境中的 CodeGraph 配置/安装线索。
   - 若能安全安装或配置，先执行；否则记录降级方案。

3. **Create Workflow Script**
   - 使用 Dynamic Workflow。
   - 第一阶段按模块和风险视角并行扫描。
   - 第二阶段对候选 bug 并行验证/反驳。
   - 第三阶段综合去重、分级和报告。

4. **Run Workflow**
   - 执行 Workflow。
   - 读取结果，必要时补充人工 Read/Grep 验证。

5. **Validation Commands**
   - 运行 `cd backend && go test ./...`。
   - 运行 `cd web && npm run check`。
   - 如果命令失败，记录失败摘要并判断是否支持某个发现。

6. **Report**
   - 输出简洁 Markdown 报告。
   - 包含 CodeGraph 状态、验证命令结果、确认发现、需人工确认项和未覆盖风险。

7. **Fix Confirmed Bugs**
   - 修复 API envelope / retryAfter / version 契约 / TS baseUrl。
   - 修复 2FA setup、文件浏览权限、告警升级事件 ownership。
   - 修复 retry 参数校验、预启动取消、automation trigger_task、WebSocket close 幂等。
   - 修复 settings 敏感值加密、GetEffective 错误处理、V1 加密迁移遗漏。
   - 对 Policy hook 明文凭据执行可验证的最小修复，若需要更大架构改动则列为延期并说明风险。

8. **Verify Fixes**
   - 运行后端相关单测与 `go test ./...`。
   - 运行前端 `npm run check`。
   - 使用多代理/Workflow 复核关键修复无明显回归。

## Validation Commands

```bash
cd backend && go test ./...
cd web && npm run check
```

## Risky Files / Rollback Points

- `.trellis/tasks/06-08-bug-audit-workflow/prd.md`
- `.trellis/tasks/06-08-bug-audit-workflow/design.md`
- `.trellis/tasks/06-08-bug-audit-workflow/implement.md`
- 如果为 CodeGraph 安装/配置工具，需记录具体变更并在用户要求时回滚。

## Sub-agent Context Manifests

- `implement.jsonl`：提供项目结构、任务要求、验证命令。
- `check.jsonl`：提供验收标准、CodeGraph 状态、报告要求。

## Review Gate Before Start

开始执行前确认：

- PRD/design/implement 已写入。
- 用户已允许创建 Trellis 任务并进入规划/执行。
- 对唯一剩余范围决策作出处理：推荐若 CodeGraph MCP 无法接入，则记录降级并继续审计。
