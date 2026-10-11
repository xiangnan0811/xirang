import { useEffect, useLayoutEffect, useRef, useState, type FormEvent } from "react";
import { useTranslation } from "react-i18next";
import { Check, Copy, TriangleAlert } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogBody,
  DialogCloseButton,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { useAuth } from "@/context/auth-context.hooks";
import type { AuthRole } from "@/context/auth-context.shared";
import { ApiError, getAuthIdentitySnapshot, getAuthSessionGeneration } from "@/lib/api/core";
import { apiClient } from "@/lib/api/client";
import { classifyRecoveryCodeRegenerationFailure, totpVerifyErrorCode } from "@/lib/api/totp-api";

type Phase = "closed" | "confirm" | "submitting" | "codes" | "unknown";
type NoticeKey = "codeInvalid" | "conflict" | "disabled" | "badRequest" | "unauthorized" | "rateLimited";
type UnknownKind = "generic" | "badRequest" | "rateLimited";

type Stamp = {
  token: string;
  role: AuthRole | null;
  generation: number;
};

type CountState =
  | { status: "idle" | "loading" | "error" | "withheld" | "unavailable" }
  | { status: "ready"; remaining: number };

function readStamp(): Stamp | null {
  const current = getAuthIdentitySnapshot();
  if (!current.token) return null;
  return {
    token: current.token,
    role: current.role,
    generation: getAuthSessionGeneration(),
  };
}

function stampsEqual(left: Stamp, right: Stamp): boolean {
  return left.token === right.token && left.role === right.role && left.generation === right.generation;
}

function stampOwnsSession(
  stamp: Stamp,
  token: string | null,
  role: AuthRole | null,
  totpEnabled: boolean,
  live: Stamp | null,
): boolean {
  return Boolean(
    totpEnabled
    && token
    && live
    && stamp.token === token
    && stamp.role === role
    && stampsEqual(stamp, live),
  );
}

function confirmedHttpStatus(error: unknown): number | null {
  if (!(error instanceof ApiError)) return null;
  if (typeof error.httpStatus !== "number" || error.httpStatus !== error.status) return null;
  return error.httpStatus;
}

const copyKeys = "settings.account.recoveryCodes";

function RecoveryUncertaintyWarning({ id, text }: { id?: string; text: string }) {
  return (
    <p
      id={id}
      role="alert"
      className="flex min-w-0 gap-2 rounded-lg border border-warning/40 bg-warning/10 px-3 py-2 text-sm text-[hsl(var(--warning-text))]"
    >
      <TriangleAlert className="mt-0.5 size-4 shrink-0" aria-hidden />
      <span className="min-w-0 break-words">{text}</span>
    </p>
  );
}

export function RecoveryCodesManager() {
  const { t } = useTranslation();
  const { token, role, totpEnabled } = useAuth();
  const [phase, setPhase] = useState<Phase>("closed");
  const [otp, setOtp] = useState("");
  const [codes, setCodes] = useState<string[]>([]);
  const [countState, setCountState] = useState<CountState>({ status: "idle" });
  const [owner, setOwner] = useState<Stamp | null>(null);
  const [dialogNotice, setDialogNotice] = useState<NoticeKey | null>(null);
  const [panelNotice, setPanelNotice] = useState<Extract<NoticeKey, "disabled" | "unauthorized"> | null>(null);
  const [unknownKind, setUnknownKind] = useState<UnknownKind>("generic");
  const [inventoryUncertain, setInventoryUncertain] = useState(false);
  const [copied, setCopied] = useState(false);
  const [copyFailed, setCopyFailed] = useState(false);
  const [sessionEpoch, setSessionEpoch] = useState(0);

  const mountedRef = useRef(true);
  const operationEpochRef = useRef(0);
  const countEpochRef = useRef(0);
  const submittingRef = useRef(false);
  const tokenRef = useRef<string | null>(token);
  const roleRef = useRef<AuthRole | null>(role);
  const totpRef = useRef(totpEnabled);
  const phaseRef = useRef<Phase>("closed");
  const codesRef = useRef<string[]>([]);
  const ownerMirrorRef = useRef<Stamp | null>(null);
  const handledEpochRef = useRef(0);
  const otpRef = useRef<HTMLInputElement | null>(null);
  const triggerRef = useRef<HTMLButtonElement | null>(null);
  const copyTimerRef = useRef<number | null>(null);
  const readCountRef = useRef<() => void>(() => {});

  const generation = getAuthSessionGeneration();
  const live = readStamp();
  const aligned = Boolean(token && totpEnabled && live && live.token === token && live.role === role);
  const mismatch = owner !== null && !stampOwnsSession(owner, token, role, totpEnabled, live);

  if (mismatch) {
    setSessionEpoch(sessionEpoch + 1);
    setPhase("closed");
    setOtp("");
    setCodes([]);
    setCountState({ status: "idle" });
    setOwner(null);
    setDialogNotice(null);
    setPanelNotice(null);
    setUnknownKind("generic");
    setInventoryUncertain(false);
    setCopied(false);
    setCopyFailed(false);
  }

  const uiPhase: Phase = mismatch ? "closed" : phase;
  const uiCodes = mismatch ? [] : codes;
  const uiOtp = mismatch ? "" : otp;
  const uiCount = mismatch ? { status: "idle" as const } : countState;
  const uiDialogNotice = mismatch ? null : dialogNotice;
  const uiPanelNotice = mismatch ? null : panelNotice;
  const uiUnknownKind = mismatch ? "generic" : unknownKind;
  const uiUncertain = mismatch ? false : inventoryUncertain;
  const uiCopied = mismatch ? false : copied;
  const uiCopyFailed = mismatch ? false : copyFailed;
  const closeLocked = uiPhase === "submitting" || uiPhase === "codes";

  function owns(stamp: Stamp, opEpoch: number): boolean {
    const current = readStamp();
    return mountedRef.current
      && operationEpochRef.current === opEpoch
      && stampOwnsSession(stamp, tokenRef.current, roleRef.current, totpRef.current, current);
  }

  useLayoutEffect(() => {
    tokenRef.current = token;
    roleRef.current = role;
    totpRef.current = totpEnabled;
    phaseRef.current = phase;
    codesRef.current = codes;
    readCountRef.current = () => {
      const stamp = readStamp();
      if (!stamp || !stampOwnsSession(stamp, tokenRef.current, roleRef.current, totpRef.current, stamp)) {
        return;
      }
      const requestEpoch = ++countEpochRef.current;
      const countStillOwned = () => {
        const current = readStamp();
        return mountedRef.current
          && countEpochRef.current === requestEpoch
          && stampOwnsSession(stamp, tokenRef.current, roleRef.current, totpRef.current, current);
      };
      setOwner(stamp);
      setCountState({ status: "loading" });
      void (async () => {
        try {
          const result = await apiClient.recoveryCodesRemaining(stamp.token);
          if (!countStillOwned()) return;
          setOwner(stamp);
          setCountState({ status: "ready", remaining: result.remaining });
        } catch {
          if (!countStillOwned()) return;
          setOwner(stamp);
          setCountState({ status: "error" });
        }
      })();
    };
  }, [codes, phase, role, token, totpEnabled]);

  useLayoutEffect(() => {
    if (sessionEpoch === handledEpochRef.current) return;
    handledEpochRef.current = sessionEpoch;
    operationEpochRef.current += 1;
    countEpochRef.current += 1;
    submittingRef.current = false;
    phaseRef.current = "closed";
    codesRef.current = [];
  }, [sessionEpoch]);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      operationEpochRef.current += 1;
      submittingRef.current = false;
      if (copyTimerRef.current !== null) {
        window.clearTimeout(copyTimerRef.current);
        copyTimerRef.current = null;
      }
    };
  }, []);

  useEffect(() => {
    readCountRef.current();
    return () => {
      countEpochRef.current += 1;
    };
  }, [token, role, totpEnabled, generation]);

  useLayoutEffect(() => {
    ownerMirrorRef.current = mismatch ? null : owner;
  }, [mismatch, owner]);

  useLayoutEffect(() => {
    if (uiPhase !== "confirm") return;
    const frame = requestAnimationFrame(() => {
      if (phaseRef.current === "confirm") {
        otpRef.current?.focus({ preventScroll: true });
      }
    });
    return () => cancelAnimationFrame(frame);
  }, [uiPhase]);

  function clearCopyTimer() {
    if (copyTimerRef.current !== null) {
      window.clearTimeout(copyTimerRef.current);
      copyTimerRef.current = null;
    }
  }

  function dismissDialog() {
    if (submittingRef.current || phaseRef.current === "submitting" || phaseRef.current === "codes") return;
    operationEpochRef.current += 1;
    clearCopyTimer();
    phaseRef.current = "closed";
    setPhase("closed");
    setOtp("");
    setCodes([]);
    codesRef.current = [];
    setDialogNotice(null);
    setCopied(false);
    setCopyFailed(false);
  }

  function acknowledgeSaved() {
    operationEpochRef.current += 1;
    submittingRef.current = false;
    clearCopyTimer();
    phaseRef.current = "closed";
    codesRef.current = [];
    setPhase("closed");
    setOtp("");
    setCodes([]);
    setDialogNotice(null);
    setCopied(false);
    setCopyFailed(false);
  }

  function openConfirm() {
    if (submittingRef.current || phaseRef.current === "submitting" || phaseRef.current === "codes") return;
    const stamp = readStamp();
    if (!stamp || !stampOwnsSession(stamp, tokenRef.current, roleRef.current, totpRef.current, stamp)) return;
    clearCopyTimer();
    setOwner(stamp);
    setOtp("");
    setCodes([]);
    codesRef.current = [];
    setDialogNotice(null);
    setCopied(false);
    setCopyFailed(false);
    phaseRef.current = "confirm";
    setPhase("confirm");
  }

  function enterUnknown(kind: UnknownKind, stamp: Stamp) {
    submittingRef.current = false;
    countEpochRef.current += 1;
    clearCopyTimer();
    codesRef.current = [];
    setOtp("");
    setCodes([]);
    setCopied(false);
    setCopyFailed(false);
    setDialogNotice(null);
    setOwner(stamp);
    setInventoryUncertain(true);
    setUnknownKind(kind);
    setCountState({ status: "withheld" });
    phaseRef.current = "unknown";
    setPhase("unknown");
  }

  function handleSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (submittingRef.current || phaseRef.current !== "confirm") return;
    const code = otp.trim();
    if (!code) return;
    const stamp = readStamp();
    if (!stamp || !stampOwnsSession(stamp, tokenRef.current, roleRef.current, totpRef.current, stamp)) {
      dismissDialog();
      return;
    }
    submittingRef.current = true;
    phaseRef.current = "submitting";
    const opEpoch = operationEpochRef.current;
    setOwner(stamp);
    setPhase("submitting");
    setDialogNotice(null);
    setOtp(code);
    void (async () => {
      try {
        const data = await apiClient.regenerateRecoveryCodes(stamp.token, code);
        if (!owns(stamp, opEpoch)) return;
        if (data.recoveryCodes.length === 0) {
          enterUnknown("generic", stamp);
          return;
        }
        submittingRef.current = false;
        countEpochRef.current += 1;
        clearCopyTimer();
        codesRef.current = data.recoveryCodes;
        setInventoryUncertain(false);
        setPanelNotice(null);
        setDialogNotice(null);
        setUnknownKind("generic");
        setOtp("");
        setCodes(data.recoveryCodes);
        setCopied(false);
        setCopyFailed(false);
        setOwner(stamp);
        setCountState({ status: "ready", remaining: data.recoveryCodes.length });
        phaseRef.current = "codes";
        setPhase("codes");
      } catch (error) {
        if (!owns(stamp, opEpoch)) return;
        const outcome = classifyRecoveryCodeRegenerationFailure(error);
        if (outcome === "no_commit") {
          submittingRef.current = false;
          setOtp("");
          const machineCode = totpVerifyErrorCode(error);
          if (machineCode === "RECOVERY_CODE_TOTP_DISABLED") {
            countEpochRef.current += 1;
            clearCopyTimer();
            codesRef.current = [];
            setCodes([]);
            setCopied(false);
            setCopyFailed(false);
            setDialogNotice(null);
            setInventoryUncertain(false);
            setCountState({ status: "unavailable" });
            setPanelNotice("disabled");
            phaseRef.current = "closed";
            setPhase("closed");
            return;
          }
          setDialogNotice(machineCode === "RECOVERY_CODE_CONFLICT" ? "conflict" : "codeInvalid");
          phaseRef.current = "confirm";
          setPhase("confirm");
          return;
        }
        const status = confirmedHttpStatus(error);
        if (status === 401) {
          submittingRef.current = false;
          countEpochRef.current += 1;
          clearCopyTimer();
          codesRef.current = [];
          setOtp("");
          setCodes([]);
          setCopied(false);
          setCopyFailed(false);
          setDialogNotice(null);
          setOwner(stamp);
          setInventoryUncertain(true);
          setCountState({ status: "unavailable" });
          setPanelNotice("unauthorized");
          phaseRef.current = "closed";
          setPhase("closed");
          return;
        }
        const kind: UnknownKind = status === 400 ? "badRequest" : status === 429 ? "rateLimited" : "generic";
        enterUnknown(kind, stamp);
      } finally {
        if (operationEpochRef.current === opEpoch) {
          submittingRef.current = false;
        }
      }
    })();
  }

  async function handleCopyCodes() {
    const stamp = ownerMirrorRef.current;
    const liveStamp = readStamp();
    const snapshot = codesRef.current;
    if (
      uiPhase !== "codes"
      || !stamp
      || !liveStamp
      || !stampsEqual(stamp, liveStamp)
      || snapshot.length === 0
    ) {
      return;
    }
    const opEpoch = operationEpochRef.current;
    const text = snapshot.join("\n");
    try {
      await navigator.clipboard.writeText(text);
      if (!owns(stamp, opEpoch) || codesRef.current.join("\n") !== text) return;
      setCopyFailed(false);
      setCopied(true);
      clearCopyTimer();
      copyTimerRef.current = window.setTimeout(() => {
        if (owns(stamp, opEpoch)) setCopied(false);
      }, 2000);
    } catch {
      if (!owns(stamp, opEpoch) || codesRef.current.join("\n") !== text) return;
      setCopied(false);
      setCopyFailed(true);
    }
  }

  if (!aligned) return null;

  const countText = uiCount.status === "ready"
    ? (uiCount.remaining === 0
      ? t(`${copyKeys}.exhausted`)
      : t(`${copyKeys}.remaining`, { count: uiCount.remaining }))
    : null;
  const unknownText = uiUnknownKind === "badRequest"
    ? t(`${copyKeys}.badRequest`)
    : uiUnknownKind === "rateLimited"
      ? t(`${copyKeys}.rateLimited`)
      : t(`${copyKeys}.unknown`);
  const description = uiPhase === "codes"
    ? t(`${copyKeys}.codesIntro`)
    : uiPhase === "submitting"
      ? t(`${copyKeys}.submitting`)
      : t(`${copyKeys}.dialogDescription`);
  const showUncertaintyWarning = uiUncertain && uiPhase !== "unknown";

  return (
    <section aria-labelledby="recovery-codes-heading" className="min-w-0 space-y-3 border-t border-border pt-3">
      <h4 id="recovery-codes-heading" className="text-sm font-medium">
        {t(`${copyKeys}.title`)}
      </h4>
      {showUncertaintyWarning && uiPhase === "closed" && !uiPanelNotice ? (
        <RecoveryUncertaintyWarning text={unknownText} />
      ) : null}
      <div aria-live="polite" aria-atomic="true" className="min-w-0 space-y-2">
        {uiCount.status === "loading" ? (
          <p role="status" className="text-sm text-muted-foreground">{t(`${copyKeys}.loading`)}</p>
        ) : null}
        {uiCount.status === "error" ? (
          <p role="alert" className="text-sm text-destructive">{t(`${copyKeys}.loadFailed`)}</p>
        ) : null}
        {countText ? (
          <p className="break-words text-sm text-foreground">{countText}</p>
        ) : null}
        {uiCount.status === "withheld" ? (
          <p role="status" className="break-words text-sm text-muted-foreground">{t(`${copyKeys}.withheld`)}</p>
        ) : null}
        {uiCount.status === "unavailable" ? (
          <p role="status" className="break-words text-sm text-muted-foreground">{t(`${copyKeys}.countHidden`)}</p>
        ) : null}
        {uiUncertain && uiCount.status === "ready" ? (
          <p className="break-words text-sm text-muted-foreground">{t(`${copyKeys}.countDoesNotProve`)}</p>
        ) : null}
      </div>
      {uiPanelNotice ? (
        <p role="alert" className="break-words rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive">
          {t(`${copyKeys}.${uiPanelNotice}`)}
        </p>
      ) : null}
      <div className="flex min-w-0 flex-wrap gap-2">
        {uiCount.status === "error" ? (
          <Button type="button" variant="outline" className="h-auto max-w-full whitespace-normal" onClick={() => readCountRef.current()}>
            {t(`${copyKeys}.retry`)}
          </Button>
        ) : null}
        {uiCount.status === "ready" || uiCount.status === "withheld" || uiCount.status === "unavailable" ? (
          <Button type="button" variant="outline" className="h-auto max-w-full whitespace-normal" onClick={() => readCountRef.current()}>
            {t(`${copyKeys}.refresh`)}
          </Button>
        ) : null}
        <Button
          ref={triggerRef}
          type="button"
          className="h-auto max-w-full whitespace-normal text-center"
          disabled={uiPhase === "submitting" || uiPhase === "codes"}
          onClick={openConfirm}
        >
          {t(`${copyKeys}.regenerate`)}
        </Button>
      </div>

      <Dialog open={uiPhase !== "closed"} onOpenChange={(open) => { if (!open) dismissDialog(); }}>
        <DialogContent
          size="sm"
          className="max-w-[calc(100%-2rem)]"
          {...(uiPhase === "unknown" ? { "aria-describedby": "recovery-codes-unknown-warning" } : {})}
          aria-busy={uiPhase === "submitting" || undefined}
          onOpenAutoFocus={(event) => {
            event.preventDefault();
            otpRef.current?.focus({ preventScroll: true });
          }}
          onCloseAutoFocus={(event) => {
            const trigger = triggerRef.current;
            if (!trigger?.isConnected) return;
            event.preventDefault();
            trigger.focus();
          }}
          onEscapeKeyDown={(event) => {
            if (closeLocked) event.preventDefault();
          }}
          onPointerDownOutside={(event) => {
            if (closeLocked) event.preventDefault();
          }}
          onInteractOutside={(event) => {
            if (closeLocked) event.preventDefault();
          }}
        >
          <DialogHeader>
            <DialogTitle>{t(`${copyKeys}.dialogTitle`)}</DialogTitle>
            {uiPhase === "unknown" ? null : (
              <DialogDescription className="break-words">{description}</DialogDescription>
            )}
            <DialogCloseButton disabled={closeLocked} />
          </DialogHeader>
          <DialogBody className="min-w-0 space-y-4">
            {uiPhase === "confirm" || uiPhase === "submitting" ? (
              <>
                {showUncertaintyWarning ? <RecoveryUncertaintyWarning text={unknownText} /> : null}
                <div className="flex min-w-0 gap-2 rounded-lg border border-warning/40 bg-warning/10 px-3 py-2 text-sm text-[hsl(var(--warning-text))]">
                  <TriangleAlert className="mt-0.5 size-4 shrink-0" aria-hidden />
                  <ul className="min-w-0 list-disc space-y-1 pl-4">
                    <li className="break-words">{t(`${copyKeys}.invalidateWarning`)}</li>
                    <li className="break-words">{t(`${copyKeys}.custody`)}</li>
                    <li className="break-words">{t(`${copyKeys}.authenticatorRequired`)}</li>
                  </ul>
                </div>
                <form id="recovery-codes-regenerate-form" className="min-w-0 space-y-3" onSubmit={handleSubmit}>
                  <div className="space-y-1.5">
                    <label className="text-sm font-medium" htmlFor="recovery-code-otp">
                      {t(`${copyKeys}.otpLabel`)}
                    </label>
                    <Input
                      ref={otpRef}
                      id="recovery-code-otp"
                      value={uiOtp}
                      onChange={(event) => setOtp(event.target.value)}
                      inputMode="numeric"
                      autoComplete="one-time-code"
                      autoCapitalize="off"
                      spellCheck={false}
                      placeholder={t(`${copyKeys}.otpPlaceholder`)}
                      aria-invalid={uiPhase === "confirm" && uiDialogNotice !== null ? true : undefined}
                      disabled={uiPhase === "submitting"}
                      required
                    />
                  </div>
                </form>
                {uiPhase === "submitting" ? (
                  <p role="status" className="text-sm text-muted-foreground">{t(`${copyKeys}.submitting`)}</p>
                ) : null}
                {uiPhase === "confirm" && uiDialogNotice ? (
                  <p role="alert" className="break-words rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive">
                    {t(`${copyKeys}.${uiDialogNotice}`)}
                  </p>
                ) : null}
              </>
            ) : null}

            {uiPhase === "codes" ? (
              <div className="min-w-0 space-y-3">
                <p className="break-words text-sm text-muted-foreground">{t(`${copyKeys}.codesIntro`)}</p>
                <ul aria-label={t(`${copyKeys}.codesLabel`)} className="min-w-0 select-text break-all rounded-lg bg-muted p-3 font-mono text-sm">
                  {uiCodes.map((code, index) => (
                    <li key={`${index}-${code}`} className="py-0.5">{code}</li>
                  ))}
                </ul>
                <Button
                  type="button"
                  variant="outline"
                  className="h-auto w-full max-w-full whitespace-normal"
                  onClick={() => { void handleCopyCodes(); }}
                >
                  {uiCopied ? <Check className="size-4" aria-hidden /> : <Copy className="size-4" aria-hidden />}
                  {uiCopied ? t(`${copyKeys}.copied`) : t(`${copyKeys}.copy`)}
                </Button>
                {uiCopyFailed ? (
                  <p role="alert" className="break-words rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive">
                    {t(`${copyKeys}.copyFailed`)}
                  </p>
                ) : null}
              </div>
            ) : null}

            {uiPhase === "unknown" ? (
              <RecoveryUncertaintyWarning id="recovery-codes-unknown-warning" text={unknownText} />
            ) : null}
          </DialogBody>
          <DialogFooter className="flex-wrap">
            {uiPhase === "confirm" || uiPhase === "submitting" ? (
              <>
                <Button type="button" variant="ghost" className="h-auto max-w-full whitespace-normal" disabled={uiPhase === "submitting"} onClick={dismissDialog}>
                  {t(`${copyKeys}.cancel`)}
                </Button>
                <Button
                  type="submit"
                  form="recovery-codes-regenerate-form"
                  className="h-auto max-w-full whitespace-normal text-center"
                  loading={uiPhase === "submitting"}
                  disabled={uiPhase === "submitting" || uiOtp.trim().length === 0}
                >
                  {t(`${copyKeys}.confirmAction`)}
                </Button>
              </>
            ) : null}
            {uiPhase === "codes" ? (
              <Button type="button" className="h-auto max-w-full whitespace-normal text-center" onClick={acknowledgeSaved}>
                {t(`${copyKeys}.savedClose`)}
              </Button>
            ) : null}
            {uiPhase === "unknown" ? (
              <>
                <Button type="button" variant="outline" className="h-auto max-w-full whitespace-normal" onClick={() => readCountRef.current()}>
                  {t(`${copyKeys}.refresh`)}
                </Button>
                <Button type="button" className="h-auto max-w-full whitespace-normal text-center" onClick={openConfirm}>
                  {t(`${copyKeys}.generateAgain`)}
                </Button>
              </>
            ) : null}
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </section>
  );
}
