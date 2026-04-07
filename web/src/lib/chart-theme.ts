export const chartColors = {
  light: {
    series: ["hsl(0 0% 3.9%)", "hsl(240 3.8% 46.1%)", "hsl(240 5% 64.9%)"],
    grid: "hsl(240 4.8% 95.9%)",
    axis: "hsl(240 3.8% 46.1%)",
    success: "hsl(160 84% 39.4%)",
    error: "hsl(0 84.2% 60.2%)",
    tooltip: { bg: "#0a0a0a", text: "#fafafa", border: "none" }
  },
  dark: {
    series: ["hsl(0 0% 98%)", "hsl(240 3.8% 46.1%)", "hsl(240 3.7% 25%)"],
    grid: "hsl(240 3.7% 15.9%)",
    axis: "hsl(240 5% 64.9%)",
    success: "hsl(160 72% 52.4%)",
    error: "hsl(0 72% 51%)",
    tooltip: { bg: "#fafafa", text: "#0a0a0a", border: "none" }
  }
} as const;

export type ChartTheme = typeof chartColors.light;

export function getChartTheme(): ChartTheme {
  const isDark = document.documentElement.classList.contains("dark");
  return isDark ? chartColors.dark : chartColors.light;
}
