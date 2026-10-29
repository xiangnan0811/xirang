import "@testing-library/jest-dom/vitest";
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import * as i18nModule from "@/i18n";

const REFRESH_INTERVAL_KEY = "xirang.settings.refresh-interval";

/**
 * Simulates a page reload: the preference store reads localStorage only when its module
 * initializes. `undefined` keeps whatever is currently stored.
 */
async function renderPersonalTabAfterReload(persisted?: string | null) {
  cleanup();
  if (persisted === null) {
    localStorage.removeItem(REFRESH_INTERVAL_KEY);
  } else if (persisted !== undefined) {
    localStorage.setItem(REFRESH_INTERVAL_KEY, persisted);
  }
  vi.resetModules();
  // Keep the single real i18next instance initialized by the test setup.
  vi.doMock("@/i18n", () => i18nModule);
  const [{ PersonalTab }, { ThemeProvider }] = await Promise.all([
    import("./settings-page.personal"),
    import("@/context/theme-context"),
  ]);
  render(
    <ThemeProvider>
      <PersonalTab />
    </ThemeProvider>,
  );
  return screen.getByRole("combobox", { name: i18nModule.default.t("settings.personal.refreshInterval") });
}

describe("PersonalTab auto refresh interval", () => {
  afterEach(() => {
    cleanup();
    vi.doUnmock("@/i18n");
    localStorage.removeItem(REFRESH_INTERVAL_KEY);
  });

  it("echoes Off after selecting it and after a reload", async () => {
    const user = userEvent.setup();
    const select = await renderPersonalTabAfterReload("30");
    expect(select).toHaveValue("30");

    await user.selectOptions(select, "0");
    expect(select).toHaveValue("0");
    expect(localStorage.getItem(REFRESH_INTERVAL_KEY)).toBe("0");

    expect(await renderPersonalTabAfterReload()).toHaveValue("0");
  });

  it("restores a non-zero interval chosen after Off across a reload", async () => {
    const user = userEvent.setup();
    const select = await renderPersonalTabAfterReload("0");

    await user.selectOptions(select, "120");
    expect(select).toHaveValue("120");

    expect(await renderPersonalTabAfterReload()).toHaveValue("120");
  });

  it.each([null, "", "abc", "-30", "0abc", "0.5", "-0.5", "-0", " 0", "30s", "1e3", "2147484"])("shows the 60s default for missing or invalid stored value %j", async (stored) => {
    expect(await renderPersonalTabAfterReload(stored)).toHaveValue("60");
  });
});
