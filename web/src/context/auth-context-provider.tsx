import {
  useCallback,
  useMemo,
  useRef,
  useState,
  type FormEvent,
  type PropsWithChildren
} from "react";
import { Button } from "@/components/ui/button";
import { Dialog, DialogBody, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import {
  AuthContext,
  type AuthContextValue,
  type AuthRole,
} from "@/context/auth-context.shared";
import i18n from "@/i18n";
import { apiClient } from "@/lib/api/client";
import { ApiError } from "@/lib/api/core";
import { clearStepUpProof as clearStoredStepUpProof, readStepUpProof, saveStepUpProof } from "@/lib/step-up-storage";

const AUTH_TOKEN_KEY = "xirang-auth-token";
const AUTH_USERNAME_KEY = "xirang-username";
const AUTH_ROLE_KEY = "xirang-role";
const AUTH_USER_ID_KEY = "xirang-user-id";
const AUTH_TOTP_ENABLED_KEY = "xirang-totp-enabled";

type StoredAuthState = {
  token: string | null;
  username: string | null;
  role: AuthRole | null;
  userId: number | null;
  totpEnabled: boolean;
};

type PendingStepUpRequest = {
  resolve: (proof: string) => void;
  reject: (error: Error) => void;
  persist: boolean;
};

function getSessionStorage() {
  if (typeof window === "undefined") {
    return null;
  }
  return window.sessionStorage;
}

function getLocalStorage() {
  if (typeof window === "undefined") {
    return null;
  }
  return window.localStorage;
}

function safeGetItem(storage: Storage | null, key: string) {
  try {
    return storage?.getItem(key) ?? null;
  } catch {
    return null;
  }
}

function safeSetItem(storage: Storage | null, key: string, value: string) {
  try {
    storage?.setItem(key, value);
  } catch {
    // ignore
  }
}

function safeRemoveItem(storage: Storage | null, key: string) {
  try {
    storage?.removeItem(key);
  } catch {
    // ignore
  }
}

function parseAuthRole(role: string | null): AuthRole | null {
  return role === "admin" || role === "operator" || role === "viewer" ? role : null;
}

function readStoredAuthState(): StoredAuthState {
  const sessionStorageRef = getSessionStorage();
  const localStorageRef = getLocalStorage();

  const sessionToken = safeGetItem(sessionStorageRef, AUTH_TOKEN_KEY);
  const sessionUsername = safeGetItem(sessionStorageRef, AUTH_USERNAME_KEY);
  const sessionRole = safeGetItem(sessionStorageRef, AUTH_ROLE_KEY);
  const sessionUserID = safeGetItem(sessionStorageRef, AUTH_USER_ID_KEY);
  const sessionTotpEnabled = safeGetItem(sessionStorageRef, AUTH_TOTP_ENABLED_KEY);
  if (sessionToken) {
    const parsedUserID = sessionUserID ? Number.parseInt(sessionUserID, 10) : Number.NaN;
    return {
      token: sessionToken,
      username: sessionUsername,
      role: parseAuthRole(sessionRole),
      userId: Number.isFinite(parsedUserID) && parsedUserID > 0 ? parsedUserID : null,
      totpEnabled: sessionTotpEnabled === "true"
    };
  }
  safeRemoveItem(sessionStorageRef, AUTH_USERNAME_KEY);
  safeRemoveItem(sessionStorageRef, AUTH_ROLE_KEY);
  safeRemoveItem(sessionStorageRef, AUTH_USER_ID_KEY);
  safeRemoveItem(sessionStorageRef, AUTH_TOTP_ENABLED_KEY);

  const legacyToken = safeGetItem(localStorageRef, AUTH_TOKEN_KEY);
  const legacyUsername = safeGetItem(localStorageRef, AUTH_USERNAME_KEY);
  const legacyRole = safeGetItem(localStorageRef, AUTH_ROLE_KEY);
  const legacyUserID = safeGetItem(localStorageRef, AUTH_USER_ID_KEY);

  if (legacyToken) {
    safeSetItem(sessionStorageRef, AUTH_TOKEN_KEY, legacyToken);
    if (legacyUsername) {
      safeSetItem(sessionStorageRef, AUTH_USERNAME_KEY, legacyUsername);
    }
    if (legacyRole) {
      safeSetItem(sessionStorageRef, AUTH_ROLE_KEY, legacyRole);
    }
    if (legacyUserID) {
      safeSetItem(sessionStorageRef, AUTH_USER_ID_KEY, legacyUserID);
    }
  }

  safeRemoveItem(localStorageRef, AUTH_TOKEN_KEY);
  safeRemoveItem(localStorageRef, AUTH_USERNAME_KEY);
  safeRemoveItem(localStorageRef, AUTH_ROLE_KEY);
  safeRemoveItem(localStorageRef, AUTH_USER_ID_KEY);

  const parsedLegacyUserID = legacyUserID ? Number.parseInt(legacyUserID, 10) : Number.NaN;
  const parsedLegacyRole = parseAuthRole(legacyRole);

  return {
    token: legacyToken,
    username: legacyToken ? legacyUsername : null,
    role: legacyToken ? parsedLegacyRole : null,
    userId: legacyToken && Number.isFinite(parsedLegacyUserID) && parsedLegacyUserID > 0 ? parsedLegacyUserID : null,
    totpEnabled: false
  };
}

export function AuthProvider({ children }: PropsWithChildren) {
  const [{ token, username, role, userId, totpEnabled }, setAuthState] = useState<StoredAuthState>(() => readStoredAuthState());
  const pendingStepUpRef = useRef<PendingStepUpRequest | null>(null);
  const [stepUpDialogOpen, setStepUpDialogOpen] = useState(false);
  const [stepUpCode, setStepUpCode] = useState("");
  const [stepUpError, setStepUpError] = useState<string | null>(null);
  const [stepUpSubmitting, setStepUpSubmitting] = useState(false);

  const login = useCallback((
    nextToken: string,
    nextUsername: string,
    nextRole?: AuthRole,
    nextUserID?: number,
    nextTotpEnabled?: boolean
  ) => {
    const sessionStorageRef = getSessionStorage();
    const localStorageRef = getLocalStorage();
    clearStoredStepUpProof();
    const validUserId = typeof nextUserID === "number" && Number.isFinite(nextUserID) && nextUserID > 0
      ? nextUserID
      : null;
    const totpEnabledValue = nextTotpEnabled ?? false;

    safeSetItem(sessionStorageRef, AUTH_TOKEN_KEY, nextToken);
    safeSetItem(sessionStorageRef, AUTH_USERNAME_KEY, nextUsername);
    if (nextRole) {
      safeSetItem(sessionStorageRef, AUTH_ROLE_KEY, nextRole);
    } else {
      safeRemoveItem(sessionStorageRef, AUTH_ROLE_KEY);
    }
    if (validUserId !== null) {
      safeSetItem(sessionStorageRef, AUTH_USER_ID_KEY, String(validUserId));
    } else {
      safeRemoveItem(sessionStorageRef, AUTH_USER_ID_KEY);
    }
    safeSetItem(sessionStorageRef, AUTH_TOTP_ENABLED_KEY, String(totpEnabledValue));
    safeRemoveItem(localStorageRef, AUTH_TOKEN_KEY);
    safeRemoveItem(localStorageRef, AUTH_USERNAME_KEY);
    safeRemoveItem(localStorageRef, AUTH_ROLE_KEY);
    safeRemoveItem(localStorageRef, AUTH_USER_ID_KEY);

    setAuthState({
      token: nextToken,
      username: nextUsername,
      role: nextRole ?? null,
      userId: validUserId,
      totpEnabled: totpEnabledValue
    });
  }, []);

  const logout = useCallback(() => {
    const sessionStorageRef = getSessionStorage();
    const localStorageRef = getLocalStorage();

    safeRemoveItem(sessionStorageRef, AUTH_TOKEN_KEY);
    safeRemoveItem(sessionStorageRef, AUTH_USERNAME_KEY);
    safeRemoveItem(sessionStorageRef, AUTH_ROLE_KEY);
    safeRemoveItem(sessionStorageRef, AUTH_USER_ID_KEY);
    safeRemoveItem(sessionStorageRef, AUTH_TOTP_ENABLED_KEY);
    clearStoredStepUpProof();
    safeRemoveItem(localStorageRef, AUTH_TOKEN_KEY);
    safeRemoveItem(localStorageRef, AUTH_USERNAME_KEY);
    safeRemoveItem(localStorageRef, AUTH_ROLE_KEY);
    safeRemoveItem(localStorageRef, AUTH_USER_ID_KEY);

    setAuthState({ token: null, username: null, role: null, userId: null, totpEnabled: false });
  }, []);

  const setTotpEnabled = useCallback((enabled: boolean) => {
    const sessionStorageRef = getSessionStorage();
    safeSetItem(sessionStorageRef, AUTH_TOTP_ENABLED_KEY, String(enabled));
    if (!enabled) {
      clearStoredStepUpProof();
    }
    setAuthState((prev) => ({ ...prev, totpEnabled: enabled }));
  }, []);

  const clearStepUpProof = useCallback(() => {
    clearStoredStepUpProof();
  }, []);

  const ensureStepUpProof = useCallback(async (options: { persist?: boolean; reuseCached?: boolean } = {}): Promise<string> => {
    const persist = options.persist ?? true;
    const reuseCached = options.reuseCached ?? persist;
    if (reuseCached) {
      const cached = readStepUpProof();
      if (cached) {
        return cached.proof;
      }
    }
    if (!persist) {
      clearStoredStepUpProof();
    }
    if (!token) {
      throw new Error(i18n.t("stepUp.loginRequired"));
    }
    if (!totpEnabled) {
      throw new Error(i18n.t("stepUp.totpRequired"));
    }
    if (pendingStepUpRef.current) {
      pendingStepUpRef.current.reject(new Error(i18n.t("stepUp.alreadyOpen")));
      pendingStepUpRef.current = null;
    }
    setStepUpCode("");
    setStepUpError(null);
    setStepUpDialogOpen(true);
    return new Promise<string>((resolve, reject) => {
      pendingStepUpRef.current = { resolve, reject, persist };
    });
  }, [token, totpEnabled]);

  const closeStepUpDialog = useCallback(() => {
    if (stepUpSubmitting) {
      return;
    }
    pendingStepUpRef.current?.reject(new Error(i18n.t("stepUp.cancelled")));
    pendingStepUpRef.current = null;
    setStepUpDialogOpen(false);
    setStepUpCode("");
    setStepUpError(null);
  }, [stepUpSubmitting]);

  const handleStepUpSubmit = useCallback(async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!token || !pendingStepUpRef.current) {
      return;
    }
    const code = stepUpCode.trim();
    if (!code) {
      setStepUpError(i18n.t("stepUp.codeRequired"));
      return;
    }
    setStepUpSubmitting(true);
    setStepUpError(null);
    try {
      const response = await apiClient.requestStepUpProof(token, code);
      const shouldPersistProof = pendingStepUpRef.current.persist;
      if (shouldPersistProof) {
        const expiresAt = Date.parse(response.expires_at);
        const ttlMillis = Number(response.proof_ttl_seconds || 0) * 1000;
        const fallbackExpiresAt = ttlMillis > 0 ? Date.now() + ttlMillis : Date.now();
        const proofExpiresAt = Number.isFinite(expiresAt) ? expiresAt : fallbackExpiresAt;
        saveStepUpProof(response.proof, proofExpiresAt);
      }
      pendingStepUpRef.current.resolve(response.proof);
      pendingStepUpRef.current = null;
      setStepUpDialogOpen(false);
      setStepUpCode("");
    } catch (error) {
      const message = error instanceof ApiError ? error.message : i18n.t("stepUp.verifyFailed");
      setStepUpError(message || i18n.t("stepUp.verifyFailed"));
    } finally {
      setStepUpSubmitting(false);
    }
  }, [stepUpCode, token]);

  const value = useMemo<AuthContextValue>(
    () => ({
      token,
      username,
      role,
      userId,
      totpEnabled,
      isAuthenticated: Boolean(token),
      login,
      logout,
      setTotpEnabled,
      ensureStepUpProof,
      clearStepUpProof
    }),
    [login, logout, role, token, userId, username, totpEnabled, setTotpEnabled, ensureStepUpProof, clearStepUpProof]
  );

  return (
    <AuthContext.Provider value={value}>
      {children}
      <Dialog open={stepUpDialogOpen} onOpenChange={(open) => {
        if (!open) {
          closeStepUpDialog();
        }
      }}>
        <DialogContent size="sm">
          <form onSubmit={handleStepUpSubmit}>
            <DialogHeader>
              <DialogTitle>{i18n.t("stepUp.title")}</DialogTitle>
              <DialogDescription>{i18n.t("stepUp.description")}</DialogDescription>
            </DialogHeader>
            <DialogBody className="space-y-3">
              <div className="space-y-1.5">
                <label className="text-sm font-medium" htmlFor="step-up-code">
                  {i18n.t("stepUp.codeLabel")}
                </label>
                <Input
                  id="step-up-code"
                  value={stepUpCode}
                  onChange={(event) => setStepUpCode(event.target.value)}
                  inputMode="numeric"
                  pattern="[0-9]*"
                  autoComplete="one-time-code"
                  placeholder={i18n.t("stepUp.codePlaceholder")}
                  disabled={stepUpSubmitting}
                />
              </div>
              {stepUpError ? (
                <p className="rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs text-destructive" role="alert">
                  {stepUpError}
                </p>
              ) : null}
            </DialogBody>
            <DialogFooter>
              <Button type="button" variant="outline" onClick={closeStepUpDialog} disabled={stepUpSubmitting}>
                {i18n.t("common.cancel")}
              </Button>
              <Button type="submit" loading={stepUpSubmitting}>
                {i18n.t("stepUp.verify")}
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>
    </AuthContext.Provider>
  );
}
