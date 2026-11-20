import { beforeEach, describe, expect, it, vi } from "vitest";

import { request } from "./core";
import {
  createBackupFileSourcesApi,
  mapBackupFileSourceComparisonPage,
  mapBackupFileSourceVersionPage,
} from "./backup-file-sources-api";

vi.mock("./core", async () => ({
  ...(await vi.importActual<typeof import("./core")>("./core")),
  request: vi.fn(),
}));

const requestMock = vi.mocked(request);
const setId = "a".repeat(32);
const basePoint = "b".repeat(32);
const comparePoint = "d".repeat(32);
const cursor = "eyJvZmZzZXQiOjEwfQ.signature_1";
const entry = (char: string) => char.repeat(64);
const blocked = { status: "blocked", reason: { code: "unknown_internal_state", params: {} } };
const expected = { baseRecoveryPointId: basePoint, compareRecoveryPointId: comparePoint };

const rawVersion = {
  recovery_point_id: basePoint,
  repository_id: "c".repeat(32),
  captured_at: "2026-08-27T00:00:00Z",
  committed_at: null,
  created_at: "2026-08-27T00:00:30Z",
  lifecycle_state: "committed",
  catalog_coverage: "complete",
  content_availability: { available: true, reason: null },
  entry_count: 12,
  logical_bytes: 4096,
  permissions: { list: true, preview: false, download: false },
  browse_state: "browsable",
};

/** Production scoped side: no raw mode, owner or MIME type, and `breadcrumb_truncated` omitted unless true. */
function side(point: string, entryId: string, overrides: Record<string, unknown> = {}) {
  return {
    recovery_point_id: point,
    entry_id: entryId,
    name: "report.txt",
    entry_type: "file",
    size: 10,
    modified_at: "2026-08-27T00:00:00Z",
    fingerprint_strength: "strong",
    breadcrumb: [
      { recovery_point_id: point, entry_id: entry("1"), name: "docs" },
      { recovery_point_id: point, entry_id: entry("2"), name: "2026" },
    ],
    ...overrides,
  };
}

function ancestors(point: string, count: number, idOffset = 0) {
  return Array.from({ length: count }, (_, index) => ({
    recovery_point_id: point,
    entry_id: (index + 1 + idOffset).toString(16).padStart(64, "0"),
    name: `d${index}`,
  }));
}

/** A truncated side: the nearest 256 ancestors plus the root-first names above them. */
function deepSide(entryId: string, omitted: unknown, overrides: Record<string, unknown> = {}) {
  return side(comparePoint, entryId, {
    breadcrumb: ancestors(comparePoint, 256), breadcrumb_truncated: true, omitted_ancestor_names: omitted, ...overrides,
  });
}

// UTF-8 bytes of the shown tail `d0/…/d255/report.txt`; one omitted root adds its name and a separator.
const deepTailBytes = new TextEncoder().encode([...ancestors(comparePoint, 256).map((crumb) => crumb.name), "report.txt"].join("/")).length;
const maximalRootBytes = 8192 - deepTailBytes - 1;
const maximalRoot = `${"é".repeat(Math.floor(maximalRootBytes / 2))}${maximalRootBytes % 2 === 1 ? "a" : ""}`;

const modified = {
  kind: "modified",
  base: side(basePoint, entry("a")),
  compare: side(comparePoint, entry("b"), { size: 12 }),
  changed_fields: ["size", "content"],
  content_equality: "different",
};
const added = {
  kind: "added",
  base: null,
  compare: side(comparePoint, entry("c"), { name: "new.txt", breadcrumb: [] }),
  changed_fields: [],
  content_equality: "unknown",
};

function page(items: unknown[], overrides: Record<string, unknown> = {}) {
  return {
    base_recovery_point_id: basePoint,
    compare_recovery_point_id: comparePoint,
    base_content_availability: { available: true, reason: null },
    compare_content_availability: { available: false, reason: { code: "repository_offline", params: {} } },
    items,
    ...overrides,
  };
}

describe("backup file source comparison boundary", () => {
  beforeEach(() => requestMock.mockReset());

  it("treats a pre-upgrade version without comparison_state as ineligible and rejects unknown states", () => {
    expect(mapBackupFileSourceVersionPage({ items: [rawVersion] }))
      .toMatchObject({ status: "available", value: { items: [{ comparisonState: "ineligible" }] } });
    for (const state of ["eligible", "indexing", "unavailable", "ineligible"]) {
      expect(mapBackupFileSourceVersionPage({ items: [{ ...rawVersion, comparison_state: state }] }))
        .toMatchObject({ status: "available", value: { items: [{ comparisonState: state }] } });
    }
    expect(mapBackupFileSourceVersionPage({ items: [{ ...rawVersion, comparison_state: "future" }] })).toEqual(blocked);
    expect(mapBackupFileSourceVersionPage({ items: [{ ...rawVersion, comparison_state: null }] })).toEqual(blocked);
  });

  it("maps a complete page with per-side availability and ancestor breadcrumbs, stripping unknown fields", () => {
    const leakedMetadata = { mode: "PRIVATE-mode", owner: "PRIVATE /home/alice token=abc", mime_type: "PRIVATE/type" };
    const mapped = mapBackupFileSourceComparisonPage(page(
      [{ ...modified, base: { ...modified.base, ...leakedMetadata }, private_path: "/PRIVATE" }, added],
      { next_cursor: cursor, provider_locator: "PRIVATE" },
    ), expected);

    expect(mapped).toEqual({
      status: "available",
      value: {
        baseRecoveryPointId: basePoint,
        compareRecoveryPointId: comparePoint,
        baseContentAvailability: { available: true, reason: null },
        compareContentAvailability: { available: false, reason: { code: "repository_offline", params: {} } },
        nextCursor: cursor,
        items: [
          {
            kind: "modified",
            base: {
              ref: { recoveryPointId: basePoint, entryId: entry("a") },
              name: "report.txt",
              entryType: "file",
              size: 10,
              modifiedAt: "2026-08-27T00:00:00.000Z",
              fingerprintStrength: "strong",
              breadcrumb: [
                { ref: { recoveryPointId: basePoint, entryId: entry("1") }, name: "docs" },
                { ref: { recoveryPointId: basePoint, entryId: entry("2") }, name: "2026" },
              ],
              omittedAncestorNames: [],
            },
            compare: expect.objectContaining({ ref: { recoveryPointId: comparePoint, entryId: entry("b") }, size: 12 }),
            changedFields: ["size", "content"],
            contentEquality: "different",
          },
          expect.objectContaining({ kind: "added", base: null, compare: expect.objectContaining({ breadcrumb: [] }) }),
        ],
      },
    });
    expect(JSON.stringify(mapped)).not.toContain("PRIVATE");
  });

  it("maps an empty complete page as no reported differences", () => {
    expect(mapBackupFileSourceComparisonPage(page([]), expected))
      .toMatchObject({ status: "available", value: { items: [], nextCursor: null } });
  });

  it.each([
    ["swapped point echo", page([], { base_recovery_point_id: comparePoint, compare_recovery_point_id: basePoint })],
    ["missing availability", page([], { base_content_availability: undefined })],
    ["availability without reason", page([], { compare_content_availability: { available: false, reason: null } })],
    ["available with a reason", page([], { base_content_availability: { available: true, reason: { code: "repository_offline", params: {} } } })],
    ["malformed cursor", page([modified], { next_cursor: "raw/path?x=1" })],
    ["cursor on an empty page", page([], { next_cursor: cursor })],
    ["oversized page", page(Array.from({ length: 201 }, () => added))],
    ["side from the other point", page([{ ...modified, base: side(comparePoint, entry("a")) }])],
    ["breadcrumb from the other point", page([{ ...added, compare: side(comparePoint, entry("c"), { breadcrumb: [{ recovery_point_id: basePoint, entry_id: entry("1"), name: "docs" }] }) }])],
    ["breadcrumb containing the item", page([{ ...added, compare: side(comparePoint, entry("c"), { breadcrumb: [{ recovery_point_id: comparePoint, entry_id: entry("c"), name: "x" }] }) }])],
    ["missing breadcrumb", page([{ ...added, compare: { ...side(comparePoint, entry("c")), breadcrumb: undefined } }])],
    ["missing side", page([{ ...modified, base: undefined }])],
    ["added with a base", page([{ ...added, base: side(basePoint, entry("e")) }])],
    ["added with changed fields", page([{ ...added, changed_fields: ["size"] }])],
    ["added with content certainty", page([{ ...added, content_equality: "equal" }])],
    ["modified without changes", page([{ ...modified, compare: side(comparePoint, entry("b")), changed_fields: [], content_equality: "equal" }])],
    ["changed field not matching sides", page([{ ...modified, changed_fields: ["owner", "content"] }])],
    ["duplicated changed field", page([{ ...modified, changed_fields: ["size", "size", "content"] }])],
    ["unknown changed field", page([{ ...modified, changed_fields: ["size", "path", "content"] }])],
    ["content change without different equality", page([{ ...modified, content_equality: "unknown" }])],
    ["content certainty from weak evidence", page([{
      ...modified,
      base: side(basePoint, entry("a"), { fingerprint_strength: "weak" }),
      compare: side(comparePoint, entry("b"), { size: 12, fingerprint_strength: "weak" }),
    }])],
    ["type_changed without a type change", page([{ ...modified, kind: "type_changed" }])],
    ["fingerprint_strength advertised alone", page([{
      ...modified,
      compare: side(comparePoint, entry("b"), { fingerprint_strength: "none" }),
      changed_fields: ["fingerprint_strength"],
      content_equality: "unknown",
    }])],
    ["fingerprint_strength advertised with metadata", page([{
      ...modified,
      compare: side(comparePoint, entry("b"), { size: 12, fingerprint_strength: "none" }),
      changed_fields: ["size", "fingerprint_strength"],
      content_equality: "unknown",
    }])],
    ["content certainty across mixed strengths", page([{
      ...modified,
      compare: side(comparePoint, entry("b"), { size: 12, fingerprint_strength: "none" }),
    }])],
    ["breadcrumb_truncated other than literal true", page([{ ...added, compare: side(comparePoint, entry("c"), { breadcrumb_truncated: false }) }])],
    ["string breadcrumb_truncated", page([{ ...added, compare: deepSide(entry("c"), ["srv"], { breadcrumb_truncated: "true" }) }])],
    ["truncated breadcrumb shorter than the bounded tail", page([{ ...added, compare: deepSide(entry("c"), ["srv"], { breadcrumb: ancestors(comparePoint, 255) }) }])],
    ["truncated breadcrumb without omitted ancestor names", page([{ ...added, compare: deepSide(entry("c"), undefined) }])],
    ["truncated breadcrumb with an empty omitted prefix", page([{ ...added, compare: deepSide(entry("c"), []) }])],
    ["null omitted names on a truncated breadcrumb", page([{ ...added, compare: deepSide(entry("c"), null) }])],
    ["omitted names that are not an array", page([{ ...added, compare: deepSide(entry("c"), "srv/data") }])],
    ["omitted names on a complete breadcrumb", page([{ ...added, compare: side(comparePoint, entry("c"), { breadcrumb: ancestors(comparePoint, 256), omitted_ancestor_names: ["srv"] }) }])],
    ["empty omitted names on a complete breadcrumb", page([{ ...added, compare: side(comparePoint, entry("c"), { omitted_ancestor_names: [] }) }])],
    ["null omitted names on a complete breadcrumb", page([{ ...added, compare: side(comparePoint, entry("c"), { omitted_ancestor_names: null }) }])],
    ["empty omitted name", page([{ ...added, compare: deepSide(entry("c"), ["srv", ""]) }])],
    ["dot-dot omitted name", page([{ ...added, compare: deepSide(entry("c"), [".."]) }])],
    ["omitted name with a separator", page([{ ...added, compare: deepSide(entry("c"), ["srv/data"]) }])],
    ["omitted name with NUL", page([{ ...added, compare: deepSide(entry("c"), ["srv\0"]) }])],
    ["non-string omitted name", page([{ ...added, compare: deepSide(entry("c"), [7]) }])],
    ["omitted name as a breadcrumb reference", page([{ ...added, compare: deepSide(entry("c"), [{ recovery_point_id: comparePoint, entry_id: entry("9"), name: "srv" }]) }])],
    ["whole path beyond 8192 UTF-8 bytes once separators count", page([{ ...added, compare: deepSide(entry("c"), [`${maximalRoot}a`]) }])],
    ["one invalid side among valid deep rows", page([
      { ...added, compare: deepSide(entry("c"), ["srv"]) },
      { ...added, compare: deepSide(entry("e"), undefined, { breadcrumb: ancestors(comparePoint, 256, 300) }) },
    ])],
    ["breadcrumb beyond the bound", page([{ ...added, compare: side(comparePoint, entry("c"), { breadcrumb: ancestors(comparePoint, 257) }) }])],
    ["empty name", page([{ ...added, compare: side(comparePoint, entry("c"), { name: "" }) }])],
    ["dot name", page([{ ...added, compare: side(comparePoint, entry("c"), { name: "." }) }])],
    ["dot-dot ancestor", page([{ ...added, compare: side(comparePoint, entry("c"), { breadcrumb: [{ recovery_point_id: comparePoint, entry_id: entry("1"), name: ".." }] }) }])],
    ["name with a separator", page([{ ...added, compare: side(comparePoint, entry("c"), { name: "a/b" }) }])],
    ["name with a backslash", page([{ ...added, compare: side(comparePoint, entry("c"), { name: "a\\b" }) }])],
    ["name with NUL", page([{ ...added, compare: side(comparePoint, entry("c"), { name: "a\0b" }) }])],
    ["name beyond 8192 UTF-8 bytes", page([{ ...added, compare: side(comparePoint, entry("c"), { name: `${"é".repeat(4096)}a` }) }])],
    ["present non-object reason params", page([], { compare_content_availability: { available: false, reason: { code: "repository_offline", params: null } } })],
    ["unknown reason code without params", page([], { compare_content_availability: { available: false, reason: { code: "future_code" } } })],
    ["modified with a type change", page([{
      ...modified,
      compare: side(comparePoint, entry("b"), { entry_type: "directory", size: 12 }),
      changed_fields: ["entry_type", "size"],
      content_equality: "unknown",
    }])],
    ["duplicated side entry", page([added, { ...added, compare: side(comparePoint, entry("c"), { name: "again.txt" }) }])],
  ])("blocks the whole page for %s", (_label, value) => {
    expect(mapBackupFileSourceComparisonPage(value, expected)).toEqual(blocked);
  });

  it("blocks a continuation that returns the cursor it was given", () => {
    expect(mapBackupFileSourceComparisonPage(page([modified], { next_cursor: cursor }), { ...expected, cursor })).toEqual(blocked);
  });

  it("maps a type change with unknown content and keeps metadata labels without raw values", () => {
    const typeChanged = {
      kind: "type_changed",
      base: side(basePoint, entry("a"), { entry_type: "directory", fingerprint_strength: "none" }),
      compare: side(comparePoint, entry("b")),
      changed_fields: ["entry_type", "mime_type"],
      content_equality: "unknown",
    };
    expect(mapBackupFileSourceComparisonPage(page([typeChanged]), expected)).toMatchObject({
      status: "available",
      value: { items: [{ kind: "type_changed", changedFields: ["entry_type", "mime_type"], contentEquality: "unknown" }] },
    });
  });

  it("accepts a server-produced mixed-strength row with unknown content and no strength label", () => {
    // A size change where one generation recorded no fingerprint and the other a strong one.
    const mixed = {
      kind: "modified",
      base: side(basePoint, entry("a"), { fingerprint_strength: "none" }),
      compare: side(comparePoint, entry("b"), { size: 12, modified_at: "2026-08-28T00:00:00Z" }),
      changed_fields: ["size", "modified_at", "owner"],
      content_equality: "unknown",
    };
    expect(mapBackupFileSourceComparisonPage(page([mixed]), expected)).toMatchObject({
      status: "available",
      value: {
        items: [{
          kind: "modified",
          base: { fingerprintStrength: "none" },
          compare: { fingerprintStrength: "strong", size: 12 },
          changedFields: ["size", "modified_at", "owner"],
          contentEquality: "unknown",
        }],
      },
    });
  });

  it("maps a bounded nearest-ancestor tail with its omitted root-first names", () => {
    const tail = ancestors(comparePoint, 256);
    const mapped = mapBackupFileSourceComparisonPage(
      page([{ ...added, compare: deepSide(entry("c"), ["srv", "   "], { normalized_path: "PRIVATE/srv/data", provider_path: "/PRIVATE" }) }]),
      expected,
    );
    expect(mapped.status).toBe("available");
    if (mapped.status !== "available") return;
    const compare = mapped.value.items[0].compare;
    expect(compare?.omittedAncestorNames).toEqual(["srv", "   "]);
    expect(compare?.breadcrumb).toHaveLength(256);
    expect(compare?.breadcrumb[0].name).toBe("d0");
    expect(compare?.breadcrumb[255].name).toBe("d255");
    expect(JSON.stringify(mapped)).not.toContain("PRIVATE");

    const complete = mapBackupFileSourceComparisonPage(
      page([{ ...added, compare: side(comparePoint, entry("c"), { breadcrumb: tail }) }]),
      expected,
    );
    expect(complete).toMatchObject({ status: "available", value: { items: [{ compare: { omittedAncestorNames: [] } }] } });
  });

  it("keeps two deep paths with identical tails and basenames distinct by their omitted names", () => {
    const mapped = mapBackupFileSourceComparisonPage(page([
      { ...added, compare: deepSide(entry("c"), ["srv", "alpha"]) },
      { ...added, compare: deepSide(entry("e"), ["srv", "beta"], { breadcrumb: ancestors(comparePoint, 256, 300) }) },
    ]), expected);
    expect(mapped).toMatchObject({
      status: "available",
      value: {
        items: [
          { compare: { name: "report.txt", omittedAncestorNames: ["srv", "alpha"], breadcrumb: expect.arrayContaining([{ ref: expect.anything(), name: "d255" }]) } },
          { compare: { name: "report.txt", omittedAncestorNames: ["srv", "beta"], breadcrumb: expect.arrayContaining([{ ref: expect.anything(), name: "d255" }]) } },
        ],
      },
    });
  });

  it("accepts a deep path at exactly 8192 UTF-8 bytes including separators", () => {
    expect(new TextEncoder().encode(maximalRoot).length + 1 + deepTailBytes).toBe(8192);
    expect(mapBackupFileSourceComparisonPage(page([{ ...added, compare: deepSide(entry("c"), [maximalRoot]) }]), expected))
      .toMatchObject({ status: "available", value: { items: [{ compare: { omittedAncestorNames: [maximalRoot] } }] } });
  });

  it("keeps whitespace-only and maximal names verbatim up to the whole-path byte bound", () => {
    // `\t/<maximal>/   ` is exactly 8192 UTF-8 bytes (1 + 1 + 8186 + 1 + 3); one more space exceeds it.
    const maximal = "é".repeat(4093);
    expect(mapBackupFileSourceComparisonPage(page([{
      ...added,
      compare: side(comparePoint, entry("c"), {
        name: "    ",
        breadcrumb: [
          { recovery_point_id: comparePoint, entry_id: entry("1"), name: "\t" },
          { recovery_point_id: comparePoint, entry_id: entry("2"), name: maximal },
        ],
      }),
    }]), expected)).toEqual(blocked);
    const mapped = mapBackupFileSourceComparisonPage(page([{
      ...added,
      compare: side(comparePoint, entry("c"), {
        name: "   ",
        breadcrumb: [
          { recovery_point_id: comparePoint, entry_id: entry("1"), name: "\t" },
          { recovery_point_id: comparePoint, entry_id: entry("2"), name: maximal },
        ],
      }),
    }]), expected);
    expect(mapped).toMatchObject({
      status: "available",
      value: { items: [{ compare: { name: "   ", breadcrumb: [{ name: "\t" }, { name: maximal }] } }] },
    });
  });

  it("accepts production availability reasons whose empty params map is omitted", () => {
    const mapped = mapBackupFileSourceComparisonPage(page([], {
      compare_content_availability: { available: false, reason: { code: "repository_offline" } },
    }), expected);
    expect(mapped).toMatchObject({
      status: "available",
      value: { compareContentAvailability: { available: false, reason: { code: "repository_offline", params: {} } } },
    });
  });

  it("posts only the strict ordered pair body to the set-scoped endpoint", async () => {
    requestMock.mockResolvedValue(page([modified], { next_cursor: cursor }));
    const signal = new AbortController().signal;
    const result = await createBackupFileSourcesApi().compareBackupFileSourceVersions("token", setId, {
      baseRecoveryPointId: basePoint,
      compareRecoveryPointId: comparePoint,
      limit: 100,
      signal,
    });

    expect(result.status).toBe("available");
    expect(requestMock).toHaveBeenCalledWith(`/backup-file-sources/sets/${setId}/diffs`, {
      method: "POST",
      token: "token",
      signal,
      body: { base_recovery_point_id: basePoint, compare_recovery_point_id: comparePoint, limit: 100 },
    });

    requestMock.mockResolvedValue(page([added]));
    await createBackupFileSourcesApi().compareBackupFileSourceVersions("token", setId, {
      baseRecoveryPointId: basePoint,
      compareRecoveryPointId: comparePoint,
      cursor,
    });
    expect(requestMock).toHaveBeenLastCalledWith(`/backup-file-sources/sets/${setId}/diffs`, {
      method: "POST",
      token: "token",
      signal: undefined,
      body: { base_recovery_point_id: basePoint, compare_recovery_point_id: comparePoint, cursor },
    });
  });

  it("rejects non-canonical input before requesting", async () => {
    const api = createBackupFileSourcesApi();
    const pair = { baseRecoveryPointId: basePoint, compareRecoveryPointId: comparePoint };
    await expect(api.compareBackupFileSourceVersions("token", "latest", pair)).rejects.toThrow("invalid backup set id");
    await expect(api.compareBackupFileSourceVersions("token", setId, { ...pair, compareRecoveryPointId: basePoint }))
      .rejects.toThrow("invalid recovery point pair");
    await expect(api.compareBackupFileSourceVersions("token", setId, { ...pair, limit: 201 })).rejects.toThrow("invalid page limit");
    await expect(api.compareBackupFileSourceVersions("token", setId, { ...pair, cursor: "raw/path" })).rejects.toThrow("invalid cursor");
    expect(requestMock).not.toHaveBeenCalled();
  });
});
