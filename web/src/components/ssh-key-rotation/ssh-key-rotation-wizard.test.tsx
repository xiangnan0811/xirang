import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { NodeRecord, SSHKeyRecord } from "@/types/domain";
import { SSHKeyRotationWizard } from "./ssh-key-rotation-wizard";

const { updateSSHKey, testConnection } = vi.hoisted(() => ({
  updateSSHKey: vi.fn(),
  testConnection: vi.fn(),
}));

vi.mock("@/lib/api/ssh-keys-api", () => ({
  createSSHKeysApi: () => ({
    updateSSHKey,
    testConnection,
  }),
}));

vi.mock("@/components/ui/toast-sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}));

const selectedKey: SSHKeyRecord = {
  id: "key-1",
  name: "生产密钥",
  username: "root",
  keyType: "ed25519",
  fingerprint: "SHA256:old",
  broadScope: false,
  disabled: false,
  expiresAt: "",
  allowedPurposes: "probe",
  allowedNodeIds: "",
  allowedNodeTags: "",
  createdAt: "2026-01-01 00:00:00",
  lastUsedAt: undefined,
};

function node(id: number, status: NodeRecord["status"], name: string): NodeRecord {
  return {
    id,
    name,
    host: `${name}.example`,
    address: `${name}.example`,
    ip: `${name}.example`,
    port: 22,
    username: "root",
    authType: "key",
    keyId: "key-1",
    basePath: "/",
    tags: [],
    status,
    lastSeenAt: "",
    lastBackupAt: "",
  };
}

describe("SSHKeyRotationWizard", () => {
  beforeEach(() => {
    updateSSHKey.mockReset();
    testConnection.mockReset();
    updateSSHKey.mockResolvedValue({ ...selectedKey, fingerprint: "SHA256:new" });
    testConnection.mockResolvedValue([
      { nodeId: "node-1", name: "node-online", host: "node-online.example", port: 22, success: true, latencyMs: 8 },
      { nodeId: "node-2", name: "node-offline", host: "node-offline.example", port: 22, success: false, latencyMs: 0, error: "refused" },
    ]);
  });

  it("verifies every affected node after rotation, including nodes whose stored status is not online", async () => {
    const user = userEvent.setup();
    const affected = [
      node(1, "online", "node-online"),
      node(2, "offline", "node-offline"),
    ];

    render(
      <SSHKeyRotationWizard
        open
        onOpenChange={vi.fn()}
        sshKeys={[selectedKey]}
        keyUsageMap={new Map([[selectedKey.id, affected]])}
        preselectedKey={selectedKey}
        token="token"
        onComplete={vi.fn()}
      />,
    );

    await user.type(screen.getByLabelText("私钥内容"), "FAKE_PRIVATE_KEY_FOR_TEST_ONLY");
    await user.click(screen.getByRole("button", { name: "下一步" }));
    await user.type(screen.getByLabelText("输入 2 以确认受影响节点数"), "2");
    await user.click(screen.getByRole("button", { name: "确认轮换" }));

    expect(await screen.findByText("node-offline")).toBeInTheDocument();
    expect(testConnection).toHaveBeenCalledWith("token", "key-1", ["node-1", "node-2"]);
    expect(updateSSHKey).toHaveBeenCalledWith("token", "key-1", expect.objectContaining({
      allowedPurposes: "probe",
    }));
    expect(screen.queryByText("跳过（离线）")).not.toBeInTheDocument();
    expect(screen.getByText("验证失败")).toBeInTheDocument();
  });
});
