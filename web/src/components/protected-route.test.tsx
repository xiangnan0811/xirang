import "@testing-library/jest-dom/vitest";
import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { ProtectedRoute } from "./protected-route";

vi.mock("@/context/auth-context", () => ({
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
  it("preserves the full return path for unauthenticated users", () => {
    render(
      <MemoryRouter
        initialEntries={["/app/settings?tab=users#security"]}
        future={{ v7_startTransition: true, v7_relativeSplatPath: true }}
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
});
