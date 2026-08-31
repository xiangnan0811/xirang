import { describe, expect, it } from "vitest";
import { NODE_PALETTE } from "@/components/node-metrics-theme";
import { CHART_TOKEN_VARS, chartSeriesColors } from "./chart-colors";

describe("chart token palettes", () => {
  it("builds node and trend palettes from CSS chart tokens", () => {
    expect(NODE_PALETTE.every((entry) => entry.stroke.startsWith("hsl(var(--chart-"))).toBe(true);
    expect(chartSeriesColors()).toEqual(
      CHART_TOKEN_VARS.map((token) => `hsl(var(${token}))`),
    );
  });
});
