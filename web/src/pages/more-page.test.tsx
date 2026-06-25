import "@testing-library/jest-dom/vitest";
import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { I18nextProvider } from "react-i18next";
import i18n, { setLanguage } from "@/i18n";
import { MorePage } from "./more-page";
import type { UserRecord } from "@/types/domain";

vi.mock("@/context/auth-context.hooks", () => ({
  useAuth: () => ({ role: "admin" as UserRecord["role"] }),
}));

function renderMorePage() {
  return render(
    <I18nextProvider i18n={i18n}>
      <MemoryRouter initialEntries={["/app/more"]}>
        <MorePage />
      </MemoryRouter>
    </I18nextProvider>
  );
}

describe("MorePage", () => {
  it("renders the translated nav.more title, not the raw key", async () => {
    await setLanguage("zh");
    renderMorePage();

    const heading = screen.getByRole("heading", { level: 1 });
    expect(heading).toHaveTextContent("更多");
    expect(heading).not.toHaveTextContent("nav.more");
  });

  it("renders the translated nav.more title in English", async () => {
    await setLanguage("en");
    renderMorePage();

    const heading = screen.getByRole("heading", { level: 1 });
    expect(heading).toHaveTextContent("More");
    expect(heading).not.toHaveTextContent("nav.more");
  });
});