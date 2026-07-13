# Codebase Research: Backup Asset Visibility

- **Date:** 2026-07-12
- **Scope:** Read-only review of current backup, restore, snapshot, file-browser, authorization, audit, frontend navigation, deployment, and related task history.
- **Question:** What already exists, why users still experience backup data as invisible, and which constraints must shape a future design?

## Executive Conclusion

The reported gap is real, but its precise shape is **fragmentation plus missing content delivery**, not a total absence of file-oriented functionality.

Xirang currently has three separate foundations:

1. A live-node SFTP browser with bounded text preview.
2. An admin-only endpoint that lists a local Rsync backup directory.
3. Restic snapshot listing, path search, diff, selection, and restore.

These foundations are not presented as a coherent backup asset experience. The Backups page only shows health, confidence, storage usage, and setup guidance. The local backup-list endpoint has no frontend consumer and no backup-content endpoint. The Restic UI lives inside the task execution-history dialog and exposes metadata/restore controls, not file contents. Rclone has no browse surface. Consequently, a user can know that a backup is healthy and can sometimes locate a path, but cannot generally inspect what was backed up, open an image, read a backed-up configuration file, play media, or download a single item.

The strongest product direction suggested by the code is a **read-only backup asset explorer** built over provider capabilities, not a general-purpose drive. Xirang's engines have materially different storage and version semantics, so a single filesystem assumption would be incorrect.

## Existing Product Surfaces

### Backups page is an assurance dashboard, not a data browser

- `web/src/pages/backups-page.tsx:67-95` renders `BackupConfidencePanel`, `BackupHealthPanel`, `StorageUsagePanel`, and `StorageGuideCard`.
- Its only primary action links to task creation (`web/src/pages/backups-page.tsx:69-76`).
- There is no repository, backup version, directory, file, or preview state on this page.

This explains the user's perception: the navigation item named “Backups” does not lead to backed-up content.

### Restic browsing exists, but is nested and metadata-only

- The Restic snapshot/search/diff controls appear only inside the task execution-history dialog (`web/src/pages/tasks-page.dialogs.tsx:170-239`).
- `SnapshotBrowser` lists snapshots and file entries, supports directory navigation, allows path selection, and starts a restore (`web/src/components/snapshot-browser.tsx:35-175`, `:185-339`).
- A regular file is rendered as a non-interactive name/size row; only directories navigate (`web/src/components/snapshot-browser.tsx:233-269`).
- There is no click-to-preview, raw-content, thumbnail, media, or single-file download API in `web/src/lib/api/snapshots-api.ts:32-65`.

### Live-node file preview is not backup preview

- `FileBrowser` is wired from the Nodes page to `listNodeFiles` and `getNodeFileContent` (`web/src/pages/nodes-page.dialogs.tsx:164-215`).
- `FilePreviewDialog` renders every response as text inside `<pre>` and clears/aborts transient content on close (`web/src/components/file-preview-dialog.tsx:23-147`).
- The backend reads at most 1 MiB and returns a JSON string (`backend/internal/api/handlers/file_handler.go:23-25`, `:145-265`).

This code is useful precedent for path safety, audit, cancellation, and bounded text preview, but it reads the current source node through SFTP. It does not prove that the backed-up copy is visible or identical.

### Local backup listing endpoint is incomplete and effectively undiscoverable

- `GET /tasks/:id/backup-files` is admin-only (`backend/internal/api/router.go:327`).
- It validates a request below `Task.RsyncTarget`, resolves symlinks, and lists at most 500 local entries (`backend/internal/api/handlers/file_handler.go:267-329`, `:475-563`).
- `filesApi.listTaskBackupFiles` exists (`web/src/lib/api/files-api.ts:53-64`), but no frontend component calls it.
- There is no corresponding task-backup content, download, or streaming endpoint.
- The endpoint assumes `RsyncTarget` is local to the Xirang process. That assumption does not hold for Restic repositories executed on the node or Rclone remotes.

## Backup Engine Capability Matrix

| Engine | Current storage/data behavior | Version semantics | Existing browse support | Content-read feasibility |
|---|---|---|---|---|
| Rsync | Xirang process pulls a node path over SSH/rsync into `Task.RsyncTarget`; default local root is `/backup/<node.backup_dir>` (`backend/internal/task/executor/executor.go:131-242`; `backend/internal/task/service.go:320-370`). | The executor syncs into a target path. The data model does not create a run-to-version record. It is normally a current mirror unless the configured layout itself creates versions. | Backend local directory list only; admin-only; no UI. | Local files can support stat, bounded reads, downloads, and HTTP Range, subject to containment and symlink checks. |
| Restic | Restic executes over SSH on the managed node. `RsyncTarget` is the repository path and may itself be a remote repository spec (`backend/internal/task/executor/restic_executor.go:25-135`). | Native immutable-ish snapshots with IDs and timestamps. | Snapshot list, directory list, path search, diff, selected-file restore. | `restic dump <snapshot> <path>` can produce a file stream; repeated/range media reads likely need a bounded local cache or materialization layer. |
| Rclone | Rclone executes over SSH on the node and syncs the source to an rclone remote (`backend/internal/task/executor/rclone_executor.go:20-84`). | `rclone sync` is a mirror; historical versions depend on the configured backend/versioning, which Xirang does not model. | No list/search/preview endpoint or UI. | `rclone lsjson/cat` can form a provider adapter; availability and random access depend on the remote/backend and node connectivity. |
| Command | Arbitrary remote command with logged output (`backend/internal/task/executor/command_executor.go:14-117`). | Undefined. | None. | No safe generic content contract; it should remain unsupported until a task explicitly declares an artifact manifest. |

The UI must expose these capability differences instead of implying that every successful task has snapshots, searchable paths, or streamable objects.

## Current Data Model And Lineage Gaps

### Task and task-run records do not identify produced assets

- `model.Task` stores source, target, executor type, and encrypted executor configuration (`backend/internal/model/task.go:12-64`).
- `model.TaskRun` stores status, timing, verification, throughput, progress, and errors, but no artifact ID, repository version, snapshot ID, manifest, root path, object count, or total logical size (`backend/internal/model/task.go:66-84`).
- Restic snapshots are queried live from Restic. They are not persisted as a generalized backup-version entity (`backend/internal/api/handlers/snapshot_handler.go:49-132`).
- Therefore a successful run cannot be reliably opened as “the data from this run,” especially for Rsync/Rclone mirrors.

### Snapshot index is useful but Restic-specific and metadata-thin

- `snapshot_file_indices` stores `task_id`, `snapshot_id`, path, size, and mtime with a unique task/snapshot/path index (`backend/internal/database/migrations/sqlite/000054_snapshot_file_index.up.sql`).
- It is lazily built on first search and currently covers Restic only (`backend/internal/snapshot/indexer.go:64-99`, `:120-228`).
- Search is path-only, capped at 200 results, and has no MIME, extension class, hash, entry type, content sensitivity, preview availability, or extraction status (`backend/internal/api/handlers/snapshot_search_handler.go:42-88`).
- Prior task history explicitly scoped out content search and cross-task search (`.trellis/tasks/archive/2026-05/05-06-snapshot-file-search/prd.md`).

### Existing evidence records can enrich, but not replace, an asset model

- `SnapshotDiffHistory` links change statistics to a task run (`backend/internal/model/backup.go:43-55`).
- `RestoreDrillEvidence` links a drill to source task/run evidence and a snapshot reference (`backend/internal/model/backup.go:7-41`).
- These can be surfaced as trust badges around an asset/version, but neither describes the asset tree.

## Authorization And Audit Findings

### Content needs a permission boundary stronger than generic task metadata

- Restic read routes use `tasks:read` plus `OwnershipTaskCheck` (`backend/internal/api/router.go:344-348`).
- `viewer` has `tasks:read`, and the ownership middleware explicitly lets viewers pass globally (`backend/internal/middleware/rbac.go:67-80`; `backend/internal/middleware/ownership.go:71-110`).
- The live-node content endpoint instead requires `nodes:files`, which is available to admin/operator but not viewer (`backend/internal/middleware/rbac.go:9-80`; `backend/internal/api/router.go:193-194`).
- The local task backup-list endpoint is admin-only and does not use task ownership (`backend/internal/api/router.go:327`).

Filenames, configuration contents, images, databases, and media are materially more sensitive than task health. A future design should introduce explicit backup-asset permissions and apply one consistent ownership rule across list, metadata, content, download, thumbnail, and search operations.

### Existing safety/audit foundations are reusable

- Node browsing resolves remote symlinks through SFTP `RealPath` before comparing allowed roots (`backend/internal/api/handlers/file_handler.go:401-473`).
- Local listing repeats containment checks after `EvalSymlinks` (`backend/internal/api/handlers/file_handler.go:475-505`).
- Credential audit records file list/preview actions with path hashes, sizes/counts, and outcomes, while dropping raw content and output (`backend/internal/api/handlers/file_handler.go:93-142`, `:178-264`, `:351-370`; `backend/internal/credentialaudit/audit.go:144-176`, `:208-280`).
- Existing restore actions already use admin-only access, step-up authentication, task-scoped short-lived grants, and audit (`backend/internal/api/router.go:326`, `:346`).

Backup-asset reads should reuse safe path hashing and purpose-aware SSH credentials, but use dedicated actions such as `backup_asset.list`, `backup_asset.preview`, and `backup_asset.download`. The design must decide which reads are normal RBAC operations and which sensitive classes require step-up/JIT intent.

## Delivery And Deployment Constraints

### Bearer-token authentication complicates native media elements

- The frontend request wrapper sends the JWT in an `Authorization` header (`web/src/lib/api/core.ts:51-69`).
- Native `<img>`, `<audio>`, `<video>`, and PDF iframe navigation cannot attach that application header.
- Fetching a whole object as a Blob works for small previews but is unsafe for large video and prevents efficient seek unless the browser can make authenticated Range requests.
- Putting a reusable bearer token in a query string would leak into URLs, history, referrers, support screenshots, and intermediary logs.

A secure design therefore needs either:

1. bounded Blob previews for small objects plus a separate mechanism for large media; or
2. short-lived, single-purpose opaque preview tickets that native media requests can redeem without exposing the session token; or
3. a much broader cookie-auth redesign, which is disproportionate to this feature.

The preview-ticket approach currently fits Xirang best.

### Streaming does not fit current generic HTTP defaults without explicit work

- The Go server has a 30-second `WriteTimeout` (`backend/cmd/server/main.go:284-291`).
- Nginx proxies `/api` to the backend and applies the current security headers (`deploy/nginx/templates/default.conf.template`).
- CSP currently allows same-origin images through `img-src 'self' data:` but does not allow Blob URLs; media falls back to `default-src 'self'` (`deploy/nginx/templates/default.conf.template:18-25`; `backend/internal/api/router.go:94-102`).
- The all-in-one image contains rsync/SSH tooling but not Restic or Rclone; those engines deliberately run on managed nodes to preserve the agentless model (`deploy/allinone/Dockerfile:39-79`).

Large downloads/video require explicit Range behavior, cancellation propagation, rate/concurrency limits, cache cleanup, response-header hardening, and either adjusted server timeout behavior or a delivery path that is not constrained by the generic 30-second response.

## Product/Architecture Implications

### A provider/capability model is required

The feature should not switch directly on UI assumptions such as `executorType === restic`. A backup-asset domain should expose capabilities such as:

- `listVersions`
- `listEntries`
- `statEntry`
- `openSequential`
- `openRange`
- `searchPaths`
- `restoreSelection`
- `downloadSelection`
- `supportsHistory`

Each provider can then report why an operation is unavailable. This preserves honest UX for Rsync mirrors, Restic snapshots, Rclone remotes, and unsupported command tasks.

### A unified read-only surface can reuse the current Backups route

The existing Backups page is already the natural navigation destination. A likely information architecture is:

- **Overview:** current confidence/health/storage panels.
- **Data:** repositories/tasks, versions, file tree, search, preview, download/restore.
- **Recovery:** restore and drill evidence.

This is a working hypothesis for later user validation, not a frozen UI design.

### “Drive-like” should describe interaction quality, not a second product scope

Useful drive patterns include familiar navigation, thumbnails, search, preview, versions, and single-file retrieval. Upload, arbitrary folder creation, editing, collaborative documents, public sharing, sync clients, and personal quotas would introduce a second data-ingestion and collaboration product with weak connection to the current backup contract.

## Confirmed Gaps

1. No unified backup-data entry point.
2. No generalized backup-version/asset lineage across engines and task runs.
3. No backed-up file content API for any engine.
4. No image/PDF/audio/video preview pipeline.
5. No single-file download flow.
6. No Rclone browse/search integration.
7. Rsync and Rclone do not guarantee historical versions.
8. Existing Restic browsing is metadata-only and buried in task history.
9. Existing task backup listing is unused, local-only, admin-only, and list-only.
10. Current task-read authorization is too coarse for sensitive backup contents.
11. Current bearer-auth/CSP/server-timeout setup is not sufficient for secure large-media playback.

## Repository-Answerable Questions Now Resolved

- **Does any file browser exist?** Yes, for live node files, with text-only preview.
- **Can any backups be browsed?** Restic paths can be browsed/searched; a local Rsync target can be listed through an unused endpoint.
- **Can backed-up contents be opened?** No general content-read path was found.
- **Are all engines equivalent?** No. Rsync/Rclone are normally mirrors; Restic has native snapshots; command output has no asset contract.
- **Can current permissions be reused unchanged?** No. Backup content is more sensitive than generic task read metadata.
- **Can video simply use an HTML `<video src>`?** Not securely with the current bearer-header authentication and without a ticket/cookie mechanism.

## Subsequent Product Decisions

Later user review resolved several questions raised by this code audit: Xirang remains an operations backup product with drive-quality read access; original download/single-item retrieval belongs in the complete target; advanced OCR/indexing/conversion/scanning/transcoding is supplied through optional Workers; and the design is not constrained to an MVP.

The remaining high-impact intent questions are:

1. Which role/session safeguards should apply to filenames, previews, original downloads, and sensitive configuration contents, while recognizing that the current deployment has one actual user.
2. Whether Xirang may temporarily materialize/decrypt remote content on local disk, and under which encryption, quota, TTL, and disable-policy guarantees.
3. Whether global search, saved views, favorites, and batch retrieval belong in the complete interaction model or remain deliberately absent drive conveniences.

## Related History

- Initial node/local file-browser feature: commit `43667ae`.
- Restic snapshot browsing foundation: commits `7e927f2` and `97406df`.
- Restic cross-snapshot path search: commit `dfeac6b` and archived task `.trellis/tasks/archive/2026-05/05-06-snapshot-file-search/prd.md`.
- File-browser residual hardening: commit `025e40f` and archived task `.trellis/tasks/archive/2026-05/05-24-p4-file-process-residual-hardening/`.
