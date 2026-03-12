import { useEffect, useState } from "react";
import { Navigate, useLocation, useNavigate } from "react-router-dom";
import { ShieldCheck } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { useAuth } from "@/context/auth-context";
import { ApiError, apiClient } from "@/lib/api/client";

type LocationState = {
  from?: string;
};

export function LoginPage() {
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

  if (isAuthenticated) {
    return <Navigate to="/app/overview" replace />;
  }

  const redirectTo = (location.state as LocationState | null)?.from ?? "/app/overview";

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
          setError(payload?.error ?? "用户名或密码错误。");
          return;
        }
        if (error.status === 403) {
          setError("当前账号无权访问该系统。");
          return;
        }
        if (error.status === 404) {
          setError("登录接口不存在：请检查前端代理或 VITE_API_BASE_URL 配置。");
          return;
        }

        setError(payload?.error ?? payload?.message ?? `登录失败（HTTP ${error.status}）`);
        return;
      }
      setError("登录失败：后端服务不可达，请检查服务状态与网络连接。");
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
        setError(payload?.error ?? `验证失败（HTTP ${error.status}）`);
        return;
      }
      setError("验证失败：后端服务不可达。");
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <div className="relative flex min-h-screen items-center justify-center overflow-hidden px-4 py-8 animate-fade-in">
      <div className="pointer-events-none absolute inset-0 bg-[radial-gradient(circle_at_18%_0%,rgba(180,137,92,0.18),transparent_35%),radial-gradient(circle_at_88%_6%,rgba(34,197,94,0.16),transparent_38%),linear-gradient(180deg,rgba(15,23,42,0.04),rgba(15,23,42,0.24))] dark:bg-[radial-gradient(circle_at_20%_0%,rgba(180,137,92,0.2),transparent_35%),radial-gradient(circle_at_88%_6%,rgba(34,197,94,0.2),transparent_38%),linear-gradient(180deg,rgba(2,6,23,0.68),rgba(2,6,23,0.9))]" />

      <div className="relative z-10 grid w-full max-w-5xl gap-4 md:max-w-3xl lg:max-w-5xl lg:grid-cols-[1.1fr_0.9fr]">
        <section className="hidden glass-panel-heavy p-8 md:flex md:flex-col md:justify-center lg:p-12">
          <div className="mb-6 inline-flex items-center gap-2 rounded-full border border-border/75 bg-background/70 px-4 py-1.5 text-xs font-medium text-muted-foreground shadow-sm animate-slide-up [animation-delay:100ms] border-border/75">
            <img src="/xirang-mark.svg" alt="XiRang" className="size-4.5 rounded-[4px] border border-border/70 bg-background" />
            <span>XiRang / X-Soil</span>
          </div>
          <h1 className="text-4xl font-extrabold tracking-tight lg:text-5xl animate-slide-up [animation-delay:150ms]">
            <span className="text-gradient">息壤集中备份管理平台</span>
          </h1>
          <p className="mt-4 max-w-lg text-base leading-relaxed text-muted-foreground animate-slide-up [animation-delay:200ms]">
            如神话中的“息壤”般持续生长，面对不断变化的数据洪流，提供可追踪、可恢复、可审计的增量备份能力。
          </p>

          <div className="mt-8 grid gap-4 sm:grid-cols-2 animate-slide-up [animation-delay:250ms]">
            <div className="glass-panel p-4 interactive-surface">
              <div className="mb-2 inline-flex h-8 w-8 items-center justify-center rounded-xl bg-primary/10 text-primary">
                <ShieldCheck className="size-4" />
              </div>
              <p className="text-sm font-semibold">实时监控</p>
              <p className="mt-1 text-xs text-muted-foreground">节点状态矩阵 + 告警分流 + 流式日志</p>
            </div>
            <div className="glass-panel p-4 interactive-surface">
              <div className="mb-2 inline-flex h-8 w-8 items-center justify-center rounded-xl bg-primary/10 text-primary">
                <ShieldCheck className="size-4" />
              </div>
              <p className="text-sm font-semibold">统一编排</p>
              <p className="mt-1 text-xs text-muted-foreground">策略、任务、通知与 SSH Key 管理</p>
            </div>
            <div className="glass-panel p-4 interactive-surface sm:col-span-2">
              <div className="mb-2 inline-flex h-8 w-8 items-center justify-center rounded-xl bg-primary/10 text-primary">
                <ShieldCheck className="size-4" />
              </div>
              <p className="text-sm font-semibold">安全合规</p>
              <p className="mt-1 text-xs text-muted-foreground">权限控制 + 审计追踪 + 失败快速闭环</p>
            </div>
          </div>
        </section>

        <Card className="flex flex-col justify-center glass-panel-heavy border-t-4 border-t-primary/80 animate-slide-up [animation-delay:200ms]">
          <CardHeader className="space-y-3 pb-6 animate-slide-up [animation-delay:250ms]">
            <div className="mx-auto mb-2 flex h-12 w-12 items-center justify-center rounded-full bg-primary/10 text-primary md:hidden">
              <ShieldCheck className="size-6" />
            </div>
            <h1 className="text-center text-3xl font-bold tracking-tight md:hidden">息壤控制台</h1>
            {requires2FA ? (
              <>
                <CardTitle className="text-2xl font-bold">两步验证</CardTitle>
                <CardDescription className="text-base">请输入验证器 App 中的验证码，或使用恢复码登录。</CardDescription>
              </>
            ) : (
              <>
                <CardTitle className="text-2xl font-bold">欢迎登录</CardTitle>
                <CardDescription className="text-base">输入管理员账号，进入节点与任务统一管理。</CardDescription>
              </>
            )}
          </CardHeader>
          <CardContent className="animate-slide-up [animation-delay:300ms]">
            {requires2FA ? (
              <form className="space-y-4" onSubmit={handle2FASubmit}>
                <div className="space-y-2">
                  <label className="text-sm font-medium" htmlFor="totp-code">
                    验证码
                  </label>
                  <Input
                    id="totp-code"
                    value={totpCode}
                    onChange={(event) => setTotpCode(event.target.value)}
                    autoComplete="one-time-code"
                    placeholder="请输入 6 位验证码或恢复码"
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
                  验证
                </Button>
                <Button
                  className="w-full"
                  type="button"
                  variant="ghost"
                  onClick={() => { setRequires2FA(false); setError(null); setTotpCode(""); }}
                >
                  返回登录
                </Button>
              </form>
            ) : (
              <form className="space-y-4" onSubmit={handleSubmit}>
                <div className="space-y-2">
                  <label className="text-sm font-medium" htmlFor="username">
                    用户名
                  </label>
                  <Input
                    id="username"
                    value={username}
                    onChange={(event) => setUsername(event.target.value)}
                    autoComplete="username"
                    placeholder="请输入用户名"
                    aria-invalid={Boolean(error)}
                    aria-describedby={error ? errorId : undefined}
                    required
                  />
                </div>

                <div className="space-y-2">
                  <label className="text-sm font-medium" htmlFor="password">
                    密码
                  </label>
                  <Input
                    id="password"
                    type="password"
                    value={password}
                    onChange={(event) => setPassword(event.target.value)}
                    autoComplete="current-password"
                    placeholder="请输入密码"
                    aria-invalid={Boolean(error)}
                    aria-describedby={error ? errorId : undefined}
                    required
                  />
                </div>

                {captchaQuestion ? (
                  <div className="space-y-2">
                    <label className="text-sm font-medium" htmlFor="captcha-answer">
                      验证码
                    </label>
                    <p className="text-sm text-muted-foreground">{captchaQuestion}</p>
                    <Input
                      id="captcha-answer"
                      type="text"
                      inputMode="numeric"
                      value={captchaAnswer}
                      onChange={(event) => setCaptchaAnswer(event.target.value)}
                      placeholder="请输入计算结果"
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
                  登录控制台
                </Button>
              </form>
            )}
          </CardContent>
        </Card>
      </div>
    </div>
  );
}
