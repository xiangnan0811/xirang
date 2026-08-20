import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

const { apiClientMock } = vi.hoisted(() => ({
  apiClientMock: {
    reconcileBackupRepository: vi.fn(),
  },
}));

vi.mock("@/lib/api/client", () => ({
  apiClient: apiClientMock,
}));

import type { BackupRepository, CatalogProjection } from "@/types/domain";

import { repository } from "./__tests__/test-utils";
import { RepositoryManagementPanel } from "./repository-management-panel";

function available(value: BackupRepository): CatalogProjection<BackupRepository> {
  return { status: "available", value };
}

function blocked(): CatalogProjection<BackupRepository> {
  return { status: "blocked", reason: { code: "unknown_internal_state", params: {} } };
}

afterEach(() => {
  vi.clearAllMocks();
});

describe("RepositoryManagementPanel", () => {
  it.each(["viewer", "operator"] as const)("keeps non-Admin %s on read-only facts and browse", (role) => {
    render(
      <RepositoryManagementPanel
        repositories={[available(repository)]}
        selectedRepositoryId={repository.id}
        viewport="desktop"
        onBrowse={vi.fn()}
        runtime={{ token: `${role}-token`, role, ensureStepUpProof: vi.fn() }}
      />,
    );

    const panel = screen.getByRole("region", { name: /Repository management|仓库管理/ });
    expect(panel).toHaveTextContent(repository.displayName);
    expect(within(panel).queryByRole("button", { name: /Reconnect|重连/ })).not.toBeInTheDocument();
    expect(within(panel).queryByRole("button", { name: /Reconcile|重新探测/ })).not.toBeInTheDocument();
    expect(within(panel).queryByRole("button", { name: /Disconnect|断开/ })).not.toBeInTheDocument();
    expect(within(panel).queryByRole("button", { name: /Scan imports|扫描导入/ })).not.toBeInTheDocument();
    expect(within(panel).queryByRole("button", { name: /Rebuild|重建/ })).not.toBeInTheDocument();
    expect(within(panel).queryByRole("button", { name: /Purge|清理/ })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Browse .*Synthetic Primary Repository|浏览.*Synthetic Primary Repository/ })).toBeInTheDocument();
  });

  it("lets Admin reconnect, reconcile, and refresh after success", async () => {
    const user = userEvent.setup();
    const onRefresh = vi.fn();
    const api = {
      connectBackupRepository: vi.fn().mockResolvedValue({
        status: "available",
        value: {
          repository: {
            id: repository.id,
            providerKind: "restic",
            displayName: repository.displayName,
            description: "",
            versionMode: "native_snapshot",
            status: "online",
            capabilityRevision: 2,
            capabilities: repository.capabilities,
            immutabilityLevel: "backend_versioned",
            lastSeenAt: null,
            lastReconciledAt: null,
            createdAt: repository.createdAt,
            updatedAt: repository.updatedAt,
          },
        },
      }),
      reconcileBackupRepository: vi.fn().mockResolvedValue({
        status: "available",
        value: {
          repository: {
            id: repository.id,
            providerKind: "restic",
            displayName: repository.displayName,
            description: "",
            versionMode: "native_snapshot",
            status: "online",
            capabilityRevision: 3,
            capabilities: repository.capabilities,
            immutabilityLevel: "backend_versioned",
            lastSeenAt: null,
            lastReconciledAt: null,
            createdAt: repository.createdAt,
            updatedAt: repository.updatedAt,
          },
        },
      }),
    };

    render(
      <RepositoryManagementPanel
        repositories={[available(repository)]}
        selectedRepositoryId={repository.id}
        viewport="desktop"
        onBrowse={vi.fn()}
        onRefresh={onRefresh}
        runtime={{ token: "admin-token", role: "admin", ensureStepUpProof: vi.fn() }}
        api={api}
      />,
    );

    await user.click(screen.getByRole("button", { name: /Reconcile|重新探测/ }));
    await user.click(screen.getByRole("button", { name: /Confirm reconcile|确认重新探测/ }));
    expect(api.reconcileBackupRepository).toHaveBeenCalledWith("admin-token", repository.id, expect.any(AbortSignal));
    expect(onRefresh).toHaveBeenCalled();

    await user.click(screen.getByRole("button", { name: /Reconnect|重连/ }));
    await user.type(screen.getByLabelText(/Task ID|任务 ID/), "42");
    await user.click(screen.getByRole("button", { name: /Confirm reconnect|确认重连/ }));
    expect(api.connectBackupRepository).toHaveBeenCalledWith(
      "admin-token",
      { taskId: 42, repositoryId: repository.id },
      expect.any(AbortSignal),
    );
  });

  it("reviews an import candidate and rebuilds without rendering locators", async () => {
    const user = userEvent.setup();
    const candidateId = "2".repeat(32);
    const api = {
      scanBackupRepositoryImports: vi.fn().mockResolvedValue({
        status: "available",
        value: {
          candidates: [{
            status: "available",
            value: {
              id: candidateId,
              repositoryId: repository.id,
              kind: "imported_baseline",
              state: "pending",
              quarantined: false,
              createdAt: "2026-08-17T00:00:00.000Z",
              reviewedAt: null,
            },
          }],
          nextCursor: null,
          discovered: 1,
          existing: 0,
        },
      }),
      reviewBackupRepositoryImportCandidate: vi.fn().mockResolvedValue({
        status: "available",
        value: {
          id: candidateId,
          repositoryId: repository.id,
          kind: "imported_baseline",
          state: "accepted",
          acceptedRecoveryPointId: "3".repeat(32),
          quarantined: false,
          createdAt: "2026-08-17T00:00:00.000Z",
          reviewedAt: "2026-08-17T01:00:00.000Z",
        },
      }),
      rebuildBackupRepositoryImports: vi.fn().mockResolvedValue({
        status: "available",
        value: { accepted: 1, catalogStarted: 1, derivedQueued: 0, partial: 0, failed: 0, reasons: {}, nextCursor: null },
      }),
    };

    render(
      <RepositoryManagementPanel
        repositories={[available(repository)]}
        selectedRepositoryId={repository.id}
        viewport="desktop"
        onBrowse={vi.fn()}
        onRefresh={vi.fn()}
        runtime={{ token: "admin-token", role: "admin", ensureStepUpProof: vi.fn() }}
        api={api}
      />,
    );

    await user.click(screen.getByRole("button", { name: /Scan imports|扫描导入/ }));
    expect(api.scanBackupRepositoryImports).toHaveBeenCalled();
    expect(screen.getByText(/imported_baseline|导入基线/)).toBeInTheDocument();
    expect(document.body.textContent).not.toMatch(/locator|PRIVATE/i);

    await user.click(screen.getByRole("button", { name: /Accept candidate|接受候选/ }));
    expect(api.reviewBackupRepositoryImportCandidate).toHaveBeenCalledWith(
      "admin-token",
      repository.id,
      candidateId,
      { decision: "accepted", acceptAs: "imported_baseline" },
      expect.any(AbortSignal),
    );
    expect(screen.queryByText(/Pending|待审/)).not.toBeInTheDocument();
    expect(screen.getByText(/Accepted|已接受/)).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: /Rebuild|重建/ }));
    await user.click(screen.getByRole("button", { name: /Confirm rebuild|确认重建/ }));
    expect(api.rebuildBackupRepositoryImports).toHaveBeenCalled();
    expect(screen.getByText(/Accepted 1|已接受 1/)).toBeInTheDocument();
    expect(screen.getByText(/Partial 0|部分 0/)).toBeInTheDocument();
    expect(screen.getByText(/Failed 0|失败 0/)).toBeInTheDocument();
  });

  it("loads further import candidate pages and accepts mutable heads as imported baselines", async () => {
    const user = userEvent.setup();
    const firstId = "2".repeat(32);
    const secondId = "3".repeat(32);
    const api = {
      scanBackupRepositoryImports: vi.fn()
        .mockResolvedValueOnce({
          status: "available",
          value: {
            candidates: [{
              status: "available",
              value: {
                id: firstId,
                repositoryId: repository.id,
                kind: "native_snapshot",
                state: "pending",
                quarantined: false,
                createdAt: "2026-08-17T00:00:00.000Z",
                reviewedAt: null,
              },
            }],
            nextCursor: "c".repeat(32),
            discovered: 1,
            existing: 0,
          },
        })
        .mockResolvedValueOnce({
          status: "available",
          value: {
            candidates: [{
              status: "available",
              value: {
                id: secondId,
                repositoryId: repository.id,
                kind: "mutable_head",
                state: "pending",
                quarantined: false,
                createdAt: "2026-08-17T00:00:00.000Z",
                reviewedAt: null,
              },
            }],
            nextCursor: null,
            discovered: 1,
            existing: 0,
          },
        }),
      reviewBackupRepositoryImportCandidate: vi.fn().mockResolvedValue({
        status: "available",
        value: {
          id: secondId,
          repositoryId: repository.id,
          kind: "imported_baseline",
          state: "accepted",
          acceptedRecoveryPointId: "4".repeat(32),
          quarantined: false,
          createdAt: "2026-08-17T00:00:00.000Z",
          reviewedAt: "2026-08-17T01:00:00.000Z",
        },
      }),
    };
    render(
      <RepositoryManagementPanel
        repositories={[available(repository)]}
        selectedRepositoryId={repository.id}
        viewport="desktop"
        onBrowse={vi.fn()}
        onRefresh={vi.fn()}
        runtime={{ token: "admin-token", role: "admin", ensureStepUpProof: vi.fn() }}
        api={api}
      />,
    );
    await user.click(screen.getByRole("button", { name: /Scan imports|扫描导入/ }));
    expect(api.scanBackupRepositoryImports).toHaveBeenCalledTimes(2);
    expect(api.scanBackupRepositoryImports).toHaveBeenLastCalledWith(
      "admin-token",
      repository.id,
      expect.objectContaining({ cursor: "c".repeat(32) }),
    );
    const acceptMutable = screen.getByRole("button", { name: new RegExp(`Accept candidate.*${secondId}|接受候选.*${secondId}`) });
    expect(acceptMutable).toBeDisabled();
    await user.selectOptions(screen.getByLabelText(new RegExp(`Accept as.*${secondId}|接受为.*${secondId}`)), "imported_baseline");
    await user.click(acceptMutable);
    expect(api.reviewBackupRepositoryImportCandidate).toHaveBeenCalledWith(
      "admin-token",
      repository.id,
      secondId,
      { decision: "accepted", acceptAs: "imported_baseline" },
      expect.any(AbortSignal),
    );
  });

  it("lists persisted pending and quarantined import candidates across pages on mount", async () => {
    const firstId = "2".repeat(32);
    const secondId = "3".repeat(32);
    const api = {
      listBackupRepositoryImportCandidates: vi.fn()
        .mockResolvedValueOnce({
          items: [{
            status: "available",
            value: {
              id: firstId,
              repositoryId: repository.id,
              kind: "imported_baseline",
              state: "pending",
              quarantined: false,
              createdAt: "2026-08-17T00:00:00.000Z",
              reviewedAt: null,
            },
          }],
          nextCursor: "c".repeat(32),
        })
        .mockResolvedValueOnce({
          items: [{
            status: "available",
            value: {
              id: secondId,
              repositoryId: repository.id,
              kind: "mutable_head",
              state: "pending",
              quarantined: true,
              createdAt: "2026-08-17T00:00:00.000Z",
              reviewedAt: null,
            },
          }],
          nextCursor: null,
        }),
    };
    render(
      <RepositoryManagementPanel
        repositories={[available(repository)]}
        selectedRepositoryId={repository.id}
        viewport="desktop"
        onBrowse={vi.fn()}
        runtime={{ token: "admin-token", role: "admin", ensureStepUpProof: vi.fn() }}
        api={api}
      />,
    );
    expect(await screen.findByText(/imported_baseline|导入基线/)).toBeInTheDocument();
    expect(await screen.findByText(/Quarantined|已隔离/)).toBeInTheDocument();
    expect(api.listBackupRepositoryImportCandidates).toHaveBeenCalledTimes(2);
    expect(api.listBackupRepositoryImportCandidates).toHaveBeenLastCalledWith(
      "admin-token",
      repository.id,
      expect.objectContaining({ cursor: "c".repeat(32) }),
    );
    expect(screen.queryByRole("button", { name: new RegExp(`Accept candidate ${secondId}|接受候选 ${secondId}`) })).not.toBeInTheDocument();
  });

  it("binds per-row actions to the clicked repository, not the selected row", async () => {
    const user = userEvent.setup();
    const second = { ...repository, id: "c".repeat(32), displayName: "Second Lifecycle Repository" };
    const api = {
      reconcileBackupRepository: vi.fn().mockResolvedValue({
        status: "available",
        value: { repository: { id: second.id, providerKind: "restic", displayName: second.displayName } },
      }),
    };
    render(
      <RepositoryManagementPanel
        repositories={[available(repository), available(second)]}
        selectedRepositoryId={repository.id}
        viewport="desktop"
        onBrowse={vi.fn()}
        onRefresh={vi.fn()}
        runtime={{ token: "admin-token", role: "admin", ensureStepUpProof: vi.fn() }}
        api={api}
      />,
    );
    await user.click(screen.getByRole("button", { name: /Reconcile Second Lifecycle Repository|重新探测 Second Lifecycle Repository/ }));
    await user.click(screen.getByRole("button", { name: /Confirm reconcile|确认重新探测/ }));
    expect(api.reconcileBackupRepository).toHaveBeenCalledWith("admin-token", second.id, expect.any(AbortSignal));
  });

  it("hides Accept for a pending quarantined candidate and shows quarantine status", async () => {
    const user = userEvent.setup();
    const quarantinedId = "2".repeat(32);
    const goodId = "3".repeat(32);
    const api = {
      scanBackupRepositoryImports: vi.fn().mockResolvedValue({
        status: "available",
        value: {
          candidates: [
            {
              status: "available",
              value: {
                id: quarantinedId,
                repositoryId: repository.id,
                kind: "imported_baseline",
                state: "pending",
                quarantined: true,
                createdAt: "2026-08-17T00:00:00.000Z",
                reviewedAt: null,
              },
            },
            {
              status: "available",
              value: {
                id: goodId,
                repositoryId: repository.id,
                kind: "native_snapshot",
                state: "pending",
                quarantined: false,
                createdAt: "2026-08-17T00:00:00.000Z",
                reviewedAt: null,
              },
            },
          ],
          nextCursor: null,
          discovered: 2,
          existing: 0,
        },
      }),
    };
    render(
      <RepositoryManagementPanel
        repositories={[available(repository)]}
        selectedRepositoryId={repository.id}
        viewport="desktop"
        onBrowse={vi.fn()}
        runtime={{ token: "admin-token", role: "admin", ensureStepUpProof: vi.fn() }}
        api={api}
      />,
    );
    await user.click(screen.getByRole("button", { name: /Scan imports|扫描导入/ }));
    expect(screen.getByText(/Quarantined|已隔离/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: new RegExp(`Accept candidate ${quarantinedId}|接受候选 ${quarantinedId}`) })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: new RegExp(`Reject candidate ${quarantinedId}|拒绝候选 ${quarantinedId}`) })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: new RegExp(`Accept candidate ${goodId}|接受候选 ${goodId}`) })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: new RegExp(`Reject candidate ${goodId}|拒绝候选 ${goodId}`) })).toBeInTheDocument();
  });

  it("rejects an import candidate on the scanned repository", async () => {
    const user = userEvent.setup();
    const candidateId = "2".repeat(32);
    const api = {
      scanBackupRepositoryImports: vi.fn().mockResolvedValue({
        status: "available",
        value: {
          candidates: [{
            status: "available",
            value: {
              id: candidateId,
              repositoryId: repository.id,
              kind: "native_snapshot",
              state: "pending",
              quarantined: false,
              createdAt: "2026-08-17T00:00:00.000Z",
              reviewedAt: null,
            },
          }],
          nextCursor: null,
          discovered: 1,
          existing: 0,
        },
      }),
      reviewBackupRepositoryImportCandidate: vi.fn().mockResolvedValue({
        status: "available",
        value: {
          id: candidateId,
          repositoryId: repository.id,
          kind: "native_snapshot",
          state: "rejected",
          quarantined: false,
          createdAt: "2026-08-17T00:00:00.000Z",
          reviewedAt: "2026-08-17T01:00:00.000Z",
        },
      }),
    };
    render(
      <RepositoryManagementPanel
        repositories={[available(repository)]}
        selectedRepositoryId={repository.id}
        viewport="desktop"
        onBrowse={vi.fn()}
        runtime={{ token: "admin-token", role: "admin", ensureStepUpProof: vi.fn() }}
        api={api}
      />,
    );
    await user.click(screen.getByRole("button", { name: /Scan imports|扫描导入/ }));
    const candidates = screen.getByRole("list", { name: /Scan imports|扫描导入/ });
    expect(within(candidates).getByText(/Native snapshot|原生快照/)).toBeInTheDocument();
    expect(document.body.textContent).not.toMatch(/native_snapshot/);
    await user.click(screen.getByRole("button", { name: /Reject candidate|拒绝候选/ }));
    expect(api.reviewBackupRepositoryImportCandidate).toHaveBeenCalledWith(
      "admin-token",
      repository.id,
      candidateId,
      { decision: "rejected" },
      expect.any(AbortSignal),
    );
    expect(screen.getByText(/Rejected|已拒绝/)).toBeInTheDocument();
  });

  it("surfaces reconnect conflict and treats a missing client method as blocked", async () => {
    const user = userEvent.setup();
    const onRefresh = vi.fn();
    const conflictApi = {
      connectBackupRepository: vi.fn().mockRejectedValue(Object.assign(new Error("stale"), { status: 409 })),
    };
    const { rerender } = render(
      <RepositoryManagementPanel
        repositories={[available(repository)]}
        selectedRepositoryId={repository.id}
        viewport="desktop"
        onBrowse={vi.fn()}
        onRefresh={onRefresh}
        runtime={{ token: "admin-token", role: "admin", ensureStepUpProof: vi.fn() }}
        api={conflictApi}
      />,
    );
    await user.click(screen.getByRole("button", { name: /Reconnect|重连/ }));
    await user.type(screen.getByLabelText(/Task ID|任务 ID/), "42");
    await user.click(screen.getByRole("button", { name: /Confirm reconnect|确认重连/ }));
    expect(await screen.findByText(/Conflict|冲突/)).toBeInTheDocument();
    expect(onRefresh).not.toHaveBeenCalled();
    await user.keyboard("{Escape}");

    rerender(
      <RepositoryManagementPanel
        repositories={[available(repository)]}
        selectedRepositoryId={repository.id}
        viewport="desktop"
        onBrowse={vi.fn()}
        onRefresh={onRefresh}
        runtime={{ token: "admin-token", role: "admin", ensureStepUpProof: vi.fn() }}
        api={{}}
      />,
    );
    await user.click(screen.getByRole("button", { name: /Reconcile|重新探测/ }));
    await user.click(screen.getByRole("button", { name: /Confirm reconcile|确认重新探测/ }));
    expect(await screen.findByText(/Current state is unavailable|当前状态不可用/)).toBeInTheDocument();
    expect(onRefresh).not.toHaveBeenCalled();
  });

  it("uses the production client when api is omitted", async () => {
    const user = userEvent.setup();
    apiClientMock.reconcileBackupRepository.mockResolvedValue({
      status: "available",
      value: { repository: { id: repository.id, providerKind: "restic", displayName: repository.displayName } },
    });
    render(
      <RepositoryManagementPanel
        repositories={[available(repository)]}
        selectedRepositoryId={repository.id}
        viewport="desktop"
        onBrowse={vi.fn()}
        runtime={{ token: "admin-token", role: "admin", ensureStepUpProof: vi.fn() }}
      />,
    );
    await user.click(screen.getByRole("button", { name: /Reconcile|重新探测/ }));
    await user.click(screen.getByRole("button", { name: /Confirm reconcile|确认重新探测/ }));
    expect(apiClientMock.reconcileBackupRepository).toHaveBeenCalledWith("admin-token", repository.id, expect.any(AbortSignal));
  });

  it("renders blocked repository facts without raw internal state", () => {
    render(
      <RepositoryManagementPanel
        repositories={[blocked()]}
        viewport="desktop"
        onBrowse={vi.fn()}
        runtime={{ token: "admin-token", role: "admin", ensureStepUpProof: vi.fn() }}
      />,
    );
    expect(document.body.textContent).not.toContain("unknown_internal_state");
  });

  it("keeps successful import candidates when another repository list fails", async () => {
    const second = { ...repository, id: "c".repeat(32), displayName: "Second Lifecycle Repository" };
    const firstId = "2".repeat(32);
    const api = {
      listBackupRepositoryImportCandidates: vi.fn()
        .mockImplementation((_token: string, repositoryId: string) => {
          if (repositoryId === second.id) {
            return Promise.reject(Object.assign(new Error("list failed"), { status: 500 }));
          }
          return Promise.resolve({
            items: [{
              status: "available",
              value: {
                id: firstId,
                repositoryId: repository.id,
                kind: "imported_baseline",
                state: "pending",
                quarantined: false,
                createdAt: "2026-08-17T00:00:00.000Z",
                reviewedAt: null,
              },
            }],
            nextCursor: null,
          });
        }),
    };
    render(
      <RepositoryManagementPanel
        repositories={[available(repository), available(second)]}
        selectedRepositoryId={repository.id}
        viewport="desktop"
        onBrowse={vi.fn()}
        runtime={{ token: "admin-token", role: "admin", ensureStepUpProof: vi.fn() }}
        api={api}
      />,
    );
    expect(await screen.findByText(/imported_baseline|导入基线/)).toBeInTheDocument();
    expect(api.listBackupRepositoryImportCandidates).toHaveBeenCalledTimes(2);
  });

  it("stops scan and rebuild after eight pages and continues from the last cursor", async () => {
    const user = userEvent.setup();
    const pageCursor = (index: number) => `c${String(index).padStart(31, "0")}`;
    const pageFromCursor = (cursor?: string) => (cursor ? Number(cursor.slice(1)) : 0);
    const scanBackupRepositoryImports = vi.fn().mockImplementation(async (_token: string, _id: string, options?: { cursor?: string }) => {
      const page = pageFromCursor(options?.cursor);
      return {
        status: "available",
        value: {
          candidates: [{
            status: "available",
            value: {
              id: page.toString(16).padStart(32, "a"),
              repositoryId: repository.id,
              kind: "native_snapshot",
              state: "pending",
              quarantined: false,
              createdAt: "2026-08-17T00:00:00.000Z",
              reviewedAt: null,
            },
          }],
          nextCursor: page < 8 ? pageCursor(page + 1) : null,
          discovered: 1,
          existing: 0,
        },
      };
    });
    const rebuildBackupRepositoryImports = vi.fn().mockImplementation(async (_token: string, _id: string, options?: { cursor?: string }) => {
      const page = pageFromCursor(options?.cursor);
      return {
        status: "available",
        value: {
          accepted: 1, catalogStarted: 1, derivedQueued: 0, partial: 0, failed: 0, reasons: {},
          nextCursor: page < 8 ? pageCursor(page + 1) : null,
        },
      };
    });
    render(
      <RepositoryManagementPanel
        repositories={[available(repository)]}
        selectedRepositoryId={repository.id}
        viewport="desktop"
        onBrowse={vi.fn()}
        onRefresh={vi.fn()}
        runtime={{ token: "admin-token", role: "admin", ensureStepUpProof: vi.fn() }}
        api={{ scanBackupRepositoryImports, rebuildBackupRepositoryImports }}
      />,
    );

    await user.click(screen.getByRole("button", { name: /Scan imports|扫描导入/ }));
    expect(scanBackupRepositoryImports).toHaveBeenCalledTimes(8);
    expect(scanBackupRepositoryImports).toHaveBeenLastCalledWith(
      "admin-token",
      repository.id,
      expect.objectContaining({ cursor: pageCursor(7) }),
    );
    await user.click(screen.getByRole("button", { name: /Continue scan|继续扫描/ }));
    expect(scanBackupRepositoryImports).toHaveBeenCalledTimes(9);
    expect(scanBackupRepositoryImports).toHaveBeenLastCalledWith(
      "admin-token",
      repository.id,
      expect.objectContaining({ cursor: pageCursor(8) }),
    );

    await user.click(screen.getByRole("button", { name: /Rebuild|重建/ }));
    await user.click(screen.getByRole("button", { name: /Confirm rebuild|确认重建/ }));
    expect(rebuildBackupRepositoryImports).toHaveBeenCalledTimes(8);
    expect(rebuildBackupRepositoryImports).toHaveBeenLastCalledWith(
      "admin-token",
      repository.id,
      expect.objectContaining({ cursor: pageCursor(7) }),
    );
    expect(screen.getByText(/Accepted 8|已接受 8/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /Continue rebuild|继续重建/ }));
    expect(rebuildBackupRepositoryImports).toHaveBeenCalledTimes(9);
    expect(rebuildBackupRepositoryImports).toHaveBeenLastCalledWith(
      "admin-token",
      repository.id,
      expect.objectContaining({ cursor: pageCursor(8) }),
    );
    expect(screen.getByText(/Accepted 9|已接受 9/)).toBeInTheDocument();
  });
});
