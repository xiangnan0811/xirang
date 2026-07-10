# PRD: P1 — REST 轮询增加标签页可见性门控

> 父任务：`07-10-0710-frontend-audit-remediation`（P1 子项）
> 类型：轻量修复（PRD-only）
> 分支：`fix/p1-poll-visibility`（从最新 `main` 切出）

---

## 1. 问题与真实性
多处 30s REST 轮询未做可见性门控，标签页隐藏（后台）时仍发请求并触发外壳重渲染：
- `web/src/hooks/use-alert-bell.ts:37`（30s 轮询未判 `document.hidden`）。
- `web/src/pages/status-page.tsx:56`（30s 轮询）。
- `web/src/hooks/use-console-data.ts:317-325`（控制台自动刷新 interval）。

`ReconnectingSocket` 已处理可见性，但 REST 轮询没有。

**风险等级**：中低（后台请求浪费 / 电量 / 无效重渲染）

---

## 2. 修复策略
轮询 tick 先判 `document.hidden`，并在 `visibilitychange` 恢复时立即拉一次：

```ts
useEffect(() => {
  if (!token) return;
  const tick = () => { if (!document.hidden) void poll(); };
  const id = setInterval(tick, 30_000);
  const onVis = () => { if (!document.hidden) void poll(); };
  document.addEventListener("visibilitychange", onVis);
  void poll(); // 首拉
  return () => {
    clearInterval(id);
    document.removeEventListener("visibilitychange", onVis);
  };
}, [token]);
```
- `use-console-data.ts` 的自动刷新 interval 同样套用（注意 P2 会把它改为响应式间隔，二者同属轮询治理，改动位置相邻，建议 P1/P2 由同一分支或紧邻 PR 完成以避免冲突）。
- `status-page.tsx` 已是公开页、可能独立开多个标签页，门控收益明显。

> 也可抽一个 `useVisibilityPolling(fn, ms)` 公共 hook 复用，避免三处重复——若抽公共 hook，放到 `web/src/hooks/` 并补单测。

---

## 3. 验收标准
- 三处轮询在 `document.hidden === true` 时不发请求、不 setState。
- 标签页从隐藏恢复可见时立即触发一次拉取。
- `jsdom` 下 `document.visibilityState` 可控，补对应测试（隐藏跳过 + 恢复即拉）。
- `cd web && npm run check` 通过。

---

## 4. 验证命令
```bash
cd web && npm run check
```

---

## 5. 依赖与顺序
- 与 A3 **并行独立**；与 P2 **邻近**，建议同批或紧邻处理（避免 `use-console-data.ts` 轮询代码冲突）。
- 分支：`fix/p1-poll-visibility`。
