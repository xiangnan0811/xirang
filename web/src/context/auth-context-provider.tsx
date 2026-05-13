import {
  useCallback,
  useMemo,
  useState,
  type PropsWithChildren
} from "react";
import {
  AuthContext,
  type AuthContextValue,
  type AuthRole,
} from "@/context/auth-context.shared";

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

  const login = useCallback((
    nextToken: string,
    nextUsername: string,
    nextRole?: AuthRole,
    nextUserID?: number,
    nextTotpEnabled?: boolean
  ) => {
    const sessionStorageRef = getSessionStorage();
    const localStorageRef = getLocalStorage();
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
    safeRemoveItem(localStorageRef, AUTH_TOKEN_KEY);
    safeRemoveItem(localStorageRef, AUTH_USERNAME_KEY);
    safeRemoveItem(localStorageRef, AUTH_ROLE_KEY);
    safeRemoveItem(localStorageRef, AUTH_USER_ID_KEY);

    setAuthState({ token: null, username: null, role: null, userId: null, totpEnabled: false });
  }, []);

  const setTotpEnabled = useCallback((enabled: boolean) => {
    const sessionStorageRef = getSessionStorage();
    safeSetItem(sessionStorageRef, AUTH_TOTP_ENABLED_KEY, String(enabled));
    setAuthState((prev) => ({ ...prev, totpEnabled: enabled }));
  }, []);

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
      setTotpEnabled
    }),
    [login, logout, role, token, userId, username, totpEnabled, setTotpEnabled]
  );

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}
