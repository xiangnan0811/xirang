# Research: Wave 2 前端审查（实读一阶段）

- **Query**: 前端 a11y / API client 一致性 / WebSocket 终端 / bundle / lazy / i18n / 状态管理深度审查
- **Scope**: internal `/Users/weibo/Code/xirang/web/`
- **Date**: 2026-05-03
- **Read budget 使用**: 约 25 个文件
- **Bundle 数据来源**: `cd web && npm run build`（已实跑，2026-05-03）

---

## Findings

### F-1 [✅] command-palette 使用 `<DialogContent>` 但缺失 `DialogTitle` —— 触发 Radix 运行时警告且 SR 无标题

- **文件:行**: `/Users/weibo/Code/xirang/web/src/components/ui/command-palette.tsx:50-120`
- **实读片段**:
  ```tsx
  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogContent size="md" className="p-0 overflow-hidden">
        <Command
          label={t("search.placeholder")}
          className="..."
        >
          <div className="flex items-center gap-3 border-b border-border px-4 py-3">
            <Command.Input
              autoFocus
              ...
              placeholder={t("search.placeholder")}
            />
  ```
  对该文件 `grep -nE "DialogTitle|VisuallyHidden|sr-only"` 无任何匹配，确认没有任何标题元素。
- **问题**: Radix `DialogContent` 强制要求 `DialogTitle`，否则在 dev 模式下抛出 `Warning: DialogContent requires a DialogTitle`，并且屏幕阅读器宣读 dialog 时只读出"对话框"无任何上下文。
- **影响**: 控制台噪音（dev 时每次打开命令面板报警告）；屏幕阅读器用户无法知道弹出的是什么对话框；a11y 审计工具会标红。
- **正确修复方向**: 在 `<DialogContent>` 内部放一个 `<VisuallyHidden asChild><DialogTitle>{t("search.placeholder")}</DialogTitle></VisuallyHidden>`（或直接放可见的 DialogTitle）。
- **工作量**: S

---

### F-2 [✅] `cron-generator.tsx` 多处 i18n 键在 `en.ts` 缺失，英文 UI 显示中文回退串

- **文件:行**:
  - 用法: `/Users/weibo/Code/xirang/web/src/components/cron-generator.tsx:205,216,222,233,244,250,259,291,300,307,318,327,334,346`
  - zh.ts 定义: `/Users/weibo/Code/xirang/web/src/i18n/locales/zh.ts:1604-1611`
  - en.ts 缺失: `/Users/weibo/Code/xirang/web/src/i18n/locales/en.ts:1548-1612`
- **实读片段**（cron-generator.tsx 一段示例 + 验证）:
  ```tsx
  // line 205 起
  <span>{t('cron.every', '每隔')}</span>
  ...
  <span>{t('cron.minuteExec', '分钟执行一次')}</span>
  ...
  <span>{t('cron.hourAtMinute', '小时的第')}</span>
  <span>{t('cron.exec', '分钟执行')}</span>
  <span>{t('cron.dailyAt', '每天的')}</span>
  ...
  <span>{t('cron.customHint', '高级自定义模式下，请直接在上方输入框中编写完整的 Cron 表达式。')}</span>
  ```

  zh.ts 1604-1611（key 存在）：
  ```ts
  every: "每",
  everyMonth: "每月",
  exec: "执行",
  execute: "执行",
  dailyAt: "每天 {{time}} 执行",
  dayAt: "每月 {{day}} 日",
  hourAtMinute: "{{hour}} 时 {{minute}} 分",
  minuteExec: "第 {{minute}} 分钟执行",
  ```

  en.ts cron 段（1548-1612）只到 `weeklyDaysAtTime / dailyAtTime / estimatedNext / weekdayNames / executionFrequency / daily / weekly / monthly / custom`，无 `every / minuteExec / hourAtMinute / dailyAt / dayAt / customHint / parseResult / at / byHour / byMinute / exec / execute / everyMonth`。
- **问题**: i18next 找不到 key 时使用第二个参数作 default value——这些 default 全是中文。英文用户切到 EN 后，cron 可视化构建器内会同时混现中文和英文。
- **影响**: 真实可见的语言不一致（英文环境下显示中文）；功能可用但视觉破坏 i18n 承诺。
- **正确修复方向**: 在 en.ts `cron` 段补齐 14 个键的英文翻译；规范上禁用"内联中文 default value"模式。
- **工作量**: S

---

### F-3 [✅] settings-page tab 标签 `silences` 硬编码"静默规则"未走 i18n

- **文件:行**: `/Users/weibo/Code/xirang/web/src/pages/settings-page.tsx:66-75`
- **实读片段**:
  ```tsx
  const tabLabels: Record<TabId, string> = {
    personal: t("settings.tabs.personal"),
    account: t("settings.tabs.account"),
    users: t("settings.tabs.users"),
    channels: t("settings.tabs.channels"),
    silences: "静默规则",                          // 👈 硬编码中文
    escalation: t("escalation.tabTitle"),
    system: t("settings.tabs.system"),
    maintenance: t("settings.tabs.maintenance"),
  };
  ```
- **问题**: 英文 UI 下"silences"标签显示中文。其他兄弟 tab 全部使用 t()。
- **影响**: 真实 i18n 缺失，每个英文用户在设置页都会看到。
- **正确修复方向**: 在 `settings.tabs` 命名空间增加 `silences: "Silence rules"`（en）/ `silences: "静默规则"`（zh），改为 `t("settings.tabs.silences")`。
- **工作量**: S

---

### F-4 [✅] `escalation-policy-editor.tsx` 与 `escalation-level-row.tsx` 硬编码中文按钮 / 提示

- **文件:行**:
  - `/Users/weibo/Code/xirang/web/src/components/escalation-policy-editor.tsx:307-312`
  - `/Users/weibo/Code/xirang/web/src/components/escalation-level-row.tsx:84-86`
- **实读片段**:
  ```tsx
  // escalation-policy-editor.tsx:306-313
  <DialogFooter>
    <Button variant="outline" onClick={() => onOpenChange(false)}>
      取消
    </Button>
    <Button onClick={() => void handleSave()} disabled={!isValid || saving}>
      {saving ? "保存中…" : "保存"}
    </Button>
  </DialogFooter>
  ```

  ```tsx
  // escalation-level-row.tsx:83-86
  <label className="text-sm font-medium">{t("escalation.levels.integrations")}</label>
  {integrations.length === 0 ? (
    <p className="text-xs text-muted-foreground">暂无可用通道</p>   // 👈 硬编码
  ) : (
  ```
- **问题**: 三处硬编码中文（取消 / 保存中… / 保存 / 暂无可用通道）；同一文件内其他文案已走 i18n，明显遗漏。
- **影响**: 英文 UI 下出现混杂中文。
- **正确修复方向**: 提取到 `escalation.actions.cancel/save/saving` 与 `escalation.levels.noIntegrations`，两份 locale 同步翻译。
- **工作量**: S

---

### F-5 [⚠️] `web-terminal.tsx` token 不刷新 + WS 重连机制缺失（与 logs-socket 行为不一致）

- **文件:行**: `/Users/weibo/Code/xirang/web/src/components/web-terminal.tsx:48-158`
- **实读片段**:
  ```tsx
  const WebTerminal: FC<WebTerminalProps> = ({ nodeId, token, onDisconnect }) => {
    ...
    useEffect(() => {
      ...
      ws = new WebSocket(wsURL);
      ws.binaryType = "arraybuffer";

      ws.onopen = () => {
        ws!.send(JSON.stringify({ type: "auth", token }));   // 闭包捕获快照 token
      };

      ws.onclose = (event) => {
        const detail = event.reason ? ` (${event.code}: ${event.reason})` : ` (code: ${event.code})`;
        terminal?.write(`\r\n\x1b[31m${t("terminal.disconnected")}${detail}\x1b[0m\r\n`);
        if (active && (event.code === 1000 || event.code === 1001)) {
          onDisconnect?.();
        }
      };
      // 没有任何 reconnect / heartbeat / visibilitychange 逻辑
    ...
    }, [nodeId, token]);
  ```
  对比 `lib/ws/logs-socket.ts:13-345`：完整有指数退避（`RETRY_BASE_DELAY_MS = 2500`, `RETRY_MAX_ATTEMPTS = 20`）+ 心跳（25s）+ visibilitychange + tokenGetter 机制。
- **问题**:
  1. 终端 WS 一旦异常关闭（网络抖动 / 服务器重启）就显示红色 disconnected 错误信息，**用户必须手动关弹窗再重开**——无任何重连。
  2. token 通过 effect 闭包传入；若使用 logs-socket 那种 `tokenGetter` 模式，登录刷新 token 后终端连接重建会用旧 token 失败（实际场景：长会话 + token 过期边界）。
  3. 没有心跳；客户端 / 反向代理空闲超时会被中断关闭；没有 ping 续命。
- **影响**: 终端体验明显比日志通道差；网络抖动时 UX 倒退；运维场景下"半夜挂着 terminal 第二天回来全是错"。
- **正确修复方向**:
  - 抽出 `lib/ws/terminal-socket.ts`，复用 logs-socket 的退避/心跳/visibility 模式；
  - 接受 `tokenGetter` 而非 token 字符串；
  - WS 关闭码非 1000/1001 时尝试有限次重连（如 5 次），失败再提示并 onDisconnect。
- **工作量**: M

---

### F-6 [✅] Bundle: `index.js` 540KB(173KB gzip) + `recharts` 525KB(158KB gzip) + `web-terminal` 332KB(84KB gzip) + `framer-motion` 133KB(44KB gzip)

- **文件:行**: 来自 `cd web && npm run build` 输出（实跑）
- **实读片段**（最大几个 chunk）:
  ```
  dist/assets/dashboard-detail-page-5Wfij4Wz.js     81.59 kB │ gzip: 26.35 kB
  dist/assets/framer-motion-CjSMG-ay.js            133.75 kB │ gzip: 44.23 kB
  dist/assets/web-terminal-DNVn9PRw.js             332.58 kB │ gzip: 84.43 kB
  dist/assets/recharts-DtfWYVCe.js                 525.75 kB │ gzip:158.30 kB
  dist/assets/index-CJSrTROj.js                    540.84 kB │ gzip:173.82 kB
  ```
  recharts 用法：`grep -rn recharts src/` 显示 12 处 import，分散在 `node-metrics-chart.tsx`、`features/nodes-detail/{stat-card,trend-chart}.tsx`、`storage-usage-panel.tsx`、`backup-health-panel.tsx`、`overview-page.traffic.tsx`、`dashboards/panel-renderer.tsx`。

  framer-motion 用法：`grep -rn framer-motion src/` 仅 3 处：`components/ui/switch.tsx:3`, `components/layout/app-shell.tsx:3`, `components/ui/motion.tsx:1`。
- **问题**:
  1. `index.js` 540KB(gzip 173KB) 已超 Vercel/Lighthouse 推荐主 bundle 阈值（150KB gzip）。
  2. `recharts` 是单独 chunk 但 525KB 仍然非常大，且无 lazy 边界——只要进任何带图页（overview、dashboard、nodes-detail）就立刻拉。可以考虑把 recharts 改为按 chart 子模块导入（`recharts/lib/cartesian/Line` 等）或换更轻方案。
  3. `web-terminal` 已经 lazy 但 332KB 主要是 xterm.js + addon-fit，可接受。
  4. `framer-motion` 仅用于 switch/app-shell 两处动画，加载 133KB 投资回报极低；switch 完全可用 CSS transition 替代。
- **影响**: 首屏体积大，移动端弱网体验差；真实部署 README 内 `bundle-budget` 脚本可能已亮黄。
- **正确修复方向**:
  - 跑 `npx vite-bundle-visualizer` 看 `index` 内具体子模块；很可能含 lucide-react 全量导入或 i18next 资源。
  - `framer-motion` → 评估替换为 CSS keyframes 或 `motion-one` 轻量库（10KB）。
  - `recharts` 调研 `react-chartjs-2` 或专门按需引入。
  - 主 `index.js` 拆 vendor + i18n locale 异步加载（zh.ts/en.ts 各 ~2400 行直接同步打入 index）。
- **工作量**: L

---

### F-7 [⚠️] i18n 资源 zh.ts (2421 行) + en.ts (2412 行) 全量同步打包

- **文件:行**: `/Users/weibo/Code/xirang/web/src/i18n/index.ts:1-25`
- **实读片段**:
  ```ts
  import zh from "./locales/zh";
  import en from "./locales/en";

  i18n.use(initReactI18next).init({
    resources: {
      zh: { translation: zh },
      en: { translation: en },
    },
    lng: detectLanguage(),
    fallbackLng: "zh",
    interpolation: { escapeValue: false },
  });
  ```
  `wc -l` 确认 zh+en 共 4833 行。
- **问题**: 两份 locale 都同步导入到主 bundle。中文用户也下载 ~50KB+ 英文资源（反之亦然）。这部分占 `index.js` 540KB 的可观比例。
- **影响**: 每个用户多下载一份不需要的 locale；语言切换实际并不需要预加载另一份。
- **正确修复方向**: 用 `i18next-http-backend` 或 dynamic import：`detectLanguage() === "zh" ? import("./locales/zh") : import("./locales/en")`。次切换语言时再 lazy 拉取另一份。
- **工作量**: M

---

### F-8 [✅] API client 风格不一致：17 个模块用 `createXApi()`，8 个用裸 `export const`

- **文件**:
  - 工厂模式（17）: `alerts-api / audit-api / auth-api / batch-api / config-api / docker-api / files-api / integrations-api / node-metrics-api / nodes-api / overview-api / policies-api / reports-api / settings-api / snapshot-diff-api / snapshots-api / ssh-keys-api / storage-guide-api / system-api / task-runs-api / tasks-api / totp-api / users-api`
  - 裸函数（8）: `alert-deliveries / anomaly / dashboards / escalation / files-api（混合）/ node-logs / silences / slo`
- **实读片段**（裸函数模式）:
  ```ts
  // anomaly.ts
  export const listAnomalyEvents = (token: string, q: AnomalyListQuery = {}) =>
    request<AnomalyListResult>(`/anomaly-events${buildQuery(q)}`, { token })

  // escalation.ts
  export const listEscalationPolicies = (token: string) =>
  export const getEscalationPolicy = (token: string, id: number) =>
  export const createEscalationPolicy = (token: string, input: EscalationPolicyInput) =>
  ```

  `client.ts` 聚合（仅工厂模式）:
  ```ts
  export const apiClient = {
    ...createAuthApi(),
    ...createNodesApi(),
    ...
  };
  ```
- **问题**:
  1. `client.ts` 聚合只覆盖 17 个；裸函数模块需 consumer 单独 import，调用形态分裂为 `apiClient.getNodes(...)` vs `import { listAnomalyEvents } from "@/lib/api/anomaly"`。
  2. `files-api` 定义了 `createFilesApi()` 但没加入 `client.ts`，反而由 `nodes-page.dialogs.tsx:26` 自行 `const filesApi = createFilesApi()` —— 双重不一致。
- **影响**: 维护负担：新加 API 不知道选哪种；测试 mock 难度高（mock `apiClient` 不能拦截裸函数）；类型导出和 token 传递规则不统一。
- **正确修复方向**: 选定一种作为约定（建议 `createXApi()` 工厂统一），把 8 个裸函数模块包成工厂并加入 `client.ts`；删除 `nodes-page.dialogs.tsx:26` 那种局部 instantiate。
- **工作量**: M

---

### F-9 [⚠️] users-api 等多个工厂方法不统一支持 `signal`，缺少 abort 取消能力

- **文件:行**: `/Users/weibo/Code/xirang/web/src/lib/api/users-api.ts:20-58`
- **实读片段**:
  ```ts
  export function createUsersApi() {
    return {
      async getUsers(token: string): Promise<UserRecord[]> {                // 👈 无 signal
        const rows = (await request<UserResponse[]>("/users", { token })) ?? [];
        return rows.map((row) => mapUser(row));
      },
      async createUser(...)
      async updateUser(...)
      async deleteUser(token: string, userId: number): Promise<void> {
        await request(`/users/${userId}`, { method: "DELETE", token });
      }
    };
  }
  ```
  对比 `nodes-api.ts:92` `getNodes(token, options?: { signal?: AbortSignal })` 和 `tasks-api.ts:160` 同样支持 signal。
- **问题**: 部分 GET 不支持 abort。组件 unmount / 路由切换时 fetch 在途 → setState 落到已 unmount 组件 → React warning（也对应 use-console-data.ts:288-294 的 `loadAbortRef.current?.abort()` 设计意图）。
- **影响**: 状态泄漏 + 偶发"can't perform a React state update on an unmounted component"；快速切换页面时偶有数据被旧请求覆盖。
- **正确修复方向**: 统一所有 GET 在 RequestOptions 上接受 signal，AbortSignal 透传到 fetch；写一份测试约定。
- **工作量**: M

---

### F-10 [⚠️] `Tree.handleToggle` 直接 mutate `item.children` 破坏 React 不可变约定

- **文件:行**: `/Users/weibo/Code/xirang/web/src/components/ui/tree.tsx:154-186`
- **实读片段**:
  ```tsx
  const handleToggle = useCallback(
    async (item: TreeItemData) => {
      const willExpand = !currentExpanded.has(item.id);
      if (willExpand && onLoadChildren && (!item.children || item.children.length === 0)) {
        setLoadingIds((prev) => new Set(prev).add(item.id));
        try {
          const children = await onLoadChildren(item);
          item.children = children;             // 👈 直接 mutate 调用方传入的对象
        } finally {
  ```
- **问题**: 在受控/外部 state 模式下，调用方持有的 `items` 数组中的某节点会被悄悄 mutate；React 渲染要靠 `setLoadingIds` 触发 re-render，但 children 改变后没有通过 setState 通知；也会让 React DevTools 时间旅行/快照失真。
- **影响**: 受控用法下子节点首次加载后，可能出现"展开但儿子不显示"或反向不一致；难复现的 stale 渲染问题。
- **正确修复方向**: 改为 `setInternalChildrenCache(map)`，把 lazy load 出来的子节点保存在 component 内 `useState<Map<string, TreeItemData[]>>`，渲染时从 map 读，而不是 mutate prop。
- **工作量**: S

---

### F-11 [⚠️] `dashboard-detail-page` 在 useEffect 内 `navigate(...)` 后立即 `if (error) return null;` —— 渲染期被破坏的状态机

- **文件:行**: `/Users/weibo/Code/xirang/web/src/pages/dashboards/dashboard-detail-page.tsx:83-97`
- **实读片段**:
  ```tsx
  useEffect(() => {
    if (error) {
      const is404 =
        error.includes("404") ||
        error.toLowerCase().includes("not found") ||
        error.includes(t("dashboards.errors.notFound"));
      toast.error(is404 ? t("dashboards.errors.notFound") : error);
      navigate("/app/dashboards");
    }
  }, [error, navigate, t]);

  if (error) return null;
  if (loading || !dashboard) {
    return <DetailPageSkeleton />;
  }
  ```
- **问题**:
  1. 错误判定使用 `error.includes("404")` —— 若 i18n 后端错误信息不含字面 "404"（例如返回 envelope code / 中文 not_found）就漏判。
  2. 用 `t("dashboards.errors.notFound")` 与 error 字符串做 `includes` 比较，与翻译串严重耦合（翻译改一个字 / 切语言后判错失败）。
  3. `if (error) return null` 在 navigate 期间会渲染空白 1 帧，体验上有空白闪烁；更稳的做法是 navigate 用 `replace=true` + 立刻在第一次渲染前 short-circuit。
- **影响**: 导航到 detail 页若拿到非典型 404（例如后端返回业务错误码）会停在空白页不跳走；翻译变化导致 not-found 判定失效。
- **正确修复方向**: 改用 `apiClient` 抛出的 `ApiError.status === 404` 判断；把检测移到 `useDashboard` hook 里返回结构化的 `notFound` 标志位。
- **工作量**: S

---

### F-12 [✅] 命令面板 `Command.Item` 的 selection 路径直接 `navigate` 后 `setOpen(false)`，但 `query` 重置依赖 effect

- **文件:行**: `/Users/weibo/Code/xirang/web/src/components/ui/command-palette.tsx:35-47`
- **实读片段**:
  ```tsx
  // Reset query when closed
  React.useEffect(() => {
    if (!open) setQuery("");
  }, [open]);

  const close = React.useCallback(() => setOpen(false), [setOpen]);

  const goTo = React.useCallback(
    (path: string) => {
      navigate(path);
      close();
    },
    [navigate, close],
  );
  ```
- **问题**: 自身没什么 bug，但 `value={...}` 的 cmdk Item 包含 `node-${id}-${name}-${ip}` 会让筛选基于这个长字符串——意味着搜索 "192.168" 这种 IP 子串可命中，但在结果上不显示 IP 之外的高亮提示（cmdk 默认 fuzzy）。属于 UX 改进点，不是功能 bug。
- **影响**: 搜索结果体验略不直观（次要）。
- **正确修复方向**: 给 Item 加 `keywords={[node.ip, node.name]}` 让 cmdk 用结构化字段评分。
- **工作量**: S（标注，非必须）

---

### F-13 [❓] auth token 存储于 sessionStorage，无 httpOnly cookie 保护

- **文件:行**: `/Users/weibo/Code/xirang/web/src/context/auth-context.tsx:11-15,77-95`
- **实读片段**:
  ```ts
  const AUTH_TOKEN_KEY = "xirang-auth-token";
  ...
  const sessionToken = safeGetItem(sessionStorageRef, AUTH_TOKEN_KEY);
  ...
  if (sessionToken) {
    return {
      token: sessionToken,
      ...
    };
  }
  ```
  以及 `lib/api/core.ts:128-135` 401 时 `sessionStorage.removeItem("xirang-auth-token")` 走清理。
- **问题**: 这是架构选择不是 bug——一旦页面有任何 XSS（包括第三方依赖），token 立即可被读取。Wave 2 范围内若已确认走 JWT + sessionStorage 是设计共识，可忽略；否则建议长期评估迁移到 httpOnly cookie + CSRF token 模式。
- **影响**: XSS 一旦发生，token 失守。当前依赖严格 CSP / 输入清洗。
- **正确修复方向**: 长期目标——后端发放 httpOnly Set-Cookie，前端只在内存中持有 user 元数据；需要后端配合改造，工作量大。
- **工作量**: L（架构级，留作 long-term；非 Wave 2 必修）

---

## 总体结论

本次实读样本约 25 个文件，覆盖：UI 基础组件（dialog/dropdown/select/tree/pagination/button/confirm-dialog/command-palette）、API 客户端 core + 5 个 module、WebSocket（logs-socket vs web-terminal）、router + ProtectedRoute + AppShell、auth-context、i18n、build 产物、若干 page（settings/dashboard-detail/overview/policies/tasks）。

**真实需修复（按收益排序）**：
1. **F-1 / F-3 / F-4** —— a11y + i18n 三处低成本高收益（command-palette 缺 DialogTitle、settings tab 硬编码、escalation 编辑器硬编码）。一日内可清。
2. **F-2** —— cron-generator 14 个英文键缺失，英文用户体验破口。一日内可补。
3. **F-5** —— web-terminal 缺 reconnect / heartbeat / token refresh，与 logs-socket 体验落差大；M 级。
4. **F-8 + F-9** —— API client 工厂 vs 裸函数风格分裂、signal 不统一；M 级，建议合并为一次重构。
5. **F-6 + F-7** —— bundle 体积优化（index 540KB / locale 同步打包），L 级，需测算 ROI。
6. **F-10 / F-11** —— 中等优先级隐患（tree mutate / dashboard-detail 错误判定耦合 i18n 字符串）。S 级。
7. **F-12** —— UX 改进，可选。
8. **F-13** —— 架构议题，超出 Wave 2。

**未发现新增问题**：
- Dialog 小屏溢出、删除二次确认、logs-socket 定时器清理、batch-result-dialog setInterval 泄漏、logs-viewer 虚拟化、SSHKeyResponse.private_key 字段——均已在 Wave 0/1 加固，本轮实读复核确认无回退。
- icon-only Button 抽样多页（policies-card/tasks-grid/reports-page）均带 aria-label。
- settings-page 自定义 tab 实现键盘 ArrowLeft/Right/Home/End 完整。
- ProtectedRoute / lazy loading 结构正确，无回归。

**置信度低（需后续二阶段确认）**：F-13 安全权衡需与后端协同，超出本轮范围。
