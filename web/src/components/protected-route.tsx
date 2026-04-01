import type { PropsWithChildren } from "react";
import { Navigate, useLocation } from "react-router-dom";
import { useAuth } from "@/context/auth-context";
import { buildReturnPath } from "@/lib/api/core";

export function ProtectedRoute({ children }: PropsWithChildren) {
  const { isAuthenticated } = useAuth();
  const location = useLocation();

  if (!isAuthenticated) {
    return <Navigate to="/login" replace state={{ from: buildReturnPath(location) }} />;
  }

  return <>{children}</>;
}
