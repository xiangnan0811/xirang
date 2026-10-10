import { useEffect, useLayoutEffect, useRef, useState } from "react";
import type { ReactElement, RefObject } from "react";
import { Link } from "react-router-dom";
import { useTranslation } from "react-i18next";
import type { TFunction } from "i18next";
import { Button } from "@/components/ui/button";
import { FormDialog } from "@/components/ui/form-dialog";
import { Input } from "@/components/ui/input";
import { Select } from "@/components/ui/select";
import { TagChips } from "@/components/ui/tag-chips";
import { toast } from "@/components/ui/toast-sonner";
import { useAuth } from "@/context/auth-context.hooks";
import type { AuthRole } from "@/context/auth-context.shared";
import {
  isQuickSilenceCategory,
  isQuickSilenceNodeId,
  type QuickSilenceMatch,
} from "@/lib/alert-silence-match";
import { apiClient } from "@/lib/api/client";
import { ApiError, getAuthIdentitySnapshot, getAuthSessionGeneration, isAuthTransitionActive, subscribeAuthTransition } from "@/lib/api/core";
import type { SilenceInput } from "@/lib/api/silences";
import { SILENCE_CATEGORIES } from "@/lib/silence-categories";
import { getErrorMessage } from "@/lib/utils";
import type { NodeRecord } from "@/types/domain";

const SILENCE_RULES_PATH = "/app/settings?tab=silences";
const DATETIME_LOCAL_PATTERN = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})$/;

type SilenceWindowEnd = {
  text: string;
  /** Preset instant. Cleared on edit so a repeated clock time is not reparsed into the other DST occurrence. */
  instant: Date | null;
};

type SilenceWindowState = {
  start: SilenceWindowEnd;
  end: SilenceWindowEnd;
};

type SilenceFieldErrors = {
  name?: string;
  start?: string;
  end?: string;
};

type QuickNodeState =
  | { status: "idle" | "loading" | "missing" | "mismatch" | "error" | "invalid" }
  | { status: "ready"; nodeId: number; name: string };

type SilenceOwner = {
  localGeneration: number;
  authGeneration: number;
  token: string;
};

export type CreateSilenceDialogProps = {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onCreated: () => void;
  token: string;
  initialMatch?: QuickSilenceMatch;
};

function pad2(value: number): string {
  return value.toString().padStart(2, "0");
}

function formatDatetimeLocal(date: Date): string {
  return `${date.getFullYear().toString().padStart(4, "0")}-${pad2(date.getMonth() + 1)}-${pad2(date.getDate())}T${pad2(date.getHours())}:${pad2(date.getMinutes())}`;
}

function formatSignedOffset(date: Date): string {
  const totalMinutes = -date.getTimezoneOffset();
  const sign = totalMinutes >= 0 ? "+" : "-";
  const absolute = Math.abs(totalMinutes);
  return `${sign}${pad2(Math.floor(absolute / 60))}:${pad2(absolute % 60)}`;
}

function floorToMinute(date: Date): Date {
  return new Date(Math.floor(date.getTime() / 60_000) * 60_000);
}

/** Strict YYYY-MM-DDTHH:mm. Round-trip rejects impossible dates and spring-forward gaps. */
function parseDatetimeLocal(value: string): Date | null {
  const match = DATETIME_LOCAL_PATTERN.exec(value);
  if (!match) return null;
  const year = Number(match[1]);
  const month = Number(match[2]);
  const day = Number(match[3]);
  const hour = Number(match[4]);
  const minute = Number(match[5]);
  if (month < 1 || month > 12 || day < 1 || day > 31 || hour > 23 || minute > 59) return null;
  const parsed = new Date(value);
  if (
    parsed.getFullYear() !== year ||
    parsed.getMonth() !== month - 1 ||
    parsed.getDate() !== day ||
    parsed.getHours() !== hour ||
    parsed.getMinutes() !== minute
  ) {
    return null;
  }
  return parsed;
}

function windowFromNow(now: Date, hours: number): SilenceWindowState {
  const start = floorToMinute(now);
  const end = new Date(start.getTime() + hours * 3_600_000);
  return {
    start: { text: formatDatetimeLocal(start), instant: start },
    end: { text: formatDatetimeLocal(end), instant: end },
  };
}

function resolveWindowEnd(end: SilenceWindowEnd): Date | null {
  return end.instant ?? parseDatetimeLocal(end.text);
}

function formatWindowSummary(start: Date, end: Date, t: TFunction): string {
  const point = (date: Date) =>
    `${formatDatetimeLocal(date)} ${t("silences.utcOffset", { offset: formatSignedOffset(date) })}`;
  return `${point(start)} → ${point(end)}`;
}

function draftKeyFor(input: {
  open: boolean;
  role: AuthRole | null;
  authToken: string | null;
  token: string;
  matchKey: string;
}): string {
  return [
    input.open ? "open" : "closed",
    input.role ?? "",
    input.authToken ?? "",
    input.token,
    input.matchKey,
  ].join("\0");
}

function isNotFound(error: unknown): boolean {
  return error instanceof ApiError && (error.status === 404 || error.httpStatus === 404);
}

function isAbortError(error: unknown): boolean {
  if (error instanceof DOMException && error.name === "AbortError") return true;
  return error instanceof Error && error.name === "AbortError";
}

function initialQuickNode(quick: boolean, nodeId: unknown, category: unknown): QuickNodeState {
  if (!quick) return { status: "idle" };
  if (!isQuickSilenceNodeId(nodeId) || !isQuickSilenceCategory(category)) return { status: "invalid" };
  return { status: "loading" };
}

function SilenceWindowField({
  id,
  label,
  end,
  error,
  disabled,
  inputRef,
  onValueChange,
}: {
  id: string;
  label: string;
  end: SilenceWindowEnd;
  error?: string;
  disabled: boolean;
  inputRef: RefObject<HTMLInputElement>;
  onValueChange: (value: string) => void;
}) {
  const { t } = useTranslation();
  const instant = resolveWindowEnd(end);
  const offset = instant ? t("silences.utcOffset", { offset: formatSignedOffset(instant) }) : null;
  const offsetId = offset ? `${id}-offset` : undefined;
  const errorId = error ? `${id}-error` : undefined;
  const describedBy = [offsetId, errorId].filter((item): item is string => Boolean(item)).join(" ");

  return (
    <div className="min-w-0 space-y-1">
      <div className="flex items-baseline justify-between gap-2">
        <label htmlFor={id} className="text-xs text-muted-foreground">
          {label}
        </label>
        {offset ? (
          <span id={offsetId} className="text-xs font-medium tabular-nums text-foreground">
            {offset}
          </span>
        ) : null}
      </div>
      <Input
        ref={inputRef}
        id={id}
        aria-label={label}
        aria-invalid={Boolean(error)}
        aria-describedby={describedBy || undefined}
        type="datetime-local"
        value={end.text}
        disabled={disabled}
        onChange={(event) => onValueChange(event.target.value)}
      />
      {error ? (
        <p id={errorId} role="alert" className="text-xs text-destructive">
          {error}
        </p>
      ) : null}
    </div>
  );
}

function QuickNodeNotice({
  state,
  onRetry,
}: {
  state: QuickNodeState;
  onRetry: () => void;
}) {
  const { t } = useTranslation();
  if (state.status === "idle" || state.status === "ready") return null;
  if (state.status === "loading") {
    return (
      <p role="status" className="text-sm text-muted-foreground">
        {t("silences.quick.nodeLoading")}
      </p>
    );
  }
  const message = state.status === "missing"
    ? t("silences.quick.nodeMissing")
    : state.status === "mismatch"
      ? t("silences.quick.nodeMismatch")
      : state.status === "invalid"
        ? t("silences.quick.rangeInvalid")
        : t("silences.quick.nodeFailed");
  const canRetry = state.status !== "invalid";
  return (
    <div role="alert" className="space-y-2">
      <p className="text-sm text-destructive">{message}</p>
      {canRetry ? (
        <Button type="button" size="sm" variant="outline" onClick={onRetry}>
          {t("silences.quick.nodeRetry")}
        </Button>
      ) : null}
    </div>
  );
}

export function CreateSilenceDialog({
  open,
  onOpenChange,
  onCreated,
  token,
  initialMatch,
}: CreateSilenceDialogProps): ReactElement | null {
  const { t } = useTranslation();
  const { token: authToken, role } = useAuth();
  const quick = initialMatch !== undefined;
  const quickNodeId = initialMatch?.nodeId;
  const quickCategory = initialMatch?.category;
  const matchKey = quick ? `${String(quickNodeId)}\0${quickCategory ?? ""}` : "";
  const draftKey = draftKeyFor({ open, role, authToken, token, matchKey });
  const [seenDraft, setSeenDraft] = useState(draftKey);
  const [name, setName] = useState("");
  const [matchNodeId, setMatchNodeId] = useState("");
  const [matchCategory, setMatchCategory] = useState("");
  const [tags, setTags] = useState<string[]>([]);
  const [silenceWindow, setSilenceWindow] = useState(() => windowFromNow(new Date(), 1));
  const [note, setNote] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [fieldErrors, setFieldErrors] = useState<SilenceFieldErrors>({});
  const [nodes, setNodes] = useState<NodeRecord[]>([]);
  const [quickNode, setQuickNode] = useState<QuickNodeState>(() => initialQuickNode(quick, quickNodeId, quickCategory));
  const [retryNonce, setRetryNonce] = useState(0);
  const [sealed, setSealed] = useState(false);
  const nameRef = useRef<HTMLInputElement>(null);
  const startRef = useRef<HTMLInputElement>(null);
  const endRef = useRef<HTMLInputElement>(null);
  const mountedRef = useRef(false);
  const localGenerationRef = useRef(0);
  const submitLockRef = useRef(false);
  const submitSerialRef = useRef(0);
  const nodeAttemptRef = useRef(0);
  const nodeAbortRef = useRef<AbortController | null>(null);
  const identityRef = useRef({ token: authToken, role });
  const onOpenChangeRef = useRef(onOpenChange);
  const onCreatedRef = useRef(onCreated);

  if (seenDraft !== draftKey) {
    setSeenDraft(draftKey);
    setName("");
    setMatchNodeId("");
    setMatchCategory("");
    setTags([]);
    setNote("");
    setFieldErrors({});
    setSilenceWindow(windowFromNow(new Date(), 1));
    setQuickNode(initialQuickNode(quick, quickNodeId, quickCategory));
    setSubmitting(false);
    setRetryNonce(0);
  }

  if (open && !sealed && (role !== "admin" || !authToken || authToken !== token)) {
    setSealed(true);
  }

  const allowed = open
    && !sealed
    && role === "admin"
    && typeof authToken === "string"
    && authToken.length > 0
    && authToken === token;

  /**
   * createSilence cannot be aborted. A settled response is applied only when
   * this mount generation, the auth generation, and the admin identity still
   * match the values captured before the request. AbortSignal alone is not
   * that fence: a late response can ignore cancellation.
   */
  const captureOwner = (): SilenceOwner | null => {
    if (!mountedRef.current || sealed) return null;
    if (isAuthTransitionActive()) return null;
    const currentToken = identityRef.current.token;
    const currentRole = identityRef.current.role;
    if (currentRole !== "admin" || !currentToken || currentToken !== token) return null;
    const snapshot = getAuthIdentitySnapshot();
    if (snapshot.token !== currentToken || snapshot.role !== "admin") return null;
    return {
      localGeneration: localGenerationRef.current,
      authGeneration: getAuthSessionGeneration(),
      token: currentToken,
    };
  };

  const ownerCurrent = (owner: SilenceOwner): boolean => {
    if (!mountedRef.current || sealed) return false;
    if (localGenerationRef.current !== owner.localGeneration) return false;
    if (isAuthTransitionActive()) return false;
    if (getAuthSessionGeneration() !== owner.authGeneration) return false;
    if (identityRef.current.token !== owner.token || identityRef.current.role !== "admin") return false;
    const snapshot = getAuthIdentitySnapshot();
    return snapshot.token === owner.token && snapshot.role === "admin";
  };


  useLayoutEffect(() => {
    onOpenChangeRef.current = onOpenChange;
    onCreatedRef.current = onCreated;
  }, [onOpenChange, onCreated]);

  useLayoutEffect(() => {
    mountedRef.current = true;
    identityRef.current = { token: authToken, role };
    localGenerationRef.current += 1;
    return () => {
      mountedRef.current = false;
      localGenerationRef.current += 1;
      submitLockRef.current = false;
      nodeAbortRef.current?.abort();
      nodeAbortRef.current = null;
    };
  }, [authToken, role, token, open, quickNodeId, quickCategory]);

  useLayoutEffect(() => {
    const seal = () => {
      localGenerationRef.current += 1;
      submitLockRef.current = false;
      nodeAbortRef.current?.abort();
      nodeAbortRef.current = null;
      setSealed(true);
      onOpenChangeRef.current(false);
    };
    if (isAuthTransitionActive()) seal();
    return subscribeAuthTransition(() => {
      if (!isAuthTransitionActive()) return;
      seal();
    });
  }, [authToken, role, token, open]);

  useLayoutEffect(() => {
    if (!open) return;
    if (role === "admin" && authToken && authToken === token) return;
    localGenerationRef.current += 1;
    submitLockRef.current = false;
    nodeAbortRef.current?.abort();
    nodeAbortRef.current = null;
    onOpenChangeRef.current(false);
  }, [open, sealed, role, authToken, token]);

  useEffect(() => {
    if (!open || sealed || role !== "admin" || !authToken || authToken !== token) return;
    const owner = captureOwner();
    if (!owner) return;
    const attempt = ++nodeAttemptRef.current;
    const controller = new AbortController();
    nodeAbortRef.current = controller;

    if (!quick) {
      void apiClient.getNodes(owner.token, { signal: controller.signal }).then((data) => {
        if (nodeAttemptRef.current !== attempt || !ownerCurrent(owner)) return;
        setNodes(data);
      }).catch(() => {
        // The previous settings dialog ignored a failed node list and kept the wildcard option.
      });
      return () => {
        controller.abort();
        if (nodeAbortRef.current === controller) nodeAbortRef.current = null;
      };
    }

    if (typeof quickNodeId !== "number" || !isQuickSilenceNodeId(quickNodeId) || !isQuickSilenceCategory(quickCategory)) {
      return () => {
        controller.abort();
        if (nodeAbortRef.current === controller) nodeAbortRef.current = null;
      };
    }

    const nodeId = quickNodeId;
    void apiClient.getNode(owner.token, nodeId, { signal: controller.signal }).then((node) => {
      if (nodeAttemptRef.current !== attempt) return;
      if (!ownerCurrent(owner)) return;
      if (node.id !== nodeId) {
        setQuickNode({ status: "mismatch" });
        return;
      }
      setQuickNode({ status: "ready", nodeId, name: node.name });
    }).catch((error: unknown) => {
      if (nodeAttemptRef.current !== attempt) return;
      if (!ownerCurrent(owner)) return;
      if (isAbortError(error)) return;
      setQuickNode({ status: isNotFound(error) ? "missing" : "error" });
    });
    return () => {
      controller.abort();
      if (nodeAbortRef.current === controller) nodeAbortRef.current = null;
    };
    // Owner capture reads refs that the layout effects above maintain.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, sealed, role, authToken, token, quick, quickNodeId, quickCategory, retryNonce]);

  const editingLocked = () => submitLockRef.current;

  const applyPreset = (hours: number) => {
    if (editingLocked()) return;
    setSilenceWindow(windowFromNow(new Date(), hours));
    setFieldErrors((current) => (
      current.start || current.end ? { ...current, start: undefined, end: undefined } : current
    ));
  };

  const editWindowEnd = (which: "start" | "end", text: string) => {
    if (editingLocked()) return;
    if (silenceWindow[which].text === text) return;
    setSilenceWindow((current) => ({
      ...current,
      [which]: { text, instant: null },
    }));
    setFieldErrors((current) => (
      current.start || current.end ? { ...current, start: undefined, end: undefined } : current
    ));
  };

  const handleSubmit = async () => {
    if (submitLockRef.current) return;
    const owner = captureOwner();
    if (!owner) return;

    const startInstant = resolveWindowEnd(silenceWindow.start);
    const endInstant = resolveWindowEnd(silenceWindow.end);
    const nextErrors: SilenceFieldErrors = {};
    if (!name.trim()) nextErrors.name = t("silences.nameRequired");
    if (!startInstant) nextErrors.start = t("silences.validationDateInvalid");
    if (!endInstant) nextErrors.end = t("silences.validationDateInvalid");
    if (startInstant && endInstant && endInstant.getTime() <= startInstant.getTime()) {
      nextErrors.end = t("silences.validationWindowInvalid");
    }
    if (nextErrors.name || nextErrors.start || nextErrors.end || !startInstant || !endInstant) {
      setFieldErrors(nextErrors);
      if (nextErrors.name) nameRef.current?.focus();
      else if (nextErrors.start) startRef.current?.focus();
      else if (nextErrors.end) endRef.current?.focus();
      return;
    }

    let matchNodeIdValue: number | null;
    let matchCategoryValue: string;
    let matchTagsValue: string[];
    if (initialMatch) {
      if (!isQuickSilenceNodeId(initialMatch.nodeId) || !isQuickSilenceCategory(initialMatch.category)) return;
      if (!(quickNode.status === "ready" && quickNode.nodeId === initialMatch.nodeId)) return;
      matchNodeIdValue = initialMatch.nodeId;
      matchCategoryValue = initialMatch.category;
      matchTagsValue = [];
    } else {
      matchNodeIdValue = matchNodeId ? Number(matchNodeId) : null;
      matchCategoryValue = matchCategory;
      matchTagsValue = tags;
    }

    const serial = ++submitSerialRef.current;
    submitLockRef.current = true;
    setSubmitting(true);
    const payload: SilenceInput = {
      name: name.trim(),
      matchNodeId: matchNodeIdValue,
      matchCategory: matchCategoryValue,
      matchTags: matchTagsValue,
      startsAt: startInstant.toISOString(),
      endsAt: endInstant.toISOString(),
      note: note.trim() || undefined,
    };
    const savedWindow = formatWindowSummary(startInstant, endInstant, t);
    try {
      await apiClient.createSilence(owner.token, payload);
      if (!ownerCurrent(owner)) return;
      toast.success(t("silences.created", { window: savedWindow }));
      if (!ownerCurrent(owner)) return;
      onOpenChangeRef.current(false);
      if (!ownerCurrent(owner)) return;
      onCreatedRef.current();
    } catch (error: unknown) {
      if (!ownerCurrent(owner)) return;
      toast.error(getErrorMessage(error));
    } finally {
      if (submitSerialRef.current === serial && ownerCurrent(owner)) {
        submitLockRef.current = false;
        setSubmitting(false);
      }
    }
  };

  if (!allowed || !open) return null;

  const quickReady = Boolean(
    initialMatch
    && quickNode.status === "ready"
    && quickNode.nodeId === initialMatch.nodeId
    && isQuickSilenceNodeId(initialMatch.nodeId)
    && isQuickSilenceCategory(initialMatch.category),
  );
  const formReady = !initialMatch || quickReady;
  const readyName = quickReady && quickNode.status === "ready" ? quickNode.name.trim() : "";
  const startInstant = resolveWindowEnd(silenceWindow.start);
  const endInstant = resolveWindowEnd(silenceWindow.end);
  const windowText = formReady && startInstant && endInstant
    ? formatWindowSummary(startInstant, endInstant, t)
    : null;

  return (
    <FormDialog
      open={open}
      onOpenChange={onOpenChange}
      title={t("silences.new")}
      description={initialMatch ? t("silences.quick.rangeExplanation") : t("silences.dialogDesc")}
      size="md"
      saving={submitting || Boolean(initialMatch && !quickReady)}
      onSubmit={handleSubmit}
      submitLabel={t("silences.create")}
      savingLabel={submitting ? t("silences.creating") : t("silences.quick.submitLocked")}
    >
      {initialMatch ? (
        <div className="min-w-0 space-y-2">
          <div className="min-w-0 space-y-1">
            <p className="text-xs font-medium text-muted-foreground">{t("silences.quick.scopeLabel")}</p>
            <p className="break-all text-sm font-medium text-foreground">
              <span>{`#${initialMatch.nodeId}`}</span>
              {readyName ? (
                <>
                  <span aria-hidden="true">{" · "}</span>
                  <span>{readyName}</span>
                </>
              ) : null}
              <span aria-hidden="true">{" · "}</span>
              <span>{initialMatch.category}</span>
            </p>
          </div>
          <p className="text-xs leading-5 text-muted-foreground">{t("silences.quick.effectExplanation")}</p>
          {windowText ? (
            <div className="min-w-0 space-y-1">
              <p className="text-xs font-medium text-muted-foreground">{t("silences.quick.windowLabel")}</p>
              <p className="break-all text-xs font-medium tabular-nums text-foreground">{windowText}</p>
            </div>
          ) : null}
          <Button asChild variant="link" size="sm" className="h-auto px-0">
            <Link to={SILENCE_RULES_PATH}>{t("silences.quick.rulesLink")}</Link>
          </Button>
        </div>
      ) : null}

      {initialMatch && !formReady ? (
        <QuickNodeNotice state={quickNode} onRetry={() => {
          setQuickNode({ status: "loading" });
          setRetryNonce((current) => current + 1);
        }} />
      ) : null}

      {formReady ? (
        <>
          <div className="space-y-1">
            <label htmlFor="silence-name" className="text-sm font-medium">
              {t("silences.name")}
            </label>
            <Input
              ref={nameRef}
              id="silence-name"
              aria-label={t("silences.name")}
              aria-invalid={Boolean(fieldErrors.name)}
              aria-describedby={fieldErrors.name ? "silence-name-error" : undefined}
              value={name}
              disabled={submitting}
              onChange={(event) => {
                if (editingLocked()) return;
                setName(event.target.value);
                setFieldErrors((current) => (current.name ? { ...current, name: undefined } : current));
              }}
              placeholder="维护窗口-A"
            />
            {fieldErrors.name ? (
              <p id="silence-name-error" role="alert" className="text-xs text-destructive">
                {fieldErrors.name}
              </p>
            ) : null}
          </div>

          {initialMatch ? null : (
            <>
              <div className="space-y-1">
                <label htmlFor="silence-node" className="text-sm font-medium">
                  {t("silences.node")}
                  <span className="ml-1 text-xs text-muted-foreground">({t("silences.nodeHint")})</span>
                </label>
                <Select
                  id="silence-node"
                  value={matchNodeId}
                  disabled={submitting}
                  onChange={(event) => {
                    if (editingLocked()) return;
                    setMatchNodeId(event.target.value);
                  }}
                >
                  <option value="">{t("silences.nodeAll")}</option>
                  {nodes.map((node) => (
                    <option key={node.id} value={String(node.id)}>
                      {node.name}
                    </option>
                  ))}
                </Select>
              </div>

              <div className="space-y-1">
                <label htmlFor="silence-category" className="text-sm font-medium">
                  {t("silences.category")}
                  <span className="ml-1 text-xs text-muted-foreground">({t("silences.categoryHint")})</span>
                </label>
                <Select
                  id="silence-category"
                  value={matchCategory}
                  disabled={submitting}
                  onChange={(event) => {
                    if (editingLocked()) return;
                    setMatchCategory(event.target.value);
                  }}
                >
                  <option value="">{t("silences.categoryAll")}</option>
                  {SILENCE_CATEGORIES.map((type) => (
                    <option key={type.value} value={type.value}>
                      {t(type.i18nKey)}
                    </option>
                  ))}
                </Select>
              </div>

              <div className="space-y-1">
                <label className="text-sm font-medium">{t("silences.tags")}</label>
                <TagChips
                  value={tags}
                  onChange={(next) => {
                    if (editingLocked()) return;
                    setTags(next);
                  }}
                  placeholder={t("silences.tagsHint")}
                />
              </div>
            </>
          )}

          <div className="space-y-1">
            <div className="flex flex-wrap items-center gap-2">
              <label className="text-sm font-medium">{t("silences.window")}</label>
              {[
                { label: t("silences.preset1h"), h: 1 },
                { label: t("silences.preset4h"), h: 4 },
                { label: t("silences.preset1d"), h: 24 },
              ].map((preset) => (
                <Button
                  key={preset.h}
                  size="sm"
                  variant="outline"
                  type="button"
                  disabled={submitting}
                  onClick={() => applyPreset(preset.h)}
                >
                  {preset.label}
                </Button>
              ))}
            </div>
            <p className="text-xs text-muted-foreground">{t("silences.presetDurationHint")}</p>
            <div className="mt-2 grid grid-cols-1 gap-3 sm:grid-cols-2">
              <SilenceWindowField
                id="silence-starts"
                label={t("silences.startsAt")}
                end={silenceWindow.start}
                error={fieldErrors.start}
                disabled={submitting}
                inputRef={startRef}
                onValueChange={(value) => editWindowEnd("start", value)}
              />
              <SilenceWindowField
                id="silence-ends"
                label={t("silences.endsAt")}
                end={silenceWindow.end}
                error={fieldErrors.end}
                disabled={submitting}
                inputRef={endRef}
                onValueChange={(value) => editWindowEnd("end", value)}
              />
            </div>
          </div>

          <div className="space-y-1">
            <label htmlFor="silence-note" className="text-sm font-medium">
              {t("silences.note")}
            </label>
            <Input
              id="silence-note"
              value={note}
              disabled={submitting}
              onChange={(event) => {
                if (editingLocked()) return;
                setNote(event.target.value);
              }}
              placeholder={t("silences.noteHint")}
            />
          </div>
        </>
      ) : null}
    </FormDialog>
  );
}
