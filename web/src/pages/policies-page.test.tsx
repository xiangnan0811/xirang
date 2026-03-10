import "@testing-library/jest-dom/vitest";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import type { ConsoleOutletContext } from "@/components/layout/app-shell";
import { PoliciesPage } from "./policies-page";

const contextRef: { current: ConsoleOutletContext } = {
  current: {} as ConsoleOutletContext,
};
const confirmMock = vi.fn().mockResolvedValue(true);

function createMemoryStorage() {
  const store = new Map<string, string>();
  return {
    clear: () => store.clear(),
    getItem: (key: string) => store.get(key) ?? null,
    key: (index: number) => Array.from(store.keys())[index] ?? null,
    removeItem: (key: string) => store.delete(key),
    setItem: (key: string, value: string) => store.set(key, value),
    get length() {
      return store.size;
    },
  } satisfies Storage;
}

vi.mock("react-router-dom", async () => {
  const actual = await vi.importActual<typeof import("react-router-dom")>(
    "react-router-dom"
  );
  return {
    ...actual,
    useOutletContext: () => contextRef.current,
  };
});

vi.mock("@/components/policy-editor-dialog", () => ({
  PolicyEditorDialog: () => null,
}));

vi.mock("@/hooks/use-confirm", () => ({
  useConfirm: () => ({
    confirm: confirmMock,
    dialog: null,
  }),
}));

vi.mock("@/components/ui/toast", () => ({
  toast: {
    success: vi.fn(),
    error: vi.fn(),
  },
}));

function createContext(overrides?: Partial<ConsoleOutletContext>) {
  const base = {
    policies: [
      {
        id: 1,
        name: "每日备份",
        sourcePath: "/data/source",
        targetPath: "/data/target",
        cron: "0 2 * * *",
        naturalLanguage: "每天凌晨 2 点",
        criticalThreshold: 1,
        enabled: true,
      },
      {
        id: 2,
        name: "每小时备份",
        sourcePath: "/data/hourly",
        targetPath: "/backup/hourly",
        cron: "0 * * * *",
        naturalLanguage: "每小时整点",
        criticalThreshold: 2,
        enabled: false,
      },
    ],
    loading: false,
    globalSearch: "",
    setGlobalSearch: vi.fn(),
    createPolicy: vi.fn().mockResolvedValue(undefined),
    updatePolicy: vi.fn().mockResolvedValue(undefined),
    deletePolicy: vi.fn().mockResolvedValue(undefined),
    togglePolicy: vi.fn().mockResolvedValue(undefined),
  } as unknown as ConsoleOutletContext;

  contextRef.current = {
    ...base,
    ...overrides,
  } as ConsoleOutletContext;
}

describe("PoliciesPage", () => {
  beforeEach(() => {
    Object.defineProperty(window, "localStorage", {
      configurable: true,
      value: createMemoryStorage(),
    });
    window.localStorage.clear();
    confirmMock.mockClear();
    createContext();
  });

  it("重置筛选时会同时清空全局搜索并恢复策略列表", async () => {
    const user = userEvent.setup();
    const setGlobalSearchMock = vi.fn((value: string) => {
      createContext({
        globalSearch: value,
        setGlobalSearch: setGlobalSearchMock,
      });
    });

    createContext({
      globalSearch: "does-not-match",
      setGlobalSearch: setGlobalSearchMock,
    });

    const view = render(
      <MemoryRouter future={{ v7_startTransition: true, v7_relativeSplatPath: true }}>
        <PoliciesPage />
      </MemoryRouter>
    );

    expect(screen.getAllByText("暂无匹配策略")).toHaveLength(2);

    await user.click(screen.getAllByRole("button", { name: "重置" })[0]);

    expect(setGlobalSearchMock).toHaveBeenCalledWith("");

    view.rerender(
      <MemoryRouter future={{ v7_startTransition: true, v7_relativeSplatPath: true }}>
        <PoliciesPage />
      </MemoryRouter>
    );

    expect(screen.getAllByText("每日备份").length).toBeGreaterThan(0);
  });
});
