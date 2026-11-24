import { useCallback, useEffect, useState } from "react";
import i18n from "@/i18n";
import { apiClient } from "@/lib/api/client";
import { getAuthSessionGeneration } from "@/lib/api/core";
import { getErrorMessage } from "@/lib/utils";
import type { BackupConfidenceData, BackupHealthData } from "@/types/domain";
import { getPostureGeneratedAt } from "@/pages/overview-page.posture.utils";
import {
  isCurrentOverviewRequest,
  useOverviewAuthScope,
  useOverviewRequestKey,
} from "@/pages/hooks/use-overview-request-key";

export interface UseOverviewPostureOptions {
  token: string | null;
  /** Manual refresh: aborts and restarts both sources. */
  refreshVersion: number;
  /** Scheduled pulse: restarts only sources with nothing in flight. */
  pollVersion: number;
}

export interface UseOverviewPostureResult {
  confidence: BackupConfidenceData | null;
  health: BackupHealthData | null;
  loading: boolean;
  confidenceLoading: boolean;
  healthLoading: boolean;
  confidenceError: string | null;
  healthError: string | null;
  generatedAt: string;
  retry: () => void;
}

type PostureLoader<T> = (token: string | null, signal: AbortSignal) => Promise<T>;

const loadConfidence: PostureLoader<BackupConfidenceData> = (token, signal) =>
  token
    ? apiClient.getBackupConfidence(token, { signal })
    : import("@/data/mock").then((mocks) => mocks.buildMockBackupConfidence());

const loadHealth: PostureLoader<BackupHealthData> = (token, signal) =>
  token
    ? apiClient.getBackupHealth(token, { signal })
    : import("@/data/mock").then((mocks) => mocks.buildMockBackupHealth());

interface PostureSourceScope {
  token: string | null;
  enabled: boolean;
  identity: string;
  scope: string;
  transitioning: boolean;
  pollVersion: number;
}

interface PostureSourceState<T> {
  data: T | null;
  error: string | null;
  loading: boolean;
}

// Each source settles on its own response so a slow sibling never hides a fast one; a failure
// keeps its message until the next result so polling does not flash the error alert.
function usePostureSource<T>(
  load: PostureLoader<T>,
  failureKey: string,
  { token, enabled, identity, scope, transitioning, pollVersion }: PostureSourceScope,
): PostureSourceState<T> {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState<boolean>(enabled);
  const requestKey = useOverviewRequestKey(scope, pollVersion, loading);
  const [tracked, setTracked] = useState({ identity, requestKey });

  if (tracked.identity !== identity) {
    setTracked({ identity, requestKey });
    setData(null);
    setError(null);
    setLoading(enabled);
  } else if (tracked.requestKey !== requestKey) {
    setTracked({ identity, requestKey });
    setLoading(enabled);
  }

  useEffect(() => {
    if (!enabled || transitioning) {
      return;
    }
    const controller = new AbortController();
    const generation = getAuthSessionGeneration();
    const fallbackKey = token ? failureKey : "overview.postureLoadFailed";
    void load(token, controller.signal)
      .then(
        (result) => {
          if (isCurrentOverviewRequest(controller.signal, generation)) {
            setData(result);
            setError(null);
          }
        },
        (reason: unknown) => {
          if (isCurrentOverviewRequest(controller.signal, generation)) {
            setData(null);
            setError(getErrorMessage(reason, i18n.t(fallbackKey)));
          }
        },
      )
      .finally(() => {
        if (isCurrentOverviewRequest(controller.signal, generation)) {
          setLoading(false);
        }
      });

    return () => {
      controller.abort();
    };
  }, [enabled, failureKey, load, requestKey, token, transitioning]);

  return { data, error, loading };
}

export function useOverviewPosture({
  token,
  refreshVersion,
  pollVersion,
}: UseOverviewPostureOptions): UseOverviewPostureResult {
  const demoModeEnabled = import.meta.env.VITE_ENABLE_DEMO_MODE === "true";
  const [reloadKey, setReloadKey] = useState<number>(0);
  const auth = useOverviewAuthScope(token);

  const sourceScope: PostureSourceScope = {
    token,
    enabled: Boolean(token) || demoModeEnabled,
    identity: auth.identity,
    scope: `${auth.scope}:${refreshVersion}:${reloadKey}`,
    transitioning: auth.transitioning,
    pollVersion,
  };
  const confidence = usePostureSource(loadConfidence, "backupConfidence.loadFailed", sourceScope);
  const health = usePostureSource(loadHealth, "backupHealth.loadFailed", sourceScope);

  const retry = useCallback(() => {
    setReloadKey((current) => current + 1);
  }, []);

  const generatedAt = getPostureGeneratedAt(confidence.data, health.data) ?? "";

  return {
    confidence: confidence.data,
    health: health.data,
    loading: confidence.loading || health.loading,
    confidenceLoading: confidence.loading,
    healthLoading: health.loading,
    confidenceError: confidence.error,
    healthError: health.error,
    generatedAt,
    retry,
  };
}
