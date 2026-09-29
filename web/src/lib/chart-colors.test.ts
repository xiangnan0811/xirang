import { describe, expect, it } from "vitest";
import { CHART_TOKEN_VARS, chartSeriesColors } from "./chart-colors";

describe("chart token palettes", () => {
  it("builds trend palettes from CSS chart tokens", () => {
    expect(chartSeriesColors()).toEqual(
      CHART_TOKEN_VARS.map((token) => `hsl(var(${token}))`),
    );
  });
});
