import { useCallback, useEffect, useRef, useState } from "react";
import { Navigate, useLocation, useNavigate } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { ShieldCheck } from "lucide-react";
import { RecoveryCodesPanel, RecoveryEnrollmentPanel, type RecoveryCodesCopyState } from "@/components/recovery-enrollment-panel";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { InlineAlert } from "@/components/ui/inline-alert";
import { Input } from "@/components/ui/input";
import { useAuth } from "@/context/auth-context.hooks";
import { useDocumentTitle } from "@/hooks/use-document-title";
import { classifyRecoveryEnrollmentFailure } from "@/lib/api/auth-api";
import { ApiError, apiClient } from "@/lib/api/client";
import { getAuthSessionGeneration, normalizeRedirectTarget, subscribeAuthSessionGeneration } from "@/lib/api/core";
import type { RecoveryEnrollmentChallenge } from "@/types/domain";

type LocationState = {
  from?: string;
};

type VerificationMode = "totp" | "recovery";

type LoginMode = "password" | "two-factor" | "recovery-enrollment" | "recovery-codes";

type RecoveryEnrollmentSession = {
  challenge: RecoveryEnrollmentChallenge;
  /** performance.now() instant after which the page treats the enrollment as expired. */
  deadline: number;
  expiresInMinutes: number;
};

type RecoveryNotice = {
  tone: "info" | "warning";
  message: string;
};

// Server enrollment TTL. The client never trusts a longer window than this even if its clock lags.
const RECOVERY_ENROLLMENT_MAX_TTL_MS = 10 * 60_000;
const RECOVERY_VERIFY_CODE_PATTERN = /^\d{6}$/;

export function LoginPage() {
  const { t } = useTranslation();
  useDocumentTitle(t("login.welcomeTitle"));
  const location = useLocation();
  const navigate = useNavigate();
  const { isAuthenticated, login } = useAuth();
  const demoModeEnabled = import.meta.env.VITE_ENABLE_DEMO_MODE === "true";

  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [retryAfter, setRetryAfter] = useState(0);

  // 验证码状态
  const [captchaId, setCaptchaId] = useState<string | null>(null);
  const [captchaQuestion, setCaptchaQuestion] = useState<string | null>(null);
  const [captchaAnswer, setCaptchaAnswer] = useState("");
  const [secondCaptchaId, setSecondCaptchaId] = useState<string | null>(null);
  const [secondCaptchaQuestion, setSecondCaptchaQuestion] = useState<string | null>(null);
  const [secondCaptchaAnswer, setSecondCaptchaAnswer] = useState("");

  const [mode, setModeState] = useState<LoginMode>("password");
  // Mirrors mode for the identity-change listener, which runs outside render.
  const modeRef = useRef<LoginMode>("password");

  // 2FA 步骤状态
  const [loginToken, setLoginToken] = useState("");
  const [totpCode, setTotpCode] = useState("");
  const [verificationMode, setVerificationMode] = useState<VerificationMode>("totp");
  const totpInputRef = useRef<HTMLInputElement | null>(null);

  // Offline-recovery state. The restricted token, secret and new codes live only in this page's
  // memory: never AuthProvider, storage, URL or cache.
  const [recovery, setRecovery] = useState<RecoveryEnrollmentSession | null>(null);
  const [recoveryCode, setRecoveryCode] = useState("");
  const [recoveryError, setRecoveryError] = useState<string | null>(null);
  const [recoveryCodes, setRecoveryCodes] = useState<string[]>([]);
  const [recoveryCodesSaved, setRecoveryCodesSaved] = useState(false);
  const [copyState, setCopyState] = useState<RecoveryCodesCopyState>("idle");
  const [recoveryNotice, setRecoveryNotice] = useState<RecoveryNotice | null>(null);
  const recoveryCodeInputRef = useRef<HTMLInputElement | null>(null);
  const codesHeadingRef = useRef<HTMLHeadingElement | null>(null);
  const recoveryCodesRef = useRef<string[]>([]);
  const verifyPendingRef = useRef(false);

  // 同步 single-flight：submitting 状态赶不上同一轮里的第二次提交。
  const submittingRef = useRef(false);
  // Request ownership: any back, expiry, identity change or unmount bumps it so late results drop.
  const requestSeqRef = useRef(0);
  // 卸载后忽略迟到结果。StrictMode 先 cleanup 再跑 setup，必须在 setup 里设回 true。
  const mountedRef = useRef(false);
  const identityChangedRef = useRef<() => void>(() => {});
  const recoveryExpiredRef = useRef<() => void>(() => {});

  const errorId = "login-form-error";

  // Countdown timer for login lockout
  useEffect(() => {
    if (retryAfter <= 0) return;
    const timer = setInterval(() => {
      setRetryAfter(prev => {
        if (prev <= 1) return 0;
        return prev - 1;
      });
    }, 1000);
    return () => clearInterval(timer);
  }, [retryAfter]);

  const formatRetryTime = (seconds: number) => {
    if (seconds >= 60) {
      const min = Math.ceil(seconds / 60);
      return t("login.lockedMinutes", { minutes: min });
    }
    return t("login.lockedSeconds", { seconds });
  };

  const fetchCaptcha = useCallback((signal?: AbortSignal) => {
    return apiClient.getCaptcha().then((data) => {
      if (signal?.aborted) return;
      // Only show challenges the backend actually validates (enabled flags).
      if (data.enabled && data.id && data.question) {
        setCaptchaId(data.id);
        setCaptchaQuestion(data.question);
        setCaptchaAnswer("");
      } else {
        setCaptchaId(null);
        setCaptchaQuestion(null);
        setCaptchaAnswer("");
      }
      if (data.secondRequired && data.secondId && data.secondQuestion) {
        setSecondCaptchaId(data.secondId);
        setSecondCaptchaQuestion(data.secondQuestion);
        setSecondCaptchaAnswer("");
      } else {
        setSecondCaptchaId(null);
        setSecondCaptchaQuestion(null);
        setSecondCaptchaAnswer("");
      }
    }).catch(() => {
      if (signal?.aborted) return;
      // 验证码接口不可用，隐藏验证码区域
      setCaptchaId(null);
      setCaptchaQuestion(null);
      setCaptchaAnswer("");
      setSecondCaptchaId(null);
      setSecondCaptchaQuestion(null);
      setSecondCaptchaAnswer("");    });
  }, []);

  useEffect(() => {
    const controller = new AbortController();
    void fetchCaptcha(controller.signal);
    return () => controller.abort();
  }, [fetchCaptcha]);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
      requestSeqRef.current += 1;
      submittingRef.current = false;
      verifyPendingRef.current = false;
      recoveryCodesRef.current = [];
    };
  }, []);

  // Every identity generation bump (including A→B→A) drops in-flight ownership and clears any
  // pending login token or recovery secret immediately.
  useEffect(() => subscribeAuthSessionGeneration(() => identityChangedRef.current()), []);

  useEffect(() => {
    if (!recovery) return;
    const timer = window.setTimeout(
      () => recoveryExpiredRef.current(),
      Math.max(0, recovery.deadline - performance.now()),
    );
    return () => window.clearTimeout(timer);
  }, [recovery]);

  useEffect(() => {
    if (mode === "password") return;
    const frame = requestAnimationFrame(() => {
      const target = mode === "two-factor"
        ? totpInputRef.current
        : mode === "recovery-enrollment"
          ? recoveryCodeInputRef.current
          : codesHeadingRef.current;
      target?.focus({ preventScroll: true });
    });
    return () => cancelAnimationFrame(frame);
  }, [mode, verificationMode]);

  const setMode = (next: LoginMode) => {
    modeRef.current = next;
    setModeState(next);
  };

  /** Releases the single-flight lock and orphans any in-flight request's result. */
  const abandonInFlight = () => {
    requestSeqRef.current += 1;
    submittingRef.current = false;
    verifyPendingRef.current = false;
    setSubmitting(false);
  };

  /** Clears every recovery secret and returns to the password form. */
  const leaveRecovery = (notice: RecoveryNotice | null) => {
    recoveryCodesRef.current = [];
    setRecovery(null);
    setRecoveryCode("");
    setRecoveryError(null);
    setRecoveryCodes([]);
    setRecoveryCodesSaved(false);
    setCopyState("idle");
    setPassword("");
    setError(null);
    setRecoveryNotice(notice);
    setMode("password");
    void fetchCaptcha();
  };

  useEffect(() => {
    identityChangedRef.current = () => {
      const current = modeRef.current;
      const wasVerifying = verifyPendingRef.current;
      abandonInFlight();
      if (current === "password") return;
      if (current === "two-factor") {
        setLoginToken("");
        setTotpCode("");
        setVerificationMode("totp");
        setError(null);
        setMode("password");
        void fetchCaptcha();
        return;
      }
      leaveRecovery({
        tone: "warning",
        message: current === "recovery-codes"
          ? t("login.recoveryEnrollmentCodesCleared")
          : wasVerifying
            ? t("login.recoveryEnrollmentUnknown")
            : t("login.recoveryEnrollmentCleared"),
      });
    };
    recoveryExpiredRef.current = () => {
      if (modeRef.current !== "recovery-enrollment") return;
      // The secret's lifetime is bounded by the deadline even mid-verification. A request already
      // sent may still have committed, so that case is reported as unknown, never as expired.
      const wasVerifying = verifyPendingRef.current;
      abandonInFlight();
      leaveRecovery({
        tone: "warning",
        message: wasVerifying ? t("login.recoveryEnrollmentUnknown") : t("login.recoveryEnrollmentExpired"),
      });
    };
  });

  const queryRedirect = new URLSearchParams(location.search).get("redirect");
  const redirectTo = normalizeRedirectTarget((location.state as LocationState | null)?.from ?? queryRedirect);

  if (isAuthenticated) {
    return <Navigate to={redirectTo} replace />;
  }

  const enterRecoveryEnrollment = (challenge: RecoveryEnrollmentChallenge, requestStartedAt: number) => {
    const receivedAt = performance.now();
    // Conservative window: never longer than the server TTL, and the whole request RTT is
    // charged because the server may have started the clock as soon as it received the request.
    const remaining = Math.min(challenge.expiresAtMs - Date.now(), RECOVERY_ENROLLMENT_MAX_TTL_MS)
      - (receivedAt - requestStartedAt);
    setPassword("");
    setCaptchaAnswer("");
    setSecondCaptchaAnswer("");
    if (remaining <= 0) {
      setRecoveryNotice({ tone: "warning", message: t("login.recoveryEnrollmentExpired") });
      void fetchCaptcha();
      return;
    }
    setRecoveryCode("");
    setRecoveryError(null);
    setRecovery({
      challenge,
      deadline: receivedAt + remaining,
      expiresInMinutes: Math.max(1, Math.floor(remaining / 60_000)),
    });
    setMode("recovery-enrollment");
  };

  const handleSubmit = async (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (submittingRef.current) return;
    submittingRef.current = true;
    const seq = ++requestSeqRef.current;
    const generation = getAuthSessionGeneration();
    const owns = () => mountedRef.current && requestSeqRef.current === seq && getAuthSessionGeneration() === generation;
    setSubmitting(true);
    setError(null);
    setRecoveryNotice(null);
    const requestStartedAt = performance.now();

    try {
      const result = await apiClient.login(username, password, {
        captchaId: captchaId ?? undefined,
        captchaAnswer: captchaQuestion ? captchaAnswer : undefined,
        secondCaptchaId: secondCaptchaId ?? undefined,
        secondCaptchaAnswer: secondCaptchaQuestion ? secondCaptchaAnswer : undefined,
      });
      if (!owns()) return;

      // Recovery first: its token is restricted and must never reach the session install below.
      if (result.recoveryEnrollment) {
        enterRecoveryEnrollment(result.recoveryEnrollment, requestStartedAt);
        return;
      }

      if (result.requires2FA && result.loginToken) {
        setVerificationMode("totp");
        setTotpCode("");
        setError(null);
        setLoginToken(result.loginToken);
        setMode("two-factor");
        return;
      }

      if (result.token && result.user) {
        login(result.token, result.user.username, result.user.role, result.user.id, result.user.totpEnabled ?? false);
        navigate(redirectTo, { replace: true });
      }
      return;
    } catch (error) {
      if (!owns()) return;
      void fetchCaptcha();
      if (error instanceof ApiError) {
        const payload =
          error.detail && typeof error.detail === "object"
            ? (error.detail as { error?: string; message?: string })
            : undefined;

        if (error.status === 401) {
          setError(payload?.error ?? t("login.errorInvalidCredentials"));
          return;
        }
        if (error.status === 403) {
          setError(t("login.errorForbidden"));
          return;
        }
        if (error.status === 404) {
          setError(t("login.errorNotFound"));
          return;
        }
        if (error.status === 423 && error.retryAfter) {
          setRetryAfter(error.retryAfter);
          setError(t("login.errorLocked", {
            seconds: error.retryAfter,
            formatted: formatRetryTime(error.retryAfter),
          }));
          return;
        }

        setError(payload?.error ?? payload?.message ?? t("login.errorLoginFailed", { status: error.status }));
        return;
      }
      setError(t("login.errorNetworkFailed"));
    } finally {
      if (requestSeqRef.current === seq) {
        submittingRef.current = false;
        if (mountedRef.current) {
          setSubmitting(false);
        }
      }
    }
  };

  const handle2FASubmit = async (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (submittingRef.current) return;
    submittingRef.current = true;
    const seq = ++requestSeqRef.current;
    const generation = getAuthSessionGeneration();
    const owns = () => mountedRef.current && requestSeqRef.current === seq && getAuthSessionGeneration() === generation;
    setSubmitting(true);
    setError(null);

    try {
      const result = await apiClient.totpLogin(loginToken, totpCode);
      if (!owns()) return;
      setLoginToken("");
      setTotpCode("");
      login(result.token, result.user.username, result.user.role, result.user.id, result.user.totpEnabled);
      navigate(redirectTo, { replace: true });
    } catch (error) {
      if (!owns()) return;
      if (error instanceof ApiError) {
        if (error.status === 401) {
          setError(t("login.errorVerificationOrSessionInvalid"));
          return;
        }
        const payload =
          error.detail && typeof error.detail === "object"
            ? (error.detail as { error?: string; message?: string })
            : undefined;
        setError(payload?.error ?? t("login.errorVerifyFailed", { status: error.status }));
        return;
      }
      setError(t("login.errorVerifyNetworkFailed"));
    } finally {
      if (requestSeqRef.current === seq) {
        submittingRef.current = false;
        if (mountedRef.current) {
          setSubmitting(false);
        }
      }
    }
  };

  const handleSwitchVerificationMode = () => {
    if (submittingRef.current) return;
    setVerificationMode((current) => (current === "totp" ? "recovery" : "totp"));
    setTotpCode("");
    setError(null);
  };

  const handleBackToLogin = () => {
    if (submittingRef.current) return;
    setMode("password");
    setLoginToken("");
    setTotpCode("");
    setError(null);
    setVerificationMode("totp");
    void fetchCaptcha();
  };

  const handleRecoveryVerify = async (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const session = recovery;
    if (submittingRef.current || !session) return;
    const code = recoveryCode.replace(/\s+/g, "");
    if (!RECOVERY_VERIFY_CODE_PATTERN.test(code)) {
      setRecoveryError(t("login.recoveryEnrollmentCodeFormat"));
      return;
    }
    if (performance.now() >= session.deadline) {
      requestSeqRef.current += 1;
      leaveRecovery({ tone: "warning", message: t("login.recoveryEnrollmentExpired") });
      return;
    }
    submittingRef.current = true;
    verifyPendingRef.current = true;
    const seq = ++requestSeqRef.current;
    const generation = getAuthSessionGeneration();
    const owns = () => mountedRef.current && requestSeqRef.current === seq && getAuthSessionGeneration() === generation;
    setSubmitting(true);
    setRecoveryError(null);

    try {
      const { recoveryCodes: codes } = await apiClient.completeRecoveryEnrollment(session.challenge.recoveryToken, code);
      if (!owns()) return;
      // The enrollment is consumed: drop token, QR and secret before showing this one-time list.
      setRecovery(null);
      setRecoveryCode("");
      recoveryCodesRef.current = codes;
      setRecoveryCodes(codes);
      setRecoveryCodesSaved(false);
      setCopyState("idle");
      setMode("recovery-codes");
    } catch (failure) {
      if (!owns()) return;
      const { kind } = classifyRecoveryEnrollmentFailure(failure);
      if (kind === "unknown") {
        // Never replay: the server may have committed and the codes are not recoverable here.
        leaveRecovery({ tone: "warning", message: t("login.recoveryEnrollmentUnknown") });
        return;
      }
      if (kind === "enrollment-invalid") {
        leaveRecovery({ tone: "warning", message: t("login.recoveryEnrollmentInvalid") });
        return;
      }
      // Definitely not committed. Retrying is allowed only while the window is still open.
      if (performance.now() >= session.deadline) {
        leaveRecovery({ tone: "warning", message: t("login.recoveryEnrollmentExpired") });
        return;
      }
      setRecoveryCode("");
      setRecoveryError(
        kind === "code-invalid"
          ? t("login.recoveryEnrollmentCodeInvalid")
          : kind === "rate-limited"
            ? t("login.recoveryEnrollmentRateLimited")
            : kind === "not-sent"
              ? t("login.recoveryEnrollmentNotSent")
              : t("login.recoveryEnrollmentCodeFormat"),
      );
      requestAnimationFrame(() => recoveryCodeInputRef.current?.focus({ preventScroll: true }));
    } finally {
      if (requestSeqRef.current === seq) {
        submittingRef.current = false;
        verifyPendingRef.current = false;
        if (mountedRef.current) {
          setSubmitting(false);
        }
      }
    }
  };

  const handleRecoveryBack = () => {
    // A sent verification may commit; leaving now would hide its one-time codes.
    if (submittingRef.current) return;
    requestSeqRef.current += 1;
    leaveRecovery(null);
  };

  const handleCopyRecoveryCodes = async () => {
    const codes = recoveryCodesRef.current;
    if (codes.length === 0) return;
    const stillShown = () => mountedRef.current && recoveryCodesRef.current === codes;
    try {
      await navigator.clipboard.writeText(codes.join("\n"));
      if (stillShown()) setCopyState("copied");
    } catch {
      if (stillShown()) setCopyState("failed");
    }
  };

  const handleRecoveryReturn = () => {
    if (!recoveryCodesSaved) return;
    requestSeqRef.current += 1;
    leaveRecovery({ tone: "info", message: t("login.recoveryEnrollmentCompleted") });
  };

  return (
    <div className="relative flex min-h-screen items-center justify-center overflow-hidden px-4 py-8 animate-fade-in">
      <div aria-hidden className="bg-login-ambient" />
      <h1 className="sr-only">{t("login.platformName")}</h1>

      <div className="relative z-10 grid w-full max-w-5xl gap-4 md:max-w-3xl lg:max-w-5xl lg:grid-cols-[1.1fr_0.9fr]">
        <section className="hidden rounded-lg border border-border bg-card shadow-md p-8 md:flex md:flex-col md:justify-center lg:p-12">
          <div className="mb-6 inline-flex items-center gap-2 rounded-full border border-border bg-background px-4 py-1.5 text-xs font-medium text-muted-foreground shadow-sm animate-slide-up [animation-delay:100ms]">
            <img src="/xirang-mark.svg" alt="XiRang" className="size-4.5 rounded-xs border border-border bg-background" />
            <span>XiRang / X-Soil</span>
          </div>
          <p className="text-4xl font-extrabold tracking-tight lg:text-5xl animate-slide-up [animation-delay:150ms]">
            <span className="bg-gradient-to-br from-primary to-info bg-clip-text text-transparent">{t("login.platformName")}</span>
          </p>
          <p className="mt-4 max-w-lg text-base leading-relaxed text-muted-foreground animate-slide-up [animation-delay:200ms]">
            {t("login.platformSlogan")}
          </p>

          <div className="mt-8 grid gap-4 sm:grid-cols-2 animate-slide-up [animation-delay:250ms]">
            <div className="rounded-lg border border-border bg-card shadow-sm hover:bg-accent transition-colors p-4">
              <div className="mb-2 inline-flex h-8 w-8 items-center justify-center rounded-xl bg-primary/10 text-primary">
                <ShieldCheck className="size-4" aria-hidden />
              </div>
              <p className="text-sm font-semibold">{t("login.featureMonitor")}</p>
              <p className="mt-1 text-xs text-muted-foreground">{t("login.featureMonitorDesc")}</p>
            </div>
            <div className="rounded-lg border border-border bg-card shadow-sm hover:bg-accent transition-colors p-4">
              <div className="mb-2 inline-flex h-8 w-8 items-center justify-center rounded-xl bg-primary/10 text-primary">
                <ShieldCheck className="size-4" aria-hidden />
              </div>
              <p className="text-sm font-semibold">{t("login.featureOrchestrate")}</p>
              <p className="mt-1 text-xs text-muted-foreground">{t("login.featureOrchestrateDesc")}</p>
            </div>
            <div className="rounded-lg border border-border bg-card shadow-sm hover:bg-accent transition-colors p-4 sm:col-span-2">
              <div className="mb-2 inline-flex h-8 w-8 items-center justify-center rounded-xl bg-primary/10 text-primary">
                <ShieldCheck className="size-4" aria-hidden />
              </div>
              <p className="text-sm font-semibold">{t("login.featureCompliance")}</p>
              <p className="mt-1 text-xs text-muted-foreground">{t("login.featureComplianceDesc")}</p>
            </div>
          </div>
        </section>

        <Card className="flex flex-col justify-center rounded-lg border border-border bg-card shadow-md animate-slide-up [animation-delay:200ms]">
          <CardHeader className="space-y-3 pb-6 animate-slide-up [animation-delay:250ms]">
            <div className="mx-auto mb-2 flex h-12 w-12 items-center justify-center rounded-full bg-primary/10 text-primary md:hidden">
              <ShieldCheck className="size-6" aria-hidden />
            </div>
            <p className="text-center text-3xl font-bold tracking-tight md:hidden">{t("login.consoleName")}</p>
            {mode === "two-factor" ? (
              <>
                <CardTitle as="h2" className="text-2xl font-bold">{t("login.twoFactorTitle")}</CardTitle>
                <CardDescription id="login-verification-help" className="text-base">
                  {verificationMode === "recovery" ? t("login.recoveryCodeHelp") : t("login.twoFactorDesc")}
                </CardDescription>
              </>
            ) : mode === "recovery-enrollment" ? (
              <>
                <CardTitle as="h2" className="text-2xl font-bold">{t("login.recoveryEnrollmentTitle")}</CardTitle>
                <CardDescription className="text-base">{t("login.recoveryEnrollmentDesc")}</CardDescription>
              </>
            ) : mode === "recovery-codes" ? (
              <>
                <CardTitle as="h2" ref={codesHeadingRef} tabIndex={-1} className="text-2xl font-bold focus:outline-none">
                  {t("login.recoveryEnrollmentCodesTitle")}
                </CardTitle>
                <CardDescription className="text-base">{t("login.recoveryEnrollmentCodesDesc")}</CardDescription>
              </>
            ) : (
              <>
                <CardTitle as="h2" className="text-2xl font-bold">{t("login.welcomeTitle")}</CardTitle>
                <CardDescription className="text-base">{t("login.welcomeDesc")}</CardDescription>
              </>
            )}
          </CardHeader>
          <CardContent className="animate-slide-up [animation-delay:300ms]">
            {mode === "two-factor" ? (
              <form className="space-y-4" onSubmit={handle2FASubmit}>
                <div className="space-y-2">
                  <label className="text-sm font-medium" htmlFor="totp-code">
                    {verificationMode === "recovery" ? t("login.recoveryCodeLabel") : t("login.totpLabel")}
                  </label>
                  <Input
                    ref={totpInputRef}
                    id="totp-code"
                    type="text"
                    value={totpCode}
                    onChange={(event) => setTotpCode(event.target.value)}
                    inputMode={verificationMode === "recovery" ? "text" : "numeric"}
                    autoComplete={verificationMode === "recovery" ? "off" : "one-time-code"}
                    autoCapitalize={verificationMode === "recovery" ? "none" : undefined}
                    spellCheck={verificationMode === "recovery" ? false : undefined}
                    placeholder={verificationMode === "recovery" ? t("login.recoveryCodePlaceholder") : t("login.totpPlaceholder")}
                    aria-invalid={Boolean(error)}
                    aria-errormessage={error ? errorId : undefined}
                    aria-describedby="login-verification-help"
                    disabled={submitting}
                    required
                  />
                </div>

                <Button
                  className="w-full"
                  type="button"
                  variant="outline"
                  disabled={submitting}
                  onClick={handleSwitchVerificationMode}
                >
                  {verificationMode === "recovery" ? t("login.useAuthenticatorCode") : t("login.useRecoveryCode")}
                </Button>

                {error ? (
                  <p
                    id={errorId}
                    role="alert"
                    className="rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive"
                  >
                    {error}
                  </p>
                ) : null}

                <Button className="w-full" type="submit" loading={submitting}>
                  {t("login.verifyButton")}
                </Button>
                <Button
                  className="w-full"
                  type="button"
                  variant="ghost"
                  disabled={submitting}
                  onClick={handleBackToLogin}
                >
                  {t("login.backToLogin")}
                </Button>
              </form>
            ) : mode === "recovery-enrollment" && recovery ? (
              <RecoveryEnrollmentPanel
                qrUrl={recovery.challenge.qrUrl}
                secret={recovery.challenge.secret}
                expiresInMinutes={recovery.expiresInMinutes}
                code={recoveryCode}
                onCodeChange={setRecoveryCode}
                onSubmit={handleRecoveryVerify}
                onBack={handleRecoveryBack}
                submitting={submitting}
                error={recoveryError}
                codeInputRef={recoveryCodeInputRef}
              />
            ) : mode === "recovery-codes" ? (
              <RecoveryCodesPanel
                codes={recoveryCodes}
                copyState={copyState}
                onCopy={() => { void handleCopyRecoveryCodes(); }}
                saved={recoveryCodesSaved}
                onSavedChange={setRecoveryCodesSaved}
                onReturn={handleRecoveryReturn}
              />
            ) : (
              <div className="space-y-4">
                {demoModeEnabled ? (
                  <div className="rounded-xl border border-warning/30 bg-warning/10 p-3 text-sm text-warning" role="status">
                    <p className="font-medium">{t("login.demoTitle")}</p>
                    <p className="mt-1 text-xs leading-relaxed">{t("login.demoDesc")}</p>
                    <Button className="mt-3 w-full" type="button" variant="secondary" onClick={() => navigate("/app/overview", { replace: true })}>
                      {t("login.demoEnter")}
                    </Button>
                  </div>
                ) : null}
                {recoveryNotice ? (
                  <div data-testid="recovery-enrollment-notice">
                    <InlineAlert tone={recoveryNotice.tone}>{recoveryNotice.message}</InlineAlert>
                  </div>
                ) : null}
                <form className="space-y-4" onSubmit={handleSubmit}>
                <div className="space-y-2">
                  <label className="text-sm font-medium" htmlFor="username">
                    {t("login.username")}
                  </label>
                  <Input
                    id="username"
                    value={username}
                    onChange={(event) => setUsername(event.target.value)}
                    autoComplete="username"
                    placeholder={t("login.usernamePlaceholder")}
                    aria-invalid={Boolean(error)}
                    aria-errormessage={error ? errorId : undefined}
                    required
                  />
                </div>

                <div className="space-y-2">
                  <label className="text-sm font-medium" htmlFor="password">
                    {t("login.password")}
                  </label>
                  <Input
                    id="password"
                    type="password"
                    value={password}
                    onChange={(event) => setPassword(event.target.value)}
                    autoComplete="current-password"
                    placeholder={t("login.passwordPlaceholder")}
                    aria-invalid={Boolean(error)}
                    aria-errormessage={error ? errorId : undefined}
                    required
                  />
                </div>

                {captchaQuestion ? (
                  <div className="space-y-2">
                    <label className="text-sm font-medium" htmlFor="captcha-answer">
                      {t("login.captcha")}
                    </label>
                    <p className="text-sm text-muted-foreground">{captchaQuestion}</p>
                    <Input
                      id="captcha-answer"
                      type="text"
                      inputMode="numeric"
                      value={captchaAnswer}
                      onChange={(event) => setCaptchaAnswer(event.target.value)}
                      placeholder={t("login.captchaPlaceholder")}
                      autoComplete="off"
                      required
                    />
                  </div>
                ) : null}

                {secondCaptchaQuestion ? (
                  <div className="space-y-2">
                    <label className="text-sm font-medium" htmlFor="second-captcha-answer">
                      {t("login.secondCaptcha")}
                    </label>
                    <p className="text-sm text-muted-foreground">{secondCaptchaQuestion}</p>
                    <Input
                      id="second-captcha-answer"
                      type="text"
                      inputMode="numeric"
                      value={secondCaptchaAnswer}
                      onChange={(event) => setSecondCaptchaAnswer(event.target.value)}
                      placeholder={t("login.captchaPlaceholder")}
                      autoComplete="off"
                      required
                    />
                  </div>
                ) : null}

                {error ? (
                  <p
                    id={errorId}
                    role="alert"
                    className="rounded-xl border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive"
                  >
                    {error}
                  </p>
                ) : null}

                <Button className="w-full" type="submit" loading={submitting}>
                  {t("login.loginButton")}
                </Button>
                </form>
              </div>
            )}
          </CardContent>
        </Card>
      </div>
    </div>
  );
}
