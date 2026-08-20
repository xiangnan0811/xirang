import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { STEP_UP_ACTIONS } from "@/lib/step-up-storage";

import { recoveryPoint, repository } from "./__tests__/test-utils";
import { RetentionPolicyPanel } from "./retention-policy-panel";

const { apiClientMock } = vi.hoisted(() => ({
  apiClientMock: {
    listRetentionPolicies: vi.fn(),
    createRetentionPolicy: vi.fn(),
    updateRetentionPolicy: vi.fn(),
    deleteRetentionPolicy: vi.fn(),
    previewRetentionPolicyImpact: vi.fn(),
    createRepositoryPurgePlan: vi.fn(),
    executeRepositoryPurge: vi.fn(),
    listRecoveryPointHolds: vi.fn(),
    createRecoveryPointHold: vi.fn(),
    releaseRecoveryPointHold: vi.fn(),
  },
}));

vi.mock("@/lib/api/client", () => ({
  apiClient: apiClientMock,
}));

const policyId = "a".repeat(32);
const holdId = "d".repeat(32);
const planId = "f".repeat(32);
const digest = "9".repeat(64);

function policy() {
  return {
    id: policyId,
    scopeKind: "repository" as const,
    scopeId: repository.id,
    revision: 2,
    rules: { version: 1, age: { keepDays: 30 } },
    ruleDigest: digest,
    status: "active" as const,
    createdBy: 7,
    updatedBy: 7,
    createdAt: "2026-08-17T00:00:00.000Z",
    updatedAt: "2026-08-17T01:00:00.000Z",
  };
}

afterEach(() => {
  vi.clearAllMocks();
});

describe("RetentionPolicyPanel", () => {
  it("hides mutations for non-Admin and does not load policies", () => {
    const api = { listRetentionPolicies: vi.fn() };
    render(
      <RetentionPolicyPanel
        repositories={[{ status: "available", value: repository }]}
        recoveryPoints={[{ status: "available", value: recoveryPoint }]}
        runtime={{ token: "operator-token", role: "operator", ensureStepUpProof: vi.fn() }}
        api={api}
      />,
    );
    expect(screen.queryByRole("region", { name: /Retention policies|保留策略/ })).not.toBeInTheDocument();
    expect(api.listRetentionPolicies).not.toHaveBeenCalled();
  });

  it("previews exact impact and executes purge only after typed confirmation, reason, and proof", async () => {
    const user = userEvent.setup();
    const ensureStepUpProof = vi.fn().mockResolvedValue("purge-proof");
    const onRefresh = vi.fn();
    const api = {
      listRetentionPolicies: vi.fn().mockResolvedValue({
        items: [{ status: "available", value: policy() }],
        nextCursor: null,
      }),
      previewRepositoryPurge: vi.fn().mockResolvedValue({
        status: "available",
        value: {
          repositoryId: repository.id,
          impactRevision: 11,
          selectedCount: 1,
          holdCount: 0,
          leaseCount: 0,
          wormCount: 0,
          points: [{ recoveryPointId: recoveryPoint.id, pointRevision: 3, capabilityRevision: 5 }],
        },
      }),
      createRepositoryPurgePlan: vi.fn().mockResolvedValue({
        status: "available",
        value: {
          id: planId,
          repositoryId: repository.id,
          revision: 1,
          impactRevision: 11,
          expiresAt: "2026-08-17T02:00:00.000Z",
          holdCount: 0,
          leaseCount: 0,
          wormCount: 0,
          status: "ready",
          itemCount: 1,
          items: [{ recoveryPointId: recoveryPoint.id, pointRevision: 3, capabilityRevision: 5 }],
        },
      }),
      executeRepositoryPurge: vi.fn().mockResolvedValue({
        status: "available",
        value: { planId, claimed: 1, blocked: 0 },
      }),
    };

    render(
      <RetentionPolicyPanel
        repositories={[{ status: "available", value: repository }]}
        recoveryPoints={[{ status: "available", value: recoveryPoint }]}
        selectedRecoveryPointId={recoveryPoint.id}
        runtime={{ token: "admin-token", role: "admin", ensureStepUpProof }}
        onRefresh={onRefresh}
        api={api}
      />,
    );

    expect(await screen.findByText(/keep 30 days|保留 30 天/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /Preview purge|预览清理/ }));
    expect(await screen.findByText(/Selected 1|选中 1/)).toBeInTheDocument();
    expect(screen.getAllByText(recoveryPoint.id).length).toBeGreaterThan(0);
    expect(api.previewRepositoryPurge).toHaveBeenCalledWith(
      "admin-token",
      repository.id,
      { recoveryPointIds: [recoveryPoint.id] },
      expect.any(AbortSignal),
    );

    const confirm = screen.getByLabelText(/Type selected count|输入选中数量/);
    await user.type(confirm, "2");
    await user.type(screen.getByLabelText(/Purge reason|清理原因/), "approved-purge");
    expect(screen.getByRole("button", { name: /Execute purge|执行清理/ })).toBeDisabled();
    expect(api.createRepositoryPurgePlan).not.toHaveBeenCalled();

    await user.clear(confirm);
    await user.type(confirm, "1");
    await user.click(screen.getByRole("button", { name: /Execute purge|执行清理/ }));
    expect(ensureStepUpProof).toHaveBeenCalledWith(STEP_UP_ACTIONS.repositoryPurge, {
      persist: false,
      reuseCached: false,
    });
    expect(api.createRepositoryPurgePlan).toHaveBeenCalled();
    expect(api.executeRepositoryPurge).toHaveBeenCalledWith(
      "admin-token",
      repository.id,
      expect.objectContaining({
        planId,
        reason: "approved-purge",
        stepUpProof: "purge-proof",
      }),
      expect.any(AbortSignal),
    );
    expect(onRefresh).toHaveBeenCalled();
    expect(await screen.findByText(/Purge claimed and is in progress|清理已认领，正在进行/)).toBeInTheDocument();
    expect(screen.queryByText(/Some selected points were blocked|部分选中恢复点被阻止/)).not.toBeInTheDocument();
    expect(document.body.textContent).not.toMatch(/locator|PRIVATE|rule_digest/i);
  });

  it("creates a legal hold and releases it with an isolated proof", async () => {
    const user = userEvent.setup();
    const ensureStepUpProof = vi.fn().mockResolvedValue("hold-proof");
    const onRefresh = vi.fn();
    const api = {
      listRetentionPolicies: vi.fn().mockResolvedValue({ items: [], nextCursor: null }),
      createRecoveryPointHold: vi.fn().mockResolvedValue({
        status: "available",
        value: {
          id: holdId,
          recoveryPointId: recoveryPoint.id,
          holdType: "legal",
          state: "active",
          createdBy: 7,
          expiresAt: null,
          releasedBy: null,
          releasedAt: null,
          createdAt: "2026-08-17T00:00:00.000Z",
          updatedAt: "2026-08-17T00:00:00.000Z",
        },
      }),
      releaseRecoveryPointHold: vi.fn().mockResolvedValue({
        status: "available",
        value: {
          id: holdId,
          recoveryPointId: recoveryPoint.id,
          holdType: "legal",
          state: "released",
          createdBy: 7,
          expiresAt: null,
          releasedBy: 7,
          releasedAt: "2026-08-17T03:00:00.000Z",
          createdAt: "2026-08-17T00:00:00.000Z",
          updatedAt: "2026-08-17T03:00:00.000Z",
        },
      }),
    };

    render(
      <RetentionPolicyPanel
        repositories={[{ status: "available", value: repository }]}
        recoveryPoints={[{ status: "available", value: recoveryPoint }]}
        selectedRecoveryPointId={recoveryPoint.id}
        runtime={{ token: "admin-token", role: "admin", ensureStepUpProof }}
        onRefresh={onRefresh}
        api={api}
      />,
    );

    await user.click(screen.getByRole("button", { name: /Create hold|创建冻结/ }));
    expect(screen.getAllByText(recoveryPoint.id).length).toBeGreaterThan(0);
    await user.type(screen.getByLabelText(/Hold reason|冻结原因/), "legal-hold-for-case");
    await user.click(screen.getByRole("button", { name: /Confirm hold|确认冻结/ }));
    expect(api.createRecoveryPointHold).toHaveBeenCalledWith(
      "admin-token",
      recoveryPoint.id,
      { holdType: "legal", reason: "legal-hold-for-case" },
      expect.any(AbortSignal),
    );
    expect(onRefresh).toHaveBeenCalled();
    expect(await screen.findByText(/Hold active|冻结生效/)).toBeInTheDocument();

    await user.type(screen.getByLabelText(/Release reason|解除原因/), "case-closed");
    await user.click(screen.getByRole("button", { name: /Release hold|解除冻结/ }));
    expect(ensureStepUpProof).toHaveBeenCalledWith(STEP_UP_ACTIONS.retentionHoldRelease, {
      persist: false,
      reuseCached: false,
    });
    expect(api.releaseRecoveryPointHold).toHaveBeenCalledWith(
      "admin-token",
      recoveryPoint.id,
      holdId,
      { reason: "case-closed", stepUpProof: "hold-proof" },
      expect.any(AbortSignal),
    );
  });

  it("loads persisted holds for the selected point and can release after remount", async () => {
    const user = userEvent.setup();
    const ensureStepUpProof = vi.fn().mockResolvedValue("hold-proof");
    const persisted = {
      id: holdId,
      recoveryPointId: recoveryPoint.id,
      holdType: "legal" as const,
      state: "active" as const,
      createdBy: 7,
      expiresAt: null,
      releasedBy: null,
      releasedAt: null,
      createdAt: "2026-08-17T00:00:00.000Z",
      updatedAt: "2026-08-17T00:00:00.000Z",
    };
    const api = {
      listRetentionPolicies: vi.fn().mockResolvedValue({ items: [], nextCursor: null }),
      listRecoveryPointHolds: vi.fn().mockResolvedValue({
        items: [{ status: "available" as const, value: persisted }],
      }),
      releaseRecoveryPointHold: vi.fn().mockResolvedValue({
        status: "available" as const,
        value: {
          ...persisted,
          state: "released" as const,
          releasedBy: 7,
          releasedAt: "2026-08-17T03:00:00.000Z",
          updatedAt: "2026-08-17T03:00:00.000Z",
        },
      }),
    };
    render(
      <RetentionPolicyPanel
        repositories={[{ status: "available", value: repository }]}
        recoveryPoints={[{ status: "available", value: recoveryPoint }]}
        selectedRecoveryPointId={recoveryPoint.id}
        runtime={{ token: "admin-token", role: "admin", ensureStepUpProof }}
        api={api}
      />,
    );
    expect(await screen.findByLabelText(new RegExp(`Hold legal ${holdId}|冻结 legal ${holdId}`))).toBeInTheDocument();
    expect(await screen.findByText(/Hold active|冻结生效/)).toBeInTheDocument();
    expect(api.listRecoveryPointHolds).toHaveBeenCalledWith("admin-token", recoveryPoint.id, expect.any(AbortSignal));
    await user.type(screen.getByLabelText(/Release reason|解除原因/), "case-closed");
    await user.click(screen.getByRole("button", { name: /Release hold|解除冻结/ }));
    expect(api.releaseRecoveryPointHold).toHaveBeenCalledWith(
      "admin-token",
      recoveryPoint.id,
      holdId,
      { reason: "case-closed", stepUpProof: "hold-proof" },
      expect.any(AbortSignal),
    );
  });

  it("surfaces conflict and blocked states without private payload text", async () => {
    const user = userEvent.setup();
    const api = {
      listRetentionPolicies: vi.fn().mockResolvedValue({
        items: [{ status: "available", value: policy() }],
        nextCursor: null,
      }),
      previewRetentionPolicyImpact: vi.fn().mockRejectedValue(Object.assign(new Error("conflict"), { status: 409 })),
    };
    render(
      <RetentionPolicyPanel
        repositories={[{ status: "available", value: repository }]}
        recoveryPoints={[{ status: "available", value: recoveryPoint }]}
        runtime={{ token: "admin-token", role: "admin", ensureStepUpProof: vi.fn() }}
        api={api}
      />,
    );
    await screen.findByRole("button", { name: /Preview impact|预览影响/ });
    await user.click(screen.getByRole("button", { name: /Preview impact|预览影响/ }));
    expect(await screen.findByText(/Conflict|冲突/)).toBeInTheDocument();
    expect(document.body.textContent).not.toContain("conflict");
  });

  it("creates, updates, and deletes a policy with expected revision", async () => {
    const user = userEvent.setup();
    const created = { ...policy(), id: "b".repeat(32), revision: 1, rules: { version: 1, age: { keepDays: 14 } } };
    const api = {
      listRetentionPolicies: vi.fn().mockResolvedValue({ items: [], nextCursor: null }),
      createRetentionPolicy: vi.fn().mockResolvedValue({ status: "available", value: created }),
      updateRetentionPolicy: vi.fn().mockResolvedValue({
        status: "available",
        value: { ...created, revision: 2, rules: { version: 1, age: { keepDays: 21 } } },
      }),
      deleteRetentionPolicy: vi.fn().mockResolvedValue({
        status: "available",
        value: { ...created, revision: 2, status: "deleted" },
      }),
    };
    render(
      <RetentionPolicyPanel
        repositories={[{ status: "available", value: repository }]}
        recoveryPoints={[]}
        selectedRepositoryId={repository.id}
        runtime={{ token: "admin-token", role: "admin", ensureStepUpProof: vi.fn() }}
        api={api}
      />,
    );
    const keepDays = await screen.findByLabelText(/Keep days|保留天数/);
    await user.clear(keepDays);
    await user.type(keepDays, "14");
    await user.click(screen.getByRole("button", { name: /Create policy|创建策略/ }));
    expect(api.createRetentionPolicy).toHaveBeenCalledWith(
      "admin-token",
      { scopeKind: "repository", scopeId: repository.id, rules: { version: 1, age: { keepDays: 14 } } },
      expect.any(AbortSignal),
    );

    const updateDays = await screen.findByLabelText(new RegExp(`Keep days.*${created.id}|保留天数.*${created.id}`));
    await user.clear(updateDays);
    await user.type(updateDays, "21");
    await user.click(screen.getByRole("button", { name: new RegExp(`Update policy.*${created.id}|更新策略.*${created.id}`) }));
    expect(api.updateRetentionPolicy).toHaveBeenCalledWith(
      "admin-token",
      created.id,
      { expectedRevision: 1, rules: { version: 1, age: { keepDays: 21 } } },
      expect.any(AbortSignal),
    );
    await user.click(screen.getByRole("button", { name: /Delete policy|删除策略/ }));
    expect(api.deleteRetentionPolicy).not.toHaveBeenCalled();
    expect(screen.getAllByText(created.id).length).toBeGreaterThan(0);
    await user.type(screen.getByLabelText(/Type policy ID|输入策略 ID/), created.id);
    await user.click(screen.getByRole("button", { name: /Confirm delete policy|确认删除策略/ }));
    expect(api.deleteRetentionPolicy).toHaveBeenCalledWith("admin-token", created.id, 2, expect.any(AbortSignal));
  });

  it("preserves count and calendar rules when updating keep days on a mixed policy", async () => {
    const user = userEvent.setup();
    const mixed = {
      ...policy(),
      rules: {
        version: 1 as const,
        age: { keepDays: 30 },
        count: { keepLatest: 5 },
        calendar: [{ unit: "month" as const, keep: 12 }],
      },
    };
    const api = {
      listRetentionPolicies: vi.fn().mockResolvedValue({
        items: [{ status: "available", value: mixed }],
        nextCursor: null,
      }),
      updateRetentionPolicy: vi.fn().mockResolvedValue({
        status: "available",
        value: { ...mixed, revision: 3, rules: { ...mixed.rules, age: { keepDays: 45 } } },
      }),
    };
    render(
      <RetentionPolicyPanel
        repositories={[{ status: "available", value: repository }]}
        recoveryPoints={[]}
        selectedRepositoryId={repository.id}
        runtime={{ token: "admin-token", role: "admin", ensureStepUpProof: vi.fn() }}
        api={api}
      />,
    );
    const updateDays = await screen.findByLabelText(new RegExp(`Keep days.*${mixed.id}|保留天数.*${mixed.id}`));
    await user.clear(updateDays);
    await user.type(updateDays, "45");
    await user.click(screen.getByRole("button", { name: new RegExp(`Update policy.*${mixed.id}|更新策略.*${mixed.id}`) }));
    expect(api.updateRetentionPolicy).toHaveBeenCalledWith(
      "admin-token",
      mixed.id,
      {
        expectedRevision: 2,
        rules: {
          version: 1,
          age: { keepDays: 45 },
          count: { keepLatest: 5 },
          calendar: [{ unit: "month", keep: 12 }],
        },
      },
      expect.any(AbortSignal),
    );
  });

  it("purges selected recovery points and does not treat blocked points as success", async () => {
    const user = userEvent.setup();
    const api = {
      listRetentionPolicies: vi.fn().mockResolvedValue({
        items: [],
        nextCursor: null,
      }),
      previewRepositoryPurge: vi.fn().mockResolvedValue({
        status: "available",
        value: {
          repositoryId: repository.id,
          impactRevision: 11,
          selectedCount: 1,
          holdCount: 0,
          leaseCount: 0,
          wormCount: 0,
          points: [{ recoveryPointId: recoveryPoint.id, pointRevision: 3, capabilityRevision: 5 }],
        },
      }),
      createRepositoryPurgePlan: vi.fn().mockResolvedValue({
        status: "available",
        value: {
          id: planId,
          repositoryId: repository.id,
          revision: 1,
          impactRevision: 11,
          expiresAt: "2026-08-17T02:00:00.000Z",
          holdCount: 0,
          leaseCount: 0,
          wormCount: 0,
          status: "ready",
          itemCount: 1,
          items: [{ recoveryPointId: recoveryPoint.id, pointRevision: 3, capabilityRevision: 5 }],
        },
      }),
      executeRepositoryPurge: vi.fn().mockResolvedValue({
        status: "available",
        value: { planId, claimed: 0, blocked: 1 },
      }),
    };
    render(
      <RetentionPolicyPanel
        repositories={[{ status: "available", value: repository }]}
        recoveryPoints={[{ status: "available", value: recoveryPoint }]}
        selectedRepositoryId={repository.id}
        selectedRecoveryPointId={recoveryPoint.id}
        runtime={{ token: "admin-token", role: "admin", ensureStepUpProof: vi.fn().mockResolvedValue("purge-proof") }}
        api={api}
      />,
    );
    await user.click(await screen.findByRole("button", { name: /Preview purge|预览清理/ }));
    await user.type(screen.getByLabelText(/Type selected count|输入选中数量/), "1");
    await user.type(screen.getByLabelText(/Purge reason|清理原因/), "approved-purge");
    await user.click(screen.getByRole("button", { name: /Execute purge|执行清理/ }));
    expect(api.createRepositoryPurgePlan).toHaveBeenCalledWith(
      "admin-token",
      repository.id,
      expect.objectContaining({ items: [{ recoveryPointId: recoveryPoint.id, pointRevision: 3, capabilityRevision: 5 }] }),
      expect.any(AbortSignal),
    );
    expect(await screen.findByText(/Some selected points were blocked|部分选中恢复点被阻止/)).toBeInTheDocument();
    expect(screen.queryByText(/Operation completed|操作已完成/)).not.toBeInTheDocument();
  });

  it("uses the production client when api is omitted", async () => {
    apiClientMock.listRetentionPolicies.mockResolvedValue({
      items: [{ status: "available", value: policy() }],
      nextCursor: null,
    });
    apiClientMock.previewRetentionPolicyImpact.mockResolvedValue({
      status: "available",
      value: {
        policyId,
        policyRevision: 2,
        impactRevision: 11,
        evaluatedAt: "2026-08-17T01:00:00.000Z",
        selectedCount: 0,
        holdCount: 0,
        leaseCount: 0,
        wormCount: 0,
        points: [],
      },
    });
    const user = userEvent.setup();
    render(
      <RetentionPolicyPanel
        repositories={[{ status: "available", value: repository }]}
        recoveryPoints={[]}
        runtime={{ token: "admin-token", role: "admin", ensureStepUpProof: vi.fn() }}
      />,
    );
    await user.click(await screen.findByRole("button", { name: /Preview impact|预览影响/ }));
    expect(apiClientMock.listRetentionPolicies).toHaveBeenCalled();
    expect(apiClientMock.previewRetentionPolicyImpact).toHaveBeenCalledWith("admin-token", policyId, 2, expect.any(AbortSignal));
  });

  it("holds the selected recovery point instead of the first listed point", async () => {
    const user = userEvent.setup();
    const otherPoint = { ...recoveryPoint, id: "c".repeat(32) };
    const api = {
      listRetentionPolicies: vi.fn().mockResolvedValue({ items: [], nextCursor: null }),
      createRecoveryPointHold: vi.fn().mockResolvedValue({
        status: "available",
        value: {
          id: holdId,
          recoveryPointId: otherPoint.id,
          holdType: "legal",
          state: "active",
          createdBy: 7,
          expiresAt: null,
          releasedBy: null,
          releasedAt: null,
          createdAt: "2026-08-17T00:00:00.000Z",
          updatedAt: "2026-08-17T00:00:00.000Z",
        },
      }),
    };
    render(
      <RetentionPolicyPanel
        repositories={[{ status: "available", value: repository }]}
        recoveryPoints={[{ status: "available", value: recoveryPoint }, { status: "available", value: otherPoint }]}
        selectedRecoveryPointId={otherPoint.id}
        runtime={{ token: "admin-token", role: "admin", ensureStepUpProof: vi.fn() }}
        api={api}
      />,
    );
    await user.click(screen.getByRole("button", { name: /Create hold|创建冻结/ }));
    await user.type(screen.getByLabelText(/Hold reason|冻结原因/), "selected-point-only");
    await user.click(screen.getByRole("button", { name: /Confirm hold|确认冻结/ }));
    expect(api.createRecoveryPointHold).toHaveBeenCalledWith(
      "admin-token",
      otherPoint.id,
      { holdType: "legal", reason: "selected-point-only" },
      expect.any(AbortSignal),
    );
  });

  it("keeps hold targeting when the route recovery point is cleared", async () => {
    const user = userEvent.setup();
    const otherPoint = { ...recoveryPoint, id: "c".repeat(32) };
    const api = {
      listRetentionPolicies: vi.fn().mockResolvedValue({ items: [], nextCursor: null }),
      listRecoveryPointHolds: vi.fn().mockResolvedValue({ items: [] }),
      createRecoveryPointHold: vi.fn().mockResolvedValue({
        status: "available",
        value: {
          id: holdId,
          recoveryPointId: otherPoint.id,
          holdType: "operational",
          state: "active",
          createdBy: 7,
          expiresAt: null,
          releasedBy: null,
          releasedAt: null,
          createdAt: "2026-08-17T00:00:00.000Z",
          updatedAt: "2026-08-17T00:00:00.000Z",
        },
      }),
    };
    render(
      <RetentionPolicyPanel
        repositories={[{ status: "available", value: repository }]}
        recoveryPoints={[{ status: "available", value: recoveryPoint }, { status: "available", value: otherPoint }]}
        runtime={{ token: "admin-token", role: "admin", ensureStepUpProof: vi.fn() }}
        api={api}
      />,
    );
    const picker = await screen.findByLabelText(/Hold recovery point|冻结恢复点/);
    await user.selectOptions(picker, otherPoint.id);
    expect(api.listRecoveryPointHolds).toHaveBeenCalledWith("admin-token", otherPoint.id, expect.any(AbortSignal));
    await user.click(screen.getByRole("button", { name: /Create hold|创建冻结/ }));
    await user.selectOptions(screen.getByLabelText(/Hold type|冻结类型/), "operational");
    await user.type(screen.getByLabelText(/Hold reason|冻结原因/), "ops-hold");
    await user.type(screen.getByLabelText(/Hold expires at|冻结到期时间/), "2028-01-01T12:00");
    await user.click(screen.getByRole("button", { name: /Confirm hold|确认冻结/ }));
    expect(api.createRecoveryPointHold).toHaveBeenCalledWith(
      "admin-token",
      otherPoint.id,
      expect.objectContaining({ holdType: "operational", reason: "ops-hold", expiresAt: expect.stringMatching(/^2028-01-01T/) }),
      expect.any(AbortSignal),
    );
  });

  it("lists legal and operational holds and requires a future expiry for operational create", async () => {
    const user = userEvent.setup();
    const legalId = "d".repeat(32);
    const operationalId = "e".repeat(32);
    const createdOperationalId = "f".repeat(32);
    const api = {
      listRetentionPolicies: vi.fn().mockResolvedValue({ items: [], nextCursor: null }),
      listRecoveryPointHolds: vi.fn().mockResolvedValue({
        items: [
          {
            status: "available" as const,
            value: {
              id: legalId,
              recoveryPointId: recoveryPoint.id,
              holdType: "legal" as const,
              state: "active" as const,
              createdBy: 7,
              expiresAt: null,
              releasedBy: null,
              releasedAt: null,
              createdAt: "2026-08-17T00:00:00.000Z",
              updatedAt: "2026-08-17T00:00:00.000Z",
            },
          },
          {
            status: "available" as const,
            value: {
              id: operationalId,
              recoveryPointId: recoveryPoint.id,
              holdType: "operational" as const,
              state: "active" as const,
              createdBy: 7,
              expiresAt: "2026-12-01T00:00:00.000Z",
              releasedBy: null,
              releasedAt: null,
              createdAt: "2026-08-17T00:00:00.000Z",
              updatedAt: "2026-08-17T00:00:00.000Z",
            },
          },
        ],
      }),
      createRecoveryPointHold: vi.fn().mockResolvedValue({
        status: "available",
        value: {
          id: createdOperationalId,
          recoveryPointId: recoveryPoint.id,
          holdType: "operational",
          state: "active",
          createdBy: 7,
          expiresAt: "2028-01-01T12:00:00.000Z",
          releasedBy: null,
          releasedAt: null,
          createdAt: "2026-08-17T00:00:00.000Z",
          updatedAt: "2026-08-17T00:00:00.000Z",
        },
      }),
      releaseRecoveryPointHold: vi.fn().mockResolvedValue({
        status: "available",
        value: {
          id: operationalId,
          recoveryPointId: recoveryPoint.id,
          holdType: "operational",
          state: "released",
          createdBy: 7,
          expiresAt: "2026-12-01T00:00:00.000Z",
          releasedBy: 7,
          releasedAt: "2026-08-17T03:00:00.000Z",
          createdAt: "2026-08-17T00:00:00.000Z",
          updatedAt: "2026-08-17T03:00:00.000Z",
        },
      }),
    };
    render(
      <RetentionPolicyPanel
        repositories={[{ status: "available", value: repository }]}
        recoveryPoints={[{ status: "available", value: recoveryPoint }]}
        selectedRecoveryPointId={recoveryPoint.id}
        runtime={{ token: "admin-token", role: "admin", ensureStepUpProof: vi.fn().mockResolvedValue("hold-proof") }}
        api={api}
      />,
    );
    expect(await screen.findByLabelText(new RegExp(`Hold legal ${legalId}|冻结 legal ${legalId}`))).toBeInTheDocument();
    expect(screen.getByLabelText(new RegExp(`Hold operational ${operationalId}|冻结 operational ${operationalId}`))).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: /Create hold|创建冻结/ }));
    await user.selectOptions(screen.getByLabelText(/Hold type|冻结类型/), "operational");
    await user.type(screen.getByLabelText(/Hold reason|冻结原因/), "ops-hold");
    expect(screen.getByRole("button", { name: /Confirm hold|确认冻结/ })).toBeDisabled();
    await user.type(screen.getByLabelText(/Hold expires at|冻结到期时间/), "2028-01-01T12:00");
    await user.click(screen.getByRole("button", { name: /Confirm hold|确认冻结/ }));
    expect(api.createRecoveryPointHold).toHaveBeenCalledWith(
      "admin-token",
      recoveryPoint.id,
      expect.objectContaining({
        holdType: "operational",
        reason: "ops-hold",
        expiresAt: expect.stringMatching(/^2028-01-01T/),
      }),
      expect.any(AbortSignal),
    );

    await user.type(screen.getByLabelText(new RegExp(`Release reason.*${operationalId}|解除原因.*${operationalId}`)), "done");
    await user.click(screen.getByRole("button", { name: new RegExp(`Release hold ${operationalId}|解除冻结 ${operationalId}`) }));
    expect(api.releaseRecoveryPointHold).toHaveBeenCalledWith(
      "admin-token",
      recoveryPoint.id,
      operationalId,
      expect.objectContaining({ reason: "done" }),
      expect.any(AbortSignal),
    );
  });

  it("follows policy pagination and edits calendar rules", async () => {
    const user = userEvent.setup();
    const first = policy();
    const second = { ...policy(), id: "b".repeat(32), revision: 1, rules: { version: 1 as const, count: { keepLatest: 3 } } };
    const api = {
      listRetentionPolicies: vi.fn()
        .mockResolvedValueOnce({
          items: [{ status: "available", value: first }],
          nextCursor: "policy-cursor-2",
        })
        .mockResolvedValueOnce({
          items: [{ status: "available", value: second }],
          nextCursor: null,
        }),
      createRetentionPolicy: vi.fn().mockResolvedValue({
        status: "available",
        value: {
          ...policy(),
          id: "c".repeat(32),
          revision: 1,
          rules: { version: 1, age: { keepDays: 30 }, calendar: [{ unit: "month", keep: 12 }] },
        },
      }),
      updateRetentionPolicy: vi.fn().mockResolvedValue({
        status: "available",
        value: { ...first, revision: 3, rules: { version: 1, age: { keepDays: 30 }, calendar: [{ unit: "week", keep: 8 }] } },
      }),
    };
    render(
      <RetentionPolicyPanel
        repositories={[{ status: "available", value: repository }]}
        recoveryPoints={[]}
        selectedRepositoryId={repository.id}
        runtime={{ token: "admin-token", role: "admin", ensureStepUpProof: vi.fn() }}
        api={api}
      />,
    );
    expect(await screen.findByRole("button", { name: new RegExp(`Update policy.*${first.id}|更新策略.*${first.id}`) })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: new RegExp(`Update policy.*${second.id}|更新策略.*${second.id}`) })).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /Load more|加载更多/ }));
    expect(api.listRetentionPolicies).toHaveBeenLastCalledWith(
      "admin-token",
      expect.objectContaining({ cursor: "policy-cursor-2" }),
    );
    expect(await screen.findByRole("button", { name: new RegExp(`Update policy.*${second.id}|更新策略.*${second.id}`) })).toBeInTheDocument();

    await user.selectOptions(screen.getAllByLabelText(/Calendar unit|日历单位/)[0], "month");
    await user.type(screen.getAllByLabelText(/Calendar keep|日历保留份数/)[0], "12");
    await user.click(screen.getByRole("button", { name: /Create policy|创建策略/ }));
    expect(api.createRetentionPolicy).toHaveBeenCalledWith(
      "admin-token",
      {
        scopeKind: "repository",
        scopeId: repository.id,
        rules: { version: 1, age: { keepDays: 30 }, calendar: [{ unit: "month", keep: 12 }] },
      },
      expect.any(AbortSignal),
    );

    await user.selectOptions(screen.getByLabelText(new RegExp(`Calendar unit.*${first.id}|日历单位.*${first.id}`)), "week");
    const calendarKeep = screen.getByLabelText(new RegExp(`Calendar keep.*${first.id}|日历保留份数.*${first.id}`));
    await user.clear(calendarKeep);
    await user.type(calendarKeep, "8");
    await user.click(screen.getByRole("button", { name: new RegExp(`Update policy.*${first.id}|更新策略.*${first.id}`) }));
    expect(api.updateRetentionPolicy).toHaveBeenCalledWith(
      "admin-token",
      first.id,
      {
        expectedRevision: 2,
        rules: {
          version: 1,
          age: { keepDays: 30 },
          calendar: [{ unit: "week", keep: 8 }],
        },
      },
      expect.any(AbortSignal),
    );
  });

  it("names policy row actions per policy id", async () => {
    const api = {
      listRetentionPolicies: vi.fn().mockResolvedValue({
        items: [{ status: "available", value: policy() }],
        nextCursor: null,
      }),
    };
    render(
      <RetentionPolicyPanel
        repositories={[{ status: "available", value: repository }]}
        recoveryPoints={[{ status: "available", value: recoveryPoint }]}
        runtime={{ token: "admin-token", role: "admin", ensureStepUpProof: vi.fn() }}
        api={api}
      />,
    );
    expect(await screen.findByRole("button", { name: new RegExp(`Update policy.*${policyId}|更新策略.*${policyId}`) })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: new RegExp(`Delete policy.*${policyId}|删除策略.*${policyId}`) })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: new RegExp(`Preview impact.*${policyId}|预览影响.*${policyId}`) })).toBeInTheDocument();
  });

  it("scopes the policy panel to the current repository and does not mutate the other", async () => {
    const user = userEvent.setup();
    const otherRepository = { ...repository, id: "e".repeat(32), displayName: "Other repository" };
    const otherPolicy = { ...policy(), id: "f".repeat(32), scopeId: otherRepository.id };
    const api = {
      listRetentionPolicies: vi.fn().mockResolvedValue({
        items: [
          { status: "available", value: policy() },
          { status: "available", value: otherPolicy },
        ],
        nextCursor: null,
      }),
      updateRetentionPolicy: vi.fn().mockResolvedValue({
        status: "available",
        value: { ...policy(), revision: 3 },
      }),
    };
    render(
      <RetentionPolicyPanel
        repositories={[{ status: "available", value: repository }, { status: "available", value: otherRepository }]}
        recoveryPoints={[]}
        selectedRepositoryId={repository.id}
        runtime={{ token: "admin-token", role: "admin", ensureStepUpProof: vi.fn() }}
        api={api}
      />,
    );
    expect(await screen.findByRole("button", { name: new RegExp(`Update policy.*${policyId}|更新策略.*${policyId}`) })).toBeInTheDocument();
    expect(screen.getByText(/Repository|仓库/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: new RegExp(`Update policy.*${otherPolicy.id}|更新策略.*${otherPolicy.id}`) })).not.toBeInTheDocument();
    expect(api.updateRetentionPolicy).not.toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: new RegExp(`Update policy.*${policyId}|更新策略.*${policyId}`) }));
    expect(api.updateRetentionPolicy).toHaveBeenCalledWith(
      "admin-token",
      policyId,
      expect.objectContaining({ expectedRevision: 2 }),
      expect.any(AbortSignal),
    );
  });

  it("hides every policy until a repository is selected when several repositories exist", async () => {
    const otherRepository = { ...repository, id: "e".repeat(32), displayName: "Other repository" };
    const otherPolicy = { ...policy(), id: "f".repeat(32), scopeId: otherRepository.id };
    render(
      <RetentionPolicyPanel
        repositories={[{ status: "available", value: repository }, { status: "available", value: otherRepository }]}
        recoveryPoints={[]}
        runtime={{ token: "admin-token", role: "admin", ensureStepUpProof: vi.fn() }}
        api={{
          listRetentionPolicies: vi.fn().mockResolvedValue({
            items: [
              { status: "available", value: policy() },
              { status: "available", value: otherPolicy },
            ],
            nextCursor: null,
          }),
        }}
      />,
    );
    await screen.findByRole("button", { name: /Create policy|创建策略/ });
    expect(screen.queryByRole("button", { name: new RegExp(`Update policy.*${policyId}|更新策略.*${policyId}`) })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: new RegExp(`Update policy.*${otherPolicy.id}|更新策略.*${otherPolicy.id}`) })).not.toBeInTheDocument();
  });

  it("preserves every calendar rule when one calendar field is edited", async () => {
    const user = userEvent.setup();
    const mixed = {
      ...policy(),
      rules: {
        version: 1 as const,
        calendar: [
          { unit: "day" as const, keep: 7 },
          { unit: "week" as const, keep: 4 },
          { unit: "month" as const, keep: 12 },
        ],
      },
    };
    const api = {
      listRetentionPolicies: vi.fn().mockResolvedValue({
        items: [{ status: "available", value: mixed }],
        nextCursor: null,
      }),
      updateRetentionPolicy: vi.fn().mockResolvedValue({
        status: "available",
        value: { ...mixed, revision: 3, rules: { ...mixed.rules, calendar: [{ unit: "day", keep: 7 }, { unit: "week", keep: 6 }, { unit: "month", keep: 12 }] } },
      }),
    };
    render(
      <RetentionPolicyPanel
        repositories={[{ status: "available", value: repository }]}
        recoveryPoints={[]}
        selectedRepositoryId={repository.id}
        runtime={{ token: "admin-token", role: "admin", ensureStepUpProof: vi.fn() }}
        api={api}
      />,
    );
    const weekKeep = await screen.findByLabelText(new RegExp(`Calendar keep.*${mixed.id} 1|日历保留份数.*${mixed.id} 1`));
    await user.clear(weekKeep);
    await user.type(weekKeep, "6");
    await user.click(screen.getByRole("button", { name: new RegExp(`Update policy.*${mixed.id}|更新策略.*${mixed.id}`) }));
    expect(api.updateRetentionPolicy).toHaveBeenCalledWith(
      "admin-token",
      mixed.id,
      {
        expectedRevision: 2,
        rules: {
          version: 1,
          calendar: [
            { unit: "day", keep: 7 },
            { unit: "week", keep: 6 },
            { unit: "month", keep: 12 },
          ],
        },
      },
      expect.any(AbortSignal),
    );
  });

  it("persists exact policy impact IDs and hold lease WORM counts", async () => {
    const user = userEvent.setup();
    const pointId = recoveryPoint.id;
    const api = {
      listRetentionPolicies: vi.fn().mockResolvedValue({
        items: [{ status: "available", value: policy() }],
        nextCursor: null,
      }),
      previewRetentionPolicyImpact: vi.fn().mockResolvedValue({
        status: "available",
        value: {
          policyId,
          policyRevision: 2,
          impactRevision: 11,
          evaluatedAt: "2026-08-17T01:00:00.000Z",
          selectedCount: 1,
          holdCount: 2,
          leaseCount: 3,
          wormCount: 4,
          points: [{ recoveryPointId: pointId, pointRevision: 3, capabilityRevision: 5 }],
        },
      }),
    };
    render(
      <RetentionPolicyPanel
        repositories={[{ status: "available", value: repository }]}
        recoveryPoints={[]}
        selectedRepositoryId={repository.id}
        runtime={{ token: "admin-token", role: "admin", ensureStepUpProof: vi.fn() }}
        api={api}
      />,
    );
    await user.click(await screen.findByRole("button", { name: /Preview impact|预览影响/ }));
    expect(await screen.findByText(/Holds 2|冻结 2/)).toBeInTheDocument();
    expect(screen.getByText(/Leases 3|租约 3/)).toBeInTheDocument();
    expect(screen.getByText(/WORM 4/)).toBeInTheDocument();
    expect(screen.getByRole("list", { name: /Selected point IDs|选中的恢复点/ })).toHaveTextContent(pointId);
  });

  it("updates count-only policies and requires the policy id to delete", async () => {
    const user = userEvent.setup();
    const countOnly = {
      ...policy(),
      rules: { version: 1 as const, count: { keepLatest: 5 } },
    };
    const api = {
      listRetentionPolicies: vi.fn().mockResolvedValue({
        items: [{ status: "available", value: countOnly }],
        nextCursor: null,
      }),
      updateRetentionPolicy: vi.fn().mockResolvedValue({
        status: "available",
        value: { ...countOnly, revision: 3, rules: { version: 1, count: { keepLatest: 9 } } },
      }),
      deleteRetentionPolicy: vi.fn().mockResolvedValue({
        status: "available",
        value: { ...countOnly, revision: 3, status: "deleted" },
      }),
    };
    render(
      <RetentionPolicyPanel
        repositories={[{ status: "available", value: repository }]}
        recoveryPoints={[]}
        selectedRepositoryId={repository.id}
        runtime={{ token: "admin-token", role: "admin", ensureStepUpProof: vi.fn() }}
        api={api}
      />,
    );
    const latest = await screen.findByLabelText(new RegExp(`Keep latest.*${countOnly.id}|保留最近份数.*${countOnly.id}`));
    await user.clear(latest);
    await user.type(latest, "9");
    await user.click(screen.getByRole("button", { name: new RegExp(`Update policy.*${countOnly.id}|更新策略.*${countOnly.id}`) }));
    expect(api.updateRetentionPolicy).toHaveBeenCalledWith(
      "admin-token",
      countOnly.id,
      { expectedRevision: 2, rules: { version: 1, count: { keepLatest: 9 } } },
      expect.any(AbortSignal),
    );
    await user.click(screen.getByRole("button", { name: /Delete policy|删除策略/ }));
    expect(screen.getByRole("button", { name: /Confirm delete policy|确认删除策略/ })).toBeDisabled();
    await user.type(screen.getByLabelText(/Type policy ID|输入策略 ID/), countOnly.id);
    await user.click(screen.getByRole("button", { name: /Confirm delete policy|确认删除策略/ }));
    expect(api.deleteRetentionPolicy).toHaveBeenCalledWith("admin-token", countOnly.id, 3, expect.any(AbortSignal));
  });

  it("omits mutable heads from the hold picker", async () => {
    const mutableHead = { ...recoveryPoint, id: "c".repeat(32), semantics: "mutable_head" as const };
    const api = {
      listRetentionPolicies: vi.fn().mockResolvedValue({ items: [], nextCursor: null }),
    };
    render(
      <RetentionPolicyPanel
        repositories={[{ status: "available", value: repository }]}
        recoveryPoints={[
          { status: "available", value: recoveryPoint },
          { status: "available", value: mutableHead },
        ]}
        selectedRepositoryId={repository.id}
        runtime={{ token: "admin-token", role: "admin", ensureStepUpProof: vi.fn() }}
        api={api}
      />,
    );
    const picker = await screen.findByLabelText(/Hold recovery point|冻结恢复点/);
    expect(picker).toHaveTextContent(recoveryPoint.id);
    expect(picker).not.toHaveTextContent(mutableHead.id);
  });

  it("does not drop page 1 hold lease WORM counts when loading more impact", async () => {
    const user = userEvent.setup();
    const pageOnePoint = recoveryPoint.id;
    const pageTwoPoint = "e".repeat(32);
    const api = {
      listRetentionPolicies: vi.fn().mockResolvedValue({
        items: [{ status: "available", value: policy() }],
        nextCursor: null,
      }),
      previewRetentionPolicyImpact: vi.fn()
        .mockResolvedValueOnce({
          status: "available",
          value: {
            policyId,
            policyRevision: 2,
            impactRevision: 11,
            evaluatedAt: "2026-08-17T01:00:00.000Z",
            selectedCount: 1,
            holdCount: 2,
            leaseCount: 3,
            wormCount: 4,
            nextCursor: "impact-page-2",
            points: [{ recoveryPointId: pageOnePoint, pointRevision: 3, capabilityRevision: 5 }],
          },
        })
        .mockResolvedValueOnce({
          status: "available",
          value: {
            policyId,
            policyRevision: 2,
            impactRevision: 11,
            evaluatedAt: "2026-08-17T01:00:00.000Z",
            selectedCount: 1,
            holdCount: 5,
            leaseCount: 1,
            wormCount: 2,
            nextCursor: null,
            points: [{ recoveryPointId: pageTwoPoint, pointRevision: 4, capabilityRevision: 6 }],
          },
        }),
    };
    render(
      <RetentionPolicyPanel
        repositories={[{ status: "available", value: repository }]}
        recoveryPoints={[]}
        selectedRepositoryId={repository.id}
        runtime={{ token: "admin-token", role: "admin", ensureStepUpProof: vi.fn() }}
        api={api}
      />,
    );
    await user.click(await screen.findByRole("button", { name: /Preview impact|预览影响/ }));
    expect(await screen.findByText(/Holds 2|冻结 2/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /Load more|加载更多/ }));
    expect(await screen.findByText(/Holds 7|冻结 7/)).toBeInTheDocument();
    expect(screen.getByText(/Leases 4|租约 4/)).toBeInTheDocument();
    expect(screen.getByText(/WORM 6/)).toBeInTheDocument();
    expect(screen.getByRole("list", { name: /Selected point IDs|选中的恢复点/ })).toHaveTextContent(pageOnePoint);
    expect(screen.getByRole("list", { name: /Selected point IDs|选中的恢复点/ })).toHaveTextContent(pageTwoPoint);
  });

  it("lists every active task-link policy and can create against the second link", async () => {
    const user = userEvent.setup();
    const firstLinkId = "11".repeat(16);
    const secondLinkId = "22".repeat(16);
    const firstPolicy = {
      ...policy(),
      id: "d1".repeat(16),
      scopeKind: "task_link" as const,
      scopeId: firstLinkId,
    };
    const secondPolicy = {
      ...policy(),
      id: "d2".repeat(16),
      scopeKind: "task_link" as const,
      scopeId: secondLinkId,
      rules: { version: 1 as const, count: { keepLatest: 4 } },
    };
    const dualLinkRepository = {
      ...repository,
      lineages: [
        {
          source: "task_link" as const,
          taskRepositoryLinkId: firstLinkId,
          taskName: "task-one",
          nodeId: 1,
          nodeName: "node-a",
          active: true,
        },
        {
          source: "task_link" as const,
          taskRepositoryLinkId: secondLinkId,
          taskName: "task-two",
          nodeId: 1,
          nodeName: "node-a",
          active: true,
        },
      ],
    };
    const api = {
      listRetentionPolicies: vi.fn().mockResolvedValue({
        items: [
          { status: "available", value: firstPolicy },
          { status: "available", value: secondPolicy },
        ],
        nextCursor: null,
      }),
      createRetentionPolicy: vi.fn().mockResolvedValue({
        status: "available",
        value: {
          ...policy(),
          id: "d3".repeat(16),
          scopeKind: "task_link",
          scopeId: secondLinkId,
          revision: 1,
        },
      }),
    };
    render(
      <RetentionPolicyPanel
        repositories={[{ status: "available", value: dualLinkRepository }]}
        recoveryPoints={[]}
        selectedRepositoryId={dualLinkRepository.id}
        runtime={{ token: "admin-token", role: "admin", ensureStepUpProof: vi.fn() }}
        api={api}
      />,
    );
    expect(await screen.findByRole("button", { name: new RegExp(`Update policy.*${firstPolicy.id}|更新策略.*${firstPolicy.id}`) })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: new RegExp(`Update policy.*${secondPolicy.id}|更新策略.*${secondPolicy.id}`) })).toBeInTheDocument();
    await user.selectOptions(screen.getByLabelText(/Policy scope|策略范围/), "task_link");
    await user.selectOptions(screen.getByLabelText(/Task link|任务链接/), secondLinkId);
    await user.click(screen.getByRole("button", { name: /Create policy|创建策略/ }));
    expect(api.createRetentionPolicy).toHaveBeenCalledWith(
      "admin-token",
      expect.objectContaining({ scopeKind: "task_link", scopeId: secondLinkId }),
      expect.any(AbortSignal),
    );
  });

  it("creates count-only and calendar-only policies", async () => {
    const user = userEvent.setup();
    const api = {
      listRetentionPolicies: vi.fn().mockResolvedValue({ items: [], nextCursor: null }),
      createRetentionPolicy: vi.fn()
        .mockResolvedValueOnce({
          status: "available",
          value: { ...policy(), id: "c1".repeat(16), revision: 1, rules: { version: 1, count: { keepLatest: 3 } } },
        })
        .mockResolvedValueOnce({
          status: "available",
          value: { ...policy(), id: "c2".repeat(16), revision: 1, rules: { version: 1, calendar: [{ unit: "week", keep: 4 }] } },
        }),
    };
    render(
      <RetentionPolicyPanel
        repositories={[{ status: "available", value: repository }]}
        recoveryPoints={[]}
        selectedRepositoryId={repository.id}
        runtime={{ token: "admin-token", role: "admin", ensureStepUpProof: vi.fn() }}
        api={api}
      />,
    );
    await screen.findByRole("button", { name: /Create policy|创建策略/ });
    await user.clear(screen.getByLabelText(/^Keep days$|^保留天数$/));
    await user.type(screen.getByLabelText(/^Keep latest$|^保留最近份数$/), "3");
    await user.click(screen.getByRole("button", { name: /Create policy|创建策略/ }));
    expect(api.createRetentionPolicy).toHaveBeenCalledWith(
      "admin-token",
      {
        scopeKind: "repository",
        scopeId: repository.id,
        rules: { version: 1, count: { keepLatest: 3 } },
      },
      expect.any(AbortSignal),
    );
    await user.clear(screen.getByRole("textbox", { name: /^Keep latest$|^保留最近份数$/ }));
    await user.selectOptions(screen.getByRole("combobox", { name: /^Calendar unit$|^日历单位$/ }), "week");
    await user.type(screen.getByRole("textbox", { name: /^Calendar keep$|^日历保留份数$/ }), "4");
    await user.click(screen.getByRole("button", { name: /Create policy|创建策略/ }));
    expect(api.createRetentionPolicy).toHaveBeenLastCalledWith(
      "admin-token",
      {
        scopeKind: "repository",
        scopeId: repository.id,
        rules: { version: 1, calendar: [{ unit: "week", keep: 4 }] },
      },
      expect.any(AbortSignal),
    );
  });

  it("removes age and can add or remove calendar rows on update", async () => {
    const user = userEvent.setup();
    const mixed = {
      ...policy(),
      rules: {
        version: 1 as const,
        age: { keepDays: 30 },
        count: { keepLatest: 5 },
        calendar: [{ unit: "day" as const, keep: 7 }],
      },
    };
    const api = {
      listRetentionPolicies: vi.fn().mockResolvedValue({
        items: [{ status: "available", value: mixed }],
        nextCursor: null,
      }),
      updateRetentionPolicy: vi.fn()
        .mockResolvedValueOnce({
          status: "available",
          value: { ...mixed, revision: 3, rules: { version: 1, count: { keepLatest: 5 }, calendar: [{ unit: "day", keep: 7 }] } },
        })
        .mockResolvedValueOnce({
          status: "available",
          value: {
            ...mixed,
            revision: 4,
            rules: { version: 1, count: { keepLatest: 5 }, calendar: [{ unit: "day", keep: 7 }, { unit: "week", keep: 4 }] },
          },
        })
        .mockResolvedValueOnce({
          status: "available",
          value: { ...mixed, revision: 5, rules: { version: 1, count: { keepLatest: 5 }, calendar: [{ unit: "week", keep: 4 }] } },
        }),
    };
    render(
      <RetentionPolicyPanel
        repositories={[{ status: "available", value: repository }]}
        recoveryPoints={[]}
        selectedRepositoryId={repository.id}
        runtime={{ token: "admin-token", role: "admin", ensureStepUpProof: vi.fn() }}
        api={api}
      />,
    );
    const keepDays = await screen.findByLabelText(new RegExp(`Keep days.*${mixed.id}|保留天数.*${mixed.id}`));
    await user.clear(keepDays);
    await user.click(screen.getByRole("button", { name: new RegExp(`Update policy.*${mixed.id}|更新策略.*${mixed.id}`) }));
    expect(api.updateRetentionPolicy).toHaveBeenCalledWith(
      "admin-token",
      mixed.id,
      {
        expectedRevision: 2,
        rules: { version: 1, count: { keepLatest: 5 }, calendar: [{ unit: "day", keep: 7 }] },
      },
      expect.any(AbortSignal),
    );
    await user.click(screen.getByRole("button", { name: /Add calendar rule|添加日历规则/ }));
    await user.selectOptions(screen.getByLabelText(new RegExp(`Calendar unit.*${mixed.id} 1|日历单位.*${mixed.id} 1`)), "week");
    await user.type(screen.getByLabelText(new RegExp(`Calendar keep.*${mixed.id} 1|日历保留份数.*${mixed.id} 1`)), "4");
    await user.click(screen.getByRole("button", { name: new RegExp(`Update policy.*${mixed.id}|更新策略.*${mixed.id}`) }));
    expect(api.updateRetentionPolicy).toHaveBeenNthCalledWith(
      2,
      "admin-token",
      mixed.id,
      {
        expectedRevision: 3,
        rules: { version: 1, count: { keepLatest: 5 }, calendar: [{ unit: "day", keep: 7 }, { unit: "week", keep: 4 }] },
      },
      expect.any(AbortSignal),
    );
    await user.click(screen.getAllByRole("button", { name: /Remove calendar rule|删除日历规则/ })[0]);
    await user.click(screen.getByRole("button", { name: new RegExp(`Update policy.*${mixed.id}|更新策略.*${mixed.id}`) }));
    expect(api.updateRetentionPolicy).toHaveBeenLastCalledWith(
      "admin-token",
      mixed.id,
      {
        expectedRevision: 4,
        rules: { version: 1, count: { keepLatest: 5 }, calendar: [{ unit: "week", keep: 4 }] },
      },
      expect.any(AbortSignal),
    );
  });
});
