import { useState } from "react";
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

  if (isAuthenticated) {
    return <Navigate to="/app/overview" replace />;
  }

  const redirectTo = (location.state as LocationState | null)?.from ?? "/app/overview";

  const handleSubmit = async (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    setSubmitting(true);
    setError(null);

    try {
      const result = await apiClient.login(username, password);
      login(result.token, result.user.username);
      navigate(redirectTo, { replace: true });
      return;
    } catch (error) {
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

  return (
    <div className="relative flex min-h-screen items-center justify-center overflow-hidden px-4 py-8">
      <div className="pointer-events-none absolute inset-0 bg-[radial-gradient(circle_at_20%_0%,rgba(180,137,92,0.2),transparent_35%),radial-gradient(circle_at_90%_10%,rgba(34,197,94,0.2),transparent_35%),linear-gradient(180deg,rgba(15,23,42,0.08),rgba(15,23,42,0.35))] dark:bg-[radial-gradient(circle_at_20%_0%,rgba(180,137,92,0.24),transparent_35%),radial-gradient(circle_at_90%_10%,rgba(34,197,94,0.24),transparent_35%),linear-gradient(180deg,rgba(2,6,23,0.72),rgba(2,6,23,0.92))]" />

      <div className="relative z-10 grid w-full max-w-5xl gap-4 lg:grid-cols-[1.1fr_0.9fr]">
        <section className="hidden rounded-2xl border border-border/70 bg-background/60 p-6 shadow-panel backdrop-blur-xl lg:block">
          <div className="inline-flex items-center gap-2 rounded-full border border-border/75 bg-background/70 px-3 py-1 text-xs text-muted-foreground">
            <img src="/xirang-mark.svg" alt="XiRang" className="size-4 rounded-sm border border-border/70" />
            XiRang / X-Soil
          </div>
          <h1 className="mt-4 text-3xl font-semibold leading-tight tracking-tight">息壤集中备份管理平台</h1>
          <p className="mt-2 text-sm text-muted-foreground">
            如神话中的“息壤”般持续生长，面对不断变化的数据洪流，提供可追踪、可恢复、可审计的增量备份能力。
          </p>

          <div className="mt-5 grid gap-3 text-sm">
            <div className="rounded-xl border border-border/75 bg-background/65 p-3">
              <p className="text-xs text-muted-foreground">实时监控</p>
              <p className="mt-1 font-medium">节点状态矩阵 + 告警分流 + 流式日志</p>
            </div>
            <div className="rounded-xl border border-border/75 bg-background/65 p-3">
              <p className="text-xs text-muted-foreground">统一编排</p>
              <p className="mt-1 font-medium">策略、任务、通知与 SSH Key 一体化管理</p>
            </div>
            <div className="rounded-xl border border-border/75 bg-background/65 p-3">
              <p className="text-xs text-muted-foreground">安全合规</p>
              <p className="mt-1 font-medium">权限控制 + 审计追踪 + 失败快速闭环</p>
            </div>
          </div>
        </section>

        <Card className="border-border/75 bg-background/75 backdrop-blur-xl">
          <CardHeader>
            <div className="mb-2 flex items-center gap-2 text-primary">
              <ShieldCheck className="size-5" />
              <span className="text-sm font-medium">XiRang 备份控制台</span>
            </div>
            <CardTitle>登录</CardTitle>
            <CardDescription>输入管理员账号，进入节点与任务统一管理。</CardDescription>
          </CardHeader>
          <CardContent>
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
                  required
                />
              </div>

              {error ? <p role="alert" className="rounded-lg border border-red-500/30 bg-red-500/10 px-3 py-2 text-sm text-red-600 dark:text-red-300">{error}</p> : null}

              <Button className="w-full" type="submit" loading={submitting}>
                登录控制台
              </Button>
            </form>
          </CardContent>
        </Card>
      </div>
    </div>
  );
}
