# Add CPU Peak Clipping on Overview Chart

## Goal

Improve the readability of the Overview page node resources CPU chart by letting users view a clipped version that suppresses occasional 100% spikes, while keeping the raw metrics and default behavior unchanged.

## What I already know

- The noisy CPU chart lives in `web/src/components/node-metrics-chart.tsx` and is rendered from `NodeMetricsPanel` on `web/src/pages/overview-page.tsx`.
- `NodeMetricsPanel` builds per-metric chart data from raw samples in `chartDataByMetric`.
- `MetricChart` currently derives `yMax` directly from the visible raw values and feeds that into Recharts.
- Memory and disk charts do not have the same spike issue.
- The Overview traffic chart already uses explicit header buttons for visualization toggles, so a chart control would fit the existing UI pattern.
- User confirmed: use percentile clipping, default remains raw, and scope is limited to the Overview page CPU chart only.

## Assumptions (temporary)

- Clipping is a presentation-layer behavior only; raw samples remain unchanged.
- The clipped view should not affect backend storage or API payloads.
- If a clipped view is exposed, it will live in the Overview node resources card rather than elsewhere in the app.

## Open Questions

- None.

## Requirements (evolving)

- Add a user-facing raw/clipped display toggle for the Overview page node resources CPU chart only.
- Use percentile clipping for the clipped CPU display.
- Keep raw CPU data unchanged in the underlying data flow.
- Default to raw display.
- In clipped mode, draw the CPU curve and compute the y-axis from clipped display values.
- In clipped mode, keep tooltip values as raw sample values.
- Do not change memory or disk charts.
- Do not affect the dashboard or other pages.

## Acceptance Criteria (evolving)

- [ ] Overview CPU chart renders in raw mode by default.
- [ ] Overview CPU chart exposes an accessible raw/clipped display toggle.
- [ ] In clipped mode, outlier spikes no longer force the chart scale to 0-100.
- [ ] In clipped mode, CPU tooltip values remain raw sample values.
- [ ] Raw sample values remain unchanged in the source data.
- [ ] Memory/disk charts and non-overview pages remain unchanged.
- [ ] Tests cover y-axis scaling and the selected display mode semantics.

## Definition of Done

- Tests added or updated for the chart behavior change.
- Frontend lint/typecheck/test/build pass.
- UI behavior remains accessible and keyboard-friendly.
- No unnecessary changes to unrelated charts or pages.

## Out of Scope

- Backend metric changes.
- Memory or disk clipping.
- Dashboard CPU chart changes.
- Persisting visualization preference across sessions.

## Technical Approach

- Implement clipping as a derived display series in `MetricChart`, so raw data remains available for tooltip formatting and the chart can render clipped points without changing stored samples.
- Add an Overview-only CPU display-mode state/control in `NodeMetricsPanel` and pass it only to the CPU `MetricChart` instance.
- Use the same raw data for memory and disk charts.
- Keep tooltip formatting raw by looking up values from the original chart point when clipped display data is active.

## Decision (ADR-lite)

**Context**: The Overview node resources CPU chart has sporadic high spikes that compress the useful trend range.

**Decision**: Use percentile clipping for the Overview CPU chart only, keep the default raw view, preserve raw metrics in the underlying data flow, expose a raw/clipped toggle, and keep tooltip values raw in clipped mode.

**Consequences**: The chart becomes easier to read when users opt in, while the raw view and raw tooltip values preserve fidelity. The clipped line may no longer match the tooltip value for outlier points, so the control label must make the presentation mode clear.

## Technical Notes

- `web/src/components/node-metrics-panel.tsx`
- `web/src/components/node-metrics-chart.tsx`
- `web/src/pages/overview-page.tsx`
- `web/src/pages/overview-page.traffic.tsx` as a control-pattern reference
- Current raw data flow: `NodeMetricsPanel` builds `chartDataByMetric`, then `MetricChart` computes `yMax` from the visible values and renders the Recharts area chart.
