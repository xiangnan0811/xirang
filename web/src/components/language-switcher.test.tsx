import "@testing-library/jest-dom/vitest";
import { describe, expect, it, beforeEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import i18n from "@/i18n";
import { LanguageSwitcher } from "./language-switcher";

describe("LanguageSwitcher", () => {
  beforeEach(async () => {
    window.localStorage.setItem("xirang.language", "zh");
    await i18n.changeLanguage("zh");
  });

  it("默认中文时显示 EN 按钮", () => {
    render(<LanguageSwitcher />);
    expect(screen.getByText("EN")).toBeInTheDocument();
    expect(screen.getByRole("button")).toHaveAttribute("aria-label", "Switch to English");
  });

  it("点击切换到英文后显示中文按钮", async () => {
    const user = userEvent.setup();
    render(<LanguageSwitcher />);

    await user.click(screen.getByRole("button"));

    await waitFor(() => {
      expect(screen.getByText("中")).toBeInTheDocument();
    });
    expect(screen.getByRole("button")).toHaveAttribute("aria-label", "切换到中文");
    expect(window.localStorage.getItem("xirang.language")).toBe("en");
  });

  it("再次点击切换回中文", async () => {
    const user = userEvent.setup();
    render(<LanguageSwitcher />);

    await user.click(screen.getByRole("button")); // zh -> en
    await waitFor(() => expect(screen.getByText("中")).toBeInTheDocument());

    await user.click(screen.getByRole("button")); // en -> zh
    await waitFor(() => expect(screen.getByText("EN")).toBeInTheDocument());

    expect(window.localStorage.getItem("xirang.language")).toBe("zh");
  });
});
