# 设计：Catalog list 权限与内容票据授权解耦

## 1. Current data flow and failure

```text
Catalog status/list projection
  permissions.list=true, preview=false, download=false
  content_available=true
             |
             v
BackupAssetsWorkspace.canPreview
  currently requires permissions.preview=true
             |
             v
AssetPreview hides “Load preview”
             |
             x  delivery-ticket API is never called
```

后端正式路径本身已经完整：

```text
authenticated role
  -> POST /recovery-points/:id/entries/:entryId/delivery-tickets
  -> middleware RBAC(backup_assets:preview)
  -> content ticket service
  -> opaque same-origin content URL
  -> renderer-specific browser preview
```

问题只在前端把两个不同权限域合并成了一个条件。

## 2. Considered approaches

### A. Recommended — UI eligibility from authenticated role plus list/content/capability facts

在工作台内定义一个窄的 native-preview eligibility：token 存在，角色精确为 Admin/Operator，Catalog list=true，内容可用，恢复点存在，并满足 renderer 的 sequential/range capability。delivery-ticket 请求及后端 RBAC 不变。

优点：

- 与当前后端 closed role matrix 和父任务 list-only 合同一致；
- 只修复错误门禁，不改变 API、数据库、Catalog 或权限注册表；
- Viewer/未知角色前端 fail-closed，服务器仍承担最终授权；
- 能直接用生产等价 fixture 做 TDD，并在真实环境只触发正式 API。

代价：前端角色资格是 UI 提示层的镜像，未来后端角色矩阵变化时必须同步测试与永久规范。通过服务端 RBAC 回归和永久 cross-layer checklist 把漂移风险显式化。

### B. Backend enriches Catalog permissions per caller

让 Catalog handler/service 根据当前请求角色返回 preview/download 权限。

不采用：它会改变已锁定的 list-only API 合同，使 Catalog 状态与内容 action 授权再次耦合，扩大到 backend/service/handler/Swagger/frontend mapper，并且仍不能取代 ticket route RBAC。

### C. Optimistically show preview to every listed user and rely only on 403

忽略角色，只要 list/content/capability 成立就显示按钮，服务器拒绝 Viewer。

不采用：当前 Viewer 虽不能列举资产，但未来权限拆分后可能产生不必要的存在探测和拒绝请求；前端不能在已知 closed role matrix 下主动放宽 UI eligibility。

## 3. Selected contract

新增/内联一个纯函数式资格判定，语义如下：

| Input | Preview UI eligibility |
|---|---|
| token + Admin/Operator + list + content + required read capability | true |
| Viewer/unknown/null role or missing token | false |
| Catalog list=false | false |
| content unavailable | false |
| renderer needs Range but openRange=false | false |
| text/metadata-hex needs sequential but openSequential=false | false |
| Catalog preview=false | ignored; this field is not the ticket authorization source |

`canDownload` 保持现状，不在本任务中借机修改。

## 4. Component and API boundary

- `BackupAssetsWorkspace` owns eligibility because it has the selected Catalog, recovery-point capabilities, selected asset renderer product, and authenticated runtime.
- `AssetPreview` remains a presentation/action component receiving `canPreview`; it does not read storage or reconstruct authorization.
- `useBackupAssetsState.loadPreview` continues calling the typed content API and maps 401/403/capability/secret-reveal failures through the existing closed error mapper.
- Ticket URL、proof、token、AssetRef 不进入 local/session storage、router state 或日志。

## 5. TDD matrix

First RED must use a production-like list-only Catalog fixture rather than the historical synthetic fixture with preview=true.

Positive:

- Admin + token + list-only Catalog + content + sequential renderer -> Load Preview visible and exact asset passed to `loadPreview`.
- Operator same inputs -> same UI attempt allowed; server RBAC regression proves permission.
- Range renderer uses openRange rather than openSequential.

Negative:

- Viewer, null role, missing token.
- Catalog list=false.
- content unavailable.
- sequential/range capability missing for the chosen renderer.
- server-side Viewer ticket request remains 403; unknown role remains 403.

Regression:

- Catalog fixture remains `preview=false`; no mapper or backend producer is changed.
- Admin-only secret reveal retry and Operator fail-closed behavior remain covered by existing state tests.
- Download gating remains unchanged.

## 6. Specs and documentation

Add a permanent frontend quality scenario for the independent ticket authorization boundary and extend the cross-layer checklist so future Catalog/UI changes use the actual upstream list-only fixture. The new task design and parent PRD supersede archived workspace design section 14.1 for current behavior; archived evidence is not rewritten.

## 7. Delivery and production acceptance

Implementation and check run through Trellis agents in the dedicated worktree. After focused/full gates, create PR, monitor required CI, merge only when green, then monitor Release Please and Docker publishing. Production upgrades with backup assets already safely enabled; use a narrow exact-asset route, perform any step-up only inside the UI, verify rendered preview without recording content, and recheck health/errors/collector count.
