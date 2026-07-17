import "@testing-library/jest-dom/vitest";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { runAxe } from "@/test/a11y-helpers";
import type { RclonePublicationSummary, TaskRecord } from "@/types/domain";
import { TaskRcloneVersioningDialog } from "./task-rclone-versioning-dialog";

const { apiClientMock } = vi.hoisted(() => ({
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
    for (const mock of Object.values(apiClientMock)) {
      mock.mockReset();
    }
  });

  afterEach(() => {
    vi.clearAllMocks();
  });

  it("completes portable binding, preflight, and first-new activation with exact revisions", async () => {
    const user = userEvent.setup();
    const onUpdated = vi.fn().mockResolvedValue(undefined);
    apiClientMock.createRclonePortableBindingSetup.mockResolvedValue({
      setupId: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
      expiresAt: futureExpiry(),
    });
    apiClientMock.setRclonePortableBinding.mockResolvedValue(portableSummary());
    apiClientMock.createRcloneVersioningPreflight.mockResolvedValue({
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
});
