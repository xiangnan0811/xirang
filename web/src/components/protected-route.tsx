import type { PropsWithChildren } from "react";
import { Navigate, useLocation } from "react-router-dom";
import { useAuth } from "@/context/auth-context.hooks";
import { buildReturnPath } from "@/lib/api/core";

export function ProtectedRoute({ children }: PropsWithChildren) {
  const { isAuthenticated } = useAuth();
  const location = useLocation();

  if (!isAuthenticated && !(import.meta.env.VITE_ENABLE_DEMO_MODE === "true" && import.meta.env.DEV)) {
    return <Navigate to="/login" replace state={{ from: buildReturnPath(location) }} />;
  }

  return <>{children}</>;
}
