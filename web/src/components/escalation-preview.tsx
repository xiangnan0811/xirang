import {
  useCallback,
  useLayoutEffect,
  useRef,
  useState,
  useSyncExternalStore,
  type ReactNode,
} from "react";
import { useTranslation } from "react-i18next";
import {
  AlertTriangle,
  ChevronDown,
  ChevronUp,
  Loader2,
  Play,
  RotateCcw,
  Sparkles,
} from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { TagChips } from "@/components/ui/tag-chips";
import { useAuth } from "@/context/auth-context.hooks";
import {
  ApiError,
  getAuthIdentitySnapshot,
  getAuthSessionGeneration,
  isAuthTransitionActive,
  subscribeAuthSessionGeneration,
  subscribeAuthTransition,
} from "@/lib/api/core";
import type { EscalationPolicyInput } from "@/lib/api/escalation";
import type {
  DraftEscalationScenario,
  EscalationPreviewResult,
  EscalationPreviewSeverity,
  EscalationPreviewStep,
} from "@/lib/api/escalation-preview";

type FailureKind = "forbidden" | "unavailable" | "rateLimited" | "failed";

type PreviewState =
  | { status: "idle" }
  | { status: "loading" }
  | { status: "ready"; data: EscalationPreviewResult }
  | { status: "expired" }
  | { status: "error"; kind: FailureKind };

const FAILURE_KEYS: Record<FailureKind, string> = {
  forbidden: "escalation.preview.forbidden",
  unavailable: "escalation.preview.unavailable",
  rateLimited: "escalation.preview.rateLimited",
  failed: "escalation.preview.failed",
};

// Field-level draft bounds. They stay in lockstep with the pre-dispatch guard in
// lib/api/escalation-preview.ts, which is lazy-loaded and therefore not imported here.
const MAX_SCENARIO_TAGS = 10;
const MAX_TAG_BYTES = 32;
const MAX_ERROR_CODE_BYTES = 128;
const MAX_NAME_BYTES = 100;
const MAX_DESCRIPTION_BYTES = 2000;
const MAX_LEVELS = 5;
const MAX_CHANNELS_PER_LEVEL = 100;
/** floor(MaxInt64 / 1e9): the backend rejects longer delays as unrepresentable durations. */
const MAX_DELAY_SECONDS = 9_223_372_036;
const SEVERITIES: readonly string[] = ["info", "warning", "critical"];
/** Same set as Go unicode.IsControl (C0, DEL, C1); a code-point check avoids control-char regex literals. */
function hasControlCharacter(value: string): boolean {
  for (const char of value) {
    const code = char.codePointAt(0)!;
    if (code <= 0x1f || (code >= 0x7f && code <= 0x9f)) return true;
  }
  return false;
}

const textEncoder = new TextEncoder();

function utf8Bytes(value: string): number {
  return textEncoder.encode(value).length;
}

function classifyFailure(error: unknown): FailureKind {
  if (error instanceof ApiError) {
    const status = error.httpStatus ?? error.status;
    if (status === 403) return "forbidden";
    if (status === 404) return "unavailable";
    if (status === 429) return "rateLimited";
  }
  return "failed";
}

function formatUtcDatetimeLocal(date: Date): string {
  const pad = (value: number, width = 2) => value.toString().padStart(width, "0");
  return `${pad(date.getUTCFullYear(), 4)}-${pad(date.getUTCMonth() + 1)}-${pad(date.getUTCDate())}T${pad(
    date.getUTCHours()
  )}:${pad(date.getUTCMinutes())}:${pad(date.getUTCSeconds())}`;
}

/**
 * Interprets a datetime-local value strictly as UTC fields and re-emits them verbatim as RFC3339 UTC.
 * No Date arithmetic is involved, so years 1-99 are not remapped to 19xx and nothing is read in the
 * browser time zone.
 */
function parseUtcDatetimeLocal(value: string): string | null {
  // Browsers (and jsdom) normalize step=1 values to "HH:MM:SS.000"; only an all-zero fraction is accepted
  // because the scenario contract is whole UTC seconds.
  const match = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})(?::(\d{2})(?:\.0{1,3})?)?$/.exec(value.trim());
  if (!match) return null;
  const [, yearText, monthText, dayText, hourText, minuteText, secondText = "00"] = match;
  const [year, month, day, hour, minute, second] = [yearText, monthText, dayText, hourText, minuteText, secondText].map(Number);
  const leap = year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0);
  const monthDays = month === 2 ? (leap ? 29 : 28) : [4, 6, 9, 11].includes(month) ? 30 : 31;
  if (year < 1 || month < 1 || month > 12 || day < 1 || day > monthDays || hour > 23 || minute > 59 || second > 59) {
    return null;
  }
  return `${yearText}-${monthText}-${dayText}T${hourText}:${minuteText}:${secondText}Z`;
}

function isPreviewablePolicy(policy: EscalationPolicyInput): boolean {
  const nameBytes = utf8Bytes(policy.name.trim());
  const levels = policy.levels;
  return (
    nameBytes >= 1 &&
    nameBytes <= MAX_NAME_BYTES &&
    utf8Bytes(policy.description ?? "") <= MAX_DESCRIPTION_BYTES &&
    SEVERITIES.includes(policy.minSeverity) &&
    levels.length >= 1 &&
    levels.length <= MAX_LEVELS &&
    levels.every(
      (level, index) =>
        Number.isSafeInteger(level.delaySeconds) &&
        level.delaySeconds <= MAX_DELAY_SECONDS &&
        (index === 0 ? level.delaySeconds === 0 : level.delaySeconds > levels[index - 1].delaySeconds) &&
        level.integrationIds.length >= 1 &&
        level.integrationIds.length <= MAX_CHANNELS_PER_LEVEL &&
        level.integrationIds.every((id) => Number.isSafeInteger(id) && id > 0) &&
        (level.severityOverride === "" || SEVERITIES.includes(level.severityOverride)) &&
        level.tags.length <= MAX_SCENARIO_TAGS &&
        level.tags.every((tag) => utf8Bytes(tag) <= MAX_TAG_BYTES)
    )
  );
}

export type EscalationDraftPreviewProps = {
  policy: EscalationPolicyInput;
  token: string;
  active: boolean;
  disabled?: boolean;
  integrationsFailed?: boolean;
};

/**
 * Draft preview embedded in EscalationPolicyEditor. The session key covers token, role and auth
 * generation, so any identity change discards scenario inputs and results (A→B→A never restores).
 * Draft or scenario edits keep the inputs but remount the runner, discarding its result immediately.
 */
export function EscalationDraftPreview({
  policy,
  token,
  active,
  disabled = false,
  integrationsFailed = false,
}: EscalationDraftPreviewProps): ReactNode {
  const { role } = useAuth();
  const generation = useSyncExternalStore(subscribeAuthSessionGeneration, getAuthSessionGeneration, () => 0);

  return (
    <EscalationDraftPreviewSession
      key={`${token}:${role ?? ""}:${generation}`}
      policy={policy}
      token={token}
      active={active}
      role={role}
      generation={generation}
      disabled={disabled}
      integrationsFailed={integrationsFailed}
    />
  );
}

function EscalationDraftPreviewSession({
  policy,
  token,
  active,
  role,
  generation,
  disabled,
  integrationsFailed,
}: {
  policy: EscalationPolicyInput;
  token: string;
  active: boolean;
  role: string | null;
  generation: number;
  disabled: boolean;
  integrationsFailed: boolean;
}) {
  const { t } = useTranslation();
  const [expanded, setExpanded] = useState(false);
  const [nodeIdText, setNodeIdText] = useState("0");
  const [errorCode, setErrorCode] = useState("");
  const [severity, setSeverity] = useState<EscalationPreviewSeverity>("warning");
  const [tags, setTags] = useState<string[]>([]);
  const [triggeredAtText, setTriggeredAtText] = useState("");
  const [evaluationAtText, setEvaluationAtText] = useState("");

  const toggleExpanded = () => {
    if (!expanded && (!triggeredAtText || !evaluationAtText)) {
      // Both defaults capture the same instant once; they never advance on their own.
      const nowUtc = formatUtcDatetimeLocal(new Date());
      setTriggeredAtText(nowUtc);
      setEvaluationAtText(nowUtc);
    }
    setExpanded((previous) => !previous);
  };

  const nodeId = /^\d+$/.test(nodeIdText.trim()) ? Number(nodeIdText.trim()) : Number.NaN;
  const cleanErrorCode = errorCode.trim();
  const errorCodeBytes = utf8Bytes(cleanErrorCode);
  const triggeredAt = parseUtcDatetimeLocal(triggeredAtText);
  const evaluationAt = parseUtcDatetimeLocal(evaluationAtText);

  const scenarioErrors = {
    nodeId: Number.isSafeInteger(nodeId) ? null : t("escalation.preview.scenario.invalidNodeId"),
    errorCode:
      errorCodeBytes >= 1 && errorCodeBytes <= MAX_ERROR_CODE_BYTES && !hasControlCharacter(cleanErrorCode)
        ? null
        : t("escalation.preview.scenario.invalidErrorCode"),
    tags:
      tags.length <= MAX_SCENARIO_TAGS && tags.every((tag) => utf8Bytes(tag) <= MAX_TAG_BYTES)
        ? null
        : t("escalation.preview.scenario.invalidTags"),
    triggeredAt: triggeredAt ? null : t("escalation.preview.scenario.invalidTriggeredAt"),
    evaluationAt: evaluationAt ? null : t("escalation.preview.scenario.invalidEvaluationAt"),
  };
  const policyValid = isPreviewablePolicy(policy);
  const scenarioValid = Object.values(scenarioErrors).every((error) => error === null);

  const scenario: DraftEscalationScenario = {
    nodeId,
    errorCode: cleanErrorCode,
    severity,
    tags,
    triggeredAt: triggeredAt ?? "",
    evaluationAt: evaluationAt ?? "",
  };
  const runnerDisabled = disabled || integrationsFailed || !policyValid || !scenarioValid;

  const fieldError = (message: string | null, id?: string) =>
    message ? <p id={id} className="text-[11px] text-destructive">{message}</p> : null;

  return (
    <div className="space-y-3 rounded-lg border border-border bg-card/60 p-3">
      <button
        type="button"
        onClick={toggleExpanded}
        className="flex w-full items-center justify-between text-left text-sm font-semibold transition-colors hover:text-primary"
        aria-expanded={expanded}
      >
        <span className="flex items-center gap-1.5">
          <Sparkles className="size-4 text-primary" aria-hidden="true" />
          {t("escalation.preview.draftTitle")}
        </span>
        {expanded ? <ChevronUp className="size-4" aria-hidden="true" /> : <ChevronDown className="size-4" aria-hidden="true" />}
      </button>

      {expanded && (
        <div className="space-y-4 pt-1">
          <p className="text-xs text-muted-foreground">{t("escalation.preview.draftHint")}</p>

          <div className="space-y-3 rounded border border-border/80 bg-background/50 p-3">
            <span className="text-xs font-semibold text-foreground/90">{t("escalation.preview.scenario.title")}</span>

            <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
              <div className="space-y-1">
                <label htmlFor="preview-node-id" className="text-xs font-medium">
                  {t("escalation.preview.scenario.nodeId")}
                </label>
                <Input
                  id="preview-node-id"
                  aria-invalid={Boolean(scenarioErrors.nodeId)}
                  aria-describedby={scenarioErrors.nodeId ? "preview-node-id-error" : undefined}
                  inputMode="numeric"
                  value={nodeIdText}
                  onChange={(event) => setNodeIdText(event.target.value)}
                  placeholder="0"
                />
                <p className="text-[11px] text-muted-foreground">{t("escalation.preview.scenario.nodeIdHint")}</p>
                {fieldError(scenarioErrors.nodeId, "preview-node-id-error")}
              </div>

              <div className="space-y-1">
                <label htmlFor="preview-error-code" className="text-xs font-medium">
                  {t("escalation.preview.scenario.errorCode")}
                </label>
                <Input
                  id="preview-error-code"
                  aria-invalid={Boolean(scenarioErrors.errorCode)}
                  aria-describedby={scenarioErrors.errorCode ? "preview-error-code-error" : undefined}
                  type="text"
                  value={errorCode}
                  onChange={(event) => setErrorCode(event.target.value)}
                  placeholder={t("escalation.preview.scenario.errorCodePlaceholder")}
                />
                {fieldError(scenarioErrors.errorCode, "preview-error-code-error")}
              </div>

              <div className="space-y-1">
                <label htmlFor="preview-severity" className="text-xs font-medium">
                  {t("escalation.preview.scenario.severity")}
                </label>
                <Select
                  id="preview-severity"
                  value={severity}
                  onChange={(event) => setSeverity(event.target.value as EscalationPreviewSeverity)}
                >
                  <option value="info">{t("escalation.severity.info")}</option>
                  <option value="warning">{t("escalation.severity.warning")}</option>
                  <option value="critical">{t("escalation.severity.critical")}</option>
                </Select>
              </div>

              <div className="space-y-1">
                <span id="preview-tags-label" className="text-xs font-medium">{t("escalation.preview.scenario.tags")}</span>
                <TagChips value={tags} onChange={setTags} placeholder="tags" aria-labelledby="preview-tags-label" />
                {fieldError(scenarioErrors.tags)}
              </div>

              <div className="space-y-1">
                <label htmlFor="preview-triggered-at" className="text-xs font-medium">
                  {t("escalation.preview.scenario.triggeredAt")}
                </label>
                <Input
                  id="preview-triggered-at"
                  aria-invalid={Boolean(scenarioErrors.triggeredAt)}
                  aria-describedby={scenarioErrors.triggeredAt ? "preview-triggered-at-error" : undefined}
                  type="datetime-local"
                  step={1}
                  value={triggeredAtText}
                  onChange={(event) => setTriggeredAtText(event.target.value)}
                />
                {fieldError(scenarioErrors.triggeredAt, "preview-triggered-at-error")}
              </div>

              <div className="space-y-1">
                <label htmlFor="preview-evaluation-at" className="text-xs font-medium">
                  {t("escalation.preview.scenario.evaluationAt")}
                </label>
                <Input
                  id="preview-evaluation-at"
                  aria-invalid={Boolean(scenarioErrors.evaluationAt)}
                  aria-describedby={scenarioErrors.evaluationAt ? "preview-evaluation-at-error" : undefined}
                  type="datetime-local"
                  step={1}
                  value={evaluationAtText}
                  onChange={(event) => setEvaluationAtText(event.target.value)}
                />
                {fieldError(scenarioErrors.evaluationAt, "preview-evaluation-at-error")}
              </div>
            </div>
          </div>

          {integrationsFailed ? (
            <div className="flex items-center gap-2 rounded border border-destructive/30 bg-destructive/10 p-2.5 text-xs text-destructive">
              <AlertTriangle className="size-4 shrink-0" aria-hidden="true" />
              <span>{t("escalation.preview.integrationsUnavailable")}</span>
            </div>
          ) : !policyValid ? (
            <p className="text-xs text-destructive">{t("escalation.preview.policyInvalid")}</p>
          ) : null}

          {/* Every input the server sees, plus enablement, is part of the key: any change discards the result. */}
          <EscalationPreviewRunner
            key={JSON.stringify([policy, scenario, runnerDisabled, active])}
            mode="draft"
            policy={policy}
            scenario={scenario}
            alertId={null}
            token={token}
            active={active}
            role={role}
            generation={generation}
            disabled={runnerDisabled}
          />
        </div>
      )}
    </div>
  );
}

export type AlertEscalationPreviewProps = {
  alertId: number | null;
  token: string;
  active: boolean;
  revisionKey: string;
};

/**
 * Read-only diagnosis for an existing alert. Admin only; renders nothing (and never requests)
 * otherwise. The session key covers the alert's public fields and identity, and the parent
 * unmounts this component whenever its alert list is cleared for a refresh.
 */
export function AlertEscalationPreview({ alertId, token, active, revisionKey }: AlertEscalationPreviewProps): ReactNode {
  const { t } = useTranslation();
  const { role } = useAuth();
  const generation = useSyncExternalStore(subscribeAuthSessionGeneration, getAuthSessionGeneration, () => 0);

  if (!token || role !== "admin") return null;

  return (
    <div className="mt-2 space-y-2 border-t border-border/60 pt-2 text-xs">
      {alertId === null ? (
        <>
          <Button type="button" size="sm" variant="outline" disabled>
            <Sparkles className="mr-1 size-3.5" aria-hidden="true" />
            {t("escalation.preview.diagnoseAction")}
          </Button>
          <p className="text-muted-foreground">{t("escalation.preview.invalidAlertId")}</p>
        </>
      ) : (
        <EscalationPreviewRunner
          key={JSON.stringify([alertId, revisionKey, active, token, role, generation])}
          mode="alert"
          policy={null}
          scenario={null}
          alertId={alertId}
          token={token}
          active={active}
          role={role}
          generation={generation}
          disabled={false}
        />
      )}
    </div>
  );
}

function EscalationPreviewRunner({
  mode,
  policy,
  scenario,
  alertId,
  token,
  active,
  role,
  generation,
  disabled,
}: {
  mode: "draft" | "alert";
  policy: EscalationPolicyInput | null;
  scenario: DraftEscalationScenario | null;
  alertId: number | null;
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
    const reset = () => {
      invalidate();
      setState({ status: "idle" });
    };
    const unsubscribeTransition = subscribeAuthTransition(() => {
      if (isAuthTransitionActive()) reset();
    });
    const unsubscribeGeneration = subscribeAuthSessionGeneration(reset);

    // Leaving the page cancels in-flight work and retires a shown snapshot rather than keeping it fresh.
    const leave = () => {
      invalidate();
      setState((previous) =>
        previous.status === "ready" ? { status: "expired" } : previous.status === "loading" ? { status: "idle" } : previous
      );
    };
    const onVisibilityChange = () => {
      if (document.visibilityState === "hidden") leave();
    };
    document.addEventListener("visibilitychange", onVisibilityChange);
    window.addEventListener("blur", leave);

    return () => {
      unsubscribeTransition();
      unsubscribeGeneration();
      document.removeEventListener("visibilitychange", onVisibilityChange);
      window.removeEventListener("blur", leave);
      invalidate();
    };
  }, [invalidate]);

  const runPreview = async () => {
    if (pendingRef.current || disabled || !allowed()) return;
    invalidate();
    const attempt = attemptRef.current;
    const controller = new AbortController();
    controllerRef.current = controller;
    pendingRef.current = true;
    setState({ status: "loading" });
    const startedAt = performance.now();

    // A superseded attempt is ignored; a current attempt whose identity no longer holds is retired
    // so it can neither publish a result nor keep the single-flight lock.
    const owns = () => {
      if (attemptRef.current !== attempt) return false;
      if (allowed()) return true;
      invalidate();
      setState({ status: "idle" });
      return false;
    };

    try {
      // Deliberately lazy (approved plan): the strict response decoder stays out of page chunks until a click.
      const api = await import("@/lib/api/escalation-preview");
      if (!owns()) return;
      const result =
        mode === "draft"
          ? await api.previewEscalationDraft(token, { policy: policy!, scenario: scenario! }, { signal: controller.signal })
          : await api.previewAlertEscalation(token, alertId!, { signal: controller.signal });
      if (!owns()) return;
      pendingRef.current = false;
      controllerRef.current = null;

      // The decoder guarantees expires_at - generated_at === PREVIEW_TTL_MS; the remainder runs on the
      // monotonic clock after subtracting this round trip and is never compared with the wall clock.
      const remainingMs = api.PREVIEW_TTL_MS - (performance.now() - startedAt);
      if (remainingMs <= 0) {
        setState({ status: "expired" });
        return;
      }
      setState({ status: "ready", data: result });
      timerRef.current = window.setTimeout(() => {
        timerRef.current = null;
        if (owns()) setState({ status: "expired" });
      }, remainingMs);
    } catch (error: unknown) {
      if (!owns()) return;
      pendingRef.current = false;
      controllerRef.current = null;
      setState({ status: "error", kind: classifyFailure(error) });
    }
  };

  const authorized = allowed();
  const visible: PreviewState = authorized ? state : { status: "idle" };
  const loading = visible.status === "loading";
  const refreshLabel = visible.status === "ready" || visible.status === "expired";
  const actionKey =
    mode === "draft"
      ? refreshLabel
        ? "escalation.preview.refresh"
        : "escalation.preview.action"
      : refreshLabel
        ? "escalation.preview.diagnoseRefresh"
        : "escalation.preview.diagnoseAction";

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center gap-2">
        <Button
          type="button"
          size="sm"
          variant="outline"
          onClick={() => void runPreview()}
          disabled={disabled || loading || transitioning || !authorized}
          aria-busy={loading || undefined}
        >
          {loading ? (
            <Loader2 className="mr-1 size-3.5 animate-spin" aria-hidden="true" />
          ) : refreshLabel ? (
            <RotateCcw className="mr-1 size-3.5" aria-hidden="true" />
          ) : mode === "draft" ? (
            <Play className="mr-1 size-3.5" aria-hidden="true" />
          ) : (
            <Sparkles className="mr-1 size-3.5" aria-hidden="true" />
          )}
          {t(actionKey)}
        </Button>
      </div>

      <div role="status" aria-live="polite" aria-atomic="true" className="space-y-2 text-xs">
        {role !== "admin" ? (
          <p className="text-muted-foreground">{t("escalation.preview.forbidden")}</p>
        ) : visible.status === "idle" ? (
          <p className="text-muted-foreground">
            {t(mode === "draft" ? "escalation.preview.idle" : "escalation.preview.diagnoseIdle")}
          </p>
        ) : visible.status === "loading" ? (
          <p className="text-muted-foreground">
            {t(mode === "draft" ? "escalation.preview.loading" : "escalation.preview.diagnosing")}
          </p>
        ) : visible.status === "expired" ? (
          <p className="text-muted-foreground">{t("escalation.preview.expired")}</p>
        ) : visible.status === "error" ? (
          <div className="flex flex-wrap items-center gap-2 text-destructive">
            <AlertTriangle className="size-4 shrink-0" aria-hidden="true" />
            <span>{t(FAILURE_KEYS[visible.kind])}</span>
            <Button
              type="button"
              size="sm"
              variant="ghost"
              className="h-6 px-2 text-xs"
              disabled={disabled || transitioning}
              onClick={() => void runPreview()}
            >
              {t("escalation.preview.retryAction")}
            </Button>
          </div>
        ) : (
          <EscalationPreviewResultView data={visible.data} />
        )}
      </div>
    </div>
  );
}

const NON_ESCALATING_KEYS = {
  no_policy: "escalation.preview.status.noPolicy",
  disabled: "escalation.preview.status.disabled",
  below_minimum: "escalation.preview.status.belowMinimum",
} as const;

function EscalationPreviewResultView({ data }: { data: EscalationPreviewResult }) {
  const { t } = useTranslation();
  const supported = data.status !== "unsupported_state";

  return (
    <div className="space-y-3 pt-1">
      <p className="text-[11px] leading-relaxed text-muted-foreground">
        {t("escalation.preview.boundary", { time: data.evaluationAt })}
      </p>

      {!supported && (
        <div className="space-y-1.5 rounded-lg border border-warning/40 bg-warning/10 p-3">
          <div className="flex items-center gap-1.5 font-medium text-[hsl(var(--warning-text))]">
            <AlertTriangle className="size-4 shrink-0 text-warning" aria-hidden="true" />
            <span>{t("escalation.preview.status.unsupportedState")}</span>
          </div>
          <ul className="list-disc space-y-0.5 pl-4 text-xs text-muted-foreground">
            {data.warnings.map((warning) => (
              <li key={warning}>{t(`escalation.preview.warnings.${warning}`)}</li>
            ))}
          </ul>
        </div>
      )}

      {supported && (
        <div className="flex flex-wrap items-center justify-between gap-2 border-b border-border/60 pb-2">
          <div className="flex min-w-0 flex-wrap items-center gap-1.5">
            {data.policy && <span className="break-all font-semibold text-foreground">{data.policy.name}</span>}
            <Badge tone="neutral">
              {t(`escalation.preview.policySource.${data.policySource}`)}
              {data.sourceResourceId !== null ? ` #${data.sourceResourceId}` : ""}
            </Badge>
          </div>
          {data.nextDueAt && (
            <div className="text-[11px] text-muted-foreground">
              <span>{t("escalation.preview.nextDue", { time: data.nextDueAt })}</span>
              {data.nextDueNow && <span className="ml-1 font-medium text-primary">{t("escalation.preview.nextDueReady")}</span>}
            </div>
          )}
        </div>
      )}

      {supported && data.warnings.length > 0 && (
        <div className="space-y-1">
          {data.warnings.map((warning) => (
            <div
              key={warning}
              className="flex items-center gap-1.5 rounded border border-warning/30 bg-warning/5 px-2.5 py-1 text-[11px] text-[hsl(var(--warning-text))]"
            >
              <AlertTriangle className="size-3 shrink-0" aria-hidden="true" />
              <span>{t(`escalation.preview.warnings.${warning}`)}</span>
            </div>
          ))}
        </div>
      )}

      {data.status === "not_open" && (
        <div className="space-y-2 rounded-lg border border-border bg-card p-3">
          <p className="font-medium text-foreground">{t("escalation.preview.status.notOpen")}</p>
          <p className="text-muted-foreground">{t("escalation.preview.resolvedNotice")}</p>
        </div>
      )}

      {(data.status === "no_policy" || data.status === "disabled" || data.status === "below_minimum") && (
        <div className="rounded-lg border border-border bg-card p-3">
          <p className="font-medium text-foreground">{t(NON_ESCALATING_KEYS[data.status])}</p>
        </div>
      )}

      {data.status === "complete" && (
        <div className="rounded-lg border border-border bg-card p-3">
          <p className="font-medium text-foreground">{t("escalation.preview.status.complete")}</p>
        </div>
      )}

      {data.steps.length > 0 && (
        <ol className="space-y-2" aria-label={t("escalation.preview.stepsLabel")}>
          {data.steps.map((step) => (
            <EscalationStepCard key={step.levelIndex} step={step} />
          ))}
        </ol>
      )}

      {data.deliveryDecision && data.deliveryCounts && (
        <div className="space-y-1 rounded border border-border/80 bg-muted/20 p-2.5 text-xs">
          <span className="font-semibold text-foreground/90">{t("escalation.preview.deliveryTitle")}</span>
          <p className="text-muted-foreground">
            {t("escalation.preview.deliveryDecision", {
              decision: t(`escalation.preview.decision.${data.deliveryDecision}`),
            })}
          </p>
          <p className="text-muted-foreground">{t("escalation.preview.deliveryCounts", data.deliveryCounts)}</p>
        </div>
      )}
    </div>
  );
}

const STEP_TONES = {
  would_enqueue: "success",
  silenced: "warning",
  no_enabled_channels: "destructive",
  blocked_minimum: "neutral",
  blocked_prior: "neutral",
} as const;

const CHANNEL_CLASSES = {
  enabled: "border-primary/40 bg-primary/10 text-primary",
  disabled: "border-border bg-muted/60 text-muted-foreground",
  missing: "border-destructive/40 bg-destructive/10 text-destructive",
} as const;

function EscalationStepCard({ step }: { step: EscalationPreviewStep }) {
  const { t } = useTranslation();
  const severityBefore = t(`escalation.severity.${step.severityBefore}`);
  const severityAfter = t(`escalation.severity.${step.severityAfter}`);

  return (
    <li className="space-y-2 rounded-lg border border-border bg-card p-3 text-xs">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex items-center gap-2">
          <span className="font-semibold text-foreground">{t("escalation.levels.level", { n: step.levelIndex + 1 })}</span>
          <span className="text-muted-foreground">{t("escalation.preview.delayOffset", { seconds: step.delaySeconds })}</span>
        </div>
        <Badge tone={STEP_TONES[step.status]}>{t(`escalation.preview.stepStatus.${step.status}`)}</Badge>
      </div>

      <dl className="grid grid-cols-1 gap-1 text-[11px] text-muted-foreground sm:grid-cols-2">
        <div>
          <dt className="inline">{t("escalation.preview.dueAtLabel")}</dt> <dd className="inline break-all">{step.dueAt}</dd>
        </div>
        <div>
          <dt className="inline">{t("escalation.preview.projectedAtLabel")}</dt>{" "}
          <dd className="inline break-all">{step.projectedAt ?? t("escalation.preview.notProjected")}</dd>
        </div>
      </dl>

      <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-[11px]">
        <span className="text-foreground/90">
          {step.severityBefore === step.severityAfter
            ? t("escalation.preview.severity", { severity: severityAfter })
            : t("escalation.preview.severityChange", { before: severityBefore, after: severityAfter })}
        </span>
        {step.tagsAfter.length > 0 && (
          <span className="break-all text-muted-foreground">{t("escalation.preview.tags", { tags: step.tagsAfter.join(", ") })}</span>
        )}
      </div>

      {step.matchedSilenceIds.length > 0 && (
        <p className="break-words text-[11px] font-medium text-[hsl(var(--warning-text))]">
          {t("escalation.preview.silencesMatched", { ids: step.matchedSilenceIds.map((id) => `#${id}`).join(", ") })}
        </p>
      )}

      <ul className="flex flex-wrap gap-1.5 pt-1">
        {step.channels.map((channel) => (
          <li
            key={channel.id}
            className={`inline-flex max-w-full items-center gap-1 rounded-full border px-2.5 py-0.5 text-[11px] font-medium ${CHANNEL_CLASSES[channel.state]}`}
          >
            <span className="break-all">{channel.name ?? `#${channel.id}`}</span>
            {channel.type && <span className="text-[10px] opacity-70">({channel.type})</span>}
            <span className="text-[10px]">· {t(`escalation.preview.channelState.${channel.state}`)}</span>
          </li>
        ))}
      </ul>
    </li>
  );
}
