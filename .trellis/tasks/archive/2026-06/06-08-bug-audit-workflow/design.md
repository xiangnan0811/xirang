# Design: 仓库潜在 Bug Dynamic Workflow 审计

## Architecture and Boundaries

本任务初始阶段只做分析和报告；用户确认后范围扩展为修复已确认问题。允许修改范围包括 Trellis 任务规划产物、必要审计记录，以及与已确认缺陷直接相关的后端/前端/配置/测试文件。

审计流程分为四层：

1. **Preflight 层**
   - 检查 Git 状态、MCP 列表、依赖与验证命令。
   - 确认 CodeGraph MCP 是否可用；若不可用，尝试可行的安装/配置路径或记录降级。

2. **Discovery 层**
   - 使用 Dynamic Workflow 并行扫描后端、前端、配置/部署、测试/契约。
   - 每个扫描代理只返回候选 bug、证据位置和需要验证的调用链。

3. **Verification 层**
   - 对候选 bug 做独立反驳验证。
   - 优先使用 CodeGraph MCP 做调用链与影响面验证；不可用时使用静态代码导航、测试和配置证据。
   - 去重同源发现，剔除不可达或证据不足项。

4. **Synthesis 层**
   - 按严重级别排序。
   - 生成简洁 Markdown 报告，包含验证命令结果和未覆盖风险。

## Data Flow and Contracts

Workflow 子代理输出应遵循统一结构：

- `title`: 简短问题标题
- `severity`: Critical / High / Medium / Low
- `files`: 文件路径与行号
- `evidence`: 代码证据摘要
- `callChain`: 调用链或数据流摘要
- `impact`: 影响面
- `fix`: 修复建议
- `confidence`: high / medium / low

最终报告只保留 high/medium 置信度且经过验证的发现。低置信度项仅在确有价值时进入“需人工确认”。

## CodeGraph MCP Usage

目标使用方式：

- 定位关键入口点：HTTP route、handler、middleware、task scheduler/executor、GORM hooks、前端 API client、hooks、页面组件。
- 查询符号引用、调用者/被调用者、跨文件依赖。
- 对候选 bug 做影响面确认。

如果当前 Claude MCP 列表没有 CodeGraph：

- 先检查是否有可安装的 CodeGraph MCP 包或项目配置。
- 如无法可靠接入，在报告中明确“CodeGraph MCP 不可用”，并使用 grep/glob/read、Trellis spec、测试命令与 Workflow 代理作为降级导航。

## Compatibility and Migration Notes

- 不修改应用代码，因此没有运行时兼容性或迁移影响。
- 若安装 MCP 或工具依赖，应避免污染生产依赖；优先使用临时 CLI、MCP 配置或开发工具链。
- 不执行 Git push/pull/merge/rebase/reset。

## Trade-offs

- **全仓库覆盖 vs 深度**：用户明确要求所有模块都分析，因此先广覆盖，再对高风险候选深入验证。
- **CodeGraph 强依赖 vs 继续审计**：CodeGraph 能提高调用链准确度，但若环境无法接入，完全阻塞会降低本轮价值；推荐允许降级继续，并在报告中透明说明。
- **报告简洁 vs 证据完整**：最终报告保持简洁，但每个问题必须保留足够文件/调用链/影响面证据。

## Fix Architecture

修复阶段按问题域分层处理：

1. **跨层 API 契约**：统一后端 envelope 与前端 request 解包语义，修复 retryAfter、version 认证契约和 TypeScript 检查失败。
2. **安全/RBAC**：保护 2FA active secret，限制文件浏览权限，补齐告警升级事件对象级授权。
3. **任务与并发**：校验 retry 参数，修正预启动取消，修复自动化 trigger_task 真正进入任务执行路径，确保 WebSocket client close 幂等。
4. **settings/加密/凭据**：避免敏感 settings 明文存库，修复 GetEffective DB 错误回退缓存，补齐 V1 加密迁移字段；Policy hook 明文凭据优先以最小安全修复处理或延期说明。

## Operational / Rollback Considerations

- Trellis 规划文件可通过 Git 删除或修改回滚。
- 若安装依赖导致配置变化，需在最终报告列出变更；若用户不希望保留，应回滚。
- Workflow 结果不直接修改业务文件。
