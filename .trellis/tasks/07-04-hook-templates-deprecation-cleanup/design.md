# Hook Templates Deprecation Cleanup Design

## Scope and Boundaries

This task removes the legacy public hook-template contract while preserving the
current policy hook model. Users can still type and edit `preHook` and
`postHook` values in the policy editor, and app-aware backup profiles continue
to render internal hook scripts through `backend/internal/profile`.

The removed surface is limited to:

- Backend `GET /api/v1/hook-templates` route and handler.
- Frontend API wrapper method and `HookTemplate` domain type.
- Policy editor insert-template controls that depended on the deprecated route.
- Documentation and generated OpenAPI references to the removed endpoint.

## Data Flow and Contracts

Before cleanup, opening `PolicyEditorDialog` triggered four requests:
`getHookTemplates`, `listEscalationPolicies`, `getProfiles`, and
`getCredentials`. After cleanup it triggers only the live dependencies:
escalation policies, app-aware profiles, and credentials. Manual hook text
fields keep binding to the policy draft and continue to round-trip through the
existing policy API.

Backend route registration removes only `/api/v1/hook-templates`. The app-aware
profiles route remains `/api/v1/app-credentials/profiles` with its existing
RBAC and handler.

## Compatibility

This is an intentional public API removal of an endpoint already documented as
deprecated. Existing persisted policies are not migrated because they store
rendered hook values, not references to template IDs. Rollback is straightforward:
restore `hook_templates_handler.go`, the router registration, frontend API
method/type, the policy editor insert-template block, and documentation entries.

## Trade-Offs

Removing the endpoint now reduces maintenance and avoids a duplicate path for
application-aware hook setup. The trade-off is that clients still calling the
deprecated endpoint will receive 404 instead of a deprecation response. The repo
evidence shows the only source consumer is the policy editor, which this task
removes.
