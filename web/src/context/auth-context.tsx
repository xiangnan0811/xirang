import {
  createContext,
  useCallback,
  useContext,
  useMemo,
  useState,
  type PropsWithChildren
} from "react";

const AUTH_TOKEN_KEY = "xirang-auth-token";
const AUTH_USERNAME_KEY = "xirang-username";

type StoredAuthState = {
  token: string | null;
  username: string | null;
};

type AuthContextValue = {
  token: string | null;
  username: string | null;
  isAuthenticated: boolean;
  login: (token: string, username: string) => void;
  logout: () => void;
};

const AuthContext = createContext<AuthContextValue | null>(null);

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

function readStoredAuthState(): StoredAuthState {
  const sessionStorageRef = getSessionStorage();
  const localStorageRef = getLocalStorage();

  const sessionToken = safeGetItem(sessionStorageRef, AUTH_TOKEN_KEY);
  const sessionUsername = safeGetItem(sessionStorageRef, AUTH_USERNAME_KEY);
  if (sessionToken) {
    return { token: sessionToken, username: sessionUsername };
  }
  safeRemoveItem(sessionStorageRef, AUTH_USERNAME_KEY);

  const legacyToken = safeGetItem(localStorageRef, AUTH_TOKEN_KEY);
  const legacyUsername = safeGetItem(localStorageRef, AUTH_USERNAME_KEY);

  if (legacyToken) {
    safeSetItem(sessionStorageRef, AUTH_TOKEN_KEY, legacyToken);
    if (legacyUsername) {
      safeSetItem(sessionStorageRef, AUTH_USERNAME_KEY, legacyUsername);
    }
  }

  safeRemoveItem(localStorageRef, AUTH_TOKEN_KEY);
  safeRemoveItem(localStorageRef, AUTH_USERNAME_KEY);

  return { token: legacyToken, username: legacyToken ? legacyUsername : null };
}

export function AuthProvider({ children }: PropsWithChildren) {
  const [{ token, username }, setAuthState] = useState<StoredAuthState>(() => readStoredAuthState());

  const login = useCallback((nextToken: string, nextUsername: string) => {
    const sessionStorageRef = getSessionStorage();
    const localStorageRef = getLocalStorage();

    safeSetItem(sessionStorageRef, AUTH_TOKEN_KEY, nextToken);
    safeSetItem(sessionStorageRef, AUTH_USERNAME_KEY, nextUsername);
    safeRemoveItem(localStorageRef, AUTH_TOKEN_KEY);
    safeRemoveItem(localStorageRef, AUTH_USERNAME_KEY);

    setAuthState({ token: nextToken, username: nextUsername });
  }, []);

  const logout = useCallback(() => {
    const sessionStorageRef = getSessionStorage();
    const localStorageRef = getLocalStorage();

    safeRemoveItem(sessionStorageRef, AUTH_TOKEN_KEY);
    safeRemoveItem(sessionStorageRef, AUTH_USERNAME_KEY);
    safeRemoveItem(localStorageRef, AUTH_TOKEN_KEY);
    safeRemoveItem(localStorageRef, AUTH_USERNAME_KEY);

    setAuthState({ token: null, username: null });
  }, []);

  const value = useMemo<AuthContextValue>(
    () => ({
      token,
      username,
      isAuthenticated: Boolean(token),
      login,
      logout
    }),
    [login, logout, token, username]
  );

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth() {
  const context = useContext(AuthContext);
  if (!context) {
    throw new Error("useAuth 必须在 AuthProvider 中使用");
  }
  return context;
}
