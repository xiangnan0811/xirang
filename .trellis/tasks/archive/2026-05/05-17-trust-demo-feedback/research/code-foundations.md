# Research: code foundations

- **Query**: Research existing code foundations and implementation constraints for task `.trellis/tasks/05-17-trust-demo-feedback` (Trust Demo and Feedback Funnel). Focus on README/docs, demo mode, mock data, setup wizard, onboarding, issue templates, safety/truthfulness constraints, tests/checks, and concrete gaps for trust-story demo and feedback funnel.
- **Scope**: internal
- **Date**: 2026-05-17

## Findings

### Files Found

| File Path | Description |
|---|---|
| `README.md` | Public entry point; currently describes Xirang as a lightweight SSH-based server operations platform with backup/restore, task automation, monitoring/alerts, terminal, and audit capabilities. |
| `docs/README.md` | Documentation index and public documentation maintenance rules; requires current, repo-backed, lean user-facing docs. |
| `docs/deployment.md` | Production deployment truth source: GitHub Release, official image, fixed `10761` entry port, external HTTPS, required production secrets. |
| `docs/env-vars.md` | Environment variable reference, including `VITE_ENABLE_DEMO_MODE` and production security variables. |
| `docs/admin/security.md` | Security hardening docs for required secrets, HTTPS via external proxy, SSH host key checking, SSRF protection, login protection, sensitive field encryption. |
| `docs/admin/backup-recovery.md` | Backup and restore docs, including restore drill behavior and safety constraints. |
| `docs/admin/monitoring-alerting.md` | Monitoring, anomaly, alerting, status page, and Prometheus docs. |
| `docs/admin/automation.md` | Event/action docs; explicitly notes some node events are reserved and not actively dispatched by current probe path. |
| `SECURITY.md` | Security reporting policy; instructs private reporting for vulnerabilities and production hardening recommendations. |
| `CONTRIBUTING.md` | General contribution and feedback entry; points users to GitHub Issues for bugs/features. |
| `.github/ISSUE_TEMPLATE/bug_report.md` | Generic bug report template. |
| `.github/ISSUE_TEMPLATE/feature_request.md` | Generic feature request template. |
| `.github/PULL_REQUEST_TEMPLATE.md` | PR checklist including backend/frontend checks, doc freshness, migration safety, local verification, security review. |
| `.github/workflows/ci.yml` | CI gates for PR title, backend lint/test/build/govulncheck, frontend audit/check/bundle budget, doc freshness, migration UTC safety. |
| `web/.env.example` | Frontend env example with commented `VITE_ENABLE_DEMO_MODE=true` for demo/testing. |
| `web/src/hooks/use-console-data.ts` | Central console data hook; dynamically loads mocks only in demo mode and no-token state. |
| `web/src/hooks/use-console-data.demo.ts` | Demo operation helpers, including simulated SSH connection success/failure. |
| `web/src/hooks/use-api-action.ts` | Write-operation guard; no-token demo mode allows local mutation path without API call. |
| `web/src/data/mock.ts` | Main frontend mock data: SSH keys, nodes, policies, tasks, overview, traffic, seed logs, alerts. |
| `web/src/components/setup-wizard.tsx` | Frontend setup wizard with local persistent state and best-effort `/me/onboarded` call. |
| `web/src/components/layout/app-shell.tsx` | App shell lazy-loads setup wizard. |
| `backend/internal/model/models.go` | User `Onboarded` field and sensitive-field sanitization foundations. |
| `backend/internal/api/handlers/auth_handler.go` | `/me` response includes `onboarded`; `CompleteOnboarding` updates `users.onboarded`. |
| `backend/internal/api/router.go` | Registers `GET /api/v1/me` and `POST /api/v1/me/onboarded`. |
| `web/src/context/auth-context-provider.tsx` | Frontend auth context stores token/user metadata but not `onboarded`. |
| `web/src/lib/api/auth-api.ts` | Auth API wrapper; no dedicated `/me` or onboarding wrapper found in researched file. |
| `web/src/components/backup-health-panel.tsx` | Backup confidence UI showing stale/never-backed-up nodes, healthy/degraded policies, success-rate trend and problems. |
| `backend/internal/api/handlers/overview_backup_health_handler.go` | Backup health API: stale nodes, degraded policies, 7-day trend, summary. |
| `web/src/lib/api/overview-api.ts` | Maps backup health API response to frontend summary fields such as `neverBackedUp`, `stale48h`, `successRate7d`. |
| `web/src/pages/backups-page.tsx` | Backups page includes backup health and storage panels. |
| `web/src/pages/reports-page.tsx` | SLA/SLO report UI with success rate, total/success runs, average duration, top failures. |
| `web/src/lib/api/policies-api.ts` | Frontend policy API includes `triggerDrill(token, policyId)`. |
| `backend/internal/api/handlers/policy_handler.go` | Policy restore-drill validation and trigger endpoint; validates cron, sandbox node, restore path, and source/sandbox separation. |
| `backend/internal/task/drill.go` | Restore drill execution flow: restore to sandbox, run validation script, cleanup, emit logs, update RTO/status. |
| `web/src/components/policy-editor-dialog.tsx` | Restore drill configuration and manual trigger UI. |
| `backend/internal/api/handlers/node_handler.go` | SSH connection test endpoint; updates status/latency/disk on success/failure and returns user-facing result. |
| `backend/internal/sshutil/ssh_auth.go` | SSH auth and host-key checking behavior, including strict checking, known_hosts, auto-accept handling. |
| `web/src/pages/nodes-page.state.ts` | Node page state/actions; save/test node flows call `testNodeConnection`. |
| `web/src/pages/nodes-page.table.tsx` | Node table action button for connection testing with accessible label. |
| `web/src/pages/nodes-page.grid.tsx` | Node grid action button for connection testing with accessible label. |
| `web/src/components/task-run-history.tsx` | Task run history with trigger, status, verify status, duration, throughput, and last error. |
| `web/src/pages/logs/logs-page.tsx` | Logs workbench with `task`, `node`, and `alert` tabs, URL filters, live WebSocket logs, and fetched history. |
| `web/src/pages/notifications/alert-center.tsx` | Alert center with filtering/sorting, highlighted alert deep-link, delivery records. |
| `backend/internal/api/handlers/node_logs_handler.go` | Node log query and alert log context API; alert logs use a ±5 minute window around alert trigger. |
| `backend/internal/api/handlers/task_run_handler.go` | Task run history and run-specific log APIs. |
| `.trellis/spec/guides/documentation-truth-guide.md` | Truthfulness guide for README/docs claims. |
| `.trellis/spec/frontend/quality-guidelines.md` | Frontend quality gate and testing guidelines. |
| `.trellis/spec/frontend/a11y-guidelines.md` | Accessibility requirements for icons, buttons, forms, dialogs, keyboard behavior. |
| `.trellis/spec/backend/quality-guidelines.md` | Backend quality/security testing guidelines, including sensitive-field sanitization. |
| `.trellis/spec/backend/deployment-runtime.md` | Deployment runtime contract: official image, fixed port, external TLS, deployment env behavior. |

### Code Patterns

#### README and public docs truth constraints

`README.md` currently frames Xirang as a lightweight server operations platform and lists capabilities that are backed by current code/docs. The first-screen value statement is broad rather than explicitly organized around “trusted operations”:

```markdown
轻量、易部署的服务器运维管理平台。Xirang 通过 SSH 集中管理多台服务器，提供备份、任务调度、监控告警、Web 终端和审计能力。
```

The current feature list includes backup/restore and restore drills:

```markdown
- **备份与恢复**：支持 Rsync、Restic、Rclone，多级保留策略、恢复演练、快照浏览与文件搜索。
```

`docs/README.md` provides the documentation guardrails:

```markdown
- 公开文档必须描述当前代码、配置、脚本和发布流程的真实状态。
- 过程文档、历史设计稿、任务计划、归档材料不进入公开 docs 目录。
- README 保持精简；部署、配置、排障和功能使用说明放在 docs 下。
```

`.trellis/spec/guides/documentation-truth-guide.md` reinforces that public claims must be backed by repository evidence and should not invent roadmap, release, Docker Hub, or GitHub state.

Deployment docs establish production constraints and claims that should remain consistent:

- `docs/deployment.md` lines 5-10: GitHub Release is the authoritative public release source, official image is `docker.io/linnea7171/xirang`, `latest` means latest stable, and no nightly/prerelease image channel is provided.
- `docs/deployment.md` lines 29-30: container entry port is fixed to `10761`, HTTPS/TLS is external.
- `docs/deployment.md` lines 51-57: production requires `ADMIN_INITIAL_PASSWORD`, `JWT_SECRET`, and `DATA_ENCRYPTION_KEY`.

Security docs align with those constraints:

- `docs/admin/security.md` lines 7-15: required strong random production secrets and weak/missing secret startup failure under `APP_ENV=production`.
- `SECURITY.md` instructs private vulnerability reporting and recommends `APP_ENV=production`, HTTPS, SSRF protection, and SSH host-key checking.

#### Demo mode switch and data loading

`docs/env-vars.md` documents the demo switch:

```markdown
| `VITE_ENABLE_DEMO_MODE` | string | — | 否 | 设为 `true` 启用 mock 数据（仅演示/测试用） |
```

`web/.env.example` contains the commented example:

```env
# Demo 模式（true 启用 mock 数据，仅用于演示/测试）
# VITE_ENABLE_DEMO_MODE=true
```

`web/src/hooks/use-console-data.ts` centralizes the main no-token demo behavior. It dynamically imports mock data only when needed:

```ts
// mock 数据仅在 demo 模式下动态导入，避免生产包包含 mock 代码
const loadMocks = () => import("@/data/mock");
```

The hook enables mock state only when `VITE_ENABLE_DEMO_MODE === "true"` and no token is present:

```ts
export function useConsoleData(token: string | null): ConsoleDataState {
  const demoModeEnabled = import.meta.env.VITE_ENABLE_DEMO_MODE === "true";
```

No-token demo branch fills app state from mock data:

```ts
if (!token) {
  if (demoModeEnabled) {
    const mocks = await loadMocks();
    setNodes(mocks.mockNodes);
    setPolicies(mocks.mockPolicies);
    setTasks(mocks.mockTasks);
    setAlerts(mocks.mockAlerts);
    setIntegrations(mocks.mockIntegrations);
    setSSHKeys(mocks.mockSSHKeys);
    setOverviewSummary(mocks.mockOverviewSummary);
    setWarning(null);
    setLoading(false);
    setLastSyncedAt(formatTimeOnly(new Date().toISOString()));
    return;
  }
```

`web/src/hooks/use-api-action.ts` defines the write-operation pattern: no token calls `ensureDemoWriteAllowed` and returns `null`, allowing caller-side local demo mutations; real token calls API.

```ts
if (!token) {
  ensureDemoWriteAllowed(label);
  return null;
}
```

`web/src/hooks/use-console-data.demo.ts` includes simulated SSH connection outcomes for demo mode:

```ts
const seed = (now.getSeconds() + nodeID * 17) % 10;
const ok = seed >= 2;
```

Failure returns:

```ts
result: { ok: false, message: "连接失败：SSH 握手超时或认证失败。" }
```

Success returns:

```ts
result: { ok: true, message: "SSH 握手成功，已更新磁盘探测信息。" }
```

#### Mock data shape

`web/src/data/mock.ts` is the main data foundation. It includes:

- `mockSSHKeys`
- `mockNodes`
- `mockPolicies`
- `mockTasks`
- `mockOverviewSummary`
- `buildMockOverviewTrafficSeries`
- `mockSeedLogs`
- `mockAlerts`
- `mockIntegrations`

Node names are production-like Chinese operational labels:

```ts
const nodeNames = [
  "北京主库",
  "上海热备",
  "广州归档",
  "深圳边缘",
  "杭州对象",
  "成都日志",
  "武汉中转",
  "西安仓储",
  "青岛镜像",
  "南京主站",
  "苏州容灾",
  "天津网关"
];
```

Mock nodes are generated at fleet scale and use private IP addresses:

```ts
export const mockNodes: NodeRecord[] = Array.from({ length: 36 }, (_, idx) => {
  const id = idx + 1;
  const status = nodeStatusByIndex(id);
  const usedGb = 180 + (id * 13) % 420;
  const totalGb = 800;
  const host = `10.30.${Math.floor(id / 10) + 1}.${(id * 7) % 255}`;
```

Mock tasks include mixed success/failure/retry/running/pending states:

```ts
const status: TaskRecord["status"] =
  idx % 7 === 0
    ? "failed"
    : idx % 5 === 0
      ? "retrying"
      : idx % 4 === 0
        ? "pending"
        : idx % 3 === 0
          ? "running"
          : "success";
```

Mock logs and alerts already include failure-oriented details:

```ts
message: "(node:广州归档-1) 网络抖动，进入退避重试",
errorCode: "XR-NODE-421",
```

```ts
message: "SSH 认证失败，私钥可能过期",
errorCode: "XR-AUTH-011",
severity: "critical",
status: "open",
retryable: true
```

Concrete mock-data constraints observed during research:

- No explicit named story fields or fixtures for “backup confidence”, “restore drill”, “SSH doctor”, or “incident timeline” were found.
- Mock policies were observed with `drill_enabled: false`; no obvious restore-drill success fixture was found.
- `mockSeedLogs` exists, but no-token `fetchTaskLogs` path in `use-console-data.ts` was observed returning empty logs, so seed logs may not surface through normal task-log fetch APIs in demo mode.
- `mockIntegrations` is empty.
- Mock hostnames/private IPs and labels can look like real infrastructure unless UI copy clearly marks demo/mock mode.

#### Setup wizard and onboarding

`web/src/components/setup-wizard.tsx` stores local UI state in `xirang.setup-wizard`:

```ts
const [wizardState, setWizardState] = usePersistentState<{
  completed: boolean;
  dismissed: boolean;
  currentStep: number;
}>("xirang.setup-wizard", {
  completed: false,
  dismissed: false,
  currentStep: 0,
});
```

The wizard opens after first login unless completed/dismissed:

```ts
if (!wizardState.completed && !wizardState.dismissed) {
  const timer = setTimeout(() => setShowDialog(true), 600);
  return () => clearTimeout(timer);
}
```

Wizard completion calls backend onboarding best-effort:

```ts
void request("/me/onboarded", {
  method: "POST",
  token: token ?? undefined,
}).catch(() => {
  /* best-effort */
});
```

`backend/internal/model/models.go` has persisted onboarding state:

```go
Onboarded     bool      `gorm:"not null;default:false" json:"onboarded"`
```

`backend/internal/api/handlers/auth_handler.go` includes `onboarded` in `Me` response and provides `CompleteOnboarding` to update it.

`backend/internal/api/router.go` registers:

```go
secured.GET("/me", authHandler.Me)
secured.POST("/me/onboarded", authHandler.CompleteOnboarding)
```

Frontend auth context stores token, username, role, userId, and totpEnabled, but no `onboarded` state was found in `web/src/context/auth-context-provider.tsx`. The auth API wrapper researched in `web/src/lib/api/auth-api.ts` did not expose `/me` or onboarding helper methods; setup wizard calls raw `request` directly.

Current wizard steps are generic setup steps, not an explicit trusted-ops demo journey.

#### Feedback funnel foundations

`CONTRIBUTING.md` points users to GitHub Issues for bugs and feature suggestions and asks users to search existing issues first.

Existing issue templates:

- `.github/ISSUE_TEMPLATE/bug_report.md` has generic sections: 描述, 复现步骤, 期望行为, 实际行为, 环境信息.
- `.github/ISSUE_TEMPLATE/feature_request.md` has generic sections: 需求描述, 使用场景, 期望方案, 备选方案, 补充信息.

No dedicated issue template or chooser category was found for deployment, backup/restore, SSH diagnostics, or feature suggestions as four separate feedback paths. Security-sensitive reports are routed separately through `SECURITY.md`, not public issues.

#### Backup confidence foundations

`web/src/components/backup-health-panel.tsx` presents confidence-oriented summary cards:

```tsx
<MiniStat
  label={t('backupHealth.neverBackedUp')}
  value={summary.neverBackedUp}
  tone={summary.neverBackedUp > 0 ? "destructive" : "success"}
/>
```

```tsx
<MiniStat
  label={t('backupHealth.successRate7d')}
  value={`${Math.round(summary.successRate7d)}%`}
  tone={summary.successRate7d >= 95 ? "success" : summary.successRate7d >= 80 ? "warning" : "destructive"}
/>
```

`backend/internal/api/handlers/overview_backup_health_handler.go` computes:

- stale or never-backed-up nodes
- policies where recent runs are degraded
- 7-day success/failure trend
- summary counts

`web/src/lib/api/overview-api.ts` maps backend trend into `successRate7d` and separates `neverBackedUp` from `stale48h`.

Observed demo constraint: `BackupHealthPanel` fetches only when `token` exists:

```ts
useEffect(() => {
  if (!token) return;
```

This means no-token demo mode may not display backup health data through this component without a separate mock path.

`web/src/pages/reports-page.tsx` also supports trust evidence through SLA report fields: success rate, total/success runs, average duration, top failures.

#### Restore drill foundations

`docs/admin/backup-recovery.md` documents restore drill behavior: periodically restore latest backup to an isolated sandbox node, run a validation script, and verify the backup is usable. It also documents safety measures: sandbox must not be source node, restore path must be absolute and not a system directory, and cleanup defaults to enabled.

`backend/internal/api/handlers/policy_handler.go` validates drill settings:

```go
if drillEnabled {
  if req.DrillCron == "" {
    respondBadRequest(c, "启用恢复演练后必须设置 drill_cron")
    return
  }
```

It enforces sandbox/source separation:

```go
for _, nid := range req.NodeIDs {
  if nid == *req.DrillTargetNodeID {
    respondBadRequest(c, "沙箱节点不能与备份源节点相同")
    return
  }
}
```

It validates restore path safety:

```go
if !strings.HasPrefix(path, "/") {
  return fmt.Errorf("drill_restore_path 必须是绝对路径")
}
if strings.Contains(path, "..") {
  return fmt.Errorf("drill_restore_path 不能包含 \"..\"")
}
forbidden := []string{"/", "/etc", "/usr", "/bin", "/sbin", "/boot"}
```

Manual trigger endpoint rejects policies without drill enabled:

```go
if !policy.DrillEnabled {
  respondBadRequest(c, "该策略未启用恢复演练")
  return
}
```

`backend/internal/task/drill.go` executes restore drill by checking sandbox connectivity, triggering restore, running validation script, cleaning up, and logging final status/RTO:

```go
m.emitLog(task.ID, runIDPtr, "info", fmt.Sprintf(
  "恢复演练完成: status=%s, RTO=%.1fs, 沙箱=%s",
  finalStatus, rtoSeconds, sandboxNode.Name), "")
```

`web/src/components/policy-editor-dialog.tsx` exposes restore drill configuration and manual trigger through `apiClient.triggerDrill(token, draft.id)`. `web/src/lib/api/policies-api.ts` maps this to `POST /policies/:id/drill-trigger`.

Demo constraint: this restore drill path requires authenticated API access and real backend state. The existing no-token demo mode does not appear to trigger this backend flow directly.

#### SSH diagnostics foundations

`backend/internal/api/handlers/node_handler.go` implements SSH connection testing. It builds auth, resolves host-key callback, dials SSH, updates node status/latency/disk, and returns success/failure. User-facing failure message is intentionally generic:

```go
"message": "SSH 连接失败，请检查主机地址、端口、认证配置",
```

`backend/internal/sshutil/ssh_auth.go` covers strict host-key behavior:

```go
strictHostCheck, err := util.ReadBoolEnv("SSH_STRICT_HOST_KEY_CHECKING", true)
```

When strict checking is disabled, it logs a warning and uses `ssh.InsecureIgnoreHostKey()`. When strict checking is enabled, unknown host behavior depends on `SSH_AUTO_ACCEPT_NEW_HOSTS`; disabled auto-accept returns:

```go
return fmt.Errorf("未知主机密钥被拒绝(host=%s)，当前已禁用自动接受(SSH_AUTO_ACCEPT_NEW_HOSTS=false)", hostname)
```

Frontend node pages expose a connection-test action through table/grid buttons with accessible labels. `web/src/pages/nodes-page.state.ts` calls `testNodeConnection` and shows toast success/error. In no-token demo mode, `use-console-data.demo.ts` can simulate SSH success/failure and update node state.

Constraint: existing product wording appears to be “测试连接” / connection test rather than an explicit “SSH doctor” diagnostic flow.

#### Incident timeline and explanation foundations

`web/src/components/task-run-history.tsx` shows per-task execution history with trigger type, status, verification status, duration, throughput, and last error.

`web/src/pages/logs/logs-page.tsx` has three log tabs:

```ts
type LogTab = "task" | "node" | "alert";
const LOG_TABS: LogTab[] = ["task", "node", "alert"];
```

It supports URL filters for task, node, and keyword, combines live WebSocket task logs with fetched history, and loads node/alert log panels.

`web/src/pages/notifications/alert-center.tsx` supports alert filtering/sorting, highlighting a deep-linked alert, and loading delivery records for a selected alert.

`backend/internal/api/handlers/node_logs_handler.go` provides alert log context around trigger time:

```go
WindowStart: alert.TriggeredAt.Add(-5 * time.Minute),
WindowEnd:   alert.TriggeredAt.Add(5 * time.Minute),
```

`backend/internal/api/handlers/task_run_handler.go` provides task run list and run-specific logs.

Current foundation supports assembling an incident explanation from alerts, alert deliveries, task runs, task logs, and node logs. No single integrated “incident timeline” demo story was found in the researched files.

#### Tests and checks

`.github/PULL_REQUEST_TEMPLATE.md` includes the expected local checks:

```markdown
- [ ] 后端：`cd backend && go test ./...` 通过
- [ ] 前端：`cd web && npm run check` 通过
- [ ] 文档/流程：`bash scripts/check-doc-freshness.sh` 通过或提醒已处理
- [ ] 迁移安全：涉及 migration 时 `bash scripts/check-migration-utc-safety.sh` 通过
```

`.github/workflows/ci.yml` additionally runs backend lint, coverage tests, build, govulncheck, frontend `npm audit --audit-level=moderate`, frontend `npm run check`, bundle budget, doc freshness, and migration UTC safety.

`.trellis/spec/frontend/quality-guidelines.md` states that the standard frontend gate is:

```bash
cd web && npm run check
```

It also requires tests for behavior changes in pages, hooks, API mappers, and UI primitives, and use of existing i18n helpers for user-facing strings.

`.trellis/spec/frontend/a11y-guidelines.md` calls out accessibility requirements: decorative icons `aria-hidden`, icon-only buttons with accessible names, labeled form inputs, Radix dialogs with `DialogTitle`, and keyboard/screen-reader clarity.

`.trellis/spec/backend/quality-guidelines.md` highlights sensitive-field sanitization and requires explicit tests for security-sensitive code paths such as SSH auth, path validation, encryption, RBAC, and ownership filtering.

### External References

No external web references were needed; this research used repository files and Trellis specs as the source of truth.

### Related Specs

| Spec | Description |
|---|---|
| `.trellis/spec/guides/documentation-truth-guide.md` | Public documentation must be current, repo-backed, and must not invent roadmap/release/hosted-state claims. |
| `.trellis/spec/frontend/quality-guidelines.md` | Frontend changes should use existing React/Vite/Tailwind patterns, i18n helpers, tests, and `npm run check`. |
| `.trellis/spec/frontend/a11y-guidelines.md` | Accessibility constraints for icons, buttons, forms, dialogs, keyboard and screen-reader behavior. |
| `.trellis/spec/backend/quality-guidelines.md` | Backend quality/security testing expectations and sensitive-field sanitation constraints. |
| `.trellis/spec/backend/deployment-runtime.md` | Deployment contract for official image, fixed port, external TLS, env behavior. |

## Caveats / Not Found

- No obvious README section for demo mode or a dedicated “trusted operations” walkthrough was found in the researched README excerpts.
- No public hosted demo infrastructure was found, and the PRD explicitly places public hosted demo infrastructure out of scope.
- No dedicated issue templates/categories were found for deployment, backup/restore, SSH diagnostics, and feature suggestions as separate feedback paths.
- No obvious UI banner or copy was found in the researched files that tells users demo mode is mock-only and not connected to real infrastructure.
- No explicit named mock story fixtures were found for “backup confidence”, “restore drill”, “SSH doctor”, or “incident timeline”.
- Backup health UI appears token/API-dependent and may not render in no-token demo mode without additional mock handling.
- Restore drill trigger path requires authenticated backend state and real configured sandbox/policy; no no-token mock trigger path was found.
- Incident data exists across task runs, task logs, node logs, alerts, and deliveries, but no single integrated incident-timeline demo surface was found.
- The researched files were sufficient for implementation constraints; no tests were run as part of this research task.
