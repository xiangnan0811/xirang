import { act, renderHook } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { useConfirm } from "./use-confirm";

describe("useConfirm cancellation", () => {
  it("rejects active and queued confirmations without reviving them on the next request", async () => {
    const { result } = renderHook(() => useConfirm());
    let active!: Promise<boolean>;
    let queued!: Promise<boolean>;
    act(() => {
      active = result.current.confirm({ title: "Delete first", description: "First request" });
      queued = result.current.confirm({ title: "Delete second", description: "Queued request" });
    });
    act(() => result.current.cancelPending());
    await expect(active).resolves.toBe(false);
    await expect(queued).resolves.toBe(false);
    expect(result.current.dialog).toBeNull();
    let fresh!: Promise<boolean>;
    act(() => {
      fresh = result.current.confirm({ title: "Fresh request", description: "New identity" });
    });
    expect(result.current.dialog).not.toBeNull();
    act(() => result.current.cancelPending());
    await expect(fresh).resolves.toBe(false);
  });
});
