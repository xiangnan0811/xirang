import { createContext } from "react";
import type { StepUpAction } from "@/lib/api/totp-api";

export type AuthRole = "admin" | "operator" | "viewer";

export type TOTPActivationUser = {
  id: number;
  username: string;
  role: AuthRole;
  totpEnabled: true;
};

export type StoredAuthState = {
  token: string | null;
  username: string | null;
  role: AuthRole | null;
  userId: number | null;
  totpEnabled: boolean;
};

export type StepUpProofOptions = {
  persist?: boolean;
  reuseCached?: boolean;
};

export type AuthContextValue = {
  token: string | null;
  username: string | null;
  role: AuthRole | null;
  userId: number | null;
  totpEnabled: boolean;
  isAuthenticated: boolean;
  authTransitioning: boolean;
  beginTOTPActivation: () => number;
  abortTOTPActivation: (id: number) => void;
  completeTOTPActivation: (id: number, token: string, user: TOTPActivationUser) => boolean;
  login: (
    token: string,
    username: string,
    role?: AuthRole,
    userId?: number,
    totpEnabled?: boolean
  ) => void;
  logout: () => void;
  setTotpEnabled: (enabled: boolean) => void;
  ensureStepUpProof: (action: StepUpAction, options?: StepUpProofOptions) => Promise<string>;
  clearStepUpProof: (action?: StepUpAction) => void;
};

export const AuthContext = createContext<AuthContextValue | null>(null);
