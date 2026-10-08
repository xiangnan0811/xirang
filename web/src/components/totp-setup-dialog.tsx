import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import { QRCodeSVG } from "qrcode.react";
import { Copy, Check } from "lucide-react";
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
import { ApiError, apiClient } from "@/lib/api/client";
import { getAuthIdentitySnapshot, getAuthSessionGeneration, publishTOTPActivationFailure } from "@/lib/api/core";
import { classifyTOTPVerifyFailure, totpVerifyErrorCode, type TOTPSetupResponse } from "@/lib/api/totp-api";

type Step = "setup" | "verify" | "recovery";

interface TOTPSetupDialogProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  token: string;
  onSuccess?: () => void;
}

type SessionStamp = {
  token: string;
  role: AuthRole | null;
  generation: number;
};

function readSessionStamp(): SessionStamp | null {
  const current = getAuthIdentitySnapshot();
  if (!current.token) {
    return null;
  }
  return {
    token: current.token,
    role: current.role,
    generation: getAuthSessionGeneration(),
  };
}

function sameSessionStamp(left: SessionStamp, right: SessionStamp): boolean {
  return left.token === right.token && left.role === right.role && left.generation === right.generation;
}

type SetupFlight = {
  stamp: SessionStamp;
  promise: Promise<TOTPSetupResponse>;
};

function hasNewerAuthSession(tokenAtStart: string, generationAtStart: number): boolean {
  const current = getAuthIdentitySnapshot();
  return getAuthSessionGeneration() !== generationAtStart
    && current.token !== null
    && current.token !== tokenAtStart;
}

function noCommitMessage(error: unknown, translate: (key: string) => string): string {
  switch (totpVerifyErrorCode(error)) {
    case "TOTP_CODE_INVALID":
      return translate("totp.codeInvalid");
    case "TOTP_ENROLLMENT_REQUIRED":
      return translate("totp.enrollmentRequired");
    case "TOTP_ENROLLMENT_EXPIRED":
      return translate("totp.enrollmentExpired");
    case "TOTP_ENROLLMENT_CONFLICT":
      return translate("totp.enrollmentConflict");
    default:
      return translate("totp.codeError");
  }
}

export function TOTPSetupDialog(props: TOTPSetupDialogProps) {
  return <TOTPSetupSession key={String(props.open)} {...props} />;
}

function TOTPSetupSession({ open, onOpenChange, token, onSuccess }: TOTPSetupDialogProps) {
  const { t } = useTranslation();
  const { role, beginTOTPActivation, abortTOTPActivation, completeTOTPActivation, logout } = useAuth();
  const [step, setStep] = useState<Step>("setup");
  const [secret, setSecret] = useState("");
  const [qrUrl, setQrUrl] = useState("");
  const [enrollmentId, setEnrollmentId] = useState("");
  const [enrollmentExpiresAt, setEnrollmentExpiresAt] = useState("");
  const [verifyCode, setVerifyCode] = useState("");
  const [recoveryCodes, setRecoveryCodes] = useState<string[]>([]);
  const [recoveryConfirmed, setRecoveryConfirmed] = useState(false);
  const [loading, setLoading] = useState(false);
  const [verifying, setVerifying] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [copyError, setCopyError] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);
  const [enrollmentStamp, setEnrollmentStamp] = useState<SessionStamp | null>(null);
  const [recoveryStamp, setRecoveryStamp] = useState<SessionStamp | null>(null);
  const [sessionEpoch, setSessionEpoch] = useState(0);
  const verifyCodeInputRef = useRef<HTMLInputElement | null>(null);
  const mountedRef = useRef(true);
  const verifyingRef = useRef(false);
  const ownerRef = useRef<number | null>(null);
  const setupFlightRef = useRef<SetupFlight | null>(null);
  const copyTimerRef = useRef<number | null>(null);
  const recoveryStampMirrorRef = useRef<SessionStamp | null>(null);
  const handledEpochRef = useRef(0);
  const liveStamp = readSessionStamp();
  const holdsEnrollment = secret !== "" || enrollmentId !== "" || step === "verify";
  const holdsRecovery = step === "recovery" || recoveryCodes.length > 0;
  const recoveryMatches = recoveryStamp !== null
    && liveStamp !== null
    && sameSessionStamp(recoveryStamp, liveStamp);
  const enrollmentMatches = enrollmentStamp !== null
    && liveStamp !== null
    && sameSessionStamp(enrollmentStamp, liveStamp);
  const secretsStale = (holdsRecovery && !recoveryMatches) || (holdsEnrollment && !enrollmentMatches && !recoveryMatches);
  if (secretsStale) {
    setSessionEpoch(sessionEpoch + 1);
    setEnrollmentStamp(null);
    setRecoveryStamp(null);
    setStep("setup");
    setSecret("");
    setQrUrl("");
    setEnrollmentId("");
    setEnrollmentExpiresAt("");
    setVerifyCode("");
    setRecoveryCodes([]);
    setRecoveryConfirmed(false);
    setCopied(false);
    setCopyError(null);
    setError(null);
    setVerifying(false);
    setLoading(false);
  }
  const uiStep = secretsStale ? "setup" : step;
  const uiSecret = secretsStale ? "" : secret;
  const uiQrUrl = secretsStale ? "" : qrUrl;
  const uiEnrollmentId = secretsStale ? "" : enrollmentId;
  const uiRecoveryCodes = secretsStale ? [] : recoveryCodes;
  const uiError = secretsStale ? null : error;
  const uiVerifying = secretsStale ? false : verifying;
  const uiCopied = secretsStale ? false : copied;
  const uiCopyError = secretsStale ? null : copyError;
  const uiRecoveryConfirmed = secretsStale ? false : recoveryConfirmed;
  const uiVerifyCode = secretsStale ? "" : verifyCode;
  const busy = (loading && !secretsStale) || (open && uiStep === "setup" && !uiSecret && !uiError);
  const closeLocked = uiVerifying || (uiStep === "recovery" && !uiRecoveryConfirmed);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      if (copyTimerRef.current !== null) {
        window.clearTimeout(copyTimerRef.current);
        copyTimerRef.current = null;
      }
      const owner = ownerRef.current;
      ownerRef.current = null;
      verifyingRef.current = false;
      if (owner !== null) {
        abortTOTPActivation(owner);
      }
    };
  }, [abortTOTPActivation]);

  useLayoutEffect(() => {
    recoveryStampMirrorRef.current = recoveryStamp;
  }, [recoveryStamp]);

  useLayoutEffect(() => {
    if (sessionEpoch === handledEpochRef.current) {
      return;
    }
    handledEpochRef.current = sessionEpoch;
    const owner = ownerRef.current;
    ownerRef.current = null;
    setupFlightRef.current = null;
    verifyingRef.current = false;
    if (owner !== null) {
      abortTOTPActivation(owner);
    }
  }, [abortTOTPActivation, sessionEpoch]);

  useEffect(() => {
    if (!open || step !== "setup" || secret) return;
    const started = readSessionStamp();
    if (!started || started.token !== token || started.role !== role) return;
    let cancelled = false;
    // StrictMode replays this effect on the same instance. A second POST would
    // replace the enrollment in the database while the first secret stays on screen.
    const currentFlight = setupFlightRef.current;
    const flight = currentFlight !== null && sameSessionStamp(currentFlight.stamp, started)
      ? currentFlight
      : {
          stamp: started,
          promise: apiClient.totpSetup(started.token),
        };
    setupFlightRef.current = flight;
    const stillCurrent = () => {
      const current = readSessionStamp();
      return !cancelled
        && mountedRef.current
        && setupFlightRef.current === flight
        && current !== null
        && sameSessionStamp(current, started);
    };

    flight.promise
      .then((data) => {
        if (!stillCurrent()) return;
        if (!data.enrollmentId) {
          setError(t("totp.enrollmentMissing"));
          return;
        }
        setEnrollmentStamp(started);
        setError(null);
        setSecret(data.secret);
        setQrUrl(data.qrUrl);
        setEnrollmentId(data.enrollmentId);
        setEnrollmentExpiresAt(data.expiresAt);
      })
      .catch((err: unknown) => {
        if (!stillCurrent()) return;
        const msg =
          err instanceof ApiError
            ? (err.detail && typeof err.detail === "object"
                ? ((err.detail as { error?: string }).error ?? t("totp.generateKeyFailed"))
                : t("totp.generateKeyFailed"))
            : t("totp.generateKeyFailed");
        setError(msg);
      })
      .finally(() => {
        if (stillCurrent()) {
          setLoading(false);
        }
      });

    return () => {
      cancelled = true;
    };
  }, [open, role, step, secret, token, t]);

  useEffect(() => {
    if (!open || step !== "verify") return;
    const frame = requestAnimationFrame(() => {
      verifyCodeInputRef.current?.focus({ preventScroll: true });
    });
    return () => cancelAnimationFrame(frame);
  }, [open, step]);

  const clearSecrets = () => {
    setEnrollmentStamp(null);
    setRecoveryStamp(null);
    setupFlightRef.current = null;
    setStep("setup");
    setSecret("");
    setQrUrl("");
    setEnrollmentId("");
    setEnrollmentExpiresAt("");
    setVerifyCode("");
    setRecoveryCodes([]);
    setRecoveryConfirmed(false);
    setError(null);
    setCopyError(null);
    setCopied(false);
  };

  const handleClose = (isOpen: boolean) => {
    if (!isOpen && closeLocked) {
      return;
    }
    if (!isOpen) {
      const confirmed = step === "recovery" && recoveryConfirmed;
      clearSecrets();
      if (confirmed) {
        onSuccess?.();
      }
    }
    onOpenChange(isOpen);
  };

  const handleVerify = async (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (verifyingRef.current) {
      return;
    }
    const expiresAtMs = Date.parse(enrollmentExpiresAt);
    if (!enrollmentId || !Number.isFinite(expiresAtMs) || expiresAtMs <= Date.now()) {
      setupFlightRef.current = null;
      setEnrollmentId("");
      setEnrollmentExpiresAt("");
      setSecret("");
      setQrUrl("");
      setStep("setup");
      setError(t("totp.enrollmentExpired"));
      return;
    }

    let owner: number;
    try {
      owner = beginTOTPActivation();
    } catch {
      return;
    }
    ownerRef.current = owner;
    verifyingRef.current = true;
    setVerifying(true);
    setError(null);
    const generationAtStart = getAuthSessionGeneration();
    const identity = getAuthIdentitySnapshot();
    if (!identity.token || identity.token !== token || identity.role !== role) {
      abortTOTPActivation(owner);
      ownerRef.current = null;
      verifyingRef.current = false;
      if (mountedRef.current) {
        setVerifying(false);
      }
      return;
    }
    const tokenAtStart = identity.token;
    const roleAtStart = identity.role;
    if (
      enrollmentStamp
      && enrollmentStamp.token === tokenAtStart
      && enrollmentStamp.role === roleAtStart
    ) {
      setEnrollmentStamp({
        token: tokenAtStart,
        role: roleAtStart,
        generation: generationAtStart,
      });
    }
    const canApply = () => {
      const current = getAuthIdentitySnapshot();
      return mountedRef.current
        && ownerRef.current === owner
        && getAuthSessionGeneration() === generationAtStart
        && current.token === tokenAtStart
        && current.role === roleAtStart;
    };
    const releaseOwner = () => {
      if (ownerRef.current === owner) {
        abortTOTPActivation(owner);
      }
      ownerRef.current = null;
    };

    try {
      const data = await apiClient.totpVerify(tokenAtStart, verifyCode, enrollmentId, owner);
      if (!canApply()) {
        if (!hasNewerAuthSession(tokenAtStart, generationAtStart)) {
          publishTOTPActivationFailure("uncertain");
          if (getAuthIdentitySnapshot().token !== null) {
            logout();
          }
        }
        releaseOwner();
        return;
      }
      const installed = completeTOTPActivation(owner, data.token, data.user);
      ownerRef.current = null;
      if (!installed || !mountedRef.current) {
        setEnrollmentStamp(null);
        return;
      }
      const replacement = readSessionStamp();
      if (
        !replacement
        || replacement.token !== data.token
        || replacement.role !== data.user.role
      ) {
        setEnrollmentStamp(null);
        setRecoveryStamp(null);
        return;
      }
      setEnrollmentStamp(null);
      setRecoveryStamp(replacement);
      setRecoveryCodes(data.recoveryCodes);
      setRecoveryConfirmed(false);
      setCopyError(null);
      setCopied(false);
      setStep("recovery");
    } catch (err) {
      const outcome = classifyTOTPVerifyFailure(err);
      if (outcome === "no_commit" && canApply()) {
        abortTOTPActivation(owner);
        ownerRef.current = null;
        setError(noCommitMessage(err, t));
        return;
      }
      if (outcome === "no_commit" || hasNewerAuthSession(tokenAtStart, generationAtStart)) {
        releaseOwner();
        return;
      }
      publishTOTPActivationFailure("uncertain");
      if (getAuthIdentitySnapshot().token !== null) {
        logout();
      }
      releaseOwner();
    } finally {
      verifyingRef.current = false;
      if (mountedRef.current) {
        setVerifying(false);
      }
    }
  };

  const handleCopyCodes = async () => {
    const stamp = recoveryStamp;
    const live = readSessionStamp();
    const codes = recoveryCodes;
    if (!mountedRef.current || !stamp || !live || !sameSessionStamp(stamp, live) || codes.length === 0) {
      return;
    }
    const copyStillCurrent = () => {
      const current = readSessionStamp();
      const bound = recoveryStampMirrorRef.current;
      return mountedRef.current
        && bound !== null
        && current !== null
        && sameSessionStamp(bound, stamp)
        && sameSessionStamp(current, stamp);
    };
    try {
      await navigator.clipboard.writeText(codes.join("\n"));
      if (!copyStillCurrent()) {
        return;
      }
      setCopyError(null);
      setCopied(true);
      if (copyTimerRef.current !== null) {
        window.clearTimeout(copyTimerRef.current);
      }
      copyTimerRef.current = window.setTimeout(() => {
        if (copyStillCurrent()) {
          setCopied(false);
        }
      }, 2000);
    } catch {
      if (!copyStillCurrent()) {
        return;
      }
      setCopied(false);
      setCopyError(t("totp.copyFailed"));
    }
  };

  return (
    <Dialog open={open} onOpenChange={handleClose}>
      <DialogContent
        size="sm"
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
          <DialogTitle>{t("totp.setupTitle")}</DialogTitle>
          <DialogDescription>
            {uiStep === "setup" && t("totp.setupStepScan")}
            {uiStep === "verify" && t("totp.setupStepVerify")}
            {uiStep === "recovery" && t("totp.setupStepRecovery")}
          </DialogDescription>
          <DialogCloseButton disabled={closeLocked} />
        </DialogHeader>

        <DialogBody className="space-y-4">
          {uiStep === "setup" && (
            <>
              {busy ? (
                <p className="text-center text-sm text-muted-foreground">{t("totp.generatingKey")}</p>
              ) : uiError ? (
                <p role="alert" className="rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive">
                  {uiError}
                </p>
              ) : (
                <>
                  {uiQrUrl && (
                    <div className="flex justify-center">
                      <div className="rounded-xl border border-border/50 bg-white p-4 shadow-sm backdrop-blur-sm">
                        <QRCodeSVG value={uiQrUrl} size={176} role="img" aria-label={t("totp.qrCodeAlt")} />
                      </div>
                    </div>
                  )}
                  <div className="space-y-1">
                    <p className="text-xs text-muted-foreground">{t("totp.cannotScanHint")}</p>
                    <p className="break-all rounded-lg bg-muted px-3 py-2 font-mono text-xs select-all">
                      {uiSecret}
                    </p>
                  </div>
                </>
              )}
            </>
          )}

          {uiStep === "verify" && (
            <form id="totp-verify-form" className="space-y-3" onSubmit={handleVerify}>
              <div className="space-y-1.5">
                <label className="text-sm font-medium" htmlFor="totp-verify-code">
                  {t("totp.verificationCode")}
                </label>
                <Input
                  ref={verifyCodeInputRef}
                  id="totp-verify-code"
                  value={uiVerifyCode}
                  onChange={(e) => setVerifyCode(e.target.value)}
                  inputMode="numeric"
                  pattern="[0-9]*"
                  autoComplete="one-time-code"
                  placeholder={t("totp.codePlaceholder")}
                  required
                  disabled={uiVerifying}
                />
              </div>
              {uiVerifying ? (
                <p role="status" className="text-sm text-muted-foreground">{t("totp.verifyInProgress")}</p>
              ) : null}
              {uiError ? (
                <p role="alert" className="rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive">
                  {uiError}
                </p>
              ) : null}
            </form>
          )}

          {uiStep === "recovery" && (
            <div className="space-y-3">
              <p className="text-sm text-muted-foreground">
                {t("totp.recoverySuccess")}
              </p>
              <div className="rounded-lg bg-muted p-3 font-mono text-sm">
                {uiRecoveryCodes.map((code, index) => (
                  <div key={`${index}-${code}`} className="py-0.5">{code}</div>
                ))}
              </div>
              <Button
                type="button"
                variant="outline"
                size="sm"
                className="w-full gap-2"
                onClick={() => { void handleCopyCodes(); }}
              >
                {uiCopied ? <Check className="size-4" /> : <Copy className="size-4" />}
                {uiCopied ? t("totp.copiedRecovery") : t("totp.copyRecovery")}
              </Button>
              {uiCopyError ? (
                <p role="alert" className="rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive">
                  {uiCopyError}
                </p>
              ) : null}
              <label className="flex items-start gap-2 text-sm" htmlFor="totp-recovery-confirmed">
                <input
                  id="totp-recovery-confirmed"
                  type="checkbox"
                  className="mt-1"
                  checked={uiRecoveryConfirmed}
                  onChange={(event) => setRecoveryConfirmed(event.target.checked)}
                />
                <span>{t("totp.recoverySavedConfirm")}</span>
              </label>
            </div>
          )}
        </DialogBody>

        <DialogFooter>
          {uiStep === "setup" && (
            <Button
              type="button"
              disabled={busy || !uiSecret || !uiEnrollmentId}
              onClick={() => { setStep("verify"); setError(null); }}
            >
              {t("common.next")}
            </Button>
          )}
          {uiStep === "verify" && (
            <>
              <Button type="button" variant="ghost" disabled={uiVerifying} onClick={() => { setStep("setup"); setError(null); }}>
                {t("common.prev")}
              </Button>
              <Button type="submit" form="totp-verify-form" loading={uiVerifying} disabled={uiVerifying}>
                {t("totp.verifyAndEnable")}
              </Button>
            </>
          )}
          {uiStep === "recovery" && (
            <Button type="button" disabled={!uiRecoveryConfirmed} onClick={() => handleClose(false)}>
              {t("common.finish")}
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
