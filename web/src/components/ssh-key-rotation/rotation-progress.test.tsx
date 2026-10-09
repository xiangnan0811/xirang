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
  },
];

describe("RotationProgress", () => {
  it("requires affected-node count acknowledgement before confirming rotation", async () => {
    const user = userEvent.setup();
    const onAcknowledgementChange = vi.fn();
    const onNext = vi.fn();
    const { rerender } = render(
      <RotationProgress
        affectedNodes={affectedNodes}
        candidatePublicKey="ssh-ed25519 AAAA"
        oldPublicKeyFingerprint="SHA256:public-old"
        newPublicKeyFingerprint="SHA256:public-new"
        onCopyPublicKey={vi.fn()}
        acknowledgement=""
        onAcknowledgementChange={onAcknowledgementChange}
        onBack={vi.fn()}
        onNext={onNext}
      />,
    );

    const confirmButton = screen.getByRole("button", { name: "确认轮换" });
    expect(confirmButton).toBeDisabled();
    expect(screen.getByText("预估数量")).toBeInTheDocument();
    expect(screen.getByText("最近测试通过")).toBeInTheDocument();
    expect(screen.getByText("最近测试未通过")).toBeInTheDocument();
    expect(screen.getByText("这里只是页面缓存的预估数量。确认后，服务端会按完整库存验证并决定是否保存。")).toBeInTheDocument();
    expect(screen.getByText("确认后将会连接主机以验证候选密钥，不会部署公钥，也不会修改 known_hosts。请先由管理员部署远端公钥。")).toBeInTheDocument();
    expect(screen.getByText("SHA256:public-old")).toBeInTheDocument();
    expect(screen.getByText("SHA256:public-new")).toBeInTheDocument();
    expect(screen.queryByText(selectedKey.fingerprint)).not.toBeInTheDocument();

    await user.type(screen.getByLabelText("输入 2 以确认预估节点数"), "1");
    expect(onAcknowledgementChange).toHaveBeenLastCalledWith("1");
    expect(onNext).not.toHaveBeenCalled();

    rerender(
      <RotationProgress
        affectedNodes={affectedNodes}
        candidatePublicKey="ssh-ed25519 AAAA"
        oldPublicKeyFingerprint="SHA256:public-old"
        newPublicKeyFingerprint="SHA256:public-new"
        onCopyPublicKey={vi.fn()}
        acknowledgement="1"
        onAcknowledgementChange={onAcknowledgementChange}
        onBack={vi.fn()}
        onNext={onNext}
      />,
    );
    expect(screen.getByText("请输入 2 以确认预估节点数。")).toBeInTheDocument();
    expect(screen.getByLabelText("输入 2 以确认预估节点数")).toHaveAttribute("aria-invalid", "true");
    await user.click(screen.getByRole("button", { name: "确认轮换" }));
    expect(onNext).not.toHaveBeenCalled();
  });

  it("allows confirmation when affected-node count acknowledgement matches", async () => {
    const user = userEvent.setup();
    const onNext = vi.fn();
    render(
      <RotationProgress
        affectedNodes={affectedNodes}
        candidatePublicKey="ssh-ed25519 AAAA"
        oldPublicKeyFingerprint="SHA256:public-old"
        newPublicKeyFingerprint="SHA256:public-new"
        onCopyPublicKey={vi.fn()}
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
