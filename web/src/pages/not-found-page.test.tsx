import "@testing-library/jest-dom/vitest";
import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { I18nextProvider } from "react-i18next";
import i18n from "@/i18n";
import { NotFoundPage } from "./not-found-page";

describe("NotFoundPage", () => {
  it("renders one heading and a home link without redirecting", () => {
    render(
      <I18nextProvider i18n={i18n}>
        <MemoryRouter initialEntries={["/this-does-not-exist"]}>
          <NotFoundPage />
        </MemoryRouter>
      </I18nextProvider>,
    );

    expect(screen.getAllByRole("heading", { level: 1 })).toHaveLength(1);
    expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent(/Page not found|页面不存在/);
    expect(screen.getByRole("link", { name: /Back to Overview|返回概览/ })).toHaveAttribute(
      "href",
      "/app/overview",
    );
  });
});
