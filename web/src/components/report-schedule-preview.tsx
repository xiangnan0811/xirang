import { useCallback, useLayoutEffect, useRef, useState, useSyncExternalStore, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { CalendarClock } from "lucide-react";
import { Button } from "@/components/ui/button";
import { useAuth } from "@/context/auth-context.hooks";
import {
  ApiError,
  getAuthIdentitySnapshot,
  getAuthSessionGeneration,
  isAuthTransitionActive,
  subscribeAuthTransition,
} from "@/lib/api/core";
import {
  createReportsApi,
  type ReportSchedulePreview as ReportSchedulePreviewData,
} from "@/lib/api/reports-api";

const reportsApi = createReportsApi();

type FailureKind = "forbidden" | "unavailable" | "rateLimited" | "failed";

type PreviewState =
  | { status: "idle" }
  | { status: "loading" }
  | { status: "ready"; data: ReportSchedulePreviewData }
  | { status: "expired" }
  | { status: "error"; kind: FailureKind };

type Props = {
  cron: string;
  token: string;
  active: boolean;
  disabled?: boolean;
};

const FAILURE_KEYS: Record<FailureKind, string> = {
  forbidden: "cron.reportPreviewForbidden",
  unavailable: "cron.reportPreviewUnavailable",
  rateLimited: "cron.reportPreviewRateLimited",
  failed: "cron.reportPreviewFailed",
};

const NO_MATCH_KEYS = {
  unsupported_month: "cron.reportPreviewMonth",
  unmatchable_field: "cron.reportPreviewField",
  horizon_exhausted: "cron.reportPreviewHorizon",
} as const;

function classifyFailure(error: unknown): FailureKind {
  if (error instanceof ApiError) {
    const status = error.httpStatus ?? error.status;
    if (status === 403) return "forbidden";
    if (status === 404) return "unavailable";
    if (status === 429) return "rateLimited";
  }
  return "failed";
}

/**
 * Report schedule estimate backed by the server's matcher. Every identity input is part of the
 * session key, so a new expression, reopened dialog, role change or auth generation starts from
 * an empty session and can never show a window that belongs to another owner.
 */
export function ReportSchedulePreview({ cron, token, active, disabled = false }: Props): ReactNode {
  const { role } = useAuth();
  const generation = getAuthSessionGeneration();
  return (
    <ReportSchedulePreviewSession
      key={JSON.stringify([active, cron, token, role, generation])}
      cron={cron}
      token={token}
      active={active}
      role={role}
      generation={generation}
      disabled={disabled}
    />
  );
}

function ReportSchedulePreviewSession({ cron, token, active, role, generation, disabled }: {
  cron: string;
  token: string;
  active: boolean;
  role: string | null;
  generation: number;
  disabled: boolean;
}) {
  const { t } = useTranslation();
  const transitioning = useSyncExternalStore(subscribeAuthTransition, isAuthTransitionActive, () => false);
  const [state, setState] = useState<PreviewState>({ status: "idle" });
  const attemptRef = useRef(0);
  const pendingRef = useRef(false);
  const controllerRef = useRef<AbortController | null>(null);
  const timerRef = useRef<number | null>(null);

  const allowed = useCallback(() => {
    if (!active || !token || role !== "admin") return false;
    if (isAuthTransitionActive() || getAuthSessionGeneration() !== generation) return false;
    const snapshot = getAuthIdentitySnapshot();
    return snapshot.token === token && snapshot.role === "admin";
  }, [active, token, role, generation]);

  // Retires the current attempt: late success/error handlers see a different attempt and drop out.
  const invalidate = useCallback(() => {
    attemptRef.current += 1;
    pendingRef.current = false;
    controllerRef.current?.abort();
    controllerRef.current = null;
    if (timerRef.current !== null) {
      window.clearTimeout(timerRef.current);
      timerRef.current = null;
    }
  }, []);

  useLayoutEffect(() => {
    const unsubscribe = subscribeAuthTransition(() => {
      if (!isAuthTransitionActive()) return;
      invalidate();
      setState({ status: "idle" });
    });
    // Background tabs and sleeping machines throttle timers; leaving the page discards the estimate.
    const leave = () => {
      invalidate();
      setState((previous) => {
        if (previous.status === "ready") return { status: "expired" };
        if (previous.status === "loading") return { status: "idle" };
        return previous;
      });
    };
    const onVisibilityChange = () => {
      if (document.visibilityState === "hidden") leave();
    };
    document.addEventListener("visibilitychange", onVisibilityChange);
    window.addEventListener("blur", leave);
    return () => {
      unsubscribe();
      document.removeEventListener("visibilitychange", onVisibilityChange);
      window.removeEventListener("blur", leave);
      invalidate();
    };
  }, [invalidate]);

  const estimate = () => {
    if (pendingRef.current || disabled || !allowed()) return;
    invalidate();
    const attempt = attemptRef.current;
    const controller = new AbortController();
    controllerRef.current = controller;
    pendingRef.current = true;
    setState({ status: "loading" });
    const startedAt = performance.now();
    // Superseded attempts are ignored; a current attempt whose identity is no longer valid is retired
    // so it can neither publish a window nor leave the single-flight lock held.
    const owns = () => {
      if (attemptRef.current !== attempt) return false;
      if (allowed()) return true;
      invalidate();
      setState({ status: "idle" });
      return false;
    };
    const settle = () => {
      pendingRef.current = false;
      controllerRef.current = null;
    };

    reportsApi.previewSchedule(token, cron, { signal: controller.signal }).then(
      (data) => {
        if (!owns()) return;
        settle();
        // The server lifetime is measured from evaluation; the full round trip is charged against it
        // and the remainder runs on the monotonic timer, never compared with the browser wall clock.
        const remainingMs = Date.parse(data.expiresAt) - Date.parse(data.evaluatedAt) - (performance.now() - startedAt);
        if (remainingMs <= 0) {
          setState({ status: "expired" });
          return;
        }
        setState({ status: "ready", data });
        timerRef.current = window.setTimeout(() => {
          timerRef.current = null;
          if (attemptRef.current !== attempt) return;
          setState({ status: "expired" });
        }, remainingMs);
      },
      (error: unknown) => {
        if (!owns()) return;
        settle();
        setState({ status: "error", kind: classifyFailure(error) });
      },
    );
  };

  const authorized = allowed();
  const visible: PreviewState = authorized || state.status === "idle" ? state : { status: "idle" };
  const loading = visible.status === "loading";
  const forbiddenRole = active && Boolean(token) && role !== "admin";
  const refreshLabel = visible.status === "ready" || visible.status === "expired";

  return (
    <div className="mt-2 space-y-2 border-t border-border/60 pt-2">
      <p className="text-xs text-muted-foreground">{t("cron.reportPreviewBoundary")}</p>
      <div className="flex flex-wrap items-center gap-2">
        <Button
          type="button"
          size="sm"
          variant="outline"
          onClick={estimate}
          disabled={disabled || loading || transitioning || !authorized}
          aria-busy={loading || undefined}
        >
          <CalendarClock className="size-3.5" aria-hidden />
          {t(refreshLabel ? "cron.reportPreviewRefresh" : "cron.reportPreviewAction")}
        </Button>
      </div>
      <div role="status" aria-live="polite" aria-atomic="true" className="space-y-1 text-xs">
        {forbiddenRole ? (
          <p className="text-muted-foreground">{t("cron.reportPreviewForbidden")}</p>
        ) : visible.status === "idle" ? (
          <p className="text-muted-foreground">{t("cron.reportPreviewIdle")}</p>
        ) : visible.status === "loading" ? (
          <p className="text-muted-foreground">{t("cron.reportPreviewLoading")}</p>
        ) : visible.status === "expired" ? (
          <p className="text-muted-foreground">{t("cron.reportPreviewExpired")}</p>
        ) : visible.status === "error" ? (
          <p className="text-[hsl(var(--warning-text))]">{t(FAILURE_KEYS[visible.kind])}</p>
        ) : (
          <PreviewResult data={visible.data} />
        )}
      </div>
    </div>
  );
}

function PreviewResult({ data }: { data: ReportSchedulePreviewData }) {
  const { t } = useTranslation();
  const basis = (
    <p className="text-muted-foreground">
      {t("cron.reportPreviewBasis", {
        timezone: data.timezone,
        time: data.evaluatedAt,
      })}
    </p>
  );

  if (data.status === "no_match") {
    return (
      <>
        <p className="font-medium text-[hsl(var(--warning-text))]">{t(NO_MATCH_KEYS[data.reason])}</p>
        {basis}
      </>
    );
  }

  // Timestamps stay exactly as the server returned them; parsing is only for the instant comparison.
  const current = Date.parse(data.windowStart) <= Date.parse(data.evaluatedAt);
  return (
    <>
      <p className="font-medium text-[hsl(var(--info-text))]">
        {t(current ? "cron.reportPreviewCurrent" : "cron.reportPreviewNext", {
          start: data.windowStart,
          end: data.windowEnd,
        })}
      </p>
      {basis}
    </>
  );
}
