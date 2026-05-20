import { createContext } from "react";

export type AuthRole = "admin" | "operator" | "viewer";

export type StoredAuthState = {
  token: string | null;
  username: string | null;
  role: AuthRole | null;
  userId: number | null;
  totpEnabled: boolean;
};

export type AuthContextValue = {
  token: string | null;
  username: string | null;
  role: AuthRole | null;
  userId: number | null;
  totpEnabled: boolean;
  isAuthenticated: boolean;
  login: (
    token: string,
    username: string,
    role?: AuthRole,
    userId?: number,
    totpEnabled?: boolean
  ) => void;
  logout: () => void;
  setTotpEnabled: (enabled: boolean) => void;
  ensureStepUpProof: () => Promise<string>;
  clearStepUpProof: () => void;
};

export const AuthContext = createContext<AuthContextValue | null>(null);
