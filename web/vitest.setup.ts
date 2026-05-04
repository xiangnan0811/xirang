import "@testing-library/jest-dom/vitest";

function createMemoryStorage(): Storage {
  const store = new Map<string, string>();

  return {
    get length() {
      return store.size;
    },
    clear() {
      store.clear();
    },
    getItem(key: string) {
      return store.has(key) ? store.get(key)! : null;
    },
    key(index: number) {
      return Array.from(store.keys())[index] ?? null;
    },
    removeItem(key: string) {
      store.delete(key);
    },
    setItem(key: string, value: string) {
      store.set(String(key), String(value));
    }
  };
}

function ensureStorage(name: "localStorage" | "sessionStorage") {
  if (typeof window === "undefined") {
    return;
  }

  const candidate = window[name];
  if (
    candidate &&
    typeof candidate.getItem === "function" &&
    typeof candidate.setItem === "function" &&
    typeof candidate.removeItem === "function" &&
    typeof candidate.clear === "function"
  ) {
    return;
  }

  const storage = createMemoryStorage();
  Object.defineProperty(window, name, {
    configurable: true,
    writable: true,
    value: storage,
  });
  Object.defineProperty(globalThis, name, {
    configurable: true,
    writable: true,
    value: storage,
  });
}

ensureStorage("localStorage");
ensureStorage("sessionStorage");

// Default to Chinese for tests (matches existing test assertions)
if (typeof window !== "undefined") {
  window.localStorage.setItem("xirang.language", "zh");
}

// jsdom 缺少 ResizeObserver；recharts、@tanstack/react-virtual 等库会用到，
// 这里提供一个 no-op stub 避免每个测试文件重复定义。
if (typeof globalThis !== "undefined" && !("ResizeObserver" in globalThis)) {
  class ResizeObserverStub {
    observe() {}
    unobserve() {}
    disconnect() {}
  }
  Object.defineProperty(globalThis, "ResizeObserver", {
    configurable: true,
    writable: true,
    value: ResizeObserverStub,
  });
}

if (typeof window !== "undefined" && !window.matchMedia) {
  Object.defineProperty(window, "matchMedia", {
    writable: true,
    value: (query: string) => ({
      matches: false,
      media: query,
      onchange: null,
      addListener: () => undefined,
      removeListener: () => undefined,
      addEventListener: () => undefined,
      removeEventListener: () => undefined,
      dispatchEvent: () => false
    })
  });
}
