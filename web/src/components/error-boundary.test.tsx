import "@testing-library/jest-dom/vitest";
import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { I18nextProvider } from "react-i18next";
import i18n from "@/i18n";
import { ErrorBoundary } from "./error-boundary";

function Boom(): ReactNode {
  throw new Error("secret internal stack marker");
}

describe("ErrorBoundary", () => {
  it("shows a page-level heading and generic copy without the raw error", () => {
    const spy = vi.spyOn(console, "error").mockImplementation(() => {});

    render(
      <I18nextProvider i18n={i18n}>
        <ErrorBoundary>
          <Boom />
        </ErrorBoundary>
      </I18nextProvider>,
    );

    expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent(/Page render error|页面渲染出错/);
    expect(screen.getByText(/Something went wrong|此页面渲染时出错/)).toBeInTheDocument();
    expect(screen.queryByText("secret internal stack marker")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Retry|重试/ })).toBeInTheDocument();

    spy.mockRestore();
  });
});
