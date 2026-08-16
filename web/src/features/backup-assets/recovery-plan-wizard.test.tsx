import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import type { RecoveryJob, RecoveryPlan, RecoveryPreflight } from "@/lib/api/backup-recovery-api";
import type { BackupRecoveryState } from "./use-backup-recovery";

import { RecoveryPlanWizard } from "./recovery-plan-wizard";

const plan: RecoveryPlan = {
  id: "1".repeat(32), state: "draft", revision: "2", repositoryId: "3".repeat(32),
  recoveryPointId: "4".repeat(32), targetMode: "in_place", targetNodeId: 9,
  targetRootId: "safe-root", conflictPolicy: "exact_mirror", securityDecision: "block",
  estimatedItems: 2, estimatedBytes: 20, createdAt: "2026-08-16T12:00:00Z", updatedAt: "2026-08-16T12:00:00Z",
};

const preflight: RecoveryPreflight = {
  planId: plan.id, persisted: true, planRevision: "2", eligible: false, preferred: false,
  reasons: ["security_blocked"], preflightId: "5".repeat(32), targetMode: "in_place",
  conflictPolicy: "exact_mirror",
  impact: { createCount: 1, overwriteCount: 1, skipCount: 0, deleteCount: 2, estimatedItems: 4, estimatedBytes: 20 },
  security: { decision: "block", findingCount: 1, overridableCategories: [] },
  observedAt: "2026-08-16T12:00:00Z", expiresAt: "2026-08-16T12:05:00Z",
};

function recoveryState(overrides: Partial<BackupRecoveryState> = {}): BackupRecoveryState {
  return {
    phase: "target",
    selection: [{ recoveryPointId: plan.recoveryPointId, entryId: "6".repeat(64) }],
    source: { repositoryId: plan.repositoryId, catalogGenerationId: "7".repeat(32) },
    target: null,
    plan: null,
    preflight: null,
    writeGrant: null,
    job: null,
    itemPage: null,
    resultPage: null,
    ticket: null,
    error: null,
    announcement: null,
    ...overrides,
  };
}

function controller(state: BackupRecoveryState) {
  return {
    state,
    open: vi.fn(),
    setTarget: vi.fn(),
    createPlan: vi.fn().mockResolvedValue(undefined),
    runPreflight: vi.fn().mockResolvedValue(undefined),
    overrideSecurity: vi.fn().mockResolvedValue(undefined),
    authorizeWrite: vi.fn().mockResolvedValue(undefined),
    execute: vi.fn().mockResolvedValue(undefined),
    authorizeExactMirrorDelete: vi.fn().mockResolvedValue(undefined),
    loadJobItems: vi.fn().mockResolvedValue(undefined),
    loadJobResults: vi.fn().mockResolvedValue(undefined),
    retainResults: vi.fn().mockResolvedValue(undefined),
    downloadResult: vi.fn().mockResolvedValue(undefined),
    cleanupResults: vi.fn().mockResolvedValue(undefined),
    cancelRecovery: vi.fn().mockResolvedValue(undefined),
    dismiss: vi.fn(),
  };
}

function pausedJob(): RecoveryJob {
  return {
    id: "8".repeat(32), planId: plan.id, outcome: "running", revision: "4",
    targetMode: "in_place", targetNodeId: 9, targetRootId: "safe-root",
    estimatedItems: 4, estimatedBytes: 20,
    progress: { totalItems: 4, completedItems: 2, succeededItems: 2, skippedItems: 0, failedItems: 0, bytesWritten: 10 },
    failureCategory: null,
    deleteCheckpoint: {
      id: "9".repeat(32), attemptId: "a".repeat(32), expectedPlanRevision: "4",
      status: "awaiting_authorization", expiresAt: "2026-08-16T12:05:00Z",
    },
    resultSet: null, plaintextDeadline: null,
    createdAt: "2026-08-16T12:00:00Z", updatedAt: "2026-08-16T12:01:00Z",
  };
}

describe("RecoveryPlanWizard", () => {
  it("owns labelled target inputs and advances only through controller actions", async () => {
    const user = userEvent.setup();
    const recovery = controller(recoveryState());
    render(<RecoveryPlanWizard open recovery={recovery} onOpenChange={vi.fn()} />);

    expect(screen.getByRole("dialog")).toHaveAttribute("aria-describedby");
    await user.selectOptions(screen.getByLabelText(/Target mode|目标模式/), "isolated");
    await user.clear(screen.getByLabelText(/Target node|目标节点/));
    await user.type(screen.getByLabelText(/Target node|目标节点/), "9");
    await user.type(screen.getByLabelText(/Target root|目标根/), "safe-root");
    await user.selectOptions(screen.getByLabelText(/Conflict policy|冲突策略/), "fail_on_conflict");
    await user.click(screen.getByRole("button", { name: /Create recovery plan|创建恢复计划/ }));

    expect(recovery.setTarget).toHaveBeenCalledWith({
      targetMode: "isolated", targetNodeId: 9, targetRootId: "safe-root", conflictPolicy: "fail_on_conflict",
    });
    expect(recovery.createPlan).toHaveBeenCalledTimes(1);
    expect(screen.getByTestId("recovery-announcement")).toHaveAttribute("aria-live", "polite");
  });

  it("never exposes override for a non-overridable finding and requires its own confirmation otherwise", async () => {
    const blocked = controller(recoveryState({ phase: "security", plan, preflight }));
    const rendered = render(<RecoveryPlanWizard open recovery={blocked} onOpenChange={vi.fn()} />);

    expect(screen.queryByRole("button", { name: /Override security finding|覆盖安全发现/ })).not.toBeInTheDocument();
    expect(screen.getByText(/cannot be overridden|不能覆盖/)).toBeInTheDocument();

    const overridable = controller(recoveryState({
      phase: "security",
      plan,
      preflight: { ...preflight, security: { ...preflight.security, overridableCategories: ["malware"] } },
    }));
    rendered.rerender(<RecoveryPlanWizard open recovery={overridable} onOpenChange={vi.fn()} />);
    const overrideButton = screen.getByRole("button", { name: /Override security finding|覆盖安全发现/ });
    expect(overrideButton).toBeDisabled();
    fireEvent.change(screen.getByLabelText(/Security override reason|安全覆盖原因/), { target: { value: "Reviewed by incident commander" } });
    fireEvent.click(screen.getByLabelText(/confirm this security override|确认此次安全覆盖/i));
    expect(overrideButton).toBeEnabled();
    fireEvent.click(overrideButton);
    expect(overridable.overrideSecurity).toHaveBeenCalledWith("malware", "Reviewed by incident commander", true);
    expect(screen.getByLabelText(/Security override reason|安全覆盖原因/)).toHaveValue("Reviewed by incident commander");
  });

  it("uses a second independent confirmation at the exact-mirror delete checkpoint and transfers focus", async () => {
    const recovery = controller(recoveryState({ phase: "delete_authorization", plan, preflight, job: pausedJob() }));
    render(<RecoveryPlanWizard open recovery={recovery} onOpenChange={vi.fn()} />);

    const heading = screen.getByRole("heading", { name: /Authorize exact-mirror deletion|授权精确镜像删除/ });
    await waitFor(() => expect(heading).toHaveFocus());
    const button = screen.getByRole("button", { name: /Authorize deletion|授权删除/ });
    expect(button).toBeDisabled();
    fireEvent.change(screen.getByLabelText(/Deletion reason|删除原因/), { target: { value: "Mirror target must match source" } });
    fireEvent.click(screen.getByLabelText(/confirm target deletion|精确镜像检查点/i));
    fireEvent.click(button);

    expect(recovery.authorizeExactMirrorDelete).toHaveBeenCalledWith("Mirror target must match source", true);
    expect(screen.getByLabelText(/Deletion reason|删除原因/)).toHaveValue("Mirror target must match source");
    expect(screen.getByLabelText(/confirm target deletion|精确镜像检查点/i)).toBeChecked();
    expect(screen.queryByLabelText(/confirm this security override|确认此次安全覆盖/i)).not.toBeInTheDocument();
  });

  it("clears the write reason when the plan context is replaced", () => {
    const first = controller(recoveryState({
      phase: "impact",
      plan: { ...plan, securityDecision: "allow_clean" },
      preflight: { ...preflight, eligible: true, reasons: [], security: { ...preflight.security, decision: "allow_clean" } },
    }));
    const rendered = render(<RecoveryPlanWizard open recovery={first} onOpenChange={vi.fn()} />);
    const reason = screen.getByLabelText(/Write authorization reason|写入授权原因/);
    fireEvent.change(reason, { target: { value: "sensitive write reason" } });
    expect(reason).toHaveValue("sensitive write reason");

    const second = controller(recoveryState({
      phase: "impact",
      plan: { ...plan, id: "b".repeat(32), securityDecision: "allow_clean" },
      preflight: {
        ...preflight,
        planId: "b".repeat(32),
        preflightId: "c".repeat(32),
        eligible: true,
        reasons: [],
        security: { ...preflight.security, decision: "allow_clean" },
      },
    }));
    rendered.rerender(<RecoveryPlanWizard open recovery={second} onOpenChange={vi.fn()} />);

    expect(screen.getByLabelText(/Write authorization reason|写入授权原因/)).toHaveValue("");
    expect(document.body).not.toHaveTextContent("sensitive write reason");
  });

  it("drives preflight, write authority, progress, verification and isolated result actions without exposing authority material", async () => {
    const recovery = controller(recoveryState({ phase: "target", plan }));
    const rendered = render(<RecoveryPlanWizard open recovery={recovery} onOpenChange={vi.fn()} />);

    fireEvent.click(screen.getByRole("button", { name: /Run preflight|运行预检/ }));
    expect(recovery.runPreflight).toHaveBeenCalledTimes(1);

    recovery.state = recoveryState({
      phase: "impact",
      plan: { ...plan, securityDecision: "allow_clean" },
      preflight: { ...preflight, eligible: true, reasons: [], security: { ...preflight.security, decision: "allow_clean" } },
    });
    rendered.rerender(<RecoveryPlanWizard open recovery={recovery} onOpenChange={vi.fn()} />);
    fireEvent.change(screen.getByLabelText(/Write authorization reason|写入授权原因/), { target: { value: "approved maintenance window" } });
    fireEvent.click(screen.getByRole("button", { name: /Authorize write|授权写入/ }));
    expect(recovery.authorizeWrite).toHaveBeenCalledWith("approved maintenance window");
    expect(screen.getByLabelText(/Write authorization reason|写入授权原因/)).toHaveValue("approved maintenance window");

    recovery.state = recoveryState({
      phase: "impact",
      plan: { ...plan, securityDecision: "allow_clean" },
      preflight: { ...preflight, eligible: true, reasons: [], security: { ...preflight.security, decision: "allow_clean" } },
      writeGrant: { id: "d".repeat(32), category: "write", expiresAt: "2026-08-16T12:05:00Z", status: "issued" },
    });
    rendered.rerender(<RecoveryPlanWizard open recovery={recovery} onOpenChange={vi.fn()} />);
    expect(document.body).not.toHaveTextContent("approved maintenance window");
    fireEvent.click(screen.getByRole("button", { name: /Execute recovery|执行恢复/ }));
    expect(recovery.execute).toHaveBeenCalledTimes(1);

    const runningJob: RecoveryJob = {
      ...pausedJob(),
      targetMode: "isolated",
      outcome: "running",
      deleteCheckpoint: null,
    };
    recovery.state = recoveryState({ phase: "progress", plan, preflight, job: runningJob, writeGrant: null });
    rendered.rerender(<RecoveryPlanWizard open recovery={recovery} onOpenChange={vi.fn()} />);
    await waitFor(() => expect(screen.getByRole("heading", { name: /Recovery progress|恢复进度/ })).toHaveFocus());
    fireEvent.click(screen.getByRole("button", { name: /Load item operations|加载逐项操作/ }));
    expect(recovery.loadJobItems).toHaveBeenCalledWith(1, 25);
    fireEvent.click(screen.getByRole("button", { name: /Cancel recovery|取消恢复/ }));
    expect(recovery.cancelRecovery).toHaveBeenCalledTimes(1);

    recovery.state = recoveryState({ phase: "verification", plan, preflight, job: { ...runningJob, outcome: "verifying" } });
    rendered.rerender(<RecoveryPlanWizard open recovery={recovery} onOpenChange={vi.fn()} />);
    await waitFor(() => expect(screen.getByRole("heading", { name: /Recovery verification|恢复验证/ })).toHaveFocus());

    const readyJob: RecoveryJob = {
      ...runningJob,
      outcome: "succeeded",
      revision: "9",
      progress: { ...runningJob.progress, completedItems: 4, succeededItems: 4 },
      resultSet: {
        id: "e".repeat(32), lifecycle: "ready", plaintextDeadline: "2026-08-16T13:00:00Z",
        hardDeadline: "2026-08-16T14:00:00Z", createdAt: "2026-08-16T12:00:00Z", updatedAt: "2026-08-16T12:10:00Z",
      },
      plaintextDeadline: "2026-08-16T13:00:00Z",
    };
    recovery.state = recoveryState({
      phase: "result",
      plan,
      preflight,
      job: readyJob,
      resultPage: {
        jobId: readyJob.id,
        resultSet: readyJob.resultSet!,
        page: 1,
        pageSize: 25,
        total: 1,
        items: [{ id: "f".repeat(32), kind: "verification_report", size: 20, modifiedAt: null, createdAt: "2026-08-16T12:10:00Z" }],
      },
    });
    rendered.rerender(<RecoveryPlanWizard open recovery={recovery} onOpenChange={vi.fn()} />);
    fireEvent.click(screen.getByRole("button", { name: /Download result|下载结果/ }));
    fireEvent.click(screen.getByRole("button", { name: /Retain results|延长结果保留/ }));
    fireEvent.click(screen.getByRole("button", { name: /Clean up results|清理结果/ }));
    expect(recovery.downloadResult).toHaveBeenCalledWith("f".repeat(32));
    expect(recovery.retainResults).toHaveBeenCalledWith(expect.stringMatching(/^2026-08-16T13:00:00\.000Z$/));
    expect(recovery.cleanupResults).toHaveBeenCalledTimes(1);
    expect(document.body).not.toHaveTextContent(/grant[_ ]?secret|step-up proof/i);
  });
});
