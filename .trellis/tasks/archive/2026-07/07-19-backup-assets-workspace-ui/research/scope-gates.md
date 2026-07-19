# Child 9 API And Scope Gates

## 1. Gate rule

Child 9 owns frontend workspace, route state, core browse/search/preview,
overlays, evidence/diff, compatibility links, i18n, accessibility, and visual
verification. It does not own backend handlers, DTOs, migrations, Provider
behavior, Worker/Derived capabilities, export, recovery jobs, retention, or
feature enablement.

The gaps below were verified on current main. Focused planning may describe
them, but future implementation must not silently cross the boundary. The
recommended default is to deliver only truthful, buildable behavior and leave
the missing interaction absent or explicitly unavailable until a separately
approved API owner supplies the contract.

## 2. Approval gates

| Gate | Parent/Child 9 expectation | Current-main evidence | Safe frontend behavior without expansion | Approval needed for full behavior |
|---|---|---|---|---|
| G1 Secret-preview negotiation | Prompt only for secret/unknown; ordinary preview has no needless step-up. | Classification is returned only by successful ticket issuance. No-proof secret/unknown and proof-bearing non-secret issuance both become generic 400. | Keep ordinary non-sensitive core preview available only where a real prior projection proves it; otherwise show metadata/hex availability without claiming secret reveal can be negotiated. Never infer from name/MIME. | Backend/API amendment that returns a non-disclosing typed `step_up_required` challenge or a preflight classification/capability projection bound to the exact AssetRef/revision. |
| G2 Saved-search rename | Create, rename, delete, execute, and broken-scope handling. | `SavedAssetSearch` and create/update payloads contain query/version/state only; no display name exists. | Create/update query/delete/execute. Use a localized neutral label plus timestamp in the current UI; do not synthesize/persist a name or claim rename support. | Child 7/API amendment adding encrypted display name with limits, optimistic versioning, mapper, migration impact analysis, and tests. |
| G3 Tag assignment state | Show and edit the complete tags assigned to an asset. | API supports assign/unassign but has no list/get assignments route. Internal lifecycle snapshots are not a user API. | Tag-definition CRUD is available. Do not show unchecked tags as authoritative or retain optimistic assignment state across navigation/reload; assignment controls remain unavailable unless a direct action can be reconciled truthfully. | Child 7/API amendment for owner-scoped assignment lookup by composite AssetRef, stable pagination/permissions, DTO mapper, and tests. |
| G4 Version expansion | Group all-retained results and expand exact historical versions. | Search groups by lineage/path token but returns only one hit, with no version count, hidden refs, or expansion cursor. | Label only the returned exact hit and its producing lineage/RP. The Versions tab shows an unavailable state; never query `latest` or guess sibling points. | Search/API amendment returning a bounded exact version-group handle/count and cursor, or a dedicated versions endpoint. |
| G5 Overlay error precision | Distinguish broken scope, quota, rate limit, idempotency collision, and stale optimistic version. | Handler maps broken scope, quota, idempotency conflict, and generic conflict to 409 `stale_state`; rate limit has no stable asset code. | For a mutation-context 409, stop retrying, refetch the affected resource, and show a localized generic conflict. Use `retryAfter` only when actually present. | Backend error-envelope amendment with closed code, retryable/stage/correlation fields and tests. |
| G6 Content fallback reasons | Explain range unavailable, cache/materialization disabled/full, source changed, renderer unsupported, and offline separately. | Successful ticket requires `capability_reason=null` and empty fallback actions; many issuance/gateway failures collapse to generic responses. | Use Catalog's real repository/content capability when present; otherwise show a generic non-destructive unavailable state and only real alternative actions. Never bypass Broker. | Content API amendment exposing a closed safe failure projection for issuance and gateway renewal. |
| G7 Exact legacy deep links | Link legacy snapshot browser/search/run results to exact Task/RP/entry. | Legacy results have native snapshot ID and raw path; no API resolves them to opaque RecoveryPoint/Entry refs. | Preserve all legacy surfaces. Add only a clearly task-scoped link (`taskId`) to the data workspace, which then asks the user to select an exact RP/entry. | Backend resolver contract with ownership, ambiguity handling, raw-path body privacy, and exact composite result. |
| G8 Browse sort parity | Route exposes non-sensitive sort/direction without false global ordering. | Browse supports only name asc/desc, size desc, modified desc. | Route codec accepts only those coupled pairs for browse. Unsupported pairs safe-reset; never client-sort cursor pages. Search keeps its own relevance/name asc/modified desc map. | Catalog API expansion plus stable cursor/order tests for any new direction/field. |
| G9 Typed UI errors | Components render localized closed errors and never raw tool messages. | `ApiError.detail` is `unknown`; known Catalog capability DTOs are typed, generic request errors are not. | Frontend-only strict parser maps only verified status/detail shapes and otherwise uses a safe localized fallback. | No backend expansion is required for the limited mapper. Precise G5/G6 behavior still needs those separate gates. |
| G10 Individual ticket revoke | Closing/changing preview revokes the old exact delivery grant. | Frontend API only issues tickets; secured router has no per-ticket revoke endpoint. Logout revokes the whole session. | Abort issuance, detach media/frame URLs, clear in-memory descriptors, and allow the short grant to expire. Do not report it as server-revoked. | Content API amendment for exact owner/session-bound revoke with idempotent behavior and audit coverage. |

## 3. Not scope gates: mandatory truthful degradation

The following are not reasons to expand Child 9:

- Worker and Derived Store are absent until Children 10-11. Render only real
  `not_deployed`/`unsupported` states and retain native Content Broker paths.
- Export belongs to Child 12. Multi-select may preserve selection and show only
  currently real per-item operations; it must not create an export control.
- Controlled recovery belongs to Child 13. `/recovery` may show current
  evidence and legacy Tasks compatibility, but no recovery plan/job/result UI.
- Retention/reconnect/purge belong to Child 14. Child 9 displays current
  lifecycle facts and never creates holds or repository mutation actions.
- `backup_assets.enabled` stays false by default through Child 14. A real
  `feature_disabled` response blocks the workspace; the frontend does not
  override or emulate enablement.
- Command Provider remains unsupported. The UI localizes the real capability
  reason and never invents a repository, RecoveryPoint, or asset list.

## 4. Approved Review Decision

The user approved option 1 on 2026-07-19:

1. **Approved scope:** implement the buildable core plus truthful unavailable
   states described above; keep G1-G8/G10 out of scope and record follow-up API
   owners.
2. A future request may approve one or more focused backend/API amendments,
   update all three Child 9
   planning documents and exact manifest, then re-review before implementation.
3. Defer Child 9 until the required API contracts exist.

No option permits a frontend guess, raw Provider access, fake fixture-only
production capability, migration use, or dependency on an unmerged sibling.

This decision approves the planning scope only. `task.py start`, product
implementation, tests, and delivery remain separately gated and unexecuted.
