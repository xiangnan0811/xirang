import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  useSyncExternalStore,
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
  type StepUpProofOptions,
  type TOTPActivationUser,
} from "@/context/auth-context.shared";
import i18n from "@/i18n";
import { apiClient } from "@/lib/api/client";
import {
  ApiError,
  AuthTransitionRejectedError,
  beginAuthTransitionBarrier,
  bumpAuthSessionGeneration,
  clearAuthTransitionBarrier,
  dismissTOTPActivationFailure,
  getAuthSessionGeneration,
  isAuthTransitionActive,
  publishTOTPActivationFailure,
  rememberAuthIdentity,
  readTOTPActivationFailure,
  releaseAuthTransitionBarrier,
  subscribeAuthTransition,
  subscribeTOTPActivationFailure,
} from "@/lib/api/core";
import type { StepUpAction } from "@/lib/api/totp-api";
import { assertStepUpPrerequisite } from "@/lib/step-up-prerequisite";
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
  promise: Promise<string>;
  persist: boolean;
  action: StepUpAction;
  authGeneration: number;
  /** Attempt owner; null keeps the historical shared, non-cancellable-while-submitting semantics. */
  ownerSignal: AbortSignal | null;
  /** Aborts this pending request's proof POST; only owned requests pass it to the transport. */
  submitController: AbortController | null;
  /** Detaches the owner abort listener; idempotent. */
  releaseOwner: () => void;
};

function stepUpAbortError(): DOMException {
  return new DOMException("Step-up proof aborted", "AbortError");
}

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

function isActivationUser(user: TOTPActivationUser): boolean {
  return Number.isInteger(user.id)
    && user.id > 0
    && typeof user.username === "string"
    && user.username.trim() !== ""
    && (user.role === "admin" || user.role === "operator" || user.role === "viewer")
    && user.totpEnabled === true;
}

function persistActivationSession(nextToken: string, user: TOTPActivationUser): boolean {
  const storage = getSessionStorage();
  if (!storage || nextToken.trim() === "") {
    return false;
  }
  try {
    storage.setItem(AUTH_TOKEN_KEY, nextToken);
    storage.setItem(AUTH_USERNAME_KEY, user.username);
    storage.setItem(AUTH_ROLE_KEY, user.role);
    storage.setItem(AUTH_USER_ID_KEY, String(user.id));
    storage.setItem(AUTH_TOTP_ENABLED_KEY, "true");
    return storage.getItem(AUTH_TOKEN_KEY) === nextToken
      && storage.getItem(AUTH_USERNAME_KEY) === user.username
      && storage.getItem(AUTH_ROLE_KEY) === user.role
      && storage.getItem(AUTH_USER_ID_KEY) === String(user.id)
      && storage.getItem(AUTH_TOTP_ENABLED_KEY) === "true";
  } catch {
    return false;
  }
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
  const [authState, setAuthState] = useState<StoredAuthState>(() => {
    const stored = readStoredAuthState();
    rememberAuthIdentity(stored.token, stored.role);
    return stored;
  });
  const { token, totpEnabled } = authState;
  const pendingStepUpRef = useRef<PendingStepUpRequest | null>(null);
  const stepUpSubmitAttemptRef = useRef(0);
  const activationOwnerRef = useRef<number | null>(null);
  const [stepUpDialogOpen, setStepUpDialogOpen] = useState(false);
  const [stepUpCode, setStepUpCode] = useState("");
  const [stepUpError, setStepUpError] = useState<string | null>(null);
  const [stepUpSubmitting, setStepUpSubmitting] = useState(false);
  const [stepUpOwned, setStepUpOwned] = useState(false);
  const authTransitioning = useSyncExternalStore(subscribeAuthTransition, isAuthTransitionActive, () => false);
  const activationFailure = useSyncExternalStore(subscribeTOTPActivationFailure, readTOTPActivationFailure, () => null);

  // Single cleanup for every pending exit except success: detach the owner, abort its in-flight POST,
  // invalidate the submit attempt so a stale finally cannot touch a successor, then reset the dialog.
  const dropPendingStepUp = useCallback((error: Error) => {
    const pending = pendingStepUpRef.current;
    pendingStepUpRef.current = null;
    stepUpSubmitAttemptRef.current += 1;
    if (pending) {
      pending.releaseOwner();
      pending.submitController?.abort();
      pending.reject(error);
    }
    setStepUpDialogOpen(false);
    setStepUpCode("");
    setStepUpError(null);
    setStepUpSubmitting(false);
    setStepUpOwned(false);
  }, []);

  useEffect(() => () => dropPendingStepUp(stepUpAbortError()), [dropPendingStepUp]);

  const login = useCallback((
    nextToken: string,
    nextUsername: string,
    nextRole?: AuthRole,
    nextUserID?: number,
    nextTotpEnabled?: boolean
  ) => {
    activationOwnerRef.current = null;
    clearAuthTransitionBarrier();
    const sessionStorageRef = getSessionStorage();
    const localStorageRef = getLocalStorage();
    bumpAuthSessionGeneration();
    dropPendingStepUp(new Error(i18n.t("stepUp.loginRequired")));
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

    const nextRoleValue = nextRole ?? null;
    rememberAuthIdentity(nextToken, nextRoleValue);
    setAuthState({
      token: nextToken,
      username: nextUsername,
      role: nextRoleValue,
      userId: validUserId,
      totpEnabled: totpEnabledValue
    });
  }, [dropPendingStepUp]);

  const logout = useCallback(() => {
    const sessionStorageRef = getSessionStorage();
    const localStorageRef = getLocalStorage();
    activationOwnerRef.current = null;
    clearAuthTransitionBarrier();

    bumpAuthSessionGeneration();
    dropPendingStepUp(new Error(i18n.t("stepUp.loginRequired")));
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

    rememberAuthIdentity(null, null);
    setAuthState({ token: null, username: null, role: null, userId: null, totpEnabled: false });
  }, [dropPendingStepUp]);

  const beginTOTPActivation = useCallback((): number => {
    if (activationOwnerRef.current !== null || isAuthTransitionActive()) {
      throw new AuthTransitionRejectedError();
    }
    const id = beginAuthTransitionBarrier();
    activationOwnerRef.current = id;
    bumpAuthSessionGeneration();
    dropPendingStepUp(new Error(i18n.t("stepUp.loginRequired")));
    clearStoredStepUpProof();
    return id;
  }, [dropPendingStepUp]);

  const abortTOTPActivation = useCallback((id: number) => {
    if (activationOwnerRef.current !== id) {
      return;
    }
    activationOwnerRef.current = null;
    releaseAuthTransitionBarrier(id);
  }, []);

  const completeTOTPActivation = useCallback((id: number, nextToken: string, user: TOTPActivationUser): boolean => {
    if (activationOwnerRef.current !== id) {
      return false;
    }
    if (!isActivationUser(user) || !persistActivationSession(nextToken, user)) {
      activationOwnerRef.current = null;
      clearAuthTransitionBarrier();
      publishTOTPActivationFailure("install-failed");
      logout();
      return false;
    }
    activationOwnerRef.current = null;
    login(nextToken, user.username, user.role, user.id, true);
    return true;
  }, [login, logout]);

  const setTotpEnabled = useCallback((enabled: boolean) => {
    const sessionStorageRef = getSessionStorage();
    safeSetItem(sessionStorageRef, AUTH_TOTP_ENABLED_KEY, String(enabled));
    if (!enabled) {
      activationOwnerRef.current = null;
      clearAuthTransitionBarrier();
      bumpAuthSessionGeneration();
      dropPendingStepUp(new Error(i18n.t("stepUp.totpRequired")));
      clearStoredStepUpProof();
    }
    setAuthState((prev) => ({ ...prev, totpEnabled: enabled }));
  }, [dropPendingStepUp]);

  const clearStepUpProof = useCallback((action?: StepUpAction) => {
    clearStoredStepUpProof(action);
  }, []);

  const ensureStepUpProof = useCallback(async (action: StepUpAction, options: StepUpProofOptions = {}): Promise<string> => {
    assertStepUpPrerequisite(token, totpEnabled);
    if (isAuthTransitionActive()) {
      throw new AuthTransitionRejectedError();
    }
    const ownerSignal = options.signal ?? null;
    if (ownerSignal?.aborted) {
      throw stepUpAbortError();
    }
    const persist = options.persist ?? true;
    const reuseCached = options.reuseCached ?? persist;
    if (reuseCached) {
      const cached = readStepUpProof(action);
      if (cached) {
        return cached.proof;
      }
    }
    if (!reuseCached) {
      clearStoredStepUpProof(action);
    }
    const existing = pendingStepUpRef.current;
    if (existing) {
      if (existing.action === action && existing.persist === persist && existing.ownerSignal === ownerSignal) {
        return existing.promise;
      }
      throw new Error(i18n.t("stepUp.alreadyOpen"));
    }
    setStepUpCode("");
    setStepUpError(null);
    setStepUpSubmitting(false);
    setStepUpOwned(ownerSignal !== null);
    setStepUpDialogOpen(true);
    let resolveRequest!: (proof: string) => void;
    let rejectRequest!: (error: Error) => void;
    const promise = new Promise<string>((resolve, reject) => {
      resolveRequest = resolve;
      rejectRequest = reject;
    });
    const pendingRequest: PendingStepUpRequest = {
      resolve: resolveRequest,
      reject: rejectRequest,
      promise,
      persist,
      action,
      authGeneration: getAuthSessionGeneration(),
      ownerSignal,
      submitController: ownerSignal ? new AbortController() : null,
      releaseOwner: () => undefined,
    };
    if (ownerSignal) {
      const onOwnerAbort = () => {
        if (pendingStepUpRef.current === pendingRequest) {
          dropPendingStepUp(stepUpAbortError());
        }
      };
      ownerSignal.addEventListener("abort", onOwnerAbort, { once: true });
      pendingRequest.releaseOwner = () => ownerSignal.removeEventListener("abort", onOwnerAbort);
    }
    pendingStepUpRef.current = pendingRequest;
    return promise;
  }, [dropPendingStepUp, token, totpEnabled]);

  const closeStepUpDialog = useCallback(() => {
    // Unowned submits stay non-cancellable; an owned attempt may always be cancelled by its own dialog.
    if (stepUpSubmitting && !pendingStepUpRef.current?.ownerSignal) {
      return;
    }
    dropPendingStepUp(new Error(i18n.t("stepUp.cancelled")));
  }, [dropPendingStepUp, stepUpSubmitting]);

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
    if (isAuthTransitionActive()) {
      return;
    }
    const pendingRequest = pendingStepUpRef.current;
    if (!pendingRequest) {
      return;
    }
    stepUpSubmitAttemptRef.current += 1;
    const submitAttempt = stepUpSubmitAttemptRef.current;
    setStepUpSubmitting(true);
    setStepUpError(null);
    try {
      const response = pendingRequest.submitController
        ? await apiClient.requestStepUpProof(token, code, pendingRequest.action, {
          signal: pendingRequest.submitController.signal,
        })
        : await apiClient.requestStepUpProof(token, code, pendingRequest.action);
      const stillCurrent = pendingStepUpRef.current === pendingRequest
        && !pendingRequest.ownerSignal?.aborted
        && pendingRequest.authGeneration === getAuthSessionGeneration()
        && safeGetItem(getSessionStorage(), AUTH_TOKEN_KEY) === token
        && !isAuthTransitionActive();
      if (!stillCurrent) {
        if (pendingStepUpRef.current === pendingRequest) {
          dropPendingStepUp(new Error(i18n.t("stepUp.loginRequired")));
        } else {
          pendingRequest.reject(new Error(i18n.t("stepUp.loginRequired")));
        }
        return;
      }
      const shouldPersistProof = pendingRequest.persist;
      if (shouldPersistProof) {
        const expiresAt = Date.parse(response.expiresAt);
        if (Number.isFinite(expiresAt)) {
          saveStepUpProof(pendingRequest.action, response.proof, expiresAt);
        }
      }
      pendingRequest.releaseOwner();
      pendingRequest.resolve(response.proof);
      pendingStepUpRef.current = null;
      setStepUpDialogOpen(false);
      setStepUpCode("");
      setStepUpOwned(false);
    } catch (error) {
      if (
        pendingStepUpRef.current !== pendingRequest ||
        pendingRequest.authGeneration !== getAuthSessionGeneration() ||
        isAuthTransitionActive()
      ) {
        return;
      }
      const message = error instanceof ApiError ? error.message : i18n.t("stepUp.verifyFailed");
      setStepUpError(message || i18n.t("stepUp.verifyFailed"));
    } finally {
      if (stepUpSubmitAttemptRef.current === submitAttempt) {
        setStepUpSubmitting(false);
      }
    }
  }, [dropPendingStepUp, stepUpCode, token]);

  const value = useMemo<AuthContextValue>(
    () => ({
      ...authState,
      isAuthenticated: Boolean(token),
      authTransitioning,
      beginTOTPActivation,
      abortTOTPActivation,
      completeTOTPActivation,
      login,
      logout,
      setTotpEnabled,
      ensureStepUpProof,
      clearStepUpProof
    }),
    [
      abortTOTPActivation,
      authState,
      authTransitioning,
      beginTOTPActivation,
      clearStepUpProof,
      completeTOTPActivation,
      ensureStepUpProof,
      login,
      logout,
      setTotpEnabled,
      token,
    ]
  );

  return (
    <AuthContext.Provider value={value}>
      {children}
      <Dialog open={activationFailure !== null} onOpenChange={(open) => {
        if (!open) {
          dismissTOTPActivationFailure();
        }
      }}>
        <DialogContent size="sm">
          <DialogHeader>
            <DialogTitle>
              {i18n.t(activationFailure === "install-failed" ? "totp.activationInstallFailed" : "totp.activationUncertain")}
            </DialogTitle>
            <DialogDescription>{i18n.t("stepUp.loginRequired")}</DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button type="button" onClick={() => dismissTOTPActivationFailure()}>
              {i18n.t("common.close")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
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
              <Button type="button" variant="outline" onClick={closeStepUpDialog} disabled={stepUpSubmitting && !stepUpOwned}>
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
