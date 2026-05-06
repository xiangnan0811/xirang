import "@testing-library/jest-dom/vitest";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { CommandPalette } from "./command-palette";

vi.mock("@/context/command-palette-context", () => ({
  useCommandPalette: () => ({
    open: true,
    setOpen: vi.fn(),
  }),
}));

vi.mock("@/context/nodes-context", () => ({
  useNodesContextOptional: () => null,
}));

vi.mock("@/context/tasks-context", () => ({
  useTasksContextOptional: () => null,
}));

vi.mock("react-i18next", () => ({
  initReactI18next: { type: "3rdParty", init: vi.fn() },
  useTranslation: () => ({
    t: (key: string) => {
      const labels: Record<string, string> = {
        "dashboards.pageTitle": "Dashboards",
        "nav.credentials": "Credentials",
        "nav.automationRules": "Automation Rules",
        "nav.serviceMonitors": "Service Monitors",
        "search.placeholder": "Search",
        "search.title": "Search",
        "search.description": "Search and open console pages, nodes, or tasks.",
        "search.emptyResults": "No results",
        "search.navigation": "Navigation",
        "search.kbd": "⌘K",
      };
      return labels[key] ?? key;
    },
    i18n: { language: "en", changeLanguage: vi.fn() },
  }),
}));

describe("CommandPalette", () => {
  beforeEach(() => {
    Element.prototype.scrollIntoView = vi.fn();
  });

  it("renders navigation from the canonical nav registry", () => {
    render(
      <MemoryRouter>
        <CommandPalette />
      </MemoryRouter>
    );

    expect(screen.getByText("Dashboards")).toBeInTheDocument();
    expect(screen.getByText("Credentials")).toBeInTheDocument();
    expect(screen.getByText("Automation Rules")).toBeInTheDocument();
    expect(screen.getByText("Service Monitors")).toBeInTheDocument();
  });
});
