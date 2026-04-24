export interface ChartTheme {
  series: readonly [string, string, string];
  grid: string;
  axis: string;
  success: string;
  error: string;
  tooltip: { bg: string; text: string; border: string };
}

// Tooltip styling uses the card/border tokens so it blends with surrounding
// UI instead of flipping to an inverted chip. The old values (black chip on
// light, white chip on dark) were a Recharts-era default and looked jarring
// against our Sage paper-tone cards — matching the dashboards panel-renderer
// and nodes-detail TrendChart's CompactTooltip look.
const cardTooltip = {
  bg: "hsl(var(--card))",
  text: "hsl(var(--card-foreground))",
  border: "1px solid hsl(var(--border))",
};

export const chartColors = {
  light: {
    series: ["hsl(0 0% 3.9%)", "hsl(240 3.8% 46.1%)", "hsl(240 5% 64.9%)"],
    grid: "hsl(240 4.8% 95.9%)",
    axis: "hsl(240 3.8% 46.1%)",
    success: "hsl(160 84% 39.4%)",
    error: "hsl(0 84.2% 60.2%)",
    tooltip: cardTooltip,
  },
  dark: {
    series: ["hsl(0 0% 98%)", "hsl(240 3.8% 46.1%)", "hsl(240 3.7% 25%)"],
    grid: "hsl(240 3.7% 15.9%)",
    axis: "hsl(240 5% 64.9%)",
    success: "hsl(160 72% 52.4%)",
    error: "hsl(0 72% 51%)",
    tooltip: cardTooltip,
  }
} satisfies Record<string, ChartTheme>;

export function getChartTheme(): ChartTheme {
  const isDark = document.documentElement.classList.contains("dark");
  return isDark ? chartColors.dark : chartColors.light;
}
