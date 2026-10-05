import "@testing-library/jest-dom/vitest";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter, useLocation } from "react-router-dom";
import { CommandPalette } from "./command-palette";

const { useAuthMock } = vi.hoisted(() => ({
  useAuthMock: vi.fn<
    () => { role: "admin" | "operator" | "viewer" | null }
  >(() => ({ role: "admin" })),
}));

vi.mock("@/context/auth-context.hooks", () => ({
  useAuth: () => useAuthMock(),
}));

vi.mock("@/context/command-palette-context.hooks", () => ({
  useCommandPalette: () => ({
    open: true,
    setOpen: vi.fn(),
  }),
}));

vi.mock("@/context/nodes-context.hooks", () => ({
  useNodesContextOptional: () => null,
}));

vi.mock("@/context/tasks-context.hooks", () => ({
  useTasksContextOptional: () => null,
}));

vi.mock("react-i18next", () => ({
  initReactI18next: { type: "3rdParty", init: vi.fn() },
  useTranslation: () => ({
    t: (key: string) => {
      const labels: Record<string, string> = {
        "nav.tasks": "Tasks",
        "nav.credentials": "Credentials",
        "nav.automationRules": "Automation Rules",
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

function LocationProbe() {
  return <output data-testid="location">{useLocation().pathname}</output>;
}

describe("CommandPalette", () => {
  beforeEach(() => {
    Element.prototype.scrollIntoView = vi.fn();
    useAuthMock.mockReturnValue({ role: "admin" });
  });

  it("renders navigation from the canonical nav registry", () => {
    render(
      <MemoryRouter>
        <CommandPalette />
      </MemoryRouter>
    );

    expect(screen.getByText("Tasks")).toBeInTheDocument();
    expect(screen.queryByText("Dashboards")).not.toBeInTheDocument();
    expect(screen.getByText("Credentials")).toBeInTheDocument();
    expect(screen.getByText("Automation Rules")).toBeInTheDocument();
  });

  it("hides admin-only navigation for non-admin roles", () => {
    useAuthMock.mockReturnValue({ role: "operator" });

    render(
      <MemoryRouter>
        <CommandPalette />
      </MemoryRouter>
    );

    expect(screen.queryByText("Credentials")).not.toBeInTheDocument();
    expect(screen.queryByText("Automation Rules")).not.toBeInTheDocument();
  });

  it.each(["viewer", null] as const)("opens backup evidence for %s without offering audit", (role) => {
    useAuthMock.mockReturnValue({ role });
    render(
      <MemoryRouter>
        <CommandPalette />
        <LocationProbe />
      </MemoryRouter>
    );
    expect(screen.queryByText("nav.audit")).not.toBeInTheDocument();
    fireEvent.click(screen.getByText("nav.backups"));
    expect(screen.getByTestId("location")).toHaveTextContent("/app/backups/overview");
  });
});
