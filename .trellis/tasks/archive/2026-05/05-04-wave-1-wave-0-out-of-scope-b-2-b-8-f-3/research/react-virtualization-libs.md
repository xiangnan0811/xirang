# Research: React 长列表虚拟化库选型

- **Query**: 给 logs/tasks/audit/nodes 4 处页面补虚拟化
- **Scope**: 外部（npm + GitHub + bundlephobia）+ 内部代码（确认 Radix + tailwind 栈）
- **Date**: 2026-05-03

## 仓库现状（必读）

- React 18.3.1 + TypeScript 5.8 + Vite 7 + Tailwind 3 + Radix UI（无 Radix Table，原生 `<table>` + Tailwind 样式，见 `web/src/pages/nodes-page.table.tsx:38-66`）
- 已用 `useClientPagination` hook（`web/src/hooks/use-client-pagination.ts`）做分页缓解
- Bundle 预算：main JS 540 KiB，main CSS 70 KiB（`web/scripts/check-bundle-budget.mjs:7-8`），新增依赖必须可控
- Logs 页面用普通 `<div>` 容器 + `.map()`（`web/src/pages/logs/logs-viewer.tsx:48-57`），高度 `h-[62vh]`，已 cap 400+200

## 候选库对比（数据日期 2026-05-03）

| 库 | 版本 | gzipped | 周下载 | 最新 commit | License | TS 一等 |
|---|---|---|---|---|---|---|
| **react-window** | 2.2.7 | **6.7 KiB** | 5.5M | 2026-04-12 | MIT | 是 |
| **@tanstack/react-virtual** | 3.13.24 | 5.4 KiB（+ core 共 ~12 KiB） | **12.9M** | 2026-04-26 | MIT | 是（headless） |
| **react-virtuoso** | 4.18.6 | 18.9 KiB | 2.9M | 2026-04-24 | MIT | 是 |
| **react-virtualized** | 9.22.6 | 27.9 KiB | 1.8M | **2025-01-20**（停滞 ~16 月） | MIT | 弱（@types 第三方） |

数据来源：`registry.npmjs.org`、`api.npmjs.org/downloads`、`bundlephobia.com/api/size`、GitHub commits API。

### react-window v2 关键变化

v2 是 **2025 年完全重写版本**，API 与 v1 不兼容。新 API：

```tsx
import { List, useDynamicRowHeight } from 'react-window';

<List
  rowComponent={Row}
  rowCount={items.length}
  rowHeight={32}                  // 或 useDynamicRowHeight() 返回的 cache
  rowProps={{ items }}             // 传给 Row 的额外 props
/>
```

**网络上多数 react-window 教程是 v1**，使用时需要核对版本号。v2 内置 `useDynamicRowHeight` 解决可变行高问题，比 v1 时代复杂的 `VariableSizeList` + 手动 measure 简洁很多。

### @tanstack/react-virtual

Headless（不渲染任何 DOM），返回 `virtualItems` 数组让你自己渲染，最大灵活性。和 TanStack Table、TanStack Query 同生态。下载量第一，部分原因是被 shadcn/ui combobox 和大量数据网格库当依赖。

```tsx
import { useVirtualizer } from '@tanstack/react-virtual';

const parentRef = useRef<HTMLDivElement>(null);
const v = useVirtualizer({
  count: items.length,
  getScrollElement: () => parentRef.current,
  estimateSize: () => 32,
  overscan: 8,
});

<div ref={parentRef} style={{ height: '62vh', overflow: 'auto' }}>
  <div style={{ height: v.getTotalSize(), position: 'relative' }}>
    {v.getVirtualItems().map((vi) => (
      <div
        key={vi.key}
        ref={v.measureElement}              // 自动测量可变高度
        data-index={vi.index}
        style={{
          position: 'absolute', top: 0, left: 0, width: '100%',
          transform: `translateY(${vi.start}px)`,
        }}
      >
        <LogEntry log={items[vi.index]} />
      </div>
    ))}
  </div>
</div>
```

### react-virtuoso

开箱即用组件，自带可变行高、ResizeObserver、sticky header、grouping、TableVirtuoso 子组件支持原生 `<table>`。bundle 最大（19 KiB）。

```tsx
import { TableVirtuoso } from 'react-virtuoso';

<TableVirtuoso
  style={{ height: '62vh' }}
  data={tasks}
  components={{
    Table: (props) => <table {...props} className="min-w-[1100px] text-left text-sm" />,
    TableHead, TableRow, TableBody,
  }}
  fixedHeaderContent={() => <tr>...</tr>}
  itemContent={(index, task) => <TaskRow task={task} />}
/>
```

### react-virtualized（不推荐）

stale 16 个月，bundle 最大，依赖 prop-types/dom-helpers/clsx 等老旧包。原作者（也是 react-window 作者）已声明 react-window 是替代品。

## 维度分析

### 与 Radix + 原生 table 的协作

仓库的表格全是原生 `<table>` + Tailwind（`min-w-[1100px]`、`overflow-x-auto`，见 `tasks-page.table.tsx:67-68`）。虚拟化要求行容器是 `position: absolute` 或自定义 `<tbody>` 渲染，会破坏表格语义。

- **react-virtuoso** 有 `TableVirtuoso` 专门解决这个问题，渲染真实的 `<table><thead><tbody>`，header 用 sticky position，最少改动现有 markup。
- **@tanstack/react-virtual** 需要把 `<tbody>` 改成 `<div>`（或用 `display: table-row` hack），失去原生 table 的水平对齐能力。或者完全用 div + grid 重写表格（参考 TanStack Table 官方示例）。
- **react-window v2** 同 tanstack，不是 table-friendly，更适合纯列表。

### 可变行高（logs 必需）

- react-virtuoso：自动，无需配置
- @tanstack/react-virtual：通过 `measureElement` 自动测量
- react-window v2：通过 `useDynamicRowHeight` hook
- react-virtualized：`CellMeasurer` + `CellMeasurerCache`，复杂

### Bundle 预算影响

| 库 | gzipped | 占预算 |
|---|---|---|
| react-window | 6.7 KiB | 1.2% |
| @tanstack/react-virtual | 5.4 KiB | 1.0% |
| react-virtuoso | 18.9 KiB | 3.5% |

都在可接受范围。如果 4 个页面都用同一个库，复用收益更高。

### SSR

仓库是 SPA（Vite + React Router），不做 SSR，此项不影响选型。

## 推荐方案

### 主推：@tanstack/react-virtual + react-virtuoso 混合

**4 个场景按需选型**，避免一刀切：

| 页面 | 数据形态 | 推荐库 | 理由 |
|---|---|---|---|
| `logs-viewer.tsx` | 流式日志，可变行高，div 容器 | **@tanstack/react-virtual** | 已经是 div + map 结构，迁移最小；`measureElement` 处理多行 log；headless 灵活控制自动滚到底部 |
| `tasks-page.table.tsx` | 原生 `<table>`，含展开子行 | **react-virtuoso** `TableVirtuoso` | 保留 table 语义；`fixedHeaderContent` 直接复用现有 thead；展开行可用 `itemSize` 通过 ResizeObserver 自动适配 |
| `audit-page.tsx` | 审计列表（需 Read 后确认是表格还是列表） | 跟上面同款 | 视具体 markup 决定 |
| `nodes-page.table.tsx` | 原生 `<table>` | **react-virtuoso** `TableVirtuoso` | 同 tasks |

**总 bundle 增量**：~24 KiB gzipped（4.4% 预算），可接受。

### 备选：全部用 @tanstack/react-virtual

如果坚持单一依赖、最小 bundle：

- 优点：5.4 KiB，TanStack 生态可信，社区最活跃
- 缺点：表格需要把 `<tbody>` 改成 `<div role="rowgroup">`（或保留 `<tbody>` 但子行变成 `<tr style={{ position: 'absolute' }}>`，sticky header 自己实现）；改造工作量更大

## 最小可行骨架（按推荐方案）

### Logs 页面（@tanstack/react-virtual）

```tsx
// web/src/pages/logs/logs-viewer.tsx
import { useVirtualizer } from '@tanstack/react-virtual';

const parentRef = useRef<HTMLDivElement>(null);
const virtualizer = useVirtualizer({
  count: filteredLogs.length,
  getScrollElement: () => parentRef.current,
  estimateSize: () => 20,         // 单行 log 大致高度
  overscan: 12,
});

return (
  <div ref={parentRef} className="terminal-surface ... h-[62vh] overflow-auto"
       role="log" aria-live="polite">
    <div style={{ height: virtualizer.getTotalSize(), position: 'relative' }}>
      {virtualizer.getVirtualItems().map((vi) => {
        const log = filteredLogs[vi.index];
        return (
          <div
            key={log.logId ?? log.id}
            ref={virtualizer.measureElement}
            data-index={vi.index}
            style={{ position: 'absolute', top: 0, left: 0, width: '100%',
                     transform: `translateY(${vi.start}px)` }}
          >
            <LogEntry log={log} hoverClass="hover:bg-white/10" />
          </div>
        );
      })}
    </div>
  </div>
);
```

### Tasks 表格（react-virtuoso TableVirtuoso）

```tsx
// web/src/pages/tasks-page.table.tsx
import { TableVirtuoso } from 'react-virtuoso';

return (
  <TableVirtuoso
    style={{ height: 'calc(100vh - 280px)' }}    // 替代 overflow-x-auto + 固定高度
    data={visibleTasks}
    components={{
      Table: (props) => (
        <table {...props} className="min-w-[1100px] text-left text-sm" />
      ),
      TableHead: React.forwardRef<HTMLTableSectionElement>((props, ref) => (
        <thead {...props} ref={ref}
          className="border-b border-border bg-muted/35 ..." />
      )),
      TableRow: (props) => (
        <tr {...props} className="border-b border-border hover:bg-muted/20" />
      ),
    }}
    fixedHeaderContent={() => (
      <tr className="...">
        <th className="w-10 px-3 py-2.5">...</th>
        {/* 其余 thead */}
      </tr>
    )}
    itemContent={(_, task) => <TaskRowCells task={task} ... />}
  />
);
```

注意：`useClientPagination` 在虚拟化后不再必要——可以渲染全量数据集。但若 API 仍分页加载，可保留 `loadMore` 回调（virtuoso 用 `endReached`，tanstack 监听 `getVirtualItems().last() === count - 1`）。

## 迁移成本估计

| 页面 | 改动 | 工作量 |
|---|---|---|
| logs-viewer | div 容器加 `parentRef`，`.map` 改成 virtualItems 渲染 | 0.5 天 |
| tasks-page.table | thead 拆到 `fixedHeaderContent`，tbody 改用 itemContent；展开子行需要测试 ResizeObserver 行为 | 1-1.5 天 |
| nodes-page.table | 同 tasks，结构更简单 | 0.5-1 天 |
| audit-page | 视 markup 决定库 | 0.5 天 |
| 移除 useClientPagination 调用 | 4 处 hook + UI 控件 | 0.5 天 |

**总计 3-4 天**。同步在 `check-bundle-budget.mjs` 跑一遍确认未超预算。

## Caveats / Not Found

- 未实测 `TableVirtuoso` 与现有 `tasks-page.table.tsx:64` 的 chain 折叠（`expandedChains`）行为是否丝滑——折叠时 row 数量瞬变，virtuoso ResizeObserver 一般能 handle，但建议实现后做手动验证。
- react-window v2 是 2025 重写版本，社区文章多停留在 v1 API。如果团队成员从网上抄代码，可能踩兼容坑。建议在 spec 里钉死："使用 react-window 时必须 v2 API（List + rowComponent）"。
- `TableVirtuoso` 不能完美保留 `min-w-[1100px]` + `overflow-x-auto` 的横向滚动：表格需要外层包一个 horizontal scroller。可在 `Scroller` component slot 自定义，需要小心 height 计算。
- 没有调研 react-virtuoso 与 framer-motion 的协作（项目已用 framer-motion）。一般 OK，但展开/折叠动画可能需要禁用以让 virtuoso 正确测量。
