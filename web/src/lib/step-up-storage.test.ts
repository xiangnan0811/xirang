import { beforeEach, describe, expect, it } from "vitest";
import { clearStepUpProof, readStepUpProof, saveStepUpProof } from "./step-up-storage";

describe("step-up proof storage", () => {
  beforeEach(() => {
    sessionStorage.clear();
    localStorage.clear();
  });

  it("仅将 proof 保存到 sessionStorage 且按过期时间读取", () => {
    const now = Date.now();
    const expiresAt = now + 60_000;
    saveStepUpProof("  proof-1  ", expiresAt);

    expect(readStepUpProof(now)).toEqual({ proof: "proof-1", expiresAt });
    expect(sessionStorage.getItem("xirang-step-up-proof")).toBe("proof-1");
    expect(localStorage.getItem("xirang-step-up-proof")).toBeNull();
  });

  it("过期 proof 会被清理", () => {
    const now = Date.now();
    saveStepUpProof("proof-2", now + 1_000);

    expect(readStepUpProof(now + 1_001)).toBeNull();
    expect(sessionStorage.getItem("xirang-step-up-proof")).toBeNull();
    expect(sessionStorage.getItem("xirang-step-up-expires-at")).toBeNull();
  });

  it("clearStepUpProof 会移除 session 中的 proof", () => {
    saveStepUpProof("proof-3", Date.now() + 10_000);

    clearStepUpProof();

    expect(readStepUpProof()).toBeNull();
    expect(sessionStorage.getItem("xirang-step-up-proof")).toBeNull();
  });
});
