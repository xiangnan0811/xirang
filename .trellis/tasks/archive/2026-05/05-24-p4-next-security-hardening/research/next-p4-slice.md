# Research: next-p4-slice

- **Query**: Research the next best executable P4 security hardening slice after completed local credential/provider seams, integration notification sanitization, and task runtime log sanitization. Constraints: local-only, no external Vault/KMS/SSH CA/session recording/command approval, no raw secrets/runtime evidence/path/content/diagnostic/Docker/endpoint/host-sensitive material in responses/logs/audit/docs/UI storage, and preserve existing API/deployment/UI behavior unless necessary.
- **Scope**: internal
- **Date**: 2026-05-24

## Findings

### Files Found

| File Path | Description |
|---|---|
| `backend/internal/model/models.go` | `NodeLog` model exposes stored `path` and `message` JSON fields. |
| `backend/internal/nodelogs/parser.go` | Parses journal/file log output into `LogEntry.Path` and `LogEntry.Message` without a local sanitizer. |
| `backend/internal/nodelogs/worker.go` | Persists parsed node-log entries directly with `CreateInBatches`. |
| `backend/internal/api/handlers/node_logs_handler.go` | Returns `[]model.NodeLog` directly from query and alert-context endpoints. |
| `backend/internal/api/handlers/node_log_config_handler.go` | Returns configured log paths and validation errors that include rejected path text. |
| `backend/internal/api/handlers/node_migrate_preflight_handler.go` | Preflight response includes node host and policy source-path fields; check messages include raw probe/dial errors and policy paths. |
| `backend/internal/api/handlers/node_doctor_handler.go` | Doctor evidence is sanitized for secret markers but not generic paths/hostnames; backup-dir checks include raw configured directory values. |
| `backend/internal/api/handlers/docker_handler.go` | Docker volume response returns Docker-derived volume names and mountpoints; audit metadata is already count/stage based. |
| `backend/internal/api/handlers/file_handler.go` | File browser returns file paths and preview content by design; audit uses path hashes rather than raw paths. |
| `.trellis/tasks/archive/2026-05/05-23-p4-task-runtime-log-sanitization/research/task-runtime-log-sanitization.md` | Confirms task runtime logs/last errors were the prior chosen P4 slice and are now completed. |
| `.trellis/tasks/archive/2026-05/05-23-p4-integration-notification-access/research/integration-notification-access.md` | Confirms integration endpoint/proxy/error response sanitization was already selected and completed. |
| `.trellis/tasks/archive/2026-05/05-23-p4-next-hardening/research/profile-hook-app-credential-flow.md` | Confirms app credential/profile-hook exposure was previously researched and is now part of the completed baseline. |

### Ranked Candidate Slices

| Rank | Candidate | Blast-radius reduction | Executability | Notes |
|---|---|---|---|---|
| 1 | **Node log content/path sanitization boundary** | High: node-log ingestion persists remote journal/file lines and returns them via normal API/WebSocket-adjacent UI surfaces. | High: backend-local, analogous to the completed task runtime sanitizer, can preserve response schema while masking `path`/`message`. | Recommended next slice. |
| 2 | **Node Doctor + migration preflight diagnostic evidence sanitizer** | High: diagnostic/preflight responses currently include host/path/error evidence, and specs explicitly call out diagnostic evidence. | High: backend-local response/evidence sanitization; narrower than node-log storage. | Good follow-up or combined only if scope stays small. |
| 3 | **Docker volume response masking** | Medium: Docker-derived names/mountpoints are returned to clients, while audit already avoids them. | Medium-high: one handler/DTO surface, but UI may rely on volume names for selection. | Smaller blast radius than node logs/diagnostics. |
| 4 | **File browser path/content response hardening** | Very high theoretical exposure: file browser returns paths and file preview content. | Low-medium: file browsing/preview is intentional product behavior; strict masking would likely break API/UI behavior. | Defer unless product chooses a degraded/metadata-only mode. |

### Code Patterns

#### 1. Node log content/path boundary is the strongest next slice

`backend/internal/model/models.go:772-781` defines `NodeLog` with JSON-exposed fields:

```go
Path    string `gorm:"type:text;not null" json:"path"`
Message string `gorm:"type:text;not null" json:"message"`
```

`backend/internal/nodelogs/parser.go:69-76` stores journal data directly into `LogEntry`, including `SystemdUnit` as `Path` and `MESSAGE` as `Message`. `backend/internal/nodelogs/parser.go:100-107` does the same for tailed file lines, using the configured file path and raw line text.

`backend/internal/nodelogs/worker.go:50-58` writes parsed entries directly with `CreateInBatches(&entries, InsertBatchSize)`. No package-local sanitizer was observed between parser output and database storage.

`backend/internal/api/handlers/node_logs_handler.go:24-28` defines API output as `Data []model.NodeLog`, and `backend/internal/api/handlers/node_logs_handler.go:124-134` returns database rows directly. Alert-context logs follow the same pattern: `alertLogsResponse.Data []model.NodeLog` at `backend/internal/api/handlers/node_logs_handler.go:137-144`, assigned directly at `backend/internal/api/handlers/node_logs_handler.go:178-190`.

`backend/internal/api/handlers/node_log_config_handler.go:38-58` validates configured log paths, but validation errors concatenate the rejected path text. `backend/internal/api/handlers/node_log_config_handler.go:75-79` and `backend/internal/api/handlers/node_log_config_handler.go:119-120` return configured path lists back to clients.

Why this ranks first: it is the closest remaining analogue to the completed task-runtime log sanitizer. It reduces stored/UI-visible runtime evidence without requiring external services or deployment changes. It can likely keep API field names and pagination/filter behavior while replacing raw path/message values with safe summaries or placeholders.

#### 2. Diagnostic/preflight evidence is also a clear local-only gap

`backend/internal/api/handlers/node_migrate_preflight_handler.go:36-52` defines preflight DTOs with `Host` and `SourcePath`. `backend/internal/api/handlers/node_migrate_preflight_handler.go:193-205` populates `SourceNode.Host`, `TargetNode.Host`, and policy summaries.

Preflight check messages include raw upstream errors or paths: target SSH probe error at `backend/internal/api/handlers/node_migrate_preflight_handler.go:216-220`, SSH session/dial error at `backend/internal/api/handlers/node_migrate_preflight_handler.go:244-254`, and policy path success/failure messages at `backend/internal/api/handlers/node_migrate_preflight_handler.go:274-296`.

`backend/internal/api/handlers/node_doctor_handler.go:38-50` returns structured `evidence` and `suggestion` fields. All evidence passes through `sanitizeDoctorEvidence` at `backend/internal/api/handlers/node_doctor_handler.go:250-256`, but that sanitizer only hides selected secret markers and delegates to `util.SanitizeMessage` (`backend/internal/api/handlers/node_doctor_handler.go:694-712`). It does not broadly hide paths/hostnames. Backup-dir evidence can include configured directory values at `backend/internal/api/handlers/node_doctor_handler.go:463-471`.

This slice is executable because it can introduce a shared local diagnostic-evidence sanitizer and DTO masking without changing the underlying diagnostic commands.

#### 3. Docker volume response is smaller but directly matches the Docker-output constraint

`backend/internal/api/handlers/docker_handler.go:23-28` exposes `DockerVolume{Name, Driver, Mountpoint}`. `backend/internal/api/handlers/docker_handler.go:145-199` parses remote Docker command output and returns those fields. Audit metadata is already sanitized to stage/count/warning at `backend/internal/api/handlers/docker_handler.go:92-116`, so the remaining gap is the client response DTO.

This is likely easy to isolate, but the user-facing value of volume discovery may depend on stable volume names. That makes it less immediately valuable than node-log or diagnostic evidence boundaries.

#### 4. File browser is high exposure but likely product-breaking

`backend/internal/api/handlers/file_handler.go:28-51` defines responses containing file names, paths, and content. Node file listing returns `FileListResponse{Path: cleanPath, Entries: entries}` at `backend/internal/api/handlers/file_handler.go:138-142`; preview returns `FileContentResponse{Path: cleanPath, Content: string(buf[:n])}` at `backend/internal/api/handlers/file_handler.go:259-264`.

The audit path is already safer: file-browser audit metadata uses `path_hash` and bounded metadata at `backend/internal/api/handlers/file_handler.go:96-99`, `backend/internal/api/handlers/file_handler.go:129-136`, and `backend/internal/api/handlers/file_handler.go:250-257`. Because raw file preview is an intentional feature, strict masking would require a product decision rather than a small P4 hardening slice.

### External References

No external references were used. The task is local-only and satisfied by internal source/spec/Trellis inspection.

### Related Specs

- `.trellis/spec/backend/logging-guidelines.md` — forbids logging decrypted values, SFTP file contents, Docker command output/volume names, Node Doctor evidence, migration preflight command output, executor config, and unsafe credential-audit metadata.
- `.trellis/spec/backend/error-handling.md` — forbids exposing raw command output, SFTP/file content, Docker output, diagnostic evidence, exported config payloads, raw SQL/encryption/stack details to clients.
- `.trellis/spec/backend/quality-guidelines.md` — requires shared sanitization for user-visible evidence/delivery errors/drill output/incident messages/notification payloads, and states credential-audit events must not store raw command output, Docker output, diagnostic evidence, file contents, or full command text.

## Caveats / Not Found

- The completed baseline was respected: SSH local provider seams, executor SSH adoption, restic resolver, app profile credential seam, integration response/error sanitization, and task runtime log sanitization were not recommended again.
- No evidence was found that node-log storage currently uses the task runtime sanitizer or a nodelogs-specific equivalent.
- Node-log query filtering still operates on stored raw `message`/`path`; masking at storage time may affect keyword/path search semantics. Masking at response time preserves search semantics but leaves raw evidence in DB. The implementation slice should choose this boundary deliberately.
- File browser response hardening conflicts with an intentional feature and should not be the next small executable slice unless the product accepts behavior changes.
