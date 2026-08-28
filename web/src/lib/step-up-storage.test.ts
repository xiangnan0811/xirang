import { readFileSync } from "node:fs";
import path from "node:path";
import { beforeEach, describe, expect, it } from "vitest";
import { STEP_UP_ACTIONS } from "./api/totp-api";
import { clearStepUpProof, readStepUpProof, saveStepUpProof } from "./step-up-storage";

describe("step-up proof storage", () => {
  beforeEach(() => {
    sessionStorage.clear();
    localStorage.clear();
  });

  it("isolates proofs by action in one versioned session map", () => {
    const now = Date.now();
    saveStepUpProof(STEP_UP_ACTIONS.terminalOpen, " terminal-proof ", now + 60_000);
    saveStepUpProof(STEP_UP_ACTIONS.taskManualTrigger, "task-proof", now + 120_000);

    expect(readStepUpProof(STEP_UP_ACTIONS.terminalOpen, now)).toEqual({
      proof: "terminal-proof",
      expiresAt: now + 60_000,
    });
    expect(readStepUpProof(STEP_UP_ACTIONS.taskManualTrigger, now)).toEqual({
      proof: "task-proof",
      expiresAt: now + 120_000,
    });
    expect(sessionStorage.getItem("xirang-step-up-proofs-v2")).toContain("terminal.open");
    expect(localStorage.getItem("xirang-step-up-proofs-v2")).toBeNull();
  });

  it("isolates retention hold release from repository purge", () => {
    const now = Date.now();
    expect(STEP_UP_ACTIONS.retentionHoldRelease).toBe("retention.hold_release");

    saveStepUpProof(STEP_UP_ACTIONS.retentionHoldRelease, "hold-proof", now + 60_000);
    saveStepUpProof(STEP_UP_ACTIONS.repositoryPurge, "purge-proof", now + 60_000);

    expect(readStepUpProof(STEP_UP_ACTIONS.retentionHoldRelease, now)?.proof).toBe("hold-proof");
    expect(readStepUpProof(STEP_UP_ACTIONS.repositoryPurge, now)?.proof).toBe("purge-proof");

    clearStepUpProof(STEP_UP_ACTIONS.retentionHoldRelease);
    expect(readStepUpProof(STEP_UP_ACTIONS.retentionHoldRelease, now)).toBeNull();
    expect(readStepUpProof(STEP_UP_ACTIONS.repositoryPurge, now)?.proof).toBe("purge-proof");
  });

  it("expires one action without clearing another", () => {
    const now = Date.now();
    saveStepUpProof(STEP_UP_ACTIONS.terminalOpen, "terminal-proof", now + 1_000);
    saveStepUpProof(STEP_UP_ACTIONS.configExport, "config-proof", now + 10_000);

    expect(readStepUpProof(STEP_UP_ACTIONS.terminalOpen, now + 1_001)).toBeNull();
    expect(readStepUpProof(STEP_UP_ACTIONS.configExport, now + 1_001)?.proof).toBe("config-proof");
  });

  it("keeps secret reveal until its exact fixed 45-minute expiry without sliding", () => {
    const issuedAt = Date.now();
    const expiresAt = issuedAt + 45 * 60_000;
    saveStepUpProof(STEP_UP_ACTIONS.assetSecretReveal, "secret-proof", expiresAt);

    expect(readStepUpProof(STEP_UP_ACTIONS.assetSecretReveal, issuedAt + 30 * 60_000)).toEqual({
      proof: "secret-proof",
      expiresAt,
    });
    expect(readStepUpProof(STEP_UP_ACTIONS.assetSecretReveal, expiresAt - 1)).toEqual({
      proof: "secret-proof",
      expiresAt,
    });
    expect(readStepUpProof(STEP_UP_ACTIONS.assetSecretReveal, expiresAt)).toBeNull();
  });

  it("clears one action or all actions", () => {
    const now = Date.now();
    saveStepUpProof(STEP_UP_ACTIONS.terminalOpen, "terminal-proof", now + 10_000);
    saveStepUpProof(STEP_UP_ACTIONS.configExport, "config-proof", now + 10_000);

    clearStepUpProof(STEP_UP_ACTIONS.terminalOpen);
    expect(readStepUpProof(STEP_UP_ACTIONS.terminalOpen, now)).toBeNull();
    expect(readStepUpProof(STEP_UP_ACTIONS.configExport, now)?.proof).toBe("config-proof");

    clearStepUpProof();
    expect(readStepUpProof(STEP_UP_ACTIONS.configExport, now)).toBeNull();
    expect(sessionStorage.getItem("xirang-step-up-proofs-v2")).toBeNull();
  });

  it("deletes legacy generic keys without promoting their proof", () => {
    sessionStorage.setItem("xirang-step-up-proof", "legacy-generic-proof");
    sessionStorage.setItem("xirang-step-up-expires-at", String(Date.now() + 60_000));

    expect(readStepUpProof(STEP_UP_ACTIONS.terminalOpen)).toBeNull();
    expect(readStepUpProof(STEP_UP_ACTIONS.taskManualTrigger)).toBeNull();
    expect(sessionStorage.getItem("xirang-step-up-proof")).toBeNull();
    expect(sessionStorage.getItem("xirang-step-up-expires-at")).toBeNull();
  });

  it("remains a dependency leaf so API core cannot form a storage cycle", () => {
    const source = readFileSync(path.resolve(process.cwd(), "src/lib/step-up-storage.ts"), "utf8");

    expect(source).not.toMatch(/(?:from|import\s*\()[^\n]*totp-api/);
  });
});
