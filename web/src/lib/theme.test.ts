import { describe, expect, it } from "vitest";
import {
  applyThemeClass,
  persistTheme,
  resolveInitialTheme,
  type ThemeMode
} from "./theme";

describe("theme helpers", () => {
  it("优先使用已持久化主题", () => {
    const theme = resolveInitialTheme("dark", false);
    expect(theme).toBe<ThemeMode>("dark");
  });

  it("没有持久化值时根据系统主题回落", () => {
    const theme = resolveInitialTheme(null, true);
    expect(theme).toBe<ThemeMode>("dark");
  });

  it("切换时写入 localStorage 并更新根节点 class", () => {
    const root = document.documentElement;
    persistTheme("dark");
    applyThemeClass(root, "dark");

    expect(localStorage.getItem("xirang-theme")).toBe("dark");
    expect(root.classList.contains("dark")).toBe(true);

    applyThemeClass(root, "light");
    expect(root.classList.contains("dark")).toBe(false);
  });
});
