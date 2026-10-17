import { useEffect, useRef, type JSX } from "react";
import { useLocation, useNavigate } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { toast } from "@/components/ui/toast-sonner";
import { useAuth } from "@/context/auth-context.hooks";
import type { AuthRole } from "@/context/auth-context.shared";
import {
  getAuthIdentitySnapshot,
  getAuthSessionGeneration,
  isAuthTransitionActive,
  subscribeAuthTransition,
} from "@/lib/api/core";
import {
  issueTicket,
  replaceVisibleNotice,
  revokeStaleNotices,
  takeTicket,
} from "@/components/access-denied-navigation.state";

const OVERVIEW_PATH = "/app/overview";
const ACCESS_DENIED_CODE = "access_denied";
const MAX_NOTICE_ID_LENGTH = 64;

function isDeniedRole(role: AuthRole | null | undefined): role is "operator" | "viewer" {
  return role === "operator" || role === "viewer";
}

function AccessDeniedRedirectNavigation({ sourcePath, locationKey }: { sourcePath: string; locationKey: string }) {
  const location = useLocation();
  const navigate = useNavigate();
  const { token, role, authTransitioning } = useAuth();
  const issuedRef = useRef<string | null>(null);

  useEffect(() => {
    if (location.pathname !== sourcePath || location.key !== locationKey) return;
    if (authTransitioning || isAuthTransitionActive()) return;
    const generation = getAuthSessionGeneration();
    const marker = `${locationKey}\u0000${generation}`;
    if (issuedRef.current === marker) return;
    issuedRef.current = marker;

    const identity = getAuthIdentitySnapshot();
    if (!token || !isDeniedRole(role) || identity.token !== token || identity.role !== role) {
      // Unconfirmed identity keeps the plain safe fallback without claiming a permission denial.
      navigate(OVERVIEW_PATH, { replace: true });
      return;
    }
    const id = crypto.randomUUID();
    issueTicket({ id, sourceLocationKey: locationKey, authGeneration: generation });
    navigate(OVERVIEW_PATH, { replace: true, state: { navigationNotice: { code: ACCESS_DENIED_CODE, id } } });
  }, [authTransitioning, location.key, location.pathname, locationKey, navigate, role, sourcePath, token]);

  return null;
}

/**
 * Replaces a denied admin-only route with Overview and hands one notice to the shell.
 * Each source history entry owns its own producer, so a retained exiting page cannot
 * swallow a later visit to the same route.
 */
export function AccessDeniedRedirect({ sourcePath }: { sourcePath: string }): JSX.Element | null {
  const location = useLocation();
  if (location.pathname !== sourcePath) return null;
  return <AccessDeniedRedirectNavigation key={location.key} sourcePath={sourcePath} locationKey={location.key} />;
}

/** Shows the access-denied notice once for the Overview entry that carries a live ticket. */
export function AccessDeniedNavigationNotice(): JSX.Element | null {
  const { t } = useTranslation();
  const location = useLocation();
  const navigate = useNavigate();
  const { token, role, authTransitioning } = useAuth();
  const handledKeyRef = useRef<string | null>(null);

  useEffect(() => {
    revokeStaleNotices();
  }, [authTransitioning, role, token]);

  useEffect(() => {
    const unsubscribe = subscribeAuthTransition(revokeStaleNotices);
    return () => {
      unsubscribe();
      // A real identity change dismisses; an unchanged StrictMode remount keeps the notice.
      revokeStaleNotices();
    };
  }, []);

  useEffect(() => {
    if (location.pathname !== OVERVIEW_PATH) return;
    const state: unknown = location.state;
    if (typeof state !== "object" || state === null || !("navigationNotice" in state)) return;
    if (handledKeyRef.current === location.key) return;
    handledKeyRef.current = location.key;

    const notice = state.navigationNotice;
    const id = typeof notice === "object" && notice !== null
      && "code" in notice && notice.code === ACCESS_DENIED_CODE
      && "id" in notice && typeof notice.id === "string"
      && notice.id.length > 0 && notice.id.length <= MAX_NOTICE_ID_LENGTH
      ? notice.id
      : null;
    const ticket = id === null ? null : takeTicket(id);
    const identity = getAuthIdentitySnapshot();
    if (
      ticket
      && token
      && isDeniedRole(role)
      && ticket.authGeneration === getAuthSessionGeneration()
      && !authTransitioning
      && !isAuthTransitionActive()
      && identity.token === token
      && identity.role === role
    ) {
      const toastId = `access-denied:${ticket.id}`;
      replaceVisibleNotice({ toastId, authGeneration: ticket.authGeneration, token, role });
      toast.info(t("navigation.accessDenied"), { id: toastId });
    }

    // Keep every other history-state field (for example securityReturnTo); only the notice is consumed.
    const rest = Object.fromEntries(Object.entries(state).filter(([field]) => field !== "navigationNotice"));
    navigate(
      { pathname: location.pathname, search: location.search, hash: location.hash },
      { replace: true, state: Object.keys(rest).length > 0 ? rest : null },
    );
  }, [authTransitioning, location, navigate, role, t, token]);

  return null;
}
