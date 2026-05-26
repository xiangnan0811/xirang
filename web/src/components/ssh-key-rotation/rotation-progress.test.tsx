import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { RotationProgress } from "./rotation-progress";
import type { NodeRecord, SSHKeyRecord } from "@/types/domain";

const selectedKey: SSHKeyRecord = {
  id: "key-1",
  name: "生产密钥",
  username: "root",
  keyType: "ed25519",
  fingerprint: "SHA256:abc123",
  broadScope: false,
  disabled: false,
  expiresAt: "",
  allowedPurposes: "",
  allowedNodeIds: "",
  allowedNodeTags: "",
  createdAt: "2026-01-01 00:00:00",
  lastUsedAt: undefined,
};

const affectedNodes: NodeRecord[] = [
  {
    id: 1,
    name: "node-a",
    host: "redacted-a",
    address: "redacted-a",
    ip: "redacted-a",
    port: 22,
    username: "root",
    authType: "key",
    keyId: "key-1",
    basePath: "/",
    tags: [],
    status: "online",
    lastSeenAt: "-",
    lastBackupAt: "-",
    diskFreePercent: 0,
    diskUsedGb: 0,
    diskTotalGb: 0,
    diskProbeAt: "-",
  },
  {
    id: 2,
    name: "node-b",
    host: "redacted-b",
    address: "redacted-b",
    ip: "redacted-b",
    port: 22,
    username: "root",
    authType: "key",
    keyId: "key-1",
    basePath: "/",
    tags: [],
    status: "offline",
    lastSeenAt: "-",
    lastBackupAt: "-",
    diskFreePercent: 0,
    diskUsedGb: 0,
    diskTotalGb: 0,
    diskProbeAt: "-",
  },
];

describe("RotationProgress", () => {
  it("requires affected-node count acknowledgement before confirming rotation", async () => {
    const user = userEvent.setup();
    const onAcknowledgementChange = vi.fn();
    const onNext = vi.fn();
    const { rerender } = render(
      <RotationProgress
        selectedKey={selectedKey}
        affectedNodes={affectedNodes}
        acknowledgement=""
        onAcknowledgementChange={onAcknowledgementChange}
        onBack={vi.fn()}
        onNext={onNext}
      />,
    );

    const confirmButton = screen.getByRole("button", { name: "确认轮换" });
    expect(confirmButton).toBeDisabled();
    expect(screen.getByText("影响总数")).toBeInTheDocument();
    expect(screen.getByText("在线")).toBeInTheDocument();
    expect(screen.getByText("离线 / 未验证")).toBeInTheDocument();

    await user.type(screen.getByLabelText("输入 2 以确认受影响节点数"), "1");
    expect(onAcknowledgementChange).toHaveBeenLastCalledWith("1");
    expect(onNext).not.toHaveBeenCalled();

    rerender(
      <RotationProgress
        selectedKey={selectedKey}
        affectedNodes={affectedNodes}
        acknowledgement="1"
        onAcknowledgementChange={onAcknowledgementChange}
        onBack={vi.fn()}
        onNext={onNext}
      />,
    );
    expect(screen.getByText("请输入 2 以确认受影响节点数。")).toBeInTheDocument();
    expect(screen.getByLabelText("输入 2 以确认受影响节点数")).toHaveAttribute("aria-invalid", "true");
    await user.click(screen.getByRole("button", { name: "确认轮换" }));
    expect(onNext).not.toHaveBeenCalled();
  });

  it("allows confirmation when affected-node count acknowledgement matches", async () => {
    const user = userEvent.setup();
    const onNext = vi.fn();
    render(
      <RotationProgress
        selectedKey={selectedKey}
        affectedNodes={affectedNodes}
        acknowledgement="2"
        onAcknowledgementChange={vi.fn()}
        onBack={vi.fn()}
        onNext={onNext}
      />,
    );

    const confirmButton = screen.getByRole("button", { name: "确认轮换" });
    expect(confirmButton).toBeEnabled();
    await user.click(confirmButton);
    expect(onNext).toHaveBeenCalledTimes(1);
  });
});
