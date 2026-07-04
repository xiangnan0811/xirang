# Hook templates 废弃路径清理

## Goal

Remove the deprecated hook template API and its remaining frontend dependency so
policy editing relies on the newer app-aware backup profiles path, reducing dead
API/UI surface without changing existing policy hook execution behavior.

## Requirements

- Remove the deprecated `GET /api/v1/hook-templates` backend route and its
  handler.
- Remove the policy editor's request to `getHookTemplates` and the advanced
  settings "insert template" UI that writes legacy pre/post hook snippets into
  the draft.
- Keep manual pre-hook and post-hook text fields working; existing saved policy
  hook values must remain editable.
- Keep app-aware backup profile and credential selection working through
  `GET /api/v1/app-credentials/profiles` and the existing credentials API.
- Remove now-unused frontend `HookTemplate` type and i18n keys tied only to the
  deleted UI.
- Update generated OpenAPI docs and user docs so they no longer advertise the
  deprecated endpoint as available.
- Do not remove `backend/internal/profile` template fields; they are still used
  internally to render app-aware profile hooks.

## Acceptance Criteria

- [ ] Router registration tests prove `GET /api/v1/hook-templates` is not
      registered and `GET /api/v1/app-credentials/profiles` remains registered.
- [ ] A policy editor test proves opening the dialog no longer calls
      `apiClient.getHookTemplates` and no insert-template UI is rendered.
- [ ] Source search finds no remaining deprecated API surface references:
      literal `hook-templates`, `getHookTemplates`,
      `policyEditor.insertTemplate`, `NewHookTemplatesHandler`,
      `HookTemplatesHandler`, or exported frontend `HookTemplate` type
      references in source docs. Internal app-aware profile fields such as
      `PreHookTemplate` are intentionally retained.
- [ ] Backend tests pass for the API/router and full backend package set.
- [ ] Frontend `npm run check` passes.
- [ ] Docs and generated Swagger output are refreshed or explicitly verified
      after route annotation removal.

## Notes

- Parent task requirement: remove deprecated hook templates only after replacing
  the frontend policy editor dependency on `GET /hook-templates`.
- Confirmed replacement: the policy editor already loads app-aware profiles via
  `apiClient.getProfiles(token)`, backed by
  `GET /api/v1/app-credentials/profiles`.
- This task is intentionally a contract removal, not a change to backup runner
  hook rendering.
