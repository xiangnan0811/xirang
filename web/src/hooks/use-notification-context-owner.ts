import { useCallback, useLayoutEffect, useRef, useSyncExternalStore } from "react";
import { useAuth } from "@/context/auth-context.hooks";
import type { AuthRole } from "@/context/auth-context.shared";
import { getAuthIdentitySnapshot, isAuthTransitionActive, subscribeAuthTransition } from "@/lib/api/core";
import { captureNotificationOwner, getNotificationContextEpoch, subscribeNotificationContextEpoch } from "@/lib/notification-config-context";

export function useNotificationContextOwner(): {
  epoch: string;
  token: string | null;
  role: AuthRole | null;
  active: boolean;
  capture: () => (() => boolean);
} {
  const { token, role } = useAuth();
  const epoch = useSyncExternalStore(subscribeNotificationContextEpoch, getNotificationContextEpoch, getNotificationContextEpoch);
  const transitioning = useSyncExternalStore(subscribeAuthTransition, isAuthTransitionActive, isAuthTransitionActive);
  const identityRef = useRef({ token, role });
  useLayoutEffect(() => { identityRef.current = { token, role }; }, [token, role]);
  const revisionRef = useRef(0);
  const mountedRef = useRef(false);
  useLayoutEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      revisionRef.current += 1;
    };
  }, []);
  const capture = useCallback(() => {
    const revision = revisionRef.current;
    const ownsIdentity = captureNotificationOwner(token, role);
    return () => mountedRef.current && epoch === getNotificationContextEpoch() && revisionRef.current === revision && identityRef.current.token === token
      && identityRef.current.role === role && ownsIdentity();
  }, [epoch, token, role]);
  const current = getAuthIdentitySnapshot();
  return {
    epoch, token, role,
    active: !!token && !transitioning && current.token === token && current.role === role,
    capture,
  };
}
