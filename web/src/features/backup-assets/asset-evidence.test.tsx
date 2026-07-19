import "@testing-library/jest-dom/vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import type { RecoveryPointDiff, RecoveryPointEvidence } from "@/types/domain";
import { recoveryPoint } from "./__tests__/test-utils";
import { AssetEvidence } from "./asset-evidence";

const evidence: RecoveryPointEvidence = {
  recoveryPointId: recoveryPoint.id,
  lineage: {
    status: "recorded",
    taskId: 7,
    taskRunId: 21,
    taskName: "Synthetic nightly backup",
    nodeId: 3,
    nodeName: "synthetic-node",
    trigger: "cron",
    runStatus: "success",
    startedAt: "2026-07-19T00:00:00Z",
    finishedAt: "2026-07-19T00:05:00Z",
  },
  manifest: {
    status: "recorded",
    id: "manifest-synthetic",
    revision: 1,
    digestAlgorithm: "sha256",
    digest: "synthetic",
    entryCount: 128,
    logicalBytes: 4096,
    generator: "xirang",
    generatorVersion: "0.45.0",
    completeness: "complete",
    createdAt: "2026-07-19T00:05:00Z",
    updatedAt: "2026-07-19T00:05:00Z",
  },
  publicationVerification: {
    status: "recorded",
    provider: "restic",
    completion: "known_exit_zero",
    failureCode: null,
    captureStartedAt: "2026-07-19T00:00:00Z",
    captureFinishedAt: "2026-07-19T00:05:00Z",
    filesProcessed: 128,
    logicalBytes: 4096,
    commitRecorded: true,
  },
  restoreDrills: { status: "not_recorded", items: [] },
};

describe("AssetEvidence", () => {
  it("renders evidence layers without upgrading them to a trust verdict", () => {
    render(
      <AssetEvidence
        mode="evidence"
        recoveryPoints={[recoveryPoint]}
        selectedRecoveryPoint={recoveryPoint}
        evidence={{ status: "ready", value: evidence }}
        diff={{ status: "idle", value: null }}
        onCompare={vi.fn()}
      />
    );

    expect(screen.getByText(/Lineage|谱系/)).toBeInTheDocument();
    expect(screen.getByText(/Manifest|清单/)).toBeInTheDocument();
    expect(screen.getByText(/Provider verification|Provider 校验/)).toBeInTheDocument();
    expect(screen.getByText(/Restore drills|恢复演练/)).toBeInTheDocument();
    expect(screen.queryByText(/Trusted|可信$/)).not.toBeInTheDocument();
  });

  it("requires two distinct exact points and keeps provider evidence separate", async () => {
    const user = userEvent.setup();
    const compare = { ...recoveryPoint, id: "9".repeat(32), producingTaskName: "Synthetic previous backup" };
    const diff: RecoveryPointDiff = {
      items: [
        {
          kind: "modified",
          base: {
            ref: { recoveryPointId: recoveryPoint.id, entryId: "c".repeat(64) },
            name: "synthetic-config.yaml",
            entryType: "file",
            size: 12,
            modifiedAt: "2026-07-19T00:00:00Z",
            mode: "0640",
            owner: "operator",
            mimeType: "text/yaml",
            fingerprintStrength: "strong",
          },
          compare: {
            ref: { recoveryPointId: compare.id, entryId: "d".repeat(64) },
            name: "synthetic-config.yaml",
            entryType: "file",
            size: 24,
            modifiedAt: "2026-07-19T01:00:00Z",
            mode: "0640",
            owner: "operator",
            mimeType: "text/yaml",
            fingerprintStrength: "strong",
          },
          changedFields: ["size", "content"],
          contentEquality: "different",
        },
      ],
      nextCursor: null,
      providerEvidence: {
        status: "unavailable",
        reason: { code: "repository_offline", params: { detail: "raw /private/provider/path" } },
      },
    };
    const onCompare = vi.fn();
    render(
      <AssetEvidence
        mode="diff"
        recoveryPoints={[recoveryPoint, compare]}
        selectedRecoveryPoint={recoveryPoint}
        evidence={{ status: "ready", value: evidence }}
        diff={{ status: "ready", value: diff }}
        onCompare={onCompare}
      />
    );

    await user.selectOptions(screen.getByRole("combobox", { name: /Compare point|比较恢复点/ }), compare.id);
    await user.click(screen.getByRole("button", { name: /Compare recovery points|比较恢复点/ }));
    expect(onCompare).toHaveBeenCalledWith(recoveryPoint.id, compare.id);
    expect(screen.getByText(/Catalog diff|目录差异/)).toBeInTheDocument();
    expect(screen.getByText("synthetic-config.yaml")).toBeInTheDocument();
    expect(screen.getByText(/^Modified$|^已修改$/)).toBeInTheDocument();
    expect(screen.getByText(/Size.*Content|大小.*内容/)).toBeInTheDocument();
    expect(screen.getByText(/^Content differs$|^内容不同$/)).toBeInTheDocument();
    expect(screen.getByText(/Provider evidence unavailable|Provider 证据不可用/)).toBeInTheDocument();
    expect(screen.getByText(/^Repository offline$|^仓库离线$/)).toBeInTheDocument();
    expect(document.body).not.toHaveTextContent("private/provider");
  });
});
