export const CHART_TOKEN_VARS = [
  "--chart-1",
  "--chart-2",
  "--chart-3",
  "--chart-ingress",
  "--chart-egress",
] as const;

export function cssChartColor(index: number): string {
  const token = CHART_TOKEN_VARS[index % CHART_TOKEN_VARS.length];
  return `hsl(var(${token}))`;
}

export function chartSeriesColors(length = CHART_TOKEN_VARS.length): string[] {
  return Array.from({ length }, (_, index) => cssChartColor(index));
}
