import { describe, expect, it } from "vitest";
import {
  SSH_PURPOSE_SUGGESTIONS,
  preserveSSHPurposeScope,
  retiredSSHPurposesIn,
  sshKeyIsBroadScope,
} from "./ssh-purpose-scope";

describe("ssh purpose scope", () => {
  it("does not suggest retired probe or node_logs purposes for new keys", () => {
    expect(SSH_PURPOSE_SUGGESTIONS).not.toContain("probe");
    expect(SSH_PURPOSE_SUGGESTIONS).not.toContain("node_logs");
  });

  it("keeps a retired-only scope instead of collapsing it to empty", () => {
    expect(preserveSSHPurposeScope(" probe ")).toBe("probe");
    expect(preserveSSHPurposeScope("node_logs")).toBe("node_logs");
    expect(preserveSSHPurposeScope(" probe , node_logs , probe ")).toBe("probe,node_logs");
    expect(preserveSSHPurposeScope('["probe","node_logs"]')).toBe("probe,node_logs");
  });

  it("matches backend broad scope for retired purposes", () => {
    expect(sshKeyIsBroadScope({
      allowedPurposes: "probe",
      allowedNodeIds: "",
      allowedNodeTags: "",
    })).toBe(true);
    expect(sshKeyIsBroadScope({
      allowedPurposes: "node_logs",
      allowedNodeIds: " ",
      allowedNodeTags: "",
    })).toBe(true);
    expect(sshKeyIsBroadScope({
      allowedPurposes: "probe,node_logs",
      allowedNodeIds: "4",
      allowedNodeTags: "",
    })).toBe(false);
    expect(sshKeyIsBroadScope({
      allowedPurposes: "probe",
      allowedNodeIds: "",
      allowedNodeTags: "prod",
    })).toBe(false);
    expect(sshKeyIsBroadScope({
      allowedPurposes: "",
      allowedNodeIds: "4",
      allowedNodeTags: "prod",
    })).toBe(true);
    expect(sshKeyIsBroadScope({
      allowedPurposes: "terminal",
      allowedNodeIds: "4",
      allowedNodeTags: "",
    })).toBe(false);
  });

  it("round-trips historical tokens mixed with current purposes", () => {
    expect(preserveSSHPurposeScope("terminal, probe, node_logs")).toBe("terminal,probe,node_logs");
    expect(retiredSSHPurposesIn("terminal,probe")).toEqual(["probe"]);
  });

  it("leaves a genuinely empty purpose list empty", () => {
    expect(preserveSSHPurposeScope("")).toBe("");
    expect(preserveSSHPurposeScope(" , ")).toBe("");
    expect(preserveSSHPurposeScope("[]")).toBe("");
    expect(sshKeyIsBroadScope({
      allowedPurposes: "",
      allowedNodeIds: "",
      allowedNodeTags: "",
    })).toBe(true);
  });
});
