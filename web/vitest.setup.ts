import "@testing-library/jest-dom/vitest";
// Wave 4 PR-A: vitest-axe 自定义 matcher（toHaveNoViolations 等）。
// 注：
// 1. vitest-axe@0.1.0 的 `extend-expect.js` 是空文件，必须显式 expect.extend(matchers) 才能在 vitest 4 注册。
// 2. 上游 extend-expect.d.ts 只 augment `Vi` namespace，与 vitest 4 的 `@vitest/expect#Matchers` 不匹配；
//    因此 TypeScript 类型扩展放在 `src/types/vitest-axe.d.ts`（被 tsconfig include）。
import * as axeMatchers from "vitest-axe/matchers";
import { afterAll, afterEach, beforeAll, expect } from "vitest";

import { server } from "@/test/mocks/server";
expect.extend(axeMatchers);

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

const { i18nReady } = await import("@/i18n");
await i18nReady;

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

beforeAll(() => {
  server.listen({ onUnhandledRequest: "error" });
});
afterEach(() => {
  server.resetHandlers();
});
afterAll(() => {
  server.close();
});

const consoleAllowlist: RegExp[] = [
  /Warning:/,
  /Not implemented:/,
  /not wrapped in act/,
  /The current testing environment is not configured to support act/,
  /Error: Could not parse CSS stylesheet/,
  /React does not recognize the `.*` prop on a DOM element/,
  /The width\(0\) and height\(0\) of chart should be greater than 0/,
  /The above error occurred in the /,
  /Consider adding an error boundary/,
  /React will try to recreate this component tree/,
];

function installConsoleGate(method: "error" | "warn") {
  const original = console[method].bind(console);
  console[method] = (...args: unknown[]) => {
    const text = args.map((value) => String(value)).join(" ");
    if (consoleAllowlist.some((pattern) => pattern.test(text))) {
      original(...args);
      return;
    }
    throw new Error(`unexpected console.${method}: ${text}`);
  };
}

installConsoleGate("error");
installConsoleGate("warn");

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
