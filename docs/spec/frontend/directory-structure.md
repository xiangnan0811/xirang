# 前端目录结构

前端位于 `web/`，使用 Vite、React 和 TypeScript，入口为 `web/src/main.tsx`。先沿用现有职责划分，再考虑新增目录。

```text
web/src/
├── components/          # 跨页面业务组件和对话框
│   ├── layout/          # 应用外壳、侧栏、移动导航
│   └── ui/              # 共用 UI 原语
├── context/             # 应用级 Context
├── features/            # 内聚功能模块
├── hooks/               # 跨页面 Hook 和控制台操作
├── i18n/                # 语言初始化与资源
├── lib/
│   ├── api/             # 类型化 API 与 DTO 映射
│   └── ws/              # WebSocket 辅助
├── pages/               # 路由页面及其片段
├── router.tsx           # 路由对象和树
├── router-pages.tsx     # 懒加载页面组件及回退界面
├── types/               # 共享领域类型
└── data/                # 本地演示数据
```

## 模块归属

- 路由页面放在 `pages/`。`router.tsx` 只构造路由对象；组件导出、懒加载声明与共用回退界面放在 `router-pages.tsx`，遵守[组件导出边界](component-guidelines.md#组件与导出边界)。
- 大页面拆为同级 `*-page.<part>.tsx`，例如 `tasks-page.dialogs.tsx`，不要为一次性片段创建多层目录。
- 拥有独立 Hook、分页签、图表和测试的内聚功能放在 `features/<feature>/`，例如 `features/nodes-detail/`。
- 共用视觉原语放在 `components/ui/`；跨页面业务对话框和面板放在 `components/`，单路由内容就近存放。
- API 包装及 snake_case 到 camelCase 的映射放在 `lib/api/`。
- 可复用 Hook 放在 `hooks/`；功能内 Hook 放在功能目录；页面专用 Hook 可放在 `pages/dashboards/hooks/`。
- 备份入口、文件中心和仓库管理的路由职责见[Catalog 合同](../domains/backup-catalog.md)。

## 命名与类型位置

- 文件用 kebab-case，例如 `node-editor-dialog.tsx`、`use-console-data.utils.ts`。
- 组件用 PascalCase 导出；Hook 用 `use*` 名称。
- 测试就近命名为 `*.test.ts` 或 `*.test.tsx`；共用原语也可放在其 `__tests__/` 目录。
- 跨模块领域类型归 `types/domain.ts`，组件本地类型就近声明，原始 API 类型保留在对应 API 模块内。
- Context 拆分见[状态管理](state-management.md)。

返回[前端入口](README.md)。
