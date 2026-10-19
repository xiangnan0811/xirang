import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  beginAuthTransitionBarrier, bumpAuthSessionGeneration, clearAuthTransitionBarrier,
  releaseAuthTransitionBarrier, rememberAuthIdentity,
} from "@/lib/api/core";
import {
  captureNotificationOwner, createNotificationConfigReturn, getNotificationContextEpoch,
  parseNotificationAlertId, parseNotificationAlertTarget, readNotificationConfigReturn,
  withoutNotificationConfigReturn,
} from "./notification-config-context";

beforeEach(() => {
  clearAuthTransitionBarrier();
  rememberAuthIdentity("A", "admin");
  bumpAuthSessionGeneration();
});

describe("safe notification navigation", () => {
  it.each(["1", "42", "9007199254740991"])("accepts both public spellings of %s", (id) => {
    expect(parseNotificationAlertId(id)).toBe(id);
    expect(parseNotificationAlertId(`alert-${id}`)).toBe(id);
  });
  it.each([null, undefined, 42, [], ["42"], "", " 42", "42 ", "0", "-1", "01", "alert-01", "1.0", "1e2", "9007199254740992", "alert-9007199254740992", "1".repeat(23)])("rejects unsafe ID %j", (value) => {
    expect(parseNotificationAlertId(value)).toBeNull();
  });
  it("rejects ambiguous targets even when duplicate values agree", () => {
    expect(parseNotificationAlertTarget([])).toBeNull();
    expect(parseNotificationAlertTarget(["alert-42"])).toBe("42");
    expect(parseNotificationAlertTarget(["42", "42"])).toBe("invalid");
    expect(parseNotificationAlertTarget(["42", "43"])).toBe("invalid");
    expect(parseNotificationAlertTarget(["9007199254740992"])).toBe("invalid");
  });
  it("admits only the exact current-document admin return contract", () => {
    const valid = createNotificationConfigReturn("42");
    expect(readNotificationConfigReturn(valid)?.alertId).toBe("42");
    expect(readNotificationConfigReturn(createNotificationConfigReturn(null))?.alertId).toBeNull();
    for (const invalid of [
      { ...valid, secret: "private" }, { ...valid, url: "https://example.com" },
      { ...valid, source: "tasks" }, { ...valid, version: 2 }, { ...valid, alertId: "alert-42" },
      { ...valid, alertId: "9007199254740992" }, { ...valid, owner: "other-document" },
      { version: 1, source: "notifications", alertId: "42" }, [], "https://example.com",
    ]) expect(readNotificationConfigReturn(invalid)).toBeNull();
    rememberAuthIdentity("A", "viewer");
    expect(readNotificationConfigReturn(valid)).toBeNull();
    expect(createNotificationConfigReturn(null)).toBeNull();
  });
  it("invalidates late responses and history across A→B→A and same-token login", () => {
    const oldReturn = createNotificationConfigReturn("42");
    const acceptLateResponse = captureNotificationOwner("A", "admin");
    rememberAuthIdentity("B", "admin");
    bumpAuthSessionGeneration();
    rememberAuthIdentity("A", "admin");
    bumpAuthSessionGeneration();
    expect(acceptLateResponse()).toBe(false);
    expect(readNotificationConfigReturn(oldReturn)).toBeNull();
    const secondOwner = captureNotificationOwner("A", "admin");
    bumpAuthSessionGeneration();
    expect(secondOwner()).toBe(false);
  });
  it("does not restore ownership when the transition barrier releases", () => {
    const saved = createNotificationConfigReturn("42");
    const owner = captureNotificationOwner("A", "admin");
    const barrier = beginAuthTransitionBarrier();
    expect(owner()).toBe(false);
    expect(createNotificationConfigReturn(null)).toBeNull();
    releaseAuthTransitionBarrier(barrier);
    expect(owner()).toBe(false);
    expect(readNotificationConfigReturn(saved)).toBeNull();
    expect(captureNotificationOwner("A", "admin")()).toBe(true);
  });
  it("rejects a same-token role change before a generation notification", () => {
    const owner = captureNotificationOwner("A", "admin");
    rememberAuthIdentity("A", "operator");
    expect(owner()).toBe(false);
  });
  it("preserves unrelated navigation state when dropping invalid context", () => {
    expect(withoutNotificationConfigReturn({ notificationConfigReturn: {}, securityReturnTo: "/app/nodes" })).toEqual({ securityReturnTo: "/app/nodes" });
    expect(withoutNotificationConfigReturn({ notificationConfigReturn: {} })).toBeNull();
    expect(withoutNotificationConfigReturn([])).toBeNull();
    expect(withoutNotificationConfigReturn(new Date())).toBeNull();
  });
  it("rejects history from an earlier module document", async () => {
    const saved = createNotificationConfigReturn("42");
    const epoch = getNotificationContextEpoch();
    // A fresh module evaluation is the document boundary; static imports cannot exercise it.
    vi.resetModules();
    const core = await import("@/lib/api/core");
    core.rememberAuthIdentity("A", "admin");
    const reloaded = await import("./notification-config-context");
    expect(reloaded.getNotificationContextEpoch()).not.toBe(epoch);
    expect(reloaded.readNotificationConfigReturn(saved)).toBeNull();
  });
  it("fails closed without Web Crypto while retaining local response ownership", async () => {
    vi.resetModules();
    vi.stubGlobal("crypto", undefined);
    try {
      // Reload without Web Crypto to exercise the initialization capability boundary.
      const core = await import("@/lib/api/core");
      core.rememberAuthIdentity("A", "admin");
      const noCrypto = await import("./notification-config-context");
      expect(noCrypto.createNotificationConfigReturn(null)).toBeNull();
      const owner = noCrypto.captureNotificationOwner("A", "admin");
      core.bumpAuthSessionGeneration();
      expect(owner()).toBe(false);
    } finally {
      vi.unstubAllGlobals();
      vi.resetModules();
    }
  });
});
