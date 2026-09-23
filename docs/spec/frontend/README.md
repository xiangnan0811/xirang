# 前端开发合同

本目录维护 React 控制台的通用约定。按变更任务阅读相关篇目；业务行为同时阅读[领域合同](../domains/README.md)，不在通用规范中复制领域状态机。

| 合同 | 阅读时机 |
|---|---|
| [目录结构](directory-structure.md) | 新增页面、模块、路由或调整文件归属 |
| [组件](component-guidelines.md) | 组件组合、页面外壳、属性、样式与导航 |
| [Hook](hook-guidelines.md) | 请求编排、副作用与资源清理 |
| [状态管理](state-management.md) | Context、认证、页面草稿和浏览器存储 |
| [类型与 API 边界](type-safety.md) | DTO、映射、响应信封和运行时校验 |
| [质量与回归](quality-guidelines.md) | 验证范围、时间夹具、演示模式 |
| [可访问性](a11y-guidelines.md) | 标签、焦点、键盘、对比度和 axe |

开发环境、命令、hooks 与 PR 流程见[贡献指南](../../../CONTRIBUTING.md)。依赖与构建工具维护见[仓库自动化](../../maintainers/automation.md)。上级入口：[开发合同](../README.md)。
