import "@testing-library/jest-dom/vitest";
import { afterEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { ProtectedRoute } from "./protected-route";

vi.mock("@/context/auth-context.hooks", () => ({
  useAuth: () => ({
    isAuthenticated: false,
  }),
}));

function LoginProbe() {
  const location = useLocation();
  const from = (location.state as { from?: string } | null)?.from ?? "";
  return <div data-testid="redirect-from">{from}</div>;
}

describe("ProtectedRoute", () => {
  afterEach(() => {
    vi.unstubAllEnvs();
  });

  it("preserves the full return path for unauthenticated users", () => {
    render(
      <MemoryRouter
        initialEntries={["/app/settings?tab=users#security"]}

>
        <Routes>
          <Route
            path="/app/settings"
            element={(
              <ProtectedRoute>
                <div>secret</div>
              </ProtectedRoute>
            )}
          />
          <Route path="/login" element={<LoginProbe />} />
        </Routes>
      </MemoryRouter>
    );

    expect(screen.getByTestId("redirect-from")).toHaveTextContent("/app/settings?tab=users#security");
  });

  it("allows unauthenticated access when demo mode is explicitly enabled", () => {
    vi.stubEnv("VITE_ENABLE_DEMO_MODE", "true");

    render(
      <MemoryRouter
        initialEntries={["/app/overview"]}

>
        <Routes>
          <Route
            path="/app/overview"
            element={(
              <ProtectedRoute>
                <div>mock-only console</div>
              </ProtectedRoute>
            )}
          />
          <Route path="/login" element={<LoginProbe />} />
        </Routes>
      </MemoryRouter>
    );

    expect(screen.getByText("mock-only console")).toBeInTheDocument();
  });
});
