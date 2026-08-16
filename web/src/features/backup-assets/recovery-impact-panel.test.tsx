import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import type { RecoveryPreflight } from "@/lib/api/backup-recovery-api";

import { RecoveryImpactPanel } from "./recovery-impact-panel";

function preflight(overrides: Partial<RecoveryPreflight> = {}): RecoveryPreflight {
  return {
    planId: "1".repeat(32),
    persisted: true,
    planRevision: "12",
    eligible: true,
    preferred: false,
    reasons: [],
    preflightId: "2".repeat(32),
    targetMode: "in_place",
    conflictPolicy: "exact_mirror",
    impact: {
      createCount: 2,
      overwriteCount: 3,
      skipCount: 1,
      deleteCount: 4,
      estimatedItems: 10,
      estimatedBytes: 2048,
    },
    security: {
      decision: "allow_clean",
      findingCount: 0,
      overridableCategories: [],
    },
    observedAt: "2026-08-16T12:00:00Z",
    expiresAt: "2026-08-16T12:05:00Z",
    ...overrides,
  };
}

describe("RecoveryImpactPanel", () => {
  it("renders the authoritative destructive impact and keeps its expiry out of live regions", () => {
    render(<RecoveryImpactPanel preflight={preflight()} />);

    expect(screen.getByTestId("recovery-impact-counts")).toHaveTextContent("2");
    expect(screen.getByTestId("recovery-impact-counts")).toHaveTextContent("3");
    expect(screen.getByTestId("recovery-impact-counts")).toHaveTextContent("1");
    expect(screen.getByTestId("recovery-impact-counts")).toHaveTextContent("4");
    expect(screen.getByTestId("recovery-impact-bytes")).toHaveTextContent("2.0 KB");
    expect(screen.getByText(/4 items will be deleted|删除 4 项/)).toBeInTheDocument();
    expect(screen.getByTestId("recovery-preflight-expiry")).not.toHaveAttribute("aria-live");
  });
});
