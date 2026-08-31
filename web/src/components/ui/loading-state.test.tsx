import "@testing-library/jest-dom/vitest";
import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { I18nextProvider } from "react-i18next";
import i18n, { setLanguage } from "@/i18n";
import { LoadingState } from "./loading-state";

describe("LoadingState", () => {
  it("announces a localized status without hardcoded English", async () => {
    await setLanguage("zh");
    render(
      <I18nextProvider i18n={i18n}>
        <LoadingState />
      </I18nextProvider>,
    );

    const status = screen.getByRole("status");
    expect(status).toHaveAttribute("aria-busy", "true");
    expect(status).toHaveTextContent("加载中...");
    expect(status).not.toHaveAttribute("aria-label", "Loading");
    expect(status.textContent).not.toBe("Loading");
  });
});
