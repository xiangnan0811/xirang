import { toast } from "@/components/ui/toast-sonner";
import type { AuthRole } from "@/context/auth-context.shared";
import { getAuthIdentitySnapshot, getAuthSessionGeneration, isAuthTransitionActive } from "@/lib/api/core";

export type PendingTicket = { id: string; sourceLocationKey: string; authGeneration: number };
export type VisibleNotice = { toastId: string; authGeneration: number; token: string; role: AuthRole };

// Only one denied navigation can be in flight; nothing here is persisted or listed.
let pendingTicket: PendingTicket | null = null;
let visibleNotice: VisibleNotice | null = null;

/** Drops the pending ticket and visible toast once their auth identity no longer holds. */
export function revokeStaleNotices(): void {
  const generation = getAuthSessionGeneration();
  const barrier = isAuthTransitionActive();
  if (pendingTicket && (barrier || pendingTicket.authGeneration !== generation)) {
    pendingTicket = null;
  }
  if (!visibleNotice) return;
  const identity = getAuthIdentitySnapshot();
  if (
    barrier
    || visibleNotice.authGeneration !== generation
    || identity.token !== visibleNotice.token
    || identity.role !== visibleNotice.role
  ) {
    toast.dismiss(visibleNotice.toastId);
    visibleNotice = null;
  }
}

/** Replaces any in-flight ticket with the one just handed to Overview. */
export function issueTicket(ticket: PendingTicket): void {
  pendingTicket = ticket;
}

export function takeTicket(id: string): PendingTicket | null {
  const ticket = pendingTicket;
  if (!ticket || ticket.id !== id) return null;
  pendingTicket = null;
  return ticket;
}

/** Records the shown notice, dismissing a different previously visible one. */
export function replaceVisibleNotice(notice: VisibleNotice): void {
  if (visibleNotice && visibleNotice.toastId !== notice.toastId) toast.dismiss(visibleNotice.toastId);
  visibleNotice = notice;
}

/** Test-only reset of module state between isolated cases. */
export function resetAccessDeniedNavigationForTests(): void {
  pendingTicket = null;
  visibleNotice = null;
}
