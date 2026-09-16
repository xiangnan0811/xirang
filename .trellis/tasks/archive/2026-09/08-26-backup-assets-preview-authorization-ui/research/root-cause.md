# Root cause — production content preview action is hidden

## Production evidence

- v0.50.9 has one active complete Catalog and one active complete Search generation, each with 60,515 indexed rows/documents.
- A narrow exact query returned one real opaque file AssetRef with complete/fresh coverage.
- The production data page opened that asset and rendered real metadata.
- The preview tab rendered “内容操作不可用”; no secret, path, name, or content is recorded here.

## Code evidence

- `backup-assets-workspace.tsx` computes `canPreview` by requiring `selectedCatalog.permissions.preview` in addition to content and renderer capability.
- Backend Catalog repository/status services intentionally create `PermissionsDTO{List: true}`; Preview and Download therefore serialize false.
- Parent acceptance PRD R3 explicitly locks Catalog permission projection as list-only and assigns preview authorization to the independent delivery-ticket/UI path.
- The delivery-ticket router independently uses `RBAC(backup_assets:preview)`.
- Backend role tests prove Admin and Operator own list+preview; Viewer/unknown own neither, and the ticket route rejects them before the feature gate.
- The selected asset falls back to the supported `metadata_hex` renderer when MIME is absent; that product needs sequential read, which the production recovery point exposes. Renderer selection is not the blocker.

## Root-cause classification

Cross-layer authorization-domain conflation: a Catalog browse projection field was treated as the content action authorization source. The upstream producer and parent acceptance contract are list-only, while a historical UI design assumed Catalog would provide per-action preview/download fields.

## Safe correction boundary

Correct only the frontend UI eligibility predicate. Require authenticated Admin/Operator, Catalog list, content availability, and exact renderer capability; then call the unchanged typed delivery-ticket API. The server remains authoritative. Do not change Catalog rows/DTOs, RBAC, ticket service, content delivery, or production data.
