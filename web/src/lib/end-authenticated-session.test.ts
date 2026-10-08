import { describe, expect, it, vi } from "vitest";
import { endAuthenticatedSession } from "./end-authenticated-session";

describe("endAuthenticatedSession", () => {
  it("clears the local session before the backend logout request settles", async () => {
    const order: string[] = [];
    let resolve!: () => void;
    const promise = new Promise<void>((done) => {
      resolve = done;
    });
    const logout = vi.fn(() => {
      order.push("local");
    });
    const requestLogout = vi.fn((token: string) => {
      order.push(`api:${token}`);
      return promise;
    });

    const done = endAuthenticatedSession({
      token: "token-1",
      logout,
      requestLogout,
    });

    expect(order).toEqual(["local", "api:token-1"]);
    expect(logout).toHaveBeenCalledTimes(1);
    resolve();
    await done;
  });

  it("does not throw when the backend logout fails", async () => {
    const logout = vi.fn();
    await expect(endAuthenticatedSession({
      token: "token-1",
      logout,
      requestLogout: vi.fn().mockRejectedValue(new Error("offline")),
    })).resolves.toBeUndefined();
    expect(logout).toHaveBeenCalledTimes(1);
  });

  it("skips the network request when there is no token", async () => {
    const requestLogout = vi.fn();
    const logout = vi.fn();
    await endAuthenticatedSession({
      token: null,
      logout,
      requestLogout,
    });
    expect(logout).toHaveBeenCalledTimes(1);
    expect(requestLogout).not.toHaveBeenCalled();
  });
});
