# PRD: P2 — 自动刷新间隔改为响应式偏好，设置变更即时生效

> 父任务：`07-10-0710-frontend-audit-remediation`（P2 子项）
> 类型：轻量修复（PRD-only）
> 分支：`fix/p2-refresh-interval-reactive`（从最新 `main` 切出）

---

## 1. 问题与真实性
`web/src/hooks/use-console-data.ts:319` 在 effect 挂载时**只取一次** `getRefreshIntervalMs()`：

```ts
useEffect(() => {
  if (!token) return;
  const ms = getRefreshIntervalMs();   // 仅挂载时快照一次
  if (ms <= 0) return;
  const interval = setInterval(() => void loadData(), ms);
  return () => clearInterval(interval);
}, [token, loadData]);
```

用户在设置页改"自动刷新频率"后，除非 `token` / `loadData` 变化，旧 `interval` 不会重建 → 设置不生效，直到下次重新挂载。

**风险等级**：低（功能正确但响应迟钝）

---

## 2. 修复策略
让刷新间隔来自**响应式偏好值**（如经由 `use-user-preferences` 暴露的订阅值），纳入 effect 依赖，偏好变化时自动重建定时器：

```ts
const refreshIntervalMs = useUserPreferenceRefreshInterval(); // 响应式
useEffect(() => {
  if (!token || refreshIntervalMs <= 0) return;
  const id = setInterval(() => void loadData(), refreshIntervalMs);
  return () => clearInterval(id);
}, [token, loadData, refreshIntervalMs]);
```
- 需确认 `use-user-preferences.ts` 是否已提供响应式读取；若只是 `getX()` 命令式读取，则补充一个 `useUserPreference(key)` 订阅 hook（监听 `storage` 事件 / 内部状态）。
- 与 P1 同处 `use-console-data.ts` 轮询区，合并处理：门控 + 响应式间隔一起改，避免重复编辑同一 effect。

---

## 3. 验收标准
- 在设置页修改自动刷新频率后，控制台定时器按新间隔重建（无需刷新页面）。
- 关闭自动刷新（`ms<=0`）时正确停止轮询。
- 补偏好变更（`storage` 事件）驱动的测试或单测 `useUserPreference`。
- `cd web && npm run check` 通过。

---

## 4. 验证命令
```bash
cd web && npm run check
```

---

## 5. 依赖与顺序
- 与 P1 **邻近**（同处 `use-console-data.ts` 轮询区），建议同批或紧邻 PR。
- 独立验证，但为避免冲突，落地顺序放在 P1 之后或同一分支。
- 分支：`fix/p2-refresh-interval-reactive`。
