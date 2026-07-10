# PRD: A2 — 稳定 `useConsoleData.refresh`（useCallback）恢复 SharedContext 渲染隔离

> 父任务：`07-10-0710-frontend-audit-remediation`（A2 子项）
> 类型：轻量修复（PRD-only 足够，无需 design/implement）
> 分支：`fix/a2-refresh-memoization`（从最新 `main` 切出）

---

## 1. 问题与真实性
`web/src/hooks/use-console-data.ts:474` 的 `refresh` 在 `return {}` 内是**每次渲染新建的箭头函数**：

```ts
return {
  // ...
  refresh: () => {
    setRefreshVersion((current) => current + 1);
    void loadData();
  },
  // ...
};
```

而 `web/src/components/layout/app-shell.tsx:84-95` 把它作为 `sharedContextValue` 的 `useMemo` 依赖：

```ts
const sharedContextValue = useMemo(() => ({ ... refresh: consoleData.refresh, ... }),
  [..., consoleData.refresh, ...]);
```

由于 `refresh` 引用每次都变，`useMemo` 每渲染都失效 → `SharedContext.Provider` 的 value 每渲染都是新对象 → 所有 `useShared()` 消费者随 `AppShellInner` 每次渲染（含 30s 自动刷新的 `loading/lastSyncedAt` 变更）无谓重渲染。其余 6 个 context 的操作函数均已 `useCallback`，独此一处破坏拆分。

**风险等级**：中（性能放大，改动极小、零行为风险）

---

## 2. 修复策略
把 `refresh` 提升为 `useCallback`，纳入 `loadData` 依赖（已是 `useCallback`），消除每次新建：

```ts
const refresh = useCallback(() => {
  setRefreshVersion((current) => current + 1);
  void loadData();
}, [loadData]);
// return 中直接透传：refresh,
```

`loadData` 本身稳定（依赖 `[token, demoModeEnabled]`），故 `refresh` 稳定 → `sharedContextValue` 仅在真正相关状态变化时重算。

---

## 3. 验收标准
- `use-console-data.ts` 中 `refresh` 经 `useCallback` 定义，`return` 透传（不再内联箭头）。
- `SharedContext` 消费者的重渲染仅由 `sharedContextValue` 依赖项变化触发。
- 现有 `web/src/components/layout/app-shell.test.tsx` 通过（其已 mock `refresh: vi.fn()`）。
- `cd web && npm run check`（typecheck + lint + test + build）通过。
- 无新增 `any` / 不安全断言。

---

## 4. 验证命令
```bash
cd web && npm run check
```

---

## 5. 依赖与顺序
- 建议**首个落地**（为后续 A1 拆分建立渲染隔离基线）。
- 无跨子任务强依赖。
- 分支：`fix/a2-refresh-memoization`。
