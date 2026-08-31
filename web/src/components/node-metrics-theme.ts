import { CHART_TOKEN_VARS } from "@/lib/chart-colors";

export const NODE_PALETTE = CHART_TOKEN_VARS.map((token) => {
  const color = `hsl(var(${token}))`;
  return { stroke: color, fill: color };
});

export type NodePaletteEntry = (typeof NODE_PALETTE)[number];
