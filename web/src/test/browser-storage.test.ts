import { afterEach, describe, expect, it, vi } from "vitest";

describe("browser storage test environment", () => {
  afterEach(() => {
    vi.restoreAllMocks();
    localStorage.clear();
    sessionStorage.clear();
  });

  it.each(["localStorage", "sessionStorage"] as const)(
    "uses browser Storage semantics and prototype methods for %s",
    (name) => {
      const storage = window[name];
      expect(globalThis[name]).toBe(storage);
      expect(storage).toBeInstanceOf(Storage);
      expect(window.Storage).toBe(Storage);

      storage.clear();
      const writes = vi.spyOn(Storage.prototype, "setItem");
      storage.setItem("storage-contract", "value");
      expect(writes).toHaveBeenCalledExactlyOnceWith("storage-contract", "value");
      expect(storage.getItem("storage-contract")).toBe("value");
      expect(storage.key(0)).toBe("storage-contract");
      expect(storage.length).toBe(1);
      expect(new StorageEvent("storage", { storageArea: storage }).storageArea).toBe(storage);
      storage.removeItem("storage-contract");
      expect(storage.getItem("storage-contract")).toBeNull();
    },
  );

  it("keeps local and session storage independent", () => {
    localStorage.setItem("storage-contract", "local");
    sessionStorage.setItem("storage-contract", "session");
    localStorage.clear();
    expect(sessionStorage.getItem("storage-contract")).toBe("session");
  });
});
