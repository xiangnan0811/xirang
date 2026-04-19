import { describe, test, expect } from "vitest";
import { buildAlertJumpHref } from "./alert-jump";

describe("buildAlertJumpHref", () => {
  test("builds a ±15min window around triggeredAt", () => {
    const href = buildAlertJumpHref({
      nodeId: 42,
      triggeredAt: "2026-04-17T10:00:00Z",
    });
    expect(href).toContain("/app/nodes/42?tab=metrics");
    const params = new URLSearchParams(href.split("?")[1]);
    const from = new Date(params.get("from") ?? "");
    const to = new Date(params.get("to") ?? "");
    expect((to.getTime() - from.getTime()) / 60000).toBe(30);
  });

  test("falls back to no window when triggeredAt is unparseable", () => {
    const href = buildAlertJumpHref({
      nodeId: 42,
      triggeredAt: "",
    });
    expect(href).toBe("/app/nodes/42?tab=metrics");
  });
});
