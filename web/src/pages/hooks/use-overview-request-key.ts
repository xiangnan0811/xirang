import { useState, useSyncExternalStore } from "react";
import {
  getAuthSessionGeneration,
  isAuthTransitionActive,
  subscribeAuthSessionGeneration,
  subscribeAuthTransition,
} from "@/lib/api/core";

export interface OverviewAuthScope {
  /** Token plus session generation: same-token identity changes still invalidate overview data. */
  identity: string;
  /** Identity plus auth barrier state; any change aborts and restarts overview requests. */
  scope: string;
  /** Auth barrier active: overview requests must not start until it is released. */
  transitioning: boolean;
}

export function useOverviewAuthScope(token: string | null): OverviewAuthScope {
  const generation = useSyncExternalStore(subscribeAuthSessionGeneration, getAuthSessionGeneration, getAuthSessionGeneration);
  const transitioning = useSyncExternalStore(subscribeAuthTransition, isAuthTransitionActive, () => false);
  const identity = `${token ?? ""}#${generation}`;
  return { identity, scope: `${identity}:${transitioning ? "paused" : "live"}`, transitioning };
}

/**
 * Scheduled overview pulses must not starve slow endpoints: a pulse advances the returned key only
 * when the current scope has nothing in flight, so a pending request keeps running across pulses.
 * Any scope change (identity, auth barrier, manual refresh, retry, traffic window) always yields a
 * new key; effects keyed on it abort the superseded request in cleanup.
 */
export function useOverviewRequestKey(scope: string, pollVersion: number, pending: boolean): string {
  const [seenPollVersion, setSeenPollVersion] = useState(pollVersion);
  const [pollTick, setPollTick] = useState(0);
  if (seenPollVersion !== pollVersion) {
    setSeenPollVersion(pollVersion);
    if (!pending) {
      setPollTick((current) => current + 1);
    }
  }
  return `${scope}|${pollTick}`;
}

/** Late results from aborted requests, an active auth barrier or a previous session are dropped. */
export function isCurrentOverviewRequest(signal: AbortSignal, generation: number): boolean {
  return !signal.aborted && !isAuthTransitionActive() && getAuthSessionGeneration() === generation;
}
