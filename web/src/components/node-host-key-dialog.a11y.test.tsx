import { render } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { NodeHostKeyDialog } from "@/components/node-host-key-dialog";
import { runAxe } from "@/test/a11y-helpers";
import type { NodeHostKeyIssueCode } from "@/types/domain";

const issue = (code: NodeHostKeyIssueCode) => ({
  nodeName: "node-prod-1",
  code,
  hostKey: {
    algorithm: "ssh-ed25519",
    fingerprintSha256: "SHA256:hostKeyDialogA11yFingerprint",
  },
});

function renderDialog(code: NodeHostKeyIssueCode, isAdmin: boolean) {
  return render(
    <NodeHostKeyDialog
      open
      issue={issue(code)}
      isAdmin={isAdmin}
      trusting={false}
      error={null}
      onTrust={vi.fn()}
      onOpenSettings={vi.fn()}
      onOpenChange={vi.fn()}
    />,
  );
}

describe("NodeHostKeyDialog a11y", () => {
  it("未知主机密钥的管理员确认没有 axe 违规", async () => {
    renderDialog("ssh_host_key_unknown", true);
    expect(await runAxe(document.body)).toHaveNoViolations();
  });

  it("未知主机密钥的非管理员提示没有 axe 违规", async () => {
    renderDialog("ssh_host_key_unknown", false);
    expect(await runAxe(document.body)).toHaveNoViolations();
  });

  it("主机密钥不一致警告没有 axe 违规", async () => {
    renderDialog("ssh_host_key_mismatch", true);
    expect(await runAxe(document.body)).toHaveNoViolations();
  });
});
