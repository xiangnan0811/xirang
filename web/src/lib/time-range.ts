export type TimeRange = "all" | "1h" | "24h" | "7d" | "30d";

// Ranges step back in local wall-clock time (setHours/setDate), so DST
// transitions keep the local hour; the bounds are sent as UTC ISO strings.
export function resolveTimeRange(range: TimeRange): { from?: string; to?: string } {
  if (range === "all") {
    return {};
  }

  const now = new Date();
  const from = new Date(now);
  if (range === "1h") {
    from.setHours(from.getHours() - 1);
  } else if (range === "24h") {
    from.setHours(from.getHours() - 24);
  } else if (range === "7d") {
    from.setDate(from.getDate() - 7);
  } else if (range === "30d") {
    from.setDate(from.getDate() - 30);
  }

  return { from: from.toISOString(), to: now.toISOString() };
}
