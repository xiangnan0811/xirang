# 页面局部入口

遵循 [项目入口](../../../AGENTS.md)。本目录保存路由页面及同级片段，测试与页面同目录；
页面专用 Hook 可放局部 `hooks/`。路由在 `web/src/router.tsx`，懒加载导出在
`web/src/router-pages.tsx`。

按修改内容加载 [目录与拆分](../../../docs/spec/frontend/directory-structure.md)、
[组件](../../../docs/spec/frontend/component-guidelines.md)、
[类型](../../../docs/spec/frontend/type-safety.md)、
[可访问性](../../../docs/spec/frontend/a11y-guidelines.md)。业务交互和兼容要求统一见
[领域合同](../../../docs/spec/domains/README.md)。
