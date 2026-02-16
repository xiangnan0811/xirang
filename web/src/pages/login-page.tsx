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
    <div className="flex min-h-screen items-center justify-center bg-muted/40 px-4">
      <div className="w-full max-w-md">
        <div className="mb-6 flex items-center justify-center gap-2">
          <img src="/xirang-mark.svg" alt="XiRang" className="size-8" />
          <span className="text-xl font-semibold">XiRang</span>
        </div>
        <Card>
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
                required
              />
            </div>

            {error ? <p className="text-sm text-red-600">{error}</p> : null}

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
