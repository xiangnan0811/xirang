import { useEffect, useState } from "react";
import { Navigate, useLocation, useNavigate } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { ShieldCheck } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { useAuth } from "@/context/auth-context";
import { ApiError, apiClient } from "@/lib/api/client";
import { normalizeRedirectTarget } from "@/lib/api/core";

type LocationState = {
  from?: string;
};

export function LoginPage() {
  const { t } = useTranslation();
  const location = useLocation();
  const navigate = useNavigate();
  const { isAuthenticated, login } = useAuth();

  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // 验证码状态
  const [captchaId, setCaptchaId] = useState<string | null>(null);
  const [captchaQuestion, setCaptchaQuestion] = useState<string | null>(null);
  const [captchaAnswer, setCaptchaAnswer] = useState("");

  // 2FA 步骤状态
  const [requires2FA, setRequires2FA] = useState(false);
  const [loginToken, setLoginToken] = useState("");
  const [totpCode, setTotpCode] = useState("");

  const errorId = "login-form-error";

  const fetchCaptcha = async () => {
    try {
      const data = await apiClient.getCaptcha();
      setCaptchaId(data.id);
      setCaptchaQuestion(data.question);
      setCaptchaAnswer("");
    } catch {
      // 验证码接口不可用（未启用），隐藏验证码区域
      setCaptchaId(null);
      setCaptchaQuestion(null);
    }
  };

  useEffect(() => {
    void fetchCaptcha();
  }, []);

  const queryRedirect = new URLSearchParams(location.search).get("redirect");
  const redirectTo = normalizeRedirectTarget((location.state as LocationState | null)?.from ?? queryRedirect);

  if (isAuthenticated) {
    return <Navigate to={redirectTo} replace />;
  }

  const handleSubmit = async (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    setSubmitting(true);
    setError(null);

    try {
      const result = await apiClient.login(
        username,
        password,
        captchaId ?? undefined,
        captchaQuestion ? captchaAnswer : undefined
      );

      if (result.requires_2fa && result.login_token) {
        setLoginToken(result.login_token);
        setRequires2FA(true);
        return;
      }

      if (result.token && result.user) {
        login(result.token, result.user.username, result.user.role as "admin" | "operator" | "viewer", result.user.id, result.user.totp_enabled ?? false);
        navigate(redirectTo, { replace: true });
      }
      return;
    } catch (error) {
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

        setError(payload?.error ?? payload?.message ?? t("login.errorLoginFailed", { status: error.status }));
        return;
      }
      setError(t("login.errorNetworkFailed"));
    } finally {
      setSubmitting(false);
    }
  };

  const handle2FASubmit = async (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    setSubmitting(true);
    setError(null);

    try {
      const result = await apiClient.totpLogin(loginToken, totpCode);
      login(result.token, result.user.username, result.user.role as "admin" | "operator" | "viewer", result.user.id, result.user.totp_enabled);
      navigate(redirectTo, { replace: true });
    } catch (error) {
      if (error instanceof ApiError) {
        const payload =
          error.detail && typeof error.detail === "object"
            ? (error.detail as { error?: string; message?: string })
            : undefined;
        setError(payload?.error ?? t("login.errorVerifyFailed", { status: error.status }));
        return;
      }
      setError(t("login.errorVerifyNetworkFailed"));
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <div className="relative flex min-h-screen items-center justify-center overflow-hidden px-4 py-8 animate-fade-in">
      <div aria-hidden className="bg-login-ambient" />

      <div className="relative z-10 grid w-full max-w-5xl gap-4 md:max-w-3xl lg:max-w-5xl lg:grid-cols-[1.1fr_0.9fr]">
        <section className="hidden rounded-lg border border-border bg-card shadow-md p-8 md:flex md:flex-col md:justify-center lg:p-12">
          <div className="mb-6 inline-flex items-center gap-2 rounded-full border border-border bg-background px-4 py-1.5 text-xs font-medium text-muted-foreground shadow-sm animate-slide-up [animation-delay:100ms]">
            <img src="/xirang-mark.svg" alt="XiRang" className="size-4.5 rounded-xs border border-border bg-background" />
            <span>XiRang / X-Soil</span>
          </div>
          <h1 className="text-4xl font-extrabold tracking-tight lg:text-5xl animate-slide-up [animation-delay:150ms]">
            <span>{t("login.platformName")}</span>
          </h1>
          <p className="mt-4 max-w-lg text-base leading-relaxed text-muted-foreground animate-slide-up [animation-delay:200ms]">
            {t("login.platformSlogan")}
          </p>

          <div className="mt-8 grid gap-4 sm:grid-cols-2 animate-slide-up [animation-delay:250ms]">
            <div className="rounded-lg border border-border bg-card shadow-sm hover:bg-accent transition-colors p-4">
              <div className="mb-2 inline-flex h-8 w-8 items-center justify-center rounded-xl bg-primary/10 text-primary">
                <ShieldCheck className="size-4" />
              </div>
              <p className="text-sm font-semibold">{t("login.featureMonitor")}</p>
              <p className="mt-1 text-xs text-muted-foreground">{t("login.featureMonitorDesc")}</p>
            </div>
            <div className="rounded-lg border border-border bg-card shadow-sm hover:bg-accent transition-colors p-4">
              <div className="mb-2 inline-flex h-8 w-8 items-center justify-center rounded-xl bg-primary/10 text-primary">
                <ShieldCheck className="size-4" />
              </div>
              <p className="text-sm font-semibold">{t("login.featureOrchestrate")}</p>
              <p className="mt-1 text-xs text-muted-foreground">{t("login.featureOrchestrateDesc")}</p>
            </div>
            <div className="rounded-lg border border-border bg-card shadow-sm hover:bg-accent transition-colors p-4 sm:col-span-2">
              <div className="mb-2 inline-flex h-8 w-8 items-center justify-center rounded-xl bg-primary/10 text-primary">
                <ShieldCheck className="size-4" />
              </div>
              <p className="text-sm font-semibold">{t("login.featureCompliance")}</p>
              <p className="mt-1 text-xs text-muted-foreground">{t("login.featureComplianceDesc")}</p>
            </div>
          </div>
        </section>

        <Card className="flex flex-col justify-center rounded-lg border border-border bg-card shadow-md animate-slide-up [animation-delay:200ms]">
          <CardHeader className="space-y-3 pb-6 animate-slide-up [animation-delay:250ms]">
            <div className="mx-auto mb-2 flex h-12 w-12 items-center justify-center rounded-full bg-primary/10 text-primary md:hidden">
              <ShieldCheck className="size-6" />
            </div>
            <h1 className="text-center text-3xl font-bold tracking-tight md:hidden">{t("login.consoleName")}</h1>
            {requires2FA ? (
              <>
                <CardTitle className="text-2xl font-bold">{t("login.twoFactorTitle")}</CardTitle>
                <CardDescription className="text-base">{t("login.twoFactorDesc")}</CardDescription>
              </>
            ) : (
              <>
                <CardTitle className="text-2xl font-bold">{t("login.welcomeTitle")}</CardTitle>
                <CardDescription className="text-base">{t("login.welcomeDesc")}</CardDescription>
              </>
            )}
          </CardHeader>
          <CardContent className="animate-slide-up [animation-delay:300ms]">
            {requires2FA ? (
              <form className="space-y-4" onSubmit={handle2FASubmit}>
                <div className="space-y-2">
                  <label className="text-sm font-medium" htmlFor="totp-code">
                    {t("login.totpLabel")}
                  </label>
                  <Input
                    id="totp-code"
                    value={totpCode}
                    onChange={(event) => setTotpCode(event.target.value)}
                    autoComplete="one-time-code"
                    placeholder={t("login.totpPlaceholder")}
                    aria-invalid={Boolean(error)}
                    aria-describedby={error ? errorId : undefined}
                    autoFocus
                    required
                  />
                </div>

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
                  onClick={() => { setRequires2FA(false); setError(null); setTotpCode(""); }}
                >
                  {t("login.backToLogin")}
                </Button>
              </form>
            ) : (
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
                    aria-describedby={error ? errorId : undefined}
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
                    aria-describedby={error ? errorId : undefined}
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
            )}
          </CardContent>
        </Card>
      </div>
    </div>
  );
}
