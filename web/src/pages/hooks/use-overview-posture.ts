import { useCallback, useEffect, useRef, useState } from "react";
import i18n from "@/i18n";
import { apiClient } from "@/lib/api/client";
import { getErrorMessage } from "@/lib/utils";
import type { BackupConfidenceData, BackupHealthData } from "@/types/domain";
import { getPostureGeneratedAt } from "@/pages/overview-page.posture.utils";

export interface UseOverviewPostureOptions {
  token: string | null;
  refreshVersion: number;
}

export interface UseOverviewPostureResult {
  confidence: BackupConfidenceData | null;
  health: BackupHealthData | null;
  loading: boolean;
  confidenceError: string | null;
  healthError: string | null;
  generatedAt: string;
  retry: () => void;
}

export function useOverviewPosture({
  token,
  refreshVersion,
}: UseOverviewPostureOptions): UseOverviewPostureResult {
  const demoModeEnabled = import.meta.env.VITE_ENABLE_DEMO_MODE === "true";

  const [confidence, setConfidence] = useState<BackupConfidenceData | null>(null);
  const [health, setHealth] = useState<BackupHealthData | null>(null);
  const [loading, setLoading] = useState<boolean>(Boolean(token) || demoModeEnabled);
  const [confidenceError, setConfidenceError] = useState<string | null>(null);
  const [healthError, setHealthError] = useState<string | null>(null);
  const [reloadKey, setReloadKey] = useState<number>(0);

  const requestRef = useRef(0);
  const [trackedToken, setTrackedToken] = useState(token);
  const fetchScope = `${token ?? ""}:${refreshVersion}:${reloadKey}`;
  const [trackedScope, setTrackedScope] = useState(fetchScope);

  if (trackedToken !== token) {
    setTrackedToken(token);
    setTrackedScope(fetchScope);
    setConfidence(null);
    setHealth(null);
    setConfidenceError(null);
    setHealthError(null);
    setLoading(Boolean(token) || demoModeEnabled);
  } else if (trackedScope !== fetchScope) {
    setTrackedScope(fetchScope);
    setConfidenceError(null);
    setHealthError(null);
    setLoading(Boolean(token) || demoModeEnabled);
  }

  useEffect(() => {
    const requestId = requestRef.current + 1;
    requestRef.current = requestId;

    if (!token) {
      if (!demoModeEnabled) {
        return;
      }
      let cancelled = false;
      import("@/data/mock")
        .then((mocks) => {
          if (!cancelled && requestRef.current === requestId) {
            setConfidence(mocks.buildMockBackupConfidence());
            setHealth(mocks.buildMockBackupHealth());
            setConfidenceError(null);
            setHealthError(null);
          }
        })
        .catch((error) => {
          if (!cancelled && requestRef.current === requestId) {
            const msg = getErrorMessage(error, i18n.t("overview.postureLoadFailed"));
            setConfidenceError(msg);
            setHealthError(msg);
          }
        })
        .finally(() => {
          if (!cancelled && requestRef.current === requestId) {
            setLoading(false);
          }
        });

      return () => {
        cancelled = true;
      };
    }

    const controller = new AbortController();
    void Promise.allSettled([
      apiClient.getBackupConfidence(token, { signal: controller.signal }),
      apiClient.getBackupHealth(token, { signal: controller.signal }),
    ])
      .then(([confidenceResult, healthResult]) => {
        if (controller.signal.aborted || requestRef.current !== requestId) {
          return;
        }
        if (confidenceResult.status === "fulfilled") {
          setConfidence(confidenceResult.value);
          setConfidenceError(null);
        } else {
          setConfidence(null);
          setConfidenceError(getErrorMessage(confidenceResult.reason, i18n.t("backupConfidence.loadFailed")));
        }

        if (healthResult.status === "fulfilled") {
          setHealth(healthResult.value);
          setHealthError(null);
        } else {
          setHealth(null);
          setHealthError(getErrorMessage(healthResult.reason, i18n.t("backupHealth.loadFailed")));
        }
      })
      .finally(() => {
        if (!controller.signal.aborted && requestRef.current === requestId) {
          setLoading(false);
        }
      });

    return () => {
      controller.abort();
    };
  }, [token, demoModeEnabled, refreshVersion, reloadKey]);

  const retry = useCallback(() => {
    setReloadKey((current) => current + 1);
  }, []);

  const generatedAt = getPostureGeneratedAt(confidence, health) ?? "";

  return {
    confidence,
    health,
    loading,
    confidenceError,
    healthError,
    generatedAt,
    retry,
  };
}
