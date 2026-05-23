# Research: Remaining non-SSH/non-restic secret materialization

- **Query**: Summarize remaining local secret materialization areas after SSH local provider adoption and restic repository access centralization, and explain why AppCredential profile hook materialization is the next narrow P4 slice.
- **Scope**: internal
- **Date**: 2026-05-23

## Findings

### Completed seams

* SSH credential construction and executor SSH dialing now route through local-provider helpers with safe descriptors.
* Restic repository password consumers now route through a shared local repository access resolver and env-prefix helper.

### Remaining local secret materialization candidates

| Candidate | Current material | Why not this slice |
|---|---|---|
| AppCredential profile rendering | Encrypted AppCredential JSON is decrypted by model hooks, parsed in handlers, and rendered into policy hooks. | Best next fit: backend-only, local-only, concentrated call sites, no API/UI/deployment change required. |
| Integration notification dispatch/test | Encrypted integration endpoint/secret/proxy values are decrypted by model hooks and consumed by alert dispatch/test paths. | Viable later seam, but broader because it crosses delivery networking, proxy behavior, channel-specific sender code, and probe/error handling. |
| Rclone/executor config | Task executor config may contain backend-specific settings and command arguments. | Broader executor semantics and config import/export implications; restic was the narrow password-only subset already completed. |
| System settings secrets | Settings may override environment values and can contain runtime-sensitive values. | Requires setting-key policy and broader configuration semantics. |
| Config import/export | Explicit secret export/import handles multiple encrypted fields. | Provider-reference semantics are out of scope for the local-only slice. |

### Recommendation

Proceed with AppCredential profile hook materialization as the next P4 slice. Add a local resolver seam that returns in-memory profile config plus safe provider/kind/source labels, replace direct handler JSON parsing at current call sites, and preserve rendered hook output and API shape.

### Risks

* Existing behavior intentionally persists generated hooks on policies; this resolver seam does not remove that exposure.
* Existing tests intentionally assert that generated hooks can contain app password material; those tests should remain behavior-preserving.
* Resolver errors and metadata must remain generic and must not include raw JSON, app password material, rendered hook text, endpoints, hostnames, paths, or command/output strings.

### External References

No external references were used. This is based on current source, archived Trellis tasks, and backend specs.
