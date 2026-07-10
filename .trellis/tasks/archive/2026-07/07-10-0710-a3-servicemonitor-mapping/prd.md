# PRD: A3 — `service-monitors` 域补齐 snake→camel 映射器与域类型

> 父任务：`07-10-0710-frontend-audit-remediation`（A3 子项）
> 类型：常规修复（建议补 design 说明映射边界；本 PRD 已含设计要点）
> 分支：`fix/a3-servicemonitor-mapping`（从最新 `main` 切出）

---

## 1. 问题与真实性
项目强约定：**API 边界必须用 `map*` 把 snake_case 映射为 camelCase，组件内禁止出现 raw snake_case**（`AGENTS.md` 反模式清单、`.trellis/spec/frontend/type-safety.md`）。

但 `service-monitors` 是较新域，完全绕过：
- `web/src/types/domain.ts:1039+` 的 `ServiceMonitor` / `NewServiceMonitorInput` / `StatusPageItem` 直接保留 `interval_seconds`、`http_method`、`http_headers`、`http_expected_status`、`uptime_pct`、`last_checked_at` 等 raw 字段（`http_headers` 甚至以 JSON 字符串形态进域类型）。
- `web/src/lib/api/service-monitors.ts` 无映射，直接 `request<ServiceMonitor[]>`。
- `web/src/pages/service-monitors-page.tsx` 直接消费 `editingMonitor.interval_seconds`、`http_method`、`http_headers`，并含 `setType(editingMonitor.type as "http" | "tcp")` 不安全断言；`web/src/pages/status-page.tsx` 消费 `item.uptime_pct`、`item.last_checked_at`。

**后果**：后端字段重命名会直接击穿前端；组件内散落解析逻辑与 `as` 断言，类型安全退化。

**风险等级**：中（一致性 / 类型安全 / 后端契约变更抗性）

---

## 2. 修复策略（映射边界设计）

### 2.1 域类型改为 camelCase
```ts
export interface ServiceMonitor {
  id: number;
  name: string;
  description: string;
  type: "http" | "tcp";
  target: string;
  intervalSeconds: number;
  timeoutSeconds: number;
  httpMethod: "GET" | "POST" | "HEAD";
  httpExpectedStatus: number;
  httpHeaders: Record<string, string>; // 已解析为对象，非 JSON 字符串
  enabled: boolean;
  lastStatus: "up" | "down" | "unknown";
  uptimePct: number;
  lastCheckedAt: string | null;
  createdAt: string;
  updatedAt: string;
}
```

### 2.2 API 边界映射器（`lib/api/service-monitors.ts` 内或独立 `map*`）
```ts
type RawServiceMonitor = Omit<ServiceMonitor, ...> & { /* raw snake_case */ };

const HTTP_METHODS = ["GET", "POST", "HEAD"] as const;
function normalizeHttpMethod(v: string): ServiceMonitor["httpMethod"] {
  return (HTTP_METHODS as readonly string[]).includes(v) ? (v as ...) : "GET";
}
function safeParseHeaders(raw: string | undefined): Record<string, string> {
  if (!raw || raw === "{}") return {};
  try {
    const obj = JSON.parse(raw);
    return obj && typeof obj === "object" ? Object.fromEntries(
      Object.entries(obj).map(([k, v]) => [k, String(v)])
    ) : {};
  } catch { return {}; }
}
function mapServiceMonitor(raw: RawServiceMonitor): ServiceMonitor { /* 逐字段映射 */ }
async list(token, signal?) {
  const raw = await request<RawServiceMonitor[]>(BASE_PATH, { token, signal });
  return (raw ?? []).map(mapServiceMonitor);
}
// get / create / update 同样在边界产出/消费 camelCase
```
- `NewServiceMonitorInput`（出站）同样 camelCase；请求体由映射器负责转回后端需要的形态（或保持 camelCase 出站、由后端统一——按既有约定，出站应在前端映射为后端形态；需对齐现有其它域的 `mapXxxInput` 模式）。
- `StatusPageItem` 同步映射（见 `status-page.tsx` 使用的 `uptime_pct`/`last_checked_at`）。

### 2.3 组件消费 camelCase
`service-monitors-page.tsx` / `status-page.tsx` 删除 `parseHeaders`/`String(v)`/`as "http"|"tcp"` 等散落逻辑，直接读 `intervalSeconds`、`httpMethod`、`httpHeaders` 等；表单 state 直接用 camelCase 字段。

---

## 3. 验收标准
- `domain.ts` 的 `ServiceMonitor` / `StatusPageItem` 无 raw `interval_seconds` / `http_method` / `http_headers` / `uptime_pct` / `last_checked_at`。
- `lib/api/service-monitors.ts` 在边界完成映射（入站 snake→camel，出站 camel→后端形态）。
- 组件内无任何 raw snake_case 字段访问，无新增 `as` 不安全断言。
- 新增映射器单测（正常 / 缺省 / 非法 method / 坏 JSON headers / 空 headers）。
- 现有 `service-monitors-page.test.tsx`、`status-page` 相关测试通过（按 camelCase 调整。
- `cd web && npm run check` 通过。

---

## 4. 验证命令
```bash
cd web && npm run check
```

---

## 5. 依赖与顺序
- 与 P1 / P2 **并行独立**。
- 可顺带清理 Q1（`automation-rules-page.tsx:260,265` 的 `(t as any)(...)` 改为窄化 `tList` 辅助）。
- 分支：`fix/a3-servicemonitor-mapping`。
- **spec 回写**：在 `frontend/type-safety.md` 或 `frontend/index.md` 显式补充"所有 API 域（含新增）必须在 `lib/api/*` 边界映射，组件与 `types/domain.ts` 只用 camelCase"规则，防止回归。
