# Wave 3 前端架构收敛 - 实施设计

- Query: 为 5 项 P3 finding 产出可执行设计
- Scope: internal（实读 + 设计，不做新审查）
- Date: 2026-05-05

---

## F-5 web-terminal lib/ws 抽象化

### 当前状态

`web/src/components/web-terminal.tsx` 全文 169 行，`useEffect` 内手写 WS 生命周期。**无 reconnect / 无 heartbeat / 无 token refresh**：

```ts
// L84-107（精简）
ws = new WebSocket(wsURL);
ws.onopen = () => { ws.send(JSON.stringify({ type: "auth", token })); };
ws.onmessage = (event) => { /* 写入 xterm */ };
ws.onclose = (event) => {
  terminal?.write(`\r\n\x1b[31m${t("terminal.disconnected")}${detail}\x1b[0m\r\n`);
  if (active && (event.code === 1000 || event.code === 1001)) {
    onDisconnect?.();   // 仅正常关闭主动关闭弹窗，异常断开纯打印不重连
  }
};
ws.onerror = () => { terminal?.write(`\r\n\x1b[31m${t("terminal.wsError")}\x1b[0m\r\n`); };
```

`web/src/lib/ws/logs-socket.ts`（352 行）已完整实现 D8 方案：

```ts
const RETRY_BASE_DELAY_MS = 2500;       // L13
const RETRY_MAX_DELAY_MS = 30_000;       // L14
const RETRY_MAX_ATTEMPTS = 20;           // L15
const HEARTBEAT_INTERVAL_MS = 25_000;    // L16

// L266-279 退避 + jitter
private scheduleReconnect() {
  if (this.reconnectAttempts >= RETRY_MAX_ATTEMPTS) return;
  const delay = Math.min(
    RETRY_BASE_DELAY_MS * Math.pow(2, this.reconnectAttempts),
    RETRY_MAX_DELAY_MS
  ) * (0.5 + Math.random() * 0.5);
  this.reconnectAttempts += 1;
  this.reconnectTimer = window.setTimeout(() => this.open(), delay);
}

// L299-310 heartbeat
private startHeartbeat() {
  this.heartbeatTimer = window.setInterval(() => {
    if (this.socket?.readyState === WebSocket.OPEN) {
      this.socket.send(JSON.stringify({ type: "ping" }));
    }
  }, HEARTBEAT_INTERVAL_MS);
}

// L289-297 token getter
private currentToken(): string {
  if (this.tokenGetter) {
    const fresh = this.tokenGetter();
    if (fresh) this.token = fresh;
  }
  return this.token;
}
```

后端 `terminal_handler.go` L168 `SetReadDeadline(... 5 second)` 等待 auth；认证后 SSH PTY 是状态化连接 — **重连后无法复用旧 SSH session**。

### 设计方案

新增 `web/src/lib/ws/reconnecting-socket.ts`（约 220 行）：

```ts
export type ReconnectingSocketOptions = {
  url: string | (() => string);          // 支持每次重连重算 URL（带 query）
  protocols?: string | string[];
  binaryType?: BinaryType;                // "arraybuffer" for terminal
  authMessage?: () => unknown;            // open 后发送（如 {type:"auth",token}）
  heartbeatMessage?: () => unknown;       // 默认 {type:"ping"}
  tokenGetter?: () => string | null;      // 重连前刷新 token
  onMessage: (data: ArrayBuffer | string) => void;
  onStatusChange?: (connected: boolean) => void;
  onReconnectStart?: () => void;          // 用于 web-terminal 显示提示
  onGiveUp?: () => void;
  retry?: {
    baseMs?: number;        // 默认 2500
    maxMs?: number;         // 默认 30_000
    maxAttempts?: number;   // 默认 20
    jitter?: boolean;       // 默认 true
  };
  heartbeatIntervalMs?: number;           // 默认 25_000
};

export class ReconnectingSocket {
  connect(): void;
  send(data: string | ArrayBufferLike | ArrayBufferView): boolean;
  close(code?: number, reason?: string): void;
  isOpen(): boolean;
  isGivingUp(): boolean;
}
```

行为：
- reconnect 使用 logs-socket 同款指数退避 + jitter（同常量）
- heartbeat：每 25s 发 ping（消息体可定制；terminal 可发空字节或忽略）
- token refresh：每次 reconnect 前调用 `tokenGetter()` 取最新 token，再放入 authMessage
- visibility：标签页回前台触发立即重连（与 logs-socket L321-337 一致）
- 不解析消息内容（与 logs-socket 不同 — 那里耦合了 LogEvent normalize）

`web-terminal.tsx` 改造（约 -40 / +60 行）：
- 删除手写 `new WebSocket(...)` + 三个 handler
- 用 `new ReconnectingSocket({ url, authMessage, tokenGetter, binaryType: "arraybuffer", onMessage, onReconnectStart, onStatusChange })`
- `onReconnectStart`：清屏（`terminal.clear()`）+ 写入 "会话已重连，请重新登录"（i18n key `terminal.reconnected`，新增）
- `onStatusChange(false)`：保持现有"disconnected"提示
- `onGiveUp`：写入 "已停止重连，请关闭终端" + 调用 `onDisconnect?.()`
- 关键约束：xterm.js 实例不重建（仅 `clear`），事件 `onData` 仍指向 socket.send；resize 逻辑不变
- 接受 token refresh 接口由父组件传入（从 AuthContext）

`logs-socket.ts` 改造（约 -120 / +30 行）：
- 保留 `LogsSocketClient` 类型不变（外部 API 兼容）
- 内部把 reconnect / heartbeat / candidate 切换 / visibility 委托给 `ReconnectingSocket`
- 仅保留 LogEvent normalize 与 candidate URL 列表逻辑（D8 多 fallback URL 是 logs 独有）
- 风险：candidate URL 切换不在通用 base 内 → 设计为 `url: () => candidates[idx]` 的回调形式让上层控制

### 影响文件
- 新增：`web/src/lib/ws/reconnecting-socket.ts` + 同名 `.test.ts`
- 修改：`web-terminal.tsx`、`logs-socket.ts`
- 新增 i18n key：`terminal.reconnected`、`terminal.reconnecting`、`terminal.giveUp`
- 测试用例：≥6（base reconnect / heartbeat ping / token refresh / max attempts / visibility resume / manual close 不重连）

---

## F-8 + F-9 API client 统一为工厂模式 + AbortSignal

### 当前状态

**Wave 2 描述需修正**：实读后裸函数文件不是 8 个而是 **7 个**，且 `files-api.ts` 已是工厂模式 + signal 全覆盖（不存在"内部双重不一致"，Wave 2 finding 描述有误）。

裸函数（`export const xxx = (token, ...) =>`）共 7 个文件：
- `dashboards.ts`（13 个 endpoint，仅 `getDashboard` / `queryPanel` 支持 signal）
- `silences.ts`（3 个）
- `slo.ts`（5 个，无一支持 signal）
- `anomaly.ts`（2 个，无一支持 signal）
- `escalation.ts`（6 个，无一支持 signal）
- `node-logs.ts`（5 个，无一支持 signal）
- `alert-deliveries.ts`（1 个）

工厂模式（17 个）：alerts / audit / auth / batch / config / docker / files / integrations / node-metrics / nodes / overview / policies / settings / snapshot-diff / snapshots / ssh-keys / storage-guide / system / task-runs / tasks / totp / users — 其中 `system-api.ts` 签名是 `(token, signal?)` **裸 signal 而非 options 对象**（L31/35/43），与其他工厂的 `options?: { signal? }` 不一致。

工厂典型样式（policies-api.ts L51-53）：
```ts
async getPolicies(token: string, options?: { signal?: AbortSignal }): Promise<PolicyRecord[]> {
  const rows = (await request<PolicyResponse[]>("/policies", { token, signal: options?.signal })) ?? [];
  return rows.map((row) => mapPolicy(row));
},
```

### 设计方案

**标准签名**：
- 写操作：`method(token: string, ...args): Promise<T>`
- 读操作：`method(token: string, ...args, options?: { signal?: AbortSignal }): Promise<T>`
- 抛弃 `system-api` 的 `(token, signal?)` 裸 signal 风格，统一为 options

**转换清单**：

| 文件 | 当前风格 | 涉及函数 | 新增 signal endpoint |
|---|---|---|---|
| dashboards.ts | 裸函数 | listDashboards / getDashboard / createDashboard / updateDashboard / deleteDashboard / addPanel / updatePanel / deletePanel / updateLayout / queryPanel / listMetrics | listDashboards / listMetrics（GET 类） |
| silences.ts | 裸函数 | listSilences / createSilence / deleteSilence | listSilences |
| slo.ts | 裸函数 | listSLOs / createSLO / updateSLO / deleteSLO / getSLOCompliance / getSLOSummary | listSLOs / getSLOCompliance / getSLOSummary |
| anomaly.ts | 裸函数 | listAnomalyEvents / listNodeAnomalyEvents | 全部 |
| escalation.ts | 裸函数 | list/get/create/update/deleteEscalationPolicy / listAlertEscalationEvents | listEscalationPolicies / getEscalationPolicy / listAlertEscalationEvents |
| node-logs.ts | 裸函数 | queryNodeLogs / getAlertLogs / getNodeLogConfig / getLogsSettings / updateNodeLogConfig / updateLogsSettings | queryNodeLogs / getAlertLogs / getNodeLogConfig / getLogsSettings |
| alert-deliveries.ts | 裸函数 | retryDelivery | (无 GET) |
| system-api.ts | 工厂但 signal 风格不一致 | getVersion / checkVersion / listBackups | 改签名为 options |

**转换示例（dashboards.ts 改造前后）**：
```ts
// 改造后
export function createDashboardsApi() {
  return {
    async listDashboards(token: string, options?: { signal?: AbortSignal }) {
      return request<Dashboard[]>("/dashboards", { token, signal: options?.signal });
    },
    async getDashboard(token: string, id: number, options?: { signal?: AbortSignal }) {
      return request<Dashboard>(`/dashboards/${id}`, { token, signal: options?.signal });
    },
    // ...
  };
}
```

**`client.ts`** 增加 `...createDashboardsApi()`、`...createSilencesApi()` 等 7 个；删除 7 个文件的 named export 改为通过 `apiClient.listDashboards(...)` 形式调用。

**调用方迁移**：约 23 个 `import { listDashboards } from "@/lib/api/dashboards"` 改为 `apiClient.listDashboards`。`use-dashboard.ts` L114 等需把 `getDashboard(token, id, signal)` 改为 `apiClient.getDashboard(token, id, { signal })`。

**测试影响**：`use-dashboard.test.ts` / `dashboards-page.test.ts` 等用 `vi.mock("@/lib/api/dashboards")` 的测试需改成 mock `@/lib/api/client`。

### 影响文件
- 修改 7 个 api 文件 + `client.ts` + `system-api.ts` 签名统一
- 调用方约 23 个文件
- 测试约 6 个文件
- 估计代码净变化 +200 / -150 行（多了一个工厂壳层）

### 与 F-9 合并的好处
F-9 单独做需要逐个增加 signal 参数；与 F-8 一起做时，工厂改造同步把 signal 加上，减少二次修改

---

## F-10 Tree expand 状态外提

### 当前状态

`web/src/components/ui/tree.tsx` L154-186：

```ts
const handleToggle = useCallback(
  async (item: TreeItemData) => {
    const willExpand = !currentExpanded.has(item.id);

    if (willExpand && onLoadChildren && (!item.children || item.children.length === 0)) {
      setLoadingIds((prev) => new Set(prev).add(item.id));
      try {
        const children = await onLoadChildren(item);
        item.children = children;       // ⚠️ L162 直接 mutate props
      } finally {
        setLoadingIds((prev) => { /* immutable */ });
      }
    }

    if (!isControlled) {
      setInternalExpanded((prev) => {
        const next = new Set(prev);
        if (willExpand) next.add(item.id); else next.delete(item.id);
        return next;
      });
    }
    onToggle?.(item);
  },
  [isControlled, currentExpanded, onLoadChildren, onToggle]
);
```

仅 `item.children = children` 这一行是 mutate；其余 `setInternalExpanded` 已经是 immutable。

**调用方实读**：`grep "ui/tree\|<Tree\b\|TreeItemData"` 在 `src/` 下**无任何业务消费方**（仅 `tree.tsx` 自身），即此组件目前未被实际使用，重构兼容风险接近零。

### 设计方案

引入 `childrenMap: Map<string, TreeItemData[]>` 由 Tree 组件管理懒加载结果：

```ts
// Tree 组件内部
const [childrenMap, setChildrenMap] = useState<Map<string, TreeItemData[]>>(new Map());

const handleToggle = useCallback(async (item) => {
  const willExpand = !currentExpanded.has(item.id);
  const cached = childrenMap.get(item.id);
  const needsLoad = willExpand && onLoadChildren
    && (!item.children || item.children.length === 0)
    && !cached;

  if (needsLoad) {
    setLoadingIds(prev => new Set(prev).add(item.id));
    try {
      const children = await onLoadChildren(item);
      setChildrenMap(prev => new Map(prev).set(item.id, children));   // ✅ immutable
    } finally {
      setLoadingIds(prev => { const n = new Set(prev); n.delete(item.id); return n; });
    }
  }
  // ... setInternalExpanded 不变
}, [...]);
```

`TreeItem` 渲染时优先取 `childrenMap.get(item.id) ?? item.children`：

```ts
// TreeItem 内
const children = childrenMap?.get(item.id) ?? item.children;
{hasChildren && isExpanded && children && children.length > 0 && (
  <div role="group">{children.map(...)}</div>
)}
```

需把 `childrenMap` 通过 props 传入 `TreeItem`。

### 影响文件
- `web/src/components/ui/tree.tsx`：约 +20 / -2 行
- 调用方：无（无业务消费）→ 风险极低
- 测试：建议补 1 个测试用例（懒加载后再次折叠展开应使用缓存）

---

## F-11 dashboard 错误码而非字符串匹配

### 当前状态

`web/src/pages/dashboards/dashboard-detail-page.tsx` L82-92：

```ts
useEffect(() => {
  if (error) {
    const is404 =
      error.includes("404") ||
      error.toLowerCase().includes("not found") ||
      error.includes(t("dashboards.errors.notFound"));   // ⚠️ i18n 字符串匹配
    toast.error(is404 ? t("dashboards.errors.notFound") : error);
    navigate("/app/dashboards");
  }
}, [error, navigate, t]);
```

`error` 来自 `use-dashboard.ts` L130-135：
```ts
.catch((err: unknown) => {
  const msg = err instanceof Error ? err.message : "加载失败";
  setError(msg);   // 丢失了 ApiError.status！
})
```

后端 `dashboard_handler.go` L50-51 已经返回标准 404：
```go
if errors.Is(err, dashboards.ErrNotFound) {
    respondNotFound(c, "看板不存在")    // → {code:404, message:"看板不存在"}
}
```

`web/src/lib/api/core.ts` L6-16 `ApiError` 已带 `status: number` 字段。

**结论**：后端不需要改；只需让前端别把 `ApiError` 降级成字符串。

### 设计方案

**方案 A（推荐，工作量极小）**：use-dashboard.ts 保留原始 `ApiError`，detail-page 用 `err.status === 404` 判断。

```ts
// use-dashboard.ts 改造
const [error, setError] = useState<ApiError | Error | null>(null);
// catch 时直接 setError(err as Error/ApiError)

// dashboard-detail-page.tsx 改造
useEffect(() => {
  if (error) {
    const is404 = error instanceof ApiError && error.status === 404;
    toast.error(is404 ? t("dashboards.errors.notFound") : (error.message || t("common.requestFailed")));
    navigate("/app/dashboards");
  }
}, [error, navigate, t]);
```

**方案 B（结构化错误码，未来扩展）**：后端 `Response` 增加 `error_code` 字段（如 `DASHBOARD_NOT_FOUND`）。当前后端 `respondNotFound` 没有 error_code 概念，加这个要改 `response.go` + 所有 handler，工作量 M-L 且收益不明显（404 已是 HTTP 标准）。

**采纳方案 A**。

### 影响文件
- `use-dashboard.ts`：3-5 行改动（error 类型放宽到 `Error`，去掉 message 提取）
- `dashboard-detail-page.tsx`：3-5 行改动（用 `instanceof ApiError && status === 404`）
- 测试：`use-dashboard.test.ts` mock `getDashboard` reject 的应换成 `new ApiError(404, ...)`

---

## 总体 PR 拆分建议

| PR | 内容 | 工作量 | 顺序 | 备注 |
|---|---|---|---|---|
| PR-A | F-10 Tree mutate | S（~30 行 + 1 test） | 第 1 | 无业务依赖，独立先做 |
| PR-B | F-11 dashboard 404 判断 | S（~10 行 + test 调整） | 第 2 | 仅前端，无后端依赖 |
| PR-C | F-8 + F-9 API client 工厂统一 + AbortSignal | M-L（~400 行 + 23 调用方 + 6 测试） | 第 3 | 体量最大；建议拆成 2 个子 PR：(1) 转工厂 + client.ts 装配，(2) 调用方迁移 |
| PR-D | F-5 web-terminal lib/ws 抽象 | M（~250 行新文件 + 改造 + 6 测试 + 3 i18n key） | 第 4 | logs-socket 兼容性需小心；建议先合 ReconnectingSocket + web-terminal，logs-socket 内部改造单独二阶段 |

---

## 实读后对 Wave 2 finding 描述的修正

1. **F-8** 描述中 "files-api.ts 内部双重不一致" 不成立 — 实读 files-api.ts 完全是工厂 + options.signal 一致风格。建议 finding 修正为 "system-api.ts 内部 signal 风格不一致（裸 signal 而非 options 对象）"。
2. **F-8** 文件计数 "8 裸函数" 应为 7 个（dashboards/silences/slo/anomaly/escalation/node-logs/alert-deliveries），files-api 不在内。
3. **F-10** Tree 当前**没有业务调用方**，重构无外部兼容压力。
4. **F-11** 后端已经返回 HTTP 404 + `{code:404}` 结构化响应；ApiError.status 也已存在 — 是前端 use-dashboard 把 ApiError 降级成 string 才被迫做字符串匹配，不需要后端配合。

---

## Caveats / 重点风险

- **PR-D 风险点**：`logs-socket.ts` 有 candidate URL 切换 + visibility 监听等独有逻辑，迁移到通用 `ReconnectingSocket` 时需保留 candidate 列表轮询（建议 `url: () => candidates[idx]` 回调形式），否则会破坏 D8 已有行为。
- **PR-C 测试 mock 链**：`vi.mock("@/lib/api/dashboards")` 改为 `vi.mock("@/lib/api/client")` 时，`apiClient` 是命名空间对象而非命名导出，mock 写法不同（需用 `vi.spyOn(apiClient, "listDashboards")`）。
- **xterm.js reconnect**：实读后端确认 SSH PTY 是状态化连接（terminal_handler.go L168 单次握手），重连后用户必须重新登录。`onReconnectStart` 必须做 `terminal.clear()` + 显式提示，否则用户会以为输入丢失。
