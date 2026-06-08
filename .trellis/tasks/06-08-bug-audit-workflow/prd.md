# 分析仓库潜在 Bug

## Goal

对 Xirang 仓库进行全域潜在 bug 审计，并在用户确认后将范围扩展为修复已确认的高/中优先级问题。使用 Dynamic Workflow 组织多代理规划与验证，在可用时使用 CodeGraph one-shot 进行代码导航、调用链分析和影响面分析；最终完成代码修复并通过后端/前端验证。

## Confirmed Facts

- 用户已要求使用 `ultracode`，本轮已明确允许使用多代理 Workflow。
- 用户要求在完整执行前先展示 workflow plan；初版 plan 已展示并获得“允许”。
- 当前仓库是 Git 仓库，启动快照为 `main` 分支且干净；创建 Trellis 任务后新增 `.trellis/tasks/06-08-bug-audit-workflow/` 规划文件。
- 项目是前后端分离的服务器运维管理平台：后端 Go/Gin/GORM，前端 React/TypeScript/Vite。
- 常用验证命令来自项目记忆与 `web/package.json`：`cd backend && go test ./...`、`cd web && npm run check`。
- 已检查 Claude MCP 列表：当前会话未显示名为 CodeGraph 的 MCP 服务器；执行前需要先做 CodeGraph 可用性确认，并在缺失时安装/配置或明确降级方案。

## Requirements

- 覆盖所有主要模块，不设置重点模块：后端、前端、配置、部署、测试和前后端契约都需要纳入分析。
- Workflow 必须是动态/多代理导向：按模块与风险视角并行发现候选问题，再进行交叉验证和去重。
- 在完整 Workflow 执行前，先完成 CodeGraph MCP 与必要依赖检查；如果缺失且可在当前环境中安装，则先安装/配置。
- 对候选 bug 使用代码导航、调用链分析和影响面分析验证真实可达性；CodeGraph 不可用时必须在报告中明确说明，并使用仓库索引、静态阅读和测试作为降级证据。
- 报告只包含经验证或高置信度的潜在 bug；低置信度猜测应放入“需人工确认”或省略。
- 在审计完成且用户确认后，允许修改产品代码以修复已确认问题；修复需要覆盖安全、跨层契约、任务状态机、settings 加密/缓存和部署/验证配置。
- 若发现安全风险，按严重程度标注，但不提供攻击性利用步骤。

## Acceptance Criteria

- [ ] 已确认或处理 CodeGraph MCP 可用性；缺失/安装失败时有清晰记录和降级说明。
- [ ] 已运行 Dynamic Workflow，多代理覆盖后端、前端、配置/部署、测试/契约等全仓库范围。
- [ ] 每个保留发现都包含文件位置、严重级别、问题说明、调用链/影响面摘要和修复建议。
- [ ] 候选发现经过独立验证/反驳流程，明显误报已去重或剔除。
- [x] 已尽可能运行 `cd backend && go test ./...` 和 `cd web && npm run check`，并在报告中记录结果；若跳过，说明原因。
- [x] 最终输出为简洁 Markdown 报告。
- [x] 已修复已确认的 Critical/High 问题，并尽量修复可安全处理的 Medium 问题。
- [x] 修复后后端测试通过。
- [x] 修复后前端 `npm run check` 通过或剩余失败有明确说明。

## Out of Scope

- 不创建 PR、不提交、不推送。
- 不修复需要产品决策或大规模重构且无法在本轮安全验证的问题；这类问题需明确列为延期。
- 不进行破坏性安全测试、DoS、批量外部目标扫描或检测规避。
- 不将仓库内容发送到未获授权的外部服务；Workflow 子代理只在当前 Claude Code 环境内分析。

## Open Questions

- 无。用户已确认：如果 CodeGraph MCP 无法在当前会话直接接入，允许使用静态导航、测试命令与 Dynamic Workflow 多代理分析作为降级方案继续审计，并在报告中透明说明。

## Notes

- Keep `prd.md` focused on requirements, constraints, and acceptance criteria.
- Lightweight tasks can remain PRD-only.
- For complex tasks, add `design.md` for technical design and `implement.md` for execution planning before `task.py start`.
