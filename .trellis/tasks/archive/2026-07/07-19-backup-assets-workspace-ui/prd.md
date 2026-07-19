# 备份资产工作区 UI

## Status

```text
phase:       implementation verified; delivery pending
task:        .trellis/tasks/07-19-backup-assets-workspace-ui
status:      in_progress
plan review: approved by user on 2026-07-19
scope path:  buildable frontend core + truthful unavailable states
branch:      codex/backup-assets-workspace-ui
base:        main @ b744b116c6a11ef02998d6182d372e9efe97abc2
worktree:    /home/murray/.codex/worktrees/bb05/xirang
start:       executed on 2026-07-19
implementation authorization: approved by user on 2026-07-19
implementation: executed
focused/full/browser verification: executed and passing
delivery:    pending
```

This PRD is the approved Child 9 implementation contract. The user separately
authorized implementation on 2026-07-19. Product implementation, local
verification, exact staging, and the work commit are complete; archive/journal/
PR/CI/merge/post-merge steps remain gated by their actual execution.

## Goal

在现有 Xirang `Backups` 单一入口内建立安静、密集、可重复使用的备份资产调查工作台：用户可以在服务端授权和真实能力投影范围内选择仓库、任务、恢复点与目录，浏览/搜索资产，进行核心安全预览，查看元数据、证据和精确两点差异，并管理当前 API 真正支持的用户覆盖。

Child 9 只交付 frontend workspace、route state、core explorer/preview、overlays、evidence/diff、legacy compatibility、i18n、a11y 与视觉验收。它不启用功能、不改变 Provider 或数据库事实，也不伪造后续 Child 的 Worker、Derived、export、controlled recovery、retention 或 GA 能力。

## User Value

- 运维人员在一次调查流程内保持仓库、恢复点、目录、筛选、选择、滚动和检查器上下文，不需要反复打开 Tasks Dialog。
- 管理员能区分“备份事实”“内容可用性”“搜索覆盖”“安全分类”“校验/演练证据”，避免把离线、部分覆盖或缺少 Worker 误判为损坏。
- 深链接只携带 opaque IDs 和非敏感展示状态；原始查询、路径、文件名、票据、proof、理由和批量 ID 不泄露到浏览器历史或存储。
- 桌面和窄屏保持同一语义模型：桌面三栏扫描，移动端按上下文选择、列表/网格、全屏检查器连续推进并可逆返回。

## Users And Jobs

### Admin

- 浏览全部服务端授权的 Repository/RecoveryPoint/Catalog。
- 查看真实 Provider、版本模式、不可变性、内容和证据状态。
- 在精确 action purpose 下执行真实可用的敏感揭示或下载；不因角色名在客户端猜测权限。
- 保留 Tasks 中的配置、调度、执行、日志与 legacy 恢复入口。

### Operator

- 只浏览服务端 ownership projection 返回的 producing lineage。
- 搜索当前或保留历史范围，正确理解 coverage/staleness 和 grouped hit。
- 使用真实允许的收藏、标签、保存搜索与最近访问能力。

### Viewer

- 不通过客户端降级或缺失身份被当作 Admin/Operator。
- 资产 API 拒绝或未返回 `list` 权限时，工作区失败关闭且不泄露存在性、名称、数量或状态。

## Requirements

### R1. Route And Navigation Contract

- `/app/backups` 使用 replace redirect 到 `/app/backups/overview`。
- `/app/backups/overview` 原样保留现有可信度、健康、存储和配置引导。
- `/app/backups/data` 承载资产浏览、搜索和 `view=repositories` 的 data 内视图。
- `/app/backups/recovery` 只承载当前真实恢复点演练/证据上下文、legacy Tasks 兼容入口和未来安全 shell，不创建 Child 13 的计划/作业/结果能力。
- 侧栏保持一个 Backups 项；不新增“备份资产”或 Repository 一级入口。
- Data route state 是 closed、canonical、reversible 的纯函数合同。允许字段仅为 `view`、`repositoryId`、正整数 `taskId`、`recoveryPointId`、opaque `parentEntryId/entryId`、opaque `savedSearchId`、`current|all_retained` scope、非敏感 type/tag/favorite、真实支持的 sort/direction、`list|grid` layout 和 closed `inspectorTab`。
- 修改一个字段保留所有无关且仍合法的字段。未知 key/value、ID 格式错误、重复冲突、unsupported sort pair 或 coupled state 错误整体返回 invalid，并 replace 到无敏感残留的安全 canonical route。
- 原始 query AST/text、path/name、cursor、selection/AssetRef 集合、ticket/content URL/cookie、proof、reason、idempotency key、label、raw error 不进入 URL、localStorage、sessionStorage 或 `history.state`。需深链接的查询只保存为服务端 opaque `savedSearchId`。

### R2. Responsive Workspace

- Desktop 使用稳定三栏：左栏为 Repository/Task/RP/directory/saved/favorite/tag/recent 上下文；中栏为 cursor + virtualized list/grid/search/filter/sort/selection；右栏为 unframed inspector。
- 三栏使用稳定 grid tracks、边界和最小/最大宽度；toolbar、icon button、row/tile、preview viewport 和 badge 容器具备固定尺寸，loading/hover/dynamic count 不推动整体布局。
- 页面 section 不包装成漂浮卡片，不嵌套 cards；只有重复 item、Dialog 或真正 framed tool 可使用 card/DataSurface。
- 中间 viewport 不压缩三栏，而转换为 context selector + 中栏并使用 side/full inspector。
- 390px 级窄屏转换为 context selector -> list/grid -> full-screen inspector。Close/Back/previous/next 保留 filter、scroll、selection、RP 和合法 route state。
- 长仓库名、文件名、路径、Provider/状态文本在 desktop、intermediate 和 mobile 不重叠；使用 stable minmax、truncate + accessible full label、wrap/break rules，而不是 viewport 字体或负字距。
- 视觉继续使用现有主题、字体、色彩、间距、Lucide 和 shadcn-style primitives；无营销 hero、decorative gradient/orb、巨大标题或新品牌。

### R3. State Ownership And Async Safety

- Router 只拥有可深链接的非敏感 closed state；临时 query、cursor pages、selection、focus/scroll anchors、tickets/proofs 和 mutation attempts 只在 memory；localStorage 只拥有版本化、严格校验且不超过 4 KiB 的 layout/column-width preferences。
- Repository、RP、directory/entry、search、overlay、content ticket、evidence 和 diff request 都支持 AbortController。
- 每个 request channel 同时绑定 monotonic sequence 和 canonical request key；abort 之外仍要求 response 在 sequence/key/selection 全部匹配后才可提交。
- 切换 repository/RP/parent/search/saved search 时按依赖 abort 并清除下游数据。旧 response 不覆盖新选择，旧 entry 不在新 RP 下复用。
- Cursor stale 清除当前 page chain 并从第一页重取；保留合法 filter/sort 和 focus intent。RP retired/expired/missing、parent/entry tombstone 或 semantic mismatch 清除依赖 route 并显示 blocked/tombstone state。
- Feature disabled、empty、partial、offline、stale、failed、tombstone、permission denied 和 unknown future DTO 都是独立状态，不使用一个通用“空列表”掩盖。

### R4. Browse And Search Semantics

- Directory browse 使用服务端 cursor 与 `name_asc/name_desc/size_desc/modified_desc`，不对单页结果做伪全局排序。
- List/grid 共享同一 ordered result、selection 和 virtualizer state；切换 layout 不重新解释 identity 或丢失上下文。
- Search 支持 current、all retained、exact saved-search scope、partial coverage、stable cursor、total relation、authoritative empty 和 grouped retained hits。
- 临时查询仅在 memory；刷新不恢复。保存搜索从 opaque ID 加载服务端 AST。
- `partial + zero` 不显示“没有结果”；只有 `coverage=complete + authoritativeEmpty=true + total=0` 才是权威空结果。
- Grouped hit 只显示服务端返回的 exact RP/lineage。当前 API 没有 version count/history refs，Versions 不猜测展开。
- Server-evaluated permissions/capabilities 是唯一 action source。客户端不通过 role、Provider kind、文件扩展名或颜色推断授权。

### R5. Orthogonal Operational States

- RecoveryPoint lifecycle 与 physical availability、mutable/versioned/WORM 属性分别展示。
- Catalog generation、coverage、staleness、content availability 分别展示。
- Content 的 offline/range/materialization/source-change/unsupported 原因只在 API 真正提供时展示；未知失败使用安全 fallback。
- Preview 的 native/derived/unsupported/not-deployed/queued/failed 是闭合状态；当前 main 无 Worker/Derived，因此不得产生 queued/derived 假状态。
- Security scan 与 sensitivity 正交；未扫描不等于安全，unknown 与 secret 都 fail closed，颜色不是唯一表达。
- 未知 Provider、DTO code、error code 使用安全本地化 fallback；raw tool/server error 不直出。

### R6. Core Inspector And Preview

- Inspector 提供 closed tabs：preview、metadata、versions、security、evidence、diff。Overlay action 通过真实 action menu/Dialog 进入，不把功能说明写进页面。
- Core preview 覆盖 escaped bounded text/config/log、safe raster、same-origin PDF、native audio/video、metadata/hex fallback 和 attachment download；HTML/XML/SVG 永不 active inline。
- Component 只消费 Child 8 opaque ticket URL，不解析/保存 secret、不拼 Provider path、不直接读取 Provider bytes。Native media/image/frame 解绑时立即清除 DOM URL reference 和内存 descriptor。
- Ticket issuance/renewal 绑定精确 AssetRef、renderer/profile/action 和 selection generation。旧 issuance abort；near-expiry 或真实 media failure 只对同一 active selection reissue。
- `asset.secret_reveal` 与 `asset.download` 使用 `{ persist:false, reuseCached:false }`；普通非敏感 preview 不做多余 step-up。当前 negotiation/revoke 缺口按 G1/G10 阻断，不用 frontend 猜测补齐。
- Range/cache/source/renderer/offline 失败只提供真实、non-destructive fallback；不绕过 Content Broker。Attachment 仅在 `download` permission 和精确 proof 下出现。
- Previous/next 以当前 ordered visible result 为准，切换时保持 filter/list scroll 并将 inspector focus 移到新标题；边界状态不导致 layout shift。

### R7. Overlay Contract

- 所有 overlay target 使用 composite `AssetRef`；overlay 不形成 retention hold、不修改 Provider/manifest/trust。
- 实现当前 API 可验证的 saved-search create/update-query/delete/execute/broken state、favorite list/toggle、tag definition CRUD、recent list/clear。
- Mutation 具有 in-memory idempotency attempt、pending lock、AbortSignal、conflict refetch 和 localized safe error。
- Saved-search rename、complete tag assignment state、precise quota/rate/idempotency errors 受 G2/G3/G5 gate 约束；不通过 local-only state 宣称完成。
- Tombstone/broken scope 保留 opaque identity 与用户 overlay 信息，绝不重新显示已删除源的旧 path/name/MIME/hash。

### R8. Evidence, Diff, Versions, And Compatibility

- Evidence 显示 manifest、publication verification、lineage 和 restore-drill recorded facts；不生成新的 trust score 或把 recorded evidence 升级为“可信”。
- Diff 必须选择两个不同的 exact RecoveryPoint；Catalog changed fields 和 Provider evidence status 分层显示，Provider unavailable 不取消 Catalog diff 的真实结果。
- Versions 只使用 producing lineage 和 exact RP。当前版本展开缺口按 G4 显示 unavailable，不查询或显示 `latest`。
- Tasks 继续拥有配置、调度、执行、日志和 legacy snapshot restore。Legacy browser/search/dialog 不删除、不复制为完整资产浏览。
- 因 current main 无 native snapshot/path -> opaque RP/entry resolver，Child 9 只增加明确的 task-context workspace link；exact compatibility 受 G7 gate 约束。

### R9. Accessibility, I18n, And Visual Quality

- Route tabs和 inspector tabs 使用 tablist/tab/tabpanel；Tree 使用 one-tab-stop roving focus、Left/Right、Up/Down、Home/End；list/grid 使用正确 roles、selection、keyboard previous/next 和 focus-visible。
- Result/selection/coverage changes 通过 concise `aria-live` summary；Dialog/portal focus trap、close return、mobile inspector return 和 virtualized offscreen focus restoration 有测试。
- Reduced motion、real-browser contrast、color-independent status、icon accessible names、long text 和 media alternatives 均纳入验收。
- zh 为默认，zh/en key 完整一致；所有 Provider/state/capability/error code 映射本地化。
- 页面不写功能介绍、快捷键教学、视觉自述或“即将推出”营销文案。Empty/error/partial/offline 只描述当前状态和当前可执行命令。
- 实施阶段必须使用 browser/CDP 在 1440x900、390x844 和一个中间 viewport 走真实 route/interaction，检查 nonblank、长文本、portal、focus、scroll、preview、console/network 与 overlap。只用合成 fixtures/MSW/CDP response，不泄露真实资产。

## Non-Goals

- 不修改 backend、schema、migration、Provider、deploy、Nginx、public API 或 public docs。
- 不占用 `000067` 或任何 migration reservation。
- 不安装 npm dependency，除非计划 amendment 单列理由、替代方案和用户批准。
- 不启用 `backup_assets.enabled`，不把 feature-disabled fixture 当作 enabled production evidence。
- 不实现 Worker protocol/capabilities、Derived Store、preview jobs、OCR/thumbnail/scan、batch export、archive browse、controlled recovery、recovery result、retention/hold/reconnect/purge 或 GA legacy removal。
- 不把 Command Provider、offline Repository、partial Catalog、unknown sensitivity 或 missing Worker 美化为成功能力。
- 不使用未合并 sibling branch、历史 detached worktree 或外部未复核状态作为代码基线。

## Constraints

- Parent `07-12-backup-data-explorer-design` 的 Execution Contract、design sections 10/17/20/21.2 和 implement Child 9/coverage/rollback contract 继续有效；current-main evidence 优先修正过时字段类型或不存在的 API 假设。
- React 18、TypeScript strict、React Router、Tailwind、现有 primitives、i18next、TanStack Virtual、MSW、Vitest/Testing Library、vitest-axe 和 Lucide 是既有栈。
- Raw snake_case 只存在于 `web/src/lib/api` mapper/fixture boundary；组件不得使用 direct `fetch`、`any`、`unknown as T` 或 raw DTO。
- 产品实现只能在 `codex/backup-assets-workspace-ui` 通过单一 PR；不得提交 main。
- 如 implementation discovery 需要 backend/API/migration/deploy/docs/feature enablement、manifest 外产品文件或新 dependency，立即停止并提交 focused amendment 审批。

## Acceptance Criteria

Product criteria below are checked only where fresh implementation evidence now
exists. Repository delivery remains a separate unchecked terminal criterion.

- [x] `/app/backups` nested routes、single navigation entry、overview parity 和 data/recovery feature-disabled behavior 通过 route/page tests。
- [x] Route parser/serializer/mutator 对 closed fields、opaque ID、canonicalization、coupled reset、param preservation 和 privacy invariants 通过 property/table tests。
- [x] Asset preferences codec 仅保存版本、layout/width，严格验证、限制 4 KiB；省略 route layout 时从偏好回填并 canonical replace，显式 route layout 优先，用户切换 layout 同步写回唯一偏好记录，校验后的 context/inspector width 驱动 desktop tracks；所有敏感 state 均不进入任何 browser storage/history channel。
- [x] Desktop/intermediate/mobile state flows 保持 RP、filter、selection、scroll、focus 和 previous/next context，无三栏压缩或重叠。
- [x] Repository/RP/directory/entry/search/overlay/content/evidence/diff 的 abort + sequence/key tests 证明 stale response 不能覆盖新选择。
- [x] Virtualized list/grid、cursor stale recovery 和 complete/partial/offline/empty/tombstone matrix 有 focused tests。
- [x] Core renderer、ticket renewal/teardown、exact-purpose step-up、unknown/secret fail-closed、HTML/XML/SVG inactive rendering 和真实 fallback 有 integration tests；G1/G6/G10 未批准能力不冒充完成。
- [x] Saved/favorite/tag/recent 仅覆盖 current API 可验证能力；G2/G3/G5 gate 行为有 explicit unavailable/conflict tests。
- [x] Evidence 与 exact two-point diff 不升级 trust；Versions 不使用 latest、不伪造 history。
- [x] Legacy Tasks browse/search/restore 保留；task-context link 不冒充 exact deep link。
- [x] Tree/list/grid/tab/dialog/portal/focus/aria-live/reduced-motion/color-independent/zh-en coverage 通过 unit/integration/axe/browser matrix。
- [x] `env -u NODE_ENV npm run check` 和 `node scripts/check-bundle-budget.mjs` 完整通过，且没有新增依赖或越界产品文件。
- [x] Browser/CDP 三 viewport 真实路由证据记录 nonblank、long-text、portal、focus、scroll、preview、console/network 和 no-overlap 结果，并给用户可访问 dev URL。
- [ ] Exact staging 与 work commit 已完成；finish-work archive auto-commit、concrete journal commit、push、single PR、required CI green、squash merge、post-merge automation 和 main sync 按顺序完成后才可声明 Child 9 complete。

## Approved Scope-Gate Disposition

用户于 2026-07-19 批准推荐路径：交付 buildable frontend core + truthful unavailable states，不扩 backend。G1-G8/G10 保持本 Child 范围外并记录为后续 API 合同；G9 为 manifest 内 frontend-only typed error mapper，不需要 backend 变更。

任何 gate 未批准都不能被 fixture、optimistic local state、generic 409 文案、role guess 或 Provider direct access 绕过。

用户于 2026-07-19 另行明确授权开始实施；`task.py start` 与产品实施可按
`implement.md` 执行，交付门禁仍以实际验证结果为准。

## Notes

- Current-main evidence：`research/current-main-ui-api-evidence.md`。
- Scope decisions：`research/scope-gates.md`。
- Implementation evidence：`research/implementation-evidence.md`。
- Browser/visual evidence：`research/visual-verification.md`。
- Technical ownership/state/route contract：`design.md`。
- Executed TDD/file/validation plan and pending delivery ledger：`implement.md`。
