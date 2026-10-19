import type { AuthRole } from "@/context/auth-context.shared";
import {
  getAuthIdentitySnapshot,
  isAuthTransitionActive,
  subscribeAuthSessionGeneration,
  subscribeAuthTransition,
} from "@/lib/api/core";

export type NotificationConfigTab = "channels" | "silences" | "escalation";
export type NotificationConfigReturn = {
  version: 1;
  source: "notifications";
  alertId: string | null;
  owner: string;
};
export type AlertNavigationTarget = string | null | "invalid";

export function parseNotificationAlertId(value: unknown): string | null {
  if (typeof value !== "string" || value.length > 22) return null;
  const digits = /^(?:alert-)?([1-9]\d*)$/.exec(value)?.[1];
  if (!digits) return null;
  const id = Number(digits);
  return Number.isSafeInteger(id) && id > 0 && String(id) === digits ? digits : null;
}

export function parseNotificationAlertTarget(values: string[]): AlertNavigationTarget {
  if (values.length === 0) return null;
  return values.length === 1 ? parseNotificationAlertId(values[0]) ?? "invalid" : "invalid";
}

function documentNonce(): string | null {
  try {
    return Array.from(crypto.getRandomValues(new Uint8Array(16)), (byte) => byte.toString(16).padStart(2, "0")).join("");
  } catch {
    return null;
  }
}

const nonce = documentNonce();
let revision = 0;
const listeners = new Set<() => void>();
function advanceEpoch(): void {
  revision += 1;
  for (const listener of listeners) listener();
}
const unsubscribeGeneration = subscribeAuthSessionGeneration(advanceEpoch);
const unsubscribeTransition = subscribeAuthTransition(() => {
  if (isAuthTransitionActive()) advanceEpoch();
});
if (import.meta.hot) {
  import.meta.hot.dispose(() => {
    unsubscribeGeneration();
    unsubscribeTransition();
  });
}

export function getNotificationContextEpoch(): string {
  return `${nonce ?? "unavailable"}:${revision}`;
}

export function subscribeNotificationContextEpoch(listener: () => void): () => void {
  listeners.add(listener);
  return () => { listeners.delete(listener); };
}

export function captureNotificationOwner(token: string | null, role: AuthRole | null): () => boolean {
  const epoch = getNotificationContextEpoch();
  return () => {
    const current = getAuthIdentitySnapshot();
    return epoch === getNotificationContextEpoch() && !isAuthTransitionActive()
      && current.token === token && current.role === role;
  };
}

function canUseReturn(): boolean {
  const identity = getAuthIdentitySnapshot();
  return nonce !== null && !!identity.token && identity.role === "admin" && !isAuthTransitionActive();
}

export function createNotificationConfigReturn(alertId: string | null): NotificationConfigReturn | null {
  if (!canUseReturn() || (alertId !== null && parseNotificationAlertId(alertId) !== alertId)) return null;
  return { version: 1, source: "notifications", alertId, owner: getNotificationContextEpoch() };
}

function isPlainObject(value: unknown): value is Record<string, unknown> {
  if (value === null || typeof value !== "object") return false;
  const prototype: unknown = Object.getPrototypeOf(value);
  return prototype === Object.prototype || prototype === null;
}

export function readNotificationConfigReturn(value: unknown): NotificationConfigReturn | null {
  if (!canUseReturn() || !isPlainObject(value)) return null;
  if (Reflect.ownKeys(value).length !== 4
    || !["version", "source", "alertId", "owner"].every((key) => Object.hasOwn(value, key))
    || value.version !== 1 || value.source !== "notifications"
    || value.owner !== getNotificationContextEpoch()
    || (value.alertId !== null && (typeof value.alertId !== "string" || parseNotificationAlertId(value.alertId) !== value.alertId))) return null;
  return { version: 1, source: "notifications", alertId: value.alertId, owner: getNotificationContextEpoch() };
}

export function withoutNotificationConfigReturn(state: unknown): Record<string, unknown> | null {
  if (!isPlainObject(state)) return null;
  const next = { ...state };
  delete next.notificationConfigReturn;
  return Reflect.ownKeys(next).length ? next : null;
}
