import "@testing-library/jest-dom/vitest";
import { beforeEach, describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import i18n, { setLanguage } from "@/i18n";
import { ThemeProvider } from "@/context/theme-context";
import { DENSITY_STORAGE_KEY, POWER_MODE_STORAGE_KEY } from "@/lib/theme";
import { runAxe } from "@/test/a11y-helpers";
import { DisplayPreferencesToggle } from "./display-preferences-toggle";

function renderToggle(className?: string) {
  return render(
    <ThemeProvider>
      <DisplayPreferencesToggle className={className} />
    </ThemeProvider>
  );
}

describe("DisplayPreferencesToggle", () => {
  beforeEach(async () => {
    window.localStorage.removeItem(DENSITY_STORAGE_KEY);
    window.localStorage.removeItem(POWER_MODE_STORAGE_KEY);
    delete document.documentElement.dataset.density;
    delete document.documentElement.dataset.power;
    window.localStorage.setItem("xirang.language", "zh");
    await setLanguage("zh");
  });

  it("renders non-colliding density and power-mode icons with stable toggle names and aria-pressed", () => {
    const { container } = renderToggle("custom-toggle-group");

    expect(container.firstElementChild).toHaveClass("custom-toggle-group");

    const densityButton = screen.getByRole("button", { name: "紧凑密度" });
    expect(densityButton).toHaveAttribute("type", "button");
    expect(densityButton).toHaveAttribute("aria-pressed", "false");
    expect(densityButton).toHaveAttribute("title", "切换紧凑密度");
    const densityIcon = densityButton.querySelector("svg");
    expect(densityIcon).toHaveClass("lucide-rows-3");
    expect(densityIcon).toHaveAttribute("aria-hidden", "true");
    expect(densityButton.querySelector(".lucide-minus, .lucide-plus")).toBeNull();

    const powerButton = screen.getByRole("button", { name: "节能模式" });
    expect(powerButton).toHaveAttribute("type", "button");
    expect(powerButton).toHaveAttribute("aria-pressed", "false");
    expect(powerButton).toHaveAttribute("title", "开启节能模式");
    const powerIcon = powerButton.querySelector("svg");
    expect(powerIcon).toHaveClass("lucide-sparkles");
    expect(powerIcon).toHaveAttribute("aria-hidden", "true");
    expect(powerButton.querySelector(".lucide-zap")).toBeNull();
  });

  it("toggles density between comfortable and compact while keeping stable aria-label", async () => {
    const user = userEvent.setup();
    renderToggle();

    const densityButton = screen.getByRole("button", { name: "紧凑密度" });
    await user.click(densityButton);

    expect(densityButton).toHaveAttribute("aria-pressed", "true");
    expect(densityButton).toHaveAttribute("aria-label", "紧凑密度");
    expect(densityButton).toHaveAttribute("title", "切换舒适密度");
    expect(densityButton.querySelector("svg")).toHaveClass("lucide-rows-4");
    expect(document.documentElement.dataset.density).toBe("compact");

    await user.click(densityButton);

    expect(densityButton).toHaveAttribute("aria-pressed", "false");
    expect(densityButton).toHaveAttribute("aria-label", "紧凑密度");
    expect(densityButton).toHaveAttribute("title", "切换紧凑密度");
    expect(densityButton.querySelector("svg")).toHaveClass("lucide-rows-3");
    expect(document.documentElement.dataset.density).toBe("comfortable");
  });

  it("toggles power-save mode between normal and save while keeping stable aria-label", async () => {
    const user = userEvent.setup();
    renderToggle();

    const powerButton = screen.getByRole("button", { name: "节能模式" });
    await user.click(powerButton);

    expect(powerButton).toHaveAttribute("aria-pressed", "true");
    expect(powerButton).toHaveAttribute("aria-label", "节能模式");
    expect(powerButton).toHaveAttribute("title", "关闭节能模式");
    expect(powerButton.querySelector("svg")).toHaveClass("lucide-leaf");
    expect(document.documentElement.dataset.power).toBe("save");

    await user.click(powerButton);

    expect(powerButton).toHaveAttribute("aria-pressed", "false");
    expect(powerButton).toHaveAttribute("aria-label", "节能模式");
    expect(powerButton).toHaveAttribute("title", "开启节能模式");
    expect(powerButton.querySelector("svg")).toHaveClass("lucide-sparkles");
    expect(document.documentElement.dataset.power).toBe("normal");
  });

  it("renders stable translated English toggle names when language is en", async () => {
    await setLanguage("en");
    window.localStorage.setItem(DENSITY_STORAGE_KEY, "compact");
    window.localStorage.setItem(POWER_MODE_STORAGE_KEY, "save");

    renderToggle();

    expect(i18n.language).toBe("en");
    const densityButton = screen.getByRole("button", { name: "Compact density" });
    expect(densityButton).toHaveAttribute("aria-pressed", "true");
    expect(densityButton).toHaveAttribute("title", "Switch to comfortable density");
    expect(densityButton.querySelector("svg")).toHaveClass("lucide-rows-4");

    const powerButton = screen.getByRole("button", { name: "Power-saving mode" });
    expect(powerButton).toHaveAttribute("aria-pressed", "true");
    expect(powerButton).toHaveAttribute("title", "Disable power-saving mode");
    expect(powerButton.querySelector("svg")).toHaveClass("lucide-leaf");
  });

  it("has no axe accessibility violations", async () => {
    const { container } = renderToggle();
    expect(await runAxe(container)).toHaveNoViolations();
  });
});
