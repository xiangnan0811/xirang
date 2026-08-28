import { http, HttpResponse, type HttpHandler } from "msw";

import fixture from "@/lib/api/__fixtures__/backup-assets.fixture.json";

const API_BASE = "/api/v1";

export type BackupAssetsFixtureScenario = "complete" | "partial_offline" | "feature_disabled";

export const backupAssetsFixtureIds = fixture.ids;
export const backupAssetsFileSourceIds = {
  online: { nodeId: 17, backupSetId: "12121212121212121212121212121212" },
  offline: { nodeId: 18, backupSetId: "34343434343434343434343434343434" },
} as const;

export function createBackupAssetsHandlers(
  scenario: BackupAssetsFixtureScenario = "complete"
): HttpHandler[] {
  const offline = scenario === "partial_offline";
  const repositoryId = offline ? fixture.ids.offlineRepository : fixture.ids.onlineRepository;
  const recoveryPoints = offline ? fixture.recoveryPoints.offline : fixture.recoveryPoints.online;
  const sourceIds = offline ? backupAssetsFileSourceIds.offline : backupAssetsFileSourceIds.online;
  const sourceNodeId = sourceIds.nodeId;
  const sourceNodeName = offline ? "synthetic-node-18" : "synthetic-node-17";
  const sourceTaskId = offline ? 72 : 71;
  const sourceSetId = sourceIds.backupSetId;

  return [
    http.get(`${API_BASE}/overview/backup-health`, () => ok(fixture.overview.backupHealth)),
    http.get(`${API_BASE}/overview/backup-confidence`, () => ok(fixture.overview.backupConfidence)),
    http.get(`${API_BASE}/overview/storage-usage`, () => ok(fixture.overview.storageUsage)),

    http.get(`${API_BASE}/backup-file-sources/recovery-points/:recoveryPointId/source`, ({ params }) => {
      if (scenario === "feature_disabled") return featureDisabled();
      const point = recoveryPoints.find((candidate) => candidate.id === params.recoveryPointId);
      return point
        ? ok({
            node_id: sourceNodeId,
            backup_set_id: sourceSetId,
            recovery_point_id: point.id,
            repository_id: point.repository_id,
            producing_task_id: sourceTaskId,
            browse_state: offline ? "unavailable" : "browsable",
            unavailable_reason: offline ? { code: "repository_offline" } : null,
          })
        : notFound();
    }),
    http.get(`${API_BASE}/backup-file-sources/nodes`, () => {
      if (scenario === "feature_disabled") return featureDisabled();
      const latestPoint = recoveryPoints[0];
      return ok({
        items: latestPoint ? [{
          node_id: sourceNodeId,
          display_name: sourceNodeName,
          backup_set_count: 1,
          retained_version_count: recoveryPoints.length,
          latest_retained_at: latestPoint.committed_at,
          catalog_coverage: latestPoint.catalog.coverage.status,
          browse_state: offline ? "unavailable" : "browsable",
          unavailable_reason: offline ? { code: "repository_offline" } : null,
        }] : [],
        next_cursor: null,
      });
    }),
    http.get(`${API_BASE}/backup-file-sources/nodes/:nodeId/sets`, ({ params }) => {
      if (scenario === "feature_disabled") return featureDisabled();
      const latestPoint = recoveryPoints[0];
      return ok({
        items: Number(params.nodeId) === sourceNodeId && latestPoint ? [{
          backup_set_id: sourceSetId,
          node_id: sourceNodeId,
          display_label: offline ? "Synthetic offline archive" : "Synthetic nightly archive",
          lineage_kind: "task",
          version_count: recoveryPoints.length,
          latest_retained_at: latestPoint.committed_at,
          catalog_coverage: latestPoint.catalog.coverage.status,
          browse_state: offline ? "unavailable" : "browsable",
          unavailable_reason: offline ? { code: "repository_offline" } : null,
        }] : [],
        next_cursor: null,
      });
    }),
    http.get(`${API_BASE}/backup-file-sources/sets/:backupSetId/versions`, ({ params }) => {
      if (scenario === "feature_disabled") return featureDisabled();
      return ok({
        items: params.backupSetId === sourceSetId ? recoveryPoints.map((point) => ({
          recovery_point_id: point.id,
          repository_id: point.repository_id,
          producing_task_id: sourceTaskId,
          captured_at: point.captured_at,
          committed_at: point.committed_at,
          created_at: point.created_at,
          lifecycle_state: point.state,
          catalog_coverage: point.catalog.coverage.status,
          browse_state: offline ? "unavailable" : "browsable",
          unavailable_reason: offline ? { code: "repository_offline" } : null,
          content_availability: point.catalog.content_availability,
          entry_count: point.entry_count,
          logical_bytes: point.logical_bytes,
          permissions: { list: true, preview: false, download: false },
        })) : [],
        next_cursor: null,
      });
    }),

    http.get(`${API_BASE}/backup-repositories`, () => {
      if (scenario === "feature_disabled") {
        return HttpResponse.json(
          { code: 503, message: "unavailable", data: { code: "feature_disabled" } },
          { status: 503 }
        );
      }
      return ok({ items: fixture.repositories, next_cursor: null });
    }),

    http.get(`${API_BASE}/backup-repositories/:repositoryId/recovery-points`, ({ params }) => {
      if (params.repositoryId !== repositoryId) return ok({ items: [], next_cursor: null });
      return ok({ items: recoveryPoints, next_cursor: null });
    }),

    http.get(`${API_BASE}/recovery-points/:recoveryPointId`, ({ params }) => {
      const point = [...fixture.recoveryPoints.online, ...fixture.recoveryPoints.offline].find(
        (candidate) => candidate.id === params.recoveryPointId
      );
      return point ? ok(point) : notFound();
    }),

    http.get(`${API_BASE}/recovery-points/:recoveryPointId/catalog-status`, ({ params }) => {
      const point = [...fixture.recoveryPoints.online, ...fixture.recoveryPoints.offline].find(
        (candidate) => candidate.id === params.recoveryPointId
      );
      return point ? ok(point.catalog) : notFound();
    }),

    http.get(`${API_BASE}/recovery-points/:recoveryPointId/entries`, ({ params, request }) => {
      const recoveryPointId = String(params.recoveryPointId);
      const parentEntryId = new URL(request.url).searchParams.get("parent");
      if (params.recoveryPointId === fixture.ids.offlineRecoveryPoint) {
        return ok({
          items: [], next_cursor: null,
          directory: { current: null, parent: null, breadcrumb: [] },
        });
      }
      if (recoveryPointId === fixture.ids.onlineRecoveryPoint && parentEntryId === fixture.ids.directoryEntry) {
        const directory = fixture.entries.find((entry) => entry.entry_id === parentEntryId);
        return ok({
          items: [],
          next_cursor: null,
          directory: {
            current: {
              recovery_point_id: recoveryPointId,
              entry_id: parentEntryId,
              name: directory?.name ?? "synthetic-directory",
            },
            parent: null,
            breadcrumb: [{
              recovery_point_id: recoveryPointId,
              entry_id: parentEntryId,
              name: directory?.name ?? "synthetic-directory",
            }],
          },
        });
      }
      const items = recoveryPointId === fixture.ids.onlineRecoveryPoint ? fixture.entries : [];
      return ok({
        items,
        next_cursor: null,
        directory: { current: null, parent: null, breadcrumb: [] },
      });
    }),

    http.get(`${API_BASE}/recovery-points/:recoveryPointId/entries/:entryId`, ({ params }) => {
      const entry = fixture.entries.find(
        (candidate) =>
          candidate.recovery_point_id === params.recoveryPointId && candidate.entry_id === params.entryId
      );
      return entry ? ok(entry) : notFound();
    }),

    http.get(`${API_BASE}/recovery-points/:recoveryPointId/evidence`, ({ params }) =>
      params.recoveryPointId === fixture.ids.onlineRecoveryPoint ? ok(fixture.evidence) : notFound()
    ),
    http.post(`${API_BASE}/recovery-point-diffs`, () => ok(fixture.diff)),

    http.get(`${API_BASE}/asset-saved-searches`, () =>
      ok({ items: fixture.overlays.savedSearches, next_cursor: null })
    ),
    http.get(`${API_BASE}/asset-favorites`, () =>
      ok({ items: fixture.overlays.favorites, next_cursor: null })
    ),
    http.get(`${API_BASE}/asset-tags`, () => ok({ items: fixture.overlays.tags, next_cursor: null })),
    http.get(`${API_BASE}/asset-recent`, () => ok({ items: fixture.overlays.recent, next_cursor: null })),

    http.post(`${API_BASE}/asset-search`, () =>
      ok({
        query_generation: "d".repeat(64),
        indexes: [
          {
            recovery_point_id: fixture.ids.onlineRecoveryPoint,
            catalog_generation_id: "10101010101010101010101010101010",
            search_generation_id: "40404040404040404040404040404040",
            projection_revision: 1,
            coverage: "complete",
            staleness: "fresh"
          }
        ],
        items: fixture.entries.slice(0, 2).map((asset) => ({
          ref: {
            recovery_point_id: asset.recovery_point_id,
            entry_id: asset.entry_id
          },
          asset,
          hit_fields: ["name"],
          score: 100,
          snippet: null
        })),
        next_cursor: null,
        total: 2,
        total_relation: "exact",
        authoritative_empty: false,
        coverage: { status: "complete" },
        suggestions: [],
        capabilities: { metadata: true, content: false },
        permissions: { list: true, secret_reveal: false }
      })
    ),

    http.post(`${API_BASE}/recovery-points/:recoveryPointId/entries/:entryId/delivery-tickets`, async ({ request }) => {
      const body = await request.json();
      if (!isTicketBody(body)) return badRequest();
      const safePreview = "preview_intent" in body;
      const renderer = safePreview ? "plain_text" : body.renderer;
      const profile = safePreview ? "text_v2" : body.profile;
      const range = renderer === "escaped_text" || renderer === "plain_text" || renderer === "metadata_hex" ? "none" : "single";
      const contentType = ticketContentType(renderer);
      const expiresAt = new Date(Date.now() + 10 * 60 * 1000).toISOString();
      const idleExpiresAt = new Date(Date.now() + 5 * 60 * 1000).toISOString();
      return ok({
        schema_version: 1,
        content_url: `/api/v1/asset-content/${"5".repeat(32)}`,
        action: body.action,
        renderer,
        profile,
        content_type: contentType,
        content_length: 128,
        truncated: false,
        etag: '"synthetic-ticket-v1"',
        last_modified: "2026-07-19T00:00:00Z",
        range,
        classification: body.action === "download" ? "secret" : "non_secret",
        expires_at: expiresAt,
        idle_expires_at: idleExpiresAt,
        capability_reason: null,
        fallback_actions: []
      });
    }),
    http.get(`${API_BASE}/asset-content/:deliveryId`, () =>
      new HttpResponse("Synthetic escaped preview fixture", {
        headers: { "Content-Type": "text/plain; charset=utf-8" }
      })
    )
  ];
}

function ok(data: unknown) {
  return HttpResponse.json({ code: 0, message: "ok", data });
}

function notFound() {
  return HttpResponse.json({ code: 404, message: "not found", data: null }, { status: 404 });
}

function badRequest() {
  return HttpResponse.json({ code: 400, message: "bad request", data: null }, { status: 400 });
}

function featureDisabled() {
  return HttpResponse.json(
    { code: 503, message: "unavailable", data: { reason: { code: "feature_disabled", params: {} } } },
    { status: 503 }
  );
}

type TicketBody =
  | { action: "preview"; preview_intent: "safe_preview_v1" }
  | { action: string; renderer: string; profile: string };

function isTicketBody(value: unknown): value is TicketBody {
  if (value === null || typeof value !== "object" || Array.isArray(value)) return false;
  const body = value as Record<string, unknown>;
  return body.action === "preview" && body.preview_intent === "safe_preview_v1" || (
    typeof body.action === "string" &&
    typeof body.renderer === "string" &&
    typeof body.profile === "string"
  );
}

function ticketContentType(renderer: string): string {
  const contentTypes: Record<string, string> = {
    escaped_text: "text/plain; charset=utf-8",
    plain_text: "text/plain; charset=utf-8",
    safe_raster: "image/png",
    same_origin_pdf: "application/pdf",
    native_audio: "audio/mpeg",
    native_video: "video/mp4",
    metadata_hex: "text/plain; charset=utf-8",
    attachment: "application/octet-stream"
  };
  return contentTypes[renderer] ?? "application/octet-stream";
}
