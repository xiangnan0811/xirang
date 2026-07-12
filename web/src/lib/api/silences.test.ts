import { describe, expect, it } from "vitest";
import { mapSilence } from "./silences";

describe("silences mapper", () => {
  it("maps silence wire fields to camelCase and normalizes tags", () => {
    expect(
      mapSilence({
        id: 9,
        name: "maint",
        match_node_id: 3,
        match_category: "XR-NODE",
        match_tags: '["prod","edge"]',
        starts_at: "2026-07-01T00:00:00Z",
        ends_at: "2026-07-01T02:00:00Z",
        created_by: 1,
        note: "n",
        created_at: "2026-07-01T00:00:00Z",
        updated_at: "2026-07-01T00:00:00Z",
      }),
    ).toEqual({
      id: 9,
      name: "maint",
      matchNodeId: 3,
      matchCategory: "XR-NODE",
      matchTags: ["prod", "edge"],
      startsAt: "2026-07-01T00:00:00Z",
      endsAt: "2026-07-01T02:00:00Z",
      createdBy: 1,
      note: "n",
      createdAt: "2026-07-01T00:00:00Z",
      updatedAt: "2026-07-01T00:00:00Z",
    });
  });

  it("normalizes ids/tags and drops non-string tag entries", () => {
    expect(
      mapSilence({
        id: "12",
        name: null,
        match_node_id: "0",
        match_tags: ["prod", 1, null, "  edge  "] as unknown as string[],
        created_by: "3",
      } as never),
    ).toMatchObject({
      id: 12,
      name: "",
      matchNodeId: null,
      matchTags: ["prod", "edge"],
      createdBy: 3,
    });
  });
});
