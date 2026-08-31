import "@testing-library/jest-dom/vitest";
import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { I18nextProvider } from "react-i18next";
import i18n, { setLanguage } from "@/i18n";
import { PageLoader } from "./router-pages";

describe("PageLoader", () => {
  it("exposes a localized busy status", async () => {
    await setLanguage("en");
    render(
      <I18nextProvider i18n={i18n}>
        <PageLoader />
      </I18nextProvider>,
    );

    const status = screen.getByRole("status");
    expect(status).toHaveAttribute("aria-busy", "true");
    expect(status).toHaveAccessibleName(/Loading/);
  });
});
