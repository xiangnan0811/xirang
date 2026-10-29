import "@testing-library/jest-dom/vitest";
import { StrictMode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor, type RenderResult } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { mapRclonePreflightResult } from "@/lib/api/tasks-api";
import { runAxe } from "@/test/a11y-helpers";
import { ApiError } from "@/lib/api/core";
import { formatTime } from "@/lib/date-utils";
import type {
  RcloneBindingSetupResult,
  RclonePublicationSummary,
  RcloneVersioningActivationResult,
  RcloneVersioningPreflightResult,
  TaskRecord,
} from "@/types/domain";
import { TaskRcloneVersioningDialog } from "./task-rclone-versioning-dialog";

const { apiClientMock, authRef } = vi.hoisted(() => ({
  authRef: {
    current: {
      token: "token" as string | null,
      role: "admin" as "admin" | "operator" | "viewer" | null,
    },
  },
  apiClientMock: {
    createRclonePortableBindingSetup: vi.fn(),
    setRclonePortableBinding: vi.fn(),
    createRcloneNativeBindingSetup: vi.fn(),
    setRcloneNativeBinding: vi.fn(),
    createRcloneVersioningPreflight: vi.fn(),
    activateRcloneVersioning: vi.fn(),
    cleanRollbackRcloneVersioning: vi.fn(),
    prepareRcloneVersioningRollback: vi.fn(),
  },
}));

vi.mock("@/lib/api/client", () => ({ apiClient: apiClientMock }));
vi.mock("@/context/auth-context.hooks", () => ({
  useAuth: () => authRef.current,
}));

const legacySummary: RclonePublicationSummary = {
  mode: "legacy_mutable",
  state: "legacy",
  reasonCode: "legacy",
  taskRevision: "9007199254740993",
  bindingRevision: "0",
  capabilityRevision: "0",
  consistencyClass: "not_evaluated",
  hashFidelity: "not_evaluated",
  estimatedReadBytes: "0",
  apiCostClass: "not_evaluated",
  storageCostClass: "not_evaluated",
  egressCostClass: "not_evaluated",
  encryptionProfile: "none",
  kmsKeyStatus: "not_applicable",
  kmsReadKeyCount: 0,
  rollbackLocatorPresent: false,
  rollbackCapability: "preparation_only",
};

const task: TaskRecord = {
  id: 17,
  name: "nightly-rclone",
  policyName: "nightly-rclone",
  nodeId: 1,
  nodeName: "backup-node",
  status: "pending",
  progress: 0,
  startedAt: "-",
  speedMbps: 0,
  enabled: false,
  executorType: "rclone",
  rclonePublication: legacySummary,
};

const futureExpiry = () => new Date(Date.now() + 60 * 60 * 1000).toISOString();
const pastExpiry = () => new Date(Date.now() - 60 * 1000).toISOString();

function portableSummary(overrides: Partial<RclonePublicationSummary> = {}): RclonePublicationSummary {
  return {
    ...legacySummary,
    mode: "versioned_prefix",
    state: "preflight_required",
    reasonCode: "preflight_required",
    bindingRevision: "1",
    rollbackLocatorPresent: true,
    rollbackCapability: "clean_available",
    ...overrides,
  };
}

describe("TaskRcloneVersioningDialog", () => {
  beforeEach(() => {
    authRef.current = { token: "token", role: "admin" };
    for (const mock of Object.values(apiClientMock)) {
      mock.mockReset();
    }
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.clearAllMocks();
  });

  it("expires an idle preflight without another user interaction", async () => {
    vi.useFakeTimers({ toFake: ["Date", "setInterval", "clearInterval"] });
    const now = new Date("2026-09-17T00:00:00Z");
    vi.setSystemTime(now);
    apiClientMock.createRcloneVersioningPreflight.mockResolvedValue({
      purpose: "activation",
      preflightId: "expiry-test",
      expiresAt: new Date(now.getTime() + 1000).toISOString(),
      summary: portableSummary({ state: "ready", reasonCode: "ready" }),
    });
    render(<TaskRcloneVersioningDialog open onOpenChange={vi.fn()} task={{ ...task, rclonePublication: portableSummary() }} token="token" onUpdated={vi.fn()} />);
    await act(async () => { fireEvent.click(screen.getByRole("button", { name: "运行预检" })); });
    expect(screen.getByRole("button", { name: "启用版本化" })).toBeEnabled();
    act(() => { vi.advanceTimersByTime(1000); });
    expect(screen.getByRole("button", { name: "启用版本化" })).toBeDisabled();
    expect(apiClientMock.activateRcloneVersioning).not.toHaveBeenCalled();
  });

  it("rechecks preflight expiry at submission before the display clock ticks", async () => {
    vi.useFakeTimers({ toFake: ["Date", "setInterval", "clearInterval"] });
    const now = new Date("2026-09-17T00:00:00Z");
    vi.setSystemTime(now);
    apiClientMock.createRcloneVersioningPreflight.mockResolvedValue({
      purpose: "activation",
      preflightId: "expiry-test",
      expiresAt: new Date(now.getTime() + 500).toISOString(),
      summary: portableSummary({ state: "ready", reasonCode: "ready" }),
    });
    render(<TaskRcloneVersioningDialog open onOpenChange={vi.fn()} task={{ ...task, rclonePublication: portableSummary() }} token="token" onUpdated={vi.fn()} />);
    await act(async () => { fireEvent.click(screen.getByRole("button", { name: "运行预检" })); });
    expect(screen.getByRole("button", { name: "启用版本化" })).toBeEnabled();
    vi.setSystemTime(now.getTime() + 600);
    fireEvent.click(screen.getByRole("button", { name: "启用版本化" }));
    expect(apiClientMock.activateRcloneVersioning).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: "启用版本化" })).toBeDisabled();
  });

  it("completes portable binding, preflight, and first-new activation with exact revisions", async () => {
    const user = userEvent.setup();
    const onUpdated = vi.fn().mockResolvedValue(undefined);
    apiClientMock.createRclonePortableBindingSetup.mockResolvedValue({
      setupId: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
      expiresAt: futureExpiry(),
      baseBindingRevision: "0",
    });
    apiClientMock.setRclonePortableBinding.mockResolvedValue(portableSummary());
    apiClientMock.createRcloneVersioningPreflight.mockResolvedValue({
      purpose: "activation",
      preflightId: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
      expiresAt: futureExpiry(),
      summary: portableSummary({
        state: "ready",
        reasonCode: "ready",
        capabilityRevision: "2",
        consistencyClass: "observationally_stable",
        hashFidelity: "download_verified_bytes",
        estimatedReadBytes: "4096",
        apiCostClass: "moderate",
        storageCostClass: "low",
        egressCostClass: "high",
      }),
    });
    apiClientMock.activateRcloneVersioning.mockResolvedValue({
      migrationChoice: "first_new_point",
      summary: portableSummary({ state: "ready", reasonCode: "ready", taskRevision: "9007199254740994", capabilityRevision: "2" }),
    });

    render(
      <TaskRcloneVersioningDialog open onOpenChange={vi.fn()} task={task} token="token" onUpdated={onUpdated} />,
    );

    expect(screen.getByRole("radio", { name: "可移植前缀" })).toBeChecked();
    await user.type(screen.getByLabelText("Remote 名称"), "archive");
    await user.type(screen.getByLabelText("受管根目录"), "archive:managed/v1");
    fireEvent.change(screen.getByLabelText("Rclone 配置"), {
      target: { value: "[archive]\ntype = s3\nsecret = FAKE_SECRET_FOR_TEST_ONLY" },
    });
    await user.click(screen.getByRole("button", { name: "保存 Portable 绑定" }));

    await waitFor(() => {
      expect(apiClientMock.setRclonePortableBinding).toHaveBeenCalledWith("token", 17, {
        expectedTaskRevision: "9007199254740993",
        expectedBindingRevision: "0",
        setupId: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
        targetRemote: "archive",
        managedRootLocator: "archive:managed/v1",
        boundConfig: "[archive]\ntype = s3\nsecret = FAKE_SECRET_FOR_TEST_ONLY",
      });
    });
    expect(screen.getByLabelText("Rclone 配置")).toHaveValue("");

    await user.click(screen.getByRole("button", { name: "运行预检" }));
    await waitFor(() => {
      expect(apiClientMock.createRcloneVersioningPreflight).toHaveBeenCalledWith("token", 17, {
        expectedTaskRevision: "9007199254740993",
        requestedMode: "versioned_prefix",
      });
    });
    expect(screen.getByRole("radio", { name: "从下一次成功运行开始" })).toBeChecked();
    await user.click(screen.getByRole("radio", { name: "导入当前基线" }));
    await user.click(screen.getByRole("checkbox"));
    await user.click(screen.getByRole("radio", { name: "从下一次成功运行开始" }));
    expect(screen.getByRole("button", { name: "启用版本化" })).toBeEnabled();
    await user.click(screen.getByRole("button", { name: "启用版本化" }));
    await waitFor(() => {
      expect(apiClientMock.activateRcloneVersioning).toHaveBeenCalledWith("token", 17, {
        expectedTaskRevision: "9007199254740993",
        preflightId: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
        migrationChoice: "first_new_point",
      });
    });
    expect(onUpdated).toHaveBeenCalled();
  });

  it("keeps activation disabled after the preflight expires", async () => {
    const user = userEvent.setup();
    apiClientMock.createRcloneVersioningPreflight.mockResolvedValue({
      purpose: "activation",
      preflightId: "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
      expiresAt: pastExpiry(),
      summary: portableSummary({ state: "ready", reasonCode: "ready", capabilityRevision: "2" }),
    });

    render(
      <TaskRcloneVersioningDialog
        open
        onOpenChange={vi.fn()}
        task={{ ...task, rclonePublication: portableSummary() }}
        token="token"
        onUpdated={vi.fn()}
      />,
    );

    await user.click(screen.getByRole("button", { name: "运行预检" }));
    await waitFor(() => {
      expect(apiClientMock.createRcloneVersioningPreflight).toHaveBeenCalled();
    });
    expect(screen.getByRole("button", { name: "启用版本化" })).toBeDisabled();
    expect(apiClientMock.activateRcloneVersioning).not.toHaveBeenCalled();
  });

  it("shows native external ID and clears every write-only credential after a failed binding", async () => {
    const user = userEvent.setup();
    apiClientMock.createRcloneNativeBindingSetup.mockResolvedValue({
      setupId: "cccccccccccccccccccccccccccccccc",
      expiresAt: futureExpiry(),
      externalId: "xirang-dddddddddddddddddddddddddddddddd",
      baseBindingRevision: "0",
    });
    apiClientMock.setRcloneNativeBinding.mockRejectedValue(new Error("private provider failure"));

    render(
      <TaskRcloneVersioningDialog open onOpenChange={vi.fn()} task={task} token="token" onUpdated={vi.fn()} />,
    );

    await user.click(screen.getByRole("radio", { name: "AWS S3 原生版本" }));
    await user.click(screen.getByRole("button", { name: "生成 External ID" }));
    expect(await screen.findByDisplayValue("xirang-dddddddddddddddddddddddddddddddd")).toHaveAttribute("readonly");

    await user.type(screen.getByLabelText("区域"), "us-east-1");
    await user.type(screen.getByLabelText("存储桶"), "private-bucket");
    await user.type(screen.getByLabelText("受管前缀"), "managed/v1/");
    await user.type(screen.getByLabelText("角色 ARN"), "arn:aws:iam::123456789012:role/xirang-rclone");
    await user.selectOptions(screen.getByLabelText("凭据来源"), "static_sts_bootstrap");
    await user.type(screen.getByLabelText("访问密钥 ID"), "FAKE_ACCESS_KEY_FOR_TEST_ONLY");
    await user.type(screen.getByLabelText("访问密钥 Secret"), "FAKE_SECRET_KEY_FOR_TEST_ONLY");
    await user.selectOptions(screen.getByLabelText("加密方式"), "sse_kms_cmk");
    await user.type(screen.getByLabelText("KMS 密钥 ARN"), "arn:aws:kms:us-east-1:123456789012:key/FAKE-KMS-FOR-TEST-ONLY");
    await user.click(screen.getByRole("button", { name: "保存 Native 绑定" }));

    await waitFor(() => expect(apiClientMock.setRcloneNativeBinding).toHaveBeenCalledTimes(1));
    expect(screen.getByLabelText("访问密钥 ID")).toHaveValue("");
    expect(screen.getByLabelText("访问密钥 Secret")).toHaveValue("");
    expect(screen.getByLabelText("KMS 密钥 ARN")).toHaveValue("");
    expect(document.body).not.toHaveTextContent("private provider failure");
  });

  it("uses the newest summary revisions for clean rollback", async () => {
    const user = userEvent.setup();
    const managedTask: TaskRecord = {
      ...task,
      rclonePublication: portableSummary({
        state: "committed",
        reasonCode: "ready",
        taskRevision: "9007199254740998",
        bindingRevision: "9",
        rollbackCapability: "clean_available",
      }),
    };
    apiClientMock.cleanRollbackRcloneVersioning.mockResolvedValue({ summary: legacySummary });

    render(
      <TaskRcloneVersioningDialog open onOpenChange={vi.fn()} task={managedTask} token="token" onUpdated={vi.fn()} />,
    );
    await user.click(screen.getByRole("button", { name: "清理回退" }));
    await waitFor(() => {
      expect(apiClientMock.cleanRollbackRcloneVersioning).toHaveBeenCalledWith("token", 17, {
        expectedTaskRevision: "9007199254740998",
        expectedBindingRevision: "9",
      });
    });
  });

  it("keeps the portaled blocked state accessible", async () => {
    render(
      <TaskRcloneVersioningDialog
        open
        onOpenChange={vi.fn()}
        task={{ ...task, rclonePublication: { ...legacySummary, state: "blocked", reasonCode: "unsupported_profile" } }}
        token="token"
        onUpdated={vi.fn()}
      />,
    );

    expect(screen.getByText("当前配置已被安全阻止")).toBeInTheDocument();
    await expect(runAxe(document.body)).resolves.toHaveNoViolations();
  });

  it("moves focus with arrow keys and exposes native credential and rollback facts", async () => {
    const user = userEvent.setup();
    const nativeTask: TaskRecord = {
      ...task,
      rclonePublication: portableSummary({
        mode: "native_object_versions",
        state: "ready",
        reasonCode: "ready",
        credentialExpiresAt: "2026-07-17T10:00:00Z",
        encryptionProfile: "sse_kms_cmk",
        kmsKeyStatus: "ready",
        kmsReadKeyCount: 2,
        rollbackLocatorPresent: true,
      }),
    };

    const { rerender } = render(
      <TaskRcloneVersioningDialog open onOpenChange={vi.fn()} task={task} token="token" onUpdated={vi.fn()} />,
    );
    const portable = screen.getByRole("radio", { name: "可移植前缀" });
    portable.focus();
    await user.keyboard("{ArrowRight}");
    expect(screen.getByRole("radio", { name: "AWS S3 原生版本" })).toHaveFocus();

    rerender(
      <TaskRcloneVersioningDialog open onOpenChange={vi.fn()} task={nativeTask} token="token" onUpdated={vi.fn()} />,
    );
    expect(screen.getByText("凭据有效期")).toBeInTheDocument();
    expect(screen.getByText("KMS 状态")).toBeInTheDocument();
    expect(screen.getByText("历史读取密钥")).toBeInTheDocument();
    expect(screen.getByText("2 个")).toBeInTheDocument();
    expect(screen.getByText("回退定位器")).toBeInTheDocument();
    expect(screen.getByText("已保留")).toBeInTheDocument();
    expect(screen.getByText("安全原因")).toBeInTheDocument();
    expect(screen.getByText("绑定、能力和验证条件已就绪。")).toBeInTheDocument();
  });

  function createDeferred<T>() {
    let resolve!: (value: T) => void;
    let reject!: (error: unknown) => void;
    const promise = new Promise<T>((res, rej) => {
      resolve = res;
      reject = rej;
    });
    return { promise, resolve, reject };
  }

  function dialogElement(open: boolean, token: string, onUpdated: () => void) {
    return (
      <TaskRcloneVersioningDialog open={open} onOpenChange={vi.fn()} task={task} token={token} onUpdated={onUpdated} />
    );
  }

  async function startPortableSetup() {
    const setup = createDeferred<RcloneBindingSetupResult>();
    const calls = apiClientMock.createRclonePortableBindingSetup.mock.calls.length;
    apiClientMock.createRclonePortableBindingSetup.mockReturnValueOnce(setup.promise);
    fireEvent.change(screen.getByLabelText("Remote 名称"), { target: { value: "archive" } });
    fireEvent.change(screen.getByLabelText("受管根目录"), { target: { value: "archive:managed/v1" } });
    fireEvent.change(screen.getByLabelText("Rclone 配置"), { target: { value: "[archive]\ntype = s3\nsecret = late" } });
    fireEvent.click(screen.getByRole("button", { name: "保存 Portable 绑定" }));
    await waitFor(() => expect(apiClientMock.createRclonePortableBindingSetup).toHaveBeenCalledTimes(calls + 1));
    return setup;
  }

  it("completes portable binding after StrictMode replay for an admin", async () => {
    apiClientMock.createRclonePortableBindingSetup.mockResolvedValue({
      setupId: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
      expiresAt: futureExpiry(),
      baseBindingRevision: "0",
    });
    apiClientMock.setRclonePortableBinding.mockResolvedValue(portableSummary());
    const onUpdated = vi.fn().mockResolvedValue(undefined);
    render(<StrictMode>{dialogElement(true, "token", onUpdated)}</StrictMode>);
    fireEvent.change(screen.getByLabelText("Remote 名称"), { target: { value: "archive" } });
    fireEvent.change(screen.getByLabelText("受管根目录"), { target: { value: "archive:managed/v1" } });
    fireEvent.change(screen.getByLabelText("Rclone 配置"), { target: { value: "[archive]\ntype = s3\n" } });
    fireEvent.click(screen.getByRole("button", { name: "保存 Portable 绑定" }));
    await waitFor(() => expect(apiClientMock.setRclonePortableBinding).toHaveBeenCalledTimes(1));
    expect(onUpdated).toHaveBeenCalledTimes(1);
  });

  it("does not start portable setup when the committed role is not admin", () => {
    authRef.current.role = "operator";
    render(dialogElement(true, "token", vi.fn()));
    fireEvent.change(screen.getByLabelText("Remote 名称"), { target: { value: "archive" } });
    fireEvent.change(screen.getByLabelText("受管根目录"), { target: { value: "archive:managed/v1" } });
    fireEvent.change(screen.getByLabelText("Rclone 配置"), { target: { value: "[archive]\ntype = s3\n" } });
    fireEvent.click(screen.getByRole("button", { name: "保存 Portable 绑定" }));
    expect(apiClientMock.createRclonePortableBindingSetup).not.toHaveBeenCalled();
  });

  it("stops before portable secret binding when token and role return", async () => {
    const onUpdated = vi.fn().mockResolvedValue(undefined);
    const view = render(dialogElement(true, "token", onUpdated));
    const setup = await startPortableSetup();

    authRef.current = { token: "token-b", role: "viewer" };
    view.rerender(dialogElement(true, "token-b", onUpdated));
    authRef.current = { token: "token", role: "admin" };
    view.rerender(dialogElement(true, "token", onUpdated));
    await act(async () => {
      setup.resolve({ setupId: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", expiresAt: futureExpiry(), baseBindingRevision: "0" });
    });

    expect(apiClientMock.setRclonePortableBinding).not.toHaveBeenCalled();
    expect(onUpdated).not.toHaveBeenCalled();
    expect(screen.queryByText("版本化操作未完成，请刷新后重试。")).not.toBeInTheDocument();
  });

  it("does not publish portable binding after close, reopen, or unmount", async () => {
    const onUpdated = vi.fn().mockResolvedValue(undefined);
    const view = render(<StrictMode>{dialogElement(true, "token", onUpdated)}</StrictMode>);
    const setup = await startPortableSetup();
    view.rerender(<StrictMode>{dialogElement(false, "token", onUpdated)}</StrictMode>);
    view.rerender(<StrictMode>{dialogElement(true, "token", onUpdated)}</StrictMode>);
    await act(async () => {
      setup.resolve({ setupId: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", expiresAt: futureExpiry(), baseBindingRevision: "0" });
    });
    expect(apiClientMock.setRclonePortableBinding).not.toHaveBeenCalled();
    expect(onUpdated).not.toHaveBeenCalled();

    const rejected = await startPortableSetup();
    view.unmount();
    await act(async () => {
      rejected.reject(new Error("unmounted"));
    });
    expect(apiClientMock.setRclonePortableBinding).not.toHaveBeenCalled();
    expect(onUpdated).not.toHaveBeenCalled();
  });

  const expiredRenewalTask: TaskRecord = {
    ...task,
    rclonePublication: portableSummary({
      state: "preflight_required",
      reasonCode: "preflight_expired",
      taskRevision: "9007199254741000",
      bindingRevision: "4",
      capabilityRevision: "3",
    }),
  };

  function renewalPreflight(overrides: Partial<RcloneVersioningPreflightResult> = {}): RcloneVersioningPreflightResult {
    return {
      purpose: "renewal",
      preflightId: "ffffffffffffffffffffffffffffffff",
      expiresAt: futureExpiry(),
      summary: portableSummary({
        state: "ready",
        reasonCode: "ready",
        taskRevision: "9007199254741000",
        bindingRevision: "4",
        capabilityRevision: "3",
      }),
      ...overrides,
    };
  }

  function renewalDialog({
    token = "token",
    taskRecord = expiredRenewalTask,
    onUpdated = vi.fn(),
  }: { token?: string; taskRecord?: TaskRecord; onUpdated?: () => void } = {}) {
    return (
      <TaskRcloneVersioningDialog open onOpenChange={vi.fn()} task={taskRecord} token={token} onUpdated={onUpdated} />
    );
  }

  function expectNoMigrationChoices() {
    expect(screen.queryByRole("radio", { name: "从下一次成功运行开始" })).not.toBeInTheDocument();
    expect(screen.queryByRole("radio", { name: "导入当前基线" })).not.toBeInTheDocument();
    expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
  }

  it("renews an expired persisted binding with a fresh ready renewal ticket and a fixed first-new policy", async () => {
    const user = userEvent.setup();
    const onUpdated = vi.fn().mockResolvedValue(undefined);
    apiClientMock.createRcloneVersioningPreflight.mockResolvedValue(renewalPreflight());
    apiClientMock.activateRcloneVersioning.mockResolvedValue({
      migrationChoice: "first_new_point",
      summary: portableSummary({
        state: "ready",
        reasonCode: "ready",
        taskRevision: "9007199254741001",
        bindingRevision: "4",
        capabilityRevision: "3",
      }),
    });
    render(renewalDialog({ onUpdated }));

    expect(screen.getByRole("button", { name: "启用版本化" })).toBeDisabled();
    await user.click(screen.getByRole("button", { name: "运行预检" }));
    await waitFor(() => {
      expect(apiClientMock.createRcloneVersioningPreflight).toHaveBeenCalledWith("token", 17, {
        expectedTaskRevision: "9007199254741000",
        requestedMode: "versioned_prefix",
      });
    });

    const confirm = await screen.findByRole("button", { name: "确认续期" });
    expect(confirm).toBeEnabled();
    expect(screen.queryByRole("button", { name: "启用版本化" })).not.toBeInTheDocument();
    expect(screen.getByText("待确认续期，当前授权尚未延长")).toBeInTheDocument();
    expectNoMigrationChoices();

    await user.click(confirm);
    await waitFor(() => {
      expect(apiClientMock.activateRcloneVersioning).toHaveBeenCalledWith("token", 17, {
        expectedTaskRevision: "9007199254741000",
        preflightId: "ffffffffffffffffffffffffffffffff",
        migrationChoice: "first_new_point",
      });
    });
    expect(apiClientMock.activateRcloneVersioning).toHaveBeenCalledTimes(1);
    await waitFor(() => expect(onUpdated).toHaveBeenCalledTimes(1));
    expect(screen.queryByRole("button", { name: "确认续期" })).not.toBeInTheDocument();
    expect(screen.queryByText("待确认续期，当前授权尚未延长")).not.toBeInTheDocument();
  });

  it("submits first_new_point for renewal even after an earlier activation ticket chose an imported baseline", async () => {
    const user = userEvent.setup();
    apiClientMock.createRcloneVersioningPreflight
      .mockResolvedValueOnce({ ...renewalPreflight(), purpose: "activation", preflightId: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" })
      .mockResolvedValueOnce(renewalPreflight());
    apiClientMock.activateRcloneVersioning.mockResolvedValue({
      migrationChoice: "first_new_point",
      summary: portableSummary({ state: "ready", reasonCode: "ready", taskRevision: "9007199254741001", bindingRevision: "4" }),
    });
    render(renewalDialog({ onUpdated: vi.fn().mockResolvedValue(undefined) }));

    await user.click(screen.getByRole("button", { name: "运行预检" }));
    await user.click(await screen.findByRole("radio", { name: "导入当前基线" }));
    await user.click(screen.getByRole("checkbox"));
    await user.click(screen.getByRole("button", { name: "运行预检" }));

    const confirm = await screen.findByRole("button", { name: "确认续期" });
    expectNoMigrationChoices();
    await user.click(confirm);
    await waitFor(() => {
      expect(apiClientMock.activateRcloneVersioning).toHaveBeenCalledWith("token", 17, {
        expectedTaskRevision: "9007199254741000",
        preflightId: "ffffffffffffffffffffffffffffffff",
        migrationChoice: "first_new_point",
      });
    });
  });

  it.each([
    ["a settling renewal ticket", () => renewalPreflight({
      summary: portableSummary({
        state: "capability_settling",
        reasonCode: "capability_settling",
        taskRevision: "9007199254741000",
        bindingRevision: "4",
      }),
    })],
    ["an expired renewal ticket", () => renewalPreflight({ expiresAt: pastExpiry() })],
  ])("blocks confirmation for %s", async (_label, result) => {
    const user = userEvent.setup();
    apiClientMock.createRcloneVersioningPreflight.mockResolvedValue(result());
    render(renewalDialog());

    await user.click(screen.getByRole("button", { name: "运行预检" }));
    await waitFor(() => expect(apiClientMock.createRcloneVersioningPreflight).toHaveBeenCalledTimes(1));
    const confirm = await screen.findByRole("button", { name: "确认续期" });
    expect(confirm).toBeDisabled();
    expectNoMigrationChoices();
    fireEvent.click(confirm);
    expect(apiClientMock.activateRcloneVersioning).not.toHaveBeenCalled();
  });

  it("announces a settling preflight as status and a failed preflight as an alert", async () => {
    const user = userEvent.setup();
    apiClientMock.createRcloneVersioningPreflight
      .mockResolvedValueOnce(renewalPreflight({
        summary: portableSummary({
          state: "capability_settling",
          reasonCode: "capability_settling",
          taskRevision: "9007199254741000",
          bindingRevision: "4",
        }),
      }))
      .mockRejectedValueOnce(new Error("private provider failure"));
    render(renewalDialog());

    await user.click(screen.getByRole("button", { name: "运行预检" }));
    await screen.findByRole("button", { name: "确认续期" });
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    const reason = screen.getByText("安全原因").nextElementSibling?.textContent ?? "";
    expect(reason).not.toBe("");
    expect(screen.getAllByRole("status").some((element) => element.textContent?.includes(reason))).toBe(true);

    await user.click(screen.getByRole("button", { name: "运行预检" }));
    expect(await screen.findByRole("alert")).toBeInTheDocument();
    expect(document.body).not.toHaveTextContent("private provider failure");
  });

  it.each([
    ["an unknown purpose mapped by the API boundary", () => mapRclonePreflightResult({
      purpose: "rotation",
      preflight_id: "dddddddddddddddddddddddddddddddd",
      expires_at: futureExpiry(),
      summary: {
        mode: "versioned_prefix",
        state: "ready",
        reason_code: "ready",
        task_revision: "9007199254741000",
        binding_revision: "4",
      },
    })],
    ["a ready ticket without a recognised purpose", (): RcloneVersioningPreflightResult => ({
      ...renewalPreflight(),
      purpose: null,
      preflightId: "dddddddddddddddddddddddddddddddd",
    })],
  ])("never offers activation or renewal for %s", async (_label, result) => {
    const user = userEvent.setup();
    apiClientMock.createRcloneVersioningPreflight.mockResolvedValue(result());
    render(renewalDialog());

    await user.click(screen.getByRole("button", { name: "运行预检" }));
    await waitFor(() => expect(apiClientMock.createRcloneVersioningPreflight).toHaveBeenCalledTimes(1));
    const activateButton = await screen.findByRole("button", { name: "启用版本化" });
    expect(activateButton).toBeDisabled();
    expect(screen.queryByRole("button", { name: "确认续期" })).not.toBeInTheDocument();
    expectNoMigrationChoices();
    fireEvent.click(activateButton);
    expect(apiClientMock.activateRcloneVersioning).not.toHaveBeenCalled();
  });

  type IdentityChange = "token" | "role" | "task";

  function changeIdentity(
    view: RenderResult,
    change: IdentityChange,
    onUpdated: () => void,
    taskRecord: TaskRecord = expiredRenewalTask,
  ) {
    if (change === "token") {
      authRef.current = { token: "token-b", role: "admin" };
      view.rerender(renewalDialog({ token: "token-b", onUpdated, taskRecord }));
    } else if (change === "role") {
      authRef.current = { token: "token", role: "viewer" };
      view.rerender(renewalDialog({ onUpdated, taskRecord }));
    } else {
      view.rerender(renewalDialog({ onUpdated, taskRecord: { ...taskRecord, id: 18, name: "other-rclone" } }));
    }
  }

  it.each<IdentityChange>(["token", "role", "task"])("discards a late renewal preflight after a %s change", async (change) => {
    const onUpdated = vi.fn().mockResolvedValue(undefined);
    const pending = createDeferred<RcloneVersioningPreflightResult>();
    apiClientMock.createRcloneVersioningPreflight.mockReturnValueOnce(pending.promise);
    const view = render(renewalDialog({ onUpdated }));

    fireEvent.click(screen.getByRole("button", { name: "运行预检" }));
    await waitFor(() => expect(apiClientMock.createRcloneVersioningPreflight).toHaveBeenCalledTimes(1));
    changeIdentity(view, change, onUpdated);
    await act(async () => {
      pending.resolve(renewalPreflight());
    });

    expect(screen.queryByRole("button", { name: "确认续期" })).not.toBeInTheDocument();
    expect(screen.queryByText("待确认续期，当前授权尚未延长")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "启用版本化" })).toBeDisabled();
    expect(apiClientMock.activateRcloneVersioning).not.toHaveBeenCalled();
    expect(onUpdated).not.toHaveBeenCalled();
  });

  it.each<IdentityChange>(["token", "role", "task"])("discards a late renewal confirmation after a %s change", async (change) => {
    const onUpdated = vi.fn().mockResolvedValue(undefined);
    const pending = createDeferred<{ migrationChoice: "first_new_point"; summary: RclonePublicationSummary }>();
    apiClientMock.createRcloneVersioningPreflight.mockResolvedValue(renewalPreflight());
    apiClientMock.activateRcloneVersioning.mockReturnValueOnce(pending.promise);
    const view = render(renewalDialog({ onUpdated }));

    fireEvent.click(screen.getByRole("button", { name: "运行预检" }));
    fireEvent.click(await screen.findByRole("button", { name: "确认续期" }));
    await waitFor(() => expect(apiClientMock.activateRcloneVersioning).toHaveBeenCalledTimes(1));
    changeIdentity(view, change, onUpdated);
    await act(async () => {
      pending.resolve({
        migrationChoice: "first_new_point",
        summary: portableSummary({ state: "ready", reasonCode: "ready", taskRevision: "9007199254741001", bindingRevision: "4" }),
      });
    });

    expect(onUpdated).not.toHaveBeenCalled();
    expect(screen.queryByRole("button", { name: "确认续期" })).not.toBeInTheDocument();
    expect(screen.queryByText("版本化操作未完成，请刷新后重试。")).not.toBeInTheDocument();
  });

  it("does not surface a late renewal confirmation failure after the token changes", async () => {
    const onUpdated = vi.fn().mockResolvedValue(undefined);
    const pending = createDeferred<never>();
    apiClientMock.createRcloneVersioningPreflight.mockResolvedValue(renewalPreflight());
    apiClientMock.activateRcloneVersioning.mockReturnValueOnce(pending.promise);
    const view = render(renewalDialog({ onUpdated }));

    fireEvent.click(screen.getByRole("button", { name: "运行预检" }));
    fireEvent.click(await screen.findByRole("button", { name: "确认续期" }));
    await waitFor(() => expect(apiClientMock.activateRcloneVersioning).toHaveBeenCalledTimes(1));
    changeIdentity(view, "token", onUpdated);
    await act(async () => {
      pending.reject(new Error("late renewal failure"));
    });

    expect(onUpdated).not.toHaveBeenCalled();
    expect(screen.queryByText("版本化操作未完成，请刷新后重试。")).not.toBeInTheDocument();
    expect(document.body).not.toHaveTextContent("late renewal failure");
  });

  it("keeps the pending renewal confirmation state accessible", async () => {
    const user = userEvent.setup();
    apiClientMock.createRcloneVersioningPreflight.mockResolvedValue(renewalPreflight());
    render(renewalDialog());

    await user.click(screen.getByRole("button", { name: "运行预检" }));
    expect(await screen.findByRole("button", { name: "确认续期" })).toBeEnabled();
    await expect(runAxe(document.body)).resolves.toHaveNoViolations();
  });

  const admissionBlockedText = "受管发布准入当前被安全阻止。";
  const portableSetupId = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa";
  const nativeSetupId = "cccccccccccccccccccccccccccccccc";
  const nativeExternalId = "xirang-dddddddddddddddddddddddddddddddd";
  const replacementConfig = "[archive]\ntype = s3\nsecret = FAKE_REPLACEMENT_SECRET_FOR_TEST_ONLY";

  const portableCandidateTask: TaskRecord = {
    ...task,
    rclonePublication: portableSummary({ taskRevision: "9007199254741010", bindingRevision: "1" }),
  };
  const nativeCandidateSummary = portableSummary({
    mode: "native_object_versions",
    taskRevision: "9007199254741020",
    bindingRevision: "1",
  });
  const nativeCandidateTask: TaskRecord = { ...task, rclonePublication: nativeCandidateSummary };

  function fillPortableFields() {
    fireEvent.change(screen.getByLabelText("Remote 名称"), { target: { value: "archive" } });
    fireEvent.change(screen.getByLabelText("受管根目录"), { target: { value: "archive:managed/v2" } });
    fireEvent.change(screen.getByLabelText("Rclone 配置"), { target: { value: replacementConfig } });
  }

  function fillNativeFields() {
    fireEvent.change(screen.getByLabelText("区域"), { target: { value: "us-east-1" } });
    fireEvent.change(screen.getByLabelText("存储桶"), { target: { value: "private-bucket" } });
    fireEvent.change(screen.getByLabelText("受管前缀"), { target: { value: "managed/v2/" } });
    fireEvent.change(screen.getByLabelText("角色 ARN"), { target: { value: "arn:aws:iam::123456789012:role/xirang-rclone" } });
    fireEvent.change(screen.getByLabelText("凭据来源"), { target: { value: "static_sts_bootstrap" } });
    fireEvent.change(screen.getByLabelText("访问密钥 ID"), { target: { value: "FAKE_ACCESS_KEY_FOR_TEST_ONLY" } });
    fireEvent.change(screen.getByLabelText("访问密钥 Secret"), { target: { value: "FAKE_SECRET_KEY_FOR_TEST_ONLY" } });
  }

  function candidateDialog(taskRecord: TaskRecord, onUpdated: () => void | Promise<void> = vi.fn()) {
    return <TaskRcloneVersioningDialog open onOpenChange={vi.fn()} task={taskRecord} token="token" onUpdated={onUpdated} />;
  }

  it("replaces a portable candidate with the setup base revision instead of the candidate summary revision", async () => {
    const onUpdated = vi.fn().mockResolvedValue(undefined);
    apiClientMock.createRclonePortableBindingSetup.mockResolvedValue({
      setupId: portableSetupId,
      expiresAt: futureExpiry(),
      baseBindingRevision: "0",
    });
    apiClientMock.setRclonePortableBinding.mockResolvedValue(
      portableSummary({ taskRevision: "9007199254741010", bindingRevision: "1" }),
    );
    render(candidateDialog(portableCandidateTask, onUpdated));

    fillPortableFields();
    fireEvent.click(screen.getByRole("button", { name: "保存 Portable 绑定" }));

    await waitFor(() => {
      expect(apiClientMock.setRclonePortableBinding).toHaveBeenCalledWith("token", 17, {
        expectedTaskRevision: "9007199254741010",
        expectedBindingRevision: "0",
        setupId: portableSetupId,
        targetRemote: "archive",
        managedRootLocator: "archive:managed/v2",
        boundConfig: replacementConfig,
      });
    });
    expect(apiClientMock.createRclonePortableBindingSetup).toHaveBeenCalledWith("token", 17, {
      expectedTaskRevision: "9007199254741010",
    });
    await waitFor(() => expect(onUpdated).toHaveBeenCalledTimes(1));
    expect(apiClientMock.createRcloneVersioningPreflight).not.toHaveBeenCalled();
    await waitFor(() => expect(screen.getByRole("button", { name: "运行预检" })).toBeEnabled());
    expect(screen.getByLabelText("Rclone 配置")).toHaveValue("");
  });

  it("replaces a native candidate with the stored setup task revision and base revision", async () => {
    const onUpdated = vi.fn().mockResolvedValue(undefined);
    apiClientMock.createRcloneNativeBindingSetup.mockResolvedValue({
      setupId: nativeSetupId,
      expiresAt: futureExpiry(),
      externalId: nativeExternalId,
      baseBindingRevision: "0",
    });
    apiClientMock.setRcloneNativeBinding.mockResolvedValue(nativeCandidateSummary);
    render(candidateDialog(nativeCandidateTask, onUpdated));

    fireEvent.click(screen.getByRole("button", { name: "生成 External ID" }));
    expect(await screen.findByDisplayValue(nativeExternalId)).toHaveAttribute("readonly");
    expect(apiClientMock.createRcloneNativeBindingSetup).toHaveBeenCalledWith("token", 17, {
      expectedTaskRevision: "9007199254741020",
    });
    fillNativeFields();
    fireEvent.click(screen.getByRole("button", { name: "保存 Native 绑定" }));

    await waitFor(() => {
      expect(apiClientMock.setRcloneNativeBinding).toHaveBeenCalledWith("token", 17, {
        expectedTaskRevision: "9007199254741020",
        expectedBindingRevision: "0",
        setupId: nativeSetupId,
        region: "us-east-1",
        bucket: "private-bucket",
        managedPrefix: "managed/v2/",
        roleArn: "arn:aws:iam::123456789012:role/xirang-rclone",
        bootstrap: {
          mode: "static_sts_bootstrap",
          accessKeyId: "FAKE_ACCESS_KEY_FOR_TEST_ONLY",
          secretAccessKey: "FAKE_SECRET_KEY_FOR_TEST_ONLY",
        },
        encryptionProfile: "sse_s3",
        kmsKeyArn: undefined,
      });
    });
    await waitFor(() => expect(onUpdated).toHaveBeenCalledTimes(1));
    expect(screen.queryByDisplayValue(nativeExternalId)).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "保存 Native 绑定" })).toBeDisabled();
    expect(apiClientMock.createRcloneVersioningPreflight).not.toHaveBeenCalled();
  });

  const unusableSetups: Array<[string, () => RcloneBindingSetupResult]> = [
    ["an unusable base revision", () => ({
      setupId: portableSetupId, expiresAt: futureExpiry(), externalId: nativeExternalId, baseBindingRevision: null,
    })],
    ["an expired ticket", () => ({
      setupId: portableSetupId, expiresAt: pastExpiry(), externalId: nativeExternalId, baseBindingRevision: "0",
    })],
    ["a missing setup id", () => ({
      setupId: "", expiresAt: futureExpiry(), externalId: nativeExternalId, baseBindingRevision: "0",
    })],
  ];

  it.each(unusableSetups)("never sends the portable secret for %s", async (_label, setup) => {
    const onUpdated = vi.fn().mockResolvedValue(undefined);
    apiClientMock.createRclonePortableBindingSetup.mockResolvedValue(setup());
    render(candidateDialog(portableCandidateTask, onUpdated));

    fillPortableFields();
    fireEvent.click(screen.getByRole("button", { name: "保存 Portable 绑定" }));

    expect(await screen.findByText(admissionBlockedText)).toBeInTheDocument();
    expect(apiClientMock.setRclonePortableBinding).not.toHaveBeenCalled();
    expect(onUpdated).not.toHaveBeenCalled();
    expect(screen.getByLabelText("Rclone 配置")).toHaveValue("");
  });

  it.each(unusableSetups)("never offers native binding for %s", async (_label, setup) => {
    apiClientMock.createRcloneNativeBindingSetup.mockResolvedValue(setup());
    render(candidateDialog(nativeCandidateTask));

    fireEvent.click(screen.getByRole("button", { name: "生成 External ID" }));

    expect(await screen.findByText(admissionBlockedText)).toBeInTheDocument();
    expect(screen.queryByDisplayValue(nativeExternalId)).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "保存 Native 绑定" })).toBeDisabled();
    expect(apiClientMock.setRcloneNativeBinding).not.toHaveBeenCalled();
  });

  it("does not send native credentials once the stored setup ticket has expired", async () => {
    vi.useFakeTimers({ toFake: ["Date"] });
    const now = new Date("2026-09-17T00:00:00Z");
    vi.setSystemTime(now);
    apiClientMock.createRcloneNativeBindingSetup.mockResolvedValue({
      setupId: nativeSetupId,
      expiresAt: new Date(now.getTime() + 60_000).toISOString(),
      externalId: nativeExternalId,
      baseBindingRevision: "0",
    });
    render(candidateDialog(nativeCandidateTask));

    await act(async () => { fireEvent.click(screen.getByRole("button", { name: "生成 External ID" })); });
    expect(screen.getByDisplayValue(nativeExternalId)).toBeInTheDocument();
    fillNativeFields();
    vi.setSystemTime(now.getTime() + 60_001);
    fireEvent.click(screen.getByRole("button", { name: "保存 Native 绑定" }));

    expect(apiClientMock.setRcloneNativeBinding).not.toHaveBeenCalled();
    expect(screen.getByText(admissionBlockedText)).toBeInTheDocument();
    expect(screen.queryByDisplayValue(nativeExternalId)).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "保存 Native 绑定" })).toBeDisabled();
  });

  it("rejects a native setup after the dialog's Task CAS moves on", async () => {
    apiClientMock.createRcloneNativeBindingSetup.mockResolvedValue({
      setupId: nativeSetupId,
      expiresAt: futureExpiry(),
      externalId: nativeExternalId,
      baseBindingRevision: "0",
    });
    apiClientMock.createRcloneVersioningPreflight.mockResolvedValue({
      purpose: "activation",
      preflightId: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
      expiresAt: futureExpiry(),
      summary: { ...nativeCandidateSummary, state: "ready", reasonCode: "ready", taskRevision: "9007199254741021" },
    });
    render(candidateDialog(nativeCandidateTask));

    fireEvent.click(screen.getByRole("button", { name: "生成 External ID" }));
    await screen.findByDisplayValue(nativeExternalId);
    fireEvent.click(screen.getByRole("button", { name: "运行预检" }));
    await waitFor(() => expect(apiClientMock.createRcloneVersioningPreflight).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(screen.getByRole("button", { name: "运行预检" })).toBeEnabled());
    fillNativeFields();
    fireEvent.click(screen.getByRole("button", { name: "保存 Native 绑定" }));

    expect(await screen.findByText(admissionBlockedText)).toBeInTheDocument();
    expect(apiClientMock.setRcloneNativeBinding).not.toHaveBeenCalled();
    expect(screen.queryByDisplayValue(nativeExternalId)).not.toBeInTheDocument();
  });

  it("clears a native setup when the parent delivers a newer Task CAS", async () => {
    apiClientMock.createRcloneNativeBindingSetup.mockResolvedValue({
      setupId: nativeSetupId,
      expiresAt: futureExpiry(),
      externalId: nativeExternalId,
      baseBindingRevision: "0",
    });
    const view = render(candidateDialog(nativeCandidateTask));

    fireEvent.click(screen.getByRole("button", { name: "生成 External ID" }));
    await screen.findByDisplayValue(nativeExternalId);
    view.rerender(candidateDialog({
      ...nativeCandidateTask,
      rclonePublication: { ...nativeCandidateSummary, taskRevision: "9007199254741021" },
    }));

    expect(screen.queryByDisplayValue(nativeExternalId)).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "保存 Native 绑定" })).toBeDisabled();
    expect(apiClientMock.setRcloneNativeBinding).not.toHaveBeenCalled();
  });

  it.each<IdentityChange>(["token", "role", "task"])("stops a delayed portable setup before the secret after a %s change", async (change) => {
    const onUpdated = vi.fn().mockResolvedValue(undefined);
    const view = render(renewalDialog({ onUpdated }));
    const setup = await startPortableSetup();

    changeIdentity(view, change, onUpdated);
    await act(async () => {
      setup.resolve({ setupId: portableSetupId, expiresAt: futureExpiry(), baseBindingRevision: "4" });
    });

    expect(apiClientMock.setRclonePortableBinding).not.toHaveBeenCalled();
    expect(onUpdated).not.toHaveBeenCalled();
    expect(screen.queryByText(admissionBlockedText)).not.toBeInTheDocument();
  });

  it.each<IdentityChange>(["token", "role", "task"])("discards a delayed native setup after a %s change", async (change) => {
    const onUpdated = vi.fn().mockResolvedValue(undefined);
    const pending = createDeferred<RcloneBindingSetupResult>();
    apiClientMock.createRcloneNativeBindingSetup.mockReturnValueOnce(pending.promise);
    const view = render(renewalDialog({ onUpdated, taskRecord: nativeCandidateTask }));

    fireEvent.click(screen.getByRole("button", { name: "生成 External ID" }));
    await waitFor(() => expect(apiClientMock.createRcloneNativeBindingSetup).toHaveBeenCalledTimes(1));
    changeIdentity(view, change, onUpdated, nativeCandidateTask);
    await act(async () => {
      pending.resolve({ setupId: nativeSetupId, expiresAt: futureExpiry(), externalId: nativeExternalId, baseBindingRevision: "0" });
    });

    expect(screen.queryByDisplayValue(nativeExternalId)).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "保存 Native 绑定" })).toBeDisabled();
    expect(apiClientMock.setRcloneNativeBinding).not.toHaveBeenCalled();
  });

  it("shows a safe conflict when a candidate replacement is rejected", async () => {
    const onUpdated = vi.fn().mockResolvedValue(undefined);
    apiClientMock.createRclonePortableBindingSetup.mockResolvedValue({
      setupId: portableSetupId,
      expiresAt: futureExpiry(),
      baseBindingRevision: "0",
    });
    apiClientMock.setRclonePortableBinding.mockRejectedValue(
      new ApiError(409, "private candidate conflict FAKE_PROVIDER_DETAIL", { data: { detail: "private candidate conflict" } }),
    );
    render(candidateDialog(portableCandidateTask, onUpdated));

    fillPortableFields();
    fireEvent.click(screen.getByRole("button", { name: "保存 Portable 绑定" }));

    expect(await screen.findByRole("alert")).toHaveTextContent("任务、绑定或预检已变化，请刷新后重试。");
    expect(document.body).not.toHaveTextContent("private candidate conflict");
    expect(document.body).not.toHaveTextContent("FAKE_REPLACEMENT_SECRET_FOR_TEST_ONLY");
    expect(onUpdated).not.toHaveBeenCalled();
    expect(apiClientMock.createRcloneVersioningPreflight).not.toHaveBeenCalled();
    expect(screen.getByLabelText("Rclone 配置")).toHaveValue("");
  });

  it("keeps the first-new activation summary and CAS through a pending and stale parent refresh", async () => {
    const user = userEvent.setup();
    const refresh = createDeferred<void>();
    const onUpdated = vi.fn(() => refresh.promise);
    const before = portableSummary({ taskRevision: "9007199254741030", bindingRevision: "4", rollbackCapability: "preparation_only" });
    const activated = portableSummary({
      state: "ready",
      reasonCode: "ready",
      taskRevision: "9007199254741032",
      bindingRevision: "4",
      rollbackCapability: "clean_available",
    });
    const staleIntermediate = portableSummary({ taskRevision: "9007199254741031", bindingRevision: "4", rollbackCapability: "preparation_only" });
    apiClientMock.createRcloneVersioningPreflight
      .mockResolvedValueOnce({
        purpose: "activation",
        preflightId: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
        expiresAt: futureExpiry(),
        summary: { ...before, state: "ready", reasonCode: "ready" },
      })
      .mockResolvedValueOnce({
        purpose: "activation",
        preflightId: "cccccccccccccccccccccccccccccccc",
        expiresAt: futureExpiry(),
        summary: activated,
      });
    apiClientMock.activateRcloneVersioning.mockResolvedValue({
      migrationChoice: "first_new_point",
      summary: activated,
    } satisfies RcloneVersioningActivationResult);
    apiClientMock.cleanRollbackRcloneVersioning.mockResolvedValue({ summary: legacySummary });
    const view = render(candidateDialog({ ...task, rclonePublication: before }, onUpdated));

    expect(screen.getByRole("button", { name: "准备回退" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "运行预检" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "启用版本化" })).toBeEnabled());
    await user.click(screen.getByRole("button", { name: "启用版本化" }));
    await waitFor(() => expect(onUpdated).toHaveBeenCalledTimes(1));
    expect(apiClientMock.activateRcloneVersioning).toHaveBeenCalledWith("token", 17, {
      expectedTaskRevision: "9007199254741030",
      preflightId: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
      migrationChoice: "first_new_point",
    });

    expect(screen.getByRole("button", { name: "清理回退" })).toBeDisabled();
    expect(screen.queryByRole("button", { name: "准备回退" })).not.toBeInTheDocument();
    view.rerender(candidateDialog({ ...task, rclonePublication: staleIntermediate }, onUpdated));
    expect(screen.getByRole("button", { name: "清理回退" })).toBeInTheDocument();

    await act(async () => { refresh.resolve(); });
    expect(screen.getByRole("button", { name: "清理回退" })).toBeEnabled();
    await user.click(screen.getByRole("button", { name: "运行预检" }));
    await waitFor(() => {
      expect(apiClientMock.createRcloneVersioningPreflight).toHaveBeenLastCalledWith("token", 17, {
        expectedTaskRevision: "9007199254741032",
        requestedMode: "versioned_prefix",
      });
    });

    view.rerender(candidateDialog({ ...task, rclonePublication: activated }, onUpdated));
    await user.click(screen.getByRole("button", { name: "清理回退" }));
    await waitFor(() => {
      expect(apiClientMock.cleanRollbackRcloneVersioning).toHaveBeenCalledWith("token", 17, {
        expectedTaskRevision: "9007199254741032",
        expectedBindingRevision: "4",
      });
    });
  });

  it("keeps an accepted later parent CAS when an older parent summary arrives after it", async () => {
    const user = userEvent.setup();
    const onUpdated = vi.fn().mockResolvedValue(undefined);
    const before = portableSummary({ taskRevision: "9007199254741050", bindingRevision: "4", rollbackCapability: "preparation_only" });
    const returned = portableSummary({
      state: "ready", reasonCode: "ready", taskRevision: "9007199254741052", bindingRevision: "4", rollbackCapability: "clean_available",
    });
    const acceptedParent = portableSummary({
      state: "committed", reasonCode: "ready", taskRevision: "9007199254741053", bindingRevision: "4", rollbackCapability: "clean_available",
    });
    const staleParent = portableSummary({ taskRevision: "9007199254741051", bindingRevision: "4", rollbackCapability: "preparation_only" });
    apiClientMock.createRcloneVersioningPreflight
      .mockResolvedValueOnce({
        purpose: "activation",
        preflightId: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
        expiresAt: futureExpiry(),
        summary: { ...before, state: "ready", reasonCode: "ready" },
      })
      .mockResolvedValueOnce({
        purpose: "activation",
        preflightId: "cccccccccccccccccccccccccccccccc",
        expiresAt: futureExpiry(),
        summary: { ...acceptedParent, state: "ready" },
      });
    apiClientMock.activateRcloneVersioning.mockResolvedValue({
      migrationChoice: "first_new_point",
      summary: returned,
    } satisfies RcloneVersioningActivationResult);
    apiClientMock.cleanRollbackRcloneVersioning.mockResolvedValue({ summary: legacySummary });
    const view = render(candidateDialog({ ...task, rclonePublication: before }, onUpdated));

    await user.click(screen.getByRole("button", { name: "运行预检" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "启用版本化" })).toBeEnabled());
    await user.click(screen.getByRole("button", { name: "启用版本化" }));
    await waitFor(() => expect(onUpdated).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(screen.getByRole("button", { name: "运行预检" })).toBeEnabled());

    view.rerender(candidateDialog({ ...task, rclonePublication: acceptedParent }, onUpdated));
    fireEvent.change(screen.getByLabelText("Remote 名称"), { target: { value: "draft-remote" } });
    view.rerender(candidateDialog({ ...task, rclonePublication: staleParent }, onUpdated));

    expect(screen.getByLabelText("Remote 名称")).toHaveValue("draft-remote");
    expect(screen.getByRole("button", { name: "清理回退" })).toBeEnabled();
    expect(screen.queryByRole("button", { name: "准备回退" })).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "运行预检" }));
    await waitFor(() => {
      expect(apiClientMock.createRcloneVersioningPreflight).toHaveBeenLastCalledWith("token", 17, {
        expectedTaskRevision: "9007199254741053",
        requestedMode: "versioned_prefix",
      });
    });
    await waitFor(() => expect(screen.getByRole("button", { name: "清理回退" })).toBeEnabled());
    await user.click(screen.getByRole("button", { name: "清理回退" }));
    await waitFor(() => {
      expect(apiClientMock.cleanRollbackRcloneVersioning).toHaveBeenCalledWith("token", 17, {
        expectedTaskRevision: "9007199254741053",
        expectedBindingRevision: "4",
      });
    });
  });

  it("hides clean rollback after an imported-baseline activation returns preparation only", async () => {
    const user = userEvent.setup();
    const onUpdated = vi.fn().mockResolvedValue(undefined);
    const before = portableSummary({ taskRevision: "9007199254741040", bindingRevision: "4", rollbackCapability: "clean_available" });
    const imported = portableSummary({
      state: "committed",
      reasonCode: "ready",
      taskRevision: "9007199254741041",
      bindingRevision: "4",
      rollbackCapability: "preparation_only",
    });
    apiClientMock.createRcloneVersioningPreflight.mockResolvedValue({
      purpose: "activation",
      preflightId: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
      expiresAt: futureExpiry(),
      summary: { ...before, state: "ready", reasonCode: "ready" },
    });
    apiClientMock.activateRcloneVersioning.mockResolvedValue({
      migrationChoice: "imported_baseline",
      summary: imported,
    } satisfies RcloneVersioningActivationResult);
    apiClientMock.prepareRcloneVersioningRollback.mockResolvedValue({ summary: imported });
    const view = render(candidateDialog({ ...task, rclonePublication: before }, onUpdated));

    expect(screen.getByRole("button", { name: "清理回退" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "运行预检" }));
    await user.click(await screen.findByRole("radio", { name: "导入当前基线" }));
    await user.click(screen.getByRole("checkbox"));
    await waitFor(() => expect(screen.getByRole("button", { name: "启用版本化" })).toBeEnabled());
    await user.click(screen.getByRole("button", { name: "启用版本化" }));
    await waitFor(() => expect(onUpdated).toHaveBeenCalledTimes(1));
    expect(apiClientMock.activateRcloneVersioning).toHaveBeenCalledWith("token", 17, {
      expectedTaskRevision: "9007199254741040",
      preflightId: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
      migrationChoice: "imported_baseline",
    });

    view.rerender(candidateDialog({ ...task, rclonePublication: before }, onUpdated));
    expect(screen.queryByRole("button", { name: "清理回退" })).not.toBeInTheDocument();
    view.rerender(candidateDialog({ ...task, rclonePublication: imported }, onUpdated));
    expect(screen.queryByRole("button", { name: "清理回退" })).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "准备回退" }));
    await waitFor(() => {
      expect(apiClientMock.prepareRcloneVersioningRollback).toHaveBeenCalledWith("token", 17, {
        expectedTaskRevision: "9007199254741041",
        expectedBindingRevision: "4",
      });
    });
  });

  it("adopts changed equal-CAS parent facts over a retained response while suppressing the stale and lower snapshots", async () => {
    const user = userEvent.setup();
    const refresh = createDeferred<void>();
    const onUpdated = vi.fn(() => refresh.promise);
    const before = portableSummary({ taskRevision: "9007199254741060", bindingRevision: "4", rollbackCapability: "preparation_only" });
    const activated = portableSummary({
      state: "ready",
      reasonCode: "ready",
      taskRevision: "9007199254741062",
      bindingRevision: "4",
      rollbackCapability: "clean_available",
      credentialExpiresAt: "2026-10-30T08:00:00Z",
    });
    const expired = {
      ...activated,
      state: "preflight_required",
      reasonCode: "preflight_expired",
      credentialExpiresAt: "2026-10-29T08:00:00Z",
    } satisfies RclonePublicationSummary;
    const preparationOnly = { ...expired, rollbackCapability: "preparation_only" } satisfies RclonePublicationSummary;
    const lower = portableSummary({ state: "ready", reasonCode: "ready", taskRevision: "9007199254741061", bindingRevision: "4" });
    const reasonText = () => screen.getByText("安全原因").nextElementSibling?.textContent;
    apiClientMock.createRcloneVersioningPreflight.mockResolvedValue({
      purpose: "activation",
      preflightId: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
      expiresAt: futureExpiry(),
      summary: { ...before, state: "ready", reasonCode: "ready" },
    });
    apiClientMock.activateRcloneVersioning.mockResolvedValue({
      migrationChoice: "first_new_point",
      summary: activated,
    } satisfies RcloneVersioningActivationResult);
    apiClientMock.prepareRcloneVersioningRollback.mockResolvedValue({ summary: preparationOnly });
    const view = render(candidateDialog({ ...task, rclonePublication: before }, onUpdated));

    await user.click(screen.getByRole("button", { name: "运行预检" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "启用版本化" })).toBeEnabled());
    await user.click(screen.getByRole("button", { name: "启用版本化" }));
    await waitFor(() => expect(onUpdated).toHaveBeenCalledTimes(1));

    view.rerender(candidateDialog({ ...task, rclonePublication: { ...before } }, onUpdated));
    expect(screen.getByText("已就绪")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "清理回退" })).toBeInTheDocument();
    view.rerender(candidateDialog({ ...task, rclonePublication: lower }, onUpdated));
    expect(screen.getByText(formatTime(activated.credentialExpiresAt))).toBeInTheDocument();
    await act(async () => { refresh.resolve(); });
    expect(screen.getByRole("button", { name: "清理回退" })).toBeEnabled();
    fireEvent.change(screen.getByLabelText("Remote 名称"), { target: { value: "draft-remote" } });

    view.rerender(candidateDialog({ ...task, rclonePublication: expired }, onUpdated));
    expect(screen.getByText("需要预检")).toBeInTheDocument();
    expect(reasonText()).toBe("预检已过期，请重新运行。");
    expect(screen.getByText(formatTime(expired.credentialExpiresAt))).toBeInTheDocument();
    expect(screen.queryByText(formatTime(activated.credentialExpiresAt))).not.toBeInTheDocument();
    expect(screen.getByLabelText("Remote 名称")).toHaveValue("draft-remote");

    view.rerender(candidateDialog({ ...task, rclonePublication: preparationOnly }, onUpdated));
    expect(screen.queryByRole("button", { name: "清理回退" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "准备回退" })).toBeEnabled();
    view.rerender(candidateDialog({ ...task, rclonePublication: lower }, onUpdated));
    expect(screen.queryByRole("button", { name: "清理回退" })).not.toBeInTheDocument();
    expect(reasonText()).toBe("预检已过期，请重新运行。");
    expect(screen.getByLabelText("Remote 名称")).toHaveValue("draft-remote");

    await user.click(screen.getByRole("button", { name: "准备回退" }));
    await waitFor(() => {
      expect(apiClientMock.prepareRcloneVersioningRollback).toHaveBeenCalledWith("token", 17, {
        expectedTaskRevision: "9007199254741062",
        expectedBindingRevision: "4",
      });
    });
    expect(apiClientMock.cleanRollbackRcloneVersioning).not.toHaveBeenCalled();
  });

  it("adopts a changed equal-CAS parent after a portable binding response without replaying the secret", async () => {
    const refresh = createDeferred<void>();
    const onUpdated = vi.fn(() => refresh.promise);
    const bound = portableSummary({
      state: "capability_settling",
      reasonCode: "capability_settling",
      taskRevision: "9007199254741011",
      bindingRevision: "2",
      rollbackCapability: "preparation_only",
    });
    const settled = { ...bound, state: "preflight_required", reasonCode: "preflight_required" } satisfies RclonePublicationSummary;
    apiClientMock.createRclonePortableBindingSetup.mockResolvedValue({
      setupId: portableSetupId,
      expiresAt: futureExpiry(),
      baseBindingRevision: "0",
    });
    apiClientMock.setRclonePortableBinding.mockResolvedValue(bound);
    const view = render(candidateDialog(portableCandidateTask, onUpdated));

    fillPortableFields();
    fireEvent.click(screen.getByRole("button", { name: "保存 Portable 绑定" }));
    await waitFor(() => expect(onUpdated).toHaveBeenCalledTimes(1));
    const unchangedParent = portableSummary({ taskRevision: "9007199254741010", bindingRevision: "1" });
    view.rerender(candidateDialog({ ...portableCandidateTask, rclonePublication: unchangedParent }, onUpdated));
    expect(screen.getByText("能力证据收敛中")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "清理回退" })).not.toBeInTheDocument();
    await act(async () => { refresh.resolve(); });
    expect(screen.getByLabelText("Rclone 配置")).toHaveValue("");

    view.rerender(candidateDialog({ ...portableCandidateTask, rclonePublication: settled }, onUpdated));
    expect(screen.getByText("需要预检")).toBeInTheDocument();
    expect(screen.getByText("安全原因").nextElementSibling?.textContent).toBe("需要新的预检才能启用受管发布。");
    expect(screen.getByRole("button", { name: "准备回退" })).toBeEnabled();
    expect(screen.getByLabelText("Rclone 配置")).toHaveValue("");
    expect(apiClientMock.createRclonePortableBindingSetup).toHaveBeenCalledTimes(1);
    expect(apiClientMock.setRclonePortableBinding).toHaveBeenCalledTimes(1);
    expect(apiClientMock.createRcloneVersioningPreflight).not.toHaveBeenCalled();
  });
});
